package spec

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"sort"
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

		// The agreement check is set-level (CheckTargetAgreement's doc), so
		// it runs once per target after every rule in the file is bound,
		// not incrementally as each rule arrives: sorting the target keys
		// here only orders which of possibly several bad targets is
		// reported first, never whether one is found, since every target
		// is still checked.
		targetKeys := make([]string, 0, len(targetBindings))
		for tk := range targetBindings {
			targetKeys = append(targetKeys, tk)
		}
		sort.Strings(targetKeys)
		for _, tk := range targetKeys {
			if err := CheckTargetAgreement(tk, targetBindings[tk]); err != nil {
				return fmt.Errorf("spec: %s %w", filePath, err)
			}
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

// versionBumpClass identifies the equivalence class CheckTargetAgreement
// partitions a target's rules into: every rule sharing one (OpType, Field)
// is a version bump of every other, whatever OpVersion each declares.
// Grouping by a key is an equivalence relation by construction — reflexive,
// symmetric and transitive — which is the whole fix WRIT-211 needed: the
// superseded incremental check tested "is candidate a version bump of this
// specific prior" pairwise, so whether a middle rule's carve-out applied
// depended on which prior it happened to be compared against first. A
// class membership test has no such dependency.
type versionBumpClass struct {
	OpType string
	Field  string
}

// TargetDisagreement is the first pair of rules bound to one target that
// fail spec/fold.md §5's / spec/schema-ops.md §8's shared-target agreement
// relation, in canonical rule order (fieldRuleOrderLess): A sorts before B.
// Attribute names what they disagree on ("strategy", "lattice",
// "value_type", "key", "key_types", "enum" or "max_length").
//
// It carries no formatted message of its own — CheckTargetAgreement builds
// that — precisely so a caller that tracks each rule's source position
// (engine/schemasrc/compile.go's checkTargetAgreement) can use B, the
// later-declared rule, to report where the disagreement actually surfaces
// (spec/schema-ops.md §8), without parsing an error string apart to find it.
type TargetDisagreement struct {
	Target    string
	Attribute string
	A, B      FieldRule
}

// FindTargetDisagreement runs the scan CheckTargetAgreement wraps into an
// error: see that function's doc for the relation itself. ok is false when
// rules holds fewer than two entries or every rule agrees.
func FindTargetDisagreement(target string, rules []FieldRule) (d TargetDisagreement, ok bool) {
	if len(rules) < 2 {
		return TargetDisagreement{}, false
	}

	sorted := append([]FieldRule(nil), rules...)
	sort.Slice(sorted, func(i, j int) bool { return fieldRuleOrderLess(sorted[i], sorted[j]) })

	classes := make(map[versionBumpClass]bool, len(sorted))
	for _, r := range sorted {
		classes[versionBumpClass{OpType: r.OpType, Field: r.Field}] = true
	}
	singleClass := len(classes) == 1

	for i := 1; i < len(sorted); i++ {
		a, b := sorted[i-1], sorted[i]
		if a.Strategy != b.Strategy {
			return TargetDisagreement{Target: target, Attribute: "strategy", A: a, B: b}, true
		}
		if !slices.Equal(a.Lattice, b.Lattice) {
			return TargetDisagreement{Target: target, Attribute: "lattice", A: a, B: b}, true
		}
		if singleClass {
			continue
		}
		if a.ValueType != b.ValueType {
			return TargetDisagreement{Target: target, Attribute: "value_type", A: a, B: b}, true
		}
		if !slices.Equal(a.Key, b.Key) {
			return TargetDisagreement{Target: target, Attribute: "key", A: a, B: b}, true
		}
		if !equalKeyTypes(a.KeyTypes, b.KeyTypes) {
			return TargetDisagreement{Target: target, Attribute: "key_types", A: a, B: b}, true
		}
		if !slices.Equal(a.Enum, b.Enum) {
			return TargetDisagreement{Target: target, Attribute: "enum", A: a, B: b}, true
		}
		if a.MaxLength != b.MaxLength {
			return TargetDisagreement{Target: target, Attribute: "max_length", A: a, B: b}, true
		}
	}
	return TargetDisagreement{}, false
}

// CheckTargetAgreement enforces spec/fold.md §5's and spec/schema-ops.md §8's
// shared-target agreement relation over rules, every rule bound to one
// target (TargetKey()) within one object type. The relation is stated as a
// genuine equivalence relation, which is what WRIT-211 changed:
//
//  1. Partition rules into versionBumpClass groups — every rule sharing one
//     (OpType, Field) is one class, whatever OpVersion each declares.
//  2. Within a class, rules MUST agree on Strategy and Lattice; they MAY
//     freely differ on ValueType, Key, KeyTypes, Enum and MaxLength (§8's
//     version-bump carve-out).
//  3. The moment the target is bound by more than one class, that carve-out
//     is gone for every rule bound to it, not only the rules straddling two
//     classes: a class internally non-uniform on an attribute cannot agree
//     with any other class on it, so the exemption vanishing wholesale is
//     the carve-out's transitive closure, not an extra rule. Every rule
//     bound to the target — within a class or across classes — must then
//     agree on all seven attributes: Strategy, Lattice, ValueType, Key,
//     KeyTypes, Enum and MaxLength.
//
// Because every comparison is plain value equality — itself an equivalence
// relation — agreement across the whole set holds iff every *consecutive*
// pair agrees once rules is sorted into canonical order (fieldRuleOrderLess,
// spec/fold.md §5's "Canonical rule order"): if a = b and b = c then a = c,
// so checking every pair would only ever find the same violation a
// consecutive scan already would (FindTargetDisagreement above does exactly
// that scan). Sorting first is also what makes the reported pair — and so
// the error message — independent of the order rules happened to be
// supplied in, closing the exact hazard the superseded incremental,
// insertion-order check left open (WRIT-211): which pair a violation gets
// reported against no longer depends on which rule a caller happened to
// bind first.
//
// It returns nil when rules holds fewer than two entries or every rule
// agrees, and a descriptive error naming the first disagreeing consecutive
// pair (in canonical order) and the attribute otherwise. It is the one
// check all three sites that resolve field rules run: spec.FieldRules below
// (this package's own tables), engine/schemasrc/compile.go's
// checkTargetAgreement (a writ.schema file, at compile time), and
// engine/schema.go's resolveSchemaTypes (the same schema, resolved from the
// log) — so writ's own hand-written Go tables are held to the exact standard
// writ imposes on writ.schema authors.
func CheckTargetAgreement(target string, rules []FieldRule) error {
	d, ok := FindTargetDisagreement(target, rules)
	if !ok {
		return nil
	}

	switch d.Attribute {
	case "strategy":
		return fmt.Errorf(
			"field rule (%s, %d, %s) reuses target %q already bound by (%s, %d, %s), but they disagree on strategy (%q vs %q); a version bump that changes strategy must declare a distinct target",
			d.B.OpType, d.B.OpVersion, d.B.Field, d.Target, d.A.OpType, d.A.OpVersion, d.A.Field, d.A.Strategy, d.B.Strategy)
	case "lattice":
		return fmt.Errorf(
			"field rule (%s, %d, %s) reuses target %q already bound by (%s, %d, %s), but they disagree on lattice (%v vs %v); lattice is consulted by the strategy at fold time, so rules sharing a target MUST agree on it even across a version bump of the same (op_type, field) (spec/schema-ops.md §8)",
			d.B.OpType, d.B.OpVersion, d.B.Field, d.Target, d.A.OpType, d.A.OpVersion, d.A.Field, d.A.Lattice, d.B.Lattice)
	default:
		return fmt.Errorf(
			"field rule (%s, %d, %s) reuses target %q already bound by (%s, %d, %s), but they disagree on %s; the target is shared by more than one (op_type, field) version-bump class, so the version-bump carve-out for value_type, key, key_types, enum and max_length applies only within a class, not between them (spec/schema-ops.md §8) — every rule sharing this target must agree on %s",
			d.B.OpType, d.B.OpVersion, d.B.Field, d.Target, d.A.OpType, d.A.OpVersion, d.A.Field, d.Attribute, d.Attribute)
	}
}

// CheckKeyColumnCollision enforces that every keyed-lww rule for one
// (op_type, op_version) which names a given key column agrees on that
// column's key_types entry, regardless of whether the two rules share a
// full key tuple or even a target: engine/codec/schema.go's
// validateFieldsAgainstRules (spec/op-envelope.md §Producer validation
// rule 3) resolves a key column's declared type by column name alone,
// scanning every keyed-lww rule declared for the (op_type, op_version) a
// body targets, not by which rule's key the column happens to belong to.
// Two rules that disagree give a producer no correct way to check the
// column's value — whichever rule's entry validateFieldsAgainstRules
// happens to see last would silently govern the other's column too — so
// this is the same "no winner is ever picked" standard
// CheckTargetCollision already holds shared targets to
// (spec/schema-ops.md §8), applied to shared key columns instead: the
// second rule to declare a disagreeing entry for an already-bound column
// is rejected outright, not silently reconciled by whichever happens to
// be resolved first or last.
//
// bound maps key column name to the keyed-lww rule that already bound it,
// scoped by the caller to one (op_type, op_version) — CheckTargetCollision
// is scoped by target instead, because a target collision spans every
// op_type sharing it, but a key column is only ever resolved within one
// (op_type, op_version)'s rule set (validateFieldsAgainstRules never sees
// more than that). candidate not being keyed-lww, or naming no column
// already in bound, is not a collision: it returns nil. It does not
// mutate bound; the caller owns recording candidate's columns once it
// decides to keep it.
func CheckKeyColumnCollision(bound map[string]FieldRule, candidate FieldRule) error {
	if candidate.Strategy != "keyed-lww" {
		return nil
	}
	cols := make([]string, 0, len(candidate.KeyTypes))
	for col := range candidate.KeyTypes {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	for _, col := range cols {
		prior, ok := bound[col]
		if !ok {
			continue
		}
		if prior.KeyTypes[col] != candidate.KeyTypes[col] {
			return fmt.Errorf(
				"field rule (%s, %d, %s) declares key column %q as %q, disagreeing with field %q's rule for the same (op_type, op_version), which declares it %q; every keyed-lww rule sharing a key column within one (op_type, op_version) must agree on that column's key_types entry, because a producer resolves a key column's declared type by column name alone (spec/op-envelope.md §Producer validation rule 3)",
				candidate.OpType, candidate.OpVersion, candidate.Field, col, candidate.KeyTypes[col], prior.Field, prior.KeyTypes[col])
		}
	}
	return nil
}
