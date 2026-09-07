package projection_test

import (
	"testing"

	"github.com/writtendev/writ/engine/projection"
)

func TestGroupIssuesByState(t *testing.T) {
	db := setupSeededDB(t)
	defer db.Close()

	groups, err := db.GroupIssues(projection.GroupByState, projection.IssueFilter{})
	if err != nil {
		t.Fatalf("GroupIssues(GroupByState): %v", err)
	}

	// Distinct states in seeded DB: closed (1), in_progress (1), open (2)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}

	// Alphabetical order: closed, in_progress, open
	if groups[0].Key != "closed" || groups[0].Count != 1 {
		t.Errorf("group 0: expected closed (1), got %s (%d)", groups[0].Key, groups[0].Count)
	}
	if groups[1].Key != "in_progress" || groups[1].Count != 1 {
		t.Errorf("group 1: expected in_progress (1), got %s (%d)", groups[1].Key, groups[1].Count)
	}
	if groups[2].Key != "open" || groups[2].Count != 2 {
		t.Errorf("group 2: expected open (2), got %s (%d)", groups[2].Key, groups[2].Count)
	}

	// Sum of counts must equal total matching issues
	totalCount := 0
	for _, g := range groups {
		totalCount += g.Count
		if len(g.Issues) != g.Count {
			t.Errorf("group %s count %d != len(issues) %d", g.Key, g.Count, len(g.Issues))
		}
	}
	if totalCount != 4 {
		t.Errorf("expected sum of counts = 4, got %d", totalCount)
	}
}

func TestGroupIssuesByAssignee(t *testing.T) {
	db := setupSeededDB(t)
	defer db.Close()

	groups, err := db.GroupIssues(projection.GroupByAssignee, projection.IssueFilter{})
	if err != nil {
		t.Fatalf("GroupIssues(GroupByAssignee): %v", err)
	}

	// Seeded issues:
	// iss-1: assignees [user:alice, user:bob]
	// iss-2: assignees [user:bob]
	// iss-3: assignees [] (unassigned)
	// iss-4: assignees [] (unassigned)
	//
	// Expected groups:
	// "" (unassigned): iss-3, iss-4 (count 2)
	// "user:alice": iss-1 (count 1)
	// "user:bob": iss-1, iss-2 (count 2)

	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}

	// Group "": unassigned
	if groups[0].Key != "" || groups[0].Count != 2 {
		t.Errorf("group 0: expected '' (2), got %q (%d)", groups[0].Key, groups[0].Count)
	}
	unassignedIDs := []string{groups[0].Issues[0].ObjectID, groups[0].Issues[1].ObjectID}
	if unassignedIDs[0] != "iss-3" || unassignedIDs[1] != "iss-4" {
		t.Errorf("expected unassigned issues [iss-3, iss-4], got %v", unassignedIDs)
	}

	// Group "user:alice"
	if groups[1].Key != "user:alice" || groups[1].Count != 1 {
		t.Errorf("group 1: expected 'user:alice' (1), got %q (%d)", groups[1].Key, groups[1].Count)
	}
	if groups[1].Issues[0].ObjectID != "iss-1" {
		t.Errorf("expected alice's issue to be iss-1, got %s", groups[1].Issues[0].ObjectID)
	}

	// Group "user:bob"
	if groups[2].Key != "user:bob" || groups[2].Count != 2 {
		t.Errorf("group 2: expected 'user:bob' (2), got %q (%d)", groups[2].Key, groups[2].Count)
	}
	bobIDs := []string{groups[2].Issues[0].ObjectID, groups[2].Issues[1].ObjectID}
	if bobIDs[0] != "iss-1" || bobIDs[1] != "iss-2" {
		t.Errorf("expected bob's issues [iss-1, iss-2], got %v", bobIDs)
	}
}

func TestGroupIssuesWithFilter(t *testing.T) {
	db := setupSeededDB(t)
	defer db.Close()

	// Filter by Label: bug (only iss-1)
	groups, err := db.GroupIssues(projection.GroupByAssignee, projection.IssueFilter{
		Label: []string{"bug"},
	})
	if err != nil {
		t.Fatalf("GroupIssues with filter: %v", err)
	}

	// iss-1 is assigned to user:alice and user:bob
	// Expected groups: "user:alice" (1), "user:bob" (1)
	if len(groups) != 2 {
		t.Fatalf("expected 2 groups for bug label, got %d", len(groups))
	}
	if groups[0].Key != "user:alice" || groups[0].Count != 1 || groups[0].Issues[0].ObjectID != "iss-1" {
		t.Errorf("unexpected alice group: %+v", groups[0])
	}
	if groups[1].Key != "user:bob" || groups[1].Count != 1 || groups[1].Issues[0].ObjectID != "iss-1" {
		t.Errorf("unexpected bob group: %+v", groups[1])
	}
}

func TestGroupIssuesInvalidKey(t *testing.T) {
	db := setupSeededDB(t)
	defer db.Close()

	_, err := db.GroupIssues(projection.GroupKey("invalid"), projection.IssueFilter{})
	if err == nil {
		t.Errorf("expected error for invalid GroupKey, got nil")
	}
}

