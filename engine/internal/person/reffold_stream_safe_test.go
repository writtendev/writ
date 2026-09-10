package person_test

import (
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/internal/person"
	"github.com/writtendev/writ/spec"
)

// longMarkRun is thirty U+0316: one more than x/text will compose before it
// gives up and inserts U+034F. A local copy of spec/reffold_test.go's own
// longMarkRun of the same name and value — this package cannot reach that
// one, since it is unexported and lives in a different package.
var longMarkRun = strings.Repeat("̖", 30)

// streamSafeInputs pins the Stream-Safe Text admissibility boundary
// TestReffoldPersonValueIsStreamSafeMatchesEngine and its Fuzz check bind
// between spec/reffold.go's PersonValueIsStreamSafe and this package's
// IsStreamSafe: one non-starter run under the limit, exactly at it, one past
// it, and well past it, plus the axes IsStreamSafe's own doc comment says it
// must get right independently of normalization: a run broken by a ccc-0
// blocker partway through, a colonless string, and invalid UTF-8.
//
// This is deliberately its own table rather than a reuse of
// spec/reffold_test.go's normalizePersonInputs. That table's boundary cases
// were chosen to pin normalization equivalence, and every one of them
// composes to a 31-run (see longMarkRun's doc comment) — so on their own
// they cannot distinguish "> 30" from ">= 30" the way TestValidPersonVectors'
// at-limit vector must: both predicates already agree that a 31-run is
// refused.
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
	"user:a\xff́",                      // invalid UTF-8
}

// TestReffoldPersonValueIsStreamSafeMatchesEngine binds the reference fold's
// copy of the Stream-Safe Text admissibility rule
// (spec.PersonValueIsStreamSafe, in spec/reffold.go) to this package's one
// definition (MaxNonStarterRun / IsStreamSafe), imported here directly
// rather than through engine/state: api/engine.txt is generated from
// ./engine only, and this package is under engine/internal, so importing
// spec from here — spec has no engine imports, so there is no cycle — binds
// the two copies without adding anything to the engine's public API
// baseline. reffold.go's copy sits outside the region
// TestReffoldIsTheSameAlgorithmAsTheEngine compares source-for-source,
// because it backs a producer-side rule and reffold.go is a reference fold,
// not a reference producer — so this behavioural test is what stops the two
// copies drifting on which values are admissible, the way
// spec/reffold_test.go's TestReffoldNormalizePersonMatchesEngine stops them
// drifting on normalization.
func TestReffoldPersonValueIsStreamSafeMatchesEngine(t *testing.T) {
	for _, in := range streamSafeInputs {
		if got, want := spec.PersonValueIsStreamSafe(in), person.IsStreamSafe(in); got != want {
			t.Errorf("reffold PersonValueIsStreamSafe(%q) = %v, person.IsStreamSafe(%q) = %v", in, got, in, want)
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
		if got, want := spec.PersonValueIsStreamSafe(in), person.IsStreamSafe(in); got != want {
			t.Errorf("reffold PersonValueIsStreamSafe(%q) = %v, person.IsStreamSafe(%q) = %v", in, got, in, want)
		}
	})
}
