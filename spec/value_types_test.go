package spec_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

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

// TestExactlyOneRuleIsUntyped asserts spec/value-types.md §0.4's exception:
// value_type is optional, and across all ten field-rules.json tables exactly
// one rule legitimately omits it — comment.create.subject, a two-field record
// (schemas/comment.schema.json $defs/subject) no catalogue entry expresses.
// Naming it here is what keeps the exception from quietly spreading: adding a
// second untyped rule anywhere fails this test by name.
func TestExactlyOneRuleIsUntyped(t *testing.T) {
	rules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules failed: %v", err)
	}

	var untyped []string
	for _, r := range rules {
		if r.ValueType == "" {
			untyped = append(untyped, r.Vocabulary+"."+r.OpType+"."+r.Field)
		}
	}

	if len(untyped) != 1 {
		t.Fatalf("expected exactly one untyped rule across all field-rules.json tables, got %d: %v", len(untyped), untyped)
	}
	if want := "comments.create.subject"; untyped[0] != want {
		t.Errorf("the one untyped rule is %q, want %q", untyped[0], want)
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
