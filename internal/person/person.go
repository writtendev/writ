// Package person holds the person-identifier grammar and normalization rule
// from spec/identifiers.md. It exists as its own package so that the fold,
// which must stay free of I/O, and the packages above it can share one
// definition of the rule without any of them importing a package that can
// spawn processes.
//
// Its imports are strings, unicode/utf8, the shared internal/textsafe
// forbidden-code-point table, and internal/person/ucd, the vendored Unicode
// tables the normalization rule is defined over. All four are pure
// table-driven computation: no filesystem, no network, no process spawning
// (textsafe imports only strings; ucd imports only sort). That is what makes
// this package's entry in engine/internal/fold's import allowlist grant no
// capability. Keep it that way — anything reached from here is reachable
// from the fold.
package person

import (
	"strings"
	"unicode/utf8"

	"github.com/writtendev/writ/internal/person/ucd"
	"github.com/writtendev/writ/internal/textsafe"
)

// Length bounds from spec/identifiers.md §Person identifiers. MaxLen is
// derived, not an independent number: a scheme, the colon, and a value.
const (
	MaxSchemeLen = 32
	MaxValueLen  = 320
	MaxLen       = MaxSchemeLen + 1 + MaxValueLen // 353
)

// MaxNonStarterRun is the largest number of consecutive non-starters — code
// points with a non-zero Canonical_Combining_Class — a person-id value's NFD
// may carry, per spec/identifiers.md §Value shape: Stream-Safe Text (UAX #15
// §13's Stream-Safe Text Format, applied literally as a producer-side bound).
//
// Measured on NFD, not on the value as written or on its NFC form:
// composition only ever removes non-starters, so this is the form that
// actually bounds what a downstream implementation — including one that
// applies Stream-Safe Text by default, the trap this document calls out
// under "Stream-Safe Text is not applied" — has to handle.
const MaxNonStarterRun = 30

// UnicodeVersion is the Unicode version spec/identifiers.md pins the
// normalization algorithm to, permanently for format v1. The tables are
// vendored (internal/person/ucd) rather than read from the compiler's
// libraries, which select their Unicode version by Go release, so the folded
// state of a log does not depend on who built the reader.
const UnicodeVersion = ucd.Version

// Split splits a person identifier into its scheme and value on the FIRST
// colon, per spec/identifiers.md. The first colon and not "a colon": an email
// address may legally carry a colon inside a quoted local part, so
// `email:"a:b"@example.com` is scheme `email` with value `"a:b"@example.com`.
//
// ok is false when s carries no colon at all; such a string is not a
// conforming person identifier (there is no bare form and no implicit
// scheme), and it is returned whole as the value so callers that must
// preserve it can.
func Split(s string) (scheme, value string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return "", s, false
	}
	return s[:i], s[i+1:], true
}

// NormalizePerson normalizes a person identifier string per
// spec/identifiers.md: the scheme is lowercased, and the value is trimmed of
// leading and trailing whitespace and folded by FoldValue.
//
// The scheme is lowercased with strings.ToLower rather than folded. A
// conforming scheme matches [a-z][a-z0-9+.-]*, so it is ASCII and the two
// agree on every scheme the grammar admits; a scheme they would disagree
// about is not a conforming identifier in the first place.
//
// A string carrying no colon is not a conforming identifier. It is folded as
// a flat string and returned rather than rejected: what a reader does with a
// non-conforming identifier is a separate decision (WRIT-124/126), and
// normalization is not the place to make it.
func NormalizePerson(s string) string {
	s = strings.TrimSpace(s)
	scheme, value, ok := Split(s)
	if !ok {
		return FoldValue(s)
	}
	return strings.ToLower(scheme) + ":" + FoldValue(strings.TrimSpace(value))
}

