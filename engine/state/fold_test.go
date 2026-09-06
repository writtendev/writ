package state_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/rand"
	"reflect"
	"testing"
	"time"

	s "github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/spec"
)

func TestOrderVectors(t *testing.T) {
	vectors, err := spec.OrderVectors()
	if err != nil {
		t.Fatalf("loading order vectors: %v", err)
	}
	if len(vectors) == 0 {
		t.Fatal("no order vectors loaded")
	}

	for _, vec := range vectors {
		t.Run(vec.Name, func(t *testing.T) {
			var ops []codec.Op
			for _, o := range vec.Ops {
				if o.ObjectID == vec.ObjectID {
					ops = append(ops, codec.Op{
						ID:      o.ID,
						Parents: o.Parents,
						Author: codec.Identity{
							When: time.Unix(o.Time, 0).UTC(),
						},
						Envelope: codec.Envelope{
							ObjectID:   o.ObjectID,
							ObjectType: "test-object",
							OpType:     "test-op",
							OpVersion:  1,
						},
					})
				}
			}

			// Test through s.Fold
			res, err := s.Fold(ops, []s.Rule{
				{
					OpType:    "test-op",
					OpVersion: 1,
					Field:     "test-field",
					Strategy:  "lww",
				},
			})
			if err != nil {
				t.Fatalf("s.Fold failed: %v", err)
			}

			gotIDs := make([]string, len(res.TotalOrder))
			for i, ref := range res.TotalOrder {
				gotIDs[i] = ref.Commit
			}

			if !reflect.DeepEqual(gotIDs, vec.ExpectedOrder) {
				t.Errorf("total order mismatch for vector %q:\n got: %v\nwant: %v", vec.Name, gotIDs, vec.ExpectedOrder)
			}

			// Verify t* matches spec reference
			specEffectiveTimes := spec.EffectiveTimes(vec.Ops, vec.ObjectID)
			for _, ref := range res.TotalOrder {
				expectedTStar := specEffectiveTimes[ref.Commit]
				if ref.TStar != expectedTStar {
					t.Errorf("t* mismatch for op %s in vector %q: got %d, want %d", ref.Commit, vec.Name, ref.TStar, expectedTStar)
				}
			}

			// Test through delegating dag.Order
			dagOrdered, err := dag.Order(ops)
			if err != nil {
				t.Fatalf("dag.Order failed: %v", err)
			}
			dagIDs := make([]string, len(dagOrdered))
			for i, o := range dagOrdered {
				dagIDs[i] = o.ID
			}
			if !reflect.DeepEqual(dagIDs, vec.ExpectedOrder) {
				t.Errorf("dag.Order mismatch for vector %q:\n got: %v\nwant: %v", vec.Name, dagIDs, vec.ExpectedOrder)
			}

			// Permutation invariance: shuffle input ops 100 times
			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			for i := 0; i < 100; i++ {
				shuffled := make([]codec.Op, len(ops))
				copy(shuffled, ops)
				r.Shuffle(len(shuffled), func(i, j int) {
					shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
				})

				shuffledRes, err := s.Fold(shuffled, []s.Rule{
					{
						OpType:    "test-op",
						OpVersion: 1,
						Field:     "test-field",
						Strategy:  "lww",
					},
				})
				if err != nil {
					t.Fatalf("permutation #%d failed: %v", i, err)
				}

				shuffledIDs := make([]string, len(shuffledRes.TotalOrder))
				for j, ref := range shuffledRes.TotalOrder {
					shuffledIDs[j] = ref.Commit
				}
				if !reflect.DeepEqual(shuffledIDs, vec.ExpectedOrder) {
					t.Fatalf("order changed across permutation #%d in %s:\n got: %v\nwant: %v", i, vec.Name, shuffledIDs, vec.ExpectedOrder)
				}
			}
		})
	}
}

