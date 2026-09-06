package state

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
)

// DocumentRules returns the declared merge rules for collaborative objects of type "document".
func DocumentRules() []Rule {
	linkKeyTypes := map[string]string{"target": "object-ref"}
	return []Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "link", OpVersion: 1, Field: "target", Strategy: "keyed-lww", Key: []string{"target"}, ValueType: "object-ref", KeyTypes: linkKeyTypes},
		{OpType: "link", OpVersion: 1, Field: "target_type", Strategy: "keyed-lww", Key: []string{"target"}, ValueType: "string", KeyTypes: linkKeyTypes},
		{OpType: "link", OpVersion: 1, Field: "relation", Strategy: "keyed-lww", Key: []string{"target"}, ValueType: "string", KeyTypes: linkKeyTypes},
		{OpType: "label", OpVersion: 1, Field: "add", Strategy: "set-observed-remove", ValueType: "string"},
		{OpType: "label", OpVersion: 1, Field: "remove", Strategy: "set-observed-remove", ValueType: "string"},
	}
}

// SectionRules returns the declared merge rules for collaborative objects of type "section".
func SectionRules() []Rule {
	return []Rule{
		{OpType: "create", OpVersion: 1, Field: "document_id", Strategy: "create-once", ValueType: "object-ref"},
		{OpType: "create", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position"},
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "body", Strategy: "multi-value", ValueType: "text"},
		{OpType: "edit", OpVersion: 1, Field: "body", Strategy: "multi-value", ValueType: "text"},
		{OpType: "move", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position"},
		{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "position", Strategy: "lww", ValueType: "position"},
		{OpType: "delete", OpVersion: 1, Field: "deleted", Strategy: "tombstone", ValueType: "bool"},
	}
}

// FoldDocument executes deterministic fold reduction on an input set of operations
// for a document collaborative object, returning the materialized Document state.
func FoldDocument(ops []codec.Op) (Document, error) {
	if len(ops) == 0 {
		return Document{}, nil
	}

	orderedOps, err := fold.OrderWithTStar(ops)
	if err != nil {
		return Document{}, err
	}

	reach := fold.BuildReachability(orderedOps)

	var state Document
	var unknownOps []UnknownOp

	type orSetRecord struct {
		opID string
		item string
	}

	var labelAdds []orSetRecord
	var labelRemoves []orSetRecord

	linksMap := make(map[string]*Link)

	rules := internalRules(DocumentRules())

	for _, o := range orderedOps {
		op := o.Op
		if op.ObjectType != "document" || op.OpVersion != 1 {
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
				return Document{}, fmt.Errorf("fold document: unmarshaling op %s body: %w", op.ID, err)
			}
		}
		if body == nil {
			body = make(map[string]any)
		}

		// A field with a declared rule carrying a value its strategy cannot
		// consume makes the whole op uninterpretable (spec/fold.md §7.1).
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

		default:
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     op.ID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
			})
		}
	}

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

// FoldSection executes deterministic fold reduction on an input set of operations
// for a section collaborative object, returning the materialized Section state.
func FoldSection(ops []codec.Op) (Section, error) {
	if len(ops) == 0 {
		return Section{}, nil
	}

	orderedOps, err := fold.OrderWithTStar(ops)
	if err != nil {
		return Section{}, err
	}

	reach := fold.BuildReachability(orderedOps)

	var state Section
	var unknownOps []UnknownOp

	type mvWrite struct {
		opID string
		val  string
	}
	var writes []mvWrite

	rules := internalRules(SectionRules())

	for _, o := range orderedOps {
		op := o.Op
		if op.ObjectType != "section" || op.OpVersion != 1 {
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
				return Section{}, fmt.Errorf("fold section: unmarshaling op %s body: %w", op.ID, err)
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
		case "create":
			if docID, ok := body["document_id"].(string); ok && state.DocumentID == "" {
				state.DocumentID = docID
			}
			if p, ok := body["position"].(string); ok {
				state.Position = p
			}
			if t, ok := body["title"].(string); ok {
				state.Title = t
			}
			if b, ok := body["body"].(string); ok {
				writes = append(writes, mvWrite{opID: op.ID, val: b})
			}

		case "edit":
			if b, ok := body["body"].(string); ok {
				writes = append(writes, mvWrite{opID: op.ID, val: b})
			}

		case "move":
			if p, ok := body["position"].(string); ok {
				state.Position = p
			}

		case "update":
			if p, ok := body["position"].(string); ok {
				state.Position = p
			}
			if t, ok := body["title"].(string); ok {
				state.Title = t
			}

		case "delete":
			state.Deleted = true

		default:
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     op.ID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
			})
		}
	}

	if len(writes) > 0 {
		var maximal []mvWrite
		for i, w1 := range writes {
			superseded := false
			for j, w2 := range writes {
				if i != j && reach.IsAncestor(w1.opID, w2.opID) {
					superseded = true
					break
				}
			}
			if !superseded {
				maximal = append(maximal, w1)
			}
		}

		seen := make(map[string]bool)
		var vals []string
		for _, w := range maximal {
			if !seen[w.val] {
				seen[w.val] = true
				vals = append(vals, w.val)
			}
		}
		sort.Strings(vals)

		if len(vals) == 1 {
			state.Body = vals[0]
		} else if len(vals) > 1 {
			state.Body = vals
		}
	}

	state.UnknownOps = unknownOps
	return state, nil
}
