package schemasrc_test

import (
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/schemasrc"
	"github.com/writtendev/writ/engine/state"
)

// schemaWithField wraps one SchemaField in the minimal Schema/SchemaType
// shape Render needs, mirroring what a foreign, non-conforming writer's
// FoldSchema output could carry: FoldSchema does no catalogue validation
// of its own (spec/value-types.md §Producer-side and reader-tolerant), so
// a value only Render's own JSON-schema-legal-but-grammar-illegal checks
// can catch is otherwise indistinguishable from a normal field.
func schemaWithField(f state.SchemaField) state.Schema {
	return state.Schema{
		ObjectID:  "sch-acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{
				Name:   "widget",
				Ops:    []state.SchemaOp{{OpType: f.OpType, OpVersion: f.OpVersion}},
				Fields: []state.SchemaField{f},
			},
		},
	}
}

// baseField is a field that Render accepts unmodified; each test case
// mutates exactly one part of it to something Parse would reject, or (for
// the accepted case) leaves it as the positive control.
func baseField() state.SchemaField {
	return state.SchemaField{
		Name: "title", OpType: "create", OpVersion: 1,
		ValueType: "string", Strategy: "lww",
	}
}

// TestRenderRejectsTextParseWouldReject pins WRIT-187 round-1 finding 3:
// Render must error, naming the offending field, for every declaration
// this grammar has no spelling for — rather than emitting source Parse
// then rejects with no indication of which declaration broke it. Each
// case below is wire-legal per spec/schemas/schema-ops.schema.json's
// define_field_body (no pattern on enum items, target, or key) but not
// something this grammar's ident production, or one of its two closed
// catalogues, can represent.
func TestRenderRejectsTextParseWouldReject(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(f state.SchemaField) state.SchemaField
		wantErr string
	}{
		{
			name: "unknown strategy",
			mutate: func(f state.SchemaField) state.SchemaField {
				f.Strategy = "blorp"
				return f
			},
			wantErr: `unknown merge strategy "blorp"`,
		},
		{
			name: "unknown value_type",
			mutate: func(f state.SchemaField) state.SchemaField {
				f.ValueType = "currency"
				return f
			},
			wantErr: `unknown value type "currency"`,
		},
		{
			name: "enum member containing a space",
			mutate: func(f state.SchemaField) state.SchemaField {
				f.ValueType = "enum"
				f.Enum = []string{"in progress", "done"}
				return f
			},
			wantErr: `enum member "in progress" would not lex`,
		},
		{
			name: "lattice element containing a space",
			mutate: func(f state.SchemaField) state.SchemaField {
				f.ValueType = "enum"
				f.Enum = []string{"open", "done"}
				f.Strategy = "lattice"
				f.Lattice = []string{"in progress", "done"}
				return f
			},
			wantErr: `lattice element "in progress" would not lex`,
		},
		{
			name: "target containing a space",
			mutate: func(f state.SchemaField) state.SchemaField {
				f.Target = "a v2"
				return f
			},
			wantErr: `target "a v2" would not parse back`,
		},
		{
			name: "target is a reserved word",
			mutate: func(f state.SchemaField) state.SchemaField {
				f.Target = "type"
				return f
			},
			wantErr: `target "type" is a reserved word`,
		},
		{
			name: "field name would not parse back",
			mutate: func(f state.SchemaField) state.SchemaField {
				f.Name = "Title"
				return f
			},
			wantErr: `field name "Title" would not parse back`,
		},
		{
			name: "key column containing a space",
			mutate: func(f state.SchemaField) state.SchemaField {
				f.Strategy = "keyed-lww"
				f.Key = []string{"a col"}
				f.KeyTypes = map[string]string{"a col": "string"}
				return f
			},
			wantErr: `key column "a col" would not lex`,
		},
		{
			name: "key column's value type is off-catalogue",
			mutate: func(f state.SchemaField) state.SchemaField {
				f.Strategy = "keyed-lww"
				f.Key = []string{"subject"}
				f.KeyTypes = map[string]string{"subject": "currency"}
				return f
			},
			wantErr: `key column "subject" declares unknown value type "currency"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := tc.mutate(baseField())
			sch := schemaWithField(mutated)
			out, err := schemasrc.Render(sch)
			if err == nil {
				t.Fatalf("Render: expected an error, got output:\n%s", out)
			}
			if !strings.Contains(err.Error(), "field \""+mutated.Name+"\"") {
				t.Errorf("Render error does not name the offending field %q: %v", mutated.Name, err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Render error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestRenderAcceptsConformingField is the positive control: a field using
// only catalogue members and lexable identifiers renders without error,
// and Parse accepts the result.
func TestRenderAcceptsConformingField(t *testing.T) {
	sch := schemaWithField(baseField())
	out, err := schemasrc.Render(sch)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if _, err := schemasrc.Parse("rendered.schema", out); err != nil {
		t.Fatalf("Parse(Render(...)): %v\n--- rendered ---\n%s", err, out)
	}
}

// TestRenderRejectsUnparseableNames pins WRIT-187 round-2 finding 2:
// namespace, a type name, and an op_type are each written by Render with
// no validation at all, even though every one of them is a bare
// object_type/op_type-shaped wire string (spec/schema-ops.md §4) with no
// pattern of its own on the wire — `object_type: "op"` is fully wire-legal
// (folds straight through FoldSchema) and renders `type op {`, which
// Parse then rejects as a reserved word, with no indication of which
// declaration broke it. Each case here mutates exactly one of the three
// slots to something wire-legal but not a legal source identifier.
func TestRenderRejectsUnparseableNames(t *testing.T) {
	baseSchema := func() state.Schema {
		return state.Schema{
			ObjectID:  "sch-acme",
			Namespace: "acme",
			Types: []state.SchemaType{
				{
					Name: "widget",
					Ops:  []state.SchemaOp{{OpType: "create", OpVersion: 1}},
					Fields: []state.SchemaField{
						{Name: "title", OpType: "create", OpVersion: 1, ValueType: "string", Strategy: "lww"},
					},
				},
			},
		}
	}

	cases := []struct {
		name    string
		mutate  func(s state.Schema) state.Schema
		wantErr string
	}{
		{
			name: "namespace containing a space",
			mutate: func(s state.Schema) state.Schema {
				s.Namespace = "acme corp"
				return s
			},
			wantErr: `namespace "acme corp" would not parse back`,
		},
		{
			name: "empty namespace",
			mutate: func(s state.Schema) state.Schema {
				s.Namespace = ""
				return s
			},
			wantErr: `namespace "" would not parse back`,
		},
		{
			name: "type name containing a space",
			mutate: func(s state.Schema) state.Schema {
				s.Types[0].Name = "wid get"
				return s
			},
			wantErr: `type name "wid get" would not parse back`,
		},
		{
			name: "type name is a reserved word",
			mutate: func(s state.Schema) state.Schema {
				s.Types[0].Name = "op"
				return s
			},
			wantErr: `type name "op" is a reserved word`,
		},
		{
			name: "op_type containing a space",
			mutate: func(s state.Schema) state.Schema {
				s.Types[0].Ops[0].OpType = "cre ate"
				s.Types[0].Fields[0].OpType = "cre ate"
				return s
			},
			wantErr: `op type name "cre ate" would not parse back`,
		},
		{
			name: "op_type is a reserved word",
			mutate: func(s state.Schema) state.Schema {
				s.Types[0].Ops[0].OpType = "description"
				s.Types[0].Fields[0].OpType = "description"
				return s
			},
			wantErr: `op type name "description" is a reserved word`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sch := tc.mutate(baseSchema())
			out, err := schemasrc.Render(sch)
			if err == nil {
				t.Fatalf("Render: expected an error, got output:\n%s", out)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Render error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestRenderAcceptsConformingNames is the positive control for
// TestRenderRejectsUnparseableNames: a schema whose namespace, type name,
// and op_type are all legal source identifiers renders without error, and
// Parse accepts the result back.
func TestRenderAcceptsConformingNames(t *testing.T) {
	sch := state.Schema{
		ObjectID:  "sch-acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{
				Name: "widget",
				Ops:  []state.SchemaOp{{OpType: "create", OpVersion: 1}},
				Fields: []state.SchemaField{
					{Name: "title", OpType: "create", OpVersion: 1, ValueType: "string", Strategy: "lww"},
				},
			},
		},
	}
	out, err := schemasrc.Render(sch)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if _, err := schemasrc.Parse("rendered.schema", out); err != nil {
		t.Fatalf("Parse(Render(...)): %v\n--- rendered ---\n%s", err, out)
	}
}
