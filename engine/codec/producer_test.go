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

// TestBuildCommitRejectsSchemaInvalidBody pins the producer obligation from
// spec/op-envelope.md: an op whose body violates its vocabulary schema is never
// built, so it is never signed and never appended. The three named cases are
// the instances that reached production as separate tickets (WRIT-114/115/119
// for person identifiers, and the unguarded verdict enum noted on PR #88).
func TestBuildCommitRejectsSchemaInvalidBody(t *testing.T) {
	overLongValue := strings.Repeat("a", 321)

	cases := []struct {
		name string
		env  codec.Envelope
	}{
		{
			name: "over-long person id",
			env: codec.Envelope{
				ObjectID:   "rev-1",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body: json.RawMessage(`{"revision":"` + strings.Repeat("0", 40) +
					`","verdict":"approve","subject":"email:` + overLongValue + `"}`),
			},
		},
		{
			name: "empty person id",
			env: codec.Envelope{
				ObjectID:   "rev-1",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body: json.RawMessage(`{"revision":"` + strings.Repeat("0", 40) +
					`","verdict":"approve","subject":""}`),
			},
		},
		{
			name: "invalid verdict enum",
			env: codec.Envelope{
				ObjectID:   "rev-1",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body: json.RawMessage(`{"revision":"` + strings.Repeat("0", 40) +
					`","verdict":"bogus"}`),
			},
		},
		{
			name: "review create missing title",
			env: codec.Envelope{
				ObjectID:   "rev-1",
				ObjectType: "review",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"description":"no title"}`),
			},
		},
		{
			name: "comment create missing subject",
			env: codec.Envelope{
				ObjectID:   "c-1",
				ObjectType: "comment",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"text":"hello"}`),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := codec.BuildCommit(tc.env, testAuthor(), nil)
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

// knownCreateBodies is a minimal, schema-valid create body for each vocabulary
// writ registers, so the forward-compatibility cases below can be driven
// against every one of them rather than against whichever one happens to be
// most permissive.
var knownCreateBodies = map[string]string{
	"review":         `{"title":"Initial"}`,
	"comment":        `{"subject":{"object_id":"rev-1","object_type":"review"},"text":"hello"}`,
	"issue":          `{"title":"Initial"}`,
	"project":        `{"title":"Initial"}`,
	"cycle":          `{"ends_at":"2026-02-01T00:00:00Z","starts_at":"2026-01-01T00:00:00Z","title":"Sprint 1"}`,
	"workflow-state": `{"name":"Todo","position":"V","type":"unstarted"}`,
	"label":          `{"name":"bug"}`,
	"document":       `{"title":"Initial"}`,
	"section":        `{"body":"Initial","document_id":"0123456789abcdef0123456789abcdef","position":"V"}`,
	"settings":       `{"name":"Initial"}`,
}

// TestBuildCommitAcceptsUnknownFieldsInEveryVocabulary asserts the producer
// check did not narrow the unknown-field tolerance that every vocabulary
// shares (spec/forward-compatibility.md). It runs against all six registered
// vocabularies: an earlier version of this test covered only review, the most
// permissive one, so it could pass without touching the others at all.
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
			}, testAuthor(), nil); err != nil {
				t.Fatalf("BuildCommit rejected an unknown field a producer must still write: %v", err)
			}
		})
	}
}

// TestBuildCommitAcceptsForeignObjectTypes asserts the registry miss is not a
// rejection: an object type this build has never heard of must still build,
// because a reader has to tolerate it and writ's own producer never emits one.
func TestBuildCommitAcceptsForeignObjectTypes(t *testing.T) {
	if _, err := codec.BuildCommit(codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "sprocket",
		OpVersion:  7,
		Body:       json.RawMessage(`{"anything":[1,2,3]}`),
	}, testAuthor(), nil); err != nil {
		t.Fatalf("BuildCommit rejected an object type it has never heard of: %v", err)
	}
}

