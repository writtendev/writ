package fixtures_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec/fixtures"
)

// testProjectionRules is a thin wrapper over state.BuiltinRules(), the
// single shared construction of "the built-in vocabulary's fold rules,
// grouped by object type" every package needing one delegates to, for
// driving projection.DB.Refresh/Rebuild directly in a test that does not go
// through package writ's own Store (which resolves and applies this
// automatically).
func testProjectionRules(t *testing.T) map[string][]state.Rule {
	t.Helper()
	rules, err := state.BuiltinRules()
	if err != nil {
		t.Fatalf("state.BuiltinRules: %v", err)
	}
	return rules
}

// TestIssueFamily registers the issue fixture family and runs all descriptions
// carrying issue collaborative objects through the typed FoldIssue golden test harness.
func TestIssueFamily(t *testing.T) {
	fixtures.Run(t, fixtures.Family{
		Name:      "issue",
		GoldenDir: "testdata/golden/issue",
		Filter: func(desc *fixtures.Description) bool {
			if !strings.HasPrefix(desc.Name, "issue-") {
				return false
			}
			for _, ref := range desc.Refs {
				for _, gen := range ref.History {
					for _, c := range gen.Commits {
						if c.Op != nil && c.Op.ObjectType == "issue" {
							return true
						}
					}
				}
			}
			return false
		},
		Runner: runIssueFixture,
	})
}

type IssueGolden struct {
	Objects []IssueObjectGolden `json:"objects"`
}

type IssueObjectGolden struct {
	ObjectID string     `json:"object_id"`
	Issue    writ.Issue `json:"issue"`
}

func runIssueFixture(t *testing.T, fix *fixtures.Fixture) ([]byte, error) {
	t.Helper()

	store, err := dag.OpenRepo(fix.Repo, identity.Identity{})
	if err != nil {
		return nil, fmt.Errorf("dag.OpenRepo failed: %w", err)
	}

	enumRes, err := store.Enumerate()
	if err != nil {
		return nil, fmt.Errorf("store.Enumerate failed: %w", err)
	}

	var golden IssueGolden

	opsByObject := enumRes.Ops
	if len(opsByObject) == 0 {
		opsByObject = make(map[string][]codec.Op)
		seenCommits := make(map[string]bool)
		cIdx := 0
		for _, ref := range fix.Description.Refs {
			isControl := strings.HasSuffix(ref.Name, "-control")
			for _, gen := range ref.History {
				gs := fix.Manifest.Generations[cIdx]
				cIdx++
				if isControl {
					continue
				}
				for ci := range gen.Commits {
					cState := gs.Commits[ci]
					if seenCommits[cState.SHA] {
						continue
					}
					seenCommits[cState.SHA] = true
					commitObj, err := fix.Repo.CommitObject(plumbing.NewHash(cState.SHA))
					if err != nil {
						return nil, fmt.Errorf("lookup commit %s: %w", cState.SHA, err)
					}
					pureCommit, err := codec.FromGitCommit(fix.Repo.Storer, commitObj)
					if err != nil {
						return nil, fmt.Errorf("from git commit %s: %w", cState.SHA, err)
					}
					op, err := codec.DecodeCommit(pureCommit)
					if err != nil {
						continue
					}
					opsByObject[op.ObjectID] = append(opsByObject[op.ObjectID], op)
				}
			}
		}
	}

	var objectIDs []string
	for objID := range opsByObject {
		objectIDs = append(objectIDs, objID)
	}
	sort.Strings(objectIDs)

	r := rand.New(rand.NewSource(42))

	for _, objID := range objectIDs {
		codecOps := opsByObject[objID]
		var issueOps []codec.Op
		for _, op := range codecOps {
			if op.ObjectType == "issue" {
				issueOps = append(issueOps, op)
			}
		}
		if len(issueOps) == 0 {
			continue
		}

		issueState, err := writ.FoldIssue(issueOps)
		if err != nil {
			return nil, fmt.Errorf("writ.FoldIssue for object %s in %s: %w", objID, fix.Name, err)
		}

		// Assert agreement between Fold(ops, IssueRules()) and FoldIssue(ops) across corpus
		objectState, err := writ.Fold(codecOps, writ.IssueRules())
		if err != nil {
			return nil, fmt.Errorf("writ.Fold for object %s in %s: %w", objID, fix.Name, err)
		}
		assertIssueFoldAgreement(t, issueState, objectState, fix.Name, objID)

		expectedJSON, err := canonicaljson.Marshal(mustJSON(t, issueState))
		if err != nil {
			return nil, fmt.Errorf("canonicalizing issue state for %s: %w", objID, err)
		}

		// Commutativity verification: shuffle input ops 100 times and verify identical output
		for i := 0; i < 100; i++ {
			shuffled := make([]codec.Op, len(issueOps))
			copy(shuffled, issueOps)
			r.Shuffle(len(shuffled), func(i, j int) {
				shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
			})

			shuffledIssue, err := writ.FoldIssue(shuffled)
			if err != nil {
				t.Fatalf("commutativity violation on permutation #%d for object %s in %s: %v", i, objID, fix.Name, err)
			}

			shuffledJSON, err := canonicaljson.Marshal(mustJSON(t, shuffledIssue))
			if err != nil {
				t.Fatalf("canonicalizing shuffled issue state on permutation #%d for %s in %s: %v", i, objID, fix.Name, err)
			}

			if !bytes.Equal(shuffledJSON, expectedJSON) {
				t.Fatalf("commutativity violation on permutation #%d for object %s in fixture %s:\n got:  %s\n want: %s",
					i, objID, fix.Name, string(shuffledJSON), string(expectedJSON))
			}
		}

		golden.Objects = append(golden.Objects, IssueObjectGolden{
			ObjectID: objID,
			Issue:    issueState,
		})
	}

	if len(golden.Objects) == 0 {
		return nil, fmt.Errorf("issue fixture %s yielded zero issue objects", fix.Name)
	}

	b, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal issue golden: %w", err)
	}
	return append(b, '\n'), nil
}

