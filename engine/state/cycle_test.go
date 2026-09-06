package state_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	s "github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec"
)

func TestCycleRulesDriftGuard(t *testing.T) {
	allRules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules failed: %v", err)
	}

	var expectedRules []s.Rule
	for _, r := range allRules {
		if r.Vocabulary == "cycle" {
			expectedRules = append(expectedRules, s.Rule{
				OpType:    r.OpType,
				OpVersion: r.OpVersion,
				Field:     r.Field,
				Strategy:  r.Strategy,
				Key:       r.Key,
				Lattice:   r.Lattice,
				ValueType: r.ValueType,
				Enum:      r.Enum,
				MaxLength: r.MaxLength,
				KeyTypes:  r.KeyTypes,
			})
		}
	}

	builtIn := s.CycleRules()
	if !reflect.DeepEqual(builtIn, expectedRules) {
		t.Fatalf("CycleRules() drifted from published cycle field-rules.json:\n got:  %+v\n want: %+v", builtIn, expectedRules)
	}
}

func TestFoldCycleEmpty(t *testing.T) {
	state, err := s.FoldCycle(nil)
	if err != nil {
		t.Fatalf("FoldCycle(nil) returned error: %v", err)
	}
	if !reflect.DeepEqual(state, s.Cycle{}) {
		t.Fatalf("expected empty Cycle, got %+v", state)
	}
}

func TestFoldCycleLifecycleAndDates(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	createOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "c-test",
			ObjectType: "cycle",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Cycle 1","starts_at":"2026-09-01T00:00:00Z","ends_at":"2026-09-15T00:00:00Z","description":"Cycle 1 description"}`),
		},
		ID: "c1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	updateOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "c-test",
			ObjectType: "cycle",
			OpType:     "update",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Cycle 1 — Renamed"}`),
		},
		ID:      "u1",
		Parents: []string{"c1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(time.Minute),
		},
	}

	setDatesOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "c-test",
			ObjectType: "cycle",
			OpType:     "set-dates",
			OpVersion:  1,
			Body:       json.RawMessage(`{"starts_at":"2026-09-01T00:00:00Z","ends_at":"2026-09-20T00:00:00Z"}`),
		},
		ID:      "d1",
		Parents: []string{"u1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	state, err := s.FoldCycle([]codec.Op{createOp, updateOp, setDatesOp})
	if err != nil {
		t.Fatalf("FoldCycle failed: %v", err)
	}

	if state.Title != "Cycle 1 — Renamed" {
		t.Errorf("title mismatch: got %q, want 'Cycle 1 — Renamed'", state.Title)
	}
	if state.Description != "Cycle 1 description" {
		t.Errorf("description mismatch: got %q, want 'Cycle 1 description'", state.Description)
	}
	if state.StartsAt != "2026-09-01T00:00:00Z" {
		t.Errorf("starts_at mismatch: got %q, want '2026-09-01T00:00:00Z'", state.StartsAt)
	}
	if state.EndsAt != "2026-09-20T00:00:00Z" {
		t.Errorf("ends_at mismatch: got %q, want '2026-09-20T00:00:00Z'", state.EndsAt)
	}
}

func TestFoldCycleMembershipORSetCausalAndConcurrent(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	iss1 := "0123456789abcdef0123456789abcdef"
	iss2 := "fedcba9876543210fedcba9876543210"

	op1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "c-test",
			ObjectType: "cycle",
			OpType:     "add-issue",
			OpVersion:  1,
			Body:       json.RawMessage(`{"issue":"` + iss1 + `"}`),
		},
		ID:     "op1",
		Author: codec.Identity{Email: "alice@example.com", When: now},
	}

	op2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "c-test",
			ObjectType: "cycle",
			OpType:     "remove-issue",
			OpVersion:  1,
			Body:       json.RawMessage(`{"issue":"` + iss1 + `"}`),
		},
		ID:      "op2",
		Parents: []string{"op1"},
		Author:  codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
	}

	op3 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "c-test",
			ObjectType: "cycle",
			OpType:     "add-issue",
			OpVersion:  1,
			Body:       json.RawMessage(`{"issue":"` + iss2 + `"}`),
		},
		ID:      "op3",
		Parents: []string{"op2"},
		Author:  codec.Identity{Email: "alice@example.com", When: now.Add(2 * time.Minute)},
	}

	state, err := s.FoldCycle([]codec.Op{op1, op2, op3})
	if err != nil {
		t.Fatalf("FoldCycle failed: %v", err)
	}

	expectedIssues := []string{iss2}
	if !reflect.DeepEqual(state.Issues, expectedIssues) {
		t.Errorf("issues mismatch: got %v, want %v", state.Issues, expectedIssues)
	}
}

