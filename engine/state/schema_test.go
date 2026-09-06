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

func TestFoldSchemaNonCanonicalOpVersionQuarantined(t *testing.T) {
	// op_version "01" has a leading zero: not the canonical decimal string
	// spec/schemas/schema-ops.schema.json's op_version_string pattern
	// requires. The fold path does not consult that JSON schema (it is
	// producer-side), so a non-conforming peer's ref can still carry it;
	// FoldSchema must quarantine the op rather than fold it in under a key
	// that toInt64 would collapse onto a conforming "1" (WRIT-186 round-1
	// finding 1).
	now := time.Unix(100, 0).UTC()
	op := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "sch-1", ObjectType: "schema", OpType: "define-field", OpVersion: 1,
			Body: mustSchemaBody(t, map[string]any{
				"type": "widget", "op_type": "wop", "op_version": "01",
				"field": "value", "strategy": "set-union",
			}),
		},
		ID:     "op-a",
		Author: codec.Identity{When: now},
	}

	sch, err := state.FoldSchema([]codec.Op{op})
	if err != nil {
		t.Fatalf("FoldSchema failed: %v", err)
	}
	if len(sch.Types) != 0 {
		t.Fatalf("expected no type installed from a non-canonical op_version, got %+v", sch.Types)
	}
	if len(sch.UnknownOps) != 1 || sch.UnknownOps[0].Commit != "op-a" {
		t.Fatalf("expected op-a quarantined as unknown, got %+v", sch.UnknownOps)
	}
}

// TestFoldSchemaDeterministicAcrossManyRuns reproduces the round-1 review's
// own check: two define-field declarations for one (type, op_type) that
// differ only in op_version ("1" vs "01") must never both survive into
// Schema.Types, and folding the same op set many times must always produce
// byte-identical output — Go map iteration order must never leak into the
// result. Distinct from TestFoldSchemaNonCanonicalOpVersionQuarantined,
// this vector also carries several conforming, canonical declarations so
// the sort itself — not just the quarantine — is exercised under repeated
// runs.
func TestFoldSchemaDeterministicAcrossManyRuns(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	mk := func(id, opType, opVersion, field, strategy string) codec.Op {
		return codec.Op{
			Envelope: codec.Envelope{
				ObjectID: "sch-1", ObjectType: "schema", OpType: "define-field", OpVersion: 1,
				Body: mustSchemaBody(t, map[string]any{
					"type": "widget", "op_type": opType, "op_version": opVersion,
					"field": field, "strategy": strategy,
				}),
			},
			ID:     id,
			Author: codec.Identity{When: now},
		}
	}

	ops := []codec.Op{
		mk("op-lww", "wop", "1", "value", "lww"),
		mk("op-non-canonical", "wop", "01", "value", "set-union"),
		mk("op-b", "wop", "1", "alpha", "lww"),
		mk("op-c", "wop", "2", "value", "lww"),
		mk("op-d", "zop", "1", "value", "lww"),
	}

	first, err := state.FoldSchema(ops)
	if err != nil {
		t.Fatalf("FoldSchema failed: %v", err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	distinct := map[string]int{}
	for i := 0; i < 500; i++ {
		got, err := state.FoldSchema(ops)
		if err != nil {
			t.Fatalf("run %d: FoldSchema failed: %v", i, err)
		}
		gotJSON, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("run %d: marshal: %v", i, err)
		}
		distinct[string(gotJSON)]++
	}
	if len(distinct) != 1 {
		t.Fatalf("expected exactly 1 distinct fold output over 500 runs, got %d: %v", len(distinct), distinct)
	}
	if _, ok := distinct[string(firstJSON)]; !ok {
		t.Fatalf("the 500-run outputs disagree with the first run")
	}

	// The non-canonical declaration must never have survived into state:
	// only wop/1/value (lww), wop/1/alpha (lww), wop/2/value (lww), and
	// zop/1/value (lww) do.
	if len(first.Types) != 1 {
		t.Fatalf("expected 1 type, got %+v", first.Types)
	}
	typ := first.Types[0]
	if len(typ.Fields) != 4 {
		t.Fatalf("expected 4 surviving fields, got %+v", typ.Fields)
	}
	for _, f := range typ.Fields {
		if f.OpType == "wop" && f.OpVersion == 1 && f.Name == "value" && f.Strategy != "lww" {
			t.Fatalf("non-canonical op_version %q leaked a set-union rule into state: %+v", "01", f)
		}
	}
	var sawUnknown bool
	for _, uo := range first.UnknownOps {
		if uo.Commit == "op-non-canonical" {
			sawUnknown = true
		}
	}
	if !sawUnknown {
		t.Fatalf("expected op-non-canonical quarantined, got unknown_ops=%+v", first.UnknownOps)
	}
}

func TestSchemaRulesValid(t *testing.T) {
	rules := state.SchemaRules()
	if len(rules) == 0 {
		t.Fatal("SchemaRules() returned no rules")
	}
}