// FoldValue applies the value half of the normalization rule in
// spec/identifiers.md §Normalization rules, pinned to Unicode UnicodeVersion:
//
//  1. NFC
//  2. Unicode default case folding (UAX #21 §2.3 toCasefold, the full C+F
//     mappings, no locale tailoring)
//  3. NFC again
//
// One algorithm for every scheme. The trailing NFC is not redundant: case
// folding does not preserve a normal form, so folding NFC input can leave a
// composable sequence behind — U+017F followed by U+0301 folds to "s" plus
// U+0301, which is not NFC — and a rule that stopped after folding would not
// be idempotent. Normalization is applied at the producer, in the fold and
// again in the projection, so a rule that changed its answer on the second
// pass would be the same interop defect it exists to remove.
func FoldValue(s string) string {
	// ASCII is already NFC, and folding it is ASCII lowercasing, which is
	// what almost every real identifier needs. TestFoldValueASCIIFastPath
	// checks the shortcut against the general path rather than assuming it.
	if isASCII(s) {
		return lowerASCII(s)
	}
	return nfc(caseFold(nfc(s)))
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func lowerASCII(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			if b == nil {
				b = []byte(s)
			}
			b[i] = c + ('a' - 'A')
		}
	}
	if b == nil {
		return s
	}
	return string(b)
}

// caseFold applies Unicode default case folding, the full C and F mappings of
// CaseFolding.txt, one code point at a time. That is the whole of it because
// toCasefold is context-free: unlike lowercasing, which has the final-sigma
// rule, no case-folding mapping depends on neighbouring characters. Bytes
// that are not valid UTF-8 are copied through untouched.
func caseFold(s string) string {
	var b strings.Builder
	copied := 0
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if f := ucd.Fold(r); f != "" {
			if b.Len() == 0 {
				b.Grow(len(s))
			}
			b.WriteString(s[copied:i])
			b.WriteString(f)
			copied = i + n
		}
		i += n
	}
	if copied == 0 {
		return s
	}
	b.WriteString(s[copied:])
	return b.String()
}

// nfc returns s in Normalization Form C.
//
// The composition is done here, segment by segment, over a canonically
// ordered decomposition, rather than by a library's whole-string NFC, because
// of what spec/identifiers.md says no implementation may do: apply
// Stream-Safe Text. A library that implements UAX #15 §13 applies it
// unconditionally — past 30 consecutive non-starters it inserts U+034F and
// stops composing — and that is reachable well inside the 320-code-point
// bound. Its segment boundaries are Stream-Safe's too, a cut after 30
// non-starters rather than at a position nothing composes across.
//
// The Unicode facts come from internal/person/ucd; this code is the
// algorithm, and is swept exhaustively against CPython, which shares nothing
// with it.
func nfc(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for len(s) > 0 {
		n := segmentLen(s)
		b.WriteString(nfcSegment(s[:n]))
		s = s[n:]
	}
	return b.String()
}

// segmentLen returns the byte length of the leading normalization segment of
// s: its first rune, plus every following rune that cannot begin a segment of
// its own.
//
// The rule is ucd.BoundaryBefore — ccc == 0 and does not combine backwards —
// applied rune by rune. That is the definition of a position nothing can
// compose across, so cutting there cannot separate a composing pair, and it
// has no length limit.
//
// Combining backwards, and not merely being a non-starter, is what keeps
// Hangul whole: V (U+1161..U+1175) and T (U+11A8..U+11C2) have ccc == 0 and
// compose onto the syllable before them, as do spacing marks such as Grantha
// U+1133E and Tamil U+0BBE. All of them report BoundaryBefore false and stay
// with their base.
func segmentLen(s string) int {
	for i := 0; i < len(s); {
		// Invalid UTF-8 decodes one byte at a time, so this always advances
		// and cannot loop; a person identifier is not required to be well
		// formed for the fold to terminate on it.
		r, n := utf8.DecodeRuneInString(s[i:])
		if i > 0 && ucd.BoundaryBefore(r) {
			return i
		}
		i += n
	}
	return len(s)
}

// nfcSegment composes one normalization segment. See nfc for why it does the
// composing itself.
func nfcSegment(seg string) string {
	if !utf8.ValidString(seg) {
		// Not a conforming identifier at all. Return the bytes untouched
		// rather than route them through a decoder that would replace them:
		// normalization is not where malformed input is decided, and identity
		// is at least deterministic and lossless.
		return seg
	}
	return compose(decompose(seg))
}

