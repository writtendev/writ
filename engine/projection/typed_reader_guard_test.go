package projection_test

import (
	"context"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/engine/state"
)

// TestTypedReaderDegradesOnRedeclaredType is WRIT-189 round 2's MAJOR-3
// finding: mergeRules (engine/schema.go) installs a log-declared schema's
// rules for a built-in type name in place of the built-in ones — "log wins
// per type" is correct and unchanged by this test — but Reviews/Review's
// SQL is generated against the built-in shape (hard-coded column literals
// like r.f_title), so once a log schema reshapes o_review, both used to
// fail forever with an opaque "no such column" straight from SQLite. That
// is reachable through the shipped `writ schema apply` and, once applied,
// permanent: nothing about the schema log ever reverts a redeclaration on
// its own.
//
// A typed reader over a type the consumer has redefined has no business
// succeeding, but it must degrade rather than brick: this asserts Refresh
// stays green through the redeclaration (materialize.go never touches
// these readers) and that Reviews/Review then fail with a clear, named
// error — "review", not a raw SQLite message — while the generic Object
// API (which never assumed the built-in shape) keeps working against the
// very same reshaped table.
func TestTypedReaderDegradesOnRedeclaredType(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	env := makeReviewEnv("rev-1", "create", 1, map[string]any{"title": "T"})
	if _, err := store.Append(ctx, env, nil); err != nil {
		t.Fatalf("store.Append failed: %v", err)
	}

	if _, err := db.Refresh(store, projection.WithSchema(testRules())); err != nil {
		t.Fatalf("Refresh (built-in shape) failed: %v", err)
	}
	if _, err := db.Reviews(projection.ReviewFilter{}); err != nil {
		t.Fatalf("db.Reviews before redeclaration failed: %v", err)
	}
	if _, err := db.Review("rev-1"); err != nil {
		t.Fatalf("db.Review before redeclaration failed: %v", err)
	}

	// A log-declared schema redeclaring "review" down to a single unrelated
	// field — legal (RulesFromSchemas' "log wins per type"), and exactly the
	// shape review-ops.schema.json's own required "title"/"status" no
	// longer matches.
	base := testRules()
	redeclared := make(map[string][]state.Rule, len(base))
	for k, v := range base {
		redeclared[k] = v
	}
	redeclared["review"] = []state.Rule{
		{OpType: "create", Field: "name", Strategy: "lww", ValueType: "string", ObjectType: "review"},
	}

	if _, err := db.Refresh(store, projection.WithSchema(redeclared)); err != nil {
		t.Fatalf("Refresh (redeclared review) failed — the redeclaration itself must never brick Refresh: %v", err)
	}

	// "redeclared" pins this to requireBuiltinShape's own error text, not
	// merely to any error that happens to mention "review" — SQLite's own
	// "no such column: r.f_title" would satisfy a bare substring check on
	// "review" purely because the wrapping message says "query reviews",
	// which is exactly the opaque failure this guard exists to replace.
	if _, err := db.Reviews(projection.ReviewFilter{}); err == nil {
		t.Fatalf("db.Reviews after redeclaration: expected an error, got none")
	} else if !strings.Contains(err.Error(), "review") || !strings.Contains(err.Error(), "redeclared") {
		t.Fatalf("db.Reviews after redeclaration: error %q does not name the redeclared type", err.Error())
	}

	if _, err := db.Review("rev-1"); err == nil {
		t.Fatalf("db.Review after redeclaration: expected an error, got none")
	} else if !strings.Contains(err.Error(), "review") || !strings.Contains(err.Error(), "redeclared") {
		t.Fatalf("db.Review after redeclaration: error %q does not name the redeclared type", err.Error())
	}

	// The generic, schema-agnostic Object API never assumed review's
	// built-in shape and so keeps working against the very same reshaped
	// table — the degrade path leaves a way to still read the object.
	obj, err := db.Object("rev-1")
	if err != nil {
		t.Fatalf("db.Object after redeclaration failed: %v", err)
	}
	if obj.ObjectType != "review" {
		t.Fatalf("db.Object after redeclaration: ObjectType = %q, want %q", obj.ObjectType, "review")
	}

	// A second Refresh back at the built-in shape must un-degrade cleanly:
	// the guard compares against the currently installed descriptor, not
	// something pinned the first time a redeclaration was seen.
	if _, err := db.Refresh(store, projection.WithSchema(testRules())); err != nil {
		t.Fatalf("Refresh (back to built-in shape) failed: %v", err)
	}
	if _, err := db.Reviews(projection.ReviewFilter{}); err != nil {
		t.Fatalf("db.Reviews after reverting to built-in shape failed: %v", err)
	}
}

