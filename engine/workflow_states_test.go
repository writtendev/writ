package writ_test

import (
	"context"
	"testing"

	"github.com/writtendev/writ/engine"
)

func TestWorkflowStatesCRUDAndOrdering(t *testing.T) {
	ctx := context.Background()
	repoDir, _ := setupConfiguredRepo(t)

	s, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	// 1. Seed defaults
	if err := s.WorkflowStates.SeedDefaults(ctx); err != nil {
		t.Fatalf("SeedDefaults failed: %v", err)
	}

	states, err := s.Query.WorkflowStates(writ.WorkflowStateFilter{})
	if err != nil {
		t.Fatalf("Query.WorkflowStates failed: %v", err)
	}
	if len(states) != 5 {
		t.Fatalf("expected 5 default states, got %d", len(states))
	}

	// Verify order: Backlog (1), Todo (V), In Progress (k), Done (s), Canceled (zV)
	expectedNames := []string{"Backlog", "Todo", "In Progress", "Done", "Canceled"}
	for i, want := range expectedNames {
		if got := states[i].WorkflowState.Name; got != want {
			t.Errorf("state[%d].Name = %q, want %q", i, got, want)
		}
	}

	// Idempotent SeedDefaults
	if err := s.WorkflowStates.SeedDefaults(ctx); err != nil {
		t.Fatalf("SeedDefaults second call failed: %v", err)
	}
	states2, err := s.Query.WorkflowStates(writ.WorkflowStateFilter{})
	if err != nil {
		t.Fatalf("Query.WorkflowStates failed: %v", err)
	}
	if len(states2) != 5 {
		t.Fatalf("expected 5 states after re-seed, got %d", len(states2))
	}

	// 2. Create custom state between Todo (V) and In Progress (k)
	reviewID, err := s.WorkflowStates.Create(ctx, writ.NewWorkflowState{
		Name:     "In Review",
		Type:     "started",
		Position: "f",
		Color:    "#f2c94c",
	})
	if err != nil {
		t.Fatalf("Create custom state failed: %v", err)
	}

	statesAfterInsert, err := s.Query.WorkflowStates(writ.WorkflowStateFilter{})
	if err != nil {
		t.Fatalf("Query.WorkflowStates failed: %v", err)
	}
	if len(statesAfterInsert) != 6 {
		t.Fatalf("expected 6 states, got %d", len(statesAfterInsert))
	}
	// "f" is between "V" and "k"
	if got := statesAfterInsert[2].WorkflowState.Name; got != "In Review" {
		t.Errorf("expected In Review at index 2, got %q", got)
	}

	// 3. Update state
	newName := "Code Review"
	newColor := "#e2b93c"
	if err := s.WorkflowStates.Update(ctx, reviewID, writ.WorkflowStateEdit{
		Name:  &newName,
		Color: &newColor,
	}); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	fetched, err := s.Query.WorkflowState(reviewID)
	if err != nil {
		t.Fatalf("Query.WorkflowState failed: %v", err)
	}
	if fetched.WorkflowState.Name != "Code Review" {
		t.Errorf("got name %q, want Code Review", fetched.WorkflowState.Name)
	}
	if fetched.WorkflowState.Color != "#e2b93c" {
		t.Errorf("got color %q, want #e2b93c", fetched.WorkflowState.Color)
	}

	// 4. Point issues at workflow states
	todoID := states[1].ObjectID
	iss1, err := s.Issues.Create(ctx, writ.NewIssue{Title: "Task 1"})
	if err != nil {
		t.Fatalf("Create issue 1: %v", err)
	}
	if err := s.Issues.SetState(ctx, iss1, writ.IssueState{State: todoID}); err != nil {
		t.Fatalf("SetState issue 1: %v", err)
	}

	iss2, err := s.Issues.Create(ctx, writ.NewIssue{Title: "Task 2"})
	if err != nil {
		t.Fatalf("Create issue 2: %v", err)
	}
	if err := s.Issues.SetState(ctx, iss2, writ.IssueState{State: reviewID}); err != nil {
		t.Fatalf("SetState issue 2: %v", err)
	}

	// Unknown state issue
	iss3, err := s.Issues.Create(ctx, writ.NewIssue{Title: "Task 3"})
	if err != nil {
		t.Fatalf("Create issue 3: %v", err)
	}
	if err := s.Issues.SetState(ctx, iss3, writ.IssueState{State: "unknownstateid123"}); err != nil {
		t.Fatalf("SetState issue 3 with unknown state: %v", err)
	}

	// 5. Test Query by State Name / Type / ID
	byName, err := s.Query.Issues(writ.IssueFilter{State: []string{"Todo"}})
	if err != nil {
		t.Fatalf("Query issues by state name Todo: %v", err)
	}
	if len(byName) != 1 || byName[0].ObjectID != iss1 {
		t.Errorf("expected iss1 for Todo, got %+v", byName)
	}

	byType, err := s.Query.Issues(writ.IssueFilter{State: []string{"started"}})
	if err != nil {
		t.Fatalf("Query issues by type started: %v", err)
	}
	if len(byType) != 1 || byType[0].ObjectID != iss2 {
		t.Errorf("expected iss2 for started, got %+v", byType)
	}
}

func TestWorkflowStatesIdenticalPositionTiebreakAndStability(t *testing.T) {
	ctx := context.Background()
	dir, _ := setupTestRepoWithID(t, "alice", "alice@writ.dev")
	s, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	// 1. Create two workflow states with identical position "V"
	_, err = s.WorkflowStates.Create(ctx, writ.NewWorkflowState{
		Name:     "Col Alpha",
		Type:     "started",
		Position: "V",
	})
	if err != nil {
		t.Fatalf("Create s1: %v", err)
	}

	_, err = s.WorkflowStates.Create(ctx, writ.NewWorkflowState{
		Name:     "Col Beta",
		Type:     "unstarted",
		Position: "V",
	})
	if err != nil {
		t.Fatalf("Create s2: %v", err)
	}

	states, err := s.Query.WorkflowStates(writ.WorkflowStateFilter{})
	if err != nil {
		t.Fatalf("Query.WorkflowStates: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("expected 2 states, got %d", len(states))
	}
	firstID := states[0].ObjectID
	secondID := states[1].ObjectID

	// 2. Update metadata (e.g. name/color) on the first state without touching position
	newName := "Col Alpha Updated"
	if err := s.WorkflowStates.Update(ctx, firstID, writ.WorkflowStateEdit{
		Name: &newName,
	}); err != nil {
		t.Fatalf("Update s1: %v", err)
	}

	statesAfter, err := s.Query.WorkflowStates(writ.WorkflowStateFilter{})
	if err != nil {
		t.Fatalf("Query.WorkflowStates after update: %v", err)
	}
	if statesAfter[0].ObjectID != firstID || statesAfter[1].ObjectID != secondID {
		t.Fatalf("state order flipped after unrelated metadata update! got %s, %s; want %s, %s",
			statesAfter[0].ObjectID, statesAfter[1].ObjectID, firstID, secondID)
	}
}
