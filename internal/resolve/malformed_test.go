package resolve_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/writtendev/writ/internal/resolve"
)

// hostile100LineTree builds a 100-line target file, big enough that any
// rangeLen up to 100 has somewhere in-bounds to search.
func hostile100LineTree() *resolve.Tree {
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	content := strings.Join(lines, "\n") + "\n"
	return resolve.NewTree(map[string][]byte{"main.go": []byte(content)}, resolve.SHA1)
}

func ctx64() []string {
	out := make([]string, 64)
	for i := range out {
		out[i] = fmt.Sprintf("ctx%d", i)
	}
	return out
}

// TestResolveHostileShapesNoPanic is WRIT-252's own pinning test: every shape
// here either panicked the resolver ladder outright (the two repros) or
// would have if the structural pre-check in resolveSide/sideWellFormed did
// not exist to catch it first. Each case is run through Resolve on a
// Go-built Anchor (the shape ParseAnchor would never itself construct, but
// nothing stops a caller from building directly) and, where the shape can be
// expressed as raw bytes, through ResolveRaw too.
func TestResolveHostileShapesNoPanic(t *testing.T) {
	tree := hostile100LineTree()

	cases := []struct {
		name       string
		side       *resolve.SideAnchor
		wantReason string
	}{
		{
			name: "short-range-elided",
			side: &resolve.SideAnchor{
				Commit: "1111111111111111111111111111111111111111",
				Path:   "main.go",
				Blob:   "2222222222222222222222222222222222222222",
				Range:  &resolve.Range{Start: 5, End: 5},
				Context: &resolve.Context{
					Lines:   ctx64(),
					Omitted: 1,
				},
			},
			wantReason: resolve.ReasonMalformed,
		},
		{
			name: "single-line-elided",
			side: &resolve.SideAnchor{
				Commit: "1111111111111111111111111111111111111111",
				Path:   "main.go",
				Blob:   "2222222222222222222222222222222222222222",
				Range:  &resolve.Range{Start: 1, End: 1},
				Context: &resolve.Context{
					Lines:   ctx64(),
					Omitted: 1,
				},
			},
			wantReason: resolve.ReasonMalformed,
		},
		{
			name: "end-before-start",
			side: &resolve.SideAnchor{
				Commit:  "1111111111111111111111111111111111111111",
				Path:    "main.go",
				Blob:    "2222222222222222222222222222222222222222",
				Range:   &resolve.Range{Start: 5, End: 3},
				Context: &resolve.Context{Lines: []string{"x"}},
			},
			wantReason: resolve.ReasonMalformed,
		},
		{
			name: "start-zero",
			side: &resolve.SideAnchor{
				Commit:  "1111111111111111111111111111111111111111",
				Path:    "main.go",
				Blob:    "2222222222222222222222222222222222222222",
				Range:   &resolve.Range{Start: 0, End: 1},
				Context: &resolve.Context{Lines: []string{"x", "y"}},
			},
			wantReason: resolve.ReasonMalformed,
		},
		{
			name: "omitted-with-short-lines",
			side: &resolve.SideAnchor{
				Commit: "1111111111111111111111111111111111111111",
				Path:   "main.go",
				Blob:   "2222222222222222222222222222222222222222",
				Range:  &resolve.Range{Start: 1, End: 100},
				Context: &resolve.Context{
					Lines:   []string{"not", "sixty-four", "entries"},
					Omitted: 97,
				},
			},
			wantReason: resolve.ReasonMalformed,
		},
		{
			name: "omitted-arithmetic-wrong",
			side: &resolve.SideAnchor{
				Commit: "1111111111111111111111111111111111111111",
				Path:   "main.go",
				Blob:   "2222222222222222222222222222222222222222",
				Range:  &resolve.Range{Start: 1, End: 100},
				Context: &resolve.Context{
					Lines:   ctx64(),
					Omitted: 30,
				},
			},
			wantReason: resolve.ReasonMalformed,
		},
		{
			name: "non-elided-length-mismatch",
			side: &resolve.SideAnchor{
				Commit:  "1111111111111111111111111111111111111111",
				Path:    "main.go",
				Blob:    "2222222222222222222222222222222222222222",
				Range:   &resolve.Range{Start: 10, End: 12},
				Context: &resolve.Context{Lines: []string{"only", "two"}},
			},
			wantReason: resolve.ReasonMalformed,
		},
		{
			name: "range-without-context",
			side: &resolve.SideAnchor{
				Commit: "1111111111111111111111111111111111111111",
				Path:   "main.go",
				Blob:   "2222222222222222222222222222222222222222",
				Range:  &resolve.Range{Start: 1, End: 1},
			},
			wantReason: resolve.ReasonMalformed,
		},
		{
			name: "context-without-range",
			side: &resolve.SideAnchor{
				Commit:  "1111111111111111111111111111111111111111",
				Path:    "main.go",
				Blob:    "2222222222222222222222222222222222222222",
				Context: &resolve.Context{Lines: []string{"x"}},
			},
			wantReason: resolve.ReasonMalformed,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			anchor := resolve.Anchor{Version: 1, New: c.side}

			var res resolve.Resolution
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("Resolve panicked: %v", r)
					}
				}()
				res = resolve.Resolve(anchor, tree)
			}()

			if res.New == nil {
				t.Fatalf("expected a new-side result, got nil")
			}
			if res.New.Outcome != resolve.OutcomeOrphaned {
				t.Errorf("outcome = %q, want %q", res.New.Outcome, resolve.OutcomeOrphaned)
			}
			if res.New.Reason != c.wantReason {
				t.Errorf("reason = %q, want %q", res.New.Reason, c.wantReason)
			}
		})
	}
}

