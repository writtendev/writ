package person_test

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"

	"github.com/writtendev/writ/engine/internal/person"
)

// The differential in differential_test.go is exhaustive over the *repertoire*
// and contains no input longer than two code points. Length is a separate
// axis, and it is where the segmentation rule lives: x/text reports
// stream-safe boundaries, which fall mid-segment after 30 non-starters, and
// applies Stream-Safe Text to its output whether or not it is asked. Both
// defects are invisible to a sweep that never builds a long string.
//
// These tests build long ones.

// TestSegmentLenKeepsCompositionsWhole asserts the boundary rule directly
// rather than inferring it from folded output. A segment must never end
// between a base and something that composes onto it, and must not end merely
// because a run of non-starters got long.
func TestSegmentLenKeepsCompositionsWhole(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int // expected segment length in runes
	}{
		{"Hangul L+V", "\u1100\u1161", 2},
		{"Hangul L+V+T", "\u1100\u1161\u11a8", 3},
		{"Grantha starter second element", "\U00011347\U0001133e", 2},
		{"Tamil starter second element", "\u0b92\u0bd7", 2},
		{"Kaithi non-starter", "\U00011099\U000110ba", 2},
		{"base plus one mark", "a\u0301", 2},
		{"base plus 40 marks", "a" + strings.Repeat("\u0316", 40), 41},
		{"two bases", "ab", 1},
		{"base then Hangul", "a\u1100", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := person.SegmentLen(tc.in)
			got := len([]rune(tc.in[:n]))
			if got != tc.want {
				t.Errorf("SegmentLen(%U) covers %d runes, want %d", []rune(tc.in), got, tc.want)
			}
		})
	}
}

// TestStreamSafeBoundaryRegression is the reported defect, pinned. Both
// spellings are conforming identifiers well inside the 320-code-point bound,
// and they name the same person.
//
// Its first two assertions are the PR #97 fold-level regression property and
// hold for the whole n range unconditionally: two spellings of the same
// person normalize identically, and normalization never inserts U+034F no
// matter how long the run gets. WRIT-140's Stream-Safe Text run-length rule
// cannot reach either \u2014 both test NormalizePerson, not Check.
//
// The third assertion tests Check, which is the producer, and producer
// behaviour for a long non-starter run is exactly what WRIT-140 changed: a
// value composes to "\u00e1" (one non-starter, U+0301) followed by n more
// (U+0316), so its NFD carries a run of n+1 consecutive non-starters. Check
// must accept it while that run is at or under person.MaxNonStarterRun and
// refuse it once the run exceeds that \u2014 which pins the boundary itself across
// the whole swept range, rather than only at one hand-picked value.
func TestStreamSafeBoundaryRegression(t *testing.T) {
	for n := 0; n <= 45; n++ {
		a := "user:a" + strings.Repeat("\u0316", n) + "\u0301"
		b := "user:\u00e1" + strings.Repeat("\u0316", n)
		na, nb := person.NormalizePerson(a), person.NormalizePerson(b)
		if na != nb {
			t.Fatalf("n=%d: %U and %U are different people:\n a: %U\n b: %U",
				n, []rune(a), []rune(b), []rune(na), []rune(nb))
		}
		if strings.ContainsRune(na, 0x034F) {
			t.Fatalf("n=%d: normalization inserted U+034F, which was never in the input: %U", n, []rune(na))
		}
		run := n + 1
		want := person.Valid
		if run > person.MaxNonStarterRun {
			want = person.ValueNotStreamSafe
		}
		if got := person.Check(na); got != want {
			t.Fatalf("n=%d: Check(%U) = %v, want %v (composed value's NFD carries a run of %d consecutive non-starters against the %d-run bound)",
				n, []rune(na), got, want, run, person.MaxNonStarterRun)
		}
	}
}

