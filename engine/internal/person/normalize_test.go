package person_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/writtendev/writ/engine/internal/person"
)

// TestPinnedUnicodeVersion binds the version spec/identifiers.md pins to the
// Unicode tables actually compiled in. x/text selects its tables by Go build
// tag, not by module version: go1.27 switches both packages to Unicode 17.0.0.
// Normalization is applied to every person identifier in every repository, so
// a toolchain bump silently changing the tables would silently change who is
// the same person. This test makes that a build failure and a deliberate spec
// amendment instead.
func TestPinnedUnicodeVersion(t *testing.T) {
	if norm.Version != person.UnicodeVersion {
		t.Errorf("x/text/unicode/norm is Unicode %s, but spec/identifiers.md pins %s — "+
			"changing the version changes normalization for every identifier; amend the spec deliberately",
			norm.Version, person.UnicodeVersion)
	}
	if cases.UnicodeVersion != person.UnicodeVersion {
		t.Errorf("x/text/cases is Unicode %s, but spec/identifiers.md pins %s",
			cases.UnicodeVersion, person.UnicodeVersion)
	}
}

// foldVectors pin the answers the algorithm in spec/identifiers.md
// §The value folding algorithm has to give. Each one fails under the rule this
// replaced, or under a plausible wrong spelling of the rule that replaces it.
var foldVectors = []struct {
	name string
	in   string
	want string
	why  string
}{
	{
		name: "dotted capital I folds to i plus combining dot",
		in:   "İ",
		want: "i̇",
		why: "the case the ticket exists for: strings.ToLower answers \"i\", " +
			"Rust's str::to_lowercase answers i+U+0307, and one person becomes two",
	},
	{
		name: "sharp s folds to ss",
		in:   "ß",
		want: "ss",
		why:  "pins full folding; simple folding would leave it alone",
	},
	{
		name: "capital sharp s folds to ss",
		in:   "ẞ",
		want: "ss",
		why:  "full folding, not the simple mapping to U+00DF",
	},
	{
		name: "decomposed e-acute composes",
		in:   "é",
		want: "é",
		why:  "without NFC this is a different person from the precomposed spelling",
	},
	{
		name: "precomposed e-acute is unchanged",
		in:   "é",
		want: "é",
		why:  "NFC is a no-op on an already-composed value",
	},
	{
		name: "long s folds and then composes",
		in:   "ſ́",
		want: "ś",
		why: "the reason the algorithm normalizes twice: folding leaves s+U+0301, " +
			"which is not NFC, and a single-NFC rule would not be idempotent",
	},
	{
		name: "Cherokee uppercase is a fold fixed point",
		in:   "Ꭰ",
		want: "Ꭰ",
		why:  "x/text toggles this to U+AB70 (golang/go#46101); folding is idempotent by definition",
	},
	{
		name: "Cherokee lowercase folds up to uppercase",
		in:   "ꭰ",
		want: "Ꭰ",
		why:  "Cherokee is the one script whose folding maps lowercase up",
	},
	{
		name: "Cherokee in the middle of a value",
		in:   "AᎠBꭰC",
		want: "aᎠbᎠc",
		why:  "the fixed points are held without disturbing the runs around them",
	},
	{
		name: "supplementary starter does not compose with a following mark",
		in:   "\U00010041̀",
		want: "\U00010041̀",
		why: "x/text truncates the starter to its low 16 bits and answers \"À\", " +
			"merging this identity with a completely different one",
	},
	{
		name: "the mark after a supplementary starter is not lost",
		in:   "\U0001042b̈",
		want: "\U0001042b̈",
		why:  "x/text answers U+04F8 here, two code points collapsing into one wrong one",
	},
	{
		name: "legitimate supplementary composition still composes",
		in:   "\U00011099\U000110ba",
		want: "\U0001109a",
		why:  "the guard checks an invariant, so it must not block a real composition",
	},
	{
		name: "legitimate supplementary composition with a starter second element",
		in:   "\U00011347\U0001133e",
		want: "\U0001134b",
		why:  "the second element here has CCC 0, so segmenting must not split the pair",
	},
	{
		name: "Hangul jamo compose across two starters",
		in:   "가",
		want: "가",
		why:  "the only composition between two starters; segmenting must not split it",
	},
	{
		name: "a false composition costs only its own segment",
		in:   "é\U00010041̀é",
		want: "é\U00010041̀é",
		why:  "the guard is per segment, so one bad pair must not discard the good ones around it",
	},
	{
		name: "Unicode 17 backward-combining starter composes in context",
		in:   "x\U000113c2\U000113c2y",
		want: "x\U000113c5y",
		why:  "UAX #15 last-retained-starter (L) tracking; backward-combining starter in context",
	},
	{
		name: "ASCII folds to ASCII lowercase",
		in:   "Alice@Example.COM",
		want: "alice@example.com",
		why:  "the fast path, and the overwhelmingly common case",
	},
}

