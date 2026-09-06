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
	reviewOpsSchemaID = "https://writ.dev/spec/review-ops.schema.json"
)

// compileReviewOpsSchemas compiles both the envelope, identifiers, and review-ops schemas.
// Compilation also validates the documents against the draft 2020-12 meta-schema.
func compileReviewOpsSchemas(t *testing.T) (*jsonschema.Schema, *jsonschema.Schema) {
	t.Helper()
	envRaw, err := spec.FS.ReadFile("schemas/op-envelope.schema.json")
	if err != nil {
		t.Fatalf("reading envelope schema: %v", err)
	}
	envDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(envRaw))
	if err != nil {
		t.Fatalf("decoding envelope schema: %v", err)
	}

	identRaw, err := spec.FS.ReadFile("schemas/identifiers.schema.json")
	if err != nil {
		t.Fatalf("reading identifiers schema: %v", err)
	}
	identDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(identRaw))
	if err != nil {
		t.Fatalf("decoding identifiers schema: %v", err)
	}

	valueTypesRaw, err := spec.FS.ReadFile("schemas/value-types.schema.json")
	if err != nil {
		t.Fatalf("reading value-types schema: %v", err)
	}
	valueTypesDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(valueTypesRaw))
	if err != nil {
		t.Fatalf("decoding value-types schema: %v", err)
	}

	revRaw, err := spec.FS.ReadFile("schemas/review-ops.schema.json")
	if err != nil {
		t.Fatalf("reading review-ops schema: %v", err)
	}
	revDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(revRaw))
	if err != nil {
		t.Fatalf("decoding review-ops schema: %v", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource(envelopeSchemaID, envDoc); err != nil {
		t.Fatalf("adding envelope schema resource: %v", err)
	}
	if err := c.AddResource(identifiersSchemaID, identDoc); err != nil {
		t.Fatalf("adding identifiers schema resource: %v", err)
	}
	if err := c.AddResource(valueTypesSchemaID, valueTypesDoc); err != nil {
		t.Fatalf("adding value-types schema resource: %v", err)
	}
	if err := c.AddResource(reviewOpsSchemaID, revDoc); err != nil {
		t.Fatalf("adding review-ops schema resource: %v", err)
	}

	envSch, err := c.Compile(envelopeSchemaID)
	if err != nil {
		t.Fatalf("compiling envelope schema: %v", err)
	}
	revSch, err := c.Compile(reviewOpsSchemaID)
	if err != nil {
		t.Fatalf("compiling review-ops schema: %v", err)
	}

	return envSch, revSch
}

// reviewInvariants enforces cross-field invariants on review op payloads
// (such as uniform OID length within a single op body across base, head,
// revision, merge_commit).
func reviewInvariants(payload map[string]any) error {
	body, ok := payload["body"].(map[string]any)
	if !ok {
		return nil
	}

	var oidLens []int
	for _, f := range []string{"base", "head", "revision", "merge_commit"} {
		if val, ok := body[f].(string); ok {
			oidLens = append(oidLens, len(val))
		}
	}

	for _, n := range oidLens {
		if n != oidLens[0] {
			return fmt.Errorf("mixed OID lengths %v: one repo has one object format", oidLens)
		}
	}

	return nil
}

func validateReviewOpVector(t *testing.T, envSch, revSch *jsonschema.Schema, raw []byte) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoding vector: %v", err)
	}
	if err := envSch.Validate(inst); err != nil {
		return fmt.Errorf("envelope schema: %w", err)
	}
	if err := revSch.Validate(inst); err != nil {
		return fmt.Errorf("review-ops schema: %w", err)
	}
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("re-decoding vector: %v", err)
	}
	if err := reviewInvariants(p); err != nil {
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

func TestReviewOpsSchemaCompiles(t *testing.T) {
	compileReviewOpsSchemas(t)
}

func TestValidReviewOpsVectors(t *testing.T) {
	envSch, revSch := compileReviewOpsSchemas(t)
	for _, name := range readDirNames(t, "testdata/review-ops/valid") {
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/review-ops/valid/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateReviewOpVector(t, envSch, revSch, raw); err != nil {
				t.Errorf("valid vector rejected: %v", err)
			}
		})
	}
}

