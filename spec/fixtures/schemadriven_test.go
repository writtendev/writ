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
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/codec/canonicaljson"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/identity"
	"github.com/writtendev/writ/internal/state"
	"github.com/writtendev/writ/spec/fixtures"
)

// TestSchemaDrivenFoldFamily registers the schema-driven fixture family: the
// ticket's central claim made executable. Every schema object in a fixture
// is folded with state.FoldSchema, resolved into per-object_type rules with
// state.RulesFromSchemas, and every other object is folded against the rules
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
// state, unknown_ops -- state.ObjectState's own JSON shape, reused verbatim).
type SchemaDrivenGolden struct {
	Schemas   []SchemaDrivenSchemaGolden   `json:"schemas"`
	Conflicts []SchemaDrivenConflictGolden `json:"conflicts,omitempty"`
	Objects   []state.ObjectState          `json:"objects"`
}

type SchemaDrivenSchemaGolden struct {
	ObjectID string       `json:"object_id"`
	Schema   state.Schema `json:"schema"`
}

// SchemaDrivenConflictGolden is the corpus-pinned projection of
// state.SchemaConflict: kind, object_type, namespace, and object_ids only
// (spec/schema-ops.md §6). Reason is deliberately absent -- it is
// human-readable, free to change wording in any release, and no independent
// implementation can be expected to reproduce writ's exact English or Go's
// %v slice formatting (WRIT-335). The closed Kind catalogue is what the
// corpus byte-compares.
type SchemaDrivenConflictGolden struct {
	Kind       state.SchemaConflictKind `json:"kind"`
	ObjectType string                   `json:"object_type,omitempty"`
	Namespace  string                   `json:"namespace,omitempty"`
	ObjectIDs  []string                 `json:"object_ids"`
}

// schemaConflictKinds is the closed set this family's runner checks every
// golden conflict's Kind against (spec/schema-ops.md §6): a test failure
// here, not a silently-passing golden, is what catches a new
// SchemaConflict construction site added without a Kind.
var schemaConflictKinds = map[state.SchemaConflictKind]bool{
	state.SchemaConflictNamespaceUngrammatical: true,
	state.SchemaConflictObjectIDMismatch:       true,
	state.SchemaConflictSchemaRedefined:        true,
	state.SchemaConflictTypeUngrammatical:      true,
	state.SchemaConflictTypeUnqualified:        true,
	state.SchemaConflictOpTypeUngrammatical:    true,
	state.SchemaConflictOpTypeReserved:         true,
	state.SchemaConflictRuleInvalid:            true,
	state.SchemaConflictKeyColumnDisagreement:  true,
	state.SchemaConflictTargetDisagreement:     true,
	state.SchemaConflictValueTypeUnknown:       true,
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
	var schemas []state.Schema
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
			sch, err := state.FoldSchema(schemaOps)
			if err != nil {
				return nil, fmt.Errorf("state.FoldSchema for object %s in %s: %w", objID, fix.Name, err)
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
				shuffledSchema, err := state.FoldSchema(shuffled)
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

	rules, conflicts := state.RulesFromSchemas(schemas)
	for _, c := range conflicts {
		if c.Kind == "" || !schemaConflictKinds[c.Kind] {
			return nil, fmt.Errorf("schema conflict in %s has Kind %q, want a non-empty member of the closed SchemaConflictKind set (spec/schema-ops.md §6): %+v", fix.Name, c.Kind, c)
		}
		golden.Conflicts = append(golden.Conflicts, SchemaDrivenConflictGolden{
			Kind: c.Kind, ObjectType: c.ObjectType, Namespace: c.Namespace, ObjectIDs: c.ObjectIDs,
		})
	}

	// RulesFromSchemas must resolve to the same rules and conflicts
	// regardless of the order schemas are handed to it (spec/schema-ops.md
	// §7 step 5: "visiting schema objects in ascending object_id order, so
	// two conforming implementations build the same index... regardless of
	// enumeration order").
	if len(schemas) > 1 {
		for i := 0; i < 20; i++ {
			shuffled := make([]state.Schema, len(schemas))
			copy(shuffled, schemas)
			r.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
			shuffledRules, shuffledConflicts := state.RulesFromSchemas(shuffled)
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
		objType := state.DetermineObjectType(ops)

		objState, err := state.Fold(ops, rules[objType])
		if err != nil {
			return nil, fmt.Errorf("state.Fold for object %s (%s) in %s: %w", objID, objType, fix.Name, err)
		}

		// Cross-check: the spec reference reducer must agree with the engine
		// on this object too (WRIT-274). Before this, only fold_test.go's
		// fold-* fixtures ran spec.Fold at all, so a divergence between the
		// engine and the reference reducer that only a schema-driven fixture
		// exercised had no way to surface.
		if _, err := crossCheckSpecFold(t, fix.Name, objID, ops, rules[objType], objState); err != nil {
			return nil, err
		}

		expectedJSON, err := canonicaljson.Marshal(mustJSON(t, objState.State))
		if err != nil {
			return nil, fmt.Errorf("canonicalizing state for %s: %w", objID, err)
		}
		for i := 0; i < 100; i++ {
			shuffled := make([]codec.Op, len(ops))
			copy(shuffled, ops)
			r.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
			shuffledState, err := state.Fold(shuffled, rules[objType])
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
