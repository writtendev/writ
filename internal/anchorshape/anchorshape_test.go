package anchorshape_test

import (
	"testing"

	"github.com/writtendev/writ/internal/anchorshape"
)

// wellFormedSide builds a structurally valid v1 side anchor with the given
// range and context overrides, so each case below isolates the one
// range/context shape under test against otherwise-ordinary commit, path,
// and blob fields.
func wellFormedSide(rangeVal, contextOverrides map[string]any) map[string]any {
	return map[string]any{
		"commit":  "1111111111111111111111111111111111111111",
		"path":    "main.go",
		"blob":    "2222222222222222222222222222222222222222",
		"range":   rangeVal,
		"context": mergeContext(contextOverrides),
	}
}

func mergeContext(overrides map[string]any) map[string]any {
	out := map[string]any{
		"before": []any{},
		"lines":  []any{"line"},
		"after":  []any{},
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

func makeLines(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = "line"
	}
	return out
}

// TestSideWellFormedIntegerBound is the regression test for WRIT-252 round
// 3: jsonInt must reject a JSON number outside the float64-exact integer
// range before ever converting it to int, rather than convert an
// out-of-range float and rely on whatever an architecture's float-to-int
// instruction happens to produce.
//
// Converting an out-of-range float64 to int is implementation-defined by
// the Go spec, and it really does differ between amd64 and arm64: on this
// machine (darwin/arm64), int64(9223372036854775808.0) (2^63, one past the
// largest float64 an int64 can hold) saturates to math.MaxInt64, but
// GOARCH=amd64 gives math.MinInt64 for the identical float64 -- the two
// architectures' floating-point-to-integer conversion instructions disagree
// on the sign of an out-of-range result. jsonInt's old round-trip check
// (`float64(int(f)) != f`) happened to accept f == 2^63 exactly on arm64,
// because float64(MaxInt64) also rounds to 2^63 -- a coincidental match
// that made round-3's finding possible: a side anchor with range.end ==
// 2^63 (JSON's "9223372036854776000") and a start/omitted chosen so the
// rest of the arithmetic lines up against MaxInt64 was well-formed on
// arm64 but malformed on amd64, for the exact same bytes.
//
// The boundary itself is round 4's finding: it must be the same
// ±(2^53-1) safe-integer bound as everywhere else in writ
// (spec/value-types.md's int/number rows, engine/internal/value's
// maxSafeInt, engine/codec/valuetype_test.go's "int bound"/"number bound"
// vectors) -- not ±2^53, which is one past the boundary those already
// treat as invalid.
func TestSideWellFormedIntegerBound(t *testing.T) {
	const maxSafe = float64(1<<53 - 1)

	cases := []struct {
		name string
		side map[string]any
		want bool
	}{
		{
			name: "end at the ±(2^53-1) boundary is well-formed",
			// start=1, end=2^53-1 => size = 2^53-1; omitted must be size-64.
			side: wellFormedSide(
				map[string]any{"start": float64(1), "end": maxSafe},
				map[string]any{"lines": makeLines(64), "omitted": maxSafe - 64},
			),
			want: true,
		},
		{
			name: "end one past the ±(2^53-1) boundary, at 2^53, is malformed",
			// 2^53 is still exactly float64-representable (the doubling
			// step to 2 per integer only starts above 2^53), so this is
			// not a precision-loss case -- it is squarely the bound
			// jsonInt must enforce: anything outside ±(2^53-1) is
			// malformed even though the float64 itself is exact.
			side: wellFormedSide(
				map[string]any{"start": float64(1), "end": maxSafe + 1},
				map[string]any{"lines": makeLines(64), "omitted": maxSafe + 1 - 64},
			),
			want: false,
		},
		{
			name: "round-3 repro: range.end == 2^63 with start/omitted chosen against MaxInt64",
			// This is the WRIT-252 round-3 finding's exact shape, reduced
			// to the values that make it reproduce: verified (see the
			// fixer's report) to be accepted (true) on darwin/arm64 but
			// refused (false) on GOARCH=amd64 under the pre-fix jsonInt --
			// and, with the fix, rejected on both.
			//
			// start=960 so that MaxInt64-63-960 lands exactly on the
			// float64-representable grid point 2^63-1024, matching what
			// arm64's saturating conversion of end and omitted would
			// otherwise let through as self-consistent arithmetic.
			side: wellFormedSide(
				map[string]any{"start": float64(960), "end": float64(9223372036854776000)},
				map[string]any{"lines": makeLines(64), "omitted": float64(9223372036854774784)},
			),
			want: false,
		},
		{
			name: "negative end far below the boundary is malformed",
			side: wellFormedSide(
				map[string]any{"start": float64(1), "end": -maxSafe - 2},
				map[string]any{},
			),
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := anchorshape.SideWellFormed(c.side)
			if got != c.want {
				t.Errorf("SideWellFormed() = %v, want %v", got, c.want)
			}
		})
	}
}
