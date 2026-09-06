package state_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/state"
)

func mustSchemaBody(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return b
}

func TestFoldSchemaEmpty(t *testing.T) {
	sch, err := state.FoldSchema(nil)
	if err != nil {
		t.Fatalf("FoldSchema(nil) error: %v", err)
	}
	if !reflect.DeepEqual(sch, state.Schema{}) {
		t.Fatalf("expected empty Schema, got %+v", sch)
	}
}

func TestFoldSchemaBootstrapWholeObject(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	opCreate := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "sch-acme",
			ObjectType: "schema",
			OpType:     "create",
			OpVersion:  1,
			Body:       mustSchemaBody(t, map[string]any{"namespace": "acme", "description": "Acme schema"}),
		},
		ID:     "op-create",
		Author: codec.Identity{When: now},
	}
	opDefineType := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "sch-acme",
			ObjectType: "schema",
			OpType:     "define-type",
			OpVersion:  1,
			Body:       mustSchemaBody(t, map[string]any{"type": "standup", "description": "Daily standup"}),
		},
		ID:      "op-define-type",
		Parents: []string{"op-create"},
		Author:  codec.Identity{When: now.Add(time.Minute)},
	}
	opDefineField := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "sch-acme",
			ObjectType: "schema",
			OpType:     "define-field",
			OpVersion:  1,
			Body: mustSchemaBody(t, map[string]any{
				"type": "standup", "op_type": "create", "op_version": "1",
				"field": "summary", "value_type": "string", "strategy": "lww",
			}),
		},
		ID:      "op-define-field",
		Parents: []string{"op-define-type"},
		Author:  codec.Identity{When: now.Add(2 * time.Minute)},
	}
	opDefineOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "sch-acme",
			ObjectType: "schema",
			OpType:     "define-op",
			OpVersion:  1,
			Body: mustSchemaBody(t, map[string]any{
				"type": "standup", "op_type": "create", "op_version": "1",
				"description": "Create a standup",
			}),
		},
		ID:      "op-define-op",
		Parents: []string{"op-define-field"},
		Author:  codec.Identity{When: now.Add(3 * time.Minute)},
	}

	sch, err := state.FoldSchema([]codec.Op{opCreate, opDefineType, opDefineField, opDefineOp})
	if err != nil {
		t.Fatalf("FoldSchema failed: %v", err)
	}

	if sch.ObjectID != "sch-acme" {
		t.Errorf("ObjectID = %q, want sch-acme", sch.ObjectID)
	}
	if sch.Namespace != "acme" {
		t.Errorf("Namespace = %q, want acme", sch.Namespace)
	}
	if sch.Description != "Acme schema" {
		t.Errorf("Description = %q, want %q", sch.Description, "Acme schema")
	}
	if len(sch.UnknownOps) != 0 {
		t.Fatalf("expected no unknown ops, got %+v", sch.UnknownOps)
	}
	if len(sch.Types) != 1 {
		t.Fatalf("expected 1 type, got %d: %+v", len(sch.Types), sch.Types)
	}
	typ := sch.Types[0]
	if typ.Name != "standup" || typ.Description != "Daily standup" || typ.Deprecated {
		t.Errorf("unexpected type: %+v", typ)
	}
	if len(typ.Fields) != 1 {
		t.Fatalf("expected 1 field, got %d: %+v", len(typ.Fields), typ.Fields)
	}
	f := typ.Fields[0]
	if f.Name != "summary" || f.OpType != "create" || f.OpVersion != 1 || f.ValueType != "string" || f.Strategy != "lww" || f.Deprecated {
		t.Errorf("unexpected field: %+v", f)
	}
	if len(typ.Ops) != 1 {
		t.Fatalf("expected 1 op, got %d: %+v", len(typ.Ops), typ.Ops)
	}
	o := typ.Ops[0]
	if o.OpType != "create" || o.OpVersion != 1 || o.Description != "Create a standup" {
		t.Errorf("unexpected op: %+v", o)
	}
}

