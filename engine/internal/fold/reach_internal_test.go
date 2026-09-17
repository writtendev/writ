package fold

import (
	"testing"

	"github.com/writtendev/writ/engine/codec"
)

// TestLazyReachOracleDefersBuild pins edit 1's laziness promise directly:
// newLazyReachOracle must not call BuildReachability until its first
// IsAncestor question, so a fold that never asks one (lww, create-once,
// set-union, append, lattice, keyed-lww) never pays for the n×⌈n/64⌉
// bitset. This lives in package fold, not fold_test, so it can inspect the
// unexported lazyReachOracle.built field directly rather than asserting
// laziness indirectly through allocation counts, which the benchmarks in
// fold_bench_test.go do separately as belt and braces.
func TestLazyReachOracleDefersBuild(t *testing.T) {
	orderedOps := []OrderedOp{
		{Op: codec.Op{ID: "op-1"}, TStar: 1},
		{Op: codec.Op{ID: "op-2", Parents: []string{"op-1"}}, TStar: 2},
		{Op: codec.Op{ID: "op-3", Parents: []string{"op-2"}}, TStar: 3},
	}

	oracle, ok := newLazyReachOracle(orderedOps).(*lazyReachOracle)
	if !ok {
		t.Fatalf("newLazyReachOracle returned %T, want *lazyReachOracle", newLazyReachOracle(orderedOps))
	}
	if oracle.built != nil {
		t.Fatalf("newLazyReachOracle built the bitset oracle eagerly, before any IsAncestor call")
	}

	if !oracle.IsAncestor("op-1", "op-3") {
		t.Fatalf("IsAncestor(op-1, op-3) = false, want true")
	}
	if oracle.built == nil {
		t.Fatalf("IsAncestor did not build the bitset oracle on first use")
	}

	// A second call must reuse the built oracle rather than rebuilding it.
	built := oracle.built
	if !oracle.IsAncestor("op-2", "op-3") {
		t.Fatalf("IsAncestor(op-2, op-3) = false, want true")
	}
	if oracle.built != built {
		t.Fatalf("second IsAncestor call rebuilt the bitset oracle instead of reusing it")
	}

	if oracle.IsAncestor("op-3", "op-1") {
		t.Fatalf("IsAncestor(op-3, op-1) = true, want false (wrong direction)")
	}
}

// TestLazyReachOracleEmptyOps pins the n == 0 edge case BuildReachability
// itself special-cases: building lazily over no ops must still answer
// IsAncestor false rather than panic.
func TestLazyReachOracleEmptyOps(t *testing.T) {
	oracle := newLazyReachOracle(nil)
	if oracle.IsAncestor("a", "b") {
		t.Fatalf("IsAncestor over no ops = true, want false")
	}
}
