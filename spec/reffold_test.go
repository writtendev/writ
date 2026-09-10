package spec_test

import (
	"strings"
	"testing"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec"
)

// normalizePersonInputs pins the axes where two spellings of the rule could
// plausibly disagree: leading and trailing whitespace of several kinds, mixed
// case in either half, the empty and all-whitespace strings, non-ASCII case
// folding, where the split falls when the value carries its own colon, and the
// colonless strings that are not conforming identifiers at all.
//
// Since WRIT-117 it also pins every step of the folding algorithm, because two
// copies of a three-step rule have far more ways to drift than two copies of
// strings.ToLower did: composition, the case fold itself, the second
// composition, and the four x/text behaviours each copy has to work around.
// The last of those is what this table missed once already — see below.
var normalizePersonInputs = []string{
	"",
	" ",
	"\t\r\n",
	":",
	"email:",
	":alice@example.com",
	"email:alice@example.com",
	"Email:Alice@Example.COM",
	"  email:alice@example.com  ",
	"\t\n EMAIL:Alice@Example.COM \r\n",
	"email:  Alice@Example.COM  ",
	"EMAIL:DEV+1@EXAMPLE.COM",
	"user:alice",
	"USER:Alice",
	"keybase:Alice",
	`email:"a:b"@example.com`,
	`EMAIL:"A:B"@Example.COM`,
	"email:a:b:c",
	"email:ÉLODIE@Example.COM",
	// Colonless: not conforming identifiers, but both copies of the rule must
	// still agree on what they fold to.
	"alice@example.com",
	"Alice@Example.COM",
	"  alice@example.com  ",
	"\t\n Alice@Example.COM \r\n",
	"DEV+1@EXAMPLE.COM",
	"Alice Example",
	"ÉLODIE@Example.COM",
	"Ünïcodé Nàme",
	"ΣΊΣΥΦΟΣ@example.com",
	"ИВАН@ПРИМЕР.РФ",
	"  日本語  ",
	"\u00a0NBSP@Example.COM\u00a0",
	// The folding algorithm, step by step.
	"user:\u0130",                             // the pinned case fold, on the character that motivated pinning it
	"user:\u00df",                             // full folding, not simple
	"user:\u1e9e",                             // the capital, which simple folding would send to U+00DF instead
	"user:Jos\u0065\u0301",                    // NFC composes a decomposed value
	"user:Jos\u00e9",                          // and leaves the precomposed spelling alone
	"user:\u017f\u0301",                       // folding leaves s+U+0301, which the second NFC composes
	"user:\u13a0",                             // Cherokee uppercase: a fold fixed point x/text toggles
	"user:\uab70",                             // Cherokee lowercase, which folds up (AB70..ABBF -> 13A0..13EF)
	"user:\u13f8",                             // and Cherokee's *second* fold range (13F8..13FD -> 13F0..13F5)
	"user:\U00010041\u0300",                   // a supplementary starter that must not compose with its mark
	"user:\U00011099\U000110ba",               // a supplementary pair that must
	"user:\U00011347\U0001133e",               // and one whose second element is a starter
	"user:\u1100\u1161",                       // Hangul, the composition between two starters
	"user:\u00e9\U00010041\u0300\u0065\u0301", // a false composition must not cost its neighbours
	"\u0130:alice",                            // a non-conforming scheme, where the two copies still must agree
	"\U00010041\u0300@example.com",            // colonless, and past the ASCII fast path
	// One input per defect the folding implementations work around, mirroring
	// the list in the nfc doc comment. Every one of these has actually
	// diverged between the two copies at some point: a hand-written table only
	// covers the cases somebody thought to write down, and these are the ones
	// that were paid for.
	"user:\U00010041\u0300",           // 1: the truncated composition key
	"user:a" + longMarkRun + "\u0301", // 2: Stream-Safe Text, and the boundary that follows from it
	"user:\u00e1" + longMarkRun,       // 2 again, from the spelling it has to match
	"user:\u00c5\u0bd7\u0316\u0301",   // 3: composing across a ccc-0 blocker
	"user:\u00c5\u0bd7\u0316\u0301\u0316\u0301",
	"user:a\xff\u0341", // invalid UTF-8, where the two copies took different exits
}

// longMarkRun is thirty U+0316: one more than x/text will compose before it
// gives up and inserts U+034F.
var longMarkRun = strings.Repeat("\u0316", 30)

