package state

import (
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/fold"
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
	// on the rule matches anything. An empty ObjectType on the op side does
	// not also match anything (WRIT-275): the op-envelope schema requires a
	// non-empty object_type on every op that reaches the log, so that half
	// of the wildcard was unreachable through the log. Before WRIT-195 it
	// was left empty on every hand-written Go rule table rather than set to
	// each table's own type — those tables were already selected per object
	// type by their callers and the typed reducers, so an empty ObjectType
	// changed nothing for them — and WRIT-195 deleted the tables outright
	// rather than retrofit them. Only log-sourced rules (RulesFromSchemas)
	// and rules built from spec.FieldRules() carry it.
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

// DetermineObjectType re-exports fold.DetermineObjectType so that
// engine/projection — whose import allowlist admits engine/state but not
// the internal engine/internal/fold package — can share the one
// implementation instead of keeping its own copy.
func DetermineObjectType(ops []codec.Op) string { return fold.DetermineObjectType(ops) }

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

	// verificationByCommit is a pure data lookup, not a fold decision:
	// fold.Fold (above) never sees Verification at all, and this only
	// copies each quarantined op's own outcome onto its UnknownOp entry
	// (AGENTS.md "Fold is pure and deterministic" — no branch on the value,
	// just a pass-through keyed by the commit id fold.Fold already reports).
	verificationByCommit := make(map[string]string, len(ops))
	for _, op := range ops {
		verificationByCommit[op.ID] = string(op.Verification.Outcome)
	}

	unknownOps := make([]UnknownOp, len(res.UnknownOps))
	for i, u := range res.UnknownOps {
		unknownOps[i] = UnknownOp{
			Commit:       u.Commit,
			ObjectType:   u.ObjectType,
			OpType:       u.OpType,
			OpVersion:    u.OpVersion,
			Verification: verificationByCommit[u.Commit],
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
