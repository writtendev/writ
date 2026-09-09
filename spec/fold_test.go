package spec_test

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"testing"
	"time"

	writ "github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
	"github.com/writtendev/writ/spec"
)

func TestOrderVectorsLoad(t *testing.T) {
	vectors, err := spec.OrderVectors()
	if err != nil {
		t.Fatalf("loading order vectors: %v", err)
	}
	if len(vectors) == 0 {
		t.Fatal("no order vectors loaded")
	}
}

func TestOrderingVectors(t *testing.T) {
	vectors, err := spec.OrderVectors()
	if err != nil {
		t.Fatalf("loading order vectors: %v", err)
	}

	for _, vec := range vectors {
		t.Run(vec.Name, func(t *testing.T) {
			gotOrder, err := spec.TotalOrder(vec.Ops, vec.ObjectID)
			if err != nil {
				t.Fatalf("spec.TotalOrder failed: %v", err)
			}

			if !reflect.DeepEqual(gotOrder, vec.ExpectedOrder) {
				t.Errorf("total order mismatch:\n got: %v\nwant: %v", gotOrder, vec.ExpectedOrder)
			}

			// Independent check: Assert that vec.ExpectedOrder is a valid topological sort of the restricted DAG
			inSet := make(map[string]bool)
			for _, op := range vec.Ops {
				if op.ObjectID == vec.ObjectID {
					inSet[op.ID] = true
				}
			}
			pos := make(map[string]int, len(vec.ExpectedOrder))
			for i, id := range vec.ExpectedOrder {
				pos[id] = i
			}
			for _, op := range vec.Ops {
				if !inSet[op.ID] {
					continue
				}
				for _, p := range op.Parents {
					if inSet[p] {
						if pos[p] >= pos[op.ID] {
							t.Errorf("topological violation: parent %s (pos %d) appears at or after child %s (pos %d)",
								p, pos[p], op.ID, pos[op.ID])
						}
					}
				}
			}

			// Permutation invariance: shuffle input ops 100 times and verify output is identical
			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			for i := 0; i < 100; i++ {
				shuffled := make([]spec.OrderOp, len(vec.Ops))
				copy(shuffled, vec.Ops)
				r.Shuffle(len(shuffled), func(i, j int) {
					shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
				})
				shuffledOrder, err := spec.TotalOrder(shuffled, vec.ObjectID)
				if err != nil {
					t.Fatalf("spec.TotalOrder on shuffled input failed: %v", err)
				}
				if !reflect.DeepEqual(shuffledOrder, vec.ExpectedOrder) {
					t.Fatalf("order changed across permutation #%d:\n got: %v\nwant: %v", i, shuffledOrder, vec.ExpectedOrder)
				}
			}
		})
	}
}

func TestTotalOrderCycleRejection(t *testing.T) {
	cyclicOps := []spec.OrderOp{
		{ID: "op-a", Parents: []string{"op-b"}, Time: 100, ObjectID: "obj-1"},
		{ID: "op-b", Parents: []string{"op-a"}, Time: 200, ObjectID: "obj-1"},
	}

	_, err := spec.TotalOrder(cyclicOps, "obj-1")
	if err == nil {
		t.Fatal("expected cycle error from TotalOrder on cyclic graph, got nil")
	}
}

func TestMergeVectorsLoad(t *testing.T) {
	vectors, err := spec.MergeVectors()
	if err != nil {
		t.Fatalf("loading merge vectors: %v", err)
	}
	if len(vectors) == 0 {
		t.Fatal("no merge vectors loaded")
	}
}

