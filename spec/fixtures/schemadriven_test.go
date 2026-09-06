package fixtures_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/spec/fixtures"
)

// TestSchemaDrivenFoldFamily registers the schema-driven fixture family: the
// ticket's central claim made executable. Every schema object in a fixture
// is folded with writ.FoldSchema, resolved into per-object_type rules with
// writ.RulesFromSchemas, and every other object is folded against the rules
// resolved for its own object_type -- with no new engine code, exactly as
// ARCHITECTURE.md §The six machines describes the schema-driven fold path.
func TestSchemaDrivenFoldFamily(t *testing.T) {
	fixtures.Run(t, fixtures.Family{
		Name:      "schema-driven",
		GoldenDir: "testdata/golden/schema-driven",
		Filter: func(desc *fixtures.Description) bool {
			return strings.HasPrefix(desc.Name, "schema-driven-")
		},
		Runner: runSchemaDrivenFixture,
	})
}

// SchemaDrivenGolden is the family's pinned shape: every schema object
// present, the conflicts RulesFromSchemas resolved between them, and every
// other object's folded ObjectState (object_id, object_type, total_order,
// state, unknown_ops -- writ.ObjectState's own JSON shape, reused verbatim).
type SchemaDrivenGolden struct {
	Schemas   []SchemaDrivenSchemaGolden `json:"schemas"`
	Conflicts []writ.SchemaConflict      `json:"conflicts,omitempty"`
	Objects   []writ.ObjectState         `json:"objects"`
}

type SchemaDrivenSchemaGolden struct {
	ObjectID string      `json:"object_id"`
	Schema   writ.Schema `json:"schema"`
}

func runSchemaDrivenFixture(t *testing.T, fix *fixtures.Fixture) ([]byte, error) {
	t.Helper()

	store, err := dag.OpenRepo(fix.Repo, identity.Identity{})
	if err != nil {
		return nil, fmt.Errorf("dag.OpenRepo failed: %w", err)
	}

	enumRes, err := store.Enumerate()
	if err != nil {
		return nil, fmt.Errorf("store.Enumerate failed: %w", err)
	}

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

	var golden SchemaDrivenGolden
	var schemas []writ.Schema
	nonSchemaOps := make(map[string][]codec.Op)
	var nonSchemaIDs []string

	for _, objID := range objectIDs {
		ops := opsByObject[objID]
		var schemaOps, otherOps []codec.Op
		for _, op := range ops {
			if op.ObjectType == "schema" {
				schemaOps = append(schemaOps, op)
			} else {
				otherOps = append(otherOps, op)
			}
		}

		if len(schemaOps) > 0 {
			sch, err := writ.FoldSchema(schemaOps)
			if err != nil {
				return nil, fmt.Errorf("writ.FoldSchema for object %s in %s: %w", objID, fix.Name, err)
			}
			schemas = append(schemas, sch)
			golden.Schemas = append(golden.Schemas, SchemaDrivenSchemaGolden{ObjectID: objID, Schema: sch})

			// Commutativity: folding a schema object's own ops is already
			// pinned exhaustively by TestSchemaFamily; re-check it lightly
			// here too, since a schema-driven fixture's schema objects are
			// distinct content this family owns.
			expectedJSON, err := canonicaljson.Marshal(mustJSON(t, sch))
			if err != nil {
				return nil, fmt.Errorf("canonicalizing schema state for %s: %w", objID, err)
			}
			for i := 0; i < 20; i++ {
				shuffled := make([]codec.Op, len(schemaOps))
				copy(shuffled, schemaOps)
				r.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
				shuffledSchema, err := writ.FoldSchema(shuffled)
				if err != nil {
					t.Fatalf("commutativity violation on permutation #%d for schema object %s in %s: %v", i, objID, fix.Name, err)
				}
				shuffledJSON, err := canonicaljson.Marshal(mustJSON(t, shuffledSchema))
				if err != nil {
					t.Fatalf("canonicalizing shuffled schema state on permutation #%d for %s in %s: %v", i, objID, fix.Name, err)
				}
				if !bytes.Equal(shuffledJSON, expectedJSON) {
					t.Fatalf("commutativity violation on permutation #%d for schema object %s in fixture %s:\n got:  %s\n want: %s",
						i, objID, fix.Name, string(shuffledJSON), string(expectedJSON))
				}
			}
		}

		if len(otherOps) > 0 {
			nonSchemaOps[objID] = otherOps
			nonSchemaIDs = append(nonSchemaIDs, objID)
		}
	}

	rules, conflicts := writ.RulesFromSchemas(schemas)
	golden.Conflicts = conflicts

	// RulesFromSchemas must resolve to the same rules and conflicts
	// regardless of the order schemas are handed to it (spec/schema-ops.md
	// §7 step 5: "visiting schema objects in ascending object_id order, so
	// two conforming implementations build the same index... regardless of
	// enumeration order").
	if len(schemas) > 1 {
		for i := 0; i < 20; i++ {
			shuffled := make([]writ.Schema, len(schemas))
			copy(shuffled, schemas)
			r.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
			shuffledRules, shuffledConflicts := writ.RulesFromSchemas(shuffled)
			if !reflect.DeepEqual(shuffledRules, rules) {
				t.Fatalf("RulesFromSchemas order-dependence on permutation #%d in %s:\n got:  %+v\nwant: %+v",
					i, fix.Name, shuffledRules, rules)
			}
			if !reflect.DeepEqual(shuffledConflicts, conflicts) {
				t.Fatalf("RulesFromSchemas conflict order-dependence on permutation #%d in %s:\n got:  %+v\nwant: %+v",
					i, fix.Name, shuffledConflicts, conflicts)
			}
		}
	}

	sort.Strings(nonSchemaIDs)
	for _, objID := range nonSchemaIDs {
		ops := nonSchemaOps[objID]
		objType := ops[0].ObjectType

		objState, err := writ.Fold(ops, rules[objType])
		if err != nil {
			return nil, fmt.Errorf("writ.Fold for object %s (%s) in %s: %w", objID, objType, fix.Name, err)
		}

		expectedJSON, err := canonicaljson.Marshal(mustJSON(t, objState.State))
		if err != nil {
			return nil, fmt.Errorf("canonicalizing state for %s: %w", objID, err)
		}
		for i := 0; i < 100; i++ {
			shuffled := make([]codec.Op, len(ops))
			copy(shuffled, ops)
			r.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
			shuffledState, err := writ.Fold(shuffled, rules[objType])
			if err != nil {
				t.Fatalf("commutativity violation on permutation #%d for object %s in %s: %v", i, objID, fix.Name, err)
			}
			shuffledJSON, err := canonicaljson.Marshal(mustJSON(t, shuffledState.State))
			if err != nil {
				t.Fatalf("canonicalizing shuffled state on permutation #%d for %s in %s: %v", i, objID, fix.Name, err)
			}
			if !bytes.Equal(shuffledJSON, expectedJSON) {
				t.Fatalf("commutativity violation on permutation #%d for object %s in fixture %s:\n got:  %s\n want: %s",
					i, objID, fix.Name, string(shuffledJSON), string(expectedJSON))
			}
		}

		golden.Objects = append(golden.Objects, objState)
	}

	if len(golden.Schemas) == 0 && len(golden.Objects) == 0 {
		return nil, fmt.Errorf("schema-driven fixture %s yielded neither schema nor ordinary objects", fix.Name)
	}

	b, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal schema-driven golden: %w", err)
	}
	return append(b, '\n'), nil
}
