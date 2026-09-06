package writ_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/spec"
)

func TestProjectRulesDriftGuard(t *testing.T) {
	allRules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules failed: %v", err)
	}

	var expectedRules []writ.Rule
	for _, r := range allRules {
		if r.Vocabulary == "project" {
			expectedRules = append(expectedRules, writ.Rule{
				OpType:    r.OpType,
				OpVersion: r.OpVersion,
				Field:     r.Field,
				Target:    r.Target,
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

	builtIn := writ.ProjectRules()
	if !reflect.DeepEqual(builtIn, expectedRules) {
		t.Fatalf("ProjectRules() drifted from published project field-rules.json:\n got:  %+v\n want: %+v", builtIn, expectedRules)
	}
}

func TestFoldProjectEmpty(t *testing.T) {
	state, err := writ.FoldProject(nil)
	if err != nil {
		t.Fatalf("FoldProject(nil) returned error: %v", err)
	}
	if !reflect.DeepEqual(state, writ.Project{}) {
		t.Fatalf("expected empty Project, got %+v", state)
	}
}

func TestFoldProjectLifecycle(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	createOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-test",
			ObjectType: "project",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Initial Project","description":"Project Description"}`),
		},
		ID: "c1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	updateOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-test",
			ObjectType: "project",
			OpType:     "update",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Updated Project"}`),
		},
		ID:      "u1",
		Parents: []string{"c1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(time.Minute),
		},
	}

	statusOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-test",
			ObjectType: "project",
			OpType:     "set-status",
			OpVersion:  1,
			Body:       json.RawMessage(`{"status":"active","reason":"Kickoff completed"}`),
		},
		ID:      "s1",
		Parents: []string{"u1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	state, err := writ.FoldProject([]codec.Op{createOp, updateOp, statusOp})
	if err != nil {
		t.Fatalf("FoldProject failed: %v", err)
	}

	if state.Title != "Updated Project" {
		t.Errorf("title mismatch: got %q, want %q", state.Title, "Updated Project")
	}
	if state.Description != "Project Description" {
		t.Errorf("description mismatch: got %q, want %q", state.Description, "Project Description")
	}
	if state.Status != "active" {
		t.Errorf("status mismatch: got %q, want %q", state.Status, "active")
	}
	if state.Reason != "Kickoff completed" {
		t.Errorf("reason mismatch: got %q, want %q", state.Reason, "Kickoff completed")
	}
}

func TestFoldProjectMembershipORSetCausalAndConcurrent(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	iss1 := "0123456789abcdef0123456789abcdef"
	iss2 := "fedcba9876543210fedcba9876543210"
	iss3 := "11112222333344445555666677778888"

	// op1: add iss1
	op1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-test",
			ObjectType: "project",
			OpType:     "add-issue",
			OpVersion:  1,
			Body:       json.RawMessage(`{"issue":"` + iss1 + `"}`),
		},
		ID: "op1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	// op2: add iss2
	op2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-test",
			ObjectType: "project",
			OpType:     "add-issue",
			OpVersion:  1,
			Body:       json.RawMessage(`{"issue":"` + iss2 + `"}`),
		},
		ID:      "op2",
		Parents: []string{"op1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(time.Minute),
		},
	}

	// op3 (causal remove): remove iss1
	op3 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-test",
			ObjectType: "project",
			OpType:     "remove-issue",
			OpVersion:  1,
			Body:       json.RawMessage(`{"issue":"` + iss1 + `"}`),
		},
		ID:      "op3",
		Parents: []string{"op2"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	// op4 (writer 1): remove iss3 (with op3 as parent)
	op4 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-test",
			ObjectType: "project",
			OpType:     "remove-issue",
			OpVersion:  1,
			Body:       json.RawMessage(`{"issue":"` + iss3 + `"}`),
		},
		ID:      "op4",
		Parents: []string{"op3"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(3 * time.Minute),
		},
	}

	// op5 (writer 2): concurrently add iss3 (with op3 as parent, concurrent with op4)
	op5 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-test",
			ObjectType: "project",
			OpType:     "add-issue",
			OpVersion:  1,
			Body:       json.RawMessage(`{"issue":"` + iss3 + `"}`),
		},
		ID:      "op5",
		Parents: []string{"op3"},
		Author: codec.Identity{
			Email: "bob@example.com",
			When:  now.Add(3 * time.Minute),
		},
	}

	state, err := writ.FoldProject([]codec.Op{op1, op2, op3, op4, op5})
	if err != nil {
		t.Fatalf("FoldProject failed: %v", err)
	}

	// iss1 is causally removed. iss2 remains. iss3 concurrent add wins over remove.
	// Sorted: iss3 ("1111...") < iss2 ("fedc...")
	expectedIssues := []string{iss3, iss2}
	if !reflect.DeepEqual(state.Issues, expectedIssues) {
		t.Errorf("issues mismatch: got %v, want %v", state.Issues, expectedIssues)
	}
}

