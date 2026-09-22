package spec_test

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/writtendev/writ/spec"
)

func TestFieldRulesSchemaValidation(t *testing.T) {
	c := jsonschema.NewCompiler()
	raw, err := spec.FS.ReadFile("schemas/field-rules.schema.json")
	if err != nil {
		t.Fatalf("reading field-rules.schema.json: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("unmarshaling schema: %v", err)
	}
	if err := c.AddResource("https://writ.dev/spec/field-rules.schema.json", doc); err != nil {
		t.Fatalf("adding schema resource: %v", err)
	}
	sch, err := c.Compile("https://writ.dev/spec/field-rules.schema.json")
	if err != nil {
		t.Fatalf("compiling schema: %v", err)
	}

	files := []string{
		"testdata/schema-ops/field-rules.json",
	}

	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			rawFile, err := spec.FS.ReadFile(file)
			if err != nil {
				t.Fatalf("reading %s: %v", file, err)
			}
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawFile))
			if err != nil {
				t.Fatalf("unmarshaling %s: %v", file, err)
			}
			if err := sch.Validate(inst); err != nil {
				t.Errorf("schema validation failed for %s: %v", file, err)
			}
		})
	}
}

// fieldRuleValidationCases is package-level so both TestValidateFieldRule
// (which runs each case) and TestValidateFieldRuleSentinelCoverage (which
// checks every sentinel is named by at least one case) share the one table
// rather than each keeping its own copy of which case exercises what.
var fieldRuleValidationCases = []struct {
	name string
	rule spec.FieldRule
	// wantSentinel is the token spec.FieldRuleSentinels is keyed by
	// (spec/fieldrules.go), naming the one violation branch this case must
	// trip -- empty means ValidateFieldRule must accept the rule. Checking
	// sentinel identity (errors.Is) rather than a message substring is what
	// TestValidateFieldRuleSentinelCoverage below can hold to account: every
	// sentinel in the table must be named by at least one case here.
	wantSentinel string
}{
	{
		name: "valid scalar lww with person-ref value type",
		rule: spec.FieldRule{
			OpType:    "resolve",
			OpVersion: 1,
			Field:     "resolved_by",
			Strategy:  "lww",
			ValueType: "person-ref",
		},
	},
	{
		name: "valid set-observed-remove with person-ref item type",
		rule: spec.FieldRule{
			OpType:    "assign",
			OpVersion: 1,
			Field:     "add",
			Strategy:  "set-observed-remove",
			ValueType: "person-ref",
		},
	},
	{
		name: "valid keyed-lww with person-ref key and value type",
		rule: spec.FieldRule{
			OpType:    "approval",
			OpVersion: 1,
			Field:     "subject",
			Strategy:  "keyed-lww",
			Key:       []string{"subject", "revision"},
			ValueType: "person-ref",
			KeyTypes:  map[string]string{"subject": "person-ref", "revision": "git-oid"},
		},
	},
	{
		name: "untyped rule (no value_type) is legal",
		rule: spec.FieldRule{
			OpType:    "create",
			OpVersion: 1,
			Field:     "subject",
			Strategy:  "create-once",
		},
	},
	{
		name: "empty op_type",
		rule: spec.FieldRule{
			OpType:    "",
			OpVersion: 1,
			Field:     "title",
			Strategy:  "lww",
			ValueType: "string",
		},
		wantSentinel: "empty-op-type",
	},
	{
		name: "op_version below 1",
		rule: spec.FieldRule{
			OpType:    "create",
			OpVersion: 0,
			Field:     "title",
			Strategy:  "lww",
			ValueType: "string",
		},
		wantSentinel: "invalid-op-version",
	},
	{
		name: "empty field",
		rule: spec.FieldRule{
			OpType:    "create",
			OpVersion: 1,
			Field:     "",
			Strategy:  "lww",
			ValueType: "string",
		},
		wantSentinel: "empty-field",
	},
	{
		name: "unknown value_type",
		rule: spec.FieldRule{
			OpType:    "create",
			OpVersion: 1,
			Field:     "title",
			Strategy:  "lww",
			ValueType: "unknown",
		},
		wantSentinel: "unknown-value-type",
	},
	{
		name: "unknown strategy",
		rule: spec.FieldRule{
			OpType:    "create",
			OpVersion: 1,
			Field:     "title",
			Strategy:  "counter",
			ValueType: "string",
		},
		wantSentinel: "unknown-strategy",
	},
	{
		name: "keyed-lww declares no key",
		rule: spec.FieldRule{
			OpType:    "approval",
			OpVersion: 1,
			Field:     "verdict",
			Strategy:  "keyed-lww",
			ValueType: "person-ref",
		},
		wantSentinel: "keyed-lww-no-key",
	},
	{
		name: "lattice defines no elements",
		rule: spec.FieldRule{
			OpType:    "ci-status",
			OpVersion: 1,
			Field:     "state",
			Strategy:  "lattice",
			ValueType: "enum",
			Enum:      []string{"pending", "success"},
		},
		wantSentinel: "lattice-no-elements",
	},
	{
		name: "enum value_type with no enum values",
		rule: spec.FieldRule{
			OpType:    "set-status",
			OpVersion: 1,
			Field:     "status",
			Strategy:  "lww",
			ValueType: "enum",
		},
		wantSentinel: "enum-no-values",
	},
	{
		name: "enum values on a non-enum value_type",
		rule: spec.FieldRule{
			OpType:    "create",
			OpVersion: 1,
			Field:     "title",
			Strategy:  "lww",
			ValueType: "string",
			Enum:      []string{"a", "b"},
		},
		wantSentinel: "enum-on-non-enum",
	},
	{
		name: "max_length on a non-string, non-text value_type",
		rule: spec.FieldRule{
			OpType:    "create",
			OpVersion: 1,
			Field:     "priority",
			Strategy:  "lww",
			ValueType: "int",
			MaxLength: 10,
		},
		wantSentinel: "max-length-value-type",
	},
	{
		name: "tombstone with a value_type other than bool",
		rule: spec.FieldRule{
			OpType:    "delete",
			OpVersion: 1,
			Field:     "deleted",
			Strategy:  "tombstone",
			ValueType: "string",
		},
		wantSentinel: "tombstone-value-type",
	},
	{
		name: "lattice with a value_type other than enum",
		rule: spec.FieldRule{
			OpType:    "ci-status",
			OpVersion: 1,
			Field:     "state",
			Strategy:  "lattice",
			Lattice:   []string{"pending", "success"},
			ValueType: "string",
		},
		wantSentinel: "lattice-value-type",
	},
	{
		name: "lattice elements not a subset of its enum",
		rule: spec.FieldRule{
			OpType:    "ci-status",
			OpVersion: 1,
			Field:     "state",
			Strategy:  "lattice",
			Lattice:   []string{"pending", "success"},
			ValueType: "enum",
			Enum:      []string{"pending", "failure"},
		},
		wantSentinel: "lattice-element-not-in-enum",
	},
	{
		name: "keyed-lww key_types missing a key column",
		rule: spec.FieldRule{
			OpType:    "approval",
			OpVersion: 1,
			Field:     "subject",
			Strategy:  "keyed-lww",
			Key:       []string{"subject", "revision"},
			ValueType: "person-ref",
			KeyTypes:  map[string]string{"subject": "person-ref"},
		},
		wantSentinel: "key-types-count",
	},
	{
		name: "keyed-lww key_types covering a column outside key",
		rule: spec.FieldRule{
			OpType:    "approval",
			OpVersion: 1,
			Field:     "subject",
			Strategy:  "keyed-lww",
			Key:       []string{"subject"},
			ValueType: "person-ref",
			KeyTypes:  map[string]string{"subject": "person-ref", "revision": "git-oid"},
		},
		wantSentinel: "key-types-count",
	},
	{
		// Distinct from the two key-types-count cases above: there the
		// *count* of KeyTypes disagrees with the count of Key. Here the
		// counts agree (both 2), so that check passes, but one Key entry
		// ("revision") has no matching KeyTypes entry -- the per-column
		// lookup is what actually fires.
		name: "keyed-lww key_types has the right count but the wrong column",
		rule: spec.FieldRule{
			OpType:    "approval",
			OpVersion: 1,
			Field:     "subject",
			Strategy:  "keyed-lww",
			Key:       []string{"subject", "revision"},
			ValueType: "person-ref",
			KeyTypes:  map[string]string{"subject": "person-ref", "other": "git-oid"},
		},
		wantSentinel: "key-types-missing-column",
	},
	{
		name: "keyed-lww key_types names an unknown value_type for a column",
		rule: spec.FieldRule{
			OpType:    "approval",
			OpVersion: 1,
			Field:     "subject",
			Strategy:  "keyed-lww",
			Key:       []string{"subject"},
			ValueType: "person-ref",
			KeyTypes:  map[string]string{"subject": "bogus"},
		},
		wantSentinel: "key-types-unknown-value-type",
	},
	{
		name: "key_types on a non-keyed-lww strategy",
		rule: spec.FieldRule{
			OpType:    "create",
			OpVersion: 1,
			Field:     "title",
			Strategy:  "lww",
			ValueType: "string",
			KeyTypes:  map[string]string{"subject": "person-ref"},
		},
		wantSentinel: "key-types-non-keyed-lww",
	},
	{
		// field, target, and key columns share one identifier grammar
		// (WRIT-203, spec/schema-ops.md §4.3): a schema-declared target
		// or key component becomes a generated SQL identifier once a
		// consumer's projection reads it, exactly as field already does.
		name: "field is not a valid identifier",
		rule: spec.FieldRule{
			OpType:    "create",
			OpVersion: 1,
			Field:     "Title",
			Strategy:  "lww",
			ValueType: "string",
		},
		wantSentinel: "invalid-field-identifier",
	},
	{
		name: "field exceeds the identifier length limit",
		rule: spec.FieldRule{
			OpType:    "create",
			OpVersion: 1,
			Field:     "a" + strings.Repeat("b", 64),
			Strategy:  "lww",
			ValueType: "string",
		},
		wantSentinel: "invalid-field-identifier",
	},
	{
		name: "target is not a valid identifier",
		rule: spec.FieldRule{
			OpType:    "create",
			OpVersion: 1,
			Field:     "title",
			Target:    "bad-target",
			Strategy:  "lww",
			ValueType: "string",
		},
		wantSentinel: "invalid-target-identifier",
	},
	{
		name: "empty target is legal (defaults to field via TargetKey)",
		rule: spec.FieldRule{
			OpType:    "create",
			OpVersion: 1,
			Field:     "title",
			Target:    "",
			Strategy:  "lww",
			ValueType: "string",
		},
	},
	{
		name: "keyed-lww key column is not a valid identifier",
		rule: spec.FieldRule{
			OpType:    "approval",
			OpVersion: 1,
			Field:     "subject",
			Strategy:  "keyed-lww",
			Key:       []string{"Subject"},
			ValueType: "person-ref",
			KeyTypes:  map[string]string{"Subject": "person-ref"},
		},
		wantSentinel: "invalid-key-identifier",
	},
}

