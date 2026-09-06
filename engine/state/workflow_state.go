package state

import (
	"encoding/json"
	"fmt"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
)

// WorkflowState represents the materialized state of a workflow-state collaborative object (v1),
// produced by FoldWorkflowState.
type WorkflowState struct {
	Name        string      `json:"name,omitempty"`
	Type        string      `json:"type,omitempty"`
	Position    string      `json:"position,omitempty"`
	Color       string      `json:"color,omitempty"`
	Description string      `json:"description,omitempty"`
	UnknownOps  []UnknownOp `json:"unknown_ops,omitempty"`
}

// FoldWorkflowState executes deterministic fold reduction on an input set of operations
// for a workflow-state collaborative object, returning the materialized WorkflowState state.
func FoldWorkflowState(ops []codec.Op) (WorkflowState, error) {
	if len(ops) == 0 {
		return WorkflowState{}, nil
	}

	orderedOps, err := fold.OrderWithTStar(ops)
	if err != nil {
		return WorkflowState{}, err
	}

	var state WorkflowState
	var unknownOps []UnknownOp

	rules := internalRules(WorkflowStateRules())

	for _, o := range orderedOps {
		op := o.Op
		if op.ObjectType != "workflow-state" || op.OpVersion != 1 {
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
				return WorkflowState{}, fmt.Errorf("fold workflow-state: unmarshaling op %s body: %w", op.ID, err)
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
			if t, ok := body["type"].(string); ok {
				state.Type = t
			}
			if p, ok := body["position"].(string); ok {
				state.Position = p
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

// WorkflowStateRules returns the built-in field merge rules for the workflow-state vocabulary (v1).
func WorkflowStateRules() []Rule {
	return []Rule{
		{OpType: "create", OpVersion: 1, Field: "name", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "type", Strategy: "lww", ValueType: "enum", Enum: []string{"backlog", "unstarted", "started", "completed", "canceled"}},
		{OpType: "create", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position"},
		{OpType: "create", OpVersion: 1, Field: "color", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "name", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "type", Strategy: "lww", ValueType: "enum", Enum: []string{"backlog", "unstarted", "started", "completed", "canceled"}},
		{OpType: "update", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position"},
		{OpType: "update", OpVersion: 1, Field: "color", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
	}
}
