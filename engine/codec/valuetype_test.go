package codec_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/spec"
)

// hex40/hex64 are placeholder git object ids of the right shape for git-oid
// fields that require one, used only as filler for sibling required fields.
var (
	hex40 = strings.Repeat("a", 40)
	hex64 = strings.Repeat("b", 64)
)

// TestFieldRuleVocabulariesIsExhaustive is the exhaustiveness guard
// fieldRuleVocabularies was missing, in the shape of
// TestEveryShippedVocabularyIsValidated above for vocabularySchemaFiles.
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
// hole): a new field-rules.json directory that spec.VocabularyObjectTypes
// has no mapping for. spec.FieldRules() itself now fails closed on that last
// case rather than deriving an empty ObjectType (spec/fieldrules.go), so in
// practice the t.Fatalf below already catches it; this loop pins the
// invariant on fieldRuleVocabularies directly too, so a bug in
// invertVocabularyObjectTypes's derivation — not just a gap in the source
// map — fails here by name as well.
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
			t.Errorf("testdata/%s/field-rules.json produced rules, but no fieldRuleVocabularies entry maps any object type to directory %q — add one to spec.vocabularyObjectTypes, or its rules bleed across object types unscoped", dir, dir)
		}
	}
}

// valueTypeCase probes one (object type, op type, field) combination that
// carries a declared value_type in the shipped field-rules.json tables. base
// is a schema-valid body for the op; invalid is spliced into it at field and
// must be refused by BuildCommit; valid (when non-nil) is spliced in and must
// be accepted.
type valueTypeCase struct {
	name       string
	objectType string
	opType     string
	base       string
	field      string
	invalid    any
	valid      any
}

// withField parses base, replaces (or adds) field with value's JSON encoding,
// and re-marshals; every other field in base is left untouched.
func withField(t *testing.T, base, field string, value any) json.RawMessage {
	t.Helper()
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(base), &m); err != nil {
		t.Fatalf("unmarshal base body %q: %v", base, err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %v: %v", value, err)
	}
	m[field] = raw
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return out
}

// valueTypeCases covers all twelve catalogue types (spec/value-types.md)
// through a real vocabulary, op and field that declares that value_type in
// the shipped field-rules.json tables. Two are the wiring's real blind spot:
// "int bound" and "number bound" splice in a value the vocabulary schema does
// not bound at all (settings' cycle_duration_weeks and issue's estimate carry
// no JSON Schema maximum), so only value_type's own ±2^53-1 check catches
// them — stubbing validateValueTypes to `return nil` would turn these two
// green to red. The rest double up with the vocabulary schema (most of the
// catalogue's validators are the same pattern the schema already $refs), but
// still exercise the codec-level wiring this ticket added: fieldRuleKey
// lookup, the collection-element branch (person-ref, via assign's array), the
// bare-scalar branch (object-ref, via add-issue's single reference), and the
// RejectError plumbing.
var valueTypeCases = []valueTypeCase{
	{
		name: "string", objectType: "label", opType: "create",
		base: `{"name":"bug"}`, field: "name",
		invalid: 42, valid: "renamed",
	},
	{
		name: "text", objectType: "comment", opType: "edit",
		base: `{"text":"hello"}`, field: "text",
		invalid: 42, valid: "goodbye",
	},
	{
		name: "int bound (gap: no schema maximum)", objectType: "settings", opType: "set",
		base: `{}`, field: "cycle_duration_weeks",
		invalid: int64(1 << 53), valid: int64(1<<53 - 1),
	},
	{
		name: "number bound (gap: no schema maximum)", objectType: "issue", opType: "create",
		base: `{"title":"Initial"}`, field: "estimate",
		invalid: float64(1 << 53), valid: 2.5,
	},
	{
		name: "bool", objectType: "settings", opType: "set",
		base: `{}`, field: "allow_zero_estimates",
		invalid: "yes", valid: true,
	},
	{
		name: "timestamp", objectType: "cycle", opType: "create",
		base: `{"title":"Sprint 1","starts_at":"2026-01-01T00:00:00Z","ends_at":"2026-02-01T00:00:00Z"}`, field: "starts_at",
		invalid: "not-a-timestamp", valid: "2026-03-01T00:00:00Z",
	},
	{
		name: "enum", objectType: "workflow-state", opType: "create",
		base: `{"name":"Todo","position":"V","type":"unstarted"}`, field: "type",
		invalid: "bogus", valid: "started",
	},
	{
		name: "person-ref (collection element)", objectType: "issue", opType: "assign",
		base: `{}`, field: "add",
		invalid: []string{"not-a-person"}, valid: []string{"email:alice@example.com"},
	},
	{
		name: "object-ref (bare scalar)", objectType: "cycle", opType: "add-issue",
		base: `{}`, field: "issue",
		invalid: "bad ref with space", valid: "0123456789abcdef0123456789abcdef",
	},
	{
		name: "git-oid", objectType: "review", opType: "revision",
		base: `{"base":"` + hex40 + `","head":"` + hex40 + `"}`, field: "base",
		invalid: "nothex", valid: hex64,
	},
	{
		name: "position", objectType: "issue", opType: "create",
		base: `{"title":"Initial"}`, field: "position",
		invalid: "V0", valid: "V",
	},
	{
		name: "anchor", objectType: "comment", opType: "create",
		base: `{"subject":{"object_id":"rev-1","object_type":"review"},"text":"hello"}`, field: "anchor",
		invalid: map[string]any{"version": 2},
		valid: map[string]any{
			"version": 1,
			"old":     map[string]any{"commit": hex40, "path": "a.txt", "blob": hex40},
		},
	},
}

// TestBuildCommitRejectsValueTypeViolations pins the half of producer rule 3
// (spec/op-envelope.md §Producer validation) that has zero coverage: a value
// that satisfies the vocabulary schema in shape but violates its declared
// value_type (spec/value-types.md) must still be refused by BuildCommit, for
// every catalogue type.
func TestBuildCommitRejectsValueTypeViolations(t *testing.T) {
	for _, tc := range valueTypeCases {
		t.Run(tc.name, func(t *testing.T) {
			env := codec.Envelope{
				ObjectID:   "obj-1",
				ObjectType: tc.objectType,
				OpType:     tc.opType,
				OpVersion:  1,
				Body:       withField(t, tc.base, tc.field, tc.invalid),
			}
			_, err := codec.BuildCommit(env, testAuthor(), nil, nil)
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
			env := codec.Envelope{
				ObjectID:   "obj-1",
				ObjectType: tc.objectType,
				OpType:     tc.opType,
				OpVersion:  1,
				Body:       withField(t, tc.base, tc.field, tc.valid),
			}
			if _, err := codec.BuildCommit(env, testAuthor(), nil, nil); err != nil {
				t.Fatalf("BuildCommit rejected a %s value conforming to its declared value_type: %v", tc.name, err)
			}
		})
	}
}
