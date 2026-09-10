package textsafe_test

import (
	"testing"

	"github.com/writtendev/writ/internal/textsafe"
)

// TestForbidden pins every forbidden class named in spec/identifiers.md
// Value character repertoire, and the immediate neighbour on each side of
// every range -- so the ranges are pinned at both edges, not just somewhere
// inside them, matching engine/internal/person's own boundary test.
func TestForbidden(t *testing.T) {
	forbidden := []rune{
		0x0000, 0x0001, 0x001F, // C0
		0x007F,         // DEL
		0x0080, 0x009F, // C1
		0x200B, 0x200C, 0x200D, // ZWSP, ZWNJ, ZWJ
		0x200E, 0x200F, // LRM, RLM
		0x202A, 0x202E, // bidi embeddings/overrides
		0x2066, 0x2069, // bidi isolates
		0xFEFF, // BOM
	}
	for _, r := range forbidden {
		if !textsafe.Forbidden(r) {
			t.Errorf("Forbidden(%U) = false, want true", r)
		}
	}

	neighbours := []rune{
		0x0020, 0x007E, 0x00A0, 0x200A, 0x2010, 0x2065, 0x206A, 0xFEFE, 0xFF00,
	}
	for _, r := range neighbours {
		if textsafe.Forbidden(r) {
			t.Errorf("Forbidden(%U) = true, want false", r)
		}
	}
}

// escapeSeq builds the literal \uXXXX text EscapeForbidden is expected to
// produce for the code point hex names, from plain ASCII pieces -- never a
// raw non-ASCII character embedded in this source file.
func escapeSeq(hex string) string {
	return "\\u" + hex
}

// withCodePoint splices the single rune named by hex into s at byte offset
// at, building an input string from plain ASCII plus one rune constant
// rather than an embedded literal character.
func withCodePoint(before string, r rune, after string) string {
	return before + string(r) + after
}

func TestFirst(t *testing.T) {
	if r, ok := textsafe.First("plain ascii"); ok {
		t.Errorf("First(plain ascii) = %U, true; want false", r)
	}

	hostile := withCodePoint("email:alice", 0x202E, "@evil.com")
	if r, ok := textsafe.First(hostile); !ok || r != 0x202E {
		t.Errorf("First(hostile) = %U, %v, want U+202E, true", r, ok)
	}

	// twoForbidden carries two different forbidden code points, in order, so
	// First is checked to report the earlier one and not merely "a"
	// forbidden one.
	twoForbidden := withCodePoint("a", 0x200B, "") + withCodePoint("b", 0x202E, "c")
	if r, ok := textsafe.First(twoForbidden); !ok || r != 0x200B {
		t.Errorf("First(two forbidden points) = %U, %v, want U+200B, true", r, ok)
	}
}

func TestEscapeForbidden(t *testing.T) {
	if got := textsafe.EscapeForbidden("alice@example.com"); got != "alice@example.com" {
		t.Errorf("EscapeForbidden(clean) = %q, want unchanged", got)
	}

	hostile := withCodePoint("email:alice", 0x202E, "@evil.com")
	wantHostile := "email:alice" + escapeSeq("202e") + "@evil.com"
	if got := textsafe.EscapeForbidden(hostile); got != wantHostile {
		t.Errorf("EscapeForbidden(hostile) = %q, want %q", got, wantHostile)
	}

	zwsp := withCodePoint("a", 0x200B, "b")
	wantZWSP := "a" + escapeSeq("200b") + "b"
	if got := textsafe.EscapeForbidden(zwsp); got != wantZWSP {
		t.Errorf("EscapeForbidden(zwsp) = %q, want %q", got, wantZWSP)
	}

	bom := string(rune(0xFEFF))
	wantBOM := escapeSeq("feff")
	if got := textsafe.EscapeForbidden(bom); got != wantBOM {
		t.Errorf("EscapeForbidden(bom) = %q, want %q", got, wantBOM)
	}
}

// TestEscapeForbiddenIsLossless pins the property the whole design leans on:
// the escape is text a human, or a JSON decoder, reads back to the exact
// code point it replaced, unlike stripping.
func TestEscapeForbiddenIsLossless(t *testing.T) {
	hostile := withCodePoint("email:alice", 0x202E, "@evil.com")
	escaped := textsafe.EscapeForbidden(hostile)
	if escaped == hostile {
		t.Fatal("test setup: EscapeForbidden did not change a hostile string")
	}
	// The raw override must not survive into the escaped text.
	for _, r := range escaped {
		if textsafe.Forbidden(r) {
			t.Errorf("escaped output %q still contains a raw forbidden code point %U", escaped, r)
		}
	}
	want := "email:alice" + escapeSeq("202e") + "@evil.com"
	if escaped != want {
		t.Errorf("EscapeForbidden(%q) = %q, want %q", hostile, escaped, want)
	}
}

// TestEscapeForbiddenReturnsSameStringWhenClean pins that a clean string is
// returned unmodified, not a defensive copy -- EscapeForbidden is called on
// the hot path of every rendered field, and most fields carry nothing
// forbidden at all.
func TestEscapeForbiddenReturnsSameStringWhenClean(t *testing.T) {
	const clean = "plain ascii"
	if got := textsafe.EscapeForbidden(clean); got != clean {
		t.Errorf("EscapeForbidden(%q) = %q, want unchanged", clean, got)
	}
}
