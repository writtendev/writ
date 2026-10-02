package ucd

import (
	"testing"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// The vendored tables are generated from x/text's answers at Unicode 17.0.0
// (gen.go), so on a toolchain whose x/text carries Unicode 17.0.0 they must
// agree with it everywhere. On a newer x/text they must still agree on every
// code point assigned at 17.0.0: Unicode's stability policies freeze a
// character's canonical decomposition, combining class, composition and case
// folding once it is assigned, so a disagreement there is drift in these
// tables, not a version difference. Code points unassigned at 17.0.0 are
// outside the comparison, since a later version may assign them — which is
// exactly why spec/identifiers.md refuses them at the producer.
//
// It skips only when x/text is older than the tables.
func requireXText(t *testing.T) {
	t.Helper()
	if norm.Version < Version || cases.UnicodeVersion < Version {
		t.Skipf("x/text is Unicode %s (norm) / %s (cases), older than the vendored %s; nothing to compare against",
			norm.Version, cases.UnicodeVersion, Version)
	}
}

func TestVendoredTablesMatchXText(t *testing.T) {
	requireXText(t)
	fc := cases.Fold()
	bad := 0
	report := func(format string, args ...any) {
		bad++
		if bad <= 30 {
			t.Errorf(format, args...)
		}
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF || !Assigned(r) {
			continue
		}
		s := string(r)

		wantD := norm.NFD.String(s)
		gotD := Decompose(r)
		if gotD == "" {
			gotD = s
		}
		if gotD != wantD {
			report("Decompose(%U) = %U, x/text NFD says %U", r, []rune(gotD), []rune(wantD))
		}

		p := norm.NFC.PropertiesString(s)
		if got, want := CCC(r), p.CCC(); got != want {
			report("CCC(%U) = %d, x/text says %d", r, got, want)
		}
		if got, want := BoundaryBefore(r), p.BoundaryBefore(); got != want {
			report("BoundaryBefore(%U) = %v, x/text says %v", r, got, want)
		}

		// x/text toggles the Cherokee letters where CaseFolding.txt holds them
		// fixed (golang/go#46101); the vendored table holds them fixed.
		wantF := fc.String(s)
		if r >= 0x13A0 && r <= 0x13F5 {
			wantF = s
		}
		gotF := Fold(r)
		if gotF == "" {
			gotF = s
		}
		if gotF != wantF {
			report("Fold(%U) = %U, x/text says %U", r, []rune(gotF), []rune(wantF))
		}
	}
	if bad > 0 {
		t.Errorf("%d disagreements with x/text", bad)
	}
}

// guardedCombine is the question the composition table answers, put to x/text
// the way gen.go put it: a two-rune NFC that yields one rune canonically
// equivalent to its input. The round trip through NFD is what refuses x/text's
// truncated-key impostors (NFC of U+10041 U+0300 is "À").
func guardedCombine(a, b rune) (rune, bool) {
	in := string([]rune{a, b})
	out := norm.NFC.String(in)
	if norm.NFD.String(out) != norm.NFD.String(in) {
		return 0, false
	}
	if rs := []rune(out); len(rs) == 1 {
		return rs[0], true
	}
	return 0, false
}

func TestComposeMatchesXText(t *testing.T) {
	requireXText(t)

	// Every tabled pair composes to what x/text says, and ucd.Compose agrees.
	for _, e := range composeTable {
		if got, ok := guardedCombine(e.a, e.b); !ok || got != e.c {
			t.Errorf("composeTable has %U + %U -> %U; x/text says %U, %v", e.a, e.b, e.c, got, ok)
		}
		if got, ok := Compose(e.a, e.b); !ok || got != e.c {
			t.Errorf("Compose(%U, %U) = %U, %v, want %U", e.a, e.b, got, ok, e.c)
		}
	}

	// The table is complete: its composites are exactly the code points whose
	// canonical decomposition NFC rebuilds, which excludes composition
	// exclusions, singletons and non-starter decompositions. Hangul syllables
	// are arithmetic and not tabled.
	inTable := map[rune]bool{}
	for _, e := range composeTable {
		inTable[e.c] = true
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF || !Assigned(r) || r >= sBase && r < sBase+sCount {
			continue
		}
		s := string(r)
		d := norm.NFD.String(s)
		primary := d != s && norm.NFC.String(d) == s
		if primary != inTable[r] {
			t.Errorf("%U: x/text says primary composite = %v, composeTable has it = %v", r, primary, inTable[r])
		}
	}

	// Hangul, by arithmetic: every L+V and LV+T.
	for l := rune(lBase); l < lBase+lCount; l++ {
		for v := rune(vBase); v < vBase+vCount; v++ {
			checkCompose(t, l, v)
			lv, _ := Compose(l, v)
			for tt := rune(tBase + 1); tt < tBase+tCount; tt++ {
				checkCompose(t, lv, tt)
			}
		}
	}
	// And the Hangul pairs that must not compose.
	checkCompose(t, 0x1161, 0x1100)  // V + L
	checkCompose(t, sBase+1, 0x11A8) // LVT + T
	checkCompose(t, 'a', 0x11A8)
	checkCompose(t, sBase, 0x11A7) // T base is not a trailing consonant
}

func checkCompose(t *testing.T, a, b rune) {
	t.Helper()
	wantC, wantOK := guardedCombine(a, b)
	if gotC, gotOK := Compose(a, b); gotC != wantC || gotOK != wantOK {
		t.Errorf("Compose(%U, %U) = %U, %v; x/text says %U, %v", a, b, gotC, gotOK, wantC, wantOK)
	}
}

// TestAssignedMatchesStdlib compares the assigned set to the standard
// library's, when the standard library's Unicode is the vendored version: the
// same source gen.go reads. A newer stdlib may have assigned more code points,
// so only code points assigned at the vendored version are compared.
func TestAssignedMatchesStdlib(t *testing.T) {
	if unicode.Version != Version {
		t.Skipf("unicode is Unicode %s, not the vendored %s", unicode.Version, Version)
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		want := unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.Cc, unicode.Cf, unicode.Co)
		if got := Assigned(r); got != want {
			t.Errorf("Assigned(%U) = %v, unicode says %v", r, got, want)
		}
	}
}

// TestAssignedBoundaries pins the facts spec/identifiers.md states about the
// set: unassigned code points, noncharacters and the unassigned tail of a
// block are Cn; private use is assigned.
func TestAssignedBoundaries(t *testing.T) {
	for _, r := range []rune{0x0378, 0x0379, 0xFDD0, 0xFDEF, 0xFFFE, 0xFFFF, 0x1FFFE, 0x3FFFD, 0xE0080, 0x10FFFF} {
		if Assigned(r) {
			t.Errorf("Assigned(%U) = true, want false (Cn)", r)
		}
	}
	for _, r := range []rune{0, 'a', 0x7F, 0x0377, 0xE000, 0xF8FF, 0xF0000, 0x10FFFD, 0x1F600} {
		if !Assigned(r) {
			t.Errorf("Assigned(%U) = false, want true", r)
		}
	}
}
