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
		"testdata/issue-ops/field-rules.json",
		"testdata/label/field-rules.json",
		"testdata/project/field-rules.json",
		"testdata/review-ops/field-rules.json",
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
			name: "valid scalar lww with person value normalization",
			rule: spec.FieldRule{
				OpType:    "resolve",
				OpVersion: 1,
				Field:     "resolved_by",
				Strategy:  "lww",
				Normalize: &spec.NormalizeRule{Value: "person"},
			},
		},
		{
			name: "valid set-observed-remove with person items normalization",
			rule: spec.FieldRule{
				OpType:    "assign",
				OpVersion: 1,
				Field:     "add",
				Strategy:  "set-observed-remove",
				Normalize: &spec.NormalizeRule{Items: "person"},
			},
		},
		{
			name: "valid keyed-lww with person key and value normalization",
			rule: spec.FieldRule{
				OpType:    "approval",
				OpVersion: 1,
				Field:     "subject",
				Strategy:  "keyed-lww",
				Key:       []string{"subject", "revision"},
				Normalize: &spec.NormalizeRule{Value: "person", Key: []string{"subject"}},
			},
		},
		{
			name: "empty normalize object",
			rule: spec.FieldRule{
				OpType:    "create",
				OpVersion: 1,
				Field:     "title",
				Strategy:  "lww",
				Normalize: &spec.NormalizeRule{},
			},
			wantErr: "empty normalize object",
		},
		{
			name: "unknown normalize value algorithm",
			rule: spec.FieldRule{
				OpType:    "create",
				OpVersion: 1,
				Field:     "title",
				Strategy:  "lww",
				Normalize: &spec.NormalizeRule{Value: "unknown"},
			},
			wantErr: "unknown normalize value algorithm",
		},
		{
			name: "unknown normalize items algorithm",
			rule: spec.FieldRule{
				OpType:    "assign",
				OpVersion: 1,
				Field:     "add",
				Strategy:  "set-observed-remove",
				Normalize: &spec.NormalizeRule{Items: "unknown"},
			},
			wantErr: "unknown normalize items algorithm",
		},
		{
			name: "normalize items on non-collection strategy",
			rule: spec.FieldRule{
				OpType:    "create",
				OpVersion: 1,
				Field:     "title",
				Strategy:  "lww",
				Normalize: &spec.NormalizeRule{Items: "person"},
			},
			wantErr: "declares normalize.items on non-collection strategy",
		},
		{
			name: "normalize value on collection strategy",
			rule: spec.FieldRule{
				OpType:    "assign",
				OpVersion: 1,
				Field:     "add",
				Strategy:  "set-observed-remove",
				Normalize: &spec.NormalizeRule{Value: "person"},
			},
			wantErr: "declares normalize.value on collection strategy",
		},
		{
			name: "normalize key on non-keyed-lww strategy",
			rule: spec.FieldRule{
				OpType:    "create",
				OpVersion: 1,
				Field:     "title",
				Strategy:  "lww",
				Normalize: &spec.NormalizeRule{Key: []string{"subject"}},
			},
			wantErr: "declares normalize.key on non-keyed-lww strategy",
		},
		{
			name: "normalized key component not in rule key",
			rule: spec.FieldRule{
				OpType:    "approval",
				OpVersion: 1,
				Field:     "subject",
				Strategy:  "keyed-lww",
				Key:       []string{"subject", "revision"},
				Normalize: &spec.NormalizeRule{Key: []string{"author"}},
			},
			wantErr: "not in rule key",
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