func TestMergeVectors(t *testing.T) {
	vectors, err := spec.MergeVectors()
	if err != nil {
		t.Fatalf("loading merge vectors: %v", err)
	}

	for _, vec := range vectors {
		t.Run(vec.Name, func(t *testing.T) {
			var rules []spec.FieldRule
			for _, fieldName := range sortedFieldNames(vec.Fields) {
				cfg := vec.Fields[fieldName]
				field := cfg.Field
				if field == "" {
					field = fieldName
				}
				rules = append(rules, spec.FieldRule{
					OpType:     cfg.OpType,
					OpVersion:  cfg.OpVersion,
					Field:      field,
					Target:     cfg.Target,
					Strategy:   cfg.Strategy,
					Key:        cfg.Key,
					Lattice:    cfg.Lattice,
					ValueType:  cfg.ValueType,
					Enum:       cfg.Enum,
					MaxLength:  cfg.MaxLength,
					KeyTypes:   cfg.KeyTypes,
					ObjectType: cfg.ObjectType,
				})
			}

			folded, err := spec.Fold(vec.Ops, rules)
			if err != nil {
				t.Fatalf("spec.Fold failed: %v", err)
			}

			gotJSON, err := canonicaljson.Marshal(mustJSON(t, folded.State))
			if err != nil {
				t.Fatalf("canonicalizing got state: %v", err)
			}

			wantJSON, err := canonicaljson.Marshal(mustJSON(t, vec.ExpectedState))
			if err != nil {
				t.Fatalf("canonicalizing want state: %v", err)
			}

			if !bytes.Equal(gotJSON, wantJSON) {
				t.Errorf("folded state mismatch:\n got: %s\nwant: %s", string(gotJSON), string(wantJSON))
			}

			wantUnknown := vec.ExpectedUnknownOps
			if wantUnknown == nil {
				wantUnknown = []string{}
			}
			var gotUnknownIDs []string
			for _, u := range folded.UnknownOps {
				gotUnknownIDs = append(gotUnknownIDs, u.Commit)
			}
			if gotUnknownIDs == nil {
				gotUnknownIDs = []string{}
			}
			if !reflect.DeepEqual(gotUnknownIDs, wantUnknown) {
				t.Errorf("unknown ops mismatch:\n got: %v\nwant: %v", gotUnknownIDs, wantUnknown)
			}

			// The vectors are the spec, and the spec binds every reducer: run
			// the same ops through the engine's generic fold and require the
			// same bytes. Without this the vectors pin only the reference, and
			// a divergence between the two Go implementations — which is how
			// WRIT-124 and WRIT-126 were found — passes.
			assertEngineAgrees(t, vec, gotJSON, folded.UnknownOps)
		})
	}
}

