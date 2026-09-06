package state

import (
	"encoding/json"
	"fmt"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
)

// FoldLabel executes deterministic fold reduction on an input set of operations
// for a label collaborative object, returning the materialized Label state.
func FoldLabel(ops []codec.Op) (Label, error) {
	if len(ops) == 0 {
		return Label{}, nil
	}

	orderedOps, err := fold.OrderWithTStar(ops)
	if err != nil {
		return Label{}, err
	}

	var state Label
	var unknownOps []UnknownOp

	rules := internalRules(LabelRules())

	for _, o := range orderedOps {
		op := o.Op
		if op.ObjectType != "label" || op.OpVersion != 1 {
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
				return Label{}, fmt.Errorf("fold label: unmarshaling op %s body: %w", op.ID, err)
			}
		}
		if body == nil {
			body = make(map[string]any)
		}

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
			if n, ok := body["name"].(string); ok {
				state.Name = n
			}
			if c, ok := body["color"].(string); ok {
				state.Color = c
			}
			if d, ok := body["description"].(string); ok {
				state.Description = d
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

	state.UnknownOps = unknownOps
	return state, nil
}

// LabelRules returns the built-in field merge rules for the label vocabulary (v1).
func LabelRules() []Rule {
	return []Rule{
		{OpType: "create", OpVersion: 1, Field: "name", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "color", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "name", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "color", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
	}
}