// TestReffoldPinnedUnicodeVersion binds the reference fold's copy of the rule
// to the Unicode tables x/text actually compiled in. x/text selects tables by
// Go build tag rather than by module version, so a toolchain bump would
// otherwise change the reference implementation's answers — and with them the
// conformance goldens every other implementation is checked against — with no
// change to this repository at all.
func TestReffoldPinnedUnicodeVersion(t *testing.T) {
	if norm.Version != spec.PersonUnicodeVersion {
		t.Errorf("x/text/unicode/norm is Unicode %s, but spec/identifiers.md pins %s",
			norm.Version, spec.PersonUnicodeVersion)
	}
	if cases.UnicodeVersion != spec.PersonUnicodeVersion {
		t.Errorf("x/text/cases is Unicode %s, but spec/identifiers.md pins %s",
			cases.UnicodeVersion, spec.PersonUnicodeVersion)
	}
}

// TestReffoldNormalizePersonMatchesEngine binds the reference fold's local copy
// of the person-identifier normalization rule to the engine's one definition,
// which lives in engine/internal/person and is reached here through the
// exported state.NormalizePerson — the name that holds folded person values.
// reffold.go deliberately does not import engine code: it is the standalone
// reference independent implementations read, and engine/internal is not
// reachable from spec/ in any case. This test is what stops the two copies
// drifting.
func TestReffoldNormalizePersonMatchesEngine(t *testing.T) {
	for _, in := range normalizePersonInputs {
		if got, want := spec.NormalizePerson(in), state.NormalizePerson(in); got != want {
			t.Errorf("reffold normalizePerson(%q) = %q, state.NormalizePerson(%q) = %q", in, got, in, want)
		}
	}
}

// FuzzReffoldNormalizePersonMatchesEngine covers the inputs the table above
// cannot enumerate.
func FuzzReffoldNormalizePersonMatchesEngine(f *testing.F) {
	for _, in := range normalizePersonInputs {
		f.Add(in)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if got, want := spec.NormalizePerson(in), state.NormalizePerson(in); got != want {
			t.Errorf("reffold normalizePerson(%q) = %q, state.NormalizePerson(%q) = %q", in, got, in, want)
		}
	})
}

// streamSafeInputs pins the Stream-Safe Text admissibility boundary
// TestReffoldPersonValueIsStreamSafeMatchesEngine and its Fuzz check bind
// between spec/reffold.go's personValueIsStreamSafe and the engine's
// IsStreamSafe: one non-starter run under the limit, exactly at it, one past
// it, and well past it, plus the axes personValueIsStreamSafe's own doc
// comment says it must get right independently of normalization: a run
// broken by a ccc-0 blocker partway through, a colonless string, and invalid
// UTF-8.
//
// This is deliberately its own table rather than a reuse of
// normalizePersonInputs above. That table's boundary cases were chosen to
// pin normalization equivalence, and every one of them composes to a 31-run
// (see longMarkRun's doc comment) — so on their own they cannot distinguish
// "> 30" from ">= 30" the way TestValidPersonVectors' at-limit vector must:
// both predicates already agree that a 31-run is refused.
var streamSafeInputs = []string{
	"",
	"user:a",
	"user:a" + strings.Repeat("̖", 29), // one under the limit
	"user:a" + strings.Repeat("̖", 30), // exactly at the limit
	"user:a" + strings.Repeat("̖", 31), // one past it
	"user:a" + strings.Repeat("̖", 40), // well past it
	"user:Åௗ̖́",         // a run broken by a ccc-0 blocker
	"user:a" + longMarkRun + "́",       // normalizePersonInputs' boundary case: composes to a 31-run
	"alice@example.com",                     // colonless, but still has a value to measure
	"user:a\xff́",                      // invalid UTF-8
}

// TestReffoldPersonValueIsStreamSafeMatchesEngine binds the reference fold's
// local copy of the Stream-Safe Text admissibility rule
// (personMaxNonStarterRun / personValueIsStreamSafe in spec/reffold.go) to the
// engine's one definition (MaxNonStarterRun / IsStreamSafe in
// engine/internal/person), reached here through the exported
// state.PersonValueIsStreamSafe on the same terms
// TestReffoldNormalizePersonMatchesEngine reaches state.NormalizePerson.
// reffold.go's copy sits outside the region TestReffoldIsTheSameAlgorithmAsTheEngine
// compares source-for-source, because it backs a producer-side rule and
// reffold.go is a reference fold, not a reference producer — so this
// behavioural test is what stops the two copies drifting on which values are
// admissible, the way the test above stops them drifting on normalization.
func TestReffoldPersonValueIsStreamSafeMatchesEngine(t *testing.T) {
	for _, in := range streamSafeInputs {
		if got, want := spec.PersonValueIsStreamSafe(in), state.PersonValueIsStreamSafe(in); got != want {
			t.Errorf("reffold personValueIsStreamSafe(%q) = %v, state.PersonValueIsStreamSafe(%q) = %v", in, got, in, want)
		}
	}
}