// decompose returns seg canonically decomposed and canonically ordered,
// alongside each rune's combining class, which the caller needs too and which
// is measurably worth computing once.
//
// Decomposition is applied one rune at a time because it is context-free —
// NFD(xy) is NFD(x) followed by NFD(y), reordered.
func decompose(seg string) ([]rune, []uint8) {
	rs := make([]rune, 0, len(seg))
	for _, r := range seg {
		if d := ucd.Decompose(r); d != "" {
			rs = append(rs, []rune(d)...)
		} else {
			rs = append(rs, r)
		}
	}
	cc := make([]uint8, len(rs))
	for i, r := range rs {
		cc[i] = ucd.CCC(r)
	}
	canonicalOrder(rs, cc)
	return rs, cc
}

// canonicalOrder applies UAX #15's Canonical Ordering Algorithm in place:
// non-starters are sorted by combining class, stably, and runes with ccc 0 are
// fixed points that nothing moves across.
//
// It sorts each maximal run of non-starters rather than the whole slice, and
// it is not the obvious insertion sort. A person identifier's segment length
// is bounded only by what an op body carries — Check is a producer-side guard
// and the fold deliberately does not call it — so this runs on attacker-chosen
// input on every reader of the repository, and the rule it replaced was
// linear. An O(m^2) sort here is an amplification vector, not a slow path.
func canonicalOrder(rs []rune, cc []uint8) {
	for i := 0; i < len(rs); {
		if cc[i] == 0 {
			i++
			continue
		}
		j := i
		for j < len(rs) && cc[j] != 0 {
			j++
		}
		sortByCCC(rs[i:j], cc[i:j])
		i = j
	}
}

// sortRunInsertionMax is the run length below which an insertion sort wins:
// almost every real combining sequence is one or two marks, and a counting
// sort's 256-entry histogram costs more than the whole run.
const sortRunInsertionMax = 32

// sortByCCC stably sorts one run of non-starters by combining class. Insertion
// sort for the short runs that occur in practice, counting sort — linear, and
// stable because it walks the run in order — for the long ones that make a
// quadratic sort worth attacking.
func sortByCCC(rs []rune, cc []uint8) {
	if len(rs) < 2 {
		return
	}
	if len(rs) <= sortRunInsertionMax {
		for i := 1; i < len(rs); i++ {
			r, c := rs[i], cc[i]
			j := i
			for ; j > 0 && cc[j-1] > c; j-- {
				rs[j], cc[j] = rs[j-1], cc[j-1]
			}
			rs[j], cc[j] = r, c
		}
		return
	}
	var counts [256]int
	for _, c := range cc {
		counts[c]++
	}
	sum := 0
	for i := range counts {
		counts[i], sum = sum, sum+counts[i]
	}
	outR := make([]rune, len(rs))
	outC := make([]uint8, len(cc))
	for i, c := range cc {
		outR[counts[c]], outC[counts[c]] = rs[i], c
		counts[c]++
	}
	copy(rs, outR)
	copy(cc, outC)
}

// compose applies UAX #15's Canonical Composition Algorithm to a canonically
// ordered decomposition. Every Unicode fact it needs — the combining classes,
// and whether a given pair composes — comes from internal/person/ucd.
//
// It tracks the last-retained starter (L) per UAX #15 so that
// backward-combining starters that appear as first elements of compositions
// (such as those introduced in Unicode 17.0.0) correctly compose when
// preceded by other text in a segment.
func compose(rs []rune, cc []uint8) string {
	if len(rs) == 0 {
		return ""
	}
	out := make([]rune, 0, len(rs))
	out = append(out, rs[0])
	lastStarter := -1
	if cc[0] == 0 {
		lastStarter = 0
	}
	maxRetained := -1
	for i, c := range rs[1:] {
		n := int(cc[i+1])
		if lastStarter >= 0 && maxRetained < n {
			if p, ok := ucd.Compose(out[lastStarter], c); ok {
				out[lastStarter] = p
				continue
			}
		}
		out = append(out, c)
		if n == 0 {
			lastStarter = len(out) - 1
			maxRetained = -1
		} else if n > maxRetained {
			maxRetained = n
		}
	}
	return string(out)
}