// TestVocabularySchemasTolerateUnknownOpTypeAndVersion pins reader safety, the
// half of WRIT-148 that lives in the schemas: every shipped vocabulary gates
// its body rules on op_version 1, so an op carrying an op type it never heard
// of, or a version it does not implement, is a *valid instance* of it.
//
// This is what a third party validating a foreign op against a published
// vocabulary schema depends on, and until WRIT-148 comment.schema.json was the
// one vocabulary that broke it — it pinned op_version and an op_type enum in an
// unconditional allOf, contradicting five sibling vocabularies, the ten
// unknown-op-type/future-version vectors their corpora ship, and
// spec/comments.md §Forward compatibility. It goes through the schemas alone,
// not through ValidateBody, because ValidateBody also enforces the producer
// rules that TestBuildCommitRefusesOpTypesItDoesNotDefine covers.
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
	"review":         "aproval",
	"comment":        "resolv",
	"issue":          "set-stat",
	"project":        "add-isue",
	"cycle":          "set-date",
	"workflow-state": "creat",
	"label":          "creat",
	"document":       "creat",
	"section":        "mov",
	"settings":       "st",
}

// TestBuildCommitRefusesOpTypesItDoesNotDefine pins producer rule 4
// (spec/op-envelope.md §Producer validation) across every vocabulary writ
// registers: an op_type or op_version this build does not define for an object
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
				if _, err := codec.BuildCommit(tc.env, testAuthor(), nil); err == nil {
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

		var inSchema []string
		if file == "document-ops.schema.json" {
			if objectType == "document" {
				inSchema = constsUnderProperty(doc["then"].(map[string]any), "op_type")
			} else if objectType == "section" {
				inSchema = constsUnderProperty(doc["else"].(map[string]any), "op_type")
			}
		} else {
			inSchema = constsUnderProperty(doc, "op_type")
		}
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
// the single vocabularyOpVersion constant: all six vocabularies gate their body
// rules on the same version this build writes. The first vocabulary to ship a
// v2 fails here, which is the signal to make the constant a per-object-type
// set rather than to widen it.
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
// one — it is the other half of the guard.
func TestEncodePayloadDoesNotValidateBody(t *testing.T) {
	cases := []struct {
		name string
		env  codec.Envelope
	}{
		{
			// Rule 3: a defined op_type whose body the vocabulary rejects.
			name: "defined op type, schema-invalid body",
			env: codec.Envelope{
				ObjectID:   "rev-1",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"not-an-oid","verdict":"bogus"}`),
			},
		},
		{
			// Rule 4: an op_type and op_version no build of writ this old
			// defines — what an op written by a newer writ looks like on the
			// way through the projection.
			name: "undefined op type and future op version",
			env: codec.Envelope{
				ObjectID:   "rev-1",
				ObjectType: "review",
				OpType:     "annotate",
				OpVersion:  2,
				Body:       json.RawMessage(`{"annotation":"written by a newer writ"}`),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := codec.EncodePayload(tc.env); err != nil {
				t.Fatalf("EncodePayload rejected an op it must still re-encode: %v", err)
			}
			if _, err := codec.BuildCommit(tc.env, testAuthor(), nil); err == nil {
				t.Fatal("BuildCommit accepted the same op EncodePayload re-encodes")
			}
		})
	}
}

// TestEveryShippedVocabularyIsValidated is the exhaustiveness guard. Before
// WRIT-129 the codec registered review and comment and silently validated
// nothing for issue, project, cycle and repo, even though spec/schemas/ ships a
// schema for each. This walks the shipped schemas rather than a hand-written
// list, so adding a vocabulary without registering it fails here.
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
		})
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

		if entry.Name() == "document-ops.schema.json" {
			vocabularies["document"] = entry.Name()
			vocabularies["section"] = entry.Name()
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
	env := codec.Envelope{
		ObjectID:   "0123456789abcdef0123456789abcdef",
		ObjectType: "review",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Add calculator functions","description":"Initial draft of addition"}`),
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
			if err := codec.ValidateBody(withRaw); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("BuildCommit", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := codec.BuildCommit(env, author, nil); err != nil {
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