// TestObjectsDegradesOnRedeclaredType is WRIT-189 round 3 MAJOR-2's finding:
// Objects (the cross-type summary query) hard-codes o_review/o_issue/
// o_comment/o_project/o_cycle column literals of its own — a text-search
// EXISTS clause per type, and a default (!IncludeDeleted) filter against
// o_comment.f_deleted that fires on every call regardless of which filters
// are set — so it was exactly as bare to a redeclaration as Reviews/Review
// used to be, despite being the very API requireBuiltinShape's own error
// message pointed a caller at as the safe fallback. This pins that Objects
// now degrades the same way every other typed reader does: a clear, named
// error instead of a raw SQLite "no such column", for every one of the five
// hard-coded types, even a filter combination that would never have reached
// the specific clause naming the redeclared type.
func TestObjectsDegradesOnRedeclaredType(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	if err := db.ApplySchema(testRules()); err != nil {
		t.Fatalf("ApplySchema (built-in shape) failed: %v", err)
	}
	if _, err := db.Objects(projection.ObjectFilter{}); err != nil {
		t.Fatalf("db.Objects before redeclaration failed: %v", err)
	}

	// A log-declared schema redeclaring "comment" down to a single
	// unrelated field — legal ("log wins per type"), and exactly the shape
	// Objects' default deleted-filter clause (o_comment.f_deleted) no
	// longer matches.
	base := testRules()
	redeclared := make(map[string][]state.Rule, len(base))
	for k, v := range base {
		redeclared[k] = v
	}
	redeclared["comment"] = []state.Rule{
		{OpType: "create", Field: "name", Strategy: "lww", ValueType: "string", ObjectType: "comment"},
	}
	if err := db.ApplySchema(redeclared); err != nil {
		t.Fatalf("ApplySchema (redeclared comment) failed — the redeclaration itself must never brick ApplySchema: %v", err)
	}

	if _, err := db.Objects(projection.ObjectFilter{}); err == nil {
		t.Fatalf("db.Objects after redeclaring comment: expected an error, got none")
	} else if !strings.Contains(err.Error(), "comment") || !strings.Contains(err.Error(), "redeclared") {
		t.Fatalf("db.Objects after redeclaring comment: error %q does not name the redeclared type", err.Error())
	} else if strings.Contains(err.Error(), "Objects") {
		// requireBuiltinShape's message used to recommend "Objects/Object"
		// as the safe fallback; now that Objects is guarded too,
		// recommending it back to a caller Objects itself just refused
		// would be circular (WRIT-189 round 3 MAJOR-2's own instruction).
		t.Fatalf("db.Objects after redeclaring comment: error %q still recommends the now-guarded Objects API", err.Error())
	}

	// Still refuses even with a filter combination that would never reach
	// the specific o_comment.f_deleted clause on its own (no Text filter,
	// IncludeDeleted true skips the default deleted filter) — Objects is
	// guarded unconditionally against every type its SQL ever hard-codes,
	// not only whichever one the active filter happens to touch.
	if _, err := db.Objects(projection.ObjectFilter{IncludeDeleted: true}); err == nil {
		t.Fatalf("db.Objects(IncludeDeleted: true) after redeclaring comment: expected an error, got none")
	}

	// The generic, schema-agnostic Object API (singular) never hard-codes
	// any built-in column and so is unaffected by the redeclaration.
	if _, err := db.Object("nonexistent"); err != projection.ErrNotFound {
		t.Fatalf("db.Object after redeclaration: err = %v, want ErrNotFound", err)
	}

	// A second ApplySchema back at the built-in shape must un-degrade
	// cleanly, exactly like every other guarded reader.
	if err := db.ApplySchema(testRules()); err != nil {
		t.Fatalf("ApplySchema (back to built-in shape) failed: %v", err)
	}
	if _, err := db.Objects(projection.ObjectFilter{}); err != nil {
		t.Fatalf("db.Objects after reverting to built-in shape failed: %v", err)
	}
}

