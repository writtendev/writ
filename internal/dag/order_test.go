package dag_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/spec"
)

func TestOrder_SpecVectors(t *testing.T) {
	vectors, err := spec.OrderVectors()
	if err != nil {
		t.Fatalf("loading order vectors: %v", err)
	}

	for _, vec := range vectors {
		t.Run(vec.Name, func(t *testing.T) {
			var input []codec.Op
			for _, op := range vec.Ops {
				if op.ObjectID == vec.ObjectID {
					input = append(input, codec.Op{
						ID:      op.ID,
						Parents: op.Parents,
						Author: codec.Identity{
							When: time.Unix(op.Time, 0).UTC(),
						},
						Envelope: codec.Envelope{
							ObjectID: op.ObjectID,
						},
					})
				}
			}

			ordered, err := dag.Order(input)
			if err != nil {
				t.Fatalf("Order failed: %v", err)
			}

			gotIDs := make([]string, len(ordered))
			for i, op := range ordered {
				gotIDs[i] = op.ID
			}

			if !reflect.DeepEqual(gotIDs, vec.ExpectedOrder) {
				t.Errorf("total order mismatch for vector %q:\n got: %v\nwant: %v", vec.Name, gotIDs, vec.ExpectedOrder)
			}
		})
	}
}

func TestOrder_EmptyInput(t *testing.T) {
	orderedNil, err := dag.Order(nil)
	if err != nil {
		t.Fatalf("Order(nil) error: %v", err)
	}
	if orderedNil != nil {
		t.Fatalf("Order(nil) expected nil, got %v", orderedNil)
	}

	orderedEmpty, err := dag.Order([]codec.Op{})
	if err != nil {
		t.Fatalf("Order([]) error: %v", err)
	}
	if orderedEmpty != nil {
		t.Fatalf("Order([]) expected nil, got %v", orderedEmpty)
	}
}

