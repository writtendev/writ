package state

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
)

// FoldProject executes deterministic fold reduction on an input set of operations
// for a project collaborative object, returning the materialized Project state.
func FoldProject(ops []codec.Op) (Project, error) {
	if len(ops) == 0 {
		return Project{}, nil
	}

	orderedOps, err := fold.OrderWithTStar(ops)
	if err != nil {
		return Project{}, err
	}

	reach := fold.BuildReachability(orderedOps)

	var state Project
	var unknownOps []UnknownOp

	type orSetRecord struct {
		opID string
		item string
	}

	var issueAdds []orSetRecord
	var issueRemoves []orSetRecord

	rules := internalRules(ProjectRules())

	for _, o := range orderedOps {
		op := o.Op
		if op.ObjectType != "project" || op.OpVersion != 1 {
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
				return Project{}, fmt.Errorf("fold project: unmarshaling op %s body: %w", op.ID, err)
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
		case "create", "update":
			if t, ok := body["title"].(string); ok {
				state.Title = t
			}
			if d, ok := body["description"].(string); ok {
				state.Description = d
			}

		case "set-status":
			if s, ok := body["status"].(string); ok {
				state.Status = s
			}
			if r, ok := body["reason"].(string); ok {
				state.Reason = r
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

// ProjectRules returns the built-in field merge rules for the project vocabulary (v1).
func ProjectRules() []Rule {
	return []Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "set-status", OpVersion: 1, Field: "status", Strategy: "lww", ValueType: "enum", Enum: []string{"planned", "active", "paused", "completed", "canceled"}},
		{OpType: "set-status", OpVersion: 1, Field: "reason", Strategy: "lww", ValueType: "string"},
		{OpType: "add-issue", OpVersion: 1, Field: "issue", Strategy: "set-observed-remove", ValueType: "object-ref"},
		{OpType: "remove-issue", OpVersion: 1, Field: "issue", Strategy: "set-observed-remove", ValueType: "object-ref"},
	}
}