// TestIssuesDegradesOnRedeclaredWorkflowState is WRIT-189 round 5's MAJOR-2
// finding: Issues' f.State branch reads o_workflow_state.f_name and
// .f_type directly (an EXISTS clause resolving a state filter value against
// a workflow state's name or type), guarded only by
// requireBuiltinShape("issue") — which says nothing about whether
// "workflow-state" still has its built-in columns. A log schema redeclaring
// workflow-state alone (issue itself untouched) used to reshape
// o_workflow_state out from under that literal and turn a state-filtered
// Issues call into a raw SQLite "no such column: ws.f_name" instead of the
// named error every other typed reader degrades to (the same class round 3
// MAJOR-2 closed for Objects). group.go's GroupIssues(GroupByState, f) with
// f.State set reaches the identical branch through Issues(f), so this
// covers it too.
func TestIssuesDegradesOnRedeclaredWorkflowState(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	if err := db.ApplySchema(testRules()); err != nil {
		t.Fatalf("ApplySchema (built-in shape) failed: %v", err)
	}
	if _, err := db.Issues(projection.IssueFilter{}); err != nil {
		t.Fatalf("db.Issues (no filter) before redeclaration failed: %v", err)
	}
	if _, err := db.Issues(projection.IssueFilter{State: []string{"open"}}); err != nil {
		t.Fatalf("db.Issues (State filter) before redeclaration failed: %v", err)
	}

	// A log-declared schema redeclaring "workflow-state" down to a single
	// unrelated field — legal ("log wins per type") — and exactly the shape
	// Issues' f.State EXISTS clause (ws.f_name, ws.f_type) no longer matches.
	// "issue" itself is untouched.
	base := testRules()
	redeclared := make(map[string][]state.Rule, len(base))
	for k, v := range base {
		redeclared[k] = v
	}
	redeclared["workflow-state"] = []state.Rule{
		{OpType: "create", Field: "label", Strategy: "lww", ValueType: "string", ObjectType: "workflow-state"},
	}
	if err := db.ApplySchema(redeclared); err != nil {
		t.Fatalf("ApplySchema (redeclared workflow-state) failed — the redeclaration itself must never brick ApplySchema: %v", err)
	}

	// A state-filtered Issues call must degrade with a clear, named error,
	// not a raw SQLite "no such column".
	if _, err := db.Issues(projection.IssueFilter{State: []string{"open"}}); err == nil {
		t.Fatalf("db.Issues (State filter) after redeclaring workflow-state: expected an error, got none")
	} else if !strings.Contains(err.Error(), "workflow-state") || !strings.Contains(err.Error(), "redeclared") {
		t.Fatalf("db.Issues (State filter) after redeclaring workflow-state: error %q does not name the redeclared type", err.Error())
	}

	// A plain, unfiltered Issues call never reaches that branch and so keeps
	// working — the guard fires only where the query actually touches
	// workflow-state's columns.
	if _, err := db.Issues(projection.IssueFilter{}); err != nil {
		t.Fatalf("db.Issues (no filter) after redeclaring workflow-state failed: %v", err)
	}

	if err := db.ApplySchema(testRules()); err != nil {
		t.Fatalf("ApplySchema (back to built-in shape) failed: %v", err)
	}
	if _, err := db.Issues(projection.IssueFilter{State: []string{"open"}}); err != nil {
		t.Fatalf("db.Issues (State filter) after reverting to built-in shape failed: %v", err)
	}
}

// TestReviewsAndIssuesDegradeOnRedeclaredLabel is WRIT-189 round 5's MAJOR-2
// sweep: appendLabelFilter's generated EXISTS clause, used by both Reviews'
// and Issues' f.Label branch, resolves a label filter value against
// o_label.f_name directly — a second type's column neither reader's own
// requireBuiltinShape("review"/"issue") guard says anything about. Same
// class and same fix as workflow-state above.
func TestReviewsAndIssuesDegradeOnRedeclaredLabel(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	if err := db.ApplySchema(testRules()); err != nil {
		t.Fatalf("ApplySchema (built-in shape) failed: %v", err)
	}
	if _, err := db.Reviews(projection.ReviewFilter{Label: []string{"bug"}}); err != nil {
		t.Fatalf("db.Reviews (Label filter) before redeclaration failed: %v", err)
	}
	if _, err := db.Issues(projection.IssueFilter{Label: []string{"bug"}}); err != nil {
		t.Fatalf("db.Issues (Label filter) before redeclaration failed: %v", err)
	}

	base := testRules()
	redeclared := make(map[string][]state.Rule, len(base))
	for k, v := range base {
		redeclared[k] = v
	}
	redeclared["label"] = []state.Rule{
		{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "label"},
	}
	if err := db.ApplySchema(redeclared); err != nil {
		t.Fatalf("ApplySchema (redeclared label) failed — the redeclaration itself must never brick ApplySchema: %v", err)
	}

	if _, err := db.Reviews(projection.ReviewFilter{Label: []string{"bug"}}); err == nil {
		t.Fatalf("db.Reviews (Label filter) after redeclaring label: expected an error, got none")
	} else if !strings.Contains(err.Error(), "label") || !strings.Contains(err.Error(), "redeclared") {
		t.Fatalf("db.Reviews (Label filter) after redeclaring label: error %q does not name the redeclared type", err.Error())
	}
	if _, err := db.Issues(projection.IssueFilter{Label: []string{"bug"}}); err == nil {
		t.Fatalf("db.Issues (Label filter) after redeclaring label: expected an error, got none")
	} else if !strings.Contains(err.Error(), "label") || !strings.Contains(err.Error(), "redeclared") {
		t.Fatalf("db.Issues (Label filter) after redeclaring label: error %q does not name the redeclared type", err.Error())
	}

	// Unfiltered-by-label calls never reach appendLabelFilter and so keep
	// working.
	if _, err := db.Reviews(projection.ReviewFilter{}); err != nil {
		t.Fatalf("db.Reviews (no Label filter) after redeclaring label failed: %v", err)
	}
	if _, err := db.Issues(projection.IssueFilter{}); err != nil {
		t.Fatalf("db.Issues (no Label filter) after redeclaring label failed: %v", err)
	}

	if err := db.ApplySchema(testRules()); err != nil {
		t.Fatalf("ApplySchema (back to built-in shape) failed: %v", err)
	}
	if _, err := db.Reviews(projection.ReviewFilter{Label: []string{"bug"}}); err != nil {
		t.Fatalf("db.Reviews (Label filter) after reverting to built-in shape failed: %v", err)
	}
	if _, err := db.Issues(projection.IssueFilter{Label: []string{"bug"}}); err != nil {
		t.Fatalf("db.Issues (Label filter) after reverting to built-in shape failed: %v", err)
	}
}
