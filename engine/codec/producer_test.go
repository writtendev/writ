package codec_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/spec"
)

func testAuthor() codec.Identity {
	return codec.Identity{
		Name:  "Alice",
		Email: "alice@example.com",
		When:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// testSchemaObjectID is the object id the declarations below are attributed
// to, so a tier 2 rejection names a schema object the way a real one does.
const testSchemaObjectID = "sch-acme"

// declareVocabulary builds the log-sourced declaration for one object type:
// the (op_type, op_version) pairs rule 4 accepts and the field rules rule 3
// checks, both read off the rules given. It stands in for what
// writ.VocabulariesFromSchemas resolves out of a repo's own log, so a codec
// test can pin tier 2 of spec/op-envelope.md's producer precedence without a
// repository — and, like spec/schema-ops.md §4.2's generosity, a field rule
// alone is enough to declare the op type it names.
func declareVocabulary(objectType string, rules ...spec.FieldRule) codec.Vocabularies {
	voc := codec.Vocabulary{
		Declared:       true,
		SchemaObjectID: testSchemaObjectID,
		OpTypes:        make(map[codec.OpVersionKey]bool),
		Fields:         make(map[codec.OpVersionKey][]spec.FieldRule),
	}
	for _, r := range rules {
		r.ObjectType = objectType
		key := codec.OpVersionKey{OpType: r.OpType, OpVersion: r.OpVersion}
		voc.OpTypes[key] = true
		voc.Fields[key] = append(voc.Fields[key], r)
	}
	return codec.Vocabularies{objectType: voc}
}

// widgetVocabulary is the declaration the plain create/update ops throughout
// this package's tests are written against: one consumer-declared object
// type, whose whole vocabulary is stated here rather than shipped by writ.
func widgetVocabulary() codec.Vocabularies {
	return declareVocabulary("widget",
		spec.FieldRule{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		spec.FieldRule{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		spec.FieldRule{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
	)
}

// TestBuildCommitRejectsInvalidBody pins the producer obligation from
// spec/op-envelope.md: an op whose body violates the vocabulary that applies
// to it is never built, so it is never signed and never appended. Both tiers
// that have a vocabulary to violate are covered — the bootstrap one writ
// embeds for `schema` (tier 1, a JSON Schema) and a log-declared one (tier 2,
// field rules), which fail in different code and would not catch each other's
// regressions.
func TestBuildCommitRejectsInvalidBody(t *testing.T) {
	cases := []struct {
		name         string
		env          codec.Envelope
		vocabularies codec.Vocabularies
	}{
		{
			name: "schema create missing namespace",
			env: codec.Envelope{
				ObjectID:   testSchemaObjectID,
				ObjectType: "schema",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"description":"no namespace"}`),
			},
		},
		{
			name: "namespace violating its grammar",
			env: codec.Envelope{
				ObjectID:   testSchemaObjectID,
				ObjectType: "schema",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"namespace":"Acme Corp"}`),
			},
		},
		{
			name: "define-field missing strategy",
			env: codec.Envelope{
				ObjectID:   testSchemaObjectID,
				ObjectType: "schema",
				OpType:     "define-field",
				OpVersion:  1,
				Body:       json.RawMessage(`{"type":"widget","op_type":"create","op_version":"1","field":"title"}`),
			},
		},
		{
			name: "define-field naming a value type outside the catalogue",
			env: codec.Envelope{
				ObjectID:   testSchemaObjectID,
				ObjectType: "schema",
				OpType:     "define-field",
				OpVersion:  1,
				Body:       json.RawMessage(`{"type":"widget","op_type":"create","op_version":"1","field":"title","strategy":"lww","value_type":"colour"}`),
			},
		},
		{
			// Rule 3 at tier 2: nothing bounds "known fields" for a
			// log-declared type but the declared rules themselves, so a
			// field no rule names is the rejection.
			name: "body field the log schema does not declare",
			env: codec.Envelope{
				ObjectID:   "w-1",
				ObjectType: "widget",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"headline":"no rule declares this"}`),
			},
			vocabularies: widgetVocabulary(),
		},
		{
			name: "value violating its declared value_type",
			env: codec.Envelope{
				ObjectID:   "w-1",
				ObjectType: "widget",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":42}`),
			},
			vocabularies: widgetVocabulary(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := codec.BuildCommit(tc.env, testAuthor(), nil, tc.vocabularies)
			if err == nil {
				t.Fatal("BuildCommit accepted a schema-invalid body")
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

// keyedWidgetVocabulary declares one keyed-lww field, "verdict", keyed on a
// "subject" column typed person-ref -- the shape WRIT-214 fixes tier 2 to
// accept: "subject" is a member of "verdict"'s key, never itself a
// declared field, exactly like writ's own bootstrap vocabulary's
// deprecate-type "type" key column (spec/op-envelope.md §Producer
// validation rule 3).
func keyedWidgetVocabulary() codec.Vocabularies {
	return declareVocabulary("widget",
		spec.FieldRule{
			OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
			Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"},
			ValueType: "string",
		},
	)
}

// TestBuildCommitAcceptsKeyedLWWKeyColumns is WRIT-214's fix, pinned directly
// against BuildCommit: a keyed-lww field's key column is declared by being a
// member of the field's own key, so a body carrying both the field and its
// key column is accepted even though no rule names the key column as a
// field of its own.
func TestBuildCommitAcceptsKeyedLWWKeyColumns(t *testing.T) {
	if _, err := codec.BuildCommit(codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "approve",
		OpVersion:  1,
		Body:       json.RawMessage(`{"verdict":"approve","subject":"email:alice@example.com"}`),
	}, testAuthor(), nil, keyedWidgetVocabulary()); err != nil {
		t.Fatalf("BuildCommit rejected a body carrying a declared keyed-lww key column: %v", err)
	}
}

// TestBuildCommitRejectsKeyedLWWKeyColumns covers the two ways a key column's
// value can still fail: it must be a JSON string regardless of its
// key_types entry (fold treats a non-string key component as
// uninterpretable, spec/fold.md §5 keyed-lww), and it must additionally
// conform to that entry, checked the same way a field's value is checked
// against its value_type.
func TestBuildCommitRejectsKeyedLWWKeyColumns(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "key column value is not a JSON string",
			body: `{"verdict":"approve","subject":123}`,
		},
		{
			name: "key column value is a string but fails its key_types entry",
			body: `{"verdict":"approve","subject":"not-a-person-ref"}`,
		},
		{
			// Round-1 review finding: JSON null took the same "no write"
			// `continue` a declared field's absent-shaped null gets, even
			// though a key column addresses a register rather than
			// carrying a value of its own and fold's keyed-lww strategy
			// (engine/internal/fold/reject.go's isString(nil) == false)
			// does not tolerate null there either — this pinned the
			// producer accept a reader quarantines forever.
			name: "key column value is JSON null",
			body: `{"verdict":"approve","subject":null}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := codec.BuildCommit(codec.Envelope{
				ObjectID:   "w-1",
				ObjectType: "widget",
				OpType:     "approve",
				OpVersion:  1,
				Body:       json.RawMessage(tc.body),
			}, testAuthor(), nil, keyedWidgetVocabulary())
			if err == nil {
				t.Fatal("BuildCommit accepted a malformed keyed-lww key column value")
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

// TestBuildCommitFieldRuleWinsOverKeyColumn pins the one ambiguity
// validateFieldsAgainstRules resolves rather than leaving undefined: where a
// name is both a declared field and a keyed-lww key column of another rule
// in the same (op_type, op_version), the field rule governs its typing.
// "subject" here is declared as a plain string field and, separately, is
// the key column of "verdict" typed person-ref -- a value that is a
// conforming string but not a conforming person-ref must be accepted,
// because the field rule (plain string, no format constraint) is the one
// that applies.
func TestBuildCommitFieldRuleWinsOverKeyColumn(t *testing.T) {
	vocabularies := declareVocabulary("widget",
		spec.FieldRule{OpType: "approve", OpVersion: 1, Field: "subject", Strategy: "lww", ValueType: "string"},
		spec.FieldRule{
			OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
			Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"},
			ValueType: "string",
		},
	)

	if _, err := codec.BuildCommit(codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "approve",
		OpVersion:  1,
		Body:       json.RawMessage(`{"verdict":"approve","subject":"not-a-person-ref-but-a-fine-string"}`),
	}, testAuthor(), nil, vocabularies); err != nil {
		t.Fatalf("BuildCommit rejected a value valid under the declared field rule, "+
			"suggesting the key-column rule ran instead: %v", err)
	}
}

// TestBuildCommitAcceptsEnumKeyColumn is round 2's fix for an enum-typed key
// column: key_types names a column's type only, with no slot for the member
// list an enum field's own enum attribute would supply, so
// validateKeyColumnValue cannot check membership for one — and holding it to
// the JSON-string requirement alone, rather than rejecting every write with
// value.Validate's empty-Params "not a member of the declared enum []", is
// the fix. A value no declared enum anywhere would admit is accepted here on
// purpose: key_types carries no member list to check it against.
func TestBuildCommitAcceptsEnumKeyColumn(t *testing.T) {
	vocabularies := declareVocabulary("widget",
		spec.FieldRule{
			OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
			Key: []string{"phase"}, KeyTypes: map[string]string{"phase": "enum"},
			ValueType: "string",
		},
	)

	if _, err := codec.BuildCommit(codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "approve",
		OpVersion:  1,
		Body:       json.RawMessage(`{"verdict":"approve","phase":"whatever-string"}`),
	}, testAuthor(), nil, vocabularies); err != nil {
		t.Fatalf("BuildCommit rejected an enum-typed key column value, "+
			"suggesting value.Validate ran against an empty member list: %v", err)
	}
}

// TestBuildCommitAcceptsNonStringShapedKeyColumns is round 3's fix for the
// other key_types entries whose ordinary catalogue encoding is not a JSON
// string: int, number, bool and anchor. Before this fix, validateKeyColumnValue
// ran value.Validate against the raw (already string-typed) value, which is
// mutually unsatisfiable for all four — every write was rejected and the
// field was permanently unwritable, with the schema itself never refused.
// The fix reads the key column's JSON-string content as the type's own
// textual encoding (spec/schema-ops.md §3.1's op_version-as-decimal-string
// precedent, generalized), so "7" decodes to the JSON integer 7, "true" to
// the JSON boolean true, and a compact anchor object's JSON text to the
// anchor value itself. Round 4 tightened "the type's own textual encoding"
// to canonicaljson's own encoding of itself (canonicalKeyColumnContent), so
// every content string below is written in that canonical form — sorted
// object members, no insignificant whitespace — on purpose, not just
// coincidentally: TestBuildCommitRejectsInvalidNonStringShapedKeyColumnEncoding
// pins the non-canonical spellings this rejects.
func TestBuildCommitAcceptsNonStringShapedKeyColumns(t *testing.T) {
	cases := []struct {
		name    string
		keyType string
		content string // the key column's JSON-string content: valueType's own canonical JSON encoding, as text.
	}{
		{name: "int", keyType: "int", content: "7"},
		{name: "number", keyType: "number", content: "3.5"},
		{name: "bool", keyType: "bool", content: "true"},
		{name: "anchor", keyType: "anchor", content: `{"old":{"blob":"b","commit":"c","path":"p"},"version":1}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vocabularies := declareVocabulary("widget",
				spec.FieldRule{
					OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
					Key: []string{"tag"}, KeyTypes: map[string]string{"tag": tc.keyType},
					ValueType: "string",
				},
			)
			body, err := json.Marshal(map[string]any{"verdict": "approve", "tag": tc.content})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			if _, err := codec.BuildCommit(codec.Envelope{
				ObjectID:   "w-1",
				ObjectType: "widget",
				OpType:     "approve",
				OpVersion:  1,
				Body:       body,
			}, testAuthor(), nil, vocabularies); err != nil {
				t.Fatalf("BuildCommit rejected a %s-typed key column whose string content %q is a conforming JSON encoding: %v",
					tc.keyType, tc.content, err)
			}
		})
	}
}

// TestBuildCommitRejectsInvalidNonStringShapedKeyColumnEncoding covers the
// three ways a non-string-shaped key column's content can still fail after
// round 3's decode fix: the content is not valid JSON at all, it decodes to
// a JSON value of the wrong shape for the declared key_types entry, or it
// is valid JSON of the right shape but not that value's one canonical
// spelling (round 4: canonicalKeyColumnContent requires the content to
// already be canonicaljson's own encoding of itself, because fold keys a
// keyed-lww register on the raw string — "7", "7.0", " 7", "1e3", and "-0"
// all mean the same int but would otherwise address four different
// registers that can never converge). All three remain producer
// rejections — the decode is stricter than fold, which never looks past
// "is this a JSON string" for a key column, so a reader still tolerates
// every shape (spec/testdata/producer/cases/keyed-lww-key-column-int-invalid-encoding.json
// pins that asymmetry at the corpus level).
func TestBuildCommitRejectsInvalidNonStringShapedKeyColumnEncoding(t *testing.T) {
	cases := []struct {
		name    string
		keyType string
		content string
	}{
		{name: "int content is not JSON", keyType: "int", content: "not-a-number"},
		{name: "int content decodes to a JSON string, not a JSON number", keyType: "int", content: `"seven"`},
		{name: "bool content is not JSON", keyType: "bool", content: "yes"},
		{name: "anchor content is not JSON", keyType: "anchor", content: "not-json"},
		{name: "int content has a non-canonical trailing .0", keyType: "int", content: "7.0"},
		{name: "int content has leading whitespace", keyType: "int", content: " 7"},
		{name: "int content has trailing whitespace", keyType: "int", content: "7 "},
		{name: "int content uses exponential notation", keyType: "int", content: "1e3"},
		{name: "int content is negative zero", keyType: "int", content: "-0"},
		{name: "anchor content has reordered, non-compact members", keyType: "anchor", content: `{"version": 1, "old": {"commit":"c","path":"p","blob":"b"}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vocabularies := declareVocabulary("widget",
				spec.FieldRule{
					OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
					Key: []string{"tag"}, KeyTypes: map[string]string{"tag": tc.keyType},
					ValueType: "string",
				},
			)
			body, err := json.Marshal(map[string]any{"verdict": "approve", "tag": tc.content})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			_, err = codec.BuildCommit(codec.Envelope{
				ObjectID:   "w-1",
				ObjectType: "widget",
				OpType:     "approve",
				OpVersion:  1,
				Body:       body,
			}, testAuthor(), nil, vocabularies)
			if err == nil {
				t.Fatalf("BuildCommit accepted a %s-typed key column with non-conforming content %q", tc.keyType, tc.content)
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

// TestBuildCommitRejectsFieldRuleAlsoKeyColumnNonString is round 3's fix for
// the other lockstep hole this PR left: byField ran instead of the
// key-column branch for a name declared both ways, so a value satisfying the
// field's own value_type (here, seq as a plain int) was never checked
// against the "MUST be a JSON string" floor every keyed-lww key column
// carries, whatever its own key_types entry says (spec/fold.md §5,
// enforced via fold.ruleAccepts). "seq" is declared as an int field with
// strategy lww, and separately is the key column of "verdict"'s keyed-lww
// rule; a JSON number is exactly what the field's own value_type wants, and
// exactly what fold's key-column check refuses, so the producer must refuse
// it too. TestBuildCommitFieldRuleWinsOverKeyColumn is the companion case
// showing this is a union, not a wholesale reversal of "the field rule
// governs the type": a value that is already a JSON string still only
// answers to the field's own (looser) rule.
func TestBuildCommitRejectsFieldRuleAlsoKeyColumnNonString(t *testing.T) {
	vocabularies := declareVocabulary("widget",
		spec.FieldRule{OpType: "approve", OpVersion: 1, Field: "seq", Strategy: "lww", ValueType: "int"},
		spec.FieldRule{
			OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
			Key: []string{"seq"}, KeyTypes: map[string]string{"seq": "string"},
			ValueType: "string",
		},
	)

	_, err := codec.BuildCommit(codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "approve",
		OpVersion:  1,
		Body:       json.RawMessage(`{"verdict":"approve","seq":7}`),
	}, testAuthor(), nil, vocabularies)
	if err == nil {
		t.Fatal("BuildCommit accepted a JSON number for a name that is both an int field and another rule's key column, " +
			"which fold.ruleAccepts would quarantine as an uninterpretable key component")
	}
	var rejErr *codec.RejectError
	if !errors.As(err, &rejErr) {
		t.Fatalf("error is not a *codec.RejectError: %v", err)
	}
	if rejErr.Reason != codec.RejectSchemaViolation {
		t.Errorf("reason = %q, want %q", rejErr.Reason, codec.RejectSchemaViolation)
	}
}

// TestBuildCommitAcceptsFieldRuleAlsoKeyColumnNonStringDecoded is round 4's
// fix for the half of the field-rule/key-column union round 3 left broken:
// a dual-role name whose *field* rule declares int, number, bool, or anchor
// was permanently unwritable, because the key-column JSON-string floor and
// value.Validate(r.ValueType, ..., val) ran against the same raw string,
// which is unsatisfiable for all four (a JSON string is never a conforming
// JSON integer, number, boolean, or object). The fix decodes that string's
// content against r.ValueType the same way validateKeyColumnValue already
// does for a key-column-only name, so "seq" declared `int lww` and also
// keyed on by "verdict" is writable via the canonical decimal string, not
// permanently refused.
func TestBuildCommitAcceptsFieldRuleAlsoKeyColumnNonStringDecoded(t *testing.T) {
	cases := []struct {
		name      string
		valueType string
		content   string // the body's JSON-string value: the field's own value_type, canonically encoded as text.
	}{
		{name: "int", valueType: "int", content: "7"},
		{name: "number", valueType: "number", content: "3.5"},
		{name: "bool", valueType: "bool", content: "true"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vocabularies := declareVocabulary("widget",
				spec.FieldRule{OpType: "approve", OpVersion: 1, Field: "seq", Strategy: "lww", ValueType: tc.valueType},
				spec.FieldRule{
					OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
					Key: []string{"seq"}, KeyTypes: map[string]string{"seq": tc.valueType},
					ValueType: "string",
				},
			)
			body, err := json.Marshal(map[string]any{"verdict": "approve", "seq": tc.content})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			if _, err := codec.BuildCommit(codec.Envelope{
				ObjectID:   "w-1",
				ObjectType: "widget",
				OpType:     "approve",
				OpVersion:  1,
				Body:       body,
			}, testAuthor(), nil, vocabularies); err != nil {
				t.Fatalf("BuildCommit rejected a dual-role %s field/key-column whose string content %q "+
					"is a conforming canonical encoding: %v", tc.valueType, tc.content, err)
			}
		})
	}
}

// TestBuildCommitRejectsFieldRuleAlsoKeyColumnNonCanonicalContent covers the
// two ways the decode TestBuildCommitAcceptsFieldRuleAlsoKeyColumnNonStringDecoded
// pins can still fail: the string's content does not conform to the
// field's own value_type at all, or it conforms but is not that value's
// one canonical spelling (round 4's canonicalKeyColumnContent, the same
// requirement TestBuildCommitRejectsInvalidNonStringShapedKeyColumnEncoding
// pins for a key-column-only name).
func TestBuildCommitRejectsFieldRuleAlsoKeyColumnNonCanonicalContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "content is not valid JSON", content: "not-a-number"},
		{name: "content is a non-canonical trailing .0", content: "7.0"},
		{name: "content has leading whitespace", content: " 7"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vocabularies := declareVocabulary("widget",
				spec.FieldRule{OpType: "approve", OpVersion: 1, Field: "seq", Strategy: "lww", ValueType: "int"},
				spec.FieldRule{
					OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
					Key: []string{"seq"}, KeyTypes: map[string]string{"seq": "int"},
					ValueType: "string",
				},
			)
			body, err := json.Marshal(map[string]any{"verdict": "approve", "seq": tc.content})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			_, err = codec.BuildCommit(codec.Envelope{
				ObjectID:   "w-1",
				ObjectType: "widget",
				OpType:     "approve",
				OpVersion:  1,
				Body:       body,
			}, testAuthor(), nil, vocabularies)
			if err == nil {
				t.Fatalf("BuildCommit accepted a dual-role int field/key-column with non-conforming content %q", tc.content)
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

// TestBuildCommitRejectsTombstoneAlsoKeyColumn is round 5's fix: round 4's
// field-rule/key-column decode branch keyed off r.ValueType alone, so a name
// that is both a tombstone field (value_type bool) and another rule's
// keyed-lww key column had its "true"/"false" string content decoded to the
// JSON boolean it needed for its own bool check -- while the raw body value
// stayed the JSON string the key-column floor demands. fold's tombstone
// reducer requires the *raw* value to already be a JSON boolean
// (engine/internal/fold/reject.go's ruleAccepts), so that JSON string is
// exactly what every reader's fold.Uninterpretable quarantines: the op was
// signed, appended, and universally ignored. No value can ever satisfy both
// the key-column floor and tombstone's own requirement for the same body
// value, so BuildCommit now refuses every value for this combination,
// whether or not the field even declares value_type "bool" (tombstone's own
// ValidateFieldRule rule permits "" too).
func TestBuildCommitRejectsTombstoneAlsoKeyColumn(t *testing.T) {
	cases := []struct {
		name      string
		valueType string
	}{
		{name: "bool value_type", valueType: "bool"},
		{name: "no declared value_type", valueType: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vocabularies := declareVocabulary("widget",
				spec.FieldRule{OpType: "approve", OpVersion: 1, Field: "flag", Strategy: "tombstone", ValueType: tc.valueType},
				spec.FieldRule{
					OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
					Key: []string{"flag"}, KeyTypes: map[string]string{"flag": "bool"},
					ValueType: "string",
				},
			)

			for _, val := range []string{"true", "false"} {
				body, err := json.Marshal(map[string]any{"verdict": "approve", "flag": val})
				if err != nil {
					t.Fatalf("marshal body: %v", err)
				}
				_, err = codec.BuildCommit(codec.Envelope{
					ObjectID:   "w-1",
					ObjectType: "widget",
					OpType:     "approve",
					OpVersion:  1,
					Body:       body,
				}, testAuthor(), nil, vocabularies)
				if err == nil {
					t.Fatalf("BuildCommit accepted %q for a tombstone field that also names a keyed-lww key column, "+
						"which fold.ruleAccepts's tombstone case would quarantine (it requires a raw JSON boolean, not a string)", val)
				}
				var rejErr *codec.RejectError
				if !errors.As(err, &rejErr) {
					t.Fatalf("error is not a *codec.RejectError: %v", err)
				}
				if rejErr.Reason != codec.RejectSchemaViolation {
					t.Errorf("reason = %q, want %q", rejErr.Reason, codec.RejectSchemaViolation)
				}
			}
		})
	}
}

// TestBuildCommitAcceptsCanonicalTimestampKeyColumn and
// TestBuildCommitRejectsNonCanonicalTimestampKeyColumn are round 5's second
// fix: timestamp was in keyColumnStringEncoded (no decode needed, since its
// ordinary encoding is already a JSON string) but that let three different
// spellings of the same instant -- a UTC "Z" offset, a numeric offset, and a
// trailing-zero-padded fraction -- each address a different keyed-lww
// register that can never converge (fold keys on the raw string;
// value.Normalize is the identity for timestamp). A timestamp key column's
// value must now already be its one canonical spelling: UTC, with
// fractional seconds present only when nonzero and written with no
// trailing zero digits.
func TestBuildCommitAcceptsCanonicalTimestampKeyColumn(t *testing.T) {
	vocabularies := declareVocabulary("widget",
		spec.FieldRule{
			OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
			Key: []string{"at"}, KeyTypes: map[string]string{"at": "timestamp"},
			ValueType: "string",
		},
	)

	for _, ts := range []string{"2024-01-01T00:00:00Z", "2024-01-01T00:00:00.5Z"} {
		t.Run(ts, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"verdict": "approve", "at": ts})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			if _, err := codec.BuildCommit(codec.Envelope{
				ObjectID:   "w-1",
				ObjectType: "widget",
				OpType:     "approve",
				OpVersion:  1,
				Body:       body,
			}, testAuthor(), nil, vocabularies); err != nil {
				t.Fatalf("BuildCommit rejected a canonically-spelled timestamp key column %q: %v", ts, err)
			}
		})
	}
}

func TestBuildCommitRejectsNonCanonicalTimestampKeyColumn(t *testing.T) {
	cases := []struct {
		name string
		ts   string
	}{
		{name: "numeric offset instead of Z", ts: "2024-01-01T01:00:00+01:00"},
		{name: "trailing zero fractional digits", ts: "2024-01-01T00:00:00.000Z"},
		{name: "not a real calendar date", ts: "2024-13-01T00:00:00Z"},
		{name: "not a timestamp at all", ts: "not-a-timestamp"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vocabularies := declareVocabulary("widget",
				spec.FieldRule{
					OpType: "approve", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww",
					Key: []string{"at"}, KeyTypes: map[string]string{"at": "timestamp"},
					ValueType: "string",
				},
			)
			body, err := json.Marshal(map[string]any{"verdict": "approve", "at": tc.ts})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			_, err = codec.BuildCommit(codec.Envelope{
				ObjectID:   "w-1",
				ObjectType: "widget",
				OpType:     "approve",
				OpVersion:  1,
				Body:       body,
			}, testAuthor(), nil, vocabularies)
			if err == nil {
				t.Fatalf("BuildCommit accepted a non-canonical timestamp key column %q", tc.ts)
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

// knownCreateBodies is a minimal, schema-valid create body for each vocabulary
// writ ships, so the forward-compatibility cases below can be driven against
// every one of them rather than against whichever one happens to be most
// permissive. Writ ships exactly one — `schema`, the bootstrap
// (spec/schema-ops.md §Bootstrap) — and the map is keyed by object type so a
// second one cannot be added to spec/schemas/ without a body here.
var knownCreateBodies = map[string]string{
	"schema": `{"namespace":"acme"}`,
}

// TestBuildCommitAcceptsUnknownFieldsInEveryVocabulary asserts the producer
// check did not narrow the unknown-field tolerance every shipped vocabulary
// has (spec/forward-compatibility.md): a field no version of it defines is
// still writable.
func TestBuildCommitAcceptsUnknownFieldsInEveryVocabulary(t *testing.T) {
	for _, objectType := range sortedVocabularies(t) {
		t.Run(objectType, func(t *testing.T) {
			body := knownCreateBodies[objectType]
			if body == "" {
				t.Fatalf("vocabulary %q has no create body in knownCreateBodies — add one so this test covers it", objectType)
			}
			// Splice a field no version of this vocabulary defines into an
			// otherwise valid create body.
			withUnknown := body[:len(body)-1] + `,"future_field":{"x":1}}`

			opType := "create"
			if !slices.Contains(codec.VocabularyOpTypes[objectType], opType) {
				opType = codec.VocabularyOpTypes[objectType][0]
			}

			if _, err := codec.BuildCommit(codec.Envelope{
				ObjectID:   "obj-1",
				ObjectType: objectType,
				OpType:     opType,
				OpVersion:  1,
				Body:       json.RawMessage(withUnknown),
			}, testAuthor(), nil, nil); err != nil {
				t.Fatalf("BuildCommit rejected an unknown field a producer must still write: %v", err)
			}
		})
	}
}

// TestBuildCommitRefusesUndeclaredObjectTypes inverts what this test used to
// pin (TestBuildCommitAcceptsForeignObjectTypes, pre-WRIT-188): an object
// type nothing in the log declares is a producer error
// (spec/op-envelope.md §Producer validation, tier 4), not a silent pass.
//
// The old rationale — "a reader has to tolerate it and writ's own producer
// never emits one" — rested on writ's producer only ever emitting types it
// embedded itself. It emits consumer-declared types instead, and rules 3/4
// ("the op_type and op_version are ones the producer itself defines") stop
// being satisfiable for a type nothing declares at all: an op of a truly
// foreign object type is exactly the un-withdrawable mistake those rules
// exist to prevent, so it is refused rather than let through. This is
// deliberately scoped to the *genuine* absence case — a *contested* object
// type (two schema objects binding one bare type) is a different tier and
// stays writable (TestContestedObjectTypeStaysWritable in
// engine/schema_test.go).
func TestBuildCommitRefusesUndeclaredObjectTypes(t *testing.T) {
	if _, err := codec.BuildCommit(codec.Envelope{
		ObjectID:   "g-1",
		ObjectType: "gadget",
		OpType:     "sprocket",
		OpVersion:  7,
		Body:       json.RawMessage(`{"anything":[1,2,3]}`),
	}, testAuthor(), nil, widgetVocabulary()); err == nil {
		t.Fatal("BuildCommit accepted an object_type no schema in the log declares")
	}
}

// TestVocabularySchemasTolerateUnknownOpTypeAndVersion pins reader safety, the
// half of WRIT-148 that lives in the schemas: every shipped vocabulary gates
// its body rules on op_version 1, so an op carrying an op type it never heard
// of, or a version it does not implement, is a *valid instance* of it.
//
// This is what a third party validating a foreign op against a published
// vocabulary schema depends on. A vocabulary that pins op_version or an
// op_type enum in an unconditional allOf breaks it, which is what this
// catches. It goes through the schemas alone, not through ValidateBody,
// because ValidateBody also enforces the producer rules that
// TestBuildCommitRefusesOpTypesItDoesNotDefine covers.
func TestVocabularySchemasTolerateUnknownOpTypeAndVersion(t *testing.T) {
	for _, objectType := range sortedVocabularies(t) {
		cases := []struct {
			name string
			env  codec.Envelope
		}{
			{
				name: "unknown op type",
				env: codec.Envelope{
					ObjectID:   "obj-1",
					ObjectType: objectType,
					OpType:     "annotate",
					OpVersion:  1,
					Body:       json.RawMessage(`{"note":"from a newer client"}`),
				},
			},
			{
				name: "future op version",
				env: codec.Envelope{
					ObjectID:   "obj-1",
					ObjectType: objectType,
					OpType:     "create",
					OpVersion:  codec.VocabularyOpVersion + 1,
					Body:       json.RawMessage(`{"headline":"v2 renamed the field"}`),
				},
			},
		}
		for _, tc := range cases {
			t.Run(objectType+"/"+tc.name, func(t *testing.T) {
				raw, err := codec.EncodePayload(tc.env)
				if err != nil {
					t.Fatalf("EncodePayload: %v", err)
				}
				if err := codec.ValidateAgainstVocabularySchema(objectType, raw); err != nil {
					t.Fatalf("%s.schema.json rejected an op a reader must tolerate (spec/forward-compatibility.md): %v",
						objectType, err)
				}
			})
		}
	}
}

// producerTypos is one misspelling of an op type each vocabulary really
// defines. A typo is the case producer rule 4 exists for: it is not caught by
// schema validation — the schemas are reader-safe by construction, so an
// unrecognized op type means the body is never examined at all — and the op it
// would write is one no reader will ever interpret.
var producerTypos = map[string]string{
	"schema": "creat",
}

// TestBuildCommitRefusesOpTypesItDoesNotDefine pins producer rule 4
// (spec/op-envelope.md §Producer validation) across every vocabulary writ
// ships: an op_type or op_version this build does not define for an object
// type it does define is refused before the commit is built, so it is never
// signed and never appended.
func TestBuildCommitRefusesOpTypesItDoesNotDefine(t *testing.T) {
	for _, objectType := range sortedVocabularies(t) {
		typo := producerTypos[objectType]
		if typo == "" {
			t.Fatalf("vocabulary %q has no entry in producerTypos — add one so this test covers it", objectType)
		}
		if defined := codec.VocabularyOpTypes[objectType]; slices.Contains(defined, typo) {
			t.Fatalf("producerTypos[%q] = %q is an op type the vocabulary defines; pick a misspelling", objectType, typo)
		}

		validOpType := "create"
		if !slices.Contains(codec.VocabularyOpTypes[objectType], validOpType) {
			validOpType = codec.VocabularyOpTypes[objectType][0]
		}

		cases := []struct {
			name string
			env  codec.Envelope
		}{
			{
				name: "typo in a defined op type",
				env: codec.Envelope{
					ObjectID:   "obj-1",
					ObjectType: objectType,
					OpType:     typo,
					OpVersion:  1,
					Body:       json.RawMessage(`{"title":"Initial"}`),
				},
			},
			{
				name: "op type from a newer client",
				env: codec.Envelope{
					ObjectID:   "obj-1",
					ObjectType: objectType,
					OpType:     "annotate",
					OpVersion:  1,
					Body:       json.RawMessage(`{"note":"from a newer client"}`),
				},
			},
			{
				name: "op version this build does not implement",
				env: codec.Envelope{
					ObjectID:   "obj-1",
					ObjectType: objectType,
					OpType:     validOpType,
					OpVersion:  codec.VocabularyOpVersion + 1,
					Body:       json.RawMessage(`{"headline":"v2 renamed the field"}`),
				},
			},
		}
		for _, tc := range cases {
			t.Run(objectType+"/"+tc.name, func(t *testing.T) {
				if _, err := codec.BuildCommit(tc.env, testAuthor(), nil, nil); err == nil {
					t.Fatalf("BuildCommit signed an op writ cannot interpret: object_type %q, op_type %q, op_version %d",
						tc.env.ObjectType, tc.env.OpType, tc.env.OpVersion)
				}
				// The same op must still validate against the vocabulary
				// schema: rule 4 binds the producer, and the schema stays
				// reader-safe.
				raw, err := codec.EncodePayload(tc.env)
				if err != nil {
					t.Fatalf("EncodePayload: %v", err)
				}
				if err := codec.ValidateAgainstVocabularySchema(objectType, raw); err != nil {
					t.Fatalf("the vocabulary schema rejected it too, so the refusal is not producer-only: %v", err)
				}
			})
		}
	}
}

// TestProducerOpTypesMatchShippedVocabularies keeps the producer's op-type
// registry and the shipped schemas in agreement. The registry cannot be read
// out of the schemas at runtime — they are reader-safe, so they accept op types
// they do not define — but they do carry an if/then branch per op type they do
// define, and that is what this compares against.
//
// A vocabulary that grows an op type without a registry entry fails here, and
// would otherwise fail closed at the producer with a confusing "not one this
// build defines" on an op type the spec plainly lists.
func TestProducerOpTypesMatchShippedVocabularies(t *testing.T) {
	for objectType, file := range shippedVocabularies(t) {
		raw, err := spec.FS.ReadFile("schemas/" + file)
		if err != nil {
			t.Fatalf("read schema %s: %v", file, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse schema %s: %v", file, err)
		}

		inSchema := constsUnderProperty(doc, "op_type")
		if len(inSchema) == 0 {
			t.Errorf("spec/schemas/%s pins no op_type consts, so this test cannot check the registry against it", file)
			continue
		}
		registered := append([]string(nil), codec.VocabularyOpTypes[objectType]...)
		sort.Strings(registered)
		if !slices.Equal(inSchema, registered) {
			t.Errorf("object type %q: spec/schemas/%s defines op types %v, the codec's producer registry has %v — update vocabularyOpTypes in engine/codec/schema.go",
				objectType, file, inSchema, registered)
		}
	}
}

// TestShippedVocabulariesGateOnTheProducedOpVersion pins the assumption behind
// the single vocabularyOpVersion constant: every shipped vocabulary gates its
// body rules on the same version this build writes. The first vocabulary to
// ship a v2 fails here, which is the signal to make the constant a
// per-object-type set rather than to widen it.
func TestShippedVocabulariesGateOnTheProducedOpVersion(t *testing.T) {
	for objectType, file := range shippedVocabularies(t) {
		raw, err := spec.FS.ReadFile("schemas/" + file)
		if err != nil {
			t.Fatalf("read schema %s: %v", file, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse schema %s: %v", file, err)
		}

		versions := constsUnderProperty(doc, "op_version")
		want := []string{strconv.FormatInt(codec.VocabularyOpVersion, 10)}
		if !slices.Equal(versions, want) {
			t.Errorf("object type %q: spec/schemas/%s gates on op_version %v, the codec produces %v",
				objectType, file, versions, want)
		}
	}
}

// TestEncodePayloadDoesNotValidateBody guards the read path. EncodePayload is
// used by the projection to re-encode ops fetched from the log whose raw bytes
// it did not keep (engine/projection/refresh.go). If producer validation ever
// moves into it, writ starts refusing to project foreign ops it reads perfectly
// well today — so the checks live in BuildCommit and this test says so.
//
// Both producer rules have to be covered, because they fail differently. Rule 3
// (the body check) would break re-encoding of an op whose op_type this build
// knows; rule 4 (the op_type/op_version check) would break it for an op from a
// newer writ, which is the forward-compatibility break this whole change
// exists to prevent. A defined op_type with a bad body does not exercise
// rule 4 at all, so the unknown-op-type case is not a variation on the first
// one — it is the other half of the guard. The last case is the type nothing
// declares at all: tier 4, the op the projection is likeliest to meet, since
// a repo that has not fetched the declaring schema object yet still reads
// every op written against it.
func TestEncodePayloadDoesNotValidateBody(t *testing.T) {
	cases := []struct {
		name         string
		env          codec.Envelope
		vocabularies codec.Vocabularies
	}{
		{
			// Rule 3: a defined op_type whose body the vocabulary rejects.
			name: "defined op type, schema-invalid body",
			env: codec.Envelope{
				ObjectID:   testSchemaObjectID,
				ObjectType: "schema",
				OpType:     "define-field",
				OpVersion:  1,
				Body:       json.RawMessage(`{"field":"title","op_type":"create","op_version":"1","strategy":"colour","type":"widget"}`),
			},
		},
		{
			// Rule 4: an op_type and op_version no build of writ this old
			// defines — what an op written by a newer writ looks like on the
			// way through the projection.
			name: "undefined op type and future op version",
			env: codec.Envelope{
				ObjectID:   testSchemaObjectID,
				ObjectType: "schema",
				OpType:     "annotate",
				OpVersion:  2,
				Body:       json.RawMessage(`{"annotation":"written by a newer writ"}`),
			},
		},
		{
			// Tier 4: an object type no schema in the log declares.
			name: "object type no schema declares",
			env: codec.Envelope{
				ObjectID:   "g-1",
				ObjectType: "gadget",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"written against a schema this repo has not fetched"}`),
			},
			vocabularies: widgetVocabulary(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := codec.EncodePayload(tc.env); err != nil {
				t.Fatalf("EncodePayload rejected an op it must still re-encode: %v", err)
			}
			if _, err := codec.BuildCommit(tc.env, testAuthor(), nil, tc.vocabularies); err == nil {
				t.Fatal("BuildCommit accepted the same op EncodePayload re-encodes")
			}
		})
	}
}

