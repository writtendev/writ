package dag

import (
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/fold"
)

var (
	// ErrCycle is returned when the operation graph contains a directed cycle.
	ErrCycle = fold.ErrCycle

	// ErrDuplicateOpID is returned when the input set contains duplicate operation IDs.
	ErrDuplicateOpID = fold.ErrDuplicateOpID

	// ErrMixedObjects is returned when the input set spans multiple object IDs.
	ErrMixedObjects = fold.ErrMixedObjects
)

// Order computes the deterministic total order L of an object's operations set S
// using Kahn's algorithm with a (t*, id) min-heap priority queue per spec/fold.md §4.
//
// Parent edges are restricted to IDs present in the input set S; missing parent commits
// or foreign object commits are omitted from ancestry without error.
//
// Order returns ErrMixedObjects if ops contains operations for more than one ObjectID,
// ErrDuplicateOpID if any op ID is repeated, or ErrCycle if a causal cycle is detected.
// If ops is empty, Order returns (nil, nil). The input slice is never mutated.
func Order(ops []codec.Op) ([]codec.Op, error) {
	return fold.Order(ops)
}