func TestFoldProjectCrossRepoReferences(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	bareIss := "0123456789abcdef0123456789abcdef"
	qualIss := "repo-a#0123456789abcdef0123456789abcdef"

	op1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-test",
			ObjectType: "project",
			OpType:     "add-issue",
			OpVersion:  1,
			Body:       json.RawMessage(`{"issue":"` + bareIss + `"}`),
		},
		ID:     "op1",
		Author: codec.Identity{Email: "alice@example.com", When: now},
	}

	op2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-test",
			ObjectType: "project",
			OpType:     "add-issue",
			OpVersion:  1,
			Body:       json.RawMessage(`{"issue":"` + qualIss + `"}`),
		},
		ID:      "op2",
		Parents: []string{"op1"},
		Author:  codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
	}

	state, err := writ.FoldProject([]codec.Op{op1, op2})
	if err != nil {
		t.Fatalf("FoldProject failed: %v", err)
	}

	if len(state.Issues) != 2 {
		t.Fatalf("expected 2 distinct issues, got %d", len(state.Issues))
	}
	if state.Issues[0] != bareIss || state.Issues[1] != qualIss {
		t.Errorf("cross-repo issues preserved mismatch: got %v", state.Issues)
	}
}

func TestFoldProjectUnknownOpVersionAndType(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	v1Create := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-compat",
			ObjectType: "project",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"V1 Title"}`),
		},
		ID:     "op-v1",
		Author: codec.Identity{Email: "alice@example.com", When: now},
	}

	v2Update := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-compat",
			ObjectType: "project",
			OpType:     "update",
			OpVersion:  2,
			Body:       json.RawMessage(`{"title":"V2 Should Be Ignored"}`),
		},
		ID:      "op-v2",
		Parents: []string{"op-v1"},
		Author:  codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
	}

	state, err := writ.FoldProject([]codec.Op{v1Create, v2Update})
	if err != nil {
		t.Fatalf("FoldProject failed: %v", err)
	}

	if state.Title != "V1 Title" {
		t.Errorf("title mismatch: got %q, want 'V1 Title'", state.Title)
	}
	if len(state.UnknownOps) != 1 || state.UnknownOps[0].Commit != "op-v2" || state.UnknownOps[0].ObjectType != "project" {
		t.Errorf("unknown_ops mismatch: %+v", state.UnknownOps)
	}
}

func TestFoldProjectMalformedBodyError(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	badOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "p-err",
			ObjectType: "project",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{malformed`),
		},
		ID:     "bad1",
		Author: codec.Identity{When: now},
	}

	_, errFold := writ.Fold([]codec.Op{badOp}, writ.ProjectRules())
	if errFold == nil {
		t.Fatal("expected Fold to error on malformed JSON body, got nil")
	}

	_, errProject := writ.FoldProject([]codec.Op{badOp})
	if errProject == nil {
		t.Fatal("expected FoldProject to error on malformed JSON body, got nil")
	}
}

func TestFoldProjectAgreement(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	ops := []codec.Op{
		{
			ID: "c1",
			Envelope: codec.Envelope{
				ObjectID:   "p-agree",
				ObjectType: "project",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"Project Title","description":"Project Description"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now},
		},
		{
			ID:      "u1",
			Parents: []string{"c1"},
			Envelope: codec.Envelope{
				ObjectID:   "p-agree",
				ObjectType: "project",
				OpType:     "update",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"New Project Title"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
		},
		{
			ID:      "s1",
			Parents: []string{"u1"},
			Envelope: codec.Envelope{
				ObjectID:   "p-agree",
				ObjectType: "project",
				OpType:     "set-status",
				OpVersion:  1,
				Body:       json.RawMessage(`{"status":"completed","reason":"Shipped"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(2 * time.Minute)},
		},
	}

	projectState, err := writ.FoldProject(ops)
	if err != nil {
		t.Fatalf("FoldProject failed: %v", err)
	}

	objectState, err := writ.Fold(ops, writ.ProjectRules())
	if err != nil {
		t.Fatalf("Fold failed: %v", err)
	}

	if projectState.Title != objectState.State["title"] {
		t.Errorf("title mismatch: got %q, want %v", projectState.Title, objectState.State["title"])
	}
	if projectState.Description != objectState.State["description"] {
		t.Errorf("description mismatch: got %q, want %v", projectState.Description, objectState.State["description"])
	}
	if projectState.Status != objectState.State["status"] {
		t.Errorf("status mismatch: got %q, want %v", projectState.Status, objectState.State["status"])
	}
	if projectState.Reason != objectState.State["reason"] {
		t.Errorf("reason mismatch: got %q, want %v", projectState.Reason, objectState.State["reason"])
	}
}
