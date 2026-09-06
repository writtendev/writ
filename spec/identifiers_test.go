package spec_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	unicodecases "golang.org/x/text/cases"
	unicodenorm "golang.org/x/text/unicode/norm"

	"github.com/writtendev/writ/spec"
)

const identifiersSchemaID = "https://writ.dev/spec/identifiers.schema.json"

// compileIdentifiersSchema compiles the identifiers schema; compilation also
// validates it against the draft 2020-12 meta-schema.
func compileIdentifiersSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := spec.FS.ReadFile("schemas/identifiers.schema.json")
	if err != nil {
		t.Fatalf("reading schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoding schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(identifiersSchemaID, doc); err != nil {
		t.Fatalf("adding schema resource: %v", err)
	}
	sch, err := c.Compile(identifiersSchemaID)
	if err != nil {
		t.Fatalf("compiling schema: %v", err)
	}
	return sch
}

// parseReference parses a reference string into designator and object ID,
// mirroring the grammar in spec/identifiers.md §Reference grammar.
func parseReference(ref string) (designator string, objectID string, err error) {
	if ref == "" {
		return "", "", fmt.Errorf("reference is empty")
	}
	parts := strings.Split(ref, "#")
	switch len(parts) {
	case 1:
		return "", parts[0], nil
	case 2:
		if parts[0] == "" {
			return "", "", fmt.Errorf("reference %q has empty repo designator", ref)
		}
		if parts[1] == "" {
			return "", "", fmt.Errorf("reference %q has empty object id", ref)
		}
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("reference %q contains multiple '#' separators", ref)
	}
}

func TestIdentifiersSchemaCompiles(t *testing.T) {
	compileIdentifiersSchema(t)
}

func TestValidReferenceVectors(t *testing.T) {
	sch := compileIdentifiersSchema(t)
	for _, name := range readDirNames(t, "testdata/references/valid") {
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/references/valid/" + name)
			if err != nil {
				t.Fatal(err)
			}

			// Schema validation
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("decoding vector JSON: %v", err)
			}
			if err := sch.Validate(inst); err != nil {
				t.Fatalf("schema validation failed: %v", err)
			}

			// Parse execution
			var vec struct {
				Reference string `json:"reference"`
			}
			if err := json.Unmarshal(raw, &vec); err != nil {
				t.Fatalf("unmarshaling vector: %v", err)
			}
			if _, _, err := parseReference(vec.Reference); err != nil {
				t.Fatalf("parseReference error: %v", err)
			}
		})
	}
}

func TestInvalidReferenceVectors(t *testing.T) {
	sch := compileIdentifiersSchema(t)

	rawIndex, err := spec.FS.ReadFile("testdata/references/invalid/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatalf("decoding index.json: %v", err)
	}

	names := readDirNames(t, "testdata/references/invalid")
	files := make(map[string]bool)
	for _, name := range names {
		if name != "index.json" {
			files[name] = true
		}
	}
	for name := range index {
		if !files[name] {
			t.Errorf("index.json lists %s but the file does not exist", name)
		}
	}

	for name := range files {
		entry, ok := index[name]
		if !ok {
			t.Errorf("%s has no index.json entry recording its expected rejection", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/references/invalid/" + name)
			if err != nil {
				t.Fatal(err)
			}
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("decoding vector: %v", err)
			}
			schemaErr := sch.Validate(inst)
			switch entry.Kind {
			case "schema":
				if schemaErr == nil {
					t.Errorf("schema accepted it; expected rejection: %s", entry.Reason)
				}
			default:
				t.Errorf("index.json kind %q unknown (want schema)", entry.Kind)
			}
		})
	}
}

