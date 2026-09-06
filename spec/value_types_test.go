package spec_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/writtendev/writ/spec"
)

// valueTypesSchemaID is the value-types.schema.json $id, used by
// compileReviewOpsSchemas (review_ops_test.go) to resolve review-ops.schema.json's
// $defs/oid, which $refs value-types.schema.json#/$defs/git-oid.
const valueTypesSchemaID = "https://writ.dev/spec/value-types.schema.json"

// TestValueTypeCountMatchesProse binds spec/value-types.md's stated catalogue
// size to spec.KnownValueTypes, mirroring TestCatalogueCountMatchesProse
// (spec/fold_test.go) for the merge-strategy catalogue: a closed catalogue
// whose size is stated wrongly in prose is the one kind of error a reader has
// no way to detect from the document alone.
func TestValueTypeCountMatchesProse(t *testing.T) {
	raw, err := os.ReadFile("value-types.md")
	if err != nil {
		raw, err = os.ReadFile(filepath.Join("spec", "value-types.md"))
		if err != nil {
			t.Fatalf("reading value-types.md: %v", err)
		}
	}

	m := regexp.MustCompile(`\*\*closed catalogue\*\* of (\d+) value types`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("value-types.md no longer states the catalogue size in the form this test reads; " +
			"update the test deliberately rather than dropping the claim")
	}
	stated, err := strconv.Atoi(string(m[1]))
	if err != nil {
		t.Fatalf("parsing stated catalogue size %q: %v", m[1], err)
	}
	if stated != len(spec.KnownValueTypes) {
		t.Errorf("value-types.md states a closed catalogue of %d value types; KnownValueTypes holds %d",
			stated, len(spec.KnownValueTypes))
	}
}

// untypedRulesAllowed is spec/value-types.md §0.4's named exception list:
// every rule across all field-rules.json tables that legitimately omits
// value_type, because no catalogue entry expresses what it holds (a
// record, or a rule table's own array/object-shaped attribute) rather than
// a scalar or a typed register. Naming them here is what keeps the
// exception from quietly spreading: an untyped rule anywhere not in this
// set fails TestUntypedRulesAreNamed by name.
var untypedRulesAllowed = map[string]bool{
	"comments.create.subject":           true,
	"schema-ops.define-field.enum":      true,
	"schema-ops.define-field.key":       true,
	"schema-ops.define-field.key_types": true,
	"schema-ops.define-field.lattice":   true,
}

// TestUntypedRulesAreNamed asserts spec/value-types.md §0.4's exception:
// value_type is optional, and exactly the rules in untypedRulesAllowed
// legitimately omit it.
func TestUntypedRulesAreNamed(t *testing.T) {
	rules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules failed: %v", err)
	}

	seen := make(map[string]bool)
	var untyped []string
	for _, r := range rules {
		if r.ValueType == "" {
			name := r.Vocabulary + "." + r.OpType + "." + r.Field
			untyped = append(untyped, name)
			seen[name] = true
			if !untypedRulesAllowed[name] {
				t.Errorf("rule %q is untyped but is not in the named exception list", name)
			}
		}
	}

	for name := range untypedRulesAllowed {
		if !seen[name] {
			t.Errorf("named exception %q is no longer untyped (or no longer exists); update untypedRulesAllowed deliberately", name)
		}
	}

	if len(untyped) != len(untypedRulesAllowed) {
		t.Errorf("expected exactly %d untyped rules, got %d: %v", len(untypedRulesAllowed), len(untyped), untyped)
	}
}

// valueTypeVectorMaxLength reads the params.max_length declared on one
// testdata/value-types/valid vector, so TestValueTypeMaxLengthUnitIsCodePoints
// cannot drift from the number the corpus actually declares.
func valueTypeVectorMaxLength(t *testing.T, path string) int {
	t.Helper()
	raw, err := spec.FS.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Params struct {
			MaxLength *int `json:"max_length"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	if v.Params.MaxLength == nil {
		t.Fatalf("%s declares no params.max_length", path)
	}
	return *v.Params.MaxLength
}

// TestValueTypeMaxLengthUnitIsCodePoints pins that a value-type rule's
// max_length counts Unicode code points (spec/value-types.md §0.3), in the
// shape of TestPersonIDLengthUnitIsCodePoints and its two siblings in
// length_units_test.go.
func TestValueTypeMaxLengthUnitIsCodePoints(t *testing.T) {
	bound := valueTypeVectorMaxLength(t, "testdata/value-types/valid/string-at-max-length-multibyte.json")

	vectors := corpusStrings(t, "testdata/value-types/valid", func(raw []byte) (string, bool) {
		var v struct {
			ValueType string          `json:"value_type"`
			Value     json.RawMessage `json:"value"`
			Params    struct {
				MaxLength int `json:"max_length"`
			} `json:"params"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", false
		}
		if v.ValueType != "string" || v.Params.MaxLength == 0 {
			return "", false
		}
		var s string
		if err := json.Unmarshal(v.Value, &s); err != nil {
			return "", false
		}
		return s, true
	})
	checkUnitsAreDistinguishable(t, bound, vectors)
}