// maxRunLen returns the length, in code points, of the longest run of
// consecutive non-starters in s's NFD. It backs the Check branch enforcing
// MaxNonStarterRun (spec/identifiers.md §Value shape: Stream-Safe Text).
//
// It decomposes s one rune at a time, the same discipline decompose (above)
// follows. A run cannot cross a starter — ccc 0 is a fixed point canonical
// ordering never moves a non-starter across — so no reordering is needed to
// find a run's length: it is measured directly off the rune-at-a-time
// decomposition order.
func maxRunLen(s string) int {
	run, max := 0, 0
	count := func(r rune) {
		if ucd.CCC(r) == 0 {
			run = 0
			return
		}
		run++
		if run > max {
			max = run
		}
	}
	for _, r := range s {
		if d := ucd.Decompose(r); d != "" {
			for _, dr := range d {
				count(dr)
			}
		} else {
			count(r)
		}
	}
	return max
}

// Problem names the ways a string can fail to be a conforming person
// identifier. It is an enumeration rather than an error so that this package
// stays free of anything the fold must not reach; callers turn it into a
// message.
type Problem int

const (
	// Valid means the identifier conforms.
	Valid Problem = iota
	// MissingScheme means the identifier carries no colon at all. There is no
	// bare form and no implicit scheme.
	MissingScheme
	// SchemeCharset means the scheme is empty or carries a character outside
	// [a-z][a-z0-9+.-]*.
	SchemeCharset
	// SchemeTooLong means the scheme exceeds MaxSchemeLen.
	SchemeTooLong
	// EmptyValue means the value is empty.
	EmptyValue
	// ForbiddenCodePoint means the value contains a code point
	// spec/identifiers.md §Value character repertoire forbids: General
	// Category Cc or Cf (C0/C1 controls, DEL, bidi controls/isolates/
	// embeddings/overrides, zero-width or invisible characters, other
	// format characters, and the tag block), or one of four named
	// Default_Ignorable code points that are not Cf. FirstForbidden names
	// the offending code point for a caller's error message.
	ForbiddenCodePoint
	// UnassignedCodePoint means the value contains a code point that is
	// unassigned (General_Category Cn) at Unicode 17.0.0, which
	// spec/identifiers.md §Value character repertoire forbids: the Unicode
	// stability policies freeze normalization and case folding only for
	// assigned code points. FirstUnassigned names the offending code point
	// for a caller's error message.
	UnassignedCodePoint
	// ValueTooLong means the value exceeds MaxValueLen code points.
	ValueTooLong
	// ValueNotStreamSafe means the value's NFD carries a run of more than
	// MaxNonStarterRun consecutive non-starters, violating
	// spec/identifiers.md §Value shape: Stream-Safe Text (UAX #15 §13).
	ValueNotStreamSafe
)

// String describes the problem for use in a caller's error message.
func (p Problem) String() string {
	switch p {
	case Valid:
		return "valid"
	case MissingScheme:
		return "missing scheme (expected scheme:value, for example email:alice@example.com or user:alice)"
	case SchemeCharset:
		return "scheme must match [a-z][a-z0-9+.-]*"
	case SchemeTooLong:
		return "scheme is longer than 32 characters"
	case EmptyValue:
		return "value is empty"
	case ForbiddenCodePoint:
		return "value contains a forbidden code point (control character, bidi control/isolate/override, zero-width/invisible character, or other format character; spec/identifiers.md §Value character repertoire)"
	case UnassignedCodePoint:
		return "value contains an unassigned code point (General_Category Cn at Unicode 17.0.0, including noncharacters; spec/identifiers.md §Value character repertoire)"
	case ValueTooLong:
		return "value is longer than 320 characters"
	case ValueNotStreamSafe:
		return "value carries more than 30 consecutive combining marks (Stream-Safe Text, UAX #15 §13; spec/identifiers.md §Value shape: Stream-Safe Text)"
	}
	return "unknown problem"
}

// FirstForbidden reports the first code point in s that
// spec/identifiers.md §Value character repertoire forbids, so a caller
// building an error message around a ForbiddenCodePoint Problem can name it
// (for example "U+202E"). It delegates to internal/textsafe's shared table —
// the same one cmd/writ escapes at display — rather than keeping a second
// copy of it here.
func FirstForbidden(s string) (rune, bool) {
	return textsafe.First(s)
}