func TestPersonIdentifierNormalization(t *testing.T) {
	// Normalization rule (spec/identifiers.md §Normalization rules): trim the
	// identifier, split at the FIRST colon, lowercase the scheme, trim the
	// value and fold it with fold(v) = NFC(toCasefold(NFC(v))).
	// Comparison rule: equal(A, B) <=> norm(A) == norm(B)
	cases := []struct {
		a     string
		b     string
		equal bool
	}{
		{"email:alice@example.com", "email:alice@example.com", true},
		{"Email:Alice@Example.COM", "email:alice@example.com", true},
		{"  email:alice@example.com  ", "email:alice@example.com", true},
		{"\t\n EMAIL:Alice@Example.COM \r\n", "email:alice@example.com", true},
		{"email:alice@example.com", "email:bob@example.com", false},
		{"email:alice@example.com", "email:alice@sub.example.com", false},
		{"email:dev+1@example.com", "email:dev+1@example.com", true},
		{"EMAIL:DEV+1@EXAMPLE.COM", "email:dev+1@example.com", true},
		// Schemes never unify, however obvious the human identity.
		{"email:alice@example.com", "user:alice", false},
		{"user:alice", "keybase:alice", false},
		// An unknown scheme compares byte-wise like any other.
		{"KeyBase:Alice", "keybase:alice", true},
		// The value's own colon belongs to the value.
		{`email:"a:b"@example.com`, `email:"a:b"@example.com`, true},
		// The folding algorithm, step by step.
		{"user:Jos\u0065\u0301", "user:Jos\u00e9", true}, // NFC composes a decomposed value
		{"user:\u0130", "user:\u0069\u0307", true},       // the pinned case fold
		{"user:\u0130", "user:i", false},                 // and not the simple-lowercase answer
		{"user:\u1e9e", "user:ss", true},                 // full folding, not simple
		{"user:stra\u00dfe", "user:STRASSE", true},       // both spellings of the word fold together
		{"user:\u017f\u0301", "user:\u015b", true},       // fold, then compose again
		{"user:\u0041\u030a", "user:\u00c5", true},       // precomposed and decomposed ring
		{"user:\u212b", "user:\u00c5", true},             // and the Angstrom sign, which NFC unifies
		{"user:\u00e9", "user:e", false},                 // composing is not stripping
		{"user:alice", "user:\u0410lice", false},         // Cyrillic A is not Latin A
	}

	// Deliberately spelled out here rather than calling any implementation:
	// this test checks the prose rule in spec/identifiers.md, so it must be
	// able to fail when every implementation agrees on the wrong thing.
	// TestReffoldNormalizePersonMatchesEngine is the test that binds the
	// implementations to each other.
	//
	// This is the algorithm exactly as written, with no correction for the
	// four x/text behaviours the implementations work around: a correction
	// here would make this a third copy of the fix rather than an independent
	// reading of the rule. Its cases therefore stay off that surface —
	// Cherokee U+13A0..U+13F5, supplementary starters, runs of more than
	// thirty non-starters, and composition across a ccc-0 blocker — all of
	// which are swept exhaustively against CPython in engine/internal/person's
	// differential tests instead.
	fold := func(v string) string {
		return unicodenorm.NFC.String(unicodecases.Fold().String(unicodenorm.NFC.String(v)))
	}
	norm := func(s string) string {
		s = strings.TrimSpace(s)
		i := strings.Index(s, ":")
		if i < 0 {
			return fold(s)
		}
		return strings.ToLower(s[:i]) + ":" + fold(strings.TrimSpace(s[i+1:]))
	}

	for _, tc := range cases {
		normA := norm(tc.a)
		normB := norm(tc.b)
		gotEqual := normA == normB
		if gotEqual != tc.equal {
			t.Errorf("equal(%q, %q) = %v, want %v (norm(%q)=%q, norm(%q)=%q)",
				tc.a, tc.b, gotEqual, tc.equal, tc.a, normA, tc.b, normB)
		}
	}
}

// personVector is one case from testdata/persons/valid. scheme and value are
// the halves of the *normalized* identifier: the bound and the grammar apply
// after normalization, so that is the form the vector describes.
type personVector struct {
	Identifier   string   `json:"identifier"`
	Scheme       string   `json:"scheme"`
	Value        string   `json:"value"`
	Normalized   string   `json:"normalized"`
	EqualTo      []string `json:"equal_to,omitempty"`
	DistinctFrom []string `json:"distinct_from,omitempty"`
}

// compilePersonIDSchema compiles a wrapper document whose single "person"
// property is the shared person-id definition, which is the only way to reach
// a $def with this validator.
func compilePersonIDSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	wrapper := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"type":    "object",
		"properties": map[string]any{
			"person": map[string]any{
				"$ref": identifiersSchemaID + "#/$defs/person-id",
			},
		},
		"required": []any{"person"},
	}
	rawIdent, err := spec.FS.ReadFile("schemas/identifiers.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	identDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawIdent))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddResource(identifiersSchemaID, identDoc); err != nil {
		t.Fatal(err)
	}
	if err := c.AddResource("https://writ.dev/test/person-wrapper.schema.json", wrapper); err != nil {
		t.Fatal(err)
	}
	compiled, err := c.Compile("https://writ.dev/test/person-wrapper.schema.json")
	if err != nil {
		t.Fatalf("compiling person wrapper schema: %v", err)
	}
	return compiled
}