func TestFoldValueVectors(t *testing.T) {
	for _, tc := range foldVectors {
		t.Run(tc.name, func(t *testing.T) {
			if got := person.FoldValue(tc.in); got != tc.want {
				t.Errorf("FoldValue(% x) = % x, want % x\n%s", tc.in, got, tc.want, tc.why)
			}
		})
	}
}

// TestNormalizePersonAppliesFoldToTheValue checks the vectors reach the value
// half of a whole identifier, and that the scheme half is untouched by them.
func TestNormalizePersonAppliesFoldToTheValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"user:İ", "user:i̇"},
		{"USER:é", "user:é"},
		{"user:é", "user:é"},
		{"  Email:Alice@Example.COM  ", "email:alice@example.com"},
		{"email:  ß  ", "email:ss"},
	}
	for _, tc := range cases {
		if got := person.NormalizePerson(tc.in); got != tc.want {
			t.Errorf("NormalizePerson(%q) = % x, want % x", tc.in, got, tc.want)
		}
	}
}

// TestDecomposedAndPrecomposedAreOnePerson is the equality property stated as
// equality rather than as bytes: these are two spellings a user cannot tell
// apart and two identities before this change.
func TestDecomposedAndPrecomposedAreOnePerson(t *testing.T) {
	decomposed := person.NormalizePerson("user:José")
	precomposed := person.NormalizePerson("user:José")
	if decomposed != precomposed {
		t.Errorf("decomposed %q and precomposed %q are different people", decomposed, precomposed)
	}
}

// TestFoldValueASCIIFastPath checks the shortcut against the general path
// rather than assuming they agree: every one-byte and two-byte ASCII string.
func TestFoldValueASCIIFastPath(t *testing.T) {
	var b [2]byte
	for i := 0; i < utf8.RuneSelf; i++ {
		b[0] = byte(i)
		s := string(b[:1])
		if got, want := person.FoldValue(s), person.FoldValueGeneral(s); got != want {
			t.Fatalf("FoldValue(% x) = % x, general path = % x", s, got, want)
		}
		for j := 0; j < utf8.RuneSelf; j++ {
			b[1] = byte(j)
			s := string(b[:2])
			if got, want := person.FoldValue(s), person.FoldValueGeneral(s); got != want {
				t.Fatalf("FoldValue(% x) = % x, general path = % x", s, got, want)
			}
		}
	}
}

// TestBoundAppliesAfterNormalization is the rule spec/identifiers.md
// §Length bounds states and WRIT-102 settled, exercised on the axis only NFC
// can reach: a value that is over the bound as written and inside it once
// composed is valid. Nothing about it is valid before composition happens.
func TestBoundAppliesAfterNormalization(t *testing.T) {
	raw := strings.Repeat("é", 321)
	if n := utf8.RuneCountInString(raw); n != 642 {
		t.Fatalf("test setup: raw value is %d code points, want 642", n)
	}
	id := person.NormalizePerson("email:" + raw)
	_, value, ok := person.Split(id)
	if !ok {
		t.Fatal("normalized identifier lost its scheme")
	}
	if n := utf8.RuneCountInString(value); n != 321 {
		t.Fatalf("normalized value is %d code points, want 321", n)
	}
	if got := person.Check(id); got != person.ValueTooLong {
		t.Errorf("Check(321 composed code points) = %v, want ValueTooLong", got)
	}

	atLimit := strings.Repeat("é", 320)
	if n := utf8.RuneCountInString(atLimit); n != 640 {
		t.Fatalf("test setup: at-limit value is %d code points, want 640", n)
	}
	id = person.NormalizePerson("email:" + atLimit)
	if got := person.Check(id); got != person.Valid {
		t.Errorf("Check(640 raw code points composing to 320) = %v, want Valid — "+
			"the bound applies to the normalized value", got)
	}
}