func assertIssueFoldAgreement(t *testing.T, issue writ.Issue, state writ.ObjectState, fixtureName, objectID string) {
	t.Helper()

	if title, ok := state.State["title"].(string); ok {
		if issue.Title != title {
			t.Errorf("[%s/%s] agreement mismatch on title: FoldIssue=%q, Fold=%q", fixtureName, objectID, issue.Title, title)
		}
	} else if issue.Title != "" {
		t.Errorf("[%s/%s] title present in FoldIssue (%q) but not in Fold", fixtureName, objectID, issue.Title)
	}

	if desc, ok := state.State["description"].(string); ok {
		if issue.Description != desc {
			t.Errorf("[%s/%s] agreement mismatch on description: FoldIssue=%q, Fold=%q", fixtureName, objectID, issue.Description, desc)
		}
	} else if issue.Description != "" {
		t.Errorf("[%s/%s] description present in FoldIssue (%q) but not in Fold", fixtureName, objectID, issue.Description)
	}

	if st, ok := state.State["state"].(string); ok {
		if issue.State != st {
			t.Errorf("[%s/%s] agreement mismatch on state: FoldIssue=%q, Fold=%q", fixtureName, objectID, issue.State, st)
		}
	}

	if reason, ok := state.State["reason"].(string); ok {
		if issue.Reason != reason {
			t.Errorf("[%s/%s] agreement mismatch on reason: FoldIssue=%q, Fold=%q", fixtureName, objectID, issue.Reason, reason)
		}
	} else if issue.Reason != "" {
		t.Errorf("[%s/%s] reason present in FoldIssue (%q) but not in Fold", fixtureName, objectID, issue.Reason)
	}

	if p, ok := state.State["priority"]; ok {
		var foldPriority int
		switch v := p.(type) {
		case int:
			foldPriority = v
		case int64:
			foldPriority = int(v)
		case float64:
			foldPriority = int(v)
		}
		if issue.Priority != foldPriority {
			t.Errorf("[%s/%s] agreement mismatch on priority: FoldIssue=%d, Fold=%d", fixtureName, objectID, issue.Priority, foldPriority)
		}
	} else if issue.Priority != 0 {
		t.Errorf("[%s/%s] priority present in FoldIssue (%d) but not in Fold", fixtureName, objectID, issue.Priority)
	}

	if est, ok := state.State["estimate"]; ok {
		var foldEst float64
		switch v := est.(type) {
		case float64:
			foldEst = v
		case int:
			foldEst = float64(v)
		case int64:
			foldEst = float64(v)
		}
		if issue.Estimate == nil || *issue.Estimate != foldEst {
			t.Errorf("[%s/%s] agreement mismatch on estimate: FoldIssue=%v, Fold=%v", fixtureName, objectID, issue.Estimate, foldEst)
		}
	} else if issue.Estimate != nil {
		t.Errorf("[%s/%s] estimate present in FoldIssue (%v) but not in Fold", fixtureName, objectID, *issue.Estimate)
	}

	if pos, ok := state.State["position"].(string); ok {
		if issue.Position != pos {
			t.Errorf("[%s/%s] agreement mismatch on position: FoldIssue=%q, Fold=%q", fixtureName, objectID, issue.Position, pos)
		}
	} else if issue.Position != "" {
		t.Errorf("[%s/%s] position present in FoldIssue (%q) but not in Fold", fixtureName, objectID, issue.Position)
	}

	// Links: keyed-lww on target
	if rawRelations, ok := state.State["relation"].([]any); ok {
		activeRelations := make(map[string]string)
		for _, v := range rawRelations {
			if m, ok := v.(map[string]any); ok {
				var keyList []string
				switch ks := m["key"].(type) {
				case []string:
					keyList = ks
				case []any:
					for _, k := range ks {
						keyList = append(keyList, fmt.Sprint(k))
					}
				}
				if len(keyList) >= 1 {
					target := keyList[0]
					val := fmt.Sprint(m["value"])
					if val != "none" && val != "" {
						activeRelations[target] = val
					}
				}
			}
		}

		if len(issue.Links) != len(activeRelations) {
			t.Errorf("[%s/%s] links count mismatch: FoldIssue=%d, Fold active=%d",
				fixtureName, objectID, len(issue.Links), len(activeRelations))
		}
		for _, l := range issue.Links {
			if expectedRel, exists := activeRelations[l.Target]; !exists || expectedRel != l.Relation {
				t.Errorf("[%s/%s] link %s mismatch: FoldIssue relation=%q, Fold=%q",
					fixtureName, objectID, l.Target, l.Relation, expectedRel)
			}
		}
	}
}

