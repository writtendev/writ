package spec

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// FieldRule specifies the merge strategy and value type for an (op_type, field) tuple.
type FieldRule struct {
	OpType     string            `json:"op_type,omitempty"`
	OpVersion  int64             `json:"op_version,omitempty"`
	Field      string            `json:"field"`
	Target     string            `json:"target,omitempty"`
	Strategy   string            `json:"strategy"`
	Key        []string          `json:"key,omitempty"`
	Lattice    []string          `json:"lattice,omitempty"`
	ValueType  string            `json:"value_type,omitempty"`
	Enum       []string          `json:"enum,omitempty"`
	MaxLength  int64             `json:"max_length,omitempty"`
	KeyTypes   map[string]string `json:"key_types,omitempty"`
	Vocabulary string            `json:"-"`
}

// TargetKey returns Target if non-empty, otherwise Field.
func (r FieldRule) TargetKey() string {
	if r.Target != "" {
		return r.Target
	}
	return r.Field
}

// NormalizesKey reports whether keyCol is declared as a person-ref key column
// (spec/value-types.md): normalization is intrinsic to the person-ref value
// type rather than a separate rule attribute.
func (r FieldRule) NormalizesKey(keyCol string) bool {
	return r.KeyTypes[keyCol] == "person-ref"
}

// NormalizesValue reports whether the rule's scalar (or keyed-register) value
// is a person-ref, and so is normalized per spec/identifiers.md.
func (r FieldRule) NormalizesValue() bool {
	return r.ValueType == "person-ref"
}

// NormalizesItems reports whether the rule's collection elements are
// person-ref values, and so are normalized per spec/identifiers.md.
func (r FieldRule) NormalizesItems() bool {
	return r.ValueType == "person-ref"
}

// ruleKey identifies one declared rule for duplicate detection. It is a struct
// rather than a formatted string because every non-test file in this package is
// a fold value path (spec/foldrendering_test.go): `fmt.Errorf` is the only call
// into `fmt` allowed here, and a composite map key needs no formatting at all.
type ruleKey struct {
	Dir       string
	OpType    string
	OpVersion int64
	Field     string
}

// KnownValueTypes is the closed catalogue of value types from
// spec/value-types.md: the second, orthogonal axis a schema needs alongside
// the merge-strategy catalogue in spec/fold.md §5.
var KnownValueTypes = map[string]bool{
	"string":     true,
	"text":       true,
	"int":        true,
	"number":     true,
	"bool":       true,
	"timestamp":  true,
	"enum":       true,
	"person-ref": true,
	"object-ref": true,
	"git-oid":    true,
	"position":   true,
	"anchor":     true,
}