// valueTypesDefNames reads the $defs names declared in
// schemas/value-types.schema.json, the same set
// TestKnownValueTypesDriftGuard (engine/internal/value/value_test.go) binds
// value.Known to. This copy stays local to spec_test because the drift
// guard's copy lives in the external value_test package, which this package
// cannot import without reaching back into engine/internal/value from spec —
// exactly the direction engine/internal/value/imports_test.go's allowlist
// forbids.
func valueTypesDefNames(t *testing.T) []string {
	t.Helper()
	raw, err := spec.FS.ReadFile("schemas/value-types.schema.json")
	if err != nil {
		t.Fatalf("reading value-types.schema.json: %v", err)
	}
	var doc struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding value-types.schema.json: %v", err)
	}
	names := make([]string, 0, len(doc.Defs))
	for name := range doc.Defs {
		names = append(names, name)
	}
	return names
}

// compileValueTypesSchemaCompiler loads schemas/value-types.schema.json plus
// every schema its $defs $ref into (identifiers.schema.json for
// person-ref/object-ref, ordering.schema.json for position,
// anchor.schema.json for anchor) as compiler resources. Loading the
// resources without compiling a location yet is what lets each $defs/<type>
// entry then be compiled separately, on its own JSON-pointer fragment
// (valueTypesSchemaID + "#/$defs/<type>") — compiler.Compile resolves a
// fragment directly, so no wrapper schema (contrast
// compilePersonIDSchema's wrapper in identifiers_test.go, needed there only
// because that test wants a schema whose top-level shape is a
// {"person": ...} envelope, not because reaching a $def requires one).
func compileValueTypesSchemaCompiler(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	c := jsonschema.NewCompiler()
	deps := []struct {
		id   string
		path string
	}{
		{identifiersSchemaID, "schemas/identifiers.schema.json"},
		{orderingSchemaID, "schemas/ordering.schema.json"},
		{anchorSchemaID, "schemas/anchor.schema.json"},
		{valueTypesSchemaID, "schemas/value-types.schema.json"},
	}
	for _, dep := range deps {
		raw, err := spec.FS.ReadFile(dep.path)
		if err != nil {
			t.Fatalf("reading %s: %v", dep.path, err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("decoding %s: %v", dep.path, err)
		}
		if err := c.AddResource(dep.id, doc); err != nil {
			t.Fatalf("adding %s as a resource: %v", dep.path, err)
		}
	}
	return c
}

// compileValueTypeDefs compiles every schemas/value-types.schema.json
// $defs/<value_type> entry in isolation, keyed by value-type name.
// Compilation also validates each $def against the draft 2020-12
// meta-schema, so this is what makes eleven of the twelve $defs (every one
// but git-oid, which review-ops.schema.json's $defs/oid already reaches)
// compiled by something, closing the gap the round-3 review flagged.
func compileValueTypeDefs(t *testing.T) map[string]*jsonschema.Schema {
	t.Helper()
	c := compileValueTypesSchemaCompiler(t)
	names := valueTypesDefNames(t)
	schemas := make(map[string]*jsonschema.Schema, len(names))
	for _, vt := range names {
		sch, err := c.Compile(valueTypesSchemaID + "#/$defs/" + vt)
		if err != nil {
			t.Fatalf("compiling $defs/%s: %v", vt, err)
		}
		schemas[vt] = sch
	}
	return schemas
}

// TestValueTypeDefsCompile pins that every $defs entry in
// schemas/value-types.schema.json compiles on its own (which also checks it
// against the draft 2020-12 meta-schema), independent of any corpus vector.
func TestValueTypeDefsCompile(t *testing.T) {
	compileValueTypeDefs(t)
}

// valueTypeVector is the shape shared by every
// testdata/value-types/{valid,invalid} file: engine/internal/value/value_test.go
// reads the same file into its own local "vector" type for value.Validate;
// this is the schema-side reader, decoding "value" as raw JSON so it can be
// re-encoded through jsonschema.UnmarshalJSON rather than round-tripped
// through Go's any-decoding (which collapses ints and floats the same way
// and would hide a schema that only rejects one of them).
type valueTypeVector struct {
	ValueType string          `json:"value_type"`
	Value     json.RawMessage `json:"value"`
}

func loadValueTypeVector(t *testing.T, path string) valueTypeVector {
	t.Helper()
	raw, err := spec.FS.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var v valueTypeVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return v
}

// TestValidValueTypeVectorsAgainstSchema drives every
// testdata/value-types/valid vector through
// schemas/value-types.schema.json's own $defs/<value_type> — the schema
// side of engine/internal/value/value_test.go's TestValueTypeVectors, which
// drives the same corpus through value.Validate. Before this test, nothing
// exercised the schema's own content against the corpus; a corrupted $def
// left every existing test green (round-3 review finding, PR #153).
func TestValidValueTypeVectorsAgainstSchema(t *testing.T) {
	schemas := compileValueTypeDefs(t)
	names := readDirNames(t, "testdata/value-types/valid")
	if len(names) == 0 {
		t.Fatal("testdata/value-types/valid yielded no vectors")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			v := loadValueTypeVector(t, "testdata/value-types/valid/"+name)
			sch, ok := schemas[v.ValueType]
			if !ok {
				t.Fatalf("vector declares value_type %q, which schemas/value-types.schema.json has no $defs entry for", v.ValueType)
			}
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(v.Value))
			if err != nil {
				t.Fatalf("decoding value: %v", err)
			}
			if err := sch.Validate(inst); err != nil {
				t.Errorf("$defs/%s rejected a valid %s vector: %v", v.ValueType, v.ValueType, err)
			}
		})
	}
}

