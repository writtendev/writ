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

func TestIssueRulesDriftGuard(t *testing.T) {
	allRules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules failed: %v", err)
	}

	var expectedRules []s.Rule
	for _, r := range allRules {
		if r.Vocabulary == "issue-ops" {
			expectedRules = append(expectedRules, s.Rule{
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

	builtIn := s.IssueRules()
	if !reflect.DeepEqual(builtIn, expectedRules) {
		t.Fatalf("IssueRules() drifted from published issue-ops field-rules.json:\n got:  %+v\n want: %+v", builtIn, expectedRules)
	}
}

func TestFoldIssueEmpty(t *testing.T) {
	state, err := s.FoldIssue(nil)
	if err != nil {
		t.Fatalf("FoldIssue(nil) returned error: %v", err)
	}
	if !reflect.DeepEqual(state, s.Issue{}) {
		t.Fatalf("expected empty Issue, got %+v", state)
	}
}

func TestFoldIssueLifecycle(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	createOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Initial Title","description":"Initial Description"}`),
		},
		ID: "c1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	updateOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "update",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Updated Title"}`),
		},
		ID:      "u1",
		Parents: []string{"c1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(time.Minute),
		},
	}

	closeOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "set-state",
			OpVersion:  1,
			Body:       json.RawMessage(`{"state":"closed","reason":"completed"}`),
		},
		ID:      "s1",
		Parents: []string{"u1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	reopenOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "set-state",
			OpVersion:  1,
			Body:       json.RawMessage(`{"state":"open","reason":"reopened for further work"}`),
		},
		ID:      "s2",
		Parents: []string{"s1"},
		Author: codec.Identity{
			Email: "bob@example.com",
			When:  now.Add(3 * time.Minute),
		},
	}

	state, err := s.FoldIssue([]codec.Op{createOp, updateOp, closeOp, reopenOp})
	if err != nil {
		t.Fatalf("FoldIssue failed: %v", err)
	}

	if state.Title != "Updated Title" {
		t.Errorf("title mismatch: got %q, want %q", state.Title, "Updated Title")
	}
	if state.Description != "Initial Description" {
		t.Errorf("description mismatch: got %q, want %q", state.Description, "Initial Description")
	}
	if state.State != "open" {
		t.Errorf("state mismatch: got %q, want %q", state.State, "open")
	}
	if state.Reason != "reopened for further work" {
		t.Errorf("reason mismatch: got %q, want %q", state.Reason, "reopened for further work")
	}
}

func TestFoldIssueDefaultStateEmpty(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	createOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Only Created"}`),
		},
		ID: "c1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	state, err := s.FoldIssue([]codec.Op{createOp})
	if err != nil {
		t.Fatalf("FoldIssue failed: %v", err)
	}

	if state.State != "" {
		t.Errorf("expected default state '' (no state), got %q", state.State)
	}
}

func TestFoldIssueOnlyUnknownOps(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	unknownOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "custom-future-op",
			OpVersion:  1,
			Body:       json.RawMessage(`{"foo":"bar"}`),
		},
		ID: "u1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	state, err := s.FoldIssue([]codec.Op{unknownOp})
	if err != nil {
		t.Fatalf("FoldIssue failed: %v", err)
	}

	if state.State != "" {
		t.Errorf("expected empty state when only unknown ops present, got %q", state.State)
	}
	if len(state.UnknownOps) != 1 {
		t.Fatalf("expected 1 unknown op, got %d", len(state.UnknownOps))
	}
}

func TestFoldIssueAssigneesORSetCausalAndConcurrent(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	// Root op: add alice and bob
	op1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "assign",
			OpVersion:  1,
			Body:       json.RawMessage(`{"add":["user:alice","user:bob"]}`),
		},
		ID: "op1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	// Causal remove: remove bob (happens-after op1) -> bob is removed
	op2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "assign",
			OpVersion:  1,
			Body:       json.RawMessage(`{"remove":["user:bob"]}`),
		},
		ID:      "op2",
		Parents: []string{"op1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(time.Minute),
		},
	}

	// Concurrent remove of charlie vs concurrent add of charlie:
	// op3 (writer 1): removes charlie (with op2 as parent)
	op3 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "assign",
			OpVersion:  1,
			Body:       json.RawMessage(`{"remove":["user:charlie"]}`),
		},
		ID:      "op3",
		Parents: []string{"op2"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	// op4 (writer 2): concurrently adds charlie (parent op2, concurrent with op3)
	op4 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "assign",
			OpVersion:  1,
			Body:       json.RawMessage(`{"add":["user:charlie"]}`),
		},
		ID:      "op4",
		Parents: []string{"op2"},
		Author: codec.Identity{
			Email: "bob@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	state, err := s.FoldIssue([]codec.Op{op1, op2, op3, op4})
	if err != nil {
		t.Fatalf("FoldIssue failed: %v", err)
	}

	// Present assignees should be alice and charlie (bob causally removed, charlie concurrent add wins)
	expectedAssignees := []string{"user:alice", "user:charlie"}
	if !reflect.DeepEqual(state.Assignees, expectedAssignees) {
		t.Errorf("assignees mismatch: got %v, want %v", state.Assignees, expectedAssignees)
	}
}