// FirstUnassigned reports the first code point in s that is unassigned
// (General_Category Cn) at Unicode 17.0.0, so a caller building an error
// message around an UnassignedCodePoint Problem can name it (for example
// "U+0378"). It mirrors FirstForbidden. An invalid byte is not a code point
// and is not reported; Check is not asked about malformed UTF-8 here.
func FirstUnassigned(s string) (rune, bool) {
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if !(r == utf8.RuneError && n == 1) && !ucd.Assigned(r) {
			return r, true
		}
		i += n
	}
	return 0, false
}

// Check reports whether s is a conforming person identifier per
// spec/identifiers.md. s is expected to be normalized already; Check tests the
// grammar and the bounds, not normalization.
//
// The bounds are enforced by rejection, never by truncation: two distinct
// identifiers truncated to the same string would collapse into one person for
// assignment, approval keying and set membership.
//
// Check is a producer-side guard. The fold does not call it: what a reader
// does with a non-conforming identifier it has already read is decided
// separately (WRIT-124/126).
func Check(s string) Problem {
	scheme, value, ok := Split(s)
	if !ok {
		return MissingScheme
	}
	if !validScheme(scheme) {
		return SchemeCharset
	}
	// A scheme is ASCII by its charset, so bytes and code points coincide.
	if len(scheme) > MaxSchemeLen {
		return SchemeTooLong
	}
	if value == "" {
		return EmptyValue
	}
	// Checked over the value only: the scheme charset (validScheme, above)
	// already admits none of these code points, so there is nothing to gain
	// checking it too. A message naming a forbidden code point beats "value
	// is longer than 320 characters" for an input that is both, which is why
	// this runs before the length check.
	if _, bad := textsafe.First(value); bad {
		return ForbiddenCodePoint
	}
	// Unassigned code points are refused for the reason the forbidden ones
	// are, and for one more: Unicode freezes NFC and case folding only for
	// assigned code points, so a value free of them folds the same under
	// every Unicode version from 17.0.0 on.
	if _, bad := FirstUnassigned(value); bad {
		return UnassignedCodePoint
	}
	if countRunes(value) > MaxValueLen {
		return ValueTooLong
	}
	// Run last: decomposing every rune of the value to check its NFD
	// non-starter run costs more than the scans above it, so it only runs on
	// a value that has already cleared every cheaper check.
	if maxRunLen(value) > MaxNonStarterRun {
		return ValueNotStreamSafe
	}
	return Valid
}

// IsStreamSafe reports whether id's value conforms to spec/identifiers.md
// §Value shape: Stream-Safe Text — its NFD carries no run of more than
// MaxNonStarterRun consecutive non-starters — independently of every other
// rule Check enforces. Check's own non-starter-run branch above computes the
// same thing inline; this is not a second implementation, just the one
// exported so a test can reach it.
//
// It exists so a differential test in this package (person_test) can bind
// this package's definition of the rule to the reference copy,
// spec.PersonValueIsStreamSafe, the way TestReffoldNormalizePersonMatchesEngine
// binds NormalizePerson to spec.NormalizePerson. That test imports spec
// directly rather than reaching it through engine/state, on purpose:
// api/engine.txt is generated from ./engine only, so nothing this package
// exports for that binding — on either side of it — reaches the engine's
// public API baseline. Nothing in engine calls IsStreamSafe outside tests: a
// real caller wants Check's full verdict, not this one axis of it.
func IsStreamSafe(id string) bool {
	_, value, ok := Split(id)
	if !ok {
		value = id
	}
	return maxRunLen(value) <= MaxNonStarterRun
}

// validScheme reports whether scheme matches [a-z][a-z0-9+.-]*.
func validScheme(scheme string) bool {
	if scheme == "" {
		return false
	}
	for i := 0; i < len(scheme); i++ {
		c := scheme[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && c >= '0' && c <= '9':
		case i > 0 && (c == '+' || c == '.' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// countRunes counts code points, the unit JSON Schema maxLength counts, so the
// engine accepts exactly what spec/schemas/identifiers.schema.json accepts.
func countRunes(s string) int {
	return utf8.RuneCountInString(s)
}