func TestInvalidReviewOpsVectors(t *testing.T) {
	_, revSch := compileReviewOpsSchemas(t)

	rawIndex, err := spec.FS.ReadFile("testdata/review-ops/invalid/index.json")
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

	names := readDirNames(t, "testdata/review-ops/invalid")
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
			raw, err := spec.FS.ReadFile("testdata/review-ops/invalid/" + entry.File)
			if err != nil {
				t.Fatalf("reading instance: %v", err)
			}
			switch entry.Rejects {
			case "schema":
				inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
				if err != nil {
					t.Fatalf("parsing instance (a schema-rejected instance must be parseable JSON): %v", err)
				}
				if err := revSch.Validate(inst); err == nil {
					t.Errorf("schema accepted the instance; expected rejection: %s", entry.Reason)
				}
			case "invariant":
				inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
				if err != nil {
					t.Fatalf("parsing instance: %v", err)
				}
				if err := revSch.Validate(inst); err != nil {
					t.Errorf("schema rejected an invariant-kind vector (%v); expected only invariant to fail: %s", err, entry.Reason)
				}
				var p map[string]any
				if err := json.Unmarshal(raw, &p); err != nil {
					t.Fatal(err)
				}
				if err := reviewInvariants(p); err == nil {
					t.Errorf("invariants accepted it; expected rejection: %s", entry.Reason)
				}
			case "canonicalization":
				canon, err := canonicaljson.Marshal(raw)
				switch entry.Category {
				case "not-canonical":
					if err != nil {
						t.Fatalf("Marshal errored (%v); a not-canonical instance must canonicalize to different bytes", err)
					}
					if bytes.Equal(canon, raw) {
						t.Errorf("bytes are canonical; expected rejection: %s", entry.Reason)
					}
				case "duplicate-key", "lone-surrogate":
					if err == nil {
						t.Fatalf("Marshal accepted the instance; expected a %s rejection: %s", entry.Category, entry.Reason)
					}
					want := map[string]string{
						"duplicate-key":  "duplicate object key",
						"lone-surrogate": "lone surrogate",
					}[entry.Category]
					if !strings.Contains(err.Error(), want) {
						t.Errorf("Marshal rejected with %q, want a %s rejection (containing %q)", err, entry.Category, want)
					}
				default:
					t.Errorf("canonicalization entry has unknown category %q", entry.Category)
				}
			default:
				t.Errorf("index entry has unknown rejects value %q", entry.Rejects)
			}
		})
	}
}

type fieldRule struct {
	OpType    string   `json:"op_type"`
	OpVersion int      `json:"op_version"`
	Field     string   `json:"field"`
	Strategy  string   `json:"strategy"`
	Key       []string `json:"key,omitempty"`
}

func TestFieldRules(t *testing.T) {
	rawRules, err := spec.FS.ReadFile("testdata/review-ops/field-rules.json")
	if err != nil {
		t.Fatalf("reading field-rules.json: %v", err)
	}

	var rules []fieldRule
	if err := json.Unmarshal(rawRules, &rules); err != nil {
		t.Fatalf("decoding field-rules.json: %v", err)
	}

	ruleMap := make(map[string]fieldRule)
	for _, r := range rules {
		if r.OpType == "" || r.OpVersion < 1 || r.Field == "" {
			t.Errorf("invalid rule entry: %+v", r)
		}
		if !spec.KnownCatalogueStrategies[r.Strategy] {
			t.Errorf("strategy %q for (%s, %d, %s) is not in the closed catalogue of spec/fold.md", r.Strategy, r.OpType, r.OpVersion, r.Field)
		}
		if r.Strategy == "keyed-lww" && len(r.Key) == 0 {
			t.Errorf("keyed-lww strategy for (%s, %d, %s) must declare a non-empty key", r.OpType, r.OpVersion, r.Field)
		}
		key := fmt.Sprintf("%s:%d:%s", r.OpType, r.OpVersion, r.Field)
		if _, exists := ruleMap[key]; exists {
			t.Errorf("duplicate rule for %s", key)
		}
		ruleMap[key] = r
	}

	// Verify that every property of every body defined in review-ops.schema.json has a declared rule.
	rawSchema, err := spec.FS.ReadFile("schemas/review-ops.schema.json")
	if err != nil {
		t.Fatalf("reading review-ops.schema.json: %v", err)
	}
	var schemaDoc struct {
		Defs map[string]struct {
			Properties map[string]any `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(rawSchema, &schemaDoc); err != nil {
		t.Fatalf("decoding schema: %v", err)
	}

	bodyDefs := map[string]string{
		"create_body":     "create",
		"revision_body":   "revision",
		"update_body":     "update",
		"set_status_body": "set-status",
		"approval_body":   "approval",
		"ci_status_body":  "ci-status",
		"assign_body":     "assign",
		"label_body":      "label",
		"link_body":       "link",
	}

	for defName, opType := range bodyDefs {
		def, ok := schemaDoc.Defs[defName]
		if !ok {
			t.Fatalf("schema missing %s in $defs", defName)
		}
		for fieldName := range def.Properties {
			key := fmt.Sprintf("%s:1:%s", opType, fieldName)
			if _, ok := ruleMap[key]; !ok {
				t.Errorf("missing field rule for (%s, op_version: 1, field: %s)", opType, fieldName)
			}
		}
	}
}

func TestGitHubReviewOpsConversionVectors(t *testing.T) {
	envSch, revSch := compileReviewOpsSchemas(t)

	for _, name := range readDirNames(t, "testdata/review-ops/github") {
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/review-ops/github/" + name)
			if err != nil {
				t.Fatal(err)
			}
			var vec struct {
				GitHub json.RawMessage   `json:"github"`
				Ops    []json.RawMessage `json:"ops"`
			}
			if err := json.Unmarshal(raw, &vec); err != nil {
				t.Fatalf("decoding vector: %v", err)
			}
			if len(vec.GitHub) == 0 {
				t.Error("missing github member in vector")
			}
			if len(vec.Ops) == 0 {
				t.Error("missing ops member in vector")
			}
			for i, opRaw := range vec.Ops {
				t.Run(fmt.Sprintf("op_%d", i), func(t *testing.T) {
					canonOp, err := canonicaljson.Marshal(opRaw)
					if err != nil {
						t.Fatalf("canonicalizing op: %v", err)
					}
					if err := validateReviewOpVector(t, envSch, revSch, canonOp); err != nil {
						t.Errorf("op %d failed validation: %v", i, err)
					}
				})
			}
		})
	}
}
