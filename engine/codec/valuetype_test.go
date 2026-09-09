package codec_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/spec"
)

// hex40/hex64 are placeholder git object ids of the right shape for the
// git-oid and anchor cases below.
var (
	hex40 = strings.Repeat("a", 40)
	hex64 = strings.Repeat("b", 64)
)

// TestFieldRuleVocabulariesIsExhaustive is the exhaustiveness guard
// fieldRuleVocabularies was missing, in the shape of
// TestEveryShippedVocabularyIsValidated for vocabularySchemaFiles.
// fieldRuleVocabularies is a second, hand-maintained map with nothing pinning
// it: a typo in a value, or a new object type registered in
// vocabularySchemaFiles and forgotten here, makes validateValueTypes silently
// no-op for every op of that vocabulary, with nothing failing.
//
// Three directions are checked: every object type the producer validates
// against a vocabulary schema must also have a fieldRuleVocabularies entry
// (the first-direction hole), every fieldRuleVocabularies value must name a
// directory spec.FieldRules() actually produced rules for — not a typo'd
// directory name that quietly indexes nothing (the second-direction hole) —
// and every directory spec.FieldRules() actually produced rules for must be
// reachable from some fieldRuleVocabularies entry (the third-direction
// hole): a new field-rules.json directory nothing maps an object type onto.
// spec.FieldRules() itself now fails closed on that last case rather than
// deriving an empty ObjectType (spec/fieldrules.go), so in practice the
// t.Fatalf below already catches it; this loop pins the invariant on
// fieldRuleVocabularies directly too, so a bug in the derivation — not just
// a gap in the source map — fails here by name as well.
func TestFieldRuleVocabulariesIsExhaustive(t *testing.T) {
	rules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules(): %v", err)
	}
	knownDirs := make(map[string]bool)
	for _, r := range rules {
		knownDirs[r.Vocabulary] = true
	}

	for objectType := range codec.VocabularySchemaFiles {
		if _, ok := codec.FieldRuleVocabularies[objectType]; !ok {
			t.Errorf("object type %q is registered in vocabularySchemaFiles but has no entry in fieldRuleVocabularies — its ops silently skip value-type validation", objectType)
		}
	}
	for objectType, dir := range codec.FieldRuleVocabularies {
		if !knownDirs[dir] {
			t.Errorf("fieldRuleVocabularies[%q] = %q, but spec.FieldRules() has no rules from a testdata/%s/field-rules.json directory — value-type validation for %q silently no-ops", objectType, dir, dir, objectType)
		}
	}

	reachable := make(map[string]bool)
	for _, dir := range codec.FieldRuleVocabularies {
		reachable[dir] = true
	}
	for dir := range knownDirs {
		if !reachable[dir] {
			t.Errorf("testdata/%s/field-rules.json produced rules, but no fieldRuleVocabularies entry maps any object type to directory %q — add one, or its rules bleed across object types unscoped", dir, dir)
		}
	}
}

// valueTypeCase probes one catalogue value type through a field a log-sourced
// schema declares it on. rule is the whole declaration — it is what the
// vocabulary the case runs against is built from — and invalid must be
// refused by BuildCommit where valid must be accepted.
type valueTypeCase struct {
	name    string
	rule    spec.FieldRule
	invalid any
	valid   any
}