// TestComposePathMatchesLibrary checks the hand-written composition against
// x/text on every input where x/text is trustworthy: below the stream-safe
// limit and off the truncated-key surface. The composition path is only
// *reached* for long runs, which are rare, so without this it would be the
// least-exercised code in the package; here it inherits the whole repertoire.
func TestComposePathMatchesLibrary(t *testing.T) {
	mismatches := 0
	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		s := string(r)
		want := norm.NFC.String(s)
		if norm.NFD.String(want) != norm.NFD.String(s) {
			continue // the truncated-key surface; the guard owns that case
		}
		if got := person.ComposeSegment(s); got != want {
			mismatches++
			if mismatches <= 20 {
				t.Errorf("ComposeSegment(%U) = %U, x/text says %U", r, []rune(got), []rune(want))
			}
		}
	}
	for _, p := range canonicalCompositions(t) {
		in := string([]rune{p.a, p.b})
		if got := person.ComposeSegment(in); got != string(p.c) {
			mismatches++
			if mismatches <= 20 {
				t.Errorf("ComposeSegment(%U + %U) = %U, want %U", p.a, p.b, []rune(got), p.c)
			}
		}
	}
	if mismatches > 0 {
		t.Errorf("%d disagreements between the composition path and x/text", mismatches)
	}
}

// lengthAxisInputs builds the strings the code-point sweeps cannot: bases
// carrying runs of non-starters from nothing up past the stream-safe limit, in
// both canonical and non-canonical order, with a composing mark placed before,
// inside and after the run.
func lengthAxisInputs(t *testing.T) []string {
	t.Helper()
	bases := []rune{
		'a', 'A', 0x00E1, 0x0130, 0x00DF, 0x017F, // Latin, incl. the folding cases
		0x0915,           // Devanagari KA
		0x0B92,           // Tamil O, which composes with a ccc-0 sign
		0x1100,           // Hangul L
		0x11347, 0x11099, // supplementary bases that legitimately compose
		0x10041, // the supplementary base the truncated key mis-composes
		0x13A0,  // Cherokee, the fold fixed point
	}
	// Marks spanning several combining classes, including ccc 0 second
	// elements, so blocking and ordering are both exercised.
	marks := []rune{
		0x0316, // ccc 220
		0x0301, // ccc 230
		0x0334, // ccc 1
		0x093C, // ccc 7
		0x0BD7, // ccc 0, composes backwards
		0x1133E, 0x110BA,
		0x0308, 0x0300, 0x0345,
	}
	lengths := []int{0, 1, 2, 28, 29, 30, 31, 32, 45, 60}

	var out []string
	for _, b := range bases {
		for _, filler := range marks {
			for _, n := range lengths {
				run := strings.Repeat(string(filler), n)
				for _, tail := range marks {
					out = append(out,
						string(b)+run+string(tail),           // composing mark after the run
						string(b)+string(tail)+run,           // and before it
						string(b)+run+string(tail)+run,       // and buried inside
						string(b)+string(tail)+run+string(b), // followed by a fresh segment
					)
				}
			}
		}
	}
	return out
}

// TestLengthAxisDifferentialAgainstCPython runs the length axis past the same
// independent reference the repertoire sweep uses. This is the sweep that
// would have caught the stream-safe boundary defect.
func TestLengthAxisDifferentialAgainstCPython(t *testing.T) {
	inputs := lengthAxisInputs(t)
	t.Logf("length-axis inputs: %d strings, longest %d code points",
		len(inputs), longestRunes(inputs))
	assertMatchesReference(t, inputs)
}

