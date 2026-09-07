package projection_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/spec/fixtures"
)

func TestFixturesIncrementalVsColdAndFoldAgreement(t *testing.T) {
	corpus, err := fixtures.LoadCorpus()
	if err != nil {
		t.Fatalf("fixtures.LoadCorpus: %v", err)
	}

	for _, desc := range corpus {
		desc := desc
		// Select fold, forward-compat, multi-writer, issue, project, cycle, and review-mixed-signals fixtures
		if !strings.HasPrefix(desc.Name, "fold-") &&
			!strings.HasPrefix(desc.Name, "forward-compat-") &&
			!strings.HasPrefix(desc.Name, "multi-writer-") &&
			!strings.HasPrefix(desc.Name, "issue-") &&
			!strings.HasPrefix(desc.Name, "project-") &&
			!strings.HasPrefix(desc.Name, "cycle-") &&
			desc.Name != "review-mixed-signals" {
			continue
		}

		t.Run(desc.Name, func(t *testing.T) {
			repoDir := filepath.Join(t.TempDir(), "repo")
			manifest, err := fixtures.Generate(desc, repoDir)
			if err != nil {
				t.Fatalf("fixtures.Generate %s: %v", desc.Name, err)
			}

			repo, err := git.PlainOpen(repoDir)
			if err != nil {
				t.Fatalf("git.PlainOpen %s: %v", desc.Name, err)
			}

			store, err := dag.OpenRepo(repo, identity.Identity{})
			if err != nil {
				t.Fatalf("dag.OpenRepo %s: %v", desc.Name, err)
			}

			// 1. Cold build
			dbCold, err := projection.Open(":memory:")
			if err != nil {
				t.Fatalf("Open cold db: %v", err)
			}
			defer dbCold.Close()

			statsCold, err := dbCold.Refresh(store, projection.WithSchema(testRules()))
			if err != nil {
				t.Fatalf("dbCold.Refresh: %v", err)
			}
			coldDump, err := dbCold.DumpTables()
			if err != nil {
				t.Fatalf("dbCold.DumpTables: %v", err)
			}

			// 2. Incremental build: step refs commit by commit
			// Record final ref tips
			finalRefs := make(map[string]plumbing.Hash)
			refChains := make(map[string][]plumbing.Hash)

			for _, r := range manifest.Refs {
				refName := plumbing.ReferenceName(r.Name)
				finalHash := plumbing.NewHash(r.Commit)
				finalRefs[r.Name] = finalHash

				// Find ancestry of finalHash in topological / chronological order
				var ancestry []plumbing.Hash
				curr := finalHash
				for !curr.IsZero() {
					ancestry = append([]plumbing.Hash{curr}, ancestry...)
					cObj, err := repo.CommitObject(curr)
					if err != nil || len(cObj.ParentHashes) == 0 {
						break
					}
					curr = cObj.ParentHashes[0]
				}
				refChains[r.Name] = ancestry

				// Reset ref to first commit in ancestry
				if len(ancestry) > 0 {
					_ = repo.Storer.SetReference(plumbing.NewReferenceFromStrings(refName.String(), ancestry[0].String()))
				}
			}

			dbInc, err := projection.Open(":memory:")
			if err != nil {
				t.Fatalf("Open inc db: %v", err)
			}
			defer dbInc.Close()

			// Initial refresh at starting commit of each ref
			_, err = dbInc.Refresh(store, projection.WithSchema(testRules()))
			if err != nil {
				t.Fatalf("dbInc.Refresh initial: %v", err)
			}

			// Step each ref forward one commit at a time with a Refresh between steps
			maxLen := 0
			for _, chain := range refChains {
				if len(chain) > maxLen {
					maxLen = len(chain)
				}
			}

			for step := 1; step < maxLen; step++ {
				movedAny := false
				for refNameStr, chain := range refChains {
					if step < len(chain) {
						refName := plumbing.ReferenceName(refNameStr)
						_ = repo.Storer.SetReference(plumbing.NewReferenceFromStrings(refName.String(), chain[step].String()))
						movedAny = true
					}
				}
				if movedAny {
					_, err := dbInc.Refresh(store, projection.WithSchema(testRules()))
					if err != nil {
						t.Fatalf("dbInc.Refresh step %d: %v", step, err)
					}
				}
			}

			// Ensure all refs are at final tips
			for refNameStr, finalHash := range finalRefs {
				refName := plumbing.ReferenceName(refNameStr)
				_ = repo.Storer.SetReference(plumbing.NewReferenceFromStrings(refName.String(), finalHash.String()))
			}
			_, err = dbInc.Refresh(store, projection.WithSchema(testRules()))
			if err != nil {
				t.Fatalf("dbInc.Refresh final: %v", err)
			}

			incDump, err := dbInc.DumpTables()
			if err != nil {
				t.Fatalf("dbInc.DumpTables: %v", err)
			}

			// 3. Assert incremental == cold
			if !reflect.DeepEqual(incDump, coldDump) {
				t.Fatalf("fixture %s: incremental dump != cold dump:\nincremental: %+v\ncold: %+v",
					desc.Name, incDump, coldDump)
			}

			// 4. Cross-check: Projection == Fold for all reviews, comments, issues, projects, cycles
			enumRes, err := store.Enumerate()
			if err != nil {
				t.Fatalf("store.Enumerate %s: %v", desc.Name, err)
			}

			for objID, ops := range enumRes.Ops {
				if len(ops) == 0 {
					continue
				}

				hasReview := false
				hasComment := false
				hasIssue := false
				hasProject := false
				hasCycle := false

				for _, op := range ops {
					switch op.ObjectType {
					case "review":
						hasReview = true
					case "comment":
						hasComment = true
					case "issue":
						hasIssue = true
					case "project":
						hasProject = true
					case "cycle":
						hasCycle = true
					}
				}

				if hasReview {
					reviewState, err := writ.FoldReview(ops)
					if err != nil {
						t.Fatalf("writ.FoldReview for %s in %s: %v", objID, desc.Name, err)
					}

					reviewsRes, err := dbCold.Reviews(projection.ReviewFilter{})
					if err != nil {
						t.Fatalf("dbCold.Reviews: %v", err)
					}
					var found *projection.ReviewResult
					for _, rr := range reviewsRes {
						if rr.ObjectID == objID {
							found = &rr
							break
						}
					}
					if found == nil {
						t.Fatalf("review %s not found in dbCold.Reviews", objID)
					}
					if !reflect.DeepEqual(found.Review, reviewState) {
						t.Fatalf("review %s in %s differs between fold and projection:\n fold: %+v\n proj: %+v",
							objID, desc.Name, reviewState, found.Review)
					}
				} else if hasComment {
					commentState, err := writ.FoldComment(ops)
					if err != nil {
						t.Fatalf("writ.FoldComment for %s in %s: %v", objID, desc.Name, err)
					}

					commentsRes, err := dbCold.Comments(projection.CommentFilter{IncludeDeleted: true})
					if err != nil {
						t.Fatalf("dbCold.Comments: %v", err)
					}
					var found *projection.CommentResult
					for _, cr := range commentsRes {
						if cr.ObjectID == objID {
							found = &cr
							break
						}
					}
					if found == nil {
						t.Fatalf("comment %s not found in dbCold.Comments", objID)
					}
					commentState.Subject.Raw = nil
					commentState.Subject.Unknown = nil
					if !reflect.DeepEqual(found.Comment, commentState) {
						t.Fatalf("comment %s in %s differs between fold and projection:\n fold: %+v\n proj: %+v",
							objID, desc.Name, commentState, found.Comment)
					}
				} else if hasIssue {
					issueState, err := writ.FoldIssue(ops)
					if err != nil {
						t.Fatalf("writ.FoldIssue for %s in %s: %v", objID, desc.Name, err)
					}

					issuesRes, err := dbCold.Issues(projection.IssueFilter{})
					if err != nil {
						t.Fatalf("dbCold.Issues: %v", err)
					}
					var found *projection.IssueResult
					for _, ir := range issuesRes {
						if ir.ObjectID == objID {
							found = &ir
							break
						}
					}
					if found == nil {
						t.Fatalf("issue %s not found in dbCold.Issues", objID)
					}
					if !reflect.DeepEqual(found.Issue, issueState) {
						t.Fatalf("issue %s in %s differs between fold and projection:\n fold: %+v\n proj: %+v",
							objID, desc.Name, issueState, found.Issue)
					}
				} else if hasProject {
					projState, err := writ.FoldProject(ops)
					if err != nil {
						t.Fatalf("writ.FoldProject for %s in %s: %v", objID, desc.Name, err)
					}
					var title, descText, status, reason string
					err = dbCold.DB().QueryRow("SELECT COALESCE(f_title, ''), COALESCE(f_description, ''), COALESCE(f_status, ''), COALESCE(f_reason, '') FROM o_project WHERE object_id = ?", objID).Scan(&title, &descText, &status, &reason)
					if err != nil {
						t.Fatalf("query project %s: %v", objID, err)
					}
					if title != projState.Title || descText != projState.Description || status != projState.Status || reason != projState.Reason {
						t.Fatalf("project %s in %s differs between fold and projection", objID, desc.Name)
					}
					issRows, err := dbCold.DB().Query("SELECT item FROM o_project__issue WHERE object_id = ? ORDER BY item ASC", objID)
					if err != nil {
						t.Fatalf("query project_issues %s: %v", objID, err)
					}
					var projIssues []string
					for issRows.Next() {
						var iss string
						if err := issRows.Scan(&iss); err != nil {
							issRows.Close()
							t.Fatalf("scan project issue: %v", err)
						}
						projIssues = append(projIssues, iss)
					}
					issRows.Close()
					if !reflect.DeepEqual(projIssues, projState.Issues) && !(len(projIssues) == 0 && len(projState.Issues) == 0) {
						t.Fatalf("project %s issues mismatch: fold=%v proj=%v", objID, projState.Issues, projIssues)
					}
				} else if hasCycle {
					cycleState, err := writ.FoldCycle(ops)
					if err != nil {
						t.Fatalf("writ.FoldCycle for %s in %s: %v", objID, desc.Name, err)
					}
					var title, descText, startsAt, endsAt string
					err = dbCold.DB().QueryRow("SELECT COALESCE(f_title, ''), COALESCE(f_description, ''), COALESCE(f_starts_at, ''), COALESCE(f_ends_at, '') FROM o_cycle WHERE object_id = ?", objID).Scan(&title, &descText, &startsAt, &endsAt)
					if err != nil {
						t.Fatalf("query cycle %s: %v", objID, err)
					}
					if title != cycleState.Title || descText != cycleState.Description || startsAt != cycleState.StartsAt || endsAt != cycleState.EndsAt {
						t.Fatalf("cycle %s in %s differs between fold and projection", objID, desc.Name)
					}
					issRows, err := dbCold.DB().Query("SELECT item FROM o_cycle__issue WHERE object_id = ? ORDER BY item ASC", objID)
					if err != nil {
						t.Fatalf("query cycle_issues %s: %v", objID, err)
					}
					var cycIssues []string
					for issRows.Next() {
						var iss string
						if err := issRows.Scan(&iss); err != nil {
							issRows.Close()
							t.Fatalf("scan cycle issue: %v", err)
						}
						cycIssues = append(cycIssues, iss)
					}
					issRows.Close()
					if !reflect.DeepEqual(cycIssues, cycleState.Issues) && !(len(cycIssues) == 0 && len(cycleState.Issues) == 0) {
						t.Fatalf("cycle %s issues mismatch: fold=%v proj=%v", objID, cycleState.Issues, cycIssues)
					}
				}
			}

			_ = statsCold
		})
	}
}
