package schemasrc

import (
	"regexp"
	"strings"
	"testing"
)

// TestKeywordsAreClosed pins the lexer's keyword table (lex.go) literally:
// adding a keyword fails this test by name, so growing the language is a
// deliberate, reviewed act rather than an incidental one — the same idiom
// spec/value_types_test.go's TestUntypedRulesAreNamed uses for value-type
// exceptions.
func TestKeywordsAreClosed(t *testing.T) {
	want := map[string]bool{
		"namespace":   true,
		"description": true,
		"type":        true,
		"op":          true,
		"deprecated":  true,
		"untyped":     true,
		"key":         true,
		"target":      true,
	}

	if len(keywords) != len(want) {
		t.Errorf("keywords has %d entries, want %d: %v", len(keywords), len(want), keywords)
	}
	for k := range want {
		if !keywords[k] {
			t.Errorf("keyword %q is missing from the lexer's keyword table", k)
		}
	}
	for k := range keywords {
		if !want[k] {
			t.Errorf("keyword %q was added to the lexer's keyword table; TestKeywordsAreClosed must be updated deliberately", k)
		}
	}
}

// TestReservationIsPositional pins the correspondence between keywords
// (the namespace/type-name/op-type-name slot's reserved-word set) and
// fieldReserved (the field-name/target(...) slot's): every word in
// keywords must be in fieldReserved iff it is "deprecated", and
// fieldReserved must contain no word that isn't in keywords. It never
// calls Parse, so it does not by itself pin what either slot actually
// does with a given word at runtime — that is pinned separately by
// contextual-keywords.schema (the positive case) and by
// deprecated-field-name.schema, deprecated-field-name-after-field.schema,
// and reserved-type-name.schema (the negative cases) in the invalid
// corpus. This is the companion to TestKeywordsAreClosed: that test pins
// the word list itself; this one pins which of those words fieldReserved
// carries forward, so the WRIT-204 relaxation (contextual keywords, not
// a shorter list) cannot silently widen or narrow fieldReserved without
// a test noticing.
func TestReservationIsPositional(t *testing.T) {
	for word := range keywords {
		wantFieldReserved := word == "deprecated"
		if fieldReserved[word] != wantFieldReserved {
			t.Errorf("fieldReserved[%q] = %v, want %v", word, fieldReserved[word], wantFieldReserved)
		}
	}
	for word := range fieldReserved {
		if !keywords[word] {
			t.Errorf("fieldReserved contains %q, which is not even in keywords", word)
		}
	}
}

var opVersionPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

// TestEmittedOpVersionIsCanonicalDecimal is the WRIT-186 round-1 bug's
// regression net on the producing side (spec/schema-ops.md §3.1):
// opVersionString is the single place in this package that formats an
// op_version body field, and every value it can be asked to format —
// including 1, 9, 10, and 100, which is where a naive implementation
// (fixed-width padding, or an off-by-one on the leading-zero check) tends
// to slip — must match the canonical decimal pattern.
func TestEmittedOpVersionIsCanonicalDecimal(t *testing.T) {
	for _, v := range []int64{1, 2, 9, 10, 11, 99, 100, 101, 999, 1000, 123456789} {
		got := opVersionString(v)
		if !opVersionPattern.MatchString(got) {
			t.Errorf("opVersionString(%d) = %q, does not match %s", v, got, opVersionPattern.String())
		}
	}
}

// TestQuoteStringEscapesEmbeddedNewline pins the property that actually
// keeps a hostile description from forging an extra line wherever
// Render's output later gets diffed: a raw U+000A inside the string
// quoteString renders must become the literal two characters `\n`, never
// a real line break passed through untouched.
//
// spec/schema-ops.md never restricts description content, so a
// non-conforming producer's `define-type`/`define-op`/`create` body can
// carry a raw newline in a description; schemasrc.Parse's own lexer
// rejects a bare newline inside a quoted string literal, so this can
// never come from a real writ.schema file, only from data already folded
// into the log this way. hostile is built from rune concatenation, not a
// literal newline in this file's source, matching the discipline
// cmd/writ's own hostile-input tests use.
//
// round 4 review of PR #185 (finding 3) found this property pinned only
// by a cmd/writ test that exercised it through two layers it did not
// need — schemasrc.Render and cmd/writ's own textsafe escape pass — and
// that kept passing even with the escape pass deleted entirely, because
// this case below, not that pass, is what delivers it. This test exists
// so the property has one owner, at the layer where it actually lives.
func TestQuoteStringEscapesEmbeddedNewline(t *testing.T) {
	sentinel := "forged addition"
	hostile := "line one" + string(rune(0x0A)) + sentinel

	got := quoteString(hostile)

	if strings.ContainsRune(got, 0x0A) {
		t.Fatalf("quoteString(%q) = %q, contains a raw newline — want the embedded U+000A escaped to the literal two characters `\\n`", hostile, got)
	}
	want := `"line one\nforged addition"`
	if got != want {
		t.Fatalf("quoteString(%q) = %q, want %q", hostile, got, want)
	}
}
