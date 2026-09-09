package spec_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/writtendev/writ/engine/codec/canonicaljson"
	"github.com/writtendev/writ/spec"
)

const (
	envelopeSchemaID  = "https://writ.dev/spec/op-envelope.schema.json"
	schemaOpsSchemaID = "https://writ.dev/spec/schema-ops.schema.json"
)

func compileSchemaOpsSchemas(t *testing.T) (*jsonschema.Schema, *jsonschema.Schema) {
	t.Helper()
	envRaw, err := spec.FS.ReadFile("schemas/op-envelope.schema.json")
	if err != nil {
		t.Fatalf("reading envelope schema: %v", err)
	}
	envDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(envRaw))
	if err != nil {
		t.Fatalf("decoding envelope schema: %v", err)
	}

	schRaw, err := spec.FS.ReadFile("schemas/schema-ops.schema.json")
	if err != nil {
		t.Fatalf("reading schema-ops schema: %v", err)
	}
	schDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schRaw))
	if err != nil {
		t.Fatalf("decoding schema-ops schema: %v", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource(envelopeSchemaID, envDoc); err != nil {
		t.Fatalf("adding envelope schema resource: %v", err)
	}
	if err := c.AddResource(schemaOpsSchemaID, schDoc); err != nil {
		t.Fatalf("adding schema-ops schema resource: %v", err)
	}

	envSch, err := c.Compile(envelopeSchemaID)
	if err != nil {
		t.Fatalf("compiling envelope schema: %v", err)
	}
	schSch, err := c.Compile(schemaOpsSchemaID)
	if err != nil {
		t.Fatalf("compiling schema-ops schema: %v", err)
	}

	return envSch, schSch
}

// schemaOpsInvariants checks a define-field payload's cross-field
// consistency through the same validator the resolver runs before a rule
// can be installed (engine/schema.go's RulesFromSchemas): a define-field op
// carrying strategy:"" or an inconsistent key/key_types/enum/lattice/
// max_length combination folds cleanly per spec/fold.md (the fold path
// performs no value-type checking), so this is the one place the invariant
// is caught for a payload the JSON schema alone cannot express.
func schemaOpsInvariants(payload map[string]any) error {
	objectType, _ := payload["object_type"].(string)
	if objectType != "schema" {
		return nil
	}
	opType, _ := payload["op_type"].(string)
	if opType != "define-field" {
		return nil
	}
	body, _ := payload["body"].(map[string]any)
	if body == nil {
		return nil
	}

	r := spec.FieldRule{
		OpType:    stringOf(body, "op_type"),
		OpVersion: 1, // the outer rule's own op_version >= 1 requirement; unrelated to body["op_version"]'s decimal-string encoding of the vocabulary being declared
		Field:     stringOf(body, "field"),
		Target:    stringOf(body, "target"),
		Strategy:  stringOf(body, "strategy"),
		ValueType: stringOf(body, "value_type"),
		Enum:      stringSliceOf(body, "enum"),
		Key:       stringSliceOf(body, "key"),
		Lattice:   stringSliceOf(body, "lattice"),
	}
	if v, ok := body["max_length"].(float64); ok {
		r.MaxLength = int64(v)
	}
	if kt, ok := body["key_types"].(map[string]any); ok {
		r.KeyTypes = make(map[string]string, len(kt))
		for k, v := range kt {
			if s, ok := v.(string); ok {
				r.KeyTypes[k] = s
			}
		}
	}

	return spec.ValidateFieldRule(r)
}

func stringOf(body map[string]any, key string) string {
	s, _ := body[key].(string)
	return s
}

func stringSliceOf(body map[string]any, key string) []string {
	raw, ok := body[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func validateSchemaOpVector(t *testing.T, envSch, schSch *jsonschema.Schema, raw []byte) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoding vector: %v", err)
	}
	if err := envSch.Validate(inst); err != nil {
		return fmt.Errorf("envelope schema: %w", err)
	}
	if err := schSch.Validate(inst); err != nil {
		return fmt.Errorf("schema-ops schema: %w", err)
	}
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("re-decoding vector: %v", err)
	}
	if err := schemaOpsInvariants(p); err != nil {
		return fmt.Errorf("invariant: %w", err)
	}
	canon, err := canonicaljson.Marshal(raw)
	if err != nil {
		return fmt.Errorf("canonicalization: %w", err)
	}
	if !bytes.Equal(canon, raw) {
		return fmt.Errorf("instance is not byte-canonical:\n  raw: %q\ncanon: %q", raw, canon)
	}
	return nil
}

