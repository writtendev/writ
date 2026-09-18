package textsafe_test

import (
	"encoding/json"
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
		0x007F, 0x009F, // DEL, C1
		0x00AD,         // soft hyphen
		0x0600, 0x0605, // Arabic number signs
		0x061C,         // Arabic letter mark
		0x06DD,         // Arabic end of ayah
		0x070F,         // Syriac abbreviation mark
		0x0890, 0x0891, // Arabic sign
		0x08E2,         // Arabic disputed end of ayah
		0x115F, 0x1160, // Hangul Choseong/Jungseong fillers (Default_Ignorable, not Cf)
		0x180E,                 // Mongolian vowel separator
		0x200B, 0x200C, 0x200D, // ZWSP, ZWNJ, ZWJ
		0x200E, 0x200F, // LRM, RLM
		0x202A, 0x202E, // bidi embeddings/overrides
		0x2060, 0x2064, // word joiner, invisible operators
		0x2066, 0x2069, 0x206A, 0x206F, // bidi isolates, reserved format chars
		0x3164,         // Hangul filler (Default_Ignorable, not Cf)
		0xFEFF,         // BOM
		0xFFA0,         // halfwidth Hangul filler (Default_Ignorable, not Cf)
		0xFFF9, 0xFFFB, // interlinear annotation characters
		0x110BD,          // Kaithi number sign
		0x110CD,          // Kaithi number sign above
		0x13430, 0x1343F, // Egyptian hieroglyph format controls
		0x1BCA0, 0x1BCA3, // shorthand format controls
		0x1D173, 0x1D17A, // musical symbol format controls
		0xE0001,          // language tag
		0xE0020, 0xE007F, // tag characters
	}
	for _, r := range forbidden {
		if !textsafe.Forbidden(r) {
			t.Errorf("Forbidden(%U) = false, want true", r)
		}
	}

	// The immediate neighbour on each side of every forbidden range above,
	// so every range is pinned at both edges rather than just somewhere
	// inside it. 0x206A moved here from the old list: it sits inside the
	// widened bidi-isolate/reserved range (U+2066-U+206F) and is now
	// Forbidden rather than a neighbour.
	neighbours := []rune{
		0x0020, 0x007E, 0x00A0,
		0x00AC, 0x00AE,
		0x05FF, 0x0606,
		0x061B, 0x061D,
		0x06DC, 0x06DE,
		0x070E, 0x0710,
		0x088F, 0x0892,
		0x08E1, 0x08E3,
		0x115E, 0x1161,
		0x180D, 0x180F,
		0x200A, 0x2010,
		0x2029, 0x202F,
		0x205F, 0x2065, 0x2070,
		0x3163, 0x3165,
		0xFEFE, 0xFF00,
		0xFF9F, 0xFFA1,
		0xFFF8, 0xFFFC,
		0x110BC, 0x110BE,
		0x110CC, 0x110CE,
		0x1342F, 0x13440,
		0x1BC9F, 0x1BCA4,
		0x1D172, 0x1D17B,
		0xE0000, 0xE0002,
		0xE001F, 0xE0080,
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

// TestEscapeForbiddenSurrogatePair pins the one property that changed when
// Forbidden grew past the Basic Multilingual Plane (the tag block and
// several other supplementary-plane Cf ranges): a code point above U+FFFF
// escapes as its UTF-16 surrogate pair, two \uXXXX escapes, not Go's
// four-hex \UXXXXXXXX -- the surrogate pair is what makes the escaped text
// valid JSON that a JSON decoder reads back losslessly (cmd/writ's emitJSON
// escapes the already-encoded document; a \U escape there would not parse).
func TestEscapeForbiddenSurrogatePair(t *testing.T) {
	tag := withCodePoint("user:ali", 0xE0020, "ce")
	wantTag := "user:ali" + escapeSeq("db40") + escapeSeq("dc20") + "ce"
	if got := textsafe.EscapeForbidden(tag); got != wantTag {
		t.Errorf("EscapeForbidden(tag character) = %q, want %q", got, wantTag)
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

// TestEscapeForbiddenSupplementaryIsLosslessJSON is the supplementary-plane
// counterpart of TestEscapeForbiddenIsLossless: a JSON string literal built
// from EscapeForbidden's output, wrapped in quotes, must decode with
// encoding/json back to the exact tag character that was there. Go's own
// \UXXXXXXXX escape is not valid inside a JSON string at all -- this is the
// property that makes the UTF-16 surrogate pair form mandatory, not merely
// stylistic.
func TestEscapeForbiddenSupplementaryIsLosslessJSON(t *testing.T) {
	hostile := withCodePoint("user:ali", 0xE0020, "ce")
	escaped := textsafe.EscapeForbidden(hostile)
	if escaped == hostile {
		t.Fatal("test setup: EscapeForbidden did not change a hostile string")
	}
	for _, r := range escaped {
		if textsafe.Forbidden(r) {
			t.Errorf("escaped output %q still contains a raw forbidden code point %U", escaped, r)
		}
	}

	var decoded string
	quoted := `"` + escaped + `"`
	if err := json.Unmarshal([]byte(quoted), &decoded); err != nil {
		t.Fatalf("json.Unmarshal(%s): %v", quoted, err)
	}
	if decoded != hostile {
		t.Errorf("decoded = %q, want the original hostile string %q", decoded, hostile)
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