func TestFoldIssueLabelsORSetCausalAndConcurrent(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	op1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "label",
			OpVersion:  1,
			Body:       json.RawMessage(`{"add":["bug","priority/high"]}`),
		},
		ID: "op1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	op2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "label",
			OpVersion:  1,
			Body:       json.RawMessage(`{"remove":["priority/high"]}`),
		},
		ID:      "op2",
		Parents: []string{"op1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(time.Minute),
		},
	}

	state, err := s.FoldIssue([]codec.Op{op1, op2})
	if err != nil {
		t.Fatalf("FoldIssue failed: %v", err)
	}

	expectedLabels := []string{"bug"}
	if !reflect.DeepEqual(state.Labels, expectedLabels) {
		t.Errorf("labels mismatch: got %v, want %v", state.Labels, expectedLabels)
	}
}

func TestFoldIssueLinksAndRetraction(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	link1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "link",
			OpVersion:  1,
			Body:       json.RawMessage(`{"target":"0123456789abcdef0123456789abcdef","target_type":"review","relation":"fixes"}`),
		},
		ID: "l1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	link2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "link",
			OpVersion:  1,
			Body:       json.RawMessage(`{"target":"fedcba9876543210fedcba9876543210","target_type":"issue","relation":"relates"}`),
		},
		ID:      "l2",
		Parents: []string{"l1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(time.Minute),
		},
	}

	// Retract link1
	retract1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "link",
			OpVersion:  1,
			Body:       json.RawMessage(`{"target":"0123456789abcdef0123456789abcdef","relation":"none"}`),
		},
		ID:      "r1",
		Parents: []string{"l2"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	state, err := s.FoldIssue([]codec.Op{link1, link2, retract1})
	if err != nil {
		t.Fatalf("FoldIssue failed: %v", err)
	}

	if len(state.Links) != 1 {
		t.Fatalf("expected 1 active link after retraction, got %d", len(state.Links))
	}

	expectedLink := s.Link{
		Target:     "fedcba9876543210fedcba9876543210",
		TargetType: "issue",
		Relation:   "relates",
	}
	if state.Links[0] != expectedLink {
		t.Errorf("link mismatch: got %+v, want %+v", state.Links[0], expectedLink)
	}
}

func TestFoldIssueCrossRepoLinksPreserved(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	bareTarget := "0123456789abcdef0123456789abcdef"
	qualifiedTarget := "repo-xyz#0123456789abcdef0123456789abcdef"

	linkBare := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "link",
			OpVersion:  1,
			Body:       json.RawMessage(`{"target":"` + bareTarget + `","target_type":"review","relation":"fixes"}`),
		},
		ID: "l-bare",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	linkQual := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-test",
			ObjectType: "issue",
			OpType:     "link",
			OpVersion:  1,
			Body:       json.RawMessage(`{"target":"` + qualifiedTarget + `","target_type":"review","relation":"fixes"}`),
		},
		ID: "l-qual",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(time.Minute),
		},
	}

	state, err := s.FoldIssue([]codec.Op{linkBare, linkQual})
	if err != nil {
		t.Fatalf("FoldIssue failed: %v", err)
	}

	if len(state.Links) != 2 {
		t.Fatalf("expected 2 distinct links for bare and qualified references, got %d", len(state.Links))
	}

	// Links are sorted by target: "0123..." < "repo-xyz#0123..."
	if state.Links[0].Target != bareTarget {
		t.Errorf("expected links[0].Target to be %q, got %q", bareTarget, state.Links[0].Target)
	}
	if state.Links[1].Target != qualifiedTarget {
		t.Errorf("expected links[1].Target to be %q, got %q", qualifiedTarget, state.Links[1].Target)
	}
}

