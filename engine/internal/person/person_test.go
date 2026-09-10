package person_test

import (
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/internal/person"
)

func TestNormalizePerson(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"email:alice@example.com", "email:alice@example.com"},
		{"Email:Alice@Example.COM", "email:alice@example.com"},
		{"  EMAIL:alice@example.com  ", "email:alice@example.com"},
		{"\t\n Email:Alice@Example.COM \r\n", "email:alice@example.com"},
		{"email:  Alice@Example.COM  ", "email:alice@example.com"},
		{"user:Alice", "user:alice"},
		{"KeyBase:Alice", "keybase:alice"},
		{"", ""},
		{"   ", ""},
		// Colonless strings are not conforming identifiers. Normalization is
		// not where that is decided, so they fold as flat strings.
		{"  ALICE  ", "alice"},
		{"alice@example.com", "alice@example.com"},
	}

	for _, tc := range cases {
		got := person.NormalizePerson(tc.input)
		if got != tc.want {
			t.Errorf("NormalizePerson(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestSplitFirstColon is the whole reason Split exists as its own function. An
// email address may carry a colon inside a quoted local part, so a rule that
// splits on "a colon" — the last one, or that refuses more than one — reads the
// scheme of `email:"a:b"@example.com` as `email:"a`, which is not a scheme, and
// rejects an identifier the format allows.
func TestSplitFirstColon(t *testing.T) {
	cases := []struct {
		in     string
		scheme string
		value  string
		ok     bool
	}{
		{`email:"a:b"@example.com`, "email", `"a:b"@example.com`, true},
		{"email:a:b:c", "email", "a:b:c", true},
		{"user:alice", "user", "alice", true},
		{":alice", "", "alice", true},
		{"email:", "email", "", true},
		{"alice@example.com", "", "alice@example.com", false},
		{"", "", "", false},
	}

	for _, tc := range cases {
		scheme, value, ok := person.Split(tc.in)
		if scheme != tc.scheme || value != tc.value || ok != tc.ok {
			t.Errorf("Split(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.in, scheme, value, ok, tc.scheme, tc.value, tc.ok)
		}
	}
}

func TestCheck(t *testing.T) {
	atLimitValue := strings.Repeat("a", 320)
	atLimitMultiByte := strings.Repeat("é", 320)
	atLimitScheme := strings.Repeat("a", 32)

	cases := []struct {
		in   string
		want person.Problem
	}{
		{"email:alice@example.com", person.Valid},
		{"user:ci", person.Valid},
		{"keybase:alice", person.Valid},
		{`email:"a:b"@example.com`, person.Valid},
		{"x+ci.bot-2:alice", person.Valid},
		{atLimitScheme + ":alice", person.Valid},
		{"email:" + atLimitValue, person.Valid},
		{"email:" + atLimitMultiByte, person.Valid},

		{"alice@example.com", person.MissingScheme},
		{"alice", person.MissingScheme},
		{"", person.MissingScheme},
		{":alice", person.SchemeCharset},
		{"Email:alice@example.com", person.SchemeCharset},
		{"2fa:alice", person.SchemeCharset},
		{"my_scheme:alice", person.SchemeCharset},
		{strings.Repeat("a", 33) + ":alice", person.SchemeTooLong},
		{"user:", person.EmptyValue},
		{"email:" + atLimitValue + "a", person.ValueTooLong},
		{"email:" + atLimitMultiByte + "é", person.ValueTooLong},
	}

	for _, tc := range cases {
		if got := person.Check(tc.in); got != tc.want {
			t.Errorf("Check(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestValueBoundCountsCodePointsNotBytes pins the unit. A 320-code-point
// multi-byte value is 640 bytes; a byte-counting bound would refuse it, and
// then the engine and the JSON Schema — which counts code points — would
// disagree about the same identifier.
func TestValueBoundCountsCodePointsNotBytes(t *testing.T) {
	id := "email:" + strings.Repeat("é", 320)
	if n := len(id); n != 646 {
		t.Fatalf("test setup: identifier is %d bytes, want 646", n)
	}
	if got := person.Check(id); got != person.Valid {
		t.Errorf("Check(320 code points / 640 bytes) = %v, want Valid", got)
	}
}

// TestCheckRejectsRatherThanTruncates: Check has no repair path at all, which
// is the point. Truncating an over-long identifier to the bound would map it
// onto whatever shorter identifier shares that prefix, making two people one
// for assignment, approval keying and set membership.
func TestCheckRejectsRatherThanTruncates(t *testing.T) {
	victim := "email:" + strings.Repeat("a", 320)
	attacker := victim + "b"

	if person.Check(victim) != person.Valid {
		t.Fatal("test setup: the shorter identifier should be valid")
	}
	if got := person.Check(attacker); got != person.ValueTooLong {
		t.Fatalf("Check(over-long) = %v, want ValueTooLong", got)
	}
	if person.NormalizePerson(attacker) == victim {
		t.Error("normalization mapped an over-long identifier onto a shorter one")
	}
}

// TestCheckForbiddenCodePoints pins WRIT-137's producer-side repertoire
// rule: every forbidden class is rejected with ForbiddenCodePoint, naming
// the exact code point via FirstForbidden, and the immediate neighbour on
// each side of every range is still Valid -- so the ranges are pinned at
// both edges, not just somewhere inside them.
func TestCheckForbiddenCodePoints(t *testing.T) {
	forbidden := []rune{
		0x0000, 0x0001, 0x001F, // C0
		0x007F,         // DEL
		0x0080, 0x009F, // C1
		0x200B, 0x200C, 0x200D, // zero-width space, ZWNJ, ZWJ
		0x200E, 0x200F, // LRM, RLM
		0x202A, 0x202E, // bidi embeddings/overrides
		0x2066, 0x2069, // bidi isolates
		0xFEFF, // BOM
	}
	for _, r := range forbidden {
		id := "email:ali" + string(r) + "ce@example.com"
		if got := person.Check(id); got != person.ForbiddenCodePoint {
			t.Errorf("Check(%q) with %U = %v, want ForbiddenCodePoint", id, r, got)
		}
		gotR, ok := person.FirstForbidden(id)
		if !ok || gotR != r {
			t.Errorf("FirstForbidden(%q) = %U, %v, want %U, true", id, gotR, ok, r)
		}
	}

	// The immediate neighbour of every forbidden range above is accepted.
	neighbours := []rune{0x0020, 0x007E, 0x00A0, 0x200A, 0x2010, 0x2065, 0x206A, 0xFEFE, 0xFF00}
	for _, r := range neighbours {
		id := "email:ali" + string(r) + "ce@example.com"
		if got := person.Check(id); got != person.Valid {
			t.Errorf("Check(%q) with neighbour %U = %v, want Valid", id, r, got)
		}
	}
}

// TestCheckForbiddenCodePointOrderedBeforeLength pins that a value both too
// long and carrying a forbidden code point is reported as the code point,
// not the length -- naming the offending character beats a generic bound
// error for an input that is both.
func TestCheckForbiddenCodePointOrderedBeforeLength(t *testing.T) {
	id := "email:" + strings.Repeat("a", 320) + string(rune(0x202E))
	if got := person.Check(id); got != person.ForbiddenCodePoint {
		t.Errorf("Check(over-long AND forbidden) = %v, want ForbiddenCodePoint", got)
	}
}

// TestCheckStreamSafeBoundary pins WRIT-140's producer-side Stream-Safe Text
// rule (spec/identifiers.md §Value shape: Stream-Safe Text): Check rejects a
// value once its NFD carries more than MaxNonStarterRun consecutive
// non-starters, pinned at the exact boundary in both directions. Neither "a"
// nor U+0316 composes with anything here, so the run length is exactly n --
// unlike TestStreamSafeBoundaryRegression's composed spelling, which measures
// n+1 for the reason its own comment explains.
func TestCheckStreamSafeBoundary(t *testing.T) {
	for n := person.MaxNonStarterRun - 2; n <= person.MaxNonStarterRun+2; n++ {
		id := "user:a" + strings.Repeat("̖", n)
		want := person.Valid
		if n > person.MaxNonStarterRun {
			want = person.ValueNotStreamSafe
		}
		if got := person.Check(id); got != want {
			t.Errorf("n=%d: Check(%q) = %v, want %v", n, id, got, want)
		}
	}
}

// TestCheckStreamSafeInteriorBlockerResetsRun pins that the rule counts a
// *run* of non-starters, not a value's total non-starter count. A ccc-0 code
// point in the middle -- even one that combines backwards onto the base, like
// the Tamil vowel sign nfc's own compose treats as a blocker -- resets the
// run, so two runs of MaxNonStarterRun either side of it are still Valid even
// though the value carries 2*MaxNonStarterRun non-starters in total.
func TestCheckStreamSafeInteriorBlockerResetsRun(t *testing.T) {
	half := strings.Repeat("̖", person.MaxNonStarterRun)
	id := "user:a" + half + "ௗ" + half
	if got := person.Check(id); got != person.Valid {
		t.Errorf("Check(%q) = %v, want Valid (the ccc-0 blocker should reset the run)", id, got)
	}
}

// TestCheckStreamSafeOrderedAfterLength pins the stated order in Check: the
// non-starter-run check is the most expensive of its tests -- it decomposes
// every rune of the value -- so it runs last, after ValueTooLong. A value
// that is both too long and carries an over-long run is reported as
// ValueTooLong.
func TestCheckStreamSafeOrderedAfterLength(t *testing.T) {
	id := "email:" + strings.Repeat("a", person.MaxValueLen) + strings.Repeat("̖", person.MaxNonStarterRun+1)
	if got := person.Check(id); got != person.ValueTooLong {
		t.Errorf("Check(over-long AND over-run) = %v, want ValueTooLong", got)
	}
}

// TestFoldValueUnaffectedByStreamSafeRule pins that WRIT-140 changes nothing
// about FoldValue: a value whose run Check now refuses still folds exactly as
// it always has, because FoldValue never calls Check and the fold path is
// untouched (spec/fold.md). None of these marks compose with "a" or with each
// other, and they are already in canonical order, so FoldValue -- NFC, case
// fold, NFC -- has nothing to change.
func TestFoldValueUnaffectedByStreamSafeRule(t *testing.T) {
	value := "a" + strings.Repeat("̖", person.MaxNonStarterRun+10)
	if got := person.Check("user:" + value); got != person.ValueNotStreamSafe {
		t.Fatalf("test setup: Check(%q) = %v, want ValueNotStreamSafe", value, got)
	}
	if got := person.FoldValue(value); got != value {
		t.Errorf("FoldValue(%U) = %U, want unchanged", []rune(value), []rune(got))
	}
}

func TestDerivedMaxLen(t *testing.T) {
	if person.MaxLen != person.MaxSchemeLen+1+person.MaxValueLen {
		t.Errorf("MaxLen = %d, want %d", person.MaxLen, person.MaxSchemeLen+1+person.MaxValueLen)
	}
	if person.MaxLen != 353 {
		t.Errorf("MaxLen = %d, want 353 (the number spec/identifiers.md and identifiers.schema.json state)", person.MaxLen)
	}
}
