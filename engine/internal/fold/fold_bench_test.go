package fold_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
)

// BenchmarkFoldLWWChain measures Fold's cost over a single-object chain of
// n purely-lww ops (WRIT-268). Before this ticket, Fold unconditionally
// built the n×⌈n/64⌉ ancestry bitset (BuildReachability) on every fold
// even though lww never calls IsAncestor. After, the lazy oracle
// (reach.go) is never built at all for an lww-only rule table, so B/op
// should lose the n² term entirely and ns/op should grow roughly linearly
// (~2x per doubling) instead of ~4x.
func BenchmarkFoldLWWChain(b *testing.B) {
	rules := []fold.Rule{
		{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww"},
	}
	for _, n := range []int{1000, 5000, 20000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			ops := lwwChain(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := fold.Fold(ops, rules); err != nil {
					b.Fatalf("Fold: %v", err)
				}
			}
		})
	}
}

// BenchmarkFoldORSetNoRemoves measures Fold's cost over a single-object
// OR-set of n adds, each a distinct item, with no removes at all
// (WRIT-268). Before this ticket, Result() built the ancestry bitset via
// an unconditionally-eager BuildReachability regardless of whether
// anything ever called IsAncestor. After: an item with no removes needs
// zero IsAncestor calls (edit 3's removesByItem short-circuit), so with
// no removes present anywhere the lazy oracle (edit 1) is never built
// either — B/op should lose the n² term entirely, same as the lww case.
func BenchmarkFoldORSetNoRemoves(b *testing.B) {
	rules := []fold.Rule{
		{OpType: "add-member", OpVersion: 1, Field: "members", Strategy: "set-observed-remove"},
	}
	for _, n := range []int{1000, 5000, 20000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			ops := orSetDistinctAdds(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := fold.Fold(ops, rules); err != nil {
					b.Fatalf("Fold: %v", err)
				}
			}
		})
	}
}

// BenchmarkFoldORSetWithRemoves measures Fold's cost over a single-object
// OR-set of n distinct items, each added and then removed by a causal
// descendant op (2n ops total) — the shape that still fires IsAncestor
// and so still builds the ancestry bitset both before and after this
// ticket. Before: Result()'s inner loop scanned every one of the n
// removes for every one of the n adds (O(n²) comparisons, the
// rem.item == add.item guard notwithstanding, since the guard runs inside
// the scan rather than avoiding it). After: removesByItem (edit 3) turns
// that into one map lookup per add, so time should improve sharply while
// B/op keeps roughly the same bitset-dominated shape — "B/op still
// carrying the bitset once the first IsAncestor fires" is the plan's own
// expectation for this case, unlike the no-removes case above.
func BenchmarkFoldORSetWithRemoves(b *testing.B) {
	rules := []fold.Rule{
		{OpType: "add-member", OpVersion: 1, Field: "members", Strategy: "set-observed-remove"},
		{OpType: "remove-member", OpVersion: 1, Field: "members", Strategy: "set-observed-remove"},
	}
	for _, n := range []int{1000, 5000, 20000} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			ops := orSetAddsWithMatchingRemoves(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := fold.Fold(ops, rules); err != nil {
					b.Fatalf("Fold: %v", err)
				}
			}
		})
	}
}

func lwwChain(n int) []codec.Op {
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ops := make([]codec.Op, n)
	for i := 0; i < n; i++ {
		var parents []string
		if i > 0 {
			parents = []string{fmt.Sprintf("op-%d", i-1)}
		}
		ops[i] = codec.Op{
			ID: fmt.Sprintf("op-%d", i),
			Envelope: codec.Envelope{
				ObjectID:   "obj-1",
				ObjectType: "acme.widget",
				OpType:     "update",
				OpVersion:  1,
				Body:       []byte(fmt.Sprintf(`{"title":"title %d"}`, i)),
			},
			Parents: parents,
			Author:  codec.Identity{When: baseTime.Add(time.Duration(i) * time.Second)},
		}
	}
	return ops
}

func orSetDistinctAdds(n int) []codec.Op {
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ops := make([]codec.Op, n)
	for i := 0; i < n; i++ {
		ops[i] = codec.Op{
			ID: fmt.Sprintf("add-%d", i),
			Envelope: codec.Envelope{
				ObjectID:   "obj-1",
				ObjectType: "acme.roster",
				OpType:     "add-member",
				OpVersion:  1,
				Body:       []byte(fmt.Sprintf(`{"members":"item-%d"}`, i)),
			},
			Author: codec.Identity{When: baseTime.Add(time.Duration(i) * time.Second)},
		}
	}
	return ops
}

func orSetAddsWithMatchingRemoves(n int) []codec.Op {
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ops := make([]codec.Op, 0, n*2)
	for i := 0; i < n; i++ {
		addID := fmt.Sprintf("add-%d", i)
		ops = append(ops, codec.Op{
			ID: addID,
			Envelope: codec.Envelope{
				ObjectID:   "obj-1",
				ObjectType: "acme.roster",
				OpType:     "add-member",
				OpVersion:  1,
				Body:       []byte(fmt.Sprintf(`{"members":"item-%d"}`, i)),
			},
			Author: codec.Identity{When: baseTime.Add(time.Duration(2*i) * time.Second)},
		})
		ops = append(ops, codec.Op{
			ID: fmt.Sprintf("remove-%d", i),
			Envelope: codec.Envelope{
				ObjectID:   "obj-1",
				ObjectType: "acme.roster",
				OpType:     "remove-member",
				OpVersion:  1,
				Body:       []byte(fmt.Sprintf(`{"members":"item-%d"}`, i)),
			},
			Parents: []string{addID},
			Author:  codec.Identity{When: baseTime.Add(time.Duration(2*i+1) * time.Second)},
		})
	}
	return ops
}