// TestIdempotent is the property the whole algorithm turns on: normalization
// runs at the producer, in the fold and again in any projection, so a rule
// that changed its answer on a second pass would be the interop defect it
// exists to remove. Swept over every code point.
func TestIdempotent(t *testing.T) {
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		s := string(r)
		once := person.FoldValue(s)
		if twice := person.FoldValue(once); twice != once {
			t.Fatalf("FoldValue is not idempotent on %U: % x then % x", r, once, twice)
		}
	}
}

// composingPair is one canonical composition: a and b compose to c.
type composingPair struct{ a, b, c rune }

// canonicalCompositions enumerates every canonical composition in the pinned
// Unicode version, by decomposing every code point and keeping the ones whose
// decomposition is a pair that composes back. This is the set the guard must
// never block, and the set whose truncated keys are the defect's whole
// surface, so both sweeps below are exhaustive over it rather than sampled.
func canonicalCompositions(t *testing.T) []composingPair {
	t.Helper()
	var pairs []composingPair
	for c := rune(0); c <= 0x10FFFF; c++ {
		if c >= 0xD800 && c <= 0xDFFF {
			continue
		}
		d := []rune(norm.NFD.String(string(c)))
		if len(d) != 2 {
			continue
		}
		if norm.NFC.String(string(d)) != string(c) {
			continue // a composition exclusion; NFC never rebuilds it
		}
		pairs = append(pairs, composingPair{d[0], d[1], c})
	}
	if len(pairs) < 1000 {
		t.Fatalf("test setup: found only %d canonical compositions, expected ~1097", len(pairs))
	}
	return pairs
}

// TestCanonicalCompositionsSurviveTheGuard checks the guard's cost: it must
// reject nothing real. Every canonical composition in Unicode 17.0.0 has to
// still compose, both on its own and with text around it, or the guard has
// traded a merge defect for a split one.
func TestCanonicalCompositionsSurviveTheGuard(t *testing.T) {
	for _, p := range canonicalCompositions(t) {
		in := string([]rune{p.a, p.b})
		if got := person.NFC(in); got != string(p.c) {
			t.Errorf("NFC(%U + %U) = %U, want %U — the guard blocked a real composition",
				p.a, p.b, []rune(got), p.c)
		}
		// In context, so segmentation cannot quietly drop the pair.
		padded := "x" + in + "y"
		if got := person.NFC(padded); got != "x"+string(p.c)+"y" {
			t.Errorf("NFC(x + %U + %U + y) = %U, want x%Uy", p.a, p.b, []rune(got), p.c)
		}
	}
}

// TestTruncatedKeyCollisionsAreRejected sweeps the defect's entire surface.
// x/text keys a composition on uint16(a)<<16|uint16(b), so for every real
// composition (a, b) there are fifteen impostor pairs per element — the same
// low sixteen bits in another plane — that the library will compose and that
// must not compose. Each one merges two distinct people into one identity.
func TestTruncatedKeyCollisionsAreRejected(t *testing.T) {
	pairs := canonicalCompositions(t)
	real := make(map[[2]rune]rune, len(pairs))
	for _, p := range pairs {
		real[[2]rune{p.a, p.b}] = p.c
	}

	expect := func(t *testing.T, a, b rune) {
		t.Helper()
		if a > 0x10FFFF || b > 0x10FFFF {
			return
		}
		if a >= 0xD800 && a <= 0xDFFF || b >= 0xD800 && b <= 0xDFFF {
			return
		}
		in := string([]rune{a, b})
		want := in
		if c, ok := real[[2]rune{a, b}]; ok {
			want = string(c)
		}
		if got := person.NFC(in); got != want {
			t.Errorf("NFC(%U + %U) = %U, want %U", a, b, []rune(got), []rune(want))
		}
	}

	for _, p := range pairs {
		for plane := rune(1); plane <= 16; plane++ {
			expect(t, p.a+plane<<16, p.b)
			expect(t, p.a, p.b+plane<<16)
		}
	}
}