func TestOrder_DuplicateOpID(t *testing.T) {
	ops := []codec.Op{
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

	_, err := dag.Order(ops)
	if err == nil {
		t.Fatal("expected error on duplicate op ID, got nil")
	}
	if !errors.Is(err, dag.ErrDuplicateOpID) {
		t.Fatalf("expected ErrDuplicateOpID, got: %v", err)
	}
}

func TestOrder_MixedObjects(t *testing.T) {
	ops := []codec.Op{
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

	_, err := dag.Order(ops)
	if err == nil {
		t.Fatal("expected error on mixed objects, got nil")
	}
	if !errors.Is(err, dag.ErrMixedObjects) {
		t.Fatalf("expected ErrMixedObjects, got: %v", err)
	}
}

func TestOrder_Cycles(t *testing.T) {
	t.Run("SelfCycle", func(t *testing.T) {
		ops := []codec.Op{
			{
				ID:      "op-1",
				Parents: []string{"op-1"},
				Envelope: codec.Envelope{
					ObjectID: "obj-1",
				},
				Author: codec.Identity{When: time.Unix(100, 0).UTC()},
			},
		}

		_, err := dag.Order(ops)
		if err == nil {
			t.Fatal("expected error on self-cycle, got nil")
		}
		if !errors.Is(err, dag.ErrCycle) {
			t.Fatalf("expected ErrCycle, got: %v", err)
		}
	})

	t.Run("Direct2Cycle", func(t *testing.T) {
		ops := []codec.Op{
			{
				ID:      "op-1",
				Parents: []string{"op-2"},
				Envelope: codec.Envelope{
					ObjectID: "obj-1",
				},
				Author: codec.Identity{When: time.Unix(100, 0).UTC()},
			},
			{
				ID:      "op-2",
				Parents: []string{"op-1"},
				Envelope: codec.Envelope{
					ObjectID: "obj-1",
				},
				Author: codec.Identity{When: time.Unix(100, 0).UTC()},
			},
		}

		_, err := dag.Order(ops)
		if err == nil {
			t.Fatal("expected error on 2-cycle, got nil")
		}
		if !errors.Is(err, dag.ErrCycle) {
			t.Fatalf("expected ErrCycle, got: %v", err)
		}
	})

	t.Run("CycleWithValidPrefix", func(t *testing.T) {
		ops := []codec.Op{
			{
				ID:      "op-root",
				Parents: nil,
				Envelope: codec.Envelope{
					ObjectID: "obj-1",
				},
				Author: codec.Identity{When: time.Unix(100, 0).UTC()},
			},
			{
				ID:      "op-cycle-a",
				Parents: []string{"op-root", "op-cycle-b"},
				Envelope: codec.Envelope{
					ObjectID: "obj-1",
				},
				Author: codec.Identity{When: time.Unix(200, 0).UTC()},
			},
			{
				ID:      "op-cycle-b",
				Parents: []string{"op-cycle-a"},
				Envelope: codec.Envelope{
					ObjectID: "obj-1",
				},
				Author: codec.Identity{When: time.Unix(300, 0).UTC()},
			},
		}

		_, err := dag.Order(ops)
		if err == nil {
			t.Fatal("expected error on cycle with valid prefix, got nil")
		}
		if !errors.Is(err, dag.ErrCycle) {
			t.Fatalf("expected ErrCycle, got: %v", err)
		}
	})
}

func TestOrder_MissingParents(t *testing.T) {
	ops := []codec.Op{
		{
			ID:      "op-1",
			Parents: []string{"missing-parent-1", "missing-parent-2"},
			Envelope: codec.Envelope{
				ObjectID: "obj-1",
			},
			Author: codec.Identity{When: time.Unix(100, 0).UTC()},
		},
		{
			ID:      "op-2",
			Parents: []string{"op-1", "missing-parent-3"},
			Envelope: codec.Envelope{
				ObjectID: "obj-1",
			},
			Author: codec.Identity{When: time.Unix(200, 0).UTC()},
		},
	}

	ordered, err := dag.Order(ops)
	if err != nil {
		t.Fatalf("unexpected error on missing parents: %v", err)
	}

	if len(ordered) != 2 {
		t.Fatalf("expected 2 ops, got %d", len(ordered))
	}
	if ordered[0].ID != "op-1" || ordered[1].ID != "op-2" {
		t.Fatalf("expected [op-1, op-2], got [%s, %s]", ordered[0].ID, ordered[1].ID)
	}
}

func TestOrder_SubsecondTruncation(t *testing.T) {
	// Two concurrent ops with identical integer seconds but different nanoseconds.
	// Both must have t* = 1000s; tiebreak must fall back to op.ID byte order.
	ops := []codec.Op{
		{
			ID: "op-bbb",
			Envelope: codec.Envelope{
				ObjectID: "obj-1",
			},
			Author: codec.Identity{When: time.Unix(1000, 999999999).UTC()},
		},
		{
			ID: "op-aaa",
			Envelope: codec.Envelope{
				ObjectID: "obj-1",
			},
			Author: codec.Identity{When: time.Unix(1000, 1).UTC()},
		},
	}

	ordered, err := dag.Order(ops)
	if err != nil {
		t.Fatalf("Order failed: %v", err)
	}

	if len(ordered) != 2 {
		t.Fatalf("expected 2 ops, got %d", len(ordered))
	}
	if ordered[0].ID != "op-aaa" || ordered[1].ID != "op-bbb" {
		t.Fatalf("expected [op-aaa, op-bbb], got [%s, %s]", ordered[0].ID, ordered[1].ID)
	}
}

func TestOrder_PurityNoInputMutation(t *testing.T) {
	ops := []codec.Op{
		{
			ID:      "op-b",
			Parents: []string{"op-a"},
			Envelope: codec.Envelope{
				ObjectID: "obj-1",
			},
			Author: codec.Identity{When: time.Unix(200, 0).UTC()},
		},
		{
			ID:      "op-a",
			Parents: []string{"foreign-parent"},
			Envelope: codec.Envelope{
				ObjectID: "obj-1",
			},
			Author: codec.Identity{When: time.Unix(100, 0).UTC()},
		},
	}

	// Snapshot input
	origOrder := []string{ops[0].ID, ops[1].ID}
	origParentsA := append([]string(nil), ops[1].Parents...)
	origParentsB := append([]string(nil), ops[0].Parents...)

	ordered1, err := dag.Order(ops)
	if err != nil {
		t.Fatalf("first Order call failed: %v", err)
	}

	// Check input slice untouched
	if ops[0].ID != origOrder[0] || ops[1].ID != origOrder[1] {
		t.Errorf("input slice order mutated: got [%s, %s], want [%s, %s]", ops[0].ID, ops[1].ID, origOrder[0], origOrder[1])
	}
	if !reflect.DeepEqual(ops[1].Parents, origParentsA) {
		t.Errorf("ops[1].Parents mutated: got %v, want %v", ops[1].Parents, origParentsA)
	}
	if !reflect.DeepEqual(ops[0].Parents, origParentsB) {
		t.Errorf("ops[0].Parents mutated: got %v, want %v", ops[0].Parents, origParentsB)
	}

	ordered2, err := dag.Order(ops)
	if err != nil {
		t.Fatalf("second Order call failed: %v", err)
	}

	if !reflect.DeepEqual(ordered1, ordered2) {
		t.Fatalf("consecutive calls returned different results:\ncall 1: %v\ncall 2: %v", ordered1, ordered2)
	}
}
