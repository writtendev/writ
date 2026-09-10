// Package textsafe holds the one table of code points spec/identifiers.md
// §Value character repertoire forbids in a person identifier's value: C0
// controls and DEL, C1 controls, bidi controls and isolates, and zero-width
// or invisible characters. Two call sites need this exact table and must
// never disagree on it — engine/internal/person.Check, which refuses the
// code points at the producer, and cmd/writ's emitJSON/fieldDisplay, which
// escape them at display — and Go's internal-package rule makes
// engine/internal/... unreachable from cmd/writ, so the table lives here, at
// the module root, reachable from both. One table, one owner.
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
// identifier's value:
//
//   - C0 controls, U+0000-U+001F
//   - DEL, U+007F
//   - C1 controls, U+0080-U+009F
//   - Zero-width / invisible characters, U+200B-U+200D (ZWSP, ZWNJ, ZWJ)
//   - Bidi marks, U+200E-U+200F (LRM, RLM)
//   - Bidi embeddings and overrides, U+202A-U+202E
//   - Bidi isolates, U+2066-U+2069
//   - The byte-order mark / zero-width no-break space, U+FEFF
//
// A blanket ban on U+200D (ZERO WIDTH JOINER) is cruder than correct: ZWJ is
// load-bearing for legitimate rendering in several Indic scripts and in
// Arabic, so this rejects some identifiers that ought to be valid. PRECIS
// IdentifierClass (RFC 8264/8265) handles exactly this with contextual rules
// (ZWJ is CONTEXTJ, permitted in specific positions) instead of a flat
// prohibition. Accepted knowingly as the pragmatic form for now;
// spec/identifiers.md records PRECIS IdentifierClass as the planned v0.2.0
// follow-up.
func Forbidden(r rune) bool {
	switch {
	case r <= 0x001F: // C0 controls
		return true
	case r == 0x007F: // DEL
		return true
	case r >= 0x0080 && r <= 0x009F: // C1 controls
		return true
	case r >= 0x200B && r <= 0x200D: // ZWSP, ZWNJ, ZWJ
		return true
	case r >= 0x200E && r <= 0x200F: // LRM, RLM
		return true
	case r >= 0x202A && r <= 0x202E: // bidi embeddings and overrides
		return true
	case r >= 0x2066 && r <= 0x2069: // bidi isolates
		return true
	case r == 0xFEFF: // BOM / zero-width no-break space
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

// hexDigits are the lowercase hex digits EscapeForbidden emits, matching
// encoding/json's own lowercase \uXXXX escapes for the C0 range it already
// escapes -- so a rendered identifier's escapes are visually uniform rather
// than mixing case by forbidden class.
const hexDigits = "0123456789abcdef"

// EscapeForbidden returns s with every Forbidden code point replaced by its
// \uXXXX escape (four lowercase hex digits; every Forbidden code point is in
// the Basic Multilingual Plane, so four digits always suffice).
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
		b.WriteString(`\u`)
		b.WriteByte(hexDigits[(r>>12)&0xF])
		b.WriteByte(hexDigits[(r>>8)&0xF])
		b.WriteByte(hexDigits[(r>>4)&0xF])
		b.WriteByte(hexDigits[r&0xF])
	}
	return b.String()
}
