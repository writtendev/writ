// Package textsafe holds the one table of code points spec/identifiers.md
// §Value character repertoire forbids in a person identifier's value:
// General_Category Cc (C0 controls and DEL, C1 controls) and Cf (format
// characters — bidi controls and isolates, zero-width/invisible characters,
// and more), at Unicode 17.0.0, plus four named Default_Ignorable code
// points that render invisible but are not Cf (see Forbidden's doc comment
// for the full accounting). Two call sites need this exact table and must
// never disagree on it — internal/person.Check, which refuses the code
// points at the producer, and engine/textsafe.go's EscapeForbidden /
// EscapeForbiddenKeepingNewlines, which escape them at display — and Go's
// internal-package rule makes internal/... unreachable from cmd/writ (or
// any other consumer outside this module), so the table lives here, at the
// module root, reachable from both. cmd/writ no longer imports this
// package directly (WRIT-311): its porcelain and --json rendering go
// through the engine's two wrappers instead, the same as any other
// consumer of the public API. One table, one owner.
//
// Producer rejection is hygiene, not the security boundary: spec/fold.md
// §7.1 makes fold total by design, so a hostile writer's op reaches the log
// regardless of what a conforming producer refuses. Escaping these code
// points wherever writ renders a person identifier for a human is what
// actually closes the spoofing attack spec/identifiers.md §Rendering a
// person identifier describes — a bidi override otherwise makes displayed
// text differ from stored text, and a zero-width character makes two
// visually identical strings compare unequal (or the reverse).
//
// Imports are strings only, deliberately: person imports this package, and
// person's own imports are enumerated in its package doc specifically so
// that reaching this package from the fold grants it nothing beyond what it
// already has.
package textsafe

import "strings"

// Forbidden reports whether r is one of the code points
// spec/identifiers.md §Value character repertoire forbids in a person
// identifier's value: General_Category Cc ∪ Cf at Unicode 17.0.0, plus four
// named Default_Ignorable code points that are not Cf. The ranges below are
// enumerated rather than looked up through unicode.Is(unicode.Cc/Cf, r), by
// design (see the package doc): a property lookup would add an import this
// package's whole reason for existing is to keep off (strings only), and
// would let a future Go toolchain's Unicode table silently change what a
// conforming producer accepts. The ranges were generated once from Go
// 1.27's unicode.Cc and unicode.Cf tables (Unicode 17.0.0) and pasted here;
// spec/identifiers.md's repertoire table and
// spec/schemas/identifiers.schema.json's pattern carry the same list.
//
//   - C0 controls, U+0000-U+001F
//   - DEL and C1 controls, U+007F-U+009F
//   - Soft hyphen, U+00AD
//   - Arabic number signs, U+0600-U+0605
//   - Arabic letter mark, U+061C
//   - Arabic end of ayah, U+06DD
//   - Syriac abbreviation mark, U+070F
//   - Arabic sign, U+0890-U+0891
//   - Arabic disputed end of ayah, U+08E2
//   - Hangul Choseong/Jungseong fillers, U+115F-U+1160 (Default_Ignorable,
//     not Cf — one of the four named exceptions)
//   - Mongolian vowel separator, U+180E
//   - Zero-width space/ZWNJ/ZWJ and bidi marks, U+200B-U+200F
//   - Bidi embeddings and overrides, U+202A-U+202E
//   - Word joiner and invisible operators, U+2060-U+2064
//   - Bidi isolates and reserved format characters, U+2066-U+206F
//   - Hangul filler, U+3164 (Default_Ignorable, not Cf — one of the four
//     named exceptions)
//   - Byte-order mark / zero-width no-break space, U+FEFF
//   - Halfwidth Hangul filler, U+FFA0 (Default_Ignorable, not Cf — one of
//     the four named exceptions)
//   - Interlinear annotation characters, U+FFF9-U+FFFB
//   - Kaithi number sign, U+110BD
//   - Kaithi number sign above, U+110CD
//   - Egyptian hieroglyph format controls, U+13430-U+1343F
//   - Shorthand format controls, U+1BCA0-U+1BCA3
//   - Musical symbol format controls, U+1D173-U+1D17A
//   - Language tag, U+E0001
//   - Tag characters, U+E0020-U+E007F
//
// A blanket ban on U+200D (ZERO WIDTH JOINER) is cruder than correct: ZWJ is
// load-bearing for legitimate rendering in several Indic scripts and in
// Arabic, so this rejects some identifiers that ought to be valid. PRECIS
// IdentifierClass (RFC 8264/8265) handles exactly this with contextual rules
// (ZWJ is CONTEXTJ, permitted in specific positions) instead of a flat
// prohibition. Accepted knowingly as the pragmatic form for now;
// spec/identifiers.md records PRECIS IdentifierClass as the planned v0.2.0
// follow-up. The same trade extends to the rest of Cf: the Arabic, Syriac
// and Kaithi number/format signs above occur in ordinary running text,
// though not plausibly in an identity label.
func Forbidden(r rune) bool {
	switch {
	case r <= 0x001F: // C0 controls
		return true
	case r >= 0x007F && r <= 0x009F: // DEL, C1 controls
		return true
	case r == 0x00AD: // soft hyphen
		return true
	case r >= 0x0600 && r <= 0x0605: // Arabic number signs
		return true
	case r == 0x061C: // Arabic letter mark
		return true
	case r == 0x06DD: // Arabic end of ayah
		return true
	case r == 0x070F: // Syriac abbreviation mark
		return true
	case r >= 0x0890 && r <= 0x0891: // Arabic sign
		return true
	case r == 0x08E2: // Arabic disputed end of ayah
		return true
	case r >= 0x115F && r <= 0x1160: // Hangul Choseong/Jungseong fillers
		return true
	case r == 0x180E: // Mongolian vowel separator
		return true
	case r >= 0x200B && r <= 0x200F: // ZWSP, ZWNJ, ZWJ, LRM, RLM
		return true
	case r >= 0x202A && r <= 0x202E: // bidi embeddings and overrides
		return true
	case r >= 0x2060 && r <= 0x2064: // word joiner, invisible operators
		return true
	case r >= 0x2066 && r <= 0x206F: // bidi isolates, reserved format chars
		return true
	case r == 0x3164: // Hangul filler
		return true
	case r == 0xFEFF: // BOM / zero-width no-break space
		return true
	case r == 0xFFA0: // halfwidth Hangul filler
		return true
	case r >= 0xFFF9 && r <= 0xFFFB: // interlinear annotation characters
		return true
	case r == 0x110BD: // Kaithi number sign
		return true
	case r == 0x110CD: // Kaithi number sign above
		return true
	case r >= 0x13430 && r <= 0x1343F: // Egyptian hieroglyph format controls
		return true
	case r >= 0x1BCA0 && r <= 0x1BCA3: // shorthand format controls
		return true
	case r >= 0x1D173 && r <= 0x1D17A: // musical symbol format controls
		return true
	case r == 0xE0001: // language tag
		return true
	case r >= 0xE0020 && r <= 0xE007F: // tag characters
		return true
	default:
		return false
	}
}

