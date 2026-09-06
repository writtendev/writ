package state

import (
	"strings"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
)

// NormalizeRule specifies normalization attributes for an (op_type, field) merge rule.
type NormalizeRule struct {
	Value string   `json:"value,omitempty"`
	Items string   `json:"items,omitempty"`
	Key   []string `json:"key,omitempty"`
}

// Rule specifies the merge strategy and parameters for an (op_type, op_version, field) tuple.
type Rule struct {
	OpType    string         `json:"op_type,omitempty"`
	OpVersion int64          `json:"op_version,omitempty"`
	Field     string         `json:"field"`
	Target    string         `json:"target,omitempty"`
	Strategy  string         `json:"strategy"`
	Key       []string       `json:"key,omitempty"`
	Lattice   []string       `json:"lattice,omitempty"`
	Normalize *NormalizeRule `json:"normalize,omitempty"`
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
		var norm *fold.NormalizeRule
		if r.Normalize != nil {
			norm = &fold.NormalizeRule{
				Value: r.Normalize.Value,
				Items: r.Normalize.Items,
				Key:   r.Normalize.Key,
			}
		}
		out[i] = fold.Rule{
			OpType:    r.OpType,
			OpVersion: r.OpVersion,
			Field:     r.Field,
			Target:    r.Target,
			Strategy:  r.Strategy,
			Key:       r.Key,
			Lattice:   r.Lattice,
			Normalize: norm,
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

// stringItems returns the items a set-valued field carries. Both set strategies
// consume the same two shapes: a `set-union` field holds a string or an array of
// strings (spec/fold.md §5.3), and so does each side of an OR-set (§5.4).
// Anything else made the operation uninterpretable (§7.1) before it reached a
// reducer, so a non-string here is unreachable and skipped rather than rendered.
//
// Every typed reducer shares this with the generic fold's own item helpers so a
// body shape one consumes cannot be silently dropped by the other. A set-union
// field read with a bare `.(string)` assertion until WRIT-124's round-2 review:
// an array-shaped value such as `{"remote":["origin","upstream"]}` is accepted
// by the uninterpretability check and folded by both the generic driver and the
// reference fold, so nothing quarantined it and the typed reducer alone
// returned no items at all — "skip invents an absence" relocated from the
// predicate into a reducer, reaching the public API and the SQLite projection.
func stringItems(raw any) []string {
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []any:
		items := make([]string, 0, len(v))
		for _, it := range v {
			if s, ok := it.(string); ok {
				items = append(items, s)
			}
		}
		return items
	case []string:
		return v
	}
	return nil
}

// extractOrSetItems extracts additions and removals from a flat OR-set body,
// accommodating both flat fields (body[addField] / body[remField]) and nested
// maps ({add: [...], remove: [...]}) present at either field (spec/fold.md §5.4).
func extractOrSetItems(body map[string]any, addField, remField string) (adds []string, removes []string) {
	if m, ok := body[addField].(map[string]any); ok {
		adds = append(adds, stringItems(m["add"])...)
		removes = append(removes, stringItems(m["remove"])...)
	} else {
		adds = append(adds, stringItems(body[addField])...)
	}
	if m, ok := body[remField].(map[string]any); ok {
		adds = append(adds, stringItems(m["add"])...)
		removes = append(removes, stringItems(m["remove"])...)
	} else {
		removes = append(removes, stringItems(body[remField])...)
	}
	return adds, removes
}

// extractScalarOrSetItems extracts additions and removals from a scalar OR-set
// body (such as project/cycle add-issue / remove-issue), supporting scalar
// items (or arrays) mapped to side by opType, or nested maps present at the field.
func extractScalarOrSetItems(body map[string]any, field string, opType string) (adds []string, removes []string) {
	if m, ok := body[field].(map[string]any); ok {
		adds = append(adds, stringItems(m["add"])...)
		removes = append(removes, stringItems(m["remove"])...)
		return adds, removes
	}
	items := stringItems(body[field])
	if strings.HasPrefix(opType, "add-") || opType == "add" {
		adds = append(adds, items...)
	} else if strings.HasPrefix(opType, "remove-") || opType == "remove" {
		removes = append(removes, items...)
	}
	return adds, removes
}