func TestSchemaOpsSchemaCompiles(t *testing.T) {
	compileSchemaOpsSchemas(t)
}

func TestValidSchemaOpsVectors(t *testing.T) {
	envSch, schSch := compileSchemaOpsSchemas(t)
	for _, name := range readDirNames(t, "testdata/schema-ops/valid") {
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/schema-ops/valid/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateSchemaOpVector(t, envSch, schSch, raw); err != nil {
				t.Errorf("valid vector rejected: %v", err)
			}
		})
	}
}

func TestInvalidSchemaOpsVectors(t *testing.T) {
	_, schSch := compileSchemaOpsSchemas(t)

	rawIndex, err := spec.FS.ReadFile("testdata/schema-ops/invalid/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var index []struct {
		File     string `json:"file"`
		Rejects  string `json:"rejects"`
		Category string `json:"category,omitempty"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatalf("decoding index.json: %v", err)
	}

	indexed := make(map[string]bool)
	for _, entry := range index {
		if indexed[entry.File] {
			t.Errorf("index.json lists %s more than once", entry.File)
		}
		indexed[entry.File] = true
	}

	names := readDirNames(t, "testdata/schema-ops/invalid")
	for _, name := range names {
		if name == "index.json" {
			continue
		}
		if !indexed[name] {
			t.Errorf("instance %s is not listed in index.json", name)
		}
	}

	for _, entry := range index {
		t.Run(entry.File, func(t *testing.T) {
			if entry.Reason == "" {
				t.Error("index entry has no reason")
			}
			raw, err := spec.FS.ReadFile("testdata/schema-ops/invalid/" + entry.File)
			if err != nil {
				t.Fatalf("reading instance: %v", err)
			}
			switch entry.Rejects {
			case "schema":
				inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
				if err != nil {
					t.Fatalf("parsing instance (a schema-rejected instance must be parseable JSON): %v", err)
				}
				if err := schSch.Validate(inst); err == nil {
					t.Errorf("schema accepted the instance; expected rejection: %s", entry.Reason)
				}
			case "invariant":
				inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
				if err != nil {
					t.Fatalf("parsing instance: %v", err)
				}
				if err := schSch.Validate(inst); err != nil {
					t.Errorf("schema rejected an invariant-kind vector (%v); expected only invariant to fail: %s", err, entry.Reason)
				}
				var p map[string]any
				if err := json.Unmarshal(raw, &p); err != nil {
					t.Fatal(err)
				}
				if err := schemaOpsInvariants(p); err == nil {
					t.Errorf("invariant accepted the instance; expected rejection: %s", entry.Reason)
				}
			case "canonicalization":
				switch entry.Category {
				case "not-canonical":
					canon, err := canonicaljson.Marshal(raw)
					if err != nil {
						return
					}
					if bytes.Equal(canon, raw) {
						t.Errorf("instance was already byte-canonical; expected non-canonical form")
					}
				case "duplicate-key":
					if _, err := canonicaljson.Marshal(raw); err == nil {
						t.Errorf("canonicalizer accepted duplicate key; expected rejection: %s", entry.Reason)
					} else if !strings.Contains(err.Error(), "duplicate") {
						t.Errorf("unexpected error for duplicate key vector: %v", err)
					}
				case "lone-surrogate":
					if _, err := canonicaljson.Marshal(raw); err == nil {
						t.Errorf("canonicalizer accepted lone surrogate; expected rejection: %s", entry.Reason)
					} else if !strings.Contains(err.Error(), "surrogate") {
						t.Errorf("unexpected error for lone surrogate vector: %v", err)
					}
				default:
					t.Fatalf("unknown canonicalization category: %s", entry.Category)
				}
			default:
				t.Fatalf("unknown rejects value in index.json: %s", entry.Rejects)
			}
		})
	}
}

// TestSchemaRulesFieldRulesLoad proves the bootstrap table
// (testdata/schema-ops/field-rules.json) loads and validates through
// spec.FieldRules like every other vocabulary's table — a malformed
// bootstrap table fails the whole spec package, not just this test
// (spec.FieldRules is called eagerly by engine/codec's schema validation).
func TestSchemaRulesFieldRulesLoad(t *testing.T) {
	rules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules failed: %v", err)
	}
	var found int
	for _, r := range rules {
		if r.Vocabulary == "schema-ops" {
			found++
		}
	}
	if found == 0 {
		t.Fatal("no schema-ops field rules loaded")
	}
}
