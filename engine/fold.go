package writ

import (
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/state"
)

// Rule specifies the merge strategy and value type for an (op_type, op_version, field) tuple.
type Rule = state.Rule

// Op is a single append-only operation, as read from or written to the log.
type Op = codec.Op

// Envelope is a signed, encoded Op together with its verification inputs.
type Envelope = codec.Envelope

// Vocabularies is the per-namespace op vocabulary derived from the schema
// objects present in the log.
type Vocabularies = codec.Vocabularies

// Sentinels re-exported from internal/fold.
var (
	// ErrCycle is returned when the operation graph contains a directed cycle.
	ErrCycle = state.ErrCycle

	// ErrDuplicateOpID is returned when the input set contains duplicate operation IDs.
	ErrDuplicateOpID = state.ErrDuplicateOpID

	// ErrMixedObjects is returned when the input set spans multiple object IDs.
	ErrMixedObjects = state.ErrMixedObjects
)

// Fold executes deterministic fold reduction on an input set of operations
// against declared field merge rules, returning the resulting ObjectState.
func Fold(ops []codec.Op, rules []Rule) (ObjectState, error) {
	return state.Fold(ops, rules)
}

// FoldSchema executes deterministic fold reduction on an input set of operations
// for a schema collaborative object, returning the materialized Schema state.
func FoldSchema(ops []codec.Op) (Schema, error) {
	return state.FoldSchema(ops)
}

// SchemaRules returns the built-in field merge rules for the schema vocabulary
// (v1) — the one rule table that never comes from the log
// (spec/schema-ops.md §Bootstrap).
func SchemaRules() []Rule {
	return state.SchemaRules()
}
