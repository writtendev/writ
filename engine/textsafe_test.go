package writ_test

import (
	"testing"

	"github.com/writtendev/writ/engine"
)

// escSeq builds the literal \uXXXX text an escape is expected to produce
// for the code point hex names, from plain ASCII pieces -- never a raw
// non-ASCII character or a literal backslash-u escape embedded in this
// source file (see internal/textsafe/textsafe_test.go's escapeSeq, same
// reasoning).
func escSeq(hex string) string {
	return "\\u" + hex
}

// TestEscapeForbidden pins EscapeForbidden as a thin pass-through to
// internal/textsafe.EscapeForbidden (WRIT-311 §3): clean text is returned
// unchanged, and a bidi override or a newline both escape to their \uXXXX
// form.
func TestEscapeForbidden(t *testing.T) {
	if got := writ.EscapeForbidden("alice@example.com"); got != "alice@example.com" {
		t.Errorf("EscapeForbidden(clean) = %q, want unchanged", got)
	}

	hostile := "email:alice" + string(rune(0x202E)) + "@evil.com"
	want := "email:alice" + escSeq("202e") + "@evil.com"
	if got := writ.EscapeForbidden(hostile); got != want {
		t.Errorf("EscapeForbidden(hostile) = %q, want %q", got, want)
	}

	withNewline := "line one\nline two"
	wantNewline := "line one" + escSeq("000a") + "line two"
	if got := writ.EscapeForbidden(withNewline); got != wantNewline {
		t.Errorf("EscapeForbidden(newline) = %q, want %q", got, wantNewline)
	}
}

// TestEscapeForbiddenKeepingNewlines pins the one difference from
// EscapeForbidden: U+000A passes through unescaped, while every other
// forbidden code point still escapes -- the shape both
// cmd/writ/store.go's escapeErrReport (when keepLineBreaks is set) and
// cmd/writ/schema.go's escapeRenderedSchemaSource need.
func TestEscapeForbiddenKeepingNewlines(t *testing.T) {
	if got := writ.EscapeForbiddenKeepingNewlines("alice@example.com"); got != "alice@example.com" {
		t.Errorf("EscapeForbiddenKeepingNewlines(clean) = %q, want unchanged", got)
	}

	withNewline := "line one\nline two"
	if got := writ.EscapeForbiddenKeepingNewlines(withNewline); got != withNewline {
		t.Errorf("EscapeForbiddenKeepingNewlines(newline only) = %q, want the newline preserved", got)
	}

	mixed := "line one\n" + string(rune(0x202E)) + "line two"
	want := "line one\n" + escSeq("202e") + "line two"
	if got := writ.EscapeForbiddenKeepingNewlines(mixed); got != want {
		t.Errorf("EscapeForbiddenKeepingNewlines(newline + bidi override) = %q, want %q", got, want)
	}
}
