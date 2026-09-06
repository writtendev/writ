package state

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
)

// FoldReview executes deterministic fold reduction on an input set of operations
// for a code review collaborative object, returning the materialized Review state.
func FoldReview(ops []codec.Op) (Review, error) {
	if len(ops) == 0 {
		return Review{}, nil
	}

	orderedOps, err := fold.OrderWithTStar(ops)
	if err != nil {
		return Review{}, err
	}

	reach := fold.BuildReachability(orderedOps)

	var state Review
	var revisions []Revision
	var unknownOps []UnknownOp

	type orSetRecord struct {
		opID string
		item string
	}
	var assignAdds []orSetRecord
	var assignRemoves []orSetRecord
	var labelAdds []orSetRecord
	var labelRemoves []orSetRecord

	type approvalKey struct {
		subject  string
		revision string
	}
	type ciStatusKey struct {
		revision string
		name     string
	}

	approvalsMap := make(map[approvalKey]*Approval)
	ciStatusesMap := make(map[ciStatusKey]*CIStatus)
	linksMap := make(map[string]*Link)

	rules := internalRules(ReviewRules())

	for _, o := range orderedOps {
		op := o.Op
		if op.ObjectType != "review" || op.OpVersion != 1 {
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
				return Review{}, fmt.Errorf("fold review: unmarshaling op %s body: %w", op.ID, err)
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
			if mc, ok := body["merge_commit"].(string); ok {
				state.MergeCommit = mc
			}
			if r, ok := body["reason"].(string); ok {
				state.Reason = r
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

		case "revision":
			base, _ := body["base"].(string)
			head, _ := body["head"].(string)
			revisions = append(revisions, Revision{
				Base: base,
				Head: head,
			})

		case "approval":
			rev, _ := body["revision"].(string)
			subject, _ := body["subject"].(string)
			subject = NormalizePerson(subject)
			if subject == "" && op.Author.Email != "" {
				subject = NormalizePerson("email:" + op.Author.Email)
			}

			key := approvalKey{subject: subject, revision: rev}
			entry, ok := approvalsMap[key]
			if !ok {
				entry = &Approval{
					Subject:  subject,
					Revision: rev,
				}
				approvalsMap[key] = entry
			}

			if v, ok := body["verdict"].(string); ok {
				entry.Verdict = v
			}
			if m, ok := body["message"].(string); ok {
				entry.Message = m
			}

		case "ci-status":
			rev, _ := body["revision"].(string)
			name, _ := body["name"].(string)

			key := ciStatusKey{revision: rev, name: name}
			entry, ok := ciStatusesMap[key]
			if !ok {
				entry = &CIStatus{
					Revision: rev,
					Name:     name,
				}
				ciStatusesMap[key] = entry
			}

			if s, ok := body["state"].(string); ok {
				entry.State = s
			}
			if u, ok := body["url"].(string); ok {
				entry.URL = u
			}
			if d, ok := body["description"].(string); ok {
				entry.Description = d
			}
			if sa, ok := body["started_at"].(string); ok {
				entry.StartedAt = sa
			}
			if ca, ok := body["completed_at"].(string); ok {
				entry.CompletedAt = ca
			}
			if eid, ok := body["external_id"].(string); ok {
				entry.ExternalID = eid
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

	state.Revisions = revisions
	state.UnknownOps = unknownOps

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

	// Approvals: omit entries whose folded verdict is "none" or empty.
	// Sort deterministically by (subject, revision).
	var approvals []Approval
	for _, app := range approvalsMap {
		if app.Verdict != "none" && app.Verdict != "" {
			approvals = append(approvals, *app)
		}
	}
	sort.Slice(approvals, func(i, j int) bool {
		if approvals[i].Subject != approvals[j].Subject {
			return approvals[i].Subject < approvals[j].Subject
		}
		return approvals[i].Revision < approvals[j].Revision
	})
	state.Approvals = approvals

	// CIStatuses: sort deterministically by (revision, name).
	var ciStatuses []CIStatus
	for _, ci := range ciStatusesMap {
		ciStatuses = append(ciStatuses, *ci)
	}
	sort.Slice(ciStatuses, func(i, j int) bool {
		if ciStatuses[i].Revision != ciStatuses[j].Revision {
			return ciStatuses[i].Revision < ciStatuses[j].Revision
		}
		return ciStatuses[i].Name < ciStatuses[j].Name
	})
	state.CIStatuses = ciStatuses

	return state, nil
}

// ReviewRules returns the built-in field merge rules for the review-ops vocabulary (v1).
func ReviewRules() []Rule {
	approvalKeyTypes := map[string]string{"subject": "person-ref", "revision": "git-oid"}
	ciStatusKeyTypes := map[string]string{"revision": "git-oid", "name": "string"}
	linkKeyTypes := map[string]string{"target": "object-ref"}
	return []Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "revision", OpVersion: 1, Field: "base", Strategy: "append", ValueType: "git-oid"},
		{OpType: "revision", OpVersion: 1, Field: "head", Strategy: "append", ValueType: "git-oid"},
		{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		{OpType: "update", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},
		{OpType: "set-status", OpVersion: 1, Field: "status", Strategy: "lww", ValueType: "enum", Enum: []string{"draft", "open", "closed", "merged"}},
		{OpType: "set-status", OpVersion: 1, Field: "merge_commit", Strategy: "lww", ValueType: "git-oid"},
		{OpType: "set-status", OpVersion: 1, Field: "reason", Strategy: "lww", ValueType: "string"},
		{OpType: "assign", OpVersion: 1, Field: "add", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref"},
		{OpType: "assign", OpVersion: 1, Field: "remove", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref"},
		{OpType: "approval", OpVersion: 1, Field: "revision", Strategy: "keyed-lww", Key: []string{"subject", "revision"}, ValueType: "git-oid", KeyTypes: approvalKeyTypes},
		{OpType: "approval", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww", Key: []string{"subject", "revision"}, ValueType: "enum", Enum: []string{"approve", "request-changes", "none"}, KeyTypes: approvalKeyTypes},
		{OpType: "approval", OpVersion: 1, Field: "subject", Strategy: "keyed-lww", Key: []string{"subject", "revision"}, ValueType: "person-ref", KeyTypes: approvalKeyTypes},
		{OpType: "approval", OpVersion: 1, Field: "message", Strategy: "keyed-lww", Key: []string{"subject", "revision"}, ValueType: "text", KeyTypes: approvalKeyTypes},
		{OpType: "ci-status", OpVersion: 1, Field: "revision", Target: "ci_revision", Strategy: "keyed-lww", Key: []string{"revision", "name"}, ValueType: "git-oid", KeyTypes: ciStatusKeyTypes},
		{OpType: "ci-status", OpVersion: 1, Field: "name", Strategy: "keyed-lww", Key: []string{"revision", "name"}, ValueType: "string", KeyTypes: ciStatusKeyTypes},
		{OpType: "ci-status", OpVersion: 1, Field: "state", Strategy: "keyed-lww", Key: []string{"revision", "name"}, ValueType: "enum", Enum: []string{"pending", "success", "failure", "error", "cancelled", "neutral", "skipped"}, KeyTypes: ciStatusKeyTypes},
		{OpType: "ci-status", OpVersion: 1, Field: "url", Strategy: "keyed-lww", Key: []string{"revision", "name"}, ValueType: "string", KeyTypes: ciStatusKeyTypes},
		{OpType: "ci-status", OpVersion: 1, Field: "description", Target: "ci_description", Strategy: "keyed-lww", Key: []string{"revision", "name"}, ValueType: "string", KeyTypes: ciStatusKeyTypes},
		{OpType: "ci-status", OpVersion: 1, Field: "started_at", Strategy: "keyed-lww", Key: []string{"revision", "name"}, ValueType: "timestamp", KeyTypes: ciStatusKeyTypes},
		{OpType: "ci-status", OpVersion: 1, Field: "completed_at", Strategy: "keyed-lww", Key: []string{"revision", "name"}, ValueType: "timestamp", KeyTypes: ciStatusKeyTypes},
		{OpType: "ci-status", OpVersion: 1, Field: "external_id", Strategy: "keyed-lww", Key: []string{"revision", "name"}, ValueType: "string", KeyTypes: ciStatusKeyTypes},
		{OpType: "label", OpVersion: 1, Field: "add", Target: "labels", Strategy: "set-observed-remove", ValueType: "object-ref"},
		{OpType: "label", OpVersion: 1, Field: "remove", Target: "labels", Strategy: "set-observed-remove", ValueType: "object-ref"},
		{OpType: "link", OpVersion: 1, Field: "target", Strategy: "keyed-lww", Key: []string{"target"}, ValueType: "object-ref", KeyTypes: linkKeyTypes},
		{OpType: "link", OpVersion: 1, Field: "target_type", Strategy: "keyed-lww", Key: []string{"target"}, ValueType: "string", KeyTypes: linkKeyTypes},
		{OpType: "link", OpVersion: 1, Field: "relation", Strategy: "keyed-lww", Key: []string{"target"}, ValueType: "enum", Enum: []string{"fixes", "relates", "none"}, KeyTypes: linkKeyTypes},
	}
}