func TestFoldSchemaDeprecateFieldTombstoneStyle(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	define := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "sch-1", ObjectType: "schema", OpType: "define-field", OpVersion: 1,
			Body: mustSchemaBody(t, map[string]any{
				"type": "t", "op_type": "create", "op_version": "1",
				"field": "f", "strategy": "lww",
			}),
		},
		ID:     "op-a",
		Author: codec.Identity{When: now},
	}
	deprecate := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "sch-1", ObjectType: "schema", OpType: "deprecate-field", OpVersion: 1,
			Body: mustSchemaBody(t, map[string]any{
				"type": "t", "op_type": "create", "op_version": "1",
				"field": "f", "deprecated": true,
			}),
		},
		ID:      "op-b",
		Parents: []string{"op-a"},
		Author:  codec.Identity{When: now.Add(time.Minute)},
	}

	sch, err := state.FoldSchema([]codec.Op{define, deprecate})
	if err != nil {
		t.Fatalf("FoldSchema failed: %v", err)
	}
	if len(sch.Types) != 1 || len(sch.Types[0].Fields) != 1 {
		t.Fatalf("unexpected shape: %+v", sch.Types)
	}
	f := sch.Types[0].Fields[0]
	if !f.Deprecated {
		t.Errorf("expected field to be deprecated, got %+v", f)
	}
	if f.Strategy != "lww" {
		t.Errorf("deprecate-field must not clobber sibling registers; strategy = %q", f.Strategy)
	}
}

func TestFoldSchemaUnknownOpsPreserved(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	create := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "sch-1", ObjectType: "schema", OpType: "create", OpVersion: 1,
			Body: mustSchemaBody(t, map[string]any{"namespace": "acme"}),
		},
		ID:     "op-create",
		Author: codec.Identity{When: now},
	}
	future := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "sch-1", ObjectType: "schema", OpType: "future-op", OpVersion: 2,
			Body: mustSchemaBody(t, map[string]any{"whatever": true}),
		},
		ID:      "op-future",
		Parents: []string{"op-create"},
		Author:  codec.Identity{When: now.Add(time.Minute)},
	}

	sch, err := state.FoldSchema([]codec.Op{create, future})
	if err != nil {
		t.Fatalf("FoldSchema failed: %v", err)
	}
	if sch.Namespace != "acme" {
		t.Errorf("Namespace = %q, want acme", sch.Namespace)
	}
	if len(sch.UnknownOps) != 1 || sch.UnknownOps[0].Commit != "op-future" {
		t.Fatalf("expected op-future quarantined as unknown, got %+v", sch.UnknownOps)
	}
}

func TestFoldSchemaBogusStrategyDoesNotErrorTheFold(t *testing.T) {
	// A define-field carrying strategy:"" folds cleanly into schema state
	// (spec/schema-ops.md §9): the fold path performs no value-type
	// checking, and it is engine/schema.go's resolver — not FoldSchema —
	// that gates this before the rule can reach NewAccumulator.
	now := time.Unix(100, 0).UTC()
	op := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "sch-1", ObjectType: "schema", OpType: "define-field", OpVersion: 1,
			Body: mustSchemaBody(t, map[string]any{
				"type": "t", "op_type": "create", "op_version": "1",
				"field": "f", "strategy": "",
			}),
		},
		ID:     "op-a",
		Author: codec.Identity{When: now},
	}

	sch, err := state.FoldSchema([]codec.Op{op})
	if err != nil {
		t.Fatalf("FoldSchema must not error on a bogus strategy value: %v", err)
	}
	if len(sch.Types) != 1 || len(sch.Types[0].Fields) != 1 {
		t.Fatalf("unexpected shape: %+v", sch.Types)
	}
	if sch.Types[0].Fields[0].Strategy != "" {
		t.Errorf("expected the raw (invalid) strategy value preserved, got %q", sch.Types[0].Fields[0].Strategy)
	}
}

func TestSchemaRulesValid(t *testing.T) {
	rules := state.SchemaRules()
	if len(rules) == 0 {
		t.Fatal("SchemaRules() returned no rules")
	}
}
