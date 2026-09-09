package projection_test

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec/fixtures"
)

// fixtureRules resolves a fixture repo's merge rules from the repo itself:
// every `schema` object folded with writ.FoldSchema, the results handed to
// writ.RulesFromSchemas. Writ hard-codes one object type, so a fixture's rule
// table is data in its own log — the projection is handed exactly what a
// reader of that repo would resolve, not a table this test invents.
func fixtureRules(t *testing.T, store *dag.Store) map[string][]state.Rule {
	t.Helper()

	enumRes, err := store.Enumerate()
	if err != nil {
		t.Fatalf("store.Enumerate: %v", err)
	}

	var objectIDs []string
	for objID := range enumRes.Ops {
		objectIDs = append(objectIDs, objID)
	}
	sort.Strings(objectIDs)

	var schemas []writ.Schema
	for _, objID := range objectIDs {
		var schemaOps []codec.Op
		for _, op := range enumRes.Ops[objID] {
			if op.ObjectType == "schema" {
				schemaOps = append(schemaOps, op)
			}
		}
		if len(schemaOps) == 0 {
			continue
		}
		sch, err := writ.FoldSchema(schemaOps)
		if err != nil {
			t.Fatalf("writ.FoldSchema for object %s: %v", objID, err)
		}
		schemas = append(schemas, sch)
	}

	rules, _ := writ.RulesFromSchemas(schemas)
	return rules
}

func TestFixturesIncrementalVsColdAndFoldAgreement(t *testing.T) {
	corpus, err := fixtures.LoadCorpus()
	if err != nil {
		t.Fatalf("fixtures.LoadCorpus: %v", err)
	}

	for _, desc := range corpus {
		desc := desc
		// The fixture families that still carry ops: fold, forward-compat,
		// multi-writer, and the schema-driven family, whose repos declare
		// their own object types the way every other fixture now does.
		if !strings.HasPrefix(desc.Name, "fold-") &&
			!strings.HasPrefix(desc.Name, "forward-compat-") &&
			!strings.HasPrefix(desc.Name, "multi-writer-") &&
			!strings.HasPrefix(desc.Name, "schema-driven-") {
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

			rules := fixtureRules(t, store)

			// 1. Cold build
			dbCold, err := projection.Open(":memory:")
			if err != nil {
				t.Fatalf("Open cold db: %v", err)
			}
			defer dbCold.Close()

			statsCold, err := dbCold.Refresh(store, projection.WithSchema(rules))
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
			_, err = dbInc.Refresh(store, projection.WithSchema(rules))
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
					_, err := dbInc.Refresh(store, projection.WithSchema(rules))
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
			_, err = dbInc.Refresh(store, projection.WithSchema(rules))
			if err != nil {
				t.Fatalf("dbInc.Refresh final: %v", err)
			}

			incDump, err := dbInc.DumpTables()
			if err != nil {
				t.Fatalf("dbInc.DumpTables: %v", err)
			}

			// 3. Assert incremental == cold. This is entirely type-agnostic:
			// it compares two SQLite materializations of the same op
			// history against each other, not against a typed fold. This
			// test used to also cross-check Projection == Fold for
			// review/comment/issue/project/cycle here, using the typed
			// Fold* functions WRIT-195 deleted along with the per-type
			// engine services. The generic replacement for that
			// cross-check — writeTypeRow's inversion of state.Fold's
			// output compared against state.Fold directly, over lww,
			// set-observed-remove, tombstone, append, and keyed-lww
			// targets, concurrent multi-writer histories, an unknown op
			// type, and a truncated ancestry — is
			// TestProjectionMatchesFoldAcrossStrategies and
			// TestProjectionMatchesFoldOnTruncatedAncestry in
			// fold_agreement_test.go, built over a schema-declared test
			// type rather than against these fixtures' still-builtin
			// (pre-WRIT-194) review/issue/etc. corpus.
			if !reflect.DeepEqual(incDump, coldDump) {
				t.Fatalf("fixture %s: incremental dump != cold dump:\nincremental: %+v\ncold: %+v",
					desc.Name, incDump, coldDump)
			}
			_ = statsCold
		})
	}
}
