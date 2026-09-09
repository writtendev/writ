package dag_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
)

func TestOrder_RealGitStoreMultiWriter(t *testing.T) {
	dir, _ := initTestRepo(t)

	identAlice := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	identBob := testIdentity("fedcba9876543210", "Bob", "bob@example.test")

	time1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	time2 := time.Date(2026, 1, 1, 10, 5, 0, 0, time.UTC)
	time3 := time.Date(2026, 1, 1, 10, 2, 0, 0, time.UTC) // Bob's clock earlier than Alice's second op
	time4 := time.Date(2026, 1, 1, 10, 10, 0, 0, time.UTC)

	nowAlice := time1
	storeAlice, err := dag.Open(dir, identAlice, withVocabularies(), dag.WithNow(func() time.Time { return nowAlice }))
	if err != nil {
		t.Fatalf("Open storeAlice failed: %v", err)
	}

	nowBob := time3
	storeBob, err := dag.Open(dir, identBob, withVocabularies(), dag.WithNow(func() time.Time { return nowBob }))
	if err != nil {
		t.Fatalf("Open storeBob failed: %v", err)
	}

	ctx := context.Background()

	// 1. Alice creates the widget
	envA1 := codec.Envelope{
		ObjectID:   "w-42",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}
	opA1, err := storeAlice.Append(ctx, envA1, nil)
	if err != nil {
		t.Fatalf("Append opA1 failed: %v", err)
	}

	// 2. Alice updates the widget
	nowAlice = time2
	envA2 := codec.Envelope{
		ObjectID:   "w-42",
		ObjectType: "widget",
		OpType:     "update",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Updated"}`),
	}
	opA2, err := storeAlice.Append(ctx, envA2, nil)
	if err != nil {
		t.Fatalf("Append opA2 failed: %v", err)
	}

	// 3. Bob adds a waypoint on the widget, causally referencing opA1
	nowBob = time3
	envB1 := codec.Envelope{
		ObjectID:   "w-42",
		ObjectType: "waypoint",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"text":"Looks good"}`),
	}
	opB1, err := storeBob.Append(ctx, envB1, []string{opA1.ID})
	if err != nil {
		t.Fatalf("Append opB1 failed: %v", err)
	}

	// 4. Alice closes the widget, causally referencing opB1 and opA2
	nowAlice = time4
	envA3 := codec.Envelope{
		ObjectID:   "w-42",
		ObjectType: "widget",
		OpType:     "set-status",
		OpVersion:  1,
		Body:       json.RawMessage(`{"status":"closed"}`),
	}
	opA3, err := storeAlice.Append(ctx, envA3, []string{opB1.ID})
	if err != nil {
		t.Fatalf("Append opA3 failed: %v", err)
	}

	// Enumerate all ops from repository
	enumResult, err := storeAlice.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}

	ops, ok := enumResult.Ops["w-42"]
	if !ok || len(ops) != 4 {
		t.Fatalf("expected 4 ops for w-42, got %d", len(ops))
	}

	ordered, err := dag.Order(ops)
	if err != nil {
		t.Fatalf("Order failed: %v", err)
	}

	if len(ordered) != 4 {
		t.Fatalf("expected 4 ordered ops, got %d", len(ordered))
	}

	// Verify IDs map to positions
	pos := make(map[string]int, len(ordered))
	for i, op := range ordered {
		pos[op.ID] = i
	}

	// Topological checks:
	// A1 is root ancestor of A2, B1, A3
	if pos[opA1.ID] > pos[opA2.ID] {
		t.Errorf("pos(A1)=%d should be < pos(A2)=%d", pos[opA1.ID], pos[opA2.ID])
	}
	if pos[opA1.ID] > pos[opB1.ID] {
		t.Errorf("pos(A1)=%d should be < pos(B1)=%d", pos[opA1.ID], pos[opB1.ID])
	}
	// A3 causally references B1 and A2 (via chain spine)
	if pos[opB1.ID] > pos[opA3.ID] {
		t.Errorf("pos(B1)=%d should be < pos(A3)=%d", pos[opB1.ID], pos[opA3.ID])
	}
	if pos[opA2.ID] > pos[opA3.ID] {
		t.Errorf("pos(A2)=%d should be < pos(A3)=%d", pos[opA2.ID], pos[opA3.ID])
	}

	// A3 must be the last op
	if ordered[3].ID != opA3.ID {
		t.Errorf("expected A3 to be last op, got %s", ordered[3].ID)
	}

	// Determinism: repeated Order calls return exact same slice
	for round := 0; round < 20; round++ {
		repeatOrdered, err := dag.Order(ops)
		if err != nil {
			t.Fatalf("round %d: Order failed: %v", round, err)
		}
		if !reflect.DeepEqual(repeatOrdered, ordered) {
			t.Fatalf("round %d: non-deterministic order", round)
		}
	}
}