func TestIssueFieldsAndRankConcurrentTiebreak(t *testing.T) {
	descs, err := fixtures.LoadCorpus()
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	var desc *fixtures.Description
	for _, d := range descs {
		if d.Name == "issue-fields-and-rank" {
			desc = d
			break
		}
	}
	if desc == nil {
		t.Fatal("issue-fields-and-rank description not found")
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	manifest, err := fixtures.Generate(desc, repoDir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}

	store, err := dag.OpenRepo(repo, identity.Identity{})
	if err != nil {
		t.Fatalf("dag.OpenRepo: %v", err)
	}

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("projection.Open: %v", err)
	}
	defer db.Close()

	if _, err := db.Refresh(store, projection.WithSchema(testProjectionRules(t))); err != nil {
		t.Fatalf("db.Refresh: %v", err)
	}

	issues, err := db.Issues(projection.IssueFilter{OrderBy: projection.OrderByPositionAsc})
	if err != nil {
		t.Fatalf("db.Issues(OrderByPositionAsc): %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}

	// Both issues share identical position 'V'
	if issues[0].Issue.Position != "V" || issues[1].Issue.Position != "V" {
		t.Fatalf("expected both issues to have position 'V', got %q and %q",
			issues[0].Issue.Position, issues[1].Issue.Position)
	}

	// Find the create op commit SHAs from manifest
	var aliceCreateSHA, bobCreateSHA string
	for _, gen := range manifest.Generations {
		for _, c := range gen.Commits {
			if strings.Contains(c.Message, "create issue/i-fields-alice") {
				aliceCreateSHA = c.SHA
			}
			if strings.Contains(c.Message, "create issue/i-fields-bob") {
				bobCreateSHA = c.SHA
			}
		}
	}
	if aliceCreateSHA == "" || bobCreateSHA == "" {
		t.Fatalf("failed to find create SHAs in manifest: alice=%s bob=%s", aliceCreateSHA, bobCreateSHA)
	}

	var expectedFirst, expectedSecond string
	if bobCreateSHA < aliceCreateSHA {
		expectedFirst = "i-fields-bob"
		expectedSecond = "i-fields-alice"
	} else {
		expectedFirst = "i-fields-alice"
		expectedSecond = "i-fields-bob"
	}

	if issues[0].ObjectID != expectedFirst || issues[1].ObjectID != expectedSecond {
		t.Errorf("expected tiebreak order %s < %s, got %s < %s",
			expectedFirst, expectedSecond, issues[0].ObjectID, issues[1].ObjectID)
	}
}