func TestFoldIssueUnknownOpVersionAndType(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	v1Create := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-compat",
			ObjectType: "issue",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"V1 Title","description":"V1 Desc"}`),
		},
		ID: "op-v1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	// Future op_version = 2
	v2Update := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-compat",
			ObjectType: "issue",
			OpType:     "update",
			OpVersion:  2,
			Body:       json.RawMessage(`{"title":"V2 Should Be Ignored"}`),
		},
		ID:      "op-v2",
		Parents: []string{"op-v1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(time.Minute),
		},
	}

	// Unknown op_type = "milestone"
	unknownType := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-compat",
			ObjectType: "issue",
			OpType:     "milestone",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Milestone Should Be Ignored"}`),
		},
		ID:      "op-unknown",
		Parents: []string{"op-v2"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	state, err := s.FoldIssue([]codec.Op{v1Create, v2Update, unknownType})
	if err != nil {
		t.Fatalf("FoldIssue failed: %v", err)
	}

	if state.Title != "V1 Title" {
		t.Errorf("expected title 'V1 Title', got %q", state.Title)
	}
	if state.Description != "V1 Desc" {
		t.Errorf("expected description 'V1 Desc', got %q", state.Description)
	}

	expectedUnknown := []s.UnknownOp{
		{Commit: "op-v2", ObjectType: "issue", OpType: "update", OpVersion: 2},
		{Commit: "op-unknown", ObjectType: "issue", OpType: "milestone", OpVersion: 1},
	}
	if !reflect.DeepEqual(state.UnknownOps, expectedUnknown) {
		t.Errorf("unknown_ops mismatch:\n got:  %+v\n want: %+v", state.UnknownOps, expectedUnknown)
	}
}

