// External test package on purpose: it imports the spec package for the
// vector corpus, and spec's own external tests import canonicaljson —
// keeping this file out of package canonicaljson means the spec package
// stays free to grow non-test engine imports without creating a test
// import cycle.
package canonicaljson_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/writtendev/writ/internal/codec/canonicaljson"
	"github.com/writtendev/writ/spec"
)

// rejectionSubstring maps each normative rejection category from
// spec/canonicalization.md to a substring this implementation's error
// for it must contain. Matching the category — not just "some error" —
// keeps a vector from silently passing for the wrong reason (e.g. a
// typo'd lone-surrogate input failing as a syntax error while the
// surrogate scan goes unexercised).
var rejectionSubstring = map[string]string{
	"duplicate-key":     "duplicate object key",
	"lone-surrogate":    "lone surrogate",
	"non-finite-number": "invalid number",
	"not-one-value":     "trailing data",
}

func loadVectors(t *testing.T) []spec.Vector {
	t.Helper()
	vecs, err := spec.CanonicalizationVectors()
	if err != nil {
		t.Fatal(err)
	}
	return vecs
}

func TestMarshalVectors(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			got, err := canonicaljson.Marshal([]byte(v.Input))
			if v.Error != "" {
				want, known := rejectionSubstring[v.Error]
				if !known {
					t.Fatalf("vector has unknown rejection category %q", v.Error)
				}
				if err == nil {
					t.Fatalf("Marshal(%q) = %q, want %s rejection", v.Input, got, v.Error)
				}
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Marshal(%q) rejected with %q, want a %s rejection (containing %q)", v.Input, err, v.Error, want)
				}
				return
			}
			if err != nil {
				t.Fatalf("Marshal(%q): %v", v.Input, err)
			}
			if string(got) != *v.Canonical {
				t.Errorf("Marshal(%q) = %q, want %q", v.Input, got, *v.Canonical)
			}
		})
	}
}

// A canonical encoding must be a fixed point of itself: canonicalizing
// already-canonical bytes changes nothing. Signing relies on this.
func TestMarshalIdempotent(t *testing.T) {
	for _, v := range loadVectors(t) {
		if v.Error != "" {
			continue
		}
		t.Run(v.Name, func(t *testing.T) {
			again, err := canonicaljson.Marshal([]byte(*v.Canonical))
			if err != nil {
				t.Fatalf("Marshal(%q): %v", *v.Canonical, err)
			}
			if string(again) != *v.Canonical {
				t.Errorf("Marshal(%q) = %q, want %q (not idempotent)", *v.Canonical, again, *v.Canonical)
			}
		})
	}
}

func TestMarshalRejectsTrailingData(t *testing.T) {
	for _, in := range []string{`{"a":1} garbage`, `{"a":1}}`, `[1]]`, `1]`, `null}`} {
		if _, err := canonicaljson.Marshal([]byte(in)); err == nil {
			t.Errorf("Marshal(%q) accepted trailing data after the JSON value", in)
		}
	}
}

func TestMarshalRejectsInvalidJSON(t *testing.T) {
	if _, err := canonicaljson.Marshal([]byte(`{"a":`)); err == nil {
		t.Fatal("Marshal accepted truncated JSON")
	}
}

// Invalid UTF-8 can't be expressed in the vector file (a JSON string can
// only carry valid text), so the rule that Marshal rejects it rather than
// letting encoding/json substitute U+FFFD is exercised directly here.
func TestMarshalRejectsInvalidUTF8(t *testing.T) {
	if _, err := canonicaljson.Marshal([]byte("{\"s\":\"a\xff b\"}")); err == nil {
		t.Fatal("Marshal accepted input that is not valid UTF-8")
	}
}

// 1e400 is valid JSON number syntax; it overflows float64 to +Inf, and
// json.Number.Float64 reports that overflow as an error, so Marshal
// rejects it before encodeNumber's own IsNaN/IsInf check ever runs.
func TestMarshalRejectsNonFiniteNumbers(t *testing.T) {
	if _, err := canonicaljson.Marshal([]byte(`1e400`)); err == nil {
		t.Fatal("Marshal accepted a number that overflows to +Inf")
	}
}

// Nesting depth is capped so a long run of '[' bytes returns an error
// instead of overflowing the goroutine stack (a fatal, unrecoverable
// crash). The boundary is pinned exactly: 10000 levels accepted, 10001
// rejected — the same ceiling encoding/json's Decode enforces, so
// canonical bytes are never something json.Unmarshal refuses to parse.
// The boundary can't live in the vector file (it would be tens of
// kilobytes of brackets), so this test is its enforcement.
func TestMarshalNestingDepthBoundary(t *testing.T) {
	// Both shapes at exactly the maximum: an empty innermost array, and
	// one holding a scalar. The second is the case that catches a cap
	// that counts leaves — scalars don't nest, so a leaf inside 10000
	// brackets is still 10000 levels, and json.Unmarshal accepts it.
	for name, in := range map[string]string{
		"empty innermost":  strings.Repeat("[", 10_000) + strings.Repeat("]", 10_000),
		"scalar innermost": strings.Repeat("[", 10_000) + "1" + strings.Repeat("]", 10_000),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := canonicaljson.Marshal([]byte(in))
			if err != nil {
				t.Fatalf("Marshal rejected 10000 nesting levels, the spec'd maximum: %v", err)
			}
			var v any
			if err := json.Unmarshal(got, &v); err != nil {
				t.Fatalf("json.Unmarshal rejected canonical bytes Marshal accepted: %v", err)
			}
		})
	}
	// One level deeper is rejected before EOF even matters — the check
	// fires at the 10001st open bracket.
	if _, err := canonicaljson.Marshal([]byte(strings.Repeat("[", 10_001))); err == nil {
		t.Fatal("Marshal accepted input nested beyond the 10000-level cap")
	}
	// ...and encoding/json agrees the 10001st level is too deep, which
	// is the property the shared bound exists to preserve.
	var v any
	tooDeep := strings.Repeat("[", 10_001) + strings.Repeat("]", 10_001)
	if err := json.Unmarshal([]byte(tooDeep), &v); err == nil {
		t.Fatal("encoding/json accepted 10001 nesting levels; the cap no longer matches it")
	}
}

// checkSurrogateEscapes must not flag surrogate-range \u sequences that
// appear outside a string's escape structure — an escaped backslash
// followed by literal "ud800" text is not an escape.
func TestSurrogateScanIgnoresEscapedBackslash(t *testing.T) {
	got, err := canonicaljson.Marshal([]byte(`{"s":"\\ud800"}`))
	if err != nil {
		t.Fatalf("Marshal rejected a literal backslash followed by text: %v", err)
	}
	if want := `{"s":"\\ud800"}`; string(got) != want {
		t.Errorf("Marshal = %q, want %q", got, want)
	}
}