// TestEveryShippedVocabularyIsValidated is the exhaustiveness guard. Writ
// ships one vocabulary schema and registers one, and this walks the shipped
// schemas rather than a hand-written list, so a second one added to
// spec/schemas/ without a vocabularySchemaFiles entry — validated against
// nothing, silently — fails here.
//
// The probe is an empty create body: every vocabulary requires at least one
// field on create, so a registered schema rejects it and an unregistered one
// returns nil.
func TestEveryShippedVocabularyIsValidated(t *testing.T) {
	for objectType, file := range shippedVocabularies(t) {
		err := codec.ValidateBody(codec.Envelope{
			ObjectID:   "obj-1",
			ObjectType: objectType,
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{}`),
		}, nil)
		if err == nil {
			t.Errorf("object type %q has a vocabulary schema in spec/schemas/%s, but the codec validates ops of that type against nothing — register it in vocabularySchemaFiles",
				objectType, file)
		}
	}
}

// shippedVocabularies maps each vocabulary schema shipped in spec/schemas/ to
// its file name, keyed by the object type it governs.
//
// A vocabulary is a schema that extends the op envelope. Identifying them that
// way is the fix for the hole this guard used to have: it previously treated
// "an object_type const was found where I looked" as the definition, so a
// vocabulary that pinned its type in a third place was skipped in silence
// rather than reported. Now the skip and the failure are different outcomes,
// and only support schemas are skipped.
func shippedVocabularies(t *testing.T) map[string]string {
	t.Helper()

	entries, err := spec.FS.ReadDir("schemas")
	if err != nil {
		t.Fatalf("read schemas dir: %v", err)
	}

	vocabularies := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := spec.FS.ReadFile("schemas/" + entry.Name())
		if err != nil {
			t.Fatalf("read schema %s: %v", entry.Name(), err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse schema %s: %v", entry.Name(), err)
		}
		if entry.Name() == envelopeSchemaFileName || !refsEnvelopeSchema(doc) {
			// A support schema: op-envelope itself, identifiers, anchor,
			// resolution. None of them governs an object type.
			continue
		}

		objectType, ok := pinnedObjectType(doc)
		if !ok {
			t.Errorf("spec/schemas/%s extends the op envelope, so it is a vocabulary, but no object_type const was found in it — this test cannot tell which object type it governs, so it cannot tell whether the codec validates that type. Pin object_type with a const, or teach pinnedObjectType where this schema puts it",
				entry.Name())
			continue
		}
		if prior, dup := vocabularies[objectType]; dup {
			t.Errorf("object type %q is governed by two schemas, %s and %s", objectType, prior, entry.Name())
		}
		vocabularies[objectType] = entry.Name()
	}

	if len(vocabularies) == 0 {
		t.Fatal("found no vocabulary schemas to check")
	}
	return vocabularies
}

// sortedVocabularies lists the shipped vocabularies' object types in a stable
// order, so table-driven cases run the same way every time.
func sortedVocabularies(t *testing.T) []string {
	t.Helper()

	vocabularies := shippedVocabularies(t)
	types := make([]string, 0, len(vocabularies))
	for objectType := range vocabularies {
		types = append(types, objectType)
	}
	sort.Strings(types)
	return types
}

// BenchmarkProducerPath measures what the producer check costs against the work
// it sits next to: canonical encoding, which every append already pays for.
func BenchmarkProducerPath(b *testing.B) {
	// The bootstrap tier's envelope: `schema`, the one object type writ
	// validates against an embedded JSON Schema.
	env := codec.Envelope{
		ObjectID:   "0123456789abcdef0123456789abcdef",
		ObjectType: "schema",
		OpType:     "define-field",
		OpVersion:  1,
		Body:       json.RawMessage(`{"field":"title","op_type":"create","op_version":"1","strategy":"lww","type":"widget","value_type":"string"}`),
	}
	author := testAuthor()

	b.Run("EncodePayload", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := codec.EncodePayload(env); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ValidateBody", func(b *testing.B) {
		raw, err := codec.EncodePayload(env)
		if err != nil {
			b.Fatal(err)
		}
		withRaw := env
		withRaw.Raw = raw
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := codec.ValidateBody(withRaw, nil); err != nil {
				b.Fatal(err)
			}
		}
	})

	// ValidateBody/LogSourced is tier 2 (spec/op-envelope.md §Producer
	// validation): a consumer-declared object type validated against a
	// log-sourced codec.Vocabularies (a resolved schema object's
	// declaration) instead of against the embedded JSON Schema tier 1 uses.
	// The resolution itself (writ.VocabulariesFromSchemas, which walks the
	// log) is not part of what this measures — engine/schema_bench_test.go's
	// BenchmarkVocabulariesCache covers that cost — this is purely
	// validateAgainstLogVocabulary's per-op cost once a Vocabularies value
	// is already in hand, exactly as dag.Store.Append pays it on every
	// append after Append's own once-per-call resolve.
	b.Run("ValidateBody/LogSourced", func(b *testing.B) {
		logEnv := codec.Envelope{
			ObjectID:   "0123456789abcdef0123456789abcdef",
			ObjectType: "widget",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Add calculator functions","description":"Initial draft of addition"}`),
		}
		raw, err := codec.EncodePayload(logEnv)
		if err != nil {
			b.Fatal(err)
		}
		withRaw := logEnv
		withRaw.Raw = raw
		vocabularies := widgetVocabulary()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := codec.ValidateBody(withRaw, vocabularies); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("BuildCommit", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := codec.BuildCommit(env, author, nil, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}

const (
	envelopeSchemaFileName = "op-envelope.schema.json"
	envelopeSchemaRef      = "https://writ.dev/spec/" + envelopeSchemaFileName
)

// refsEnvelopeSchema reports whether a schema document extends the op envelope
// schema, which is what makes it a vocabulary rather than a support schema.
//
// This is the identifying property, and the previous version of this test used
// the wrong one: it treated "an object_type const was found" as the test for
// being a vocabulary, so a vocabulary that pinned its object_type anywhere the
// extractor did not look was silently skipped rather than reported. A schema
// is a vocabulary because of what it extends, and every shipped one says so.
func refsEnvelopeSchema(node any) bool {
	switch n := node.(type) {
	case map[string]any:
		if ref, ok := n["$ref"].(string); ok && ref == envelopeSchemaRef {
			return true
		}
		for _, v := range n {
			if refsEnvelopeSchema(v) {
				return true
			}
		}
	case []any:
		for _, v := range n {
			if refsEnvelopeSchema(v) {
				return true
			}
		}
	}
	return false
}

// pinnedObjectType reports the object type a vocabulary schema fixes with a
// const, wherever in the document it puts it: the shipped schemas use the
// top-level properties map, an allOf branch, and an if branch, and searching
// the whole document costs nothing and cannot be outgrown by a fourth style.
// A vocabulary whose type still cannot be read fails the caller loudly at the
// call site rather than being skipped.
func pinnedObjectType(doc map[string]any) (string, bool) {
	return findConstObjectType(doc)
}

func findConstObjectType(node any) (string, bool) {
	switch n := node.(type) {
	case map[string]any:
		if props, ok := n["properties"].(map[string]any); ok {
			if ot, ok := props["object_type"].(map[string]any); ok {
				if t, ok := ot["const"].(string); ok {
					return t, true
				}
			}
		}
		for _, key := range sortedKeys(n) {
			if t, ok := findConstObjectType(n[key]); ok {
				return t, true
			}
		}
	case []any:
		for _, v := range n {
			if t, ok := findConstObjectType(v); ok {
				return t, true
			}
		}
	}
	return "", false
}

// constsUnderProperty collects every const a schema pins for the named
// property, wherever in the document it appears — the shipped vocabularies put
// their op_type consts in if branches nested under then/allOf, and their
// op_version const in the top-level if. Values are returned sorted, in their
// JSON text form, with duplicates removed.
func constsUnderProperty(doc map[string]any, property string) []string {
	found := map[string]bool{}
	var walk func(node any)
	walk = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			if props, ok := n["properties"].(map[string]any); ok {
				if prop, ok := props[property].(map[string]any); ok {
					if c, ok := prop["const"]; ok {
						found[constText(c)] = true
					}
				}
			}
			for _, key := range sortedKeys(n) {
				walk(n[key])
			}
		case []any:
			for _, v := range n {
				walk(v)
			}
		}
	}
	walk(doc)

	values := make([]string, 0, len(found))
	for v := range found {
		values = append(values, v)
	}
	sort.Strings(values)
	return values
}

// constText renders a JSON const value as text, so string and numeric consts
// can be compared the same way.
func constText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case float64:
		return strconv.FormatFloat(c, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", c)
	}
}

// sortedKeys keeps the walk deterministic, so a schema that somehow pinned two
// different object types reports the same one on every run.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
