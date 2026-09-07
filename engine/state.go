package writ

import (
	"github.com/writtendev/writ/engine/resolve"
	"github.com/writtendev/writ/engine/state"
)

// Anchor is a content-based comment position object (v1).
// Fold carries anchors verbatim as data per spec/fold.md §6.
type Anchor = resolve.Anchor

// OpRef identifies an operation in an object's total order sequence L
// along with its causality-monotone effective timestamp t*.
type OpRef = state.OpRef

// UnknownOp records an operation that was preserved in the DAG and participated
// in ordering and ancestry, but whose (op_type, op_version) had no declared rules
// per spec/fold.md §7.
type UnknownOp = state.UnknownOp

// ObjectState is the folded state produced by the fold driver for a collaborative object.
type ObjectState = state.ObjectState

// ParseReference parses a reference string into its repository designator and target object ID.
func ParseReference(ref string) (string, string, error) {
	return state.ParseReference(ref)
}

// NormalizePerson normalizes a person identifier string per spec/identifiers.md
// (scheme lowercased; value trimmed and case-folded).
func NormalizePerson(s string) string {
	return state.NormalizePerson(s)
}