// valueTypeCases covers all twelve catalogue types (spec/value-types.md)
// through a log-declared vocabulary, which is where a consumer's value types
// live: `schema` aside, every object type is declared by a schema object in
// the repo's own log, and its field rules are the *only* thing that types its
// bodies. There is no per-vocabulary JSON Schema behind them to catch a
// malformed value first — so every case here is a real probe of
// value.Validate's wiring, and stubbing it to `return nil` turns all twelve
// red. "int bound" and "number bound" say so most sharply: they splice in a
// value that is well-formed JSON of the right kind and violates only the
// catalogue's own ±2^53-1 bound, which nothing but the value type could
// catch. The table also covers the two branches the collection strategies
// take: a set-union array types its elements, and a bare scalar under one is
// validated as a single element.
var valueTypeCases = []valueTypeCase{
	{
		name:    "string",
		rule:    spec.FieldRule{OpType: "set", OpVersion: 1, Field: "name", Strategy: "lww", ValueType: "string"},
		invalid: 42, valid: "renamed",
	},
	{
		name:    "text",
		rule:    spec.FieldRule{OpType: "set", OpVersion: 1, Field: "note", Strategy: "multi-value", ValueType: "text"},
		invalid: 42, valid: "goodbye",
	},
	{
		name:    "int bound",
		rule:    spec.FieldRule{OpType: "set", OpVersion: 1, Field: "count", Strategy: "lww", ValueType: "int"},
		invalid: int64(1 << 53), valid: int64(1<<53 - 1),
	},
	{
		name:    "number bound",
		rule:    spec.FieldRule{OpType: "set", OpVersion: 1, Field: "weight", Strategy: "lww", ValueType: "number"},
		invalid: float64(1 << 53), valid: 2.5,
	},
	{
		name:    "bool",
		rule:    spec.FieldRule{OpType: "set", OpVersion: 1, Field: "archived", Strategy: "tombstone", ValueType: "bool"},
		invalid: "yes", valid: true,
	},
	{
		name:    "timestamp",
		rule:    spec.FieldRule{OpType: "set", OpVersion: 1, Field: "starts_at", Strategy: "lww", ValueType: "timestamp"},
		invalid: "not-a-timestamp", valid: "2026-03-01T00:00:00Z",
	},
	{
		name:    "enum",
		rule:    spec.FieldRule{OpType: "set", OpVersion: 1, Field: "state", Strategy: "lww", ValueType: "enum", Enum: []string{"open", "done"}},
		invalid: "bogus", valid: "done",
	},
	{
		name:    "person-ref (collection element)",
		rule:    spec.FieldRule{OpType: "set", OpVersion: 1, Field: "owners", Target: "owners", Strategy: "set-union", ValueType: "person-ref"},
		invalid: []string{"not-a-person"}, valid: []string{"email:alice@example.com"},
	},
	{
		name:    "object-ref (bare scalar)",
		rule:    spec.FieldRule{OpType: "set", OpVersion: 1, Field: "member", Target: "members", Strategy: "set-observed-remove", ValueType: "object-ref"},
		invalid: "bad ref with space", valid: "0123456789abcdef0123456789abcdef",
	},
	{
		name:    "git-oid",
		rule:    spec.FieldRule{OpType: "set", OpVersion: 1, Field: "origin", Strategy: "lww", ValueType: "git-oid"},
		invalid: "nothex", valid: hex64,
	},
	{
		name:    "position",
		rule:    spec.FieldRule{OpType: "set", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position"},
		invalid: "V0", valid: "V",
	},
	{
		name:    "anchor",
		rule:    spec.FieldRule{OpType: "set", OpVersion: 1, Field: "marker", Strategy: "lww", ValueType: "anchor"},
		invalid: map[string]any{"version": 2},
		valid: map[string]any{
			"version": 1,
			"old":     map[string]any{"commit": hex40, "path": "a.txt", "blob": hex40},
		},
	},
}

// valueTypeEnvelope is the op one case writes: a body carrying nothing but
// the field under test, so the only rule that can refuse it is the one the
// case declares.
func valueTypeEnvelope(t *testing.T, tc valueTypeCase, value any) codec.Envelope {
	t.Helper()
	body, err := json.Marshal(map[string]any{tc.rule.Field: value})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     tc.rule.OpType,
		OpVersion:  tc.rule.OpVersion,
		Body:       body,
	}
}

