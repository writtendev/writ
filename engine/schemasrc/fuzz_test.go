package schemasrc

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// FuzzParse asserts Parse never panics on any input, that any *File it
// returns survives Compile without panicking either, and that Format
// preserves every comment in src, unmangled and with the same
// multiplicity — run under `make fuzz` (Makefile's FUZZTIME-bounded
// target). Seeded from both corpora so the fuzzer starts from inputs that
// are already close to the grammar's edges.
//
// The comment-multiset assertion is this package's in-house closure of
// WRIT-187 PR #157's round-1 through round-3 review findings: round 1
// found Format silently dropping a comment by *position* (a same-line
// trailing comment in five spots); round 2 found it dropping one by
// *value* (a bare `#` with an empty body, which a `c != ""` guard
// mistook for "no comment"); round 3 found it silently rewriting one by
// *encoding* (an invalid-UTF-8 byte in a comment body, repaired to
// U+FFFD rather than rejected). Each round's regression net was an
// enumeration — commentPositions in format_test.go, collectComments in
// corpus_test.go — that happened not to cover the new axis; this asserts
// the property directly instead, so a fourth axis nobody has thought of
// yet fails here rather than shipping silently.
func FuzzParse(f *testing.F) {
	for _, dir := range []string{"testdata/valid", "testdata/invalid"} {
		matches, err := filepath.Glob(filepath.Join(dir, "*.schema"))
		if err != nil {
			f.Fatalf("globbing %s: %v", dir, err)
		}
		for _, m := range matches {
			src, err := os.ReadFile(m)
			if err != nil {
				f.Fatalf("reading %s: %v", m, err)
			}
			f.Add(src)
		}
	}
	f.Add([]byte(""))
	f.Add([]byte("namespace"))
	f.Add([]byte("namespace \xff\xfe"))
	f.Add([]byte("namespace acme\ndescription \"caf\xe9\"\n"))
	f.Add([]byte("namespace acme  # caf\xe9\n"))

	f.Fuzz(func(t *testing.T, src []byte) {
		file, err := Parse("fuzz.schema", src)
		if err != nil {
			return
		}
		// A *File Parse returns without error must survive Compile without
		// panicking. Compile is allowed to reject it (an untyped AST built
		// from adversarial input can still fail spec.ValidateFieldRule),
		// but never allowed to panic.
		_, _ = Compile(file, "sch-fuzz")

		// src lexed cleanly (Parse succeeded above), so Format's own
		// internal re-Parse of the same bytes must too; a failure here
		// would mean Parse is non-deterministic, not a legitimate rejection.
		formatted, err := Format("fuzz.schema", src)
		if err != nil {
			t.Fatalf("Format failed on input Parse just accepted: %v\nsrc: %q", err, src)
		}
		before := commentMultiset(t, src)
		after := commentMultiset(t, formatted)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("Format did not preserve the comment multiset\nbefore: %v\nafter:  %v\nsrc:\n%s\nformatted:\n%s", before, after, src, formatted)
		}
	})
}

// commentMultiset returns every comment token's body (the text after '#',
// trimmed, per lex.go's lexComment), keyed by body with its count as
// value — a multiset, since a source file legitimately repeats the same
// comment body (two bare `#`s, or two lines both saying "TODO"). It walks
// the real lexer, unlike corpus_test.go's collectComments, which scans
// raw text and cannot tell a comment from a '#' inside a string literal;
// that shortcut is fine for the fixed, known corpus it checks, but not for
// arbitrary fuzzer-discovered input.
func commentMultiset(t *testing.T, src []byte) map[string]int {
	t.Helper()
	toks, err := lexAll("fuzz.schema", src)
	if err != nil {
		t.Fatalf("lexAll: %v (Parse just accepted this input)", err)
	}
	m := make(map[string]int)
	for _, tok := range toks {
		if tok.Kind == tokComment {
			m[tok.Text]++
		}
	}
	return m
}
