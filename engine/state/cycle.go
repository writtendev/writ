package state

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
)

// FoldCycle executes deterministic fold reduction on an input set of operations
// for a cycle collaborative object, returning the materialized Cycle state.
func FoldCycle(ops []codec.Op) (Cycle, error) {
	if len(ops) == 0 {
		return Cycle{}, nil
	}

	orderedOps, err := fold.OrderWithTStar(ops)
	if err != nil {
		return Cycle{}, err
	}

	reach := fold.BuildReachability(orderedOps)

	var state Cycle
	var unknownOps []UnknownOp

	type orSetRecord struct {
		opID string
		item string
	}

	var issueAdds []orSetRecord
	var issueRemoves []orSetRecord

	rules := internalRules(CycleRules())

	for _, o := range orderedOps {
		op := o.Op
		if op.ObjectType != "cycle" || op.OpVersion != 1 {
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     op.ID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
			})
			continue
		}

		var body map[string]any
		if len(op.Body) > 0 {
			if err := json.Unmarshal(op.Body, &body); err != nil {
				return Cycle{}, fmt.Errorf("fold cycle: unmarshaling op %s body: %w", op.ID, err)
			}
		}
		if body == nil {
			body = make(map[string]any)
		}

		// A field with a declared rule carrying a value its strategy cannot
		// consume makes the whole op uninterpretable (spec/fold.md §7.1). It is
		// quarantined here on exactly the terms the generic driver applies, so
		// the typed reducer and fold.Fold reject the same operations.
		if fold.Uninterpretable(op, body, rules) {
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     op.ID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
			})
			continue
		}

		switch op.OpType {
		case "create":
			if t, ok := body["title"].(string); ok {
				state.Title = t
			}
			if d, ok := body["description"].(string); ok {
				state.Description = d
			}
			if s, ok := body["starts_at"].(string); ok {
				state.StartsAt = s
			}
			if e, ok := body["ends_at"].(string); ok {
				state.EndsAt = e
			}

		case "update":
			if t, ok := body["title"].(string); ok {
				state.Title = t
			}
			if d, ok := body["description"].(string); ok {
				state.Description = d
			}

		case "set-dates":
			if s, ok := body["starts_at"].(string); ok {
				state.StartsAt = s
			}
			if e, ok := body["ends_at"].(string); ok {
				state.EndsAt = e
			}

		case "add-issue", "remove-issue":
			adds, removes := extractScalarOrSetItems(body, "issue", op.OpType)
			for _, iss := range adds {
				if iss != "" {
					issueAdds = append(issueAdds, orSetRecord{opID: op.ID, item: iss})
				}
			}
			for _, iss := range removes {
				if iss != "" {
					issueRemoves = append(issueRemoves, orSetRecord{opID: op.ID, item: iss})
				}
			}

		default:
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     op.ID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
			})
		}
	}

	// Issues OR-set: add-wins over causal removes, emitted sorted
	presentSet := make(map[string]bool)
	for _, add := range issueAdds {
		removed := false
		for _, rem := range issueRemoves {
			if rem.item == add.item && reach.IsAncestor(add.opID, rem.opID) {
				removed = true
				break
			}
		}
		if !removed {
			presentSet[add.item] = true
		}
	}
	var issues []string
	for k := range presentSet {
		issues = append(issues, k)
	}
	sort.Strings(issues)
	state.Issues = issues

	state.UnknownOps = unknownOps

	return state, nil
}

// CycleRules returns the built-in field merge rules for the cycle vocabulary (v1).
func CycleRules() []Rule {
	return []Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "starts_at", Strategy: "lww", ValueType: "timestamp"},
		{OpType: "create", OpVersion: 1, Field: "ends_at", Strategy: "lww", ValueType: "timestamp"},
		{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "set-dates", OpVersion: 1, Field: "starts_at", Strategy: "lww", ValueType: "timestamp"},
		{OpType: "set-dates", OpVersion: 1, Field: "ends_at", Strategy: "lww", ValueType: "timestamp"},
		{OpType: "add-issue", OpVersion: 1, Field: "issue", Strategy: "set-observed-remove", ValueType: "object-ref"},
		{OpType: "remove-issue", OpVersion: 1, Field: "issue", Strategy: "set-observed-remove", ValueType: "object-ref"},
	}
}