// FuzzReffoldPersonValueIsStreamSafeMatchesEngine covers the inputs the table
// above cannot enumerate.
func FuzzReffoldPersonValueIsStreamSafeMatchesEngine(f *testing.F) {
	for _, in := range streamSafeInputs {
		f.Add(in)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if got, want := spec.PersonValueIsStreamSafe(in), state.PersonValueIsStreamSafe(in); got != want {
			t.Errorf("reffold personValueIsStreamSafe(%q) = %v, state.PersonValueIsStreamSafe(%q) = %v", in, got, in, want)
		}
	})
}

// emptyScalarRules is a synthetic rule table for the two tests below, which
// pin how the reference fold treats an empty scalar — an empty string under
// lww, and a person-ref that normalizes to nothing. Writ ships no vocabulary
// to borrow a table from (spec/schema-ops.md §Bootstrap), so the rules a
// consumer's schema would declare are stated here instead.
var emptyScalarRules = []spec.FieldRule{
	{OpType: "create", OpVersion: 1, Field: "subject", Strategy: "create-once", ValueType: "object-ref"},
	{OpType: "create", OpVersion: 1, Field: "text", Strategy: "lww", ValueType: "text"},
	{OpType: "edit", OpVersion: 1, Field: "text", Strategy: "lww", ValueType: "text"},
	{OpType: "resolve", OpVersion: 1, Field: "resolved", Strategy: "lww", ValueType: "bool"},
	{OpType: "resolve", OpVersion: 1, Field: "resolved_by", Strategy: "lww", ValueType: "person-ref"},
}

func TestReffoldEmptyScalars(t *testing.T) {
	rules := emptyScalarRules

	ops := []spec.MergeOp{
		{
			ID:        "c-create",
			Time:      1767225600,
			ObjectID:  "n-spec-scalar",
			OpType:    "create",
			OpVersion: 1,
			Body: map[string]any{
				"subject": map[string]any{
					"object_type": "widget",
					"object_id":   "w-1",
				},
				"text": "Initial text",
			},
		},
		{
			ID:        "c-edit",
			Parents:   []string{"c-create"},
			Time:      1767225660,
			ObjectID:  "n-spec-scalar",
			OpType:    "edit",
			OpVersion: 1,
			Body: map[string]any{
				"text": "",
			},
		},
		{
			ID:        "c-resolve",
			Parents:   []string{"c-edit"},
			Time:      1767225720,
			ObjectID:  "n-spec-scalar",
			OpType:    "resolve",
			OpVersion: 1,
			Body: map[string]any{
				"resolved":    true,
				"resolved_by": "   ",
			},
		},
	}

	res, err := spec.Fold(ops, rules)
	if err != nil {
		t.Fatalf("spec.Fold failed: %v", err)
	}

	if val, ok := res.State["text"]; !ok {
		t.Errorf("expected 'text' in spec.Fold state")
	} else if val != "" {
		t.Errorf("expected 'text' to be %q, got %q", "", val)
	}

	if val, ok := res.State["resolved_by"]; !ok {
		t.Errorf("expected 'resolved_by' in spec.Fold state")
	} else if val != "" {
		t.Errorf("expected 'resolved_by' to be %q, got %q", "", val)
	}
}

func TestReffoldResolveWithoutResolvedBy(t *testing.T) {
	rules := emptyScalarRules

	ops := []spec.MergeOp{
		{
			ID:        "c-create",
			Time:      1767225600,
			ObjectID:  "n-reffold-no-resolved-by",
			OpType:    "create",
			OpVersion: 1,
			Body: map[string]any{
				"subject": map[string]any{
					"object_type": "widget",
					"object_id":   "w-1",
				},
				"text": "Initial text",
			},
		},
		{
			ID:        "c-resolve-no-resolved-by",
			Parents:   []string{"c-create"},
			Time:      1767225660,
			ObjectID:  "n-reffold-no-resolved-by",
			OpType:    "resolve",
			OpVersion: 1,
			Body: map[string]any{
				"resolved": true,
			},
		},
	}

	res, err := spec.Fold(ops, rules)
	if err != nil {
		t.Fatalf("spec.Fold failed: %v", err)
	}

	if val, ok := res.State["resolved"]; !ok {
		t.Errorf("expected 'resolved' in spec.Fold state")
	} else if val != true {
		t.Errorf("expected 'resolved' to be true, got %v", val)
	}

	if val, ok := res.State["resolved_by"]; ok && val != "" {
		t.Errorf("expected 'resolved_by' to be empty or unset, got %v", val)
	}
}