// sortedFieldNames returns fields's keys in a fixed, deterministic order.
// vec.Fields is a Go map, whose iteration order Go deliberately randomizes
// per range statement; a vector whose rules collide on Target (the
// object-type scoping vectors, and the two-fields-one-target vectors,
// spec/fold.md §5) must produce the same accumulator-building order in
// both the reference and engine rule-building loops on every run, not
// whichever order the map hashed to this time. Both loops now apply every
// matching rule rather than stopping at the first (WRIT-201), so this no
// longer picks a *winner* between rules — but for the strategies whose
// result depends on the order matching rules contribute in (`append`,
// and same-operation ties under `lww`/`create-once`/`keyed-lww`,
// spec/fold.md §5), a vector's expected_state still has to be pinned
// against one fixed order rather than an arbitrary one.
func sortedFieldNames(fields map[string]spec.StrategyConfig) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// assertEngineAgrees drives a merge vector through writ.Fold and requires
// byte-identical state and the same quarantined ops as the reference fold.
func assertEngineAgrees(t *testing.T, vec spec.MergeVector, wantStateJSON []byte, wantUnknown []spec.UnknownOp) {
	t.Helper()

	var rules []writ.Rule
	for _, fieldName := range sortedFieldNames(vec.Fields) {
		cfg := vec.Fields[fieldName]
		field := cfg.Field
		if field == "" {
			field = fieldName
		}
		rules = append(rules, writ.Rule{
			OpType:     cfg.OpType,
			OpVersion:  cfg.OpVersion,
			Field:      field,
			Target:     cfg.Target,
			Strategy:   cfg.Strategy,
			Key:        cfg.Key,
			Lattice:    cfg.Lattice,
			ValueType:  cfg.ValueType,
			Enum:       cfg.Enum,
			MaxLength:  cfg.MaxLength,
			KeyTypes:   cfg.KeyTypes,
			ObjectType: cfg.ObjectType,
		})
	}

	var ops []codec.Op
	for _, op := range vec.Ops {
		if op.ObjectID != vec.ObjectID {
			continue
		}
		body, err := json.Marshal(op.Body)
		if err != nil {
			t.Fatalf("marshaling body of op %s: %v", op.ID, err)
		}
		ops = append(ops, codec.Op{
			Envelope: codec.Envelope{
				ObjectID:   op.ObjectID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
				Body:       body,
			},
			ID:      op.ID,
			Parents: op.Parents,
			Author:  codec.Identity{When: time.Unix(op.Time, 0).UTC()},
		})
	}

	res, err := writ.Fold(ops, rules)
	if err != nil {
		t.Fatalf("writ.Fold failed: %v", err)
	}

	gotJSON, err := canonicaljson.Marshal(mustJSON(t, res.State))
	if err != nil {
		t.Fatalf("canonicalizing engine state: %v", err)
	}
	if !bytes.Equal(gotJSON, wantStateJSON) {
		t.Errorf("engine fold state differs from the reference:\n engine: %s\n ref:    %s",
			string(gotJSON), string(wantStateJSON))
	}

	gotUnknown := make([]spec.UnknownOp, len(res.UnknownOps))
	for i, u := range res.UnknownOps {
		gotUnknown[i] = spec.UnknownOp{
			Commit:     u.Commit,
			ObjectType: u.ObjectType,
			OpType:     u.OpType,
			OpVersion:  u.OpVersion,
		}
	}
	if wantUnknown == nil {
		wantUnknown = []spec.UnknownOp{}
	}
	if !reflect.DeepEqual(gotUnknown, wantUnknown) {
		t.Errorf("engine unknown ops differ from the reference:\n engine: %+v\n ref:    %+v",
			gotUnknown, wantUnknown)
	}
}

// TestMergeCoverage guards that every strategy in the closed catalogue has at least one vector.
func TestMergeCoverage(t *testing.T) {
	vectors, err := spec.MergeVectors()
	if err != nil {
		t.Fatalf("loading merge vectors: %v", err)
	}

	covered := make(map[string]bool)
	for _, vec := range vectors {
		for _, cfg := range vec.Fields {
			covered[cfg.Strategy] = true
		}
	}

	for strat := range spec.KnownCatalogueStrategies {
		if !covered[strat] {
			t.Errorf("catalogue strategy %q has no test vector in testdata/fold/merge/", strat)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return b
}

// TestCatalogueCountMatchesProse binds §5's "closed catalogue of N" to the set
// the code actually enforces. The two drifted once already: the prose said 7
// while the table listed 8 and KnownCatalogueStrategies held 8, and it took an
// external reviewer to notice. A closed catalogue whose size is stated wrongly
// is the one kind of error a reader has no way to detect from the document.
func TestCatalogueCountMatchesProse(t *testing.T) {
	raw, err := os.ReadFile("fold.md")
	if err != nil {
		raw, err = os.ReadFile(filepath.Join("spec", "fold.md"))
		if err != nil {
			t.Fatalf("reading fold.md: %v", err)
		}
	}

	m := regexp.MustCompile(`\*\*closed catalogue\*\* of (\d+) per-field merge strategies`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("fold.md no longer states the catalogue size in the form this test reads; " +
			"update the test deliberately rather than dropping the claim")
	}
	stated, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("parsing stated catalogue size %q: %v", m[1], err)
	}
	if stated != len(spec.KnownCatalogueStrategies) {
		t.Errorf("fold.md §5 states a closed catalogue of %d strategies; KnownCatalogueStrategies holds %d",
			stated, len(spec.KnownCatalogueStrategies))
	}
}