// ValidateFieldRule validates an individual field rule definition.
func ValidateFieldRule(r FieldRule) error {
	if r.OpType == "" {
		return fmt.Errorf("rule with empty op_type")
	}
	if r.OpVersion < 1 {
		return fmt.Errorf("rule with invalid op_version: %d", r.OpVersion)
	}
	if r.Field == "" {
		return fmt.Errorf("rule with empty field")
	}
	if !KnownCatalogueStrategies[r.Strategy] {
		return fmt.Errorf("rule for (%s, %s) has unknown strategy %q", r.OpType, r.Field, r.Strategy)
	}
	if r.Strategy == "keyed-lww" && len(r.Key) == 0 {
		return fmt.Errorf("rule for (%s, %s) uses keyed-lww but declares no key", r.OpType, r.Field)
	}
	if r.Strategy == "lattice" && len(r.Lattice) == 0 {
		return fmt.Errorf("rule for (%s, %s) uses lattice but defines no elements", r.OpType, r.Field)
	}

	// value_type is optional (spec/value-types.md): a rule declaring none is
	// untyped, mirroring the "no declared strategy" idiom of spec/fold.md §5.
	// A declared one must be a member of the closed catalogue.
	if r.ValueType != "" && !KnownValueTypes[r.ValueType] {
		return fmt.Errorf("rule for (%s, %s) declares unknown value_type %q", r.OpType, r.Field, r.ValueType)
	}

	// enum is required iff value_type == "enum", forbidden otherwise.
	if r.ValueType == "enum" && len(r.Enum) == 0 {
		return fmt.Errorf("rule for (%s, %s) declares value_type enum but no enum values", r.OpType, r.Field)
	}
	if r.ValueType != "enum" && len(r.Enum) > 0 {
		return fmt.Errorf("rule for (%s, %s) declares enum values on non-enum value_type %q", r.OpType, r.Field, r.ValueType)
	}

	// max_length only parameterises string and text.
	if r.MaxLength != 0 && r.ValueType != "string" && r.ValueType != "text" {
		return fmt.Errorf("rule for (%s, %s) declares max_length on value_type %q; only string and text take one", r.OpType, r.Field, r.ValueType)
	}

	// tombstone's accumulator tests val == true / val == false and nothing
	// else, so any value_type other than bool is a rule that can never fire.
	if r.Strategy == "tombstone" && r.ValueType != "" && r.ValueType != "bool" {
		return fmt.Errorf("rule for (%s, %s) uses tombstone with value_type %q; only bool typechecks", r.OpType, r.Field, r.ValueType)
	}

	// lattice's semilattice elements and the field's legal values must be the
	// same list: value_type enum only, and every lattice element a member of
	// the declared enum.
	if r.Strategy == "lattice" {
		if r.ValueType != "" && r.ValueType != "enum" {
			return fmt.Errorf("rule for (%s, %s) uses lattice with value_type %q; only enum typechecks", r.OpType, r.Field, r.ValueType)
		}
		if r.ValueType == "enum" {
			for _, le := range r.Lattice {
				found := false
				for _, e := range r.Enum {
					if e == le {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("rule for (%s, %s) declares lattice element %q not a member of its enum %v", r.OpType, r.Field, le, r.Enum)
				}
			}
		}
	}

	// key_types is required for keyed-lww, covering exactly the columns key
	// declares, and forbidden everywhere else.
	if r.Strategy == "keyed-lww" {
		if len(r.KeyTypes) != len(r.Key) {
			return fmt.Errorf("rule for (%s, %s) declares key_types covering %d column(s), want exactly the %d in key %v", r.OpType, r.Field, len(r.KeyTypes), len(r.Key), r.Key)
		}
		for _, k := range r.Key {
			kt, ok := r.KeyTypes[k]
			if !ok {
				return fmt.Errorf("rule for (%s, %s) declares no key_types entry for key column %q", r.OpType, r.Field, k)
			}
			if !KnownValueTypes[kt] {
				return fmt.Errorf("rule for (%s, %s) declares unknown key_types value_type %q for column %q", r.OpType, r.Field, kt, k)
			}
		}
	} else if len(r.KeyTypes) > 0 {
		return fmt.Errorf("rule for (%s, %s) declares key_types on non-keyed-lww strategy %q", r.OpType, r.Field, r.Strategy)
	}

	return nil
}

// keyGroupKey identifies the set of sibling rules that share one keyed-lww
// key tuple, for the cross-rule key_types consistency check in FieldRules:
// every rule keyed on the same (op_type, op_version, key) must declare the
// same key_types, because they describe the same key columns.
type keyGroupKey struct {
	Dir       string
	OpType    string
	OpVersion int64
	Key       string
}

// FieldRules loads all field-rules.json files from the embedded spec.FS and validates each entry
// against the closed catalogue of strategies defined in spec/fold.md.
func FieldRules() ([]FieldRule, error) {
	var allRules []FieldRule
	seen := make(map[ruleKey]bool)
	keyTypesByGroup := make(map[keyGroupKey]map[string]string)

	err := fs.WalkDir(FS, "testdata", func(filePath string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(filePath, "field-rules.json") {
			return nil
		}

		raw, err := FS.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("spec: reading %s: %w", filePath, err)
		}

		var rules []FieldRule
		if err := json.Unmarshal(raw, &rules); err != nil {
			return fmt.Errorf("spec: decoding %s: %w", filePath, err)
		}

		vocab := path.Base(path.Dir(filePath))
		for _, r := range rules {
			if err := ValidateFieldRule(r); err != nil {
				return fmt.Errorf("spec: %s %w", filePath, err)
			}

			key := ruleKey{Dir: path.Dir(filePath), OpType: r.OpType, OpVersion: r.OpVersion, Field: r.Field}
			if seen[key] {
				return fmt.Errorf("spec: %s has duplicate rule for (%s, %d, %s) under %s",
					filePath, r.OpType, r.OpVersion, r.Field, key.Dir)
			}
			seen[key] = true

			if r.Strategy == "keyed-lww" {
				group := keyGroupKey{Dir: path.Dir(filePath), OpType: r.OpType, OpVersion: r.OpVersion, Key: strings.Join(r.Key, "\x00")}
				if prior, ok := keyTypesByGroup[group]; ok {
					if !equalKeyTypes(prior, r.KeyTypes) {
						return fmt.Errorf("spec: %s field %q declares key_types %v, disagreeing with sibling rule(s) on key %v under (%s, %d): %v",
							filePath, r.Field, r.KeyTypes, r.Key, r.OpType, r.OpVersion, prior)
					}
				} else {
					keyTypesByGroup[group] = r.KeyTypes
				}
			}

			r.Vocabulary = vocab
			allRules = append(allRules, r)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("spec: loading field rules: %w", err)
	}

	return allRules, nil
}

// equalKeyTypes reports whether a and b declare the same key column to
// value-type mapping, order-independent.
func equalKeyTypes(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