func TestFoldCycleCrossRepoReferences(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	bareIss := "0123456789abcdef0123456789abcdef"
	qualIss := "repo-xyz#0123456789abcdef0123456789abcdef"

	op1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "c-test",
			ObjectType: "cycle",
			OpType:     "add-issue",
			OpVersion:  1,
			Body:       json.RawMessage(`{"issue":"` + bareIss + `"}`),
		},
		ID:     "op1",
		Author: codec.Identity{Email: "alice@example.com", When: now},
	}

	op2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "c-test",
			ObjectType: "cycle",
			OpType:     "add-issue",
			OpVersion:  1,
			Body:       json.RawMessage(`{"issue":"` + qualIss + `"}`),
		},
		ID:      "op2",
		Parents: []string{"op1"},
		Author:  codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
	}

	state, err := s.FoldCycle([]codec.Op{op1, op2})
	if err != nil {
		t.Fatalf("FoldCycle failed: %v", err)
	}

	if len(state.Issues) != 2 {
		t.Fatalf("expected 2 distinct issues, got %d", len(state.Issues))
	}
	if state.Issues[0] != bareIss || state.Issues[1] != qualIss {
		t.Errorf("issues mismatch: got %v", state.Issues)
	}
}

func TestFoldCycleUnknownOpVersionAndType(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	v1Create := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "c-compat",
			ObjectType: "cycle",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Cycle 1","starts_at":"2026-09-01T00:00:00Z","ends_at":"2026-09-15T00:00:00Z"}`),
		},
		ID:     "op-v1",
		Author: codec.Identity{Email: "alice@example.com", When: now},
	}

	v2Update := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "c-compat",
			ObjectType: "cycle",
			OpType:     "update",
			OpVersion:  2,
			Body:       json.RawMessage(`{"title":"V2 Ignored"}`),
		},
		ID:      "op-v2",
		Parents: []string{"op-v1"},
		Author:  codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
	}

	state, err := s.FoldCycle([]codec.Op{v1Create, v2Update})
	if err != nil {
		t.Fatalf("FoldCycle failed: %v", err)
	}

	if state.Title != "Cycle 1" {
		t.Errorf("title mismatch: got %q, want 'Cycle 1'", state.Title)
	}
	if len(state.UnknownOps) != 1 || state.UnknownOps[0].Commit != "op-v2" || state.UnknownOps[0].ObjectType != "cycle" {
		t.Errorf("unknown_ops mismatch: %+v", state.UnknownOps)
	}
}

