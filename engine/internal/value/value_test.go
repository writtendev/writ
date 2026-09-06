package value_test

import (
	"encoding/json"
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