func TestMergeVectors(t *testing.T) {
	vectors, err := spec.MergeVectors()
	if err != nil {
		t.Fatalf("loading merge vectors: %v", err)
	}
	if len(vectors) == 0 {
		t.Fatal("no merge vectors loaded")
	}

	for _, vec := range vectors {
		t.Run(vec.Name, func(t *testing.T) {
			var rules []s.Rule
			for fieldName, cfg := range vec.Fields {
				field := cfg.Field
				if field == "" {
					field = fieldName
				}
				rules = append(rules, s.Rule{
					OpType:    cfg.OpType,
					OpVersion: cfg.OpVersion,
					Field:     field,
					Target:    cfg.Target,
					Strategy:  cfg.Strategy,
					Key:       cfg.Key,
					Lattice:   cfg.Lattice,
				})
			}

			var ops []codec.Op
			for _, o := range vec.Ops {
				bodyBytes, err := json.Marshal(o.Body)
				if err != nil {
					t.Fatalf("marshal op body: %v", err)
				}
				ops = append(ops, codec.Op{
					ID:      o.ID,
					Parents: o.Parents,
					Author: codec.Identity{
						When: time.Unix(o.Time, 0).UTC(),
					},
					Envelope: codec.Envelope{
						ObjectID:   o.ObjectID,
						ObjectType: "test-object",
						OpType:     o.OpType,
						OpVersion:  o.OpVersion,
						Body:       bodyBytes,
					},
				})
			}

			res, err := s.Fold(ops, rules)
			if err != nil {
				t.Fatalf("s.Fold failed: %v", err)
			}

			gotJSON, err := canonicaljson.Marshal(mustJSON(t, res.State))
			if err != nil {
				t.Fatalf("canonicalizing got state: %v", err)
			}

			wantJSON, err := canonicaljson.Marshal(mustJSON(t, vec.ExpectedState))
			if err != nil {
				t.Fatalf("canonicalizing want state: %v", err)
			}

			if !bytes.Equal(gotJSON, wantJSON) {
				t.Errorf("folded state mismatch for vector %q:\n got:  %s\n want: %s", vec.Name, string(gotJSON), string(wantJSON))
			}

			// The quarantine channel is the vector's second assertion, not a
			// detail of the reference implementation: spec/fold.md §7.1 rule 2
			// makes "which ops contributed nothing" part of what a conforming
			// reader produces. spec/fold_test.go cross-checks the two Go folds
			// against each other; this pins s.Fold against the vector
			// directly, so a regression here names the vector that saw it.
			wantUnknown := vec.ExpectedUnknownOps
			if wantUnknown == nil {
				wantUnknown = []string{}
			}
			gotUnknown := make([]string, 0, len(res.UnknownOps))
			for _, u := range res.UnknownOps {
				gotUnknown = append(gotUnknown, u.Commit)
			}
			if !reflect.DeepEqual(gotUnknown, wantUnknown) {
				t.Errorf("unknown ops mismatch for vector %q:\n got:  %v\n want: %v", vec.Name, gotUnknown, wantUnknown)
			}

			// Permutation invariance: shuffle input ops 100 times and verify identical bytes
			r := rand.New(rand.NewSource(time.Now().UnixNano()))
			for i := 0; i < 100; i++ {
				shuffled := make([]codec.Op, len(ops))
				copy(shuffled, ops)
				r.Shuffle(len(shuffled), func(i, j int) {
					shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
				})

				shuffledRes, err := s.Fold(shuffled, rules)
				if err != nil {
					t.Fatalf("permutation #%d failed: %v", i, err)
				}

				shuffledJSON, err := canonicaljson.Marshal(mustJSON(t, shuffledRes.State))
				if err != nil {
					t.Fatalf("canonicalizing shuffled state #%d: %v", i, err)
				}

				if !bytes.Equal(shuffledJSON, gotJSON) {
					t.Fatalf("state changed across permutation #%d in %s:\n got:  %s\n want: %s",
						i, vec.Name, string(shuffledJSON), string(gotJSON))
				}
			}
		})
	}
}

func TestFold_EmptyInput(t *testing.T) {
	resNil, err := s.Fold(nil, nil)
	if err != nil {
		t.Fatalf("Fold(nil) error: %v", err)
	}
	if resNil.State == nil || len(resNil.State) != 0 {
		t.Fatalf("Fold(nil) expected empty state map, got %v", resNil.State)
	}

	resEmpty, err := s.Fold([]codec.Op{}, []s.Rule{})
	if err != nil {
		t.Fatalf("Fold([]) error: %v", err)
	}
	if resEmpty.State == nil || len(resEmpty.State) != 0 {
		t.Fatalf("Fold([]) expected empty state map, got %v", resEmpty.State)
	}
}