func TestValidateFieldRule(t *testing.T) {
	for _, tc := range fieldRuleValidationCases {
		t.Run(tc.name, func(t *testing.T) {
			err := spec.ValidateFieldRule(tc.rule)
			if tc.wantSentinel == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error wrapping sentinel %q, got nil", tc.wantSentinel)
			}
			sentinel, ok := spec.FieldRuleSentinels[tc.wantSentinel]
			if !ok {
				t.Fatalf("test bug: %q is not a key of spec.FieldRuleSentinels", tc.wantSentinel)
			}
			if !errors.Is(err, sentinel) {
				t.Fatalf("error %q does not wrap sentinel %q", err, tc.wantSentinel)
			}
		})
	}
}

// TestValidateFieldRuleSentinelCoverage asserts every sentinel
// spec.FieldRuleSentinels names is tripped by at least one case in
// fieldRuleValidationCases above -- the real coverage backstop for
// ValidateFieldRule's violation branches: a branch added to
// spec/fieldrules.go with a new sentinel but no covering case here fails
// this test by name. This is narrower than, and does not replace,
// TestInvalidSchemaOpsVectors' coverage check in schema_ops_test.go: that
// one only requires every token a testdata/schema-ops/invalid/index.json
// entry names to exist in spec.FieldRuleSentinels, which is a strict
// subset -- six of this table's tokens have no conformance vector at all
// (see the declared inventory in schema_ops_test.go) and are covered only
// here.
func TestValidateFieldRuleSentinelCoverage(t *testing.T) {
	named := make(map[string]bool)
	for _, tc := range fieldRuleValidationCases {
		if tc.wantSentinel != "" {
			named[tc.wantSentinel] = true
		}
	}
	for token := range spec.FieldRuleSentinels {
		if !named[token] {
			t.Errorf("no case in fieldRuleValidationCases names sentinel token %q; it is untested", token)
		}
	}
}