func personSchemaAccepts(sch *jsonschema.Schema, id string) error {
	return sch.Validate(map[string]any{"person": id})
}

// TestValidPersonVectors drives every testdata/persons/valid vector through the
// reference implementation's own split and normalization — the code an
// independent implementer reads — and through the shared person-id schema.
func TestValidPersonVectors(t *testing.T) {
	sch := compilePersonIDSchema(t)
	for _, name := range readDirNames(t, "testdata/persons/valid") {
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/persons/valid/" + name)
			if err != nil {
				t.Fatal(err)
			}
			var vec personVector
			if err := json.Unmarshal(raw, &vec); err != nil {
				t.Fatalf("decoding vector: %v", err)
			}

			norm := spec.NormalizePerson(vec.Identifier)
			if norm != vec.Normalized {
				t.Errorf("norm(%q) = %q, want %q", vec.Identifier, norm, vec.Normalized)
			}

			scheme, value, ok := spec.SplitPerson(norm)
			if !ok {
				t.Fatalf("splitPerson(%q) reported no scheme", norm)
			}
			if scheme != vec.Scheme {
				t.Errorf("scheme of %q = %q, want %q", norm, scheme, vec.Scheme)
			}
			if value != vec.Value {
				t.Errorf("value of %q = %q, want %q", norm, value, vec.Value)
			}

			if err := personSchemaAccepts(sch, vec.Normalized); err != nil {
				t.Errorf("schema rejected the normalized identifier: %v", err)
			}

			for _, other := range vec.EqualTo {
				if got := spec.NormalizePerson(other); got != norm {
					t.Errorf("%q should denote the same person as %q, but normalizes to %q", other, vec.Identifier, got)
				}
			}
			for _, other := range vec.DistinctFrom {
				if got := spec.NormalizePerson(other); got == norm {
					t.Errorf("%q must not denote the same person as %q, but both normalize to %q", other, vec.Identifier, got)
				}
			}
		})
	}
}

// TestInvalidPersonVectors checks that the shared person-id schema rejects
// every testdata/persons/invalid vector, and that index.json accounts for each
// one — a rejection nobody wrote a reason for is a rejection nobody checked.
func TestInvalidPersonVectors(t *testing.T) {
	sch := compilePersonIDSchema(t)

	rawIndex, err := spec.FS.ReadFile("testdata/persons/invalid/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatalf("decoding index.json: %v", err)
	}

	files := make(map[string]bool)
	for _, name := range readDirNames(t, "testdata/persons/invalid") {
		if name != "index.json" {
			files[name] = true
		}
	}
	for name := range index {
		if !files[name] {
			t.Errorf("index.json lists %s but the file does not exist", name)
		}
	}

	for name := range files {
		entry, ok := index[name]
		if !ok {
			t.Errorf("%s has no index.json entry recording its expected rejection", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/persons/invalid/" + name)
			if err != nil {
				t.Fatal(err)
			}
			var vec personVector
			if err := json.Unmarshal(raw, &vec); err != nil {
				t.Fatalf("decoding vector: %v", err)
			}
			if err := personSchemaAccepts(sch, vec.Identifier); err == nil {
				t.Errorf("schema accepted %q; expected rejection: %s", vec.Identifier, entry.Reason)
			}
		})
	}
}

// TestPersonIDBoundRejectsRatherThanTruncates pins the difference between the
// two ways a length cap can be implemented. A validator that truncated to the
// bound would accept the over-long identifier and silently make it equal to a
// shorter one; that collapse is the attack the spec's first normative rule
// exists to stop, so the observable behaviour has to be a refusal.
func TestPersonIDBoundRejectsRatherThanTruncates(t *testing.T) {
	sch := compilePersonIDSchema(t)

	atLimit := "email:" + strings.Repeat("a", 320)
	overLimit := "email:" + strings.Repeat("a", 321)
	truncated := overLimit[:len(atLimit)]

	if err := personSchemaAccepts(sch, atLimit); err != nil {
		t.Fatalf("a 320-code-point value must validate: %v", err)
	}
	if err := personSchemaAccepts(sch, overLimit); err == nil {
		t.Fatal("a 321-code-point value must be refused")
	}
	// The truncation an implementation might be tempted to perform is a
	// different, already-taken identifier. Nothing may map the one onto the
	// other.
	if truncated != atLimit {
		t.Fatalf("test setup: truncating overLimit should reproduce atLimit")
	}
	if spec.NormalizePerson(overLimit) == atLimit {
		t.Error("normalization truncated an over-long identifier onto a shorter one")
	}
}

