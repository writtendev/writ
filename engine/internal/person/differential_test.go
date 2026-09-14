package person_test

import (
	"bufio"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"golang.org/x/text/unicode/norm"
	"golang.org/x/text/unicode/rangetable"

	"github.com/writtendev/writ/engine/internal/person"
)

// reference is the folding algorithm spec/identifiers.md specifies, written in
// somebody else's Unicode implementation. CPython carries its own tables and
// shares no code with x/text, so agreeing with it is evidence that the
// algorithm is portable rather than evidence that one library is
// self-consistent — which is the only claim worth making about a rule whose
// whole job is for two independent implementations to reach the same answer.
const reference = `
import sys, unicodedata
out = []
for line in sys.stdin.buffer.read().decode('utf-8').splitlines():
    if not line:
        out.append('')
        continue
    s = ''.join(chr(int(h, 16)) for h in line.split())
    f = unicodedata.normalize('NFC', unicodedata.normalize('NFC', s).casefold())
    out.append(' '.join('%X' % ord(c) for c in f))
sys.stdout.write('\n'.join(out))
`

// TestDifferentialAgainstCPython folds every code point assigned in the pinned
// Unicode version, every canonical composition, and every truncated-key
// impostor pair, and requires CPython to agree on all of them.
//
// This sweep is exhaustive over the *repertoire*, not over input length: no
// input here is longer than two code points. Length is a separate axis with
// its own defects, and length_test.go is where it is covered.
//
// Inputs are restricted to what the host CPython has assigned (or the pinned
// version, whichever is earlier): for characters assigned in both, Unicode's
// stability policies make composition and case folding fixed, so a disagreement
// is this implementation's defect and not a version skew. Full differential
// cross-checks for Unicode 17 additions are suspended until Python 3.15 is
// released with Unicode 17 support. Skipped where python3 is unavailable.
func TestDifferentialAgainstCPython(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; the differential reference cannot be run")
	}

	pyVerOut, err := exec.Command(python, "-c", "import unicodedata; print(unicodedata.unidata_version)").Output()
	if err != nil {
		t.Fatalf("querying host python unicodedata.unidata_version: %v", err)
	}
	pyVersion := strings.TrimSpace(string(pyVerOut))

	targetVersion := person.UnicodeVersion
	if pyVersion != "" && pyVersion < person.UnicodeVersion {
		t.Logf("host python unicodedata.unidata_version is %s (earlier than pinned Unicode %s); "+
			"capping differential comparison repertoire to %s (full differential cross-checks "+
			"for Unicode 17 additions are suspended until Python 3.15)",
			pyVersion, person.UnicodeVersion, pyVersion)
		targetVersion = pyVersion
	}

	assigned := rangetable.Assigned(targetVersion)
	if assigned == nil {
		t.Skipf("x/text carries no range table for Unicode %s", targetVersion)
	}

	var inputs []string
	// Every code point in an input must be assigned in the compared version.
	// The filter is not decoration: the impostor sweep below reaches into
	// higher planes, and a code point CPython knows about and targetVersion
	// does not (or vice versa) would report a version difference as an
	// implementation defect.
	// U+10D57 (Garay, assigned in 16.0.0) is one such, and did.
	add := func(rs ...rune) {
		hex := make([]string, len(rs))
		for i, r := range rs {
			if !unicode.Is(assigned, r) {
				return
			}
			hex[i] = strconv.FormatInt(int64(r), 16)
		}
		inputs = append(inputs, strings.Join(hex, " "))
	}

	for r := rune(0); r <= 0x10FFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		add(r)
	}
	singles := len(inputs)

	pairs := canonicalCompositions(t)
	for _, p := range pairs {
		add(p.a, p.b)
		for plane := rune(1); plane <= 16; plane++ {
			if a := p.a + plane<<16; a <= 0x10FFFF {
				add(a, p.b)
			}
			if b := p.b + plane<<16; b <= 0x10FFFF {
				add(p.a, b)
			}
		}
	}
	t.Logf("differential inputs: %d single code points, %d pair cases, %d total",
		singles, len(inputs)-singles, len(inputs))

	cmd := exec.Command(python, "-c", reference)
	cmd.Stdin = strings.NewReader(strings.Join(inputs, "\n"))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the reference implementation: %v\n%s", err, stderr.String())
	}

	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	mismatches := 0
	for i := 0; sc.Scan(); i++ {
		if i >= len(inputs) {
			t.Fatalf("reference returned more lines than it was given")
		}
		want, err := decodeHexRunes(sc.Text())
		if err != nil {
			t.Fatalf("decoding reference output for %q: %v", inputs[i], err)
		}
		in, err := decodeHexRunes(inputs[i])
		if err != nil {
			t.Fatalf("decoding input %q: %v", inputs[i], err)
		}
		if got := person.FoldValue(in); got != want {
			mismatches++
			if mismatches <= 20 {
				t.Errorf("FoldValue(%U) = %U, CPython says %U",
					[]rune(in), []rune(got), []rune(want))
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading reference output: %v", err)
	}
	if mismatches > 0 {
		t.Errorf("%d of %d inputs disagree with the reference implementation", mismatches, len(inputs))
	}
}

// TestDifferentialCatchesTheDefects is the differential's own test: a sweep
// that agrees with everything proves nothing. It runs the raw x/text pipeline
// — no Cherokee fixed points, no composition of its own — past CPython and
// requires CPython to reject it.
//
// Comparing the naive pipeline against FoldValue would be the weaker test:
// two implementations that broke the same way would agree, and the comparison
// would pass. The reference is the independent one, so the reference is what
// this compares against.
func TestDifferentialCatchesTheDefects(t *testing.T) {
	naive := func(s string) string {
		return norm.NFC.String(person.CaseFoldRaw(norm.NFC.String(s)))
	}
	cases := []struct {
		in  string
		why string
	}{
		{"\u13a0", "Cherokee fold toggle"},
		{"\U00010041\u0300", "truncated composition key"},
		{"a" + strings.Repeat("\u0316", 30) + "\u0301", "stream-safe boundary and U+034F insertion"},
		{"\u00c5\u0bd7\u0316\u0301", "composition across a blocker"},
	}
	inputs := make([]string, len(cases))
	for i, tc := range cases {
		inputs[i] = tc.in
	}
	want := referenceFold(t, inputs)

	for i, tc := range cases {
		if got := person.FoldValue(tc.in); got != want[i] {
			t.Errorf("FoldValue(%U) = %U, reference says %U — the guarded path is wrong for %s",
				[]rune(tc.in), []rune(got), []rune(want[i]), tc.why)
		}
		if naive(tc.in) == want[i] {
			t.Errorf("the raw x/text pipeline already agrees with the reference on %U (%s); "+
				"this case no longer demonstrates anything", []rune(tc.in), tc.why)
		}
	}
}

// referenceFold folds each input with CPython and returns the answers in
// order. Skips the test when python3 is unavailable.
func referenceFold(t *testing.T, inputs []string) []string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; the differential reference cannot be run")
	}
	encoded := make([]string, len(inputs))
	for i, in := range inputs {
		var hex []string
		for _, r := range in {
			hex = append(hex, strconv.FormatInt(int64(r), 16))
		}
		encoded[i] = strings.Join(hex, " ")
	}
	cmd := exec.Command(python, "-c", reference)
	cmd.Stdin = strings.NewReader(strings.Join(encoded, "\n"))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running the reference implementation: %v", err)
	}
	lines := strings.Split(string(out), "\n")
	if len(lines) != len(inputs) {
		t.Fatalf("reference returned %d lines for %d inputs", len(lines), len(inputs))
	}
	got := make([]string, len(lines))
	for i, line := range lines {
		d, err := decodeHexRunes(line)
		if err != nil {
			t.Fatalf("decoding reference output: %v", err)
		}
		got[i] = d
	}
	return got
}

func decodeHexRunes(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	var b strings.Builder
	for _, h := range strings.Fields(s) {
		v, err := strconv.ParseInt(h, 16, 32)
		if err != nil {
			return "", fmt.Errorf("bad code point %q: %w", h, err)
		}
		b.WriteRune(rune(v))
	}
	return b.String(), nil
}