// assertMatchesReference folds every input and requires CPython to agree.
func assertMatchesReference(t *testing.T, inputs []string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; the differential reference cannot be run")
	}

	encoded := make([]string, len(inputs))
	for i, in := range inputs {
		hex := make([]string, 0, len([]rune(in)))
		for _, r := range in {
			hex = append(hex, strconv.FormatInt(int64(r), 16))
		}
		encoded[i] = strings.Join(hex, " ")
	}

	cmd := exec.Command(python, "-c", reference)
	cmd.Stdin = strings.NewReader(strings.Join(encoded, "\n"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the reference implementation: %v\n%s", err, stderr.String())
	}

	lines := strings.Split(string(out), "\n")
	if len(lines) != len(inputs) {
		t.Fatalf("reference returned %d lines for %d inputs", len(lines), len(inputs))
	}
	mismatches := 0
	for i, line := range lines {
		want, err := decodeHexRunes(line)
		if err != nil {
			t.Fatalf("decoding reference output for %q: %v", encoded[i], err)
		}
		if got := person.FoldValue(inputs[i]); got != want {
			mismatches++
			if mismatches <= 15 {
				t.Errorf("FoldValue(%U)\n  got  %U\n  want %U",
					[]rune(inputs[i]), []rune(got), []rune(want))
			}
		}
	}
	if mismatches > 0 {
		t.Errorf("%d of %d length-axis inputs disagree with the reference", mismatches, len(inputs))
	}
}

// TestLengthAxisIdempotent: the property has to survive length too.
func TestLengthAxisIdempotent(t *testing.T) {
	for _, in := range lengthAxisInputs(t) {
		once := person.FoldValue(in)
		if twice := person.FoldValue(once); twice != once {
			t.Fatalf("not idempotent on %U:\n once  %U\n twice %U",
				[]rune(in), []rune(once), []rune(twice))
		}
	}
}

func longestRunes(ss []string) int {
	max := 0
	for _, s := range ss {
		if n := len([]rune(s)); n > max {
			max = n
		}
	}
	return max
}

// adversarialInputs builds pseudorandom strings over an alphabet chosen to hit
// every mechanism at once: composing and non-composing marks across several
// combining classes, ccc-0 marks that combine backwards, Hangul jamo, the
// Cherokee fold fixed points, the supplementary bases the truncated key
// mis-composes and the ones that legitimately compose, and characters whose
// folding changes length. Deterministically seeded so a failure is
// reproducible.
func adversarialInputs() []string {
	alphabet := []rune{
		'a', 'A', 'z', 0x00E1, 0x00E9, 0x00DF, 0x1E9E, 0x017F, 0x0130, 0x0131,
		0x0300, 0x0301, 0x0308, 0x0316, 0x0334, 0x0345, 0x093C, 0x0BD7, 0x0BBE,
		0x0B92, 0x0915, 0x1100, 0x1161, 0x11A8, 0x13A0, 0xAB70,
		0x03A3, 0x03C2, 0x212B, 0x0041, 0x030A,
		0x10041, 0x1042B, 0x11099, 0x110BA, 0x11347, 0x1133E, 0x101FD,
		0x0F73, 0x0F75, 0x0344, 0xFB05, 0x2126,
	}
	// xorshift64*, so the corpus is identical on every machine and every run.
	state := uint64(0x9E3779B97F4A7C15)
	next := func(n int) int {
		state ^= state >> 12
		state ^= state << 25
		state ^= state >> 27
		return int((state * 0x2545F4914F6CDD1D) >> 33 % uint64(n))
	}
	var out []string
	for i := 0; i < 60000; i++ {
		n := 1 + next(80)
		rs := make([]rune, n)
		for j := range rs {
			rs[j] = alphabet[next(len(alphabet))]
		}
		out = append(out, string(rs))
	}
	return out
}

// TestAdversarialDifferentialAgainstCPython is the length-axis sweep without
// the structure: the structured generator can only find defects in the shapes
// it was written to produce, and the stream-safe defect was found by a shape
// nobody had thought to write down until it broke.
func TestAdversarialDifferentialAgainstCPython(t *testing.T) {
	inputs := adversarialInputs()
	t.Logf("adversarial inputs: %d strings, longest %d code points",
		len(inputs), longestRunes(inputs))
	assertMatchesReference(t, inputs)

	for _, in := range inputs {
		once := person.FoldValue(in)
		if twice := person.FoldValue(once); twice != once {
			t.Fatalf("not idempotent on %U:\n once  %U\n twice %U",
				[]rune(in), []rune(once), []rune(twice))
		}
	}
}