func TestFoldIssueMalformedBodyError(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	badOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "i-err",
			ObjectType: "issue",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{invalid json`),
		},
		ID:     "bad1",
		Author: codec.Identity{When: now},
	}

	_, errFold := s.Fold([]codec.Op{badOp}, s.IssueRules())
	if errFold == nil {
		t.Fatal("expected Fold to error on malformed JSON body, got nil")
	}

	_, errIssue := s.FoldIssue([]codec.Op{badOp})
	if errIssue == nil {
		t.Fatal("expected FoldIssue to error on malformed JSON body, got nil")
	}
}

func TestFoldIssueAgreement(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	ops := []codec.Op{
		{
			ID: "c1",
			Envelope: codec.Envelope{
				ObjectID:   "i-agree",
				ObjectType: "issue",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"Initial Title","description":"Initial Description"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now},
		},
		{
			ID:      "u1",
			Parents: []string{"c1"},
			Envelope: codec.Envelope{
				ObjectID:   "i-agree",
				ObjectType: "issue",
				OpType:     "update",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"Updated Title"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
		},
		{
			ID:      "s1",
			Parents: []string{"u1"},
			Envelope: codec.Envelope{
				ObjectID:   "i-agree",
				ObjectType: "issue",
				OpType:     "set-state",
				OpVersion:  1,
				Body:       json.RawMessage(`{"state":"closed","reason":"resolved"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(2 * time.Minute)},
		},
		{
			ID:      "l1",
			Parents: []string{"s1"},
			Envelope: codec.Envelope{
				ObjectID:   "i-agree",
				ObjectType: "issue",
				OpType:     "link",
				OpVersion:  1,
				Body:       json.RawMessage(`{"target":"0123456789abcdef0123456789abcdef","target_type":"review","relation":"fixes"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(3 * time.Minute)},
		},
	}

	issueState, err := s.FoldIssue(ops)
	if err != nil {
		t.Fatalf("FoldIssue failed: %v", err)
	}

	objectState, err := s.Fold(ops, s.IssueRules())
	if err != nil {
		t.Fatalf("Fold failed: %v", err)
	}

	if issueState.Title != objectState.State["title"] {
		t.Errorf("title mismatch: got %q, want %v", issueState.Title, objectState.State["title"])
	}
	if issueState.Description != objectState.State["description"] {
		t.Errorf("description mismatch: got %q, want %v", issueState.Description, objectState.State["description"])
	}
	if issueState.State != objectState.State["state"] {
		t.Errorf("state mismatch: got %q, want %v", issueState.State, objectState.State["state"])
	}
	if issueState.Reason != objectState.State["reason"] {
		t.Errorf("reason mismatch: got %q, want %v", issueState.Reason, objectState.State["reason"])
	}

	if len(issueState.Links) != 1 {
		t.Fatalf("expected 1 link in FoldIssue, got %d", len(issueState.Links))
	}
	if issueState.Links[0].Target != "0123456789abcdef0123456789abcdef" || issueState.Links[0].Relation != "fixes" {
		t.Errorf("link mismatch: %+v", issueState.Links[0])
	}
}

func TestFoldIssuePersonNormalization(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	// 1. Assign with whitespace and mixed case in both halves:
	//    "  Email:Alice@Example.COM  ", "EMAIL:Bob@Example.Com"
	// 2. Remove normalized: "email:alice@example.com", add " email:Charlie@Example.COM "
	ops := []codec.Op{
		{
			ID: "op1",
			Envelope: codec.Envelope{
				ObjectID:   "i-norm",
				ObjectType: "issue",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"Normalization Issue"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now},
		},
		{
			ID:      "op2",
			Parents: []string{"op1"},
			Envelope: codec.Envelope{
				ObjectID:   "i-norm",
				ObjectType: "issue",
				OpType:     "assign",
				OpVersion:  1,
				Body:       json.RawMessage(`{"add":["  Email:Alice@Example.COM  ", "EMAIL:Bob@Example.Com"]}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
		},
		{
			ID:      "op3",
			Parents: []string{"op2"},
			Envelope: codec.Envelope{
				ObjectID:   "i-norm",
				ObjectType: "issue",
				OpType:     "assign",
				OpVersion:  1,
				Body:       json.RawMessage(`{"remove":["email:alice@example.com"],"add":[" email:Charlie@Example.COM "]}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(2 * time.Minute)},
		},
	}

	state, err := s.FoldIssue(ops)
	if err != nil {
		t.Fatalf("FoldIssue failed: %v", err)
	}

	// Assignees: alice removed, bob and charlie present (lowercase, sorted)
	wantAssignees := []string{"email:bob@example.com", "email:charlie@example.com"}
	if !reflect.DeepEqual(state.Assignees, wantAssignees) {
		t.Errorf("Assignees = %v, want %v", state.Assignees, wantAssignees)
	}
}

func TestFoldIssueOrSetBodyShapes(t *testing.T) {
	labelOp := func(id, parent, body string, when int64) codec.Op {
		op := codec.Op{
			Envelope: codec.Envelope{
				ObjectID:   "i-shapes",
				ObjectType: "issue",
				OpType:     "label",
				OpVersion:  1,
				Body:       json.RawMessage(body),
			},
			ID:     id,
			Author: codec.Identity{When: time.Unix(when, 0).UTC()},
		}
		if parent != "" {
			op.Parents = []string{parent}
		}
		return op
	}

	ops := []codec.Op{
		labelOp("l1", "", `{"add":["urgent","triage"]}`, 100),
		labelOp("l2", "l1", `{"add":"solo"}`, 200),
		labelOp("l3", "l2", `{"add":null}`, 300),
		labelOp("l4", "l3", `{"add":["never"],"remove":["triage",7]}`, 400),
		labelOp("l5", "l4", `{"add":["kept"],"remove":["urgent"]}`, 500),
		labelOp("l6-remove-only", "l5", `{"remove":["solo"]}`, 600),
	}

	issue, err := s.FoldIssue(ops)
	if err != nil {
		t.Fatalf("FoldIssue: %v", err)
	}

	want := []string{"kept", "triage"}
	if !reflect.DeepEqual(issue.Labels, want) {
		t.Errorf("Labels = %v, want %v", issue.Labels, want)
	}

	wantQuarantined := []string{"l3", "l4"}
	var got []string
	for _, u := range issue.UnknownOps {
		got = append(got, u.Commit)
	}
	if !reflect.DeepEqual(got, wantQuarantined) {
		t.Errorf("quarantined = %v, want %v", got, wantQuarantined)
	}

	generic, err := s.Fold(ops, s.IssueRules())
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if !reflect.DeepEqual(generic.State["labels"], want) {
		t.Errorf("generic labels = %v, want %v", generic.State["labels"], want)
	}
}

func TestFoldIssueNestedShapeAtFlatField(t *testing.T) {
	labelOp := func(id, parent, body string, when int64) codec.Op {
		op := codec.Op{
			Envelope: codec.Envelope{
				ObjectID:   "i-nested",
				ObjectType: "issue",
				OpType:     "label",
				OpVersion:  1,
				Body:       json.RawMessage(body),
			},
			ID:     id,
			Author: codec.Identity{When: time.Unix(when, 0).UTC()},
		}
		if parent != "" {
			op.Parents = []string{parent}
		}
		return op
	}

	ops := []codec.Op{
		labelOp("l1", "", `{"add":{"add":["feature","urgent"],"remove":[]}}`, 100),
		labelOp("l2", "l1", `{"add":{"remove":["urgent"]}}`, 200),
		labelOp("l3", "l2", `{"remove":{"add":["bug"],"remove":["feature"]}}`, 300),
	}

	issue, err := s.FoldIssue(ops)
	if err != nil {
		t.Fatalf("FoldIssue: %v", err)
	}

	want := []string{"bug"}
	if !reflect.DeepEqual(issue.Labels, want) {
		t.Errorf("Labels = %v, want %v", issue.Labels, want)
	}

	generic, err := s.Fold(ops, s.IssueRules())
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if !reflect.DeepEqual(generic.State["labels"], want) {
		t.Errorf("generic labels = %v, want %v", generic.State["labels"], want)
	}
}

func TestFoldIssueMixedFlatAndNestedShapes(t *testing.T) {
	labelOp := func(id, parent, body string, when int64) codec.Op {
		op := codec.Op{
			Envelope: codec.Envelope{
				ObjectID:   "i-mixed",
				ObjectType: "issue",
				OpType:     "label",
				OpVersion:  1,
				Body:       json.RawMessage(body),
			},
			ID:     id,
			Author: codec.Identity{When: time.Unix(when, 0).UTC()},
		}
		if parent != "" {
			op.Parents = []string{parent}
		}
		return op
	}

	ops := []codec.Op{
		labelOp("l1", "", `{"add":"feature","remove":{"remove":["old"]}}`, 100),
		labelOp("l2", "l1", `{"add":{"add":["bug"]},"remove":"temporary"}`, 200),
		labelOp("l3", "l2", `{"add":{"add":["urgent"],"remove":["feature"]},"remove":{"add":["security"],"remove":["bug"]}}`, 300),
	}

	issue, err := s.FoldIssue(ops)
	if err != nil {
		t.Fatalf("FoldIssue: %v", err)
	}

	want := []string{"security", "urgent"}
	if !reflect.DeepEqual(issue.Labels, want) {
		t.Errorf("Labels = %v, want %v", issue.Labels, want)
	}

	generic, err := s.Fold(ops, s.IssueRules())
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if !reflect.DeepEqual(generic.State["labels"], want) {
		t.Errorf("generic labels = %v, want %v", generic.State["labels"], want)
	}
}

// TestFoldIssueGenericMatchesTypedWithOverlappingAssigneeAndLabel is
// WRIT-198's behavioural regression test, the issue counterpart to
// engine/state/review_test.go's
// TestFoldReviewGenericMatchesTypedWithOverlappingAssigneeAndLabel. Before
// the fix, assign.{add,remove} and label.{add,remove} declared no target,
// so both pairs fell back to the shared body field names "add"/"remove":
// the generic fold merged an issue's assignees and labels into one set. An
// assignee and a label sharing the identical underlying string is exactly
// the case a shared, undiscriminating target cannot represent.
func TestFoldIssueGenericMatchesTypedWithOverlappingAssigneeAndLabel(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	const shared = "shared-item"

	ops := []codec.Op{
		{
			ID: "op-create",
			Envelope: codec.Envelope{
				ObjectID:   "iss-overlap",
				ObjectType: "issue",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"Overlap Test"}`),
			},
			Author: codec.Identity{When: now},
		},
		{
			ID:      "op-assign",
			Parents: []string{"op-create"},
			Envelope: codec.Envelope{
				ObjectID:   "iss-overlap",
				ObjectType: "issue",
				OpType:     "assign",
				OpVersion:  1,
				Body:       json.RawMessage(`{"add":["` + shared + `"]}`),
			},
			Author: codec.Identity{When: now.Add(time.Minute)},
		},
		{
			ID:      "op-label",
			Parents: []string{"op-assign"},
			Envelope: codec.Envelope{
				ObjectID:   "iss-overlap",
				ObjectType: "issue",
				OpType:     "label",
				OpVersion:  1,
				Body:       json.RawMessage(`{"add":["` + shared + `"]}`),
			},
			Author: codec.Identity{When: now.Add(2 * time.Minute)},
		},
	}

	typed, err := s.FoldIssue(ops)
	if err != nil {
		t.Fatalf("FoldIssue: %v", err)
	}
	wantSet := []string{shared}
	if !reflect.DeepEqual(typed.Assignees, wantSet) {
		t.Fatalf("typed Assignees = %v, want %v", typed.Assignees, wantSet)
	}
	if !reflect.DeepEqual(typed.Labels, wantSet) {
		t.Fatalf("typed Labels = %v, want %v", typed.Labels, wantSet)
	}

	generic, err := s.Fold(ops, s.IssueRules())
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if !reflect.DeepEqual(generic.State["assignees"], wantSet) {
		t.Errorf("generic assignees = %v, want %v", generic.State["assignees"], wantSet)
	}
	if !reflect.DeepEqual(generic.State["labels"], wantSet) {
		t.Errorf("generic labels = %v, want %v", generic.State["labels"], wantSet)
	}
}