func TestFold_ErrorCases(t *testing.T) {
	t.Run("Cycle", func(t *testing.T) {
		cyclicOps := []codec.Op{
			{
				ID:      "op-a",
				Parents: []string{"op-b"},
				Envelope: codec.Envelope{
					ObjectID: "obj-1",
				},
				Author: codec.Identity{When: time.Unix(100, 0).UTC()},
			},
			{
				ID:      "op-b",
				Parents: []string{"op-a"},
				Envelope: codec.Envelope{
					ObjectID: "obj-1",
				},
				Author: codec.Identity{When: time.Unix(200, 0).UTC()},
			},
		}

		_, err := s.Fold(cyclicOps, nil)
		if err == nil {
			t.Fatal("expected ErrCycle on cyclic graph, got nil")
		}
		if !errors.Is(err, s.ErrCycle) {
			t.Fatalf("expected ErrCycle, got: %v", err)
		}
	})

	t.Run("DuplicateOpID", func(t *testing.T) {
		dupOps := []codec.Op{
			{
				ID: "op-1",
				Envelope: codec.Envelope{
					ObjectID: "obj-1",
				},
				Author: codec.Identity{When: time.Unix(100, 0).UTC()},
			},
			{
				ID: "op-1",
				Envelope: codec.Envelope{
					ObjectID: "obj-1",
				},
				Author: codec.Identity{When: time.Unix(200, 0).UTC()},
			},
		}

		_, err := s.Fold(dupOps, nil)
		if err == nil {
			t.Fatal("expected ErrDuplicateOpID on duplicate IDs, got nil")
		}
		if !errors.Is(err, s.ErrDuplicateOpID) {
			t.Fatalf("expected ErrDuplicateOpID, got: %v", err)
		}
	})

	t.Run("MixedObjects", func(t *testing.T) {
		mixedOps := []codec.Op{
			{
				ID: "op-1",
				Envelope: codec.Envelope{
					ObjectID: "obj-1",
				},
				Author: codec.Identity{When: time.Unix(100, 0).UTC()},
			},
			{
				ID: "op-2",
				Envelope: codec.Envelope{
					ObjectID: "obj-2",
				},
				Author: codec.Identity{When: time.Unix(200, 0).UTC()},
			},
		}

		_, err := s.Fold(mixedOps, nil)
		if err == nil {
			t.Fatal("expected ErrMixedObjects on mixed object IDs, got nil")
		}
		if !errors.Is(err, s.ErrMixedObjects) {
			t.Fatalf("expected ErrMixedObjects, got: %v", err)
		}
	})

	t.Run("UnknownStrategy", func(t *testing.T) {
		ops := []codec.Op{
			{
				ID: "op-1",
				Envelope: codec.Envelope{
					ObjectID:  "obj-1",
					OpType:    "custom",
					OpVersion: 1,
					Body:      json.RawMessage(`{"field": "val"}`),
				},
				Author: codec.Identity{When: time.Unix(100, 0).UTC()},
			},
		}
		rules := []s.Rule{
			{
				OpType:    "custom",
				OpVersion: 1,
				Field:     "field",
				Strategy:  "counter", // Not in closed catalogue
			},
		}

		_, err := s.Fold(ops, rules)
		if err == nil {
			t.Fatal("expected error on unknown strategy, got nil")
		}
	})
}

func TestFold_UnknownOpsPreserved(t *testing.T) {
	ops := []codec.Op{
		{
			ID: "op-root",
			Envelope: codec.Envelope{
				ObjectID:  "obj-1",
				OpType:    "create",
				OpVersion: 1,
				Body:      json.RawMessage(`{"title": "Initial Title"}`),
			},
			Author: codec.Identity{When: time.Unix(100, 0).UTC()},
		},
		{
			ID:      "op-unknown-future",
			Parents: []string{"op-root"},
			Envelope: codec.Envelope{
				ObjectID:   "obj-1",
				ObjectType: "custom",
				OpType:     "future-op-type",
				OpVersion:  2,
				Body:       json.RawMessage(`{"future_field": "future_val"}`),
			},
			Author: codec.Identity{When: time.Unix(200, 0).UTC()},
		},
		{
			ID:      "op-update",
			Parents: []string{"op-unknown-future"},
			Envelope: codec.Envelope{
				ObjectID:  "obj-1",
				OpType:    "update",
				OpVersion: 1,
				Body:      json.RawMessage(`{"title": "Updated Title"}`),
			},
			Author: codec.Identity{When: time.Unix(300, 0).UTC()},
		},
	}

	rules := []s.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww"},
		{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww"},
	}

	res, err := s.Fold(ops, rules)
	if err != nil {
		t.Fatalf("s.Fold failed: %v", err)
	}

	// Known fields folded properly
	if res.State["title"] != "Updated Title" {
		t.Errorf("expected title %q, got %v", "Updated Title", res.State["title"])
	}

	// Total order includes all 3 ops
	if len(res.TotalOrder) != 3 {
		t.Fatalf("expected 3 ops in TotalOrder, got %d", len(res.TotalOrder))
	}
	if res.TotalOrder[0].Commit != "op-root" ||
		res.TotalOrder[1].Commit != "op-unknown-future" ||
		res.TotalOrder[2].Commit != "op-update" {
		t.Fatalf("unexpected total order sequence: %v", res.TotalOrder)
	}

	// Unknown op reported in UnknownOps
	if len(res.UnknownOps) != 1 {
		t.Fatalf("expected 1 unknown op, got %d", len(res.UnknownOps))
	}
	u := res.UnknownOps[0]
	if u.Commit != "op-unknown-future" || u.ObjectType != "custom" || u.OpType != "future-op-type" || u.OpVersion != 2 {
		t.Errorf("unexpected unknown op record: %+v", u)
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
