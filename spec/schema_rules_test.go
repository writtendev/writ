package spec_test

import (
	"encoding/json"
	"testing"

	"github.com/writtendev/writ/spec"
)

// matrixEntry is one cell of the rule-validation matrix: a (strategy,
// value_type) pair and spec.ValidateFieldRule's verdict on it.
type matrixEntry struct {
	Strategy  string `json:"strategy"`
	ValueType string `json:"value_type,omitempty"`
	Expect    string `json:"expect"`
	Reason    string `json:"reason,omitempty"`
}

// loadSchemaRuleMatrix reads testdata/schema-rules/matrix.json. It lives
// under a directory literally named anything but "field-rules.json"
// (spec.FieldRules() and TestUntypedRulesAreNamed, spec/value_types_test.go,
// both match only that exact filename) precisely so this matrix is never
// folded into the field-rules union the `fold` fixture family folds
// against (spec/fixtures/fold_test.go) or scanned for untyped rules.
func loadSchemaRuleMatrix(t *testing.T) []matrixEntry {
	t.Helper()
	raw, err := spec.FS.ReadFile("testdata/schema-rules/matrix.json")
	if err != nil {
		t.Fatalf("reading testdata/schema-rules/matrix.json: %v", err)
	}
	var entries []matrixEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("decoding testdata/schema-rules/matrix.json: %v", err)
	}
	return entries
}

// TestSchemaRuleMatrix asserts spec.ValidateFieldRule's verdict against
// every (strategy, value_type) cell in the closed cross-product: the 9
// merge strategies (spec/fold.md §5) times the 12 value types plus the
// untyped ("no value_type declared") cell (spec/value-types.md), for 9*13
// = 117 cells total. This is axis D of WRIT-190's conformance corpus: the
// cross-product is covered exhaustively and cheaply here, as a
// rule-validation matrix, rather than as ~95 new fold fixtures pinning
// byte-identical output (the read path consults value_type for exactly one
// purpose — person-ref normalization, spec/value-types.md
// §Producer-side and reader-tolerant — so almost every typed cell folds
// identically to its untyped twin; a fold fixture per cell would pin the
// corpus generator, not the format).
//
// The matrix is bidirectionally bound to the two closed catalogues, the
// way TestUntypedRulesAreNamed binds its exception list: a strategy or
// value type added to spec.KnownCatalogueStrategies or spec.KnownValueTypes
// without a matching matrix row fails this test by the missing cell's own
// name, and a stale or duplicate row fails it too.
func TestSchemaRuleMatrix(t *testing.T) {
	entries := loadSchemaRuleMatrix(t)

	wantCells := len(spec.KnownCatalogueStrategies) * (len(spec.KnownValueTypes) + 1)
	if len(entries) != wantCells {
		t.Fatalf("matrix.json has %d cells, want %d (%d strategies * (%d value types + 1 untyped))",
			len(entries), wantCells, len(spec.KnownCatalogueStrategies), len(spec.KnownValueTypes))
	}

	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.Strategy == "" {
			t.Fatalf("matrix row has empty strategy: %+v", e)
		}
		if e.Expect != "accept" && e.Expect != "reject" {
			t.Fatalf("matrix row (%s, %q) has invalid expect %q (must be accept or reject)", e.Strategy, e.ValueType, e.Expect)
		}
		if e.Expect == "reject" && e.Reason == "" {
			t.Fatalf("matrix row (%s, %q) expects reject but carries no reason", e.Strategy, e.ValueType)
		}
		if e.Expect == "accept" && e.Reason != "" {
			t.Fatalf("matrix row (%s, %q) expects accept but carries a reject reason %q", e.Strategy, e.ValueType, e.Reason)
		}
		if !spec.KnownCatalogueStrategies[e.Strategy] {
			t.Errorf("matrix row names strategy %q, not a member of spec.KnownCatalogueStrategies", e.Strategy)
		}
		if e.ValueType != "" && !spec.KnownValueTypes[e.ValueType] {
			t.Errorf("matrix row names value_type %q, not a member of spec.KnownValueTypes", e.ValueType)
		}

		key := e.Strategy + "\x00" + e.ValueType
		if seen[key] {
			t.Errorf("matrix has duplicate row for (strategy=%s, value_type=%q)", e.Strategy, e.ValueType)
		}
		seen[key] = true

		rule := buildMatrixRule(e.Strategy, e.ValueType)
		err := spec.ValidateFieldRule(rule)
		switch {
		case e.Expect == "accept" && err != nil:
			t.Errorf("(strategy=%s, value_type=%q) expected accept, ValidateFieldRule rejected: %v", e.Strategy, e.ValueType, err)
		case e.Expect == "reject" && err == nil:
			t.Errorf("(strategy=%s, value_type=%q) expected reject (%s), ValidateFieldRule accepted", e.Strategy, e.ValueType, e.Reason)
		}
	}

	// Bidirectional: every cell of the closed cross-product must have a row.
	// This is what catches a catalogue addition the matrix was never
	// updated for, by the missing cell's own name.
	valueTypes := []string{""}
	for vt := range spec.KnownValueTypes {
		valueTypes = append(valueTypes, vt)
	}
	for strategy := range spec.KnownCatalogueStrategies {
		for _, vt := range valueTypes {
			key := strategy + "\x00" + vt
			if !seen[key] {
				t.Errorf("no matrix row for (strategy=%s, value_type=%q); spec.KnownCatalogueStrategies/spec.KnownValueTypes gained a member matrix.json does not cover", strategy, vt)
			}
		}
	}
}

// buildMatrixRule constructs the FieldRule for one (strategy, value_type)
// matrix cell, supplying exactly the strategy-structural fields
// spec.ValidateFieldRule requires so the only thing under test is the
// strategy/value_type combination itself: a non-empty key (and matching
// key_types) for keyed-lww, non-empty lattice elements (and a matching
// enum when value_type is "enum") for lattice.
func buildMatrixRule(strategy, valueType string) spec.FieldRule {
	rule := spec.FieldRule{
		OpType:    "test-op",
		OpVersion: 1,
		Field:     "value",
		Strategy:  strategy,
		ValueType: valueType,
	}
	switch strategy {
	case "keyed-lww":
		rule.Key = []string{"k"}
		rule.KeyTypes = map[string]string{"k": "string"}
	case "lattice":
		rule.Lattice = []string{"a", "b"}
	}
	if valueType == "enum" {
		rule.Enum = []string{"a", "b"}
	}
	return rule
}
