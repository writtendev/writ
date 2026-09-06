package state

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
)

// FoldIssue executes deterministic fold reduction on an input set of operations
// for an issue collaborative object, returning the materialized Issue state.
func FoldIssue(ops []codec.Op) (Issue, error) {
	if len(ops) == 0 {
		return Issue{}, nil
	}

	orderedOps, err := fold.OrderWithTStar(ops)
	if err != nil {
		return Issue{}, err
	}

	reach := fold.BuildReachability(orderedOps)

	var state Issue
	var unknownOps []UnknownOp

	type orSetRecord struct {
		opID string
		item string
	}

	var assignAdds []orSetRecord
	var assignRemoves []orSetRecord
	var labelAdds []orSetRecord
	var labelRemoves []orSetRecord

	linksMap := make(map[string]*Link)

	rules := internalRules(IssueRules())

	for _, o := range orderedOps {
		op := o.Op
		if op.ObjectType != "issue" || op.OpVersion != 1 {
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
				return Issue{}, fmt.Errorf("fold issue: unmarshaling op %s body: %w", op.ID, err)
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
			if p, ok := body["priority"]; ok && p != nil {
				switch val := p.(type) {
				case float64:
					state.Priority = int(val)
				case int:
					state.Priority = val
				case int64:
					state.Priority = int(val)
				case json.Number:
					if i, err := val.Int64(); err == nil {
						state.Priority = int(i)
					}
				}
			}
			if e, ok := body["estimate"]; ok && e != nil {
				switch val := e.(type) {
				case float64:
					est := val
					state.Estimate = &est
				case int:
					est := float64(val)
					state.Estimate = &est
				case int64:
					est := float64(val)
					state.Estimate = &est
				case json.Number:
					if f, err := val.Float64(); err == nil {
						state.Estimate = &f
					}
				}
			}
			if pos, ok := body["position"].(string); ok {
				state.Position = pos
			}

		case "set-state":
			if s, ok := body["state"].(string); ok {
				state.State = s
			}
			if r, ok := body["reason"].(string); ok {
				state.Reason = r
			}
			if pos, ok := body["position"].(string); ok {
				state.Position = pos
			}

		case "assign":
			adds, removes := extractOrSetItems(body, "add", "remove")
			for _, it := range adds {
				if item := NormalizePerson(it); item != "" {
					assignAdds = append(assignAdds, orSetRecord{opID: op.ID, item: item})
				}
			}
			for _, it := range removes {
				if item := NormalizePerson(it); item != "" {
					assignRemoves = append(assignRemoves, orSetRecord{opID: op.ID, item: item})
				}
			}

		case "label":
			adds, removes := extractOrSetItems(body, "add", "remove")
			for _, it := range adds {
				if it != "" {
					labelAdds = append(labelAdds, orSetRecord{opID: op.ID, item: it})
				}
			}
			for _, it := range removes {
				if it != "" {
					labelRemoves = append(labelRemoves, orSetRecord{opID: op.ID, item: it})
				}
			}

		case "link":
			target, _ := body["target"].(string)
			if target != "" {
				entry, ok := linksMap[target]
				if !ok {
					entry = &Link{Target: target}
					linksMap[target] = entry
				}
				if tt, ok := body["target_type"].(string); ok {
					entry.TargetType = tt
				}
				if rel, ok := body["relation"].(string); ok {
					entry.Relation = rel
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

	// Assignees OR-set: add-wins over causal removes, emitted sorted
	assignPresent := make(map[string]bool)
	for _, add := range assignAdds {
		removed := false
		for _, rem := range assignRemoves {
			if rem.item == add.item && reach.IsAncestor(add.opID, rem.opID) {
				removed = true
				break
			}
		}
		if !removed {
			assignPresent[add.item] = true
		}
	}
	var assignees []string
	for k := range assignPresent {
		assignees = append(assignees, k)
	}
	sort.Strings(assignees)
	state.Assignees = assignees

	// Labels OR-set: add-wins over causal removes, emitted sorted
	labelPresent := make(map[string]bool)
	for _, add := range labelAdds {
		removed := false
		for _, rem := range labelRemoves {
			if rem.item == add.item && reach.IsAncestor(add.opID, rem.opID) {
				removed = true
				break
			}
		}
		if !removed {
			labelPresent[add.item] = true
		}
	}
	var labels []string
	for k := range labelPresent {
		labels = append(labels, k)
	}
	sort.Strings(labels)
	state.Labels = labels

	// Links keyed-LWW: omit entries with relation "none" or empty, sort by target
	var links []Link
	for _, l := range linksMap {
		if l.Relation != "none" && l.Relation != "" {
			links = append(links, *l)
		}
	}
	sort.Slice(links, func(i, j int) bool {
		return links[i].Target < links[j].Target
	})
	state.Links = links

	state.UnknownOps = unknownOps

	return state, nil
}

// IssueRules returns the built-in field merge rules for the issue-ops vocabulary (v1).
func IssueRules() []Rule {
	linkKeyTypes := map[string]string{"target": "object-ref"}
	return []Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "priority", Strategy: "lww", ValueType: "int"},
		{OpType: "create", OpVersion: 1, Field: "estimate", Strategy: "lww", ValueType: "number"},
		{OpType: "create", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position"},
		{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "priority", Strategy: "lww", ValueType: "int"},
		{OpType: "update", OpVersion: 1, Field: "estimate", Strategy: "lww", ValueType: "number"},
		{OpType: "update", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position"},
		{OpType: "set-state", OpVersion: 1, Field: "state", Strategy: "lww", ValueType: "object-ref"},
		{OpType: "set-state", OpVersion: 1, Field: "reason", Strategy: "lww", ValueType: "string"},
		{OpType: "set-state", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position"},
		{OpType: "assign", OpVersion: 1, Field: "add", Strategy: "set-observed-remove", ValueType: "person-ref"},
		{OpType: "assign", OpVersion: 1, Field: "remove", Strategy: "set-observed-remove", ValueType: "person-ref"},
		{OpType: "label", OpVersion: 1, Field: "add", Strategy: "set-observed-remove", ValueType: "object-ref"},
		{OpType: "label", OpVersion: 1, Field: "remove", Strategy: "set-observed-remove", ValueType: "string"},
		{OpType: "link", OpVersion: 1, Field: "target", Strategy: "keyed-lww", Key: []string{"target"}, ValueType: "object-ref", KeyTypes: linkKeyTypes},
		{OpType: "link", OpVersion: 1, Field: "target_type", Strategy: "keyed-lww", Key: []string{"target"}, ValueType: "string", KeyTypes: linkKeyTypes},
		{OpType: "link", OpVersion: 1, Field: "relation", Strategy: "keyed-lww", Key: []string{"target"}, ValueType: "enum", Enum: []string{"fixes", "relates", "none"}, KeyTypes: linkKeyTypes},
	}
}