func TestFoldCycleMalformedBodyError(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	badOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "c-err",
			ObjectType: "cycle",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{malformed`),
		},
		ID:     "bad1",
		Author: codec.Identity{When: now},
	}

	_, errFold := s.Fold([]codec.Op{badOp}, s.CycleRules())
	if errFold == nil {
		t.Fatal("expected Fold to error on malformed JSON body, got nil")
	}

	_, errCycle := s.FoldCycle([]codec.Op{badOp})
	if errCycle == nil {
		t.Fatal("expected FoldCycle to error on malformed JSON body, got nil")
	}
}

func TestFoldCycleAgreement(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	ops := []codec.Op{
		{
			ID: "c1",
			Envelope: codec.Envelope{
				ObjectID:   "c-agree",
				ObjectType: "cycle",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"Cycle Title","description":"Cycle Description","starts_at":"2026-09-01T00:00:00Z","ends_at":"2026-09-15T00:00:00Z"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now},
		},
		{
			ID:      "u1",
			Parents: []string{"c1"},
			Envelope: codec.Envelope{
				ObjectID:   "c-agree",
				ObjectType: "cycle",
				OpType:     "update",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"New Cycle Title"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
		},
		{
			ID:      "d1",
			Parents: []string{"u1"},
			Envelope: codec.Envelope{
				ObjectID:   "c-agree",
				ObjectType: "cycle",
				OpType:     "set-dates",
				OpVersion:  1,
				Body:       json.RawMessage(`{"starts_at":"2026-09-02T00:00:00Z","ends_at":"2026-09-16T00:00:00Z"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(2 * time.Minute)},
		},
		{
			ID:      "iss1",
			Parents: []string{"d1"},
			Envelope: codec.Envelope{
				ObjectID:   "c-agree",
				ObjectType: "cycle",
				OpType:     "add-issue",
				OpVersion:  1,
				Body:       json.RawMessage(`{"issue":"ISSUE-1"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(3 * time.Minute)},
		},
		{
			ID:      "iss2",
			Parents: []string{"iss1"},
			Envelope: codec.Envelope{
				ObjectID:   "c-agree",
				ObjectType: "cycle",
				OpType:     "add-issue",
				OpVersion:  1,
				Body:       json.RawMessage(`{"issue":"ISSUE-2"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(4 * time.Minute)},
		},
		{
			ID:      "iss3",
			Parents: []string{"iss2"},
			Envelope: codec.Envelope{
				ObjectID:   "c-agree",
				ObjectType: "cycle",
				OpType:     "remove-issue",
				OpVersion:  1,
				Body:       json.RawMessage(`{"issue":"ISSUE-1"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(5 * time.Minute)},
		},
	}

	cycleState, err := s.FoldCycle(ops)
	if err != nil {
		t.Fatalf("FoldCycle failed: %v", err)
	}

	objectState, err := s.Fold(ops, s.CycleRules())
	if err != nil {
		t.Fatalf("Fold failed: %v", err)
	}

	if cycleState.Title != objectState.State["title"] {
		t.Errorf("title mismatch: got %q, want %v", cycleState.Title, objectState.State["title"])
	}
	if cycleState.Description != objectState.State["description"] {
		t.Errorf("description mismatch: got %q, want %v", cycleState.Description, objectState.State["description"])
	}
	if cycleState.StartsAt != objectState.State["starts_at"] {
		t.Errorf("starts_at mismatch: got %q, want %v", cycleState.StartsAt, objectState.State["starts_at"])
	}
	if cycleState.EndsAt != objectState.State["ends_at"] {
		t.Errorf("ends_at mismatch: got %q, want %v", cycleState.EndsAt, objectState.State["ends_at"])
	}
	if !reflect.DeepEqual(cycleState.Issues, objectState.State["issue"]) {
		t.Errorf("issues mismatch: got %v, want %v", cycleState.Issues, objectState.State["issue"])
	}
}

func TestFoldCycleScalarAndNestedMembership(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	ops := []codec.Op{
		{
			ID: "op1",
			Envelope: codec.Envelope{
				ObjectID:   "c-mem",
				ObjectType: "cycle",
				OpType:     "add-issue",
				OpVersion:  1,
				Body:       json.RawMessage(`{"issue":["ISSUE-1","ISSUE-2"]}`),
			},
			Author: codec.Identity{When: now},
		},
		{
			ID:      "op2",
			Parents: []string{"op1"},
			Envelope: codec.Envelope{
				ObjectID:   "c-mem",
				ObjectType: "cycle",
				OpType:     "remove-issue",
				OpVersion:  1,
				Body:       json.RawMessage(`{"issue":"ISSUE-1"}`),
			},
			Author: codec.Identity{When: now.Add(time.Minute)},
		},
		{
			ID:      "op3",
			Parents: []string{"op2"},
			Envelope: codec.Envelope{
				ObjectID:   "c-mem",
				ObjectType: "cycle",
				OpType:     "add-issue",
				OpVersion:  1,
				Body:       json.RawMessage(`{"issue":{"add":["ISSUE-3"],"remove":["ISSUE-2"]}}`),
			},
			Author: codec.Identity{When: now.Add(2 * time.Minute)},
		},
	}

	cycleState, err := s.FoldCycle(ops)
	if err != nil {
		t.Fatalf("FoldCycle failed: %v", err)
	}

	objectState, err := s.Fold(ops, s.CycleRules())
	if err != nil {
		t.Fatalf("Fold failed: %v", err)
	}

	wantIssues := []string{"ISSUE-3"}
	if !reflect.DeepEqual(cycleState.Issues, wantIssues) {
		t.Errorf("FoldCycle issues = %v, want %v", cycleState.Issues, wantIssues)
	}
	if !reflect.DeepEqual(objectState.State["issue"], wantIssues) {
		t.Errorf("Fold generic issue = %v, want %v", objectState.State["issue"], wantIssues)
	}
}
