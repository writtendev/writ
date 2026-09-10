package value_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/internal/value"
	"github.com/writtendev/writ/spec"
)

// vector mirrors one entry under spec/testdata/value-types/{valid,invalid}/:
// {"value_type": ..., "value": ..., "params": {...}}.
type vector struct {
	ValueType string `json:"value_type"`
	Value     any    `json:"value"`
	Params    struct {
		Enum      []string `json:"enum"`
		MaxLength int64    `json:"max_length"`
	} `json:"params"`
}

func readDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := spec.FS.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
	}
	return names
}

func loadVector(t *testing.T, path string) vector {
	t.Helper()
	raw, err := spec.FS.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var v vector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return v
}

// TestValueTypeVectors drives every testdata/value-types/valid vector through
// value.Validate and requires it to be accepted.
func TestValueTypeVectors(t *testing.T) {
	names := readDirNames(t, "testdata/value-types/valid")
	if len(names) == 0 {
		t.Fatal("testdata/value-types/valid yielded no vectors")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			v := loadVector(t, "testdata/value-types/valid/"+name)
			params := value.Params{Enum: v.Params.Enum, MaxLength: v.Params.MaxLength}
			if err := value.Validate(v.ValueType, params, v.Value); err != nil {
				t.Errorf("value.Validate(%q, %+v, %v) = %v, want accepted", v.ValueType, params, v.Value, err)
			}
		})
	}
}

// TestInvalidValueTypeVectors drives every testdata/value-types/invalid vector
// through value.Validate and requires rejection, cross-checked against
// invalid/index.json's recorded reason in the established style
// (testdata/persons/invalid/index.json is the model).
func TestInvalidValueTypeVectors(t *testing.T) {
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
	if len(index) == 0 {
		t.Fatal("invalid/index.json is empty")
	}

	names := readDirNames(t, "testdata/value-types/invalid")
	seen := make(map[string]bool)
	for _, name := range names {
		if name == "index.json" {
			continue
		}
		seen[name] = true
		entry, ok := index[name]
		if !ok {
			t.Errorf("%s has no entry in invalid/index.json", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			v := loadVector(t, "testdata/value-types/invalid/"+name)
			params := value.Params{Enum: v.Params.Enum, MaxLength: v.Params.MaxLength}
			err := value.Validate(v.ValueType, params, v.Value)
			if err == nil {
				t.Fatalf("value.Validate(%q, %+v, %v) accepted; want rejected: %s", v.ValueType, params, v.Value, entry.Reason)
			}
		})
	}
	for name := range index {
		if !seen[name] {
			t.Errorf("invalid/index.json names %s, which does not exist under testdata/value-types/invalid", name)
		}
	}
}

// TestValueTypeCoversWholeCatalogue guards that every value type in the
// closed catalogue has at least one valid vector AND at least one invalid
// vector, the same coverage discipline spec/fold_test.go's TestMergeCoverage
// applies to strategies. Both sides are enforced because spec/value-types.md
// and spec/README.md both normatively claim "a valid and invalid instance per
// catalogue type" — a claim only true if this test checks the invalid side
// too, not just the valid one.
func TestValueTypeCoversWholeCatalogue(t *testing.T) {
	validCovered := make(map[string]bool)
	for _, name := range readDirNames(t, "testdata/value-types/valid") {
		v := loadVector(t, "testdata/value-types/valid/"+name)
		validCovered[v.ValueType] = true
	}
	invalidCovered := make(map[string]bool)
	for _, name := range readDirNames(t, "testdata/value-types/invalid") {
		if name == "index.json" {
			continue
		}
		v := loadVector(t, "testdata/value-types/invalid/"+name)
		invalidCovered[v.ValueType] = true
	}
	for vt := range value.Known {
		if !validCovered[vt] {
			t.Errorf("value type %q has no valid vector under testdata/value-types/valid", vt)
		}
		if !invalidCovered[vt] {
			t.Errorf("value type %q has no invalid vector under testdata/value-types/invalid", vt)
		}
	}
}

// TestPersonRefRejectionQuotesValueSafely is WRIT-137's pinning test for the
// premise check its plan recorded: strconv.IsPrint is false for every
// forbidden code point (spec/identifiers.md §Value character repertoire), so
// %q already escapes all of them via strconv.Quote -- this is a pin, not a
// change, guarding against a future switch from %q to %s in the person-ref
// rejection message going unnoticed. It also checks the message names the
// offending code point.
func TestPersonRefRejectionQuotesValueSafely(t *testing.T) {
	hostile := "email:alice" + string(rune(0x202E)) + "@evil.com"
	err := value.Validate("person-ref", value.Params{}, hostile)
	if err == nil {
		t.Fatal("value.Validate accepted a person-ref value carrying a bidi override")
	}
	wantQuoted := strconv.Quote(hostile)
	if !strings.Contains(err.Error(), wantQuoted) {
		t.Errorf("error %q does not contain the %%q-quoted value %q", err.Error(), wantQuoted)
	}
	// strconv.Quote escapes a non-printable rune as lowercase \uXXXX.
	if !strings.Contains(err.Error(), "\\u202e") {
		t.Errorf("error %q does not contain the escaped hostile code point", err.Error())
	}
	if !strings.Contains(err.Error(), "U+202E") {
		t.Errorf("error %q does not name the offending code point", err.Error())
	}
}

