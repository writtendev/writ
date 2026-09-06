package spec_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/writtendev/writ/spec"
)

func TestFieldRulesSchemaValidation(t *testing.T) {
	c := jsonschema.NewCompiler()
	raw, err := spec.FS.ReadFile("schemas/field-rules.schema.json")
	if err != nil {
		t.Fatalf("reading field-rules.schema.json: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("unmarshaling schema: %v", err)
	}
	if err := c.AddResource("https://writ.dev/spec/field-rules.schema.json", doc); err != nil {
		t.Fatalf("adding schema resource: %v", err)
	}
	sch, err := c.Compile("https://writ.dev/spec/field-rules.schema.json")
	if err != nil {
		t.Fatalf("compiling schema: %v", err)
	}

	files := []string{
		"testdata/comments/field-rules.json",
		"testdata/cycle/field-rules.json",
		"testdata/document/field-rules.json",
		"testdata/issue-ops/field-rules.json",
		"testdata/label/field-rules.json",
		"testdata/project/field-rules.json",
		"testdata/review-ops/field-rules.json",
		"testdata/section/field-rules.json",
		"testdata/settings/field-rules.json",
		"testdata/workflow-state/field-rules.json",
	}

	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			rawFile, err := spec.FS.ReadFile(file)
			if err != nil {
				t.Fatalf("reading %s: %v", file, err)
			}
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawFile))
			if err != nil {
				t.Fatalf("unmarshaling %s: %v", file, err)
			}
			if err := sch.Validate(inst); err != nil {
				t.Errorf("schema validation failed for %s: %v", file, err)
			}
		})
	}
}

func TestValidateFieldRule(t *testing.T) {
	tests := []struct {
		name    string
		rule    spec.FieldRule
		wantErr string
	}{
		{
			name: "valid scalar lww with person-ref value type",
			rule: spec.FieldRule{
				OpType:    "resolve",
				OpVersion: 1,
				Field:     "resolved_by",
				Strategy:  "lww",
				ValueType: "person-ref",
			},
		},
		{
			name: "valid set-observed-remove with person-ref item type",
			rule: spec.FieldRule{
				OpType:    "assign",
				OpVersion: 1,
				Field:     "add",
				Strategy:  "set-observed-remove",
				ValueType: "person-ref",
			},
		},
		{
			name: "valid keyed-lww with person-ref key and value type",
			rule: spec.FieldRule{
				OpType:    "approval",
				OpVersion: 1,
				Field:     "subject",
				Strategy:  "keyed-lww",
				Key:       []string{"subject", "revision"},
				ValueType: "person-ref",
				KeyTypes:  map[string]string{"subject": "person-ref", "revision": "git-oid"},
			},
		},
		{
			name: "untyped rule (no value_type) is legal",
			rule: spec.FieldRule{
				OpType:    "create",
				OpVersion: 1,
				Field:     "subject",
				Strategy:  "create-once",
			},
		},
		{
			name: "unknown value_type",
			rule: spec.FieldRule{
				OpType:    "create",
				OpVersion: 1,
				Field:     "title",
				Strategy:  "lww",
				ValueType: "unknown",
			},
			wantErr: "unknown value_type",
		},
		{
			name: "enum value_type with no enum values",
			rule: spec.FieldRule{
				OpType:    "set-status",
				OpVersion: 1,
				Field:     "status",
				Strategy:  "lww",
				ValueType: "enum",
			},
			wantErr: "value_type enum but no enum values",
		},
		{
			name: "enum values on a non-enum value_type",
			rule: spec.FieldRule{
				OpType:    "create",
				OpVersion: 1,
				Field:     "title",
				Strategy:  "lww",
				ValueType: "string",
				Enum:      []string{"a", "b"},
			},
			wantErr: "declares enum values on non-enum value_type",
		},
		{
			name: "max_length on a non-string, non-text value_type",
			rule: spec.FieldRule{
				OpType:    "create",
				OpVersion: 1,
				Field:     "priority",
				Strategy:  "lww",
				ValueType: "int",
				MaxLength: 10,
			},
			wantErr: "declares max_length on value_type",
		},
		{
			name: "tombstone with a value_type other than bool",
			rule: spec.FieldRule{
				OpType:    "delete",
				OpVersion: 1,
				Field:     "deleted",
				Strategy:  "tombstone",
				ValueType: "string",
			},
			wantErr: "uses tombstone with value_type",
		},
		{
			name: "lattice with a value_type other than enum",
			rule: spec.FieldRule{
				OpType:    "ci-status",
				OpVersion: 1,
				Field:     "state",
				Strategy:  "lattice",
				Lattice:   []string{"pending", "success"},
				ValueType: "string",
			},
			wantErr: "uses lattice with value_type",
		},
		{
			name: "lattice elements not a subset of its enum",
			rule: spec.FieldRule{
				OpType:    "ci-status",
				OpVersion: 1,
				Field:     "state",
				Strategy:  "lattice",
				Lattice:   []string{"pending", "success"},
				ValueType: "enum",
				Enum:      []string{"pending", "failure"},
			},
			wantErr: "not a member of its enum",
		},
		{
			name: "keyed-lww key_types missing a key column",
			rule: spec.FieldRule{
				OpType:    "approval",
				OpVersion: 1,
				Field:     "subject",
				Strategy:  "keyed-lww",
				Key:       []string{"subject", "revision"},
				ValueType: "person-ref",
				KeyTypes:  map[string]string{"subject": "person-ref"},
			},
			wantErr: "key_types covering",
		},
		{
			name: "keyed-lww key_types covering a column outside key",
			rule: spec.FieldRule{
				OpType:    "approval",
				OpVersion: 1,
				Field:     "subject",
				Strategy:  "keyed-lww",
				Key:       []string{"subject"},
				ValueType: "person-ref",
				KeyTypes:  map[string]string{"subject": "person-ref", "revision": "git-oid"},
			},
			wantErr: "key_types covering",
		},
		{
			name: "key_types on a non-keyed-lww strategy",
			rule: spec.FieldRule{
				OpType:    "create",
				OpVersion: 1,
				Field:     "title",
				Strategy:  "lww",
				ValueType: "string",
				KeyTypes:  map[string]string{"subject": "person-ref"},
			},
			wantErr: "declares key_types on non-keyed-lww strategy",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := spec.ValidateFieldRule(tc.rule)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
			}
		})
	}
}

func TestFieldRuleTargetKey(t *testing.T) {
	r1 := spec.FieldRule{Field: "description"}
	if got := r1.TargetKey(); got != "description" {
		t.Fatalf("expected Field 'description', got %q", got)
	}
	r2 := spec.FieldRule{Field: "description", Target: "ci_description"}
	if got := r2.TargetKey(); got != "ci_description" {
		t.Fatalf("expected Target 'ci_description', got %q", got)
	}
}