func TestFieldRuleTargetKey(t *testing.T) {
	r1 := spec.FieldRule{Field: "description"}
	if got := r1.TargetKey(); got != "description" {
		t.Fatalf("expected Field 'description', got %q", got)
	}
	r2 := spec.FieldRule{Field: "description", Target: "ci_description"}
	if got := r2.TargetKey(); got != "ci_description" {
		t.Fatalf("expected Target 'ci_description', got %q", got)
	}
}

// TestCheckTargetAgreement pins WRIT-211's transitive shared-target
// agreement relation (spec/fold.md §5, spec/schema-ops.md §8): rules
// sharing a TargetKey must always agree on Strategy, Lattice, Key and
// KeyTypes, and — unless every one of them belongs to the same (op_type,
// field) version-bump equivalence class — must also agree on ValueType,
// Enum and MaxLength. Lattice is held to agreement even within a single
// class — WRIT-206, pinned below by the "different lattice ordering"
// cases both cross-op_type and within a version bump — because, unlike
// the remaining three, it is consulted by the strategy at fold time: two
// same-strategy lattice rules sharing a target but declaring different
// orderings are exactly as order-dependent as two rules disagreeing on
// strategy itself. Key and KeyTypes are likewise held to agreement even
// within a single class — WRIT-234, pinned below by the "different key
// arity" and "different key_types" cases — for a different reason: a key
// tuple is the keyed-lww register's identity, not what it holds, so a
// version bump that changes either splits one register into two under a
// target the fold still treats as one.
func TestCheckTargetAgreement(t *testing.T) {
	tests := []struct {
		name    string
		rules   []spec.FieldRule
		wantErr bool
	}{
		{
			name: "different strategy, same target: rejected",
			rules: []spec.FieldRule{
				{OpType: "create", Field: "priority", Strategy: "lww", ValueType: "string"},
				{OpType: "create", OpVersion: 2, Field: "priority", Strategy: "lattice", ValueType: "string"},
			},
			wantErr: true,
		},
		{
			name: "same strategy, different value_type, cross op_type: rejected",
			rules: []spec.FieldRule{
				{OpType: "link-person", Field: "add", Strategy: "set-observed-remove", ValueType: "person-ref"},
				{OpType: "link-object", Field: "add", Strategy: "set-observed-remove", ValueType: "object-ref"},
			},
			wantErr: true,
		},
		{
			name: "same strategy, different key, cross field: rejected",
			rules: []spec.FieldRule{
				{OpType: "approval", Field: "revision", Strategy: "keyed-lww", Key: []string{"subject", "revision"}},
				{OpType: "ci-status", Field: "revision", Strategy: "keyed-lww", Key: []string{"revision", "name"}},
			},
			wantErr: true,
		},
		{
			name: "same strategy, different lattice ordering, cross op_type: rejected",
			rules: []spec.FieldRule{
				{OpType: "promote", Field: "level", Target: "level", Strategy: "lattice", Lattice: []string{"low", "high"}},
				{OpType: "demote", Field: "level", Target: "level", Strategy: "lattice", Lattice: []string{"high", "low"}},
			},
			wantErr: true,
		},
		{
			name: "version bump, same op_type and field, different value_type: permitted",
			rules: []spec.FieldRule{
				{OpType: "widget-op", OpVersion: 1, Field: "value", Strategy: "lww", ValueType: "string"},
				{OpType: "widget-op", OpVersion: 2, Field: "value", Strategy: "lww", ValueType: "enum", Enum: []string{"draft", "approved"}},
			},
			wantErr: false,
		},
		{
			// WRIT-206: the version-bump carve-out used to exempt lattice
			// too, so this pair — same (op_type, field), same strategy,
			// disagreeing only on lattice ordering — was wrongly permitted.
			// Lattice is not on spec/schema-ops.md §8's "MAY freely change"
			// list, so a version bump must still agree on it.
			name: "version bump, same op_type and field, different lattice ordering: rejected",
			rules: []spec.FieldRule{
				{OpType: "promote", OpVersion: 1, Field: "level", Strategy: "lattice", Lattice: []string{"low", "high"}},
				{OpType: "promote", OpVersion: 2, Field: "level", Strategy: "lattice", Lattice: []string{"high", "low"}},
			},
			wantErr: true,
		},
		{
			// WRIT-234: key and key_types used to be on §8's "MAY freely
			// change" list, the same as value_type; the ruling closed that
			// carve-out because a key tuple is the keyed-lww register's
			// identity, not what it holds, so a version bump that changes
			// key arity must declare a distinct target instead.
			name: "version bump, same op_type and field, different key arity: rejected",
			rules: []spec.FieldRule{
				{OpType: "approval", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww", Key: []string{"subject", "revision"}},
				{OpType: "approval", OpVersion: 2, Field: "verdict", Strategy: "keyed-lww", Key: []string{"subject"}},
			},
			wantErr: true,
		},
		{
			name: "version bump, same op_type and field, different key_types: rejected",
			rules: []spec.FieldRule{
				{OpType: "approval", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww", Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "string"}},
				{OpType: "approval", OpVersion: 2, Field: "verdict", Strategy: "keyed-lww", Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"}},
			},
			wantErr: true,
		},
		{
			name: "version bump, same op_type and field, same lattice, different value_type: permitted",
			rules: []spec.FieldRule{
				{OpType: "promote", OpVersion: 1, Field: "level", Strategy: "lattice", ValueType: "enum", Enum: []string{"low", "high"}, Lattice: []string{"low", "high"}},
				{OpType: "promote", OpVersion: 2, Field: "level", Strategy: "lattice", ValueType: "enum", Enum: []string{"low", "medium", "high"}, Lattice: []string{"low", "high"}},
			},
			wantErr: false,
		},
		{
			name: "version bump, same op_type and field, strategy changes: still rejected",
			rules: []spec.FieldRule{
				{OpType: "widget-op", OpVersion: 1, Field: "value", Strategy: "lww", ValueType: "string"},
				{OpType: "widget-op", OpVersion: 2, Field: "value", Strategy: "set-union", ValueType: "string"},
			},
			wantErr: true,
		},
		{
			name: "cross op_type, everything agrees: permitted (the 28 deliberate shares)",
			rules: []spec.FieldRule{
				{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string"},
				{OpType: "update", Field: "title", Strategy: "lww", ValueType: "string"},
			},
			wantErr: false,
		},
		{
			// WRIT-211's own three-rule vector: (X, f, v1, string) and
			// (X, f, v2, int) are a version-bump class of one another (free
			// to disagree on value_type in isolation), but a third rule
			// (Y, g) shares their target and is not a version bump of
			// either. The old pairwise, candidate-vs-bound form dropped a
			// different one of the three depending on which order they were
			// considered in; the transitive relation must reject the whole
			// set regardless of the order rules are supplied in, because
			// the target is bound by more than one (op_type, field) class
			// and the version-bump exemption is void for every rule on it,
			// including the pair that would have been permitted alone.
			name: "three-rule case: a version-bump class plus an unrelated class sharing its target is rejected as a whole",
			rules: []spec.FieldRule{
				{OpType: "reset", Field: "value", Target: "mode", Strategy: "lww", ValueType: "string"},
				{OpType: "configure", OpVersion: 1, Field: "mode", Strategy: "lww", ValueType: "string"},
				{OpType: "configure", OpVersion: 2, Field: "mode", Strategy: "lww", ValueType: "int"},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target := tc.rules[0].TargetKey()
			err := spec.CheckTargetAgreement(target, tc.rules)
			if tc.wantErr && err == nil {
				t.Fatalf("expected an agreement error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
		})
	}
}

// TestCheckKeyColumnAgreement mirrors TestCheckTargetAgreement above for
// the second axis WRIT-214 closed: every rule participating in one key
// column, within one (op_type, op_version), must agree both on that
// column's key_types entry (engine/codec/schema.go's
// validateFieldsAgainstRules resolves a key column's declared type by
// column name alone) and on the JSON shape its value can take (a key
// column's value must be a JSON string, which a dual-role tombstone field
// can never be). The check is set-level, so every case below is stated as
// the whole participating set rather than a candidate against a bound
// prior, and every case is run in both orders: the verdict must be a
// function of the set alone (WRIT-211's standard, applied to this axis by
// WRIT-214 round 5).
func TestCheckKeyColumnAgreement(t *testing.T) {
	tests := []struct {
		name    string
		column  string
		rules   []spec.FieldRule
		wantErr bool
	}{
		{
			// The round-1 reviewer's own repro: "verdict" keyed on
			// key(subject) and "score" keyed on key(subject, phase)
			// individually pass ValidateFieldRule and
			// CheckTargetAgreement (different targets), but disagree on
			// what "subject" is.
			name:   "different key tuples sharing a column name that disagree: rejected",
			column: "subject",
			rules: []spec.FieldRule{
				{
					OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
					Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"},
				},
				{
					OpType: "approve", OpVersion: 1, Field: "score", Strategy: "keyed-lww",
					Key: []string{"subject", "phase"}, KeyTypes: map[string]string{"subject": "string", "phase": "string"},
				},
			},
			wantErr: true,
		},
		{
			name:   "different key tuples sharing a column name that agree: permitted",
			column: "subject",
			rules: []spec.FieldRule{
				{
					OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
					Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"},
				},
				{
					OpType: "approve", OpVersion: 1, Field: "score", Strategy: "keyed-lww",
					Key: []string{"subject", "phase"}, KeyTypes: map[string]string{"subject": "person-ref", "phase": "string"},
				},
			},
			wantErr: false,
		},
		{
			// Three rules, two of which agree: a candidate-vs-bound check
			// installs a different survivor (and a different number of
			// them) depending on which arrives first. The set-level
			// answer is the same either way — the whole column is
			// withheld — which is what running both orders below pins.
			name:   "three rules, one dissenting: the whole column is refused",
			column: "subject",
			rules: []spec.FieldRule{
				{
					OpType: "approve", OpVersion: 1, Field: "aa", Strategy: "keyed-lww",
					Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"},
				},
				{
					OpType: "approve", OpVersion: 1, Field: "mm", Strategy: "keyed-lww",
					Key: []string{"subject", "phase"}, KeyTypes: map[string]string{"subject": "string", "phase": "string"},
				},
				{
					OpType: "approve", OpVersion: 1, Field: "zz", Strategy: "keyed-lww",
					Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"},
				},
			},
			wantErr: true,
		},
		{
			// A non-keyed-lww rule sharing the column's name is the
			// dual-role case: it is part of the participating set, but
			// binds nothing, so agreement on the column's type is
			// unaffected.
			name:   "a dual-role lww field: not a disagreement",
			column: "subject",
			rules: []spec.FieldRule{
				{
					OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
					Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"},
				},
				{OpType: "approve", OpVersion: 1, Field: "subject", Strategy: "lww", ValueType: "string"},
			},
			wantErr: false,
		},
		{
			// The dual-role tombstone WRIT-214 round 5 found: no value
			// ever satisfies both roles, so the combination is refused
			// where the schema resolves rather than on every write.
			name:   "a dual-role tombstone field: rejected",
			column: "flag",
			rules: []spec.FieldRule{
				{
					OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww", ValueType: "string",
					Key: []string{"flag"}, KeyTypes: map[string]string{"flag": "bool"},
				},
				{OpType: "approve", OpVersion: 1, Field: "flag", Strategy: "tombstone", ValueType: "bool"},
			},
			wantErr: true,
		},
		{
			// tombstone's value_type is legally "" as well as "bool"
			// (ValidateFieldRule), and a blank one is refused just the
			// same: the contradiction is between the strategy's reducer
			// and the key-column floor, not between two value types.
			name:   "a dual-role tombstone field with no value_type: rejected",
			column: "flag",
			rules: []spec.FieldRule{
				{
					OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww", ValueType: "string",
					Key: []string{"flag"}, KeyTypes: map[string]string{"flag": "string"},
				},
				{OpType: "approve", OpVersion: 1, Field: "flag", Strategy: "tombstone"},
			},
			wantErr: true,
		},
		{
			// Only one rule participates: a column no other rule names
			// and no field shares a name with is always fine.
			name:   "a single binding rule: never a disagreement",
			column: "phase",
			rules: []spec.FieldRule{
				{
					OpType: "approve", OpVersion: 1, Field: "score", Strategy: "keyed-lww",
					Key: []string{"subject", "phase"}, KeyTypes: map[string]string{"subject": "string", "phase": "string"},
				},
			},
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Both orders, because the verdict must be a function of the
			// set and not of the order a caller happened to collect it in.
			forward := append([]spec.FieldRule(nil), tc.rules...)
			reversed := make([]spec.FieldRule, len(tc.rules))
			for i, r := range tc.rules {
				reversed[len(tc.rules)-1-i] = r
			}
			for _, order := range []struct {
				name  string
				rules []spec.FieldRule
			}{{"declared order", forward}, {"reversed", reversed}} {
				err := spec.CheckKeyColumnAgreement(tc.column, order.rules)
				if tc.wantErr && err == nil {
					t.Fatalf("%s: expected a disagreement error, got nil", order.name)
				}
				if !tc.wantErr && err != nil {
					t.Fatalf("%s: expected agreement, got: %v", order.name, err)
				}
			}
		})
	}
}

// TestKeyColumnsBoundIsSortedAndDeduplicated pins the grouping key both
// callers of CheckKeyColumnAgreement share: the columns a rule set binds,
// each once, in an order that is a function of the set rather than of map
// iteration (WRIT-186).
func TestKeyColumnsBoundIsSortedAndDeduplicated(t *testing.T) {
	rules := []spec.FieldRule{
		{
			OpType: "approve", OpVersion: 1, Field: "score", Strategy: "keyed-lww",
			Key: []string{"subject", "phase"}, KeyTypes: map[string]string{"subject": "string", "phase": "string"},
		},
		{
			OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
			Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "string"},
		},
		{OpType: "approve", OpVersion: 1, Field: "note", Strategy: "lww", ValueType: "string"},
	}
	for i := 0; i < 20; i++ {
		got := spec.KeyColumnsBound(rules)
		want := []string{"phase", "subject"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("KeyColumnsBound = %v, want %v", got, want)
		}
	}
}

// TestFieldRulesNoUndeclaredTargetCollisions asserts that every shipped
// vocabulary in spec/testdata/**/field-rules.json passes the standard
// TestCheckTargetAgreement pins above — the same standard writ imposes on
// writ.schema authors (engine/schemasrc/compile.go's checkTargetAgreement)
// and on log-resolved schemas (engine/schema.go's resolveSchemaTypes).
// spec.FieldRules() runs this check itself while loading, so a corpus that
// violates it fails to load at all; this test exists to name the invariant
// directly, so a future collision (a renamed target reused, a reconciled
// value_type drifting back apart) fails here by name rather than as an
// opaque error deep in some unrelated test's setup.
func TestFieldRulesNoUndeclaredTargetCollisions(t *testing.T) {
	if _, err := spec.FieldRules(); err != nil {
		t.Fatalf("spec.FieldRules() failed; it runs the target-collision check while loading, so this may be a collision in the shipped corpus, an unmapped vocabulary directory, or a duplicate object type: %v", err)
	}
}

// TestFieldRulesDeriveNonEmptyObjectType asserts that every rule
// spec.FieldRules() loads from the shipped testdata/**/field-rules.json
// corpus carries a non-empty ObjectType. FieldRules derives ObjectType from
// bootstrapObjectTypes (spec/fieldrules.go) keyed by directory, and fails
// closed — returning an error rather than an empty string — when a
// directory has no entry there; this test pins that every shipped directory
// does have one, so a directory added without updating the map fails here
// by name instead of silently matching every object type in fold's rule
// matching (spec/fold.md §5). It does not apply to the abstract merge vectors
// under testdata/fold/merge/, which declare no object type at all and do not
// go through FieldRules.
func TestFieldRulesDeriveNonEmptyObjectType(t *testing.T) {
	rules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules(): %v", err)
	}
	for _, r := range rules {
		if r.ObjectType == "" {
			t.Errorf("rule (%s, %d, %s) from directory %q has empty ObjectType; add an entry to bootstrapObjectTypes for that directory", r.OpType, r.OpVersion, r.Field, r.Vocabulary)
		}
	}
}
