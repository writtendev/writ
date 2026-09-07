package state

import (
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
)

// Rule specifies the merge strategy and value type for an (op_type, op_version, field) tuple.
type Rule struct {
	OpType    string            `json:"op_type,omitempty"`
	OpVersion int64             `json:"op_version,omitempty"`
	Field     string            `json:"field"`
	Target    string            `json:"target,omitempty"`
	Strategy  string            `json:"strategy"`
	Key       []string          `json:"key,omitempty"`
	Lattice   []string          `json:"lattice,omitempty"`
	ValueType string            `json:"value_type,omitempty"`
	Enum      []string          `json:"enum,omitempty"`
	MaxLength int64             `json:"max_length,omitempty"`
	KeyTypes  map[string]string `json:"key_types,omitempty"`
	// ObjectType scopes the rule to one object type (spec/fold.md §5): empty
	// on either the rule or the op matches anything. It is left empty on
	// every hand-written Go rule table (ReviewRules, IssueRules, etc.)
	// rather than set to the table's own type: those tables are already
	// selected per object type by their callers and the typed reducers, so
	// an empty ObjectType changes nothing for them, and WRIT-194 deletes the
	// tables outright — setting it on every literal there would be
	// throwaway work. Only log-sourced rules (RulesFromSchemas) and rules
	// built from spec.FieldRules() carry it.
	ObjectType string `json:"object_type,omitempty"`
	// Deprecated is carried through from a schema-declared field's
	// deprecate-field state (spec/schema-ops.md §4.6, §5): it is metadata
	// for a producer or UI to read, discouraging new writes. It never
	// affects folding — a deprecated rule is installed and matched exactly
	// like any other, because already-signed ops written under it must keep
	// folding (spec/schema-ops.md §7 step 3; AGENTS.md "old clients must not
	// destroy new clients' data").
	Deprecated bool `json:"deprecated,omitempty"`
}

// TargetKey returns Target if non-empty, otherwise Field.
func (r Rule) TargetKey() string {
	if r.Target != "" {
		return r.Target
	}
	return r.Field
}

// Sentinels re-exported from internal/fold.
var (
	// ErrCycle is returned when the operation graph contains a directed cycle.
	ErrCycle = fold.ErrCycle

	// ErrDuplicateOpID is returned when the input set contains duplicate operation IDs.
	ErrDuplicateOpID = fold.ErrDuplicateOpID

	// ErrMixedObjects is returned when the input set spans multiple object IDs.
	ErrMixedObjects = fold.ErrMixedObjects
)

// internalRules converts the public rule table into the internal one. The
// typed reducers below share it so that they and the generic driver decide
// which operations are uninterpretable from the same rules.
func internalRules(rules []Rule) []fold.Rule {
	out := make([]fold.Rule, len(rules))
	for i, r := range rules {
		out[i] = fold.Rule{
			OpType:     r.OpType,
			OpVersion:  r.OpVersion,
			Field:      r.Field,
			Target:     r.Target,
			Strategy:   r.Strategy,
			Key:        r.Key,
			Lattice:    r.Lattice,
			ValueType:  r.ValueType,
			Enum:       r.Enum,
			MaxLength:  r.MaxLength,
			KeyTypes:   r.KeyTypes,
			ObjectType: r.ObjectType,
		}
	}
	return out
}

// Fold executes deterministic fold reduction on an input set of operations
// against declared field merge rules, returning the resulting ObjectState.
func Fold(ops []codec.Op, rules []Rule) (ObjectState, error) {
	res, err := fold.Fold(ops, internalRules(rules))
	if err != nil {
		return ObjectState{}, err
	}

	totalOrder := make([]OpRef, len(res.TotalOrder))
	for i, ref := range res.TotalOrder {
		totalOrder[i] = OpRef{
			Commit: ref.Commit,
			TStar:  ref.TStar,
		}
	}

	unknownOps := make([]UnknownOp, len(res.UnknownOps))
	for i, u := range res.UnknownOps {
		unknownOps[i] = UnknownOp{
			Commit:     u.Commit,
			ObjectType: u.ObjectType,
			OpType:     u.OpType,
			OpVersion:  u.OpVersion,
		}
	}

	return ObjectState{
		ObjectID:   res.ObjectID,
		ObjectType: res.ObjectType,
		TotalOrder: totalOrder,
		State:      res.State,
		UnknownOps: unknownOps,
	}, nil
}