// TestGroupIssuesByStateUnresolvedStateLandsUnknown proves FC-16 tolerance at
// the grouping layer: an issue whose state reference matches no known
// workflow-state (by object ID or by name) falls through to the "Unknown"
// group. It must never be silently coerced into a real workflow state, since
// this ticket (WRIT-183) removes the "open"/"closed" bridging that used to
// do exactly that.
func TestGroupIssuesByStateUnresolvedStateLandsUnknown(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()
	if err := db.ApplySchema(testRules()); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	rawDB := db.DB()

	insertObject(t, rawDB, "ws-todo", "workflow-state", 1, "op-ws-1", "A", "a@example.com", 900, 900)
	execSQL(t, rawDB, "INSERT INTO o_workflow_state (object_id, f_name, f_type, f_position, f_position__op_id, f_color, f_description) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"ws-todo", "Todo", "unstarted", "V", "op-ws-1", "", "")
	insertObject(t, rawDB, "ws-done", "workflow-state", 1, "op-ws-2", "A", "a@example.com", 910, 910)
	execSQL(t, rawDB, "INSERT INTO o_workflow_state (object_id, f_name, f_type, f_position, f_position__op_id, f_color, f_description) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"ws-done", "Done", "completed", "s", "op-ws-2", "", "")

	insertObject(t, rawDB, "iss-known", "issue", 1, "op-1", "A", "a@example.com", 1000, 1000)
	execSQL(t, rawDB, "INSERT INTO o_issue (object_id, f_title, f_description, f_state, f_reason) VALUES (?, ?, ?, ?, ?)",
		"iss-known", "Known state issue", "", "ws-todo", "")

	insertObject(t, rawDB, "iss-unresolved", "issue", 1, "op-2", "A", "a@example.com", 1010, 1010)
	execSQL(t, rawDB, "INSERT INTO o_issue (object_id, f_title, f_description, f_state, f_reason) VALUES (?, ?, ?, ?, ?)",
		"iss-unresolved", "Unresolved state issue", "", "not-a-real-state", "")

	groups, err := db.GroupIssues(projection.GroupByState, projection.IssueFilter{})
	if err != nil {
		t.Fatalf("GroupIssues(GroupByState): %v", err)
	}

	if len(groups) != 2 {
		t.Fatalf("expected 2 groups (Todo, Unknown), got %d: %+v", len(groups), groups)
	}

	var todoGroup, unknownGroup *projection.Group
	for i := range groups {
		switch groups[i].Key {
		case "Todo":
			todoGroup = &groups[i]
		case "Unknown":
			unknownGroup = &groups[i]
		}
	}

	if todoGroup == nil || todoGroup.Count != 1 || todoGroup.Issues[0].ObjectID != "iss-known" {
		t.Errorf("expected Todo group with iss-known, got %+v", todoGroup)
	}
	if unknownGroup == nil || unknownGroup.Count != 1 || unknownGroup.Issues[0].ObjectID != "iss-unresolved" {
		t.Errorf("expected Unknown group with iss-unresolved (not coerced into a real state), got %+v", unknownGroup)
	}
}

func TestGroupIssuesByPriority(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()
	if err := db.ApplySchema(testRules()); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	rawDB := db.DB()
	insertObject(t, rawDB, "iss-u", "issue", 1, "op-1", "A", "a@example.com", 1000, 1000)
	insertObject(t, rawDB, "iss-h1", "issue", 1, "op-2", "A", "a@example.com", 1010, 1010)
	insertObject(t, rawDB, "iss-h2", "issue", 1, "op-3", "A", "a@example.com", 1020, 1020)
	insertObject(t, rawDB, "iss-n", "issue", 1, "op-4", "A", "a@example.com", 1030, 1030)

	execSQL(t, rawDB, "INSERT INTO o_issue (object_id, f_title, f_description, f_state, f_reason, f_priority, f_position, f_position__op_id) VALUES (?, ?, ?, ?, '', ?, ?, ?)",
		"iss-u", "Urgent issue", "", "open", 1, "V", "op-1")
	execSQL(t, rawDB, "INSERT INTO o_issue (object_id, f_title, f_description, f_state, f_reason, f_priority, f_position, f_position__op_id) VALUES (?, ?, ?, ?, '', ?, ?, ?)",
		"iss-h2", "High issue 2", "", "open", 2, "aV", "op-3")
	execSQL(t, rawDB, "INSERT INTO o_issue (object_id, f_title, f_description, f_state, f_reason, f_priority, f_position, f_position__op_id) VALUES (?, ?, ?, ?, '', ?, ?, ?)",
		"iss-h1", "High issue 1", "", "open", 2, "V", "op-2")
	execSQL(t, rawDB, "INSERT INTO o_issue (object_id, f_title, f_description, f_state, f_reason, f_priority, f_position, f_position__op_id) VALUES (?, ?, ?, ?, '', ?, ?, ?)",
		"iss-n", "None issue", "", "open", 0, "V", "op-4")

	groups, err := db.GroupIssues(projection.GroupByPriority, projection.IssueFilter{})
	if err != nil {
		t.Fatalf("GroupIssues(GroupByPriority): %v", err)
	}

	if len(groups) != 3 {
		t.Fatalf("expected 3 groups (urgent, high, none), got %d", len(groups))
	}
	if groups[0].Key != "urgent" || groups[0].Count != 1 {
		t.Errorf("expected urgent group, got %s (%d)", groups[0].Key, groups[0].Count)
	}
	if groups[1].Key != "high" || groups[1].Count != 2 {
		t.Errorf("expected high group, got %s (%d)", groups[1].Key, groups[1].Count)
	}
	if groups[1].Issues[0].ObjectID != "iss-h1" || groups[1].Issues[1].ObjectID != "iss-h2" {
		t.Errorf("expected [iss-h1, iss-h2], got [%s, %s]", groups[1].Issues[0].ObjectID, groups[1].Issues[1].ObjectID)
	}
	if groups[2].Key != "none" || groups[2].Count != 1 {
		t.Errorf("expected none group, got %s (%d)", groups[2].Key, groups[2].Count)
	}
}