// TestResolveRawHostileShapesNoPanic exercises ResolveRaw directly against
// raw bytes that never pass through a Go-built SideAnchor at all: a
// non-integer version, a side that isn't even an object, and shapes with no
// identifiable side.
func TestResolveRawHostileShapesNoPanic(t *testing.T) {
	tree := hostile100LineTree()

	cases := []struct {
		name    string
		raw     string
		wantOld string // "" means res.Old must be nil
		wantNew string // "" means res.New must be nil
	}{
		{
			name:    "version-as-string",
			raw:     `{"version":"1","new":{"commit":"1111111111111111111111111111111111111111","path":"main.go","blob":"2222222222222222222222222222222222222222"}}`,
			wantNew: resolve.ReasonMalformed,
		},
		{
			name:    "version-missing",
			raw:     `{"new":{"commit":"1111111111111111111111111111111111111111","path":"main.go","blob":"2222222222222222222222222222222222222222"}}`,
			wantNew: resolve.ReasonMalformed,
		},
		{
			name:    "new-as-string",
			raw:     `{"version":1,"new":"oops"}`,
			wantNew: resolve.ReasonMalformed,
		},
		{
			name:    "new-range-not-object",
			raw:     `{"version":1,"new":{"commit":"1111111111111111111111111111111111111111","path":"main.go","blob":"2222222222222222222222222222222222222222","range":"oops","context":{"before":[],"lines":["x"],"after":[]}}}`,
			wantNew: resolve.ReasonMalformed,
		},
		{
			name:    "unsupported-version-wins-over-side-decode-failure",
			raw:     `{"version":2,"new":"oops"}`,
			wantNew: resolve.ReasonUnsupportedVersion,
		},
		{
			// WRIT-252 round 4: version must be decoded under the exact
			// same ±(2^53-1) exact-integer rule as range.start/range.end/
			// context.omitted (spec/value-types.md), not Go's own int
			// literal parsing, which parses this 19-digit literal as the
			// exact int64 9007199254740992 and only then compares it to 1
			// -- giving "unsupported-version" where the shared rule (which
			// every side field is already held to) says the number itself
			// is out of bounds and thus malformed.
			name:    "version-one-past-safe-integer-bound-is-malformed",
			raw:     `{"version":9007199254740992,"new":{"commit":"1111111111111111111111111111111111111111","path":"main.go","blob":"2222222222222222222222222222222222222222"}}`,
			wantNew: resolve.ReasonMalformed,
		},
		{
			// WRIT-252 round 4: "1.0" is a JSON integer under the shared
			// rule (float64(1.0) round-trips through int exactly), the
			// same as range.start/range.end already accept it, so version
			// must resolve as version 1 and proceed to the ladder rather
			// than orphan "malformed" the way Go's own int-literal parsing
			// used to (it errors on the decimal point). The side has no
			// range/context, so it is a whole-file anchor; "main.go"
			// exists in the target tree but the blob given here never
			// matches it, so the ladder's only reachable outcome is
			// "no-candidate" -- which is itself proof version 1.0 reached
			// the ladder at all instead of orphaning at the version gate.
			name:    "version-1.0-is-accepted-as-version-1",
			raw:     `{"version":1.0,"new":{"commit":"1111111111111111111111111111111111111111","path":"main.go","blob":"2222222222222222222222222222222222222222"}}`,
			wantNew: resolve.ReasonNoCandidate,
		},
		{
			name: "non-object-anchor",
			raw:  `"just a string"`,
		},
		{
			name: "bare-number-anchor",
			raw:  `42`,
		},
		{
			name: "no-sides",
			raw:  `{}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var res resolve.Resolution
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("ResolveRaw panicked: %v", r)
					}
				}()
				res = resolve.ResolveRaw([]byte(c.raw), tree)
			}()

			if c.wantOld == "" {
				if res.Old != nil {
					t.Errorf("Old = %+v, want nil", res.Old)
				}
			} else {
				if res.Old == nil || res.Old.Reason != c.wantOld {
					t.Errorf("Old = %+v, want reason %q", res.Old, c.wantOld)
				}
			}

			if c.wantNew == "" {
				if res.New != nil {
					t.Errorf("New = %+v, want nil", res.New)
				}
			} else {
				if res.New == nil || res.New.Reason != c.wantNew {
					t.Errorf("New = %+v, want reason %q", res.New, c.wantNew)
				}
			}
		})
	}
}