// First reports the first Forbidden code point in s, if any -- so a caller
// building an error message, or deciding whether escaping is even
// necessary, has something to name without a second pass over s.
func First(s string) (rune, bool) {
	for _, r := range s {
		if Forbidden(r) {
			return r, true
		}
	}
	return 0, false
}

// hexDigits are the lowercase hex digits EscapeRune emits, matching
// encoding/json's own lowercase \uXXXX escapes for the C0 range it already
// escapes -- so a rendered identifier's escapes are visually uniform rather
// than mixing case by forbidden class.
const hexDigits = "0123456789abcdef"

// EscapeRune appends r's JSON string escape to b: a four-hex-digit \uXXXX
// escape for a code point in the Basic Multilingual Plane, or its UTF-16
// surrogate pair (\uXXXX\uXXXX) for a code point above it. Forbidden now
// reaches the tag block (U+E0001, U+E0020-U+E007F) and several other
// supplementary-plane Cf ranges, so a caller can no longer assume every
// escaped code point fits in one \uXXXX -- this is the one place that
// decides how to split a code point that does not.
//
// It is exported so cmd/writ's own hand-rolled `\u%04x` escapers
// (store.go's escapeErrReport, schema.go's escapeRenderedSchemaSource) can
// share this implementation instead of each assuming the BMP-only case that
// no longer holds.
func EscapeRune(b *strings.Builder, r rune) {
	if r <= 0xFFFF {
		writeHex4(b, r)
		return
	}
	r -= 0x10000
	writeHex4(b, 0xD800+(r>>10))
	writeHex4(b, 0xDC00+(r&0x3FF))
}

func writeHex4(b *strings.Builder, r rune) {
	b.WriteString(`\u`)
	b.WriteByte(hexDigits[(r>>12)&0xF])
	b.WriteByte(hexDigits[(r>>8)&0xF])
	b.WriteByte(hexDigits[(r>>4)&0xF])
	b.WriteByte(hexDigits[r&0xF])
}

// EscapeForbidden returns s with every Forbidden code point replaced by its
// JSON \uXXXX escape (or, for a code point above the Basic Multilingual
// Plane -- the tag block and a handful of other supplementary-plane Cf
// ranges are now Forbidden -- its \uXXXX\uXXXX UTF-16 surrogate pair; see
// EscapeRune).
//
// Escaping is preferred over stripping (spec/identifiers.md §Rendering a
// person identifier): it is lossless -- a JSON parser, or a human reading the
// literal escape text, recovers the exact code point that was there, where
// stripping would silently change what the log says. s is returned
// unmodified, not copied, when it contains nothing Forbidden.
func EscapeForbidden(s string) string {
	if _, ok := First(s); !ok {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !Forbidden(r) {
			b.WriteRune(r)
			continue
		}
		EscapeRune(&b, r)
	}
	return b.String()
}