func TestPersonIdentifierSchema(t *testing.T) {
	compiled := compilePersonIDSchema(t)

	// The RFC 5321 ceiling exactly as the *value*: 64-octet local part, "@",
	// 255-octet domain. The bound applies to the value, so the whole identifier
	// is 326 characters here — inside the derived 353 whole-string bound.
	atLimitValue := strings.Repeat("a", 64) + "@" + strings.Repeat("b", 251) + ".com"
	if len(atLimitValue) != 320 {
		t.Fatalf("test setup: atLimitValue is %d characters, want 320", len(atLimitValue))
	}
	overLimitValue := strings.Repeat("a", 64) + "@" + strings.Repeat("b", 252) + ".com"
	if len(overLimitValue) != 321 {
		t.Fatalf("test setup: overLimitValue is %d characters, want 321", len(overLimitValue))
	}

	// maxLength counts code points, not bytes, and the engine guard counts them
	// the same way. The ASCII pair above cannot show the difference — there
	// bytes and code points coincide — so bracket the bound again with a
	// multi-byte value, where 320 characters are 640 bytes.
	atLimitMultiByte := strings.Repeat("é", 320)
	overLimitMultiByte := strings.Repeat("é", 321)
	if n := utf8.RuneCountInString(atLimitMultiByte); n != 320 || len(atLimitMultiByte) != 640 {
		t.Fatalf("test setup: atLimitMultiByte is %d characters / %d bytes, want 320 / 640", n, len(atLimitMultiByte))
	}

	atLimitScheme := strings.Repeat("a", 32)

	validCases := []string{
		"email:alice@example.com",
		"email:bob.builder+test@example.co.uk",
		"user:alice",
		"user:ci",
		"keybase:alice", // an unrecognized scheme is still well formed
		`email:"a:b"@example.com`,
		"x+ci.bot-2:alice",
		atLimitScheme + ":alice",
		"email:" + atLimitValue,
		"email:" + atLimitMultiByte,
	}
	for _, v := range validCases {
		if err := personSchemaAccepts(compiled, v); err != nil {
			t.Errorf("expected person %q to be valid, got %v", v, err)
		}
	}

	invalidCases := []any{
		"",                                 // no scheme, no value
		"alice@example.com",                // bare: no scheme
		"alice",                            // bare handle
		":alice",                           // empty scheme
		"user:",                            // empty value
		"Email:alice@example.com",          // scheme is not lowercase
		"2fa:alice",                        // scheme must start with [a-z]
		"my_scheme:alice",                  // underscore is outside the scheme charset
		strings.Repeat("a", 33) + ":alice", // scheme over 32 characters
		"email:" + overLimitValue,          // value over 320 code points
		"email:" + overLimitMultiByte,      // value over 320 code points, counted in code points
		"  email:alice@example.com  ",      // an op body carries the normalized form
		12345,                              // not a string
		nil,                                // null
		[]any{},                            // array
	}
	for _, inv := range invalidCases {
		inst := map[string]any{"person": inv}
		if err := compiled.Validate(inst); err == nil {
			t.Errorf("expected person %v to be invalid, but schema accepted it", inv)
		}
	}
}

func TestWriterIDVsPersonID(t *testing.T) {
	// Writer ID: 16 lowercase hex characters (device-scoped namespace under refs/writ/<writer-id>/)
	// Person ID: collaborative actor identity, scheme ":" value
	writerID := "0123456789abcdef"
	personID := "email:alice@example.com"

	isHex16 := func(s string) bool {
		if len(s) != 16 {
			return false
		}
		for _, c := range s {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return false
			}
		}
		return true
	}

	if !isHex16(writerID) {
		t.Errorf("writerID %q should match 16-hex format", writerID)
	}
	if isHex16(personID) {
		t.Errorf("personID %q should not match 16-hex format", personID)
	}
}