// TestPersonRefSchemeProblemNotCodePointDecorated pins a round-1 review
// finding on WRIT-137's PR: the (U+XXXX) suffix must be attached only when
// person.Check's returned Problem is actually ForbiddenCodePoint, not
// whenever the value happens to contain a forbidden code point somewhere.
// A scheme-shaped failure (SchemeCharset here) on a value whose *value* half
// also carries a forbidden code point must report the scheme problem alone --
// naming a code point that is not the reported problem is misleading, not
// merely decorative.
func TestPersonRefSchemeProblemNotCodePointDecorated(t *testing.T) {
	hostile := "my_scheme:ali" + string(rune(0x202E)) + "ce"
	err := value.Validate("person-ref", value.Params{}, hostile)
	if err == nil {
		t.Fatal("value.Validate accepted a person-ref value with an invalid scheme")
	}
	if !strings.Contains(err.Error(), "scheme must match") {
		t.Errorf("error %q does not report the scheme problem", err.Error())
	}
	if strings.Contains(err.Error(), "U+202E") || strings.Contains(err.Error(), "(U+") {
		t.Errorf("error %q wrongly decorates a scheme problem with a forbidden-code-point suffix", err.Error())
	}
}

// TestValueTypeUnknownRejected pins that an undeclared value type is not
// silently accepted.
func TestValueTypeUnknownRejected(t *testing.T) {
	if err := value.Validate("frobnicate", value.Params{}, "anything"); err == nil {
		t.Fatal("value.Validate accepted an unknown value type")
	} else if !strings.Contains(err.Error(), "unknown value type") {
		t.Errorf("error %q does not mention the unknown type", err.Error())
	}
}

// TestNormalizeOnlyPersonRef pins that Normalize is the identity for every
// value type except person-ref, which is the one normalization behaviour
// spec/value-types.md defines.
func TestNormalizeOnlyPersonRef(t *testing.T) {
	const in = "email:Alice@Example.COM"
	for vt := range value.Known {
		got := value.Normalize(vt, in)
		if vt == "person-ref" {
			if got == in {
				t.Errorf("Normalize(%q, %q) = %q, want it normalized (changed)", vt, in, got)
			}
			continue
		}
		if got != in {
			t.Errorf("Normalize(%q, %q) = %q, want unchanged", vt, in, got)
		}
	}
}

// TestKnownValueTypesDriftGuard binds value.Known to its other three
// hand-maintained appearances of the closed catalogue — spec.KnownValueTypes
// (spec/fieldrules.go), schemas/value-types.schema.json's $defs, and
// field-rules.schema.json's value_type enum — in the shape of
// TestFieldRuleVocabulariesIsExhaustive (engine/codec/valuetype_test.go),
// round 1's fix for the same class of problem. value.Known cannot import
// spec (engine/internal/value stays person + stdlib only, so fold stays free
// of I/O: engine/internal/fold/imports_test.go), so the binding has to live
// here instead, in the external test package, which is free to import both.
//
// Without this, a 13th value type added to spec.KnownValueTypes, both
// schemas, and value-types.md's prose count — but forgotten in value.Known
// and Validate's switch — leaves every existing test green: a rule declaring
// the new type passes ValidateFieldRule and the field-rules schema, but
// validateValueTypes then hits Validate's default branch and rejects every
// write to that field with "unknown value type", silently, at produce time.
func TestKnownValueTypesDriftGuard(t *testing.T) {
	schemaDefs := valueTypesSchemaDefs(t)
	fieldRuleEnum := fieldRuleValueTypeEnum(t)

	sources := map[string]map[string]bool{
		"spec.KnownValueTypes":                      spec.KnownValueTypes,
		"schemas/value-types.schema.json's $defs":   schemaDefs,
		"field-rules.schema.json's value_type enum": fieldRuleEnum,
	}
	for name, other := range sources {
		for vt := range value.Known {
			if !other[vt] {
				t.Errorf("value.Known has %q, %s does not", vt, name)
			}
		}
		for vt := range other {
			if !value.Known[vt] {
				t.Errorf("%s has %q, value.Known does not", name, vt)
			}
		}
	}

	// Validate's switch must have a live case for every catalogued type, not
	// just a Known entry: hitting the default branch is exactly the failure
	// this guard exists to catch. nil isn't a valid instance of any type, but
	// every real case rejects it with a type-specific message rather than
	// falling through to "unknown value type" — so this only fails if the
	// switch itself is missing a case.
	for vt := range spec.KnownValueTypes {
		err := value.Validate(vt, value.Params{}, nil)
		if err != nil && strings.Contains(err.Error(), "unknown value type") {
			t.Errorf("value.Validate(%q, ...) = %v; Validate's switch has no case for %q", vt, err, vt)
		}
	}
}

// valueTypesSchemaDefs returns the $defs names declared in
// schemas/value-types.schema.json.
func valueTypesSchemaDefs(t *testing.T) map[string]bool {
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
	names := make(map[string]bool, len(doc.Defs))
	for name := range doc.Defs {
		names[name] = true
	}
	return names
}

// fieldRuleValueTypeEnum returns the member list of
// field-rules.schema.json's $defs/field-rule/properties/value_type enum.
func fieldRuleValueTypeEnum(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := spec.FS.ReadFile("schemas/field-rules.schema.json")
	if err != nil {
		t.Fatalf("reading field-rules.schema.json: %v", err)
	}
	var doc struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding field-rules.schema.json: %v", err)
	}
	fieldRule, ok := doc.Defs["field-rule"]
	if !ok {
		t.Fatal("field-rules.schema.json has no $defs/field-rule")
	}
	prop, ok := fieldRule.Properties["value_type"]
	if !ok {
		t.Fatal("field-rules.schema.json's $defs/field-rule has no value_type property")
	}
	names := make(map[string]bool, len(prop.Enum))
	for _, name := range prop.Enum {
		names[name] = true
	}
	return names
}
