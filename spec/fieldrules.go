package spec

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
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
	// ObjectType is derived from Vocabulary via bootstrapObjectTypes, not
	// serialized: the log's define-field already carries a `type` field, and
	// this is that same association, made reachable for the fold matching
	// layer to scope rule matching by object type (spec/fold.md §5) without
	// changing the normative field-rules.json or define-field wire shape.
	ObjectType string `json:"-"`
}

// TargetKey returns Target if non-empty, otherwise Field.
func (r FieldRule) TargetKey() string {
	if r.Target != "" {
		return r.Target
	}
	return r.Field
}

// fieldRuleOrderLess is the canonical rule order spec/fold.md §5 requires
// two rules bound to one target to contribute in: ascending op_type (code
// unit order), then op_version, then field. Every component is rule content
// a reader of the schema can derive for itself, which is what makes two
// independent implementations fold the same log to the same state when one
// operation writes two fields sharing a target.
func fieldRuleOrderLess(a, b FieldRule) bool {
	if a.OpType != b.OpType {
		return a.OpType < b.OpType
	}
	if a.OpVersion != b.OpVersion {
		return a.OpVersion < b.OpVersion
	}
	return a.Field < b.Field
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

// bootstrapObjectTypes maps a field-rules.json directory (FieldRule.Vocabulary,
// the testdata/ basename) to the object type whose ops it declares rules for.
// The corpus ships exactly one such table — `schema` is writ's one hard-coded
// vocabulary, and its rule table is the only one that does not come from the
// log (spec/schema-ops.md §Bootstrap) — but the association is still a map,
// because the directory name and the object type it covers are not the same
// string and nothing else records which is which.
var bootstrapObjectTypes = map[string]string{
	"schema-ops": "schema",
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
	// objectTypeDirs asserts bootstrapObjectTypes is injective: it records
	// the first directory seen claiming each object type, so a second
	// directory mapped to that same object type is caught here rather than
	// silently sharing a target-collision universe with the first one
	// unchecked (targetBindings below is fresh per file — see its comment).
	objectTypeDirs := make(map[string]string)

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
		// A field-rules.json directory with no bootstrapObjectTypes entry
		// fails closed here rather than deriving ObjectType "" — which would
		// match every object type in fold's rule matching (spec/fold.md §5)
		// and silently disable scoping for this vocabulary's rules.
		objectType, ok := bootstrapObjectTypes[vocab]
		if !ok {
			return fmt.Errorf("spec: %s: directory %q has no entry in bootstrapObjectTypes; add one mapping %q to the object type its ops declare rules for", filePath, vocab, vocab)
		}
		// Two directories claiming the same object type would let their
		// rules collide across the file boundary that targetBindings (below)
		// assumes separates distinct object types — assert the map stays
		// one directory per object type.
		if priorDir, ok := objectTypeDirs[objectType]; ok && priorDir != vocab {
			return fmt.Errorf("spec: bootstrapObjectTypes maps both %q and %q to object type %q; each object type must have exactly one field-rules.json directory", priorDir, vocab, objectType)
		}
		objectTypeDirs[objectType] = vocab
		// targetBindings is fresh per file: each field-rules.json directory
		// declares the rule table for exactly one object type, so a target
		// collision is only ever checked within one file's rules, never
		// across directories — where two object types both declaring a
		// "title" target is unrelated and fine. The injectivity check above
		// is what keeps that assumption true: it rejects a second directory
		// sharing an object type before any of its rules could bypass this
		// check unseen.
		targetBindings := make(map[string][]FieldRule)
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

			if err := CheckTargetCollision(targetBindings, r); err != nil {
				return fmt.Errorf("spec: %s %w", filePath, err)
			}
			targetBindings[r.TargetKey()] = append(targetBindings[r.TargetKey()], r)

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
			r.ObjectType = objectType
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

// CheckTargetCollision enforces spec/fold.md §5's and spec/schema-ops.md §8's
// shared-target agreement rule for one candidate rule against bound, every
// rule already accepted within the same object type for the candidate's
// target (TargetKey()) — not merely the most recently accepted one: rules
// sharing a target MUST always agree on Strategy and on Lattice, and —
// unless they are an op_version bump of the same (op_type, field), which
// those sections permit to freely change everything else — MUST also agree
// on ValueType, Key, KeyTypes, Enum and MaxLength. Lattice is held to
// agreement even across a version bump: unlike the other five, it is
// consulted by the strategy at fold time, so two same-strategy rules
// sharing a target that disagree on it are exactly as order-dependent as
// two that disagree on strategy.
//
// The rule is set-level, so this check is too: comparing a candidate only
// against the last-bound rule let a version-bump carve-out against a
// *middle* rule rebind the target, after which a *later* candidate was
// compared only to the rebound rule and never caught disagreeing with the
// *first* — the same order-dependence hazard WRIT-186 named for accumulator
// instantiation ("two conforming implementations that list rules
// differently would disagree"), reintroduced here in the validator meant to
// prevent it. Checking every bound rule closes that hole regardless of
// declaration order.
//
// It is the one check all three sites that resolve field rules run, so
// writ's own hand-written Go tables are held to the exact standard writ
// imposes on writ.schema authors: spec.FieldRules below (this package's own
// tables), engine/schemasrc/compile.go's checkTargetCollision (a writ.schema
// file, at compile time) and engine/schema.go's RulesFromSchemas (the same
// schema, resolved from the log). It returns a descriptive error naming the
// disagreement, or nil when candidate does not collide — including when
// nothing is yet bound to its target. It does not mutate bound; the caller
// owns recording the accepted rule once it decides to keep it.
func CheckTargetCollision(bound map[string][]FieldRule, candidate FieldRule) error {
	targetKey := candidate.TargetKey()
	for _, prior := range bound[targetKey] {
		if prior.Strategy != candidate.Strategy {
			return fmt.Errorf(
				"field rule (%s, %d, %s) reuses target %q already bound to strategy %q with a different strategy %q; a version bump that changes strategy must declare a distinct target",
				candidate.OpType, candidate.OpVersion, candidate.Field, targetKey, prior.Strategy, candidate.Strategy)
		}
		// An op_version bump of the same (op_type, field) MAY freely change
		// value_type, key, key_types, enum and max_length (spec/schema-ops.md
		// §8) — that carve-out applies only against this specific prior, and
		// no wider: every other pair here shares a target across a different
		// op_type or field and must still agree. It does not exempt lattice:
		// equalMergeAttrs still runs, told which attributes this pair is a
		// version bump of one another so it can skip only the genuinely
		// freely-changeable ones and still hold lattice to agreement.
		versionBump := prior.OpType == candidate.OpType && prior.Field == candidate.Field
		if !equalMergeAttrs(prior, candidate, versionBump) {
			if versionBump {
				return fmt.Errorf(
					"field rule (%s, %d, %s) is a version bump of (%s, %d, %s) sharing target %q, but they disagree on lattice; a version bump MAY freely change value_type, key, key_types, enum and max_length but MUST still agree on lattice, which is consulted by the strategy at fold time (spec/schema-ops.md §8)",
					candidate.OpType, candidate.OpVersion, candidate.Field, prior.OpType, prior.OpVersion, prior.Field, targetKey)
			}
			return fmt.Errorf(
				"field rule (%s, %d, %s) reuses target %q already bound by (%s, %d, %s), but they disagree on value_type, key, key_types, enum, max_length or lattice; rules sharing a target across different op_types or fields must agree on every merge attribute (spec/fold.md §5)",
				candidate.OpType, candidate.OpVersion, candidate.Field, targetKey, prior.OpType, prior.OpVersion, prior.Field)
		}
	}
	return nil
}

// equalMergeAttrs reports whether a and b agree on the merge attributes
// CheckTargetCollision holds a target share to. Strategy is checked
// separately by the caller, and op_type/field/op_version identify the rule
// rather than describe its merge behaviour, so neither belongs here.
//
// versionBump reports whether a and b are an op_version bump of the same
// (op_type, field) — the one case spec/schema-ops.md §8 lets change
// value_type, key, key_types, enum and max_length freely, so those five are
// skipped when it is true. lattice is never skipped, version bump or not:
// unlike the other five, it is consulted by the strategy at fold time — the
// lattice accumulator (engine/internal/fold/strategy.go, spec/reffold.go)
// reads it to order its semilattice — so two rules sharing a target that
// disagree on it are exactly as order-dependent as two that disagree on
// strategy, whether or not they are a version bump of one another.
func equalMergeAttrs(a, b FieldRule, versionBump bool) bool {
	if !versionBump {
		if a.ValueType != b.ValueType ||
			!slices.Equal(a.Key, b.Key) ||
			!equalKeyTypes(a.KeyTypes, b.KeyTypes) ||
			!slices.Equal(a.Enum, b.Enum) ||
			a.MaxLength != b.MaxLength {
			return false
		}
	}
	return slices.Equal(a.Lattice, b.Lattice)
}