// schemaOutOfScopeInvalid records the testdata/value-types/invalid vectors
// that schemas/value-types.schema.json's own top-level description declares
// out of scope for a $def in isolation: "Parameterisation a rule may
// declare (enum's member list, max_length) is not expressed here ... it is
// enforced by engine/internal/value alongside these schemas." Both vectors
// carry a value that is a conforming instance of the bare JSON type
// ($defs/enum is just "type": "string"; $defs/string carries no
// max_length) — their rejection depends on a rule's own params, which a
// bare $def is never given. The schema accepting them is therefore not
// schema/implementation drift; it is this documented split of
// responsibility holding. Every other invalid vector has no such excuse and
// must be rejected by the $def itself.
var schemaOutOfScopeInvalid = map[string]string{
	"enum-non-member.json":        "enum membership is the declaring rule's own member list, not expressed in $defs/enum",
	"string-over-max-length.json": "max_length is the declaring rule's own parameter, not expressed in $defs/string",
}

// TestInvalidValueTypeVectorsAgainstSchema drives every
// testdata/value-types/invalid vector through
// schemas/value-types.schema.json's own $defs/<value_type>, requiring
// rejection — the schema side of
// engine/internal/value/value_test.go's TestInvalidValueTypeVectors.
// schemaOutOfScopeInvalid documents the only two vectors where the schema
// and value.Validate legitimately disagree (see its comment); every other
// vector must be rejected by the schema exactly as value.Validate rejects
// it, or this test reports the disagreement rather than weakening the
// vector.
func TestInvalidValueTypeVectorsAgainstSchema(t *testing.T) {
	schemas := compileValueTypeDefs(t)

	rawIndex, err := spec.FS.ReadFile("testdata/value-types/invalid/index.json")
	if err != nil {
		t.Fatalf("reading invalid/index.json: %v", err)
	}
	var index map[string]struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatalf("decoding invalid/index.json: %v", err)
	}

	names := readDirNames(t, "testdata/value-types/invalid")
	checked := 0
	for _, name := range names {
		if name == "index.json" {
			continue
		}
		entry, ok := index[name]
		if !ok {
			// TestInvalidValueTypeVectors (engine/internal/value/value_test.go)
			// already fails on a missing index entry; do not double-report it.
			continue
		}
		checked++
		t.Run(name, func(t *testing.T) {
			v := loadValueTypeVector(t, "testdata/value-types/invalid/"+name)
			sch, ok := schemas[v.ValueType]
			if !ok {
				t.Fatalf("vector declares value_type %q, which schemas/value-types.schema.json has no $defs entry for", v.ValueType)
			}
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(v.Value))
			if err != nil {
				t.Fatalf("decoding value: %v", err)
			}
			schemaErr := sch.Validate(inst)
			if reason, outOfScope := schemaOutOfScopeInvalid[name]; outOfScope {
				if schemaErr != nil {
					t.Errorf("%s is recorded in schemaOutOfScopeInvalid (%s), but the schema rejected it (%v) — the exception is stale and should be removed", name, reason, schemaErr)
				}
				return
			}
			if schemaErr == nil {
				t.Errorf("$defs/%s accepted an invalid vector; value.Validate rejects it for: %s. If this is a genuine schema/implementation disagreement, record it in schemaOutOfScopeInvalid with a reason rather than weakening the vector.", v.ValueType, entry.Reason)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no testdata/value-types/invalid vectors were checked against the schema")
	}
	for name := range schemaOutOfScopeInvalid {
		if _, ok := index[name]; !ok {
			t.Errorf("schemaOutOfScopeInvalid names %s, which testdata/value-types/invalid/index.json no longer lists", name)
		}
	}
}