// TestBuildCommitRejectsValueTypeViolations pins the half of producer rule 3
// (spec/op-envelope.md §Producer validation) that is about values rather than
// field names: a value that is well-formed JSON but violates its field's
// declared value_type (spec/value-types.md) must be refused by BuildCommit,
// for every catalogue type.
func TestBuildCommitRejectsValueTypeViolations(t *testing.T) {
	for _, tc := range valueTypeCases {
		t.Run(tc.name, func(t *testing.T) {
			env := valueTypeEnvelope(t, tc, tc.invalid)
			_, err := codec.BuildCommit(env, testAuthor(), nil, declareVocabulary(env.ObjectType, tc.rule))
			if err == nil {
				t.Fatalf("BuildCommit accepted a %s value violating its declared value_type", tc.name)
			}
			var rejErr *codec.RejectError
			if !errors.As(err, &rejErr) {
				t.Fatalf("error is not a *codec.RejectError: %v", err)
			}
			if rejErr.Reason != codec.RejectSchemaViolation {
				t.Errorf("reason = %q, want %q", rejErr.Reason, codec.RejectSchemaViolation)
			}
		})
	}
}

// TestBuildCommitAcceptsConformingValueTypeVectors is the companion to
// TestBuildCommitRejectsValueTypeViolations: the same fields with a value that
// conforms to the declared value_type must still build. It matters most for
// the two bound cases (int, number), where it proves the boundary itself
// (±2^53-1) is inclusive, not just that something over it is rejected.
func TestBuildCommitAcceptsConformingValueTypeVectors(t *testing.T) {
	for _, tc := range valueTypeCases {
		t.Run(tc.name, func(t *testing.T) {
			env := valueTypeEnvelope(t, tc, tc.valid)
			if _, err := codec.BuildCommit(env, testAuthor(), nil, declareVocabulary(env.ObjectType, tc.rule)); err != nil {
				t.Fatalf("BuildCommit rejected a %s value conforming to its declared value_type: %v", tc.name, err)
			}
		})
	}
}

// TestBootstrapVocabularyValidatesValueTypes covers the same rule on the one
// path the cases above never touch: tier 1, where `schema` ops are checked
// against the shipped JSON Schema plus the value types in
// spec/testdata/schema-ops/field-rules.json (validateValueTypes). max_length
// is the probe because schema-ops.schema.json bounds it below (minimum 1) and
// not above, so its ±2^53-1 ceiling is the declared value_type's alone —
// stubbing validateValueTypes to `return nil` turns this red and leaves every
// other bootstrap-tier assertion green.
func TestBootstrapVocabularyValidatesValueTypes(t *testing.T) {
	body := func(maxLength any) json.RawMessage {
		raw, err := json.Marshal(map[string]any{
			"type": "widget", "op_type": "create", "op_version": "1",
			"field": "title", "strategy": "lww", "value_type": "string",
			"max_length": maxLength,
		})
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		return raw
	}
	env := func(maxLength any) codec.Envelope {
		return codec.Envelope{
			ObjectID:   testSchemaObjectID,
			ObjectType: "schema",
			OpType:     "define-field",
			OpVersion:  1,
			Body:       body(maxLength),
		}
	}

	_, err := codec.BuildCommit(env(int64(1<<53)), testAuthor(), nil, nil)
	if err == nil {
		t.Fatal("BuildCommit accepted an int beyond spec/canonicalization.md's ±2^53-1 bound")
	}
	var rejErr *codec.RejectError
	if !errors.As(err, &rejErr) {
		t.Fatalf("error is not a *codec.RejectError: %v", err)
	}
	if rejErr.Reason != codec.RejectSchemaViolation {
		t.Errorf("reason = %q, want %q", rejErr.Reason, codec.RejectSchemaViolation)
	}

	if _, err := codec.BuildCommit(env(int64(1<<53-1)), testAuthor(), nil, nil); err != nil {
		t.Fatalf("BuildCommit rejected the inclusive bound itself: %v", err)
	}
}
