package state_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
	s "github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec"
)

func TestReviewRulesDriftGuard(t *testing.T) {
	allRules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules failed: %v", err)
	}

	var expectedRules []s.Rule
	for _, r := range allRules {
		if r.Vocabulary == "review-ops" {
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

	builtIn := s.ReviewRules()
	if !reflect.DeepEqual(builtIn, expectedRules) {
		t.Fatalf("ReviewRules() drifted from published review-ops field-rules.json:\n got:  %+v\n want: %+v", builtIn, expectedRules)
	}
}

func TestFoldReviewEmpty(t *testing.T) {
	state, err := s.FoldReview(nil)
	if err != nil {
		t.Fatalf("FoldReview(nil) returned error: %v", err)
	}
	if !reflect.DeepEqual(state, s.Review{}) {
		t.Fatalf("expected empty Review, got %+v", state)
	}
}

func TestFoldReviewApprovalsAndRetraction(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	op1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-test",
			ObjectType: "review",
			OpType:     "approval",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","verdict":"approve","subject":"user:alice","message":"looks good"}`),
		},
		ID: "op1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	op2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-test",
			ObjectType: "review",
			OpType:     "approval",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"2222222222222222222222222222222222222222","verdict":"request-changes","subject":"user:bob","message":"need tests"}`),
		},
		ID: "op2",
		Author: codec.Identity{
			Email: "bob@example.com",
			When:  now.Add(time.Minute),
		},
	}

	// Alice retracts her verdict on rev 1
	op3 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-test",
			ObjectType: "review",
			OpType:     "approval",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","verdict":"none","subject":"user:alice","message":"retracted"}`),
		},
		ID:      "op3",
		Parents: []string{"op1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	state, err := s.FoldReview([]codec.Op{op1, op2, op3})
	if err != nil {
		t.Fatalf("FoldReview failed: %v", err)
	}

	if len(state.Approvals) != 1 {
		t.Fatalf("expected 1 active approval after retraction, got %d: %+v", len(state.Approvals), state.Approvals)
	}

	if state.Approvals[0].Subject != "user:bob" || state.Approvals[0].Revision != "2222222222222222222222222222222222222222" || state.Approvals[0].Verdict != "request-changes" {
		t.Errorf("approval mismatch: got %+v", state.Approvals[0])
	}
}

func TestFoldReviewApprovalMessageResetOnSubsequentVerdict(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	const rev = "1111111111111111111111111111111111111111"

	t.Run("omitted message preserves existing message under keyed-lww", func(t *testing.T) {
		op1 := codec.Op{
			ID: "op1",
			Envelope: codec.Envelope{
				ObjectID:   "r-msg",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"` + rev + `","verdict":"request-changes","subject":"user:alice","message":"Please fix tests before merging"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now},
		}
		op2 := codec.Op{
			ID:      "op2",
			Parents: []string{"op1"},
			Envelope: codec.Envelope{
				ObjectID:   "r-msg",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"` + rev + `","verdict":"approve","subject":"user:alice"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
		}

		state, err := s.FoldReview([]codec.Op{op1, op2})
		if err != nil {
			t.Fatalf("FoldReview failed: %v", err)
		}
		if len(state.Approvals) != 1 {
			t.Fatalf("expected 1 active approval, got %d", len(state.Approvals))
		}
		if state.Approvals[0].Verdict != "approve" {
			t.Errorf("verdict = %q, want %q", state.Approvals[0].Verdict, "approve")
		}
		if state.Approvals[0].Message != "Please fix tests before merging" {
			t.Errorf("expected preserved message %q after subsequent approve omitting message, got %q", "Please fix tests before merging", state.Approvals[0].Message)
		}
	})

	t.Run("explicit new comment updates message", func(t *testing.T) {
		op1 := codec.Op{
			ID: "op1",
			Envelope: codec.Envelope{
				ObjectID:   "r-msg",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"` + rev + `","verdict":"request-changes","subject":"user:alice","message":"Please fix tests before merging"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now},
		}
		op2 := codec.Op{
			ID:      "op2",
			Parents: []string{"op1"},
			Envelope: codec.Envelope{
				ObjectID:   "r-msg",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"` + rev + `","verdict":"approve","subject":"user:alice","message":"Tests look great now"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
		}

		state, err := s.FoldReview([]codec.Op{op1, op2})
		if err != nil {
			t.Fatalf("FoldReview failed: %v", err)
		}
		if len(state.Approvals) != 1 {
			t.Fatalf("expected 1 active approval, got %d", len(state.Approvals))
		}
		if state.Approvals[0].Verdict != "approve" {
			t.Errorf("verdict = %q, want %q", state.Approvals[0].Verdict, "approve")
		}
		if state.Approvals[0].Message != "Tests look great now" {
			t.Errorf("expected updated message %q, got %q", "Tests look great now", state.Approvals[0].Message)
		}
	})

	t.Run("empty string message resets to empty", func(t *testing.T) {
		op1 := codec.Op{
			ID: "op1",
			Envelope: codec.Envelope{
				ObjectID:   "r-msg",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"` + rev + `","verdict":"request-changes","subject":"user:alice","message":"Please fix tests before merging"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now},
		}
		op2 := codec.Op{
			ID:      "op2",
			Parents: []string{"op1"},
			Envelope: codec.Envelope{
				ObjectID:   "r-msg",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"` + rev + `","verdict":"approve","subject":"user:alice","message":""}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
		}

		state, err := s.FoldReview([]codec.Op{op1, op2})
		if err != nil {
			t.Fatalf("FoldReview failed: %v", err)
		}
		if len(state.Approvals) != 1 {
			t.Fatalf("expected 1 active approval, got %d", len(state.Approvals))
		}
		if state.Approvals[0].Verdict != "approve" {
			t.Errorf("verdict = %q, want %q", state.Approvals[0].Verdict, "approve")
		}
		if state.Approvals[0].Message != "" {
			t.Errorf("expected empty message after subsequent approve with message: \"\", got %q", state.Approvals[0].Message)
		}
	})
}

func TestFoldReviewRevisionsPairing(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	rev1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-rev",
			ObjectType: "review",
			OpType:     "revision",
			OpVersion:  1,
			Body:       json.RawMessage(`{"base":"0123456789abcdef0123456789abcdef01234567","head":"1111111111111111111111111111111111111111"}`),
		},
		ID:     "rev1",
		Author: codec.Identity{When: now},
	}

	rev2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-rev",
			ObjectType: "review",
			OpType:     "revision",
			OpVersion:  1,
			Body:       json.RawMessage(`{"base":"0123456789abcdef0123456789abcdef01234567","head":"2222222222222222222222222222222222222222"}`),
		},
		ID:      "rev2",
		Parents: []string{"rev1"},
		Author:  codec.Identity{When: now.Add(time.Minute)},
	}

	state, err := s.FoldReview([]codec.Op{rev1, rev2})
	if err != nil {
		t.Fatalf("FoldReview failed: %v", err)
	}

	if len(state.Revisions) != 2 {
		t.Fatalf("expected 2 revisions, got %d", len(state.Revisions))
	}
	if state.Revisions[0].Base != "0123456789abcdef0123456789abcdef01234567" || state.Revisions[0].Head != "1111111111111111111111111111111111111111" {
		t.Errorf("rev[0] mismatch: %+v", state.Revisions[0])
	}
	if state.Revisions[1].Base != "0123456789abcdef0123456789abcdef01234567" || state.Revisions[1].Head != "2222222222222222222222222222222222222222" {
		t.Errorf("rev[1] mismatch: %+v", state.Revisions[1])
	}
}

func TestFoldReviewCIStatuses(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	ci1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-ci",
			ObjectType: "review",
			OpType:     "ci-status",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","name":"ci/test","state":"pending","url":"https://ci.example.com/1"}`),
		},
		ID:     "ci1",
		Author: codec.Identity{When: now},
	}

	ci2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-ci",
			ObjectType: "review",
			OpType:     "ci-status",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","name":"ci/test","state":"success","url":"https://ci.example.com/1","description":"passed","started_at":"2026-01-01T00:00:00Z","completed_at":"2026-01-01T00:01:00Z","external_id":"ext-1"}`),
		},
		ID:      "ci2",
		Parents: []string{"ci1"},
		Author:  codec.Identity{When: now.Add(time.Minute)},
	}

	ciLint := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-ci",
			ObjectType: "review",
			OpType:     "ci-status",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","name":"ci/lint","state":"success"}`),
		},
		ID:     "ciLint",
		Author: codec.Identity{When: now},
	}

	state, err := s.FoldReview([]codec.Op{ci1, ci2, ciLint})
	if err != nil {
		t.Fatalf("FoldReview failed: %v", err)
	}

	if len(state.CIStatuses) != 2 {
		t.Fatalf("expected 2 ci statuses, got %d", len(state.CIStatuses))
	}
	// Deterministically sorted by (revision, name): ci/lint < ci/test
	if state.CIStatuses[0].Name != "ci/lint" || state.CIStatuses[0].State != "success" {
		t.Errorf("ci[0] mismatch: %+v", state.CIStatuses[0])
	}
	if state.CIStatuses[1].Name != "ci/test" || state.CIStatuses[1].State != "success" || state.CIStatuses[1].Description != "passed" || state.CIStatuses[1].ExternalID != "ext-1" {
		t.Errorf("ci[1] mismatch: %+v", state.CIStatuses[1])
	}
}

func TestFoldReviewMalformedBodyError(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	badOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-err",
			ObjectType: "review",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{not valid json`),
		},
		ID:     "bad1",
		Author: codec.Identity{When: now},
	}

	_, errFold := s.Fold([]codec.Op{badOp}, s.ReviewRules())
	if errFold == nil {
		t.Fatal("expected Fold to error on malformed JSON body, got nil")
	}

	_, errReview := s.FoldReview([]codec.Op{badOp})
	if errReview == nil {
		t.Fatal("expected FoldReview to error on malformed JSON body, got nil")
	}
}

func TestFoldReviewMergedToOpenTolerance(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	createOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-merge",
			ObjectType: "review",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Initial Review"}`),
		},
		ID: "c1",
		Author: codec.Identity{
			Email: "author@example.com",
			When:  now,
		},
	}

	mergedOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-merge",
			ObjectType: "review",
			OpType:     "set-status",
			OpVersion:  1,
			Body:       json.RawMessage(`{"status":"merged","merge_commit":"abcdef0123456789abcdef0123456789abcdef01"}`),
		},
		ID:      "m1",
		Parents: []string{"c1"},
		Author: codec.Identity{
			Email: "author@example.com",
			When:  now.Add(time.Minute),
		},
	}

	reopenOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-merge",
			ObjectType: "review",
			OpType:     "set-status",
			OpVersion:  1,
			Body:       json.RawMessage(`{"status":"open","reason":"accidental merge"}`),
		},
		ID:      "r1",
		Parents: []string{"m1"},
		Author: codec.Identity{
			Email: "author@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	state, err := s.FoldReview([]codec.Op{createOp, mergedOp, reopenOp})
	if err != nil {
		t.Fatalf("FoldReview failed to tolerate merged -> open transition: %v", err)
	}

	if state.Status != "open" {
		t.Errorf("expected status 'open', got %q", state.Status)
	}
	if state.Reason != "accidental merge" {
		t.Errorf("expected reason 'accidental merge', got %q", state.Reason)
	}
	if state.MergeCommit != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("expected merge_commit to be preserved, got %q", state.MergeCommit)
	}
}

func TestFoldReviewSetStatusOmitsMergeCommit(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	status1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-status",
			ObjectType: "review",
			OpType:     "set-status",
			OpVersion:  1,
			Body:       json.RawMessage(`{"status":"merged","merge_commit":"1111111111111111111111111111111111111111","reason":"Landed"}`),
		},
		ID: "s1",
		Author: codec.Identity{
			Email: "author@example.com",
			When:  now,
		},
	}

	status2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-status",
			ObjectType: "review",
			OpType:     "set-status",
			OpVersion:  1,
			Body:       json.RawMessage(`{"status":"closed","reason":"Superseded"}`),
		},
		ID:      "s2",
		Parents: []string{"s1"},
		Author: codec.Identity{
			Email: "author@example.com",
			When:  now.Add(time.Minute),
		},
	}

	state, err := s.FoldReview([]codec.Op{status1, status2})
	if err != nil {
		t.Fatalf("FoldReview failed: %v", err)
	}

	if state.Status != "closed" {
		t.Errorf("expected status 'closed', got %q", state.Status)
	}
	if state.Reason != "Superseded" {
		t.Errorf("expected reason 'Superseded', got %q", state.Reason)
	}
	if state.MergeCommit != "1111111111111111111111111111111111111111" {
		t.Errorf("expected merge_commit preserved, got %q", state.MergeCommit)
	}
}

func TestFoldReviewUnknownOpVersionAndType(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	v1Create := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-compat",
			ObjectType: "review",
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
			ObjectID:   "r-compat",
			ObjectType: "review",
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

	// Unknown op_type = "archive"
	unknownType := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-compat",
			ObjectType: "review",
			OpType:     "archive",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Archive Should Be Ignored"}`),
		},
		ID:      "op-unknown",
		Parents: []string{"op-v2"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	state, err := s.FoldReview([]codec.Op{v1Create, v2Update, unknownType})
	if err != nil {
		t.Fatalf("FoldReview failed: %v", err)
	}

	if state.Title != "V1 Title" {
		t.Errorf("expected title 'V1 Title', got %q", state.Title)
	}
	if state.Description != "V1 Desc" {
		t.Errorf("expected description 'V1 Desc', got %q", state.Description)
	}

	expectedUnknown := []s.UnknownOp{
		{Commit: "op-v2", ObjectType: "review", OpType: "update", OpVersion: 2},
		{Commit: "op-unknown", ObjectType: "review", OpType: "archive", OpVersion: 1},
	}
	if !reflect.DeepEqual(state.UnknownOps, expectedUnknown) {
		t.Errorf("unknown_ops mismatch:\n got:  %+v\n want: %+v", state.UnknownOps, expectedUnknown)
	}
}

func TestFoldReviewAgreement(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	ops := []codec.Op{
		{
			ID: "c1",
			Envelope: codec.Envelope{
				ObjectID:   "r-agree",
				ObjectType: "review",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"Initial Title","description":"Initial Description"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now},
		},
		{
			ID:      "r1",
			Parents: []string{"c1"},
			Envelope: codec.Envelope{
				ObjectID:   "r-agree",
				ObjectType: "review",
				OpType:     "revision",
				OpVersion:  1,
				Body:       json.RawMessage(`{"base":"0123456789abcdef0123456789abcdef01234567","head":"1111111111111111111111111111111111111111"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
		},
		{
			ID:      "u1",
			Parents: []string{"r1"},
			Envelope: codec.Envelope{
				ObjectID:   "r-agree",
				ObjectType: "review",
				OpType:     "update",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"Updated Title"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(2 * time.Minute)},
		},
		{
			ID:      "s1",
			Parents: []string{"u1"},
			Envelope: codec.Envelope{
				ObjectID:   "r-agree",
				ObjectType: "review",
				OpType:     "set-status",
				OpVersion:  1,
				Body:       json.RawMessage(`{"status":"open","reason":"Ready"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(3 * time.Minute)},
		},
		{
			ID:      "app1",
			Parents: []string{"s1"},
			Envelope: codec.Envelope{
				ObjectID:   "r-agree",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","verdict":"approve","subject":"user:bob","message":"LGTM"}`),
			},
			Author: codec.Identity{Email: "bob@example.com", When: now.Add(4 * time.Minute)},
		},
		{
			ID:      "app2",
			Parents: []string{"s1"},
			Envelope: codec.Envelope{
				ObjectID:   "r-agree",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","verdict":"approve","subject":"user:alice"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(4 * time.Minute)},
		},
		{
			ID:      "app2-retract",
			Parents: []string{"app2"},
			Envelope: codec.Envelope{
				ObjectID:   "r-agree",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","verdict":"none","subject":"user:alice"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(5 * time.Minute)},
		},
		{
			ID:      "ci1",
			Parents: []string{"s1"},
			Envelope: codec.Envelope{
				ObjectID:   "r-agree",
				ObjectType: "review",
				OpType:     "ci-status",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","name":"ci/lint","state":"success"}`),
			},
			Author: codec.Identity{Email: "ci@example.com", When: now.Add(4 * time.Minute)},
		},
	}

	reviewState, err := s.FoldReview(ops)
	if err != nil {
		t.Fatalf("FoldReview failed: %v", err)
	}

	objectState, err := s.Fold(ops, s.ReviewRules())
	if err != nil {
		t.Fatalf("Fold failed: %v", err)
	}

	if reviewState.Title != objectState.State["title"] {
		t.Errorf("title mismatch: got %q, want %v", reviewState.Title, objectState.State["title"])
	}
	if reviewState.Description != objectState.State["description"] {
		t.Errorf("description mismatch: got %q, want %v", reviewState.Description, objectState.State["description"])
	}
	if reviewState.Status != objectState.State["status"] {
		t.Errorf("status mismatch: got %q, want %v", reviewState.Status, objectState.State["status"])
	}
	if reviewState.Reason != objectState.State["reason"] {
		t.Errorf("reason mismatch: got %q, want %v", reviewState.Reason, objectState.State["reason"])
	}

	baseList, ok := objectState.State["base"].([]any)
	if !ok || len(baseList) != len(reviewState.Revisions) {
		t.Fatalf("base list mismatch: got %v, want %d revisions", baseList, len(reviewState.Revisions))
	}
	for i, rev := range reviewState.Revisions {
		if rev.Base != baseList[i] {
			t.Errorf("revision[%d].Base mismatch: got %q, want %v", i, rev.Base, baseList[i])
		}
	}

	// Approvals: Bob active, Alice retracted
	if len(reviewState.Approvals) != 1 {
		t.Fatalf("expected 1 active approval in FoldReview, got %d", len(reviewState.Approvals))
	}
	if reviewState.Approvals[0].Subject != "user:bob" || reviewState.Approvals[0].Verdict != "approve" {
		t.Errorf("unexpected active approval: %+v", reviewState.Approvals[0])
	}

	// CI Statuses: ci/lint success
	if len(reviewState.CIStatuses) != 1 {
		t.Fatalf("expected 1 active ci status in FoldReview, got %d", len(reviewState.CIStatuses))
	}
	if reviewState.CIStatuses[0].Name != "ci/lint" || reviewState.CIStatuses[0].State != "success" {
		t.Errorf("unexpected active ci status: %+v", reviewState.CIStatuses[0])
	}
}

func TestFoldReviewAssign(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	// Initial create
	op1 := codec.Op{
		ID: "op1",
		Envelope: codec.Envelope{
			ObjectID:   "r-assign",
			ObjectType: "review",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Review with Assignees"}`),
		},
		Author: codec.Identity{Email: "alice@example.com", When: now},
	}

	// Assign alice and bob
	op2 := codec.Op{
		ID:      "op2",
		Parents: []string{"op1"},
		Envelope: codec.Envelope{
			ObjectID:   "r-assign",
			ObjectType: "review",
			OpType:     "assign",
			OpVersion:  1,
			Body:       json.RawMessage(`{"add":["user:alice","user:bob"]}`),
		},
		Author: codec.Identity{Email: "alice@example.com", When: now.Add(time.Minute)},
	}

	// Causal remove bob, add charlie
	op3 := codec.Op{
		ID:      "op3",
		Parents: []string{"op2"},
		Envelope: codec.Envelope{
			ObjectID:   "r-assign",
			ObjectType: "review",
			OpType:     "assign",
			OpVersion:  1,
			Body:       json.RawMessage(`{"remove":["user:bob"],"add":["user:charlie"]}`),
		},
		Author: codec.Identity{Email: "alice@example.com", When: now.Add(2 * time.Minute)},
	}

	// Concurrent op removing charlie from op1 (before charlie was added in op3)
	op4 := codec.Op{
		ID:      "op4",
		Parents: []string{"op1"},
		Envelope: codec.Envelope{
			ObjectID:   "r-assign",
			ObjectType: "review",
			OpType:     "assign",
			OpVersion:  1,
			Body:       json.RawMessage(`{"remove":["user:charlie"]}`),
		},
		Author: codec.Identity{Email: "bob@example.com", When: now.Add(3 * time.Minute)},
	}

	state, err := s.FoldReview([]codec.Op{op1, op2, op3, op4})
	if err != nil {
		t.Fatalf("FoldReview failed: %v", err)
	}

	// Alice and Charlie should be present (charlie add in op3 wins over concurrent remove in op4; bob removed causally)
	wantAssignees := []string{"user:alice", "user:charlie"}
	if !reflect.DeepEqual(state.Assignees, wantAssignees) {
		t.Fatalf("assignees mismatch: got %v, want %v", state.Assignees, wantAssignees)
	}
}

func TestFoldReviewLabelsORSetCausalAndConcurrent(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	op1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-test",
			ObjectType: "review",
			OpType:     "label",
			OpVersion:  1,
			Body:       json.RawMessage(`{"add":["area/engine","priority/high"]}`),
		},
		ID: "op1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	op2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-test",
			ObjectType: "review",
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

	state, err := s.FoldReview([]codec.Op{op1, op2})
	if err != nil {
		t.Fatalf("FoldReview failed: %v", err)
	}

	expectedLabels := []string{"area/engine"}
	if !reflect.DeepEqual(state.Labels, expectedLabels) {
		t.Errorf("labels mismatch: got %v, want %v", state.Labels, expectedLabels)
	}
}

func TestFoldReviewLinksAndRetraction(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	link1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-test",
			ObjectType: "review",
			OpType:     "link",
			OpVersion:  1,
			Body:       json.RawMessage(`{"target":"0123456789abcdef0123456789abcdef","target_type":"issue","relation":"fixes"}`),
		},
		ID: "l1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	link2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-test",
			ObjectType: "review",
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
			ObjectID:   "r-test",
			ObjectType: "review",
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

	state, err := s.FoldReview([]codec.Op{link1, link2, retract1})
	if err != nil {
		t.Fatalf("FoldReview failed: %v", err)
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

func TestFoldReviewPersonNormalization(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	// 1. Assign with whitespace and mixed case in both halves:
	//    "  Email:Alice@Example.COM  ", "EMAIL:Bob@Example.Com"
	// 2. Remove normalized: "email:alice@example.com", add " email:Charlie@Example.COM "
	// 3. Approval with whitespace and mixed case: "  Email:Carol@Example.COM  ", verdict "approve"
	// 4. Approval update normalized: "email:carol@example.com", verdict "request-changes"
	ops := []codec.Op{
		{
			ID: "op1",
			Envelope: codec.Envelope{
				ObjectID:   "r-norm",
				ObjectType: "review",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"title":"Normalization Review"}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now},
		},
		{
			ID:      "op2",
			Parents: []string{"op1"},
			Envelope: codec.Envelope{
				ObjectID:   "r-norm",
				ObjectType: "review",
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
				ObjectID:   "r-norm",
				ObjectType: "review",
				OpType:     "assign",
				OpVersion:  1,
				Body:       json.RawMessage(`{"remove":["email:alice@example.com"],"add":[" email:Charlie@Example.COM "]}`),
			},
			Author: codec.Identity{Email: "alice@example.com", When: now.Add(2 * time.Minute)},
		},
		{
			ID:      "op4",
			Parents: []string{"op3"},
			Envelope: codec.Envelope{
				ObjectID:   "r-norm",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","verdict":"approve","subject":"  Email:Carol@Example.COM  "}`),
			},
			Author: codec.Identity{Email: "carol@example.com", When: now.Add(3 * time.Minute)},
		},
		{
			ID:      "op5",
			Parents: []string{"op4"},
			Envelope: codec.Envelope{
				ObjectID:   "r-norm",
				ObjectType: "review",
				OpType:     "approval",
				OpVersion:  1,
				Body:       json.RawMessage(`{"revision":"1111111111111111111111111111111111111111","verdict":"request-changes","subject":"email:carol@example.com"}`),
			},
			Author: codec.Identity{Email: "carol@example.com", When: now.Add(4 * time.Minute)},
		},
	}

	state, err := s.FoldReview(ops)
	if err != nil {
		t.Fatalf("FoldReview failed: %v", err)
	}

	// Assignees: alice removed, bob and charlie present (lowercase, sorted)
	wantAssignees := []string{"email:bob@example.com", "email:charlie@example.com"}
	if !reflect.DeepEqual(state.Assignees, wantAssignees) {
		t.Errorf("Assignees = %v, want %v", state.Assignees, wantAssignees)
	}

	// Approvals: email:carol@example.com single entry with updated verdict
	if len(state.Approvals) != 1 {
		t.Fatalf("Approvals count = %d, want 1", len(state.Approvals))
	}
	if state.Approvals[0].Subject != "email:carol@example.com" {
		t.Errorf("Approval Subject = %q, want %q", state.Approvals[0].Subject, "email:carol@example.com")
	}
	if state.Approvals[0].Verdict != "request-changes" {
		t.Errorf("Approval Verdict = %q, want %q", state.Approvals[0].Verdict, "request-changes")
	}
}

// TestFoldReviewOrSetBodyShapes pins the typed reducer against the generic
// fold on the OR-set body shapes spec/fold.md §5.4 declares for the flat shape,
// which is the one review `label` and `assign` operations actually use.
//
// Two of these were disagreements. A side holding a bare string is one item,
// and the reducers read only arrays, so `{"add": "solo"}` was accepted by the
// uninterpretability check and then folded to nothing — the silent drop §7.1's
// rationale rejects. And a side that is present holding `null` is a write
// claimed with no value in it: it is uninterpretable, not an absent side, or
// `{"add": null}` and `{}` would fold identically.
func TestFoldReviewOrSetBodyShapes(t *testing.T) {
	labelOp := func(id, parent, body string, when int64) codec.Op {
		op := codec.Op{
			Envelope: codec.Envelope{
				ObjectID:   "r-shapes",
				ObjectType: "review",
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

	review, err := s.FoldReview(ops)
	if err != nil {
		t.Fatalf("FoldReview: %v", err)
	}

	// l2's bare string is one item. l3 and l4 are uninterpretable, so l4's
	// well-formed `never` dies with the operation and its malformed remove of
	// `triage` removes nothing. l6-remove-only causally removes `solo`.
	want := []string{"kept", "triage"}
	if !reflect.DeepEqual(review.Labels, want) {
		t.Errorf("Labels = %v, want %v", review.Labels, want)
	}

	wantQuarantined := []string{"l3", "l4"}
	var got []string
	for _, u := range review.UnknownOps {
		got = append(got, u.Commit)
	}
	if !reflect.DeepEqual(got, wantQuarantined) {
		t.Errorf("quarantined = %v, want %v", got, wantQuarantined)
	}

	// The generic fold must reach the same set through the same rules.
	generic, err := s.Fold(ops, s.ReviewRules())
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if !reflect.DeepEqual(generic.State["add"], want) {
		t.Errorf("generic add = %v, want %v", generic.State["add"], want)
	}
}

func TestFoldReviewNestedShapeAtFlatField(t *testing.T) {
	labelOp := func(id, parent, body string, when int64) codec.Op {
		op := codec.Op{
			Envelope: codec.Envelope{
				ObjectID:   "r-nested",
				ObjectType: "review",
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

	review, err := s.FoldReview(ops)
	if err != nil {
		t.Fatalf("FoldReview: %v", err)
	}

	want := []string{"bug"}
	if !reflect.DeepEqual(review.Labels, want) {
		t.Errorf("Labels = %v, want %v", review.Labels, want)
	}

	generic, err := s.Fold(ops, s.ReviewRules())
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if !reflect.DeepEqual(generic.State["add"], want) {
		t.Errorf("generic add = %v, want %v", generic.State["add"], want)
	}
}

func TestFoldReviewMixedFlatAndNestedShapes(t *testing.T) {
	labelOp := func(id, parent, body string, when int64) codec.Op {
		op := codec.Op{
			Envelope: codec.Envelope{
				ObjectID:   "r-mixed",
				ObjectType: "review",
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

	// l1: flat add, nested remove
	// l2: nested add, flat remove
	// l3: nested add AND nested remove in the same op
	ops := []codec.Op{
		labelOp("l1", "", `{"add":"feature","remove":{"remove":["old"]}}`, 100),
		labelOp("l2", "l1", `{"add":{"add":["bug"]},"remove":"temporary"}`, 200),
		labelOp("l3", "l2", `{"add":{"add":["urgent"],"remove":["feature"]},"remove":{"add":["security"],"remove":["bug"]}}`, 300),
	}

	review, err := s.FoldReview(ops)
	if err != nil {
		t.Fatalf("FoldReview: %v", err)
	}

	// l1 adds "feature"
	// l2 adds "bug"
	// l3 removes "feature" (via add.remove), adds "urgent" (via add.add), adds "security" (via remove.add), removes "bug" (via remove.remove)
	// Remaining: "security", "urgent"
	want := []string{"security", "urgent"}
	if !reflect.DeepEqual(review.Labels, want) {
		t.Errorf("Labels = %v, want %v", review.Labels, want)
	}

	generic, err := s.Fold(ops, s.ReviewRules())
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if !reflect.DeepEqual(generic.State["add"], want) {
		t.Errorf("generic add = %v, want %v", generic.State["add"], want)
	}
	if !reflect.DeepEqual(generic.State["remove"], want) {
		t.Errorf("generic remove = %v, want %v", generic.State["remove"], want)
	}

	// Reference fold cross-check
	var mergeOps []spec.MergeOp
	for _, o := range ops {
		var bm map[string]any
		_ = json.Unmarshal(o.Body, &bm)
		mergeOps = append(mergeOps, spec.MergeOp{
			ID:        o.ID,
			Parents:   o.Parents,
			Time:      o.Author.When.Unix(),
			ObjectID:  o.ObjectID,
			OpType:    o.OpType,
			OpVersion: o.OpVersion,
			Author: spec.MergeAuthor{
				Name:  o.Author.Name,
				Email: o.Author.Email,
			},
			Body: bm,
		})
	}
	var fieldRules []spec.FieldRule
	for _, r := range s.ReviewRules() {
		fieldRules = append(fieldRules, spec.FieldRule{
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
	refRes, err := spec.Fold(mergeOps, fieldRules)
	if err != nil {
		t.Fatalf("spec.Fold: %v", err)
	}
	if !reflect.DeepEqual(refRes.State["add"], want) {
		t.Errorf("ref add = %v, want %v", refRes.State["add"], want)
	}
	if !reflect.DeepEqual(refRes.State["remove"], want) {
		t.Errorf("ref remove = %v, want %v", refRes.State["remove"], want)
	}
}

func TestFoldReviewSubjectlessApprovals(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	rev := "1111111111111111111111111111111111111111"

	opAlice := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-subjectless",
			ObjectType: "review",
			OpType:     "approval",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"` + rev + `","verdict":"approve","message":"looks great"}`),
		},
		ID: "op-alice-app",
		Author: codec.Identity{
			Email: "Alice@Example.COM",
			When:  now,
		},
	}

	opBob := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-subjectless",
			ObjectType: "review",
			OpType:     "approval",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"` + rev + `","verdict":"request-changes","message":"needs tests"}`),
		},
		ID: "op-bob-app",
		Author: codec.Identity{
			Email: "bob@example.com",
			When:  now.Add(time.Minute),
		},
	}

	state, err := s.FoldReview([]codec.Op{opAlice, opBob})
	if err != nil {
		t.Fatalf("FoldReview failed: %v", err)
	}

	if len(state.Approvals) != 2 {
		t.Fatalf("expected 2 active approvals, got %d: %+v", len(state.Approvals), state.Approvals)
	}

	wantAlice := s.Approval{
		Subject:  "email:alice@example.com",
		Revision: rev,
		Verdict:  "approve",
		Message:  "looks great",
	}
	wantBob := s.Approval{
		Subject:  "email:bob@example.com",
		Revision: rev,
		Verdict:  "request-changes",
		Message:  "needs tests",
	}

	if state.Approvals[0] != wantAlice {
		t.Errorf("approval[0] mismatch: got %+v, want %+v", state.Approvals[0], wantAlice)
	}
	if state.Approvals[1] != wantBob {
		t.Errorf("approval[1] mismatch: got %+v, want %+v", state.Approvals[1], wantBob)
	}

	// Whitespace-only subject from Alice updates Alice's approval (normalizes away to commit author email)
	opAliceUpdate := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-subjectless",
			ObjectType: "review",
			OpType:     "approval",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"` + rev + `","verdict":"request-changes","subject":"   ","message":"rethinking"}`),
		},
		ID:      "op-alice-update",
		Parents: []string{"op-alice-app"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	stateUpdated, err := s.FoldReview([]codec.Op{opAlice, opBob, opAliceUpdate})
	if err != nil {
		t.Fatalf("FoldReview with update failed: %v", err)
	}

	if len(stateUpdated.Approvals) != 2 {
		t.Fatalf("expected 2 active approvals after update, got %d: %+v", len(stateUpdated.Approvals), stateUpdated.Approvals)
	}

	wantAliceUpdated := s.Approval{
		Subject:  "email:alice@example.com",
		Revision: rev,
		Verdict:  "request-changes",
		Message:  "rethinking",
	}
	if stateUpdated.Approvals[0] != wantAliceUpdated {
		t.Errorf("updated approval[0] mismatch: got %+v, want %+v", stateUpdated.Approvals[0], wantAliceUpdated)
	}
}

func TestFoldReviewApprovalAndCIStatusKeyAgreement(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	rev := "1111111111111111111111111111111111111111"

	opCreate := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-dual",
			ObjectType: "review",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Initial Review","description":"Review desc"}`),
		},
		ID: "op-create",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	opApp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-dual",
			ObjectType: "review",
			OpType:     "approval",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"` + rev + `","verdict":"approve","subject":"user:alice","message":"LGTM"}`),
		},
		ID:      "op-app",
		Parents: []string{"op-create"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now.Add(time.Minute),
		},
	}

	opCI := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "r-dual",
			ObjectType: "review",
			OpType:     "ci-status",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"` + rev + `","name":"build","state":"success","url":"https://ci.example.com/1"}`),
		},
		ID:      "op-ci",
		Parents: []string{"op-app"},
		Author: codec.Identity{
			Email: "ci-bot@example.com",
			When:  now.Add(2 * time.Minute),
		},
	}

	ops := []codec.Op{opCreate, opApp, opCI}

	// 1. Fold via engine generic fold
	generic, err := s.Fold(ops, s.ReviewRules())
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}

	wantRevision := []any{
		map[string]any{
			"key":   []string{rev, "build"},
			"value": rev,
		},
		map[string]any{
			"key":   []string{"user:alice", rev},
			"value": rev,
		},
	}

	if !reflect.DeepEqual(generic.State["revision"], wantRevision) {
		t.Errorf("revision mismatch:\n got:  %v\n want: %v", generic.State["revision"], wantRevision)
	}

	// 2. FoldReview typed fold verification
	review, err := s.FoldReview(ops)
	if err != nil {
		t.Fatalf("FoldReview: %v", err)
	}
	if len(review.Approvals) != 1 {
		t.Fatalf("expected 1 approval, got %d", len(review.Approvals))
	}
	if review.Approvals[0].Subject != "user:alice" || review.Approvals[0].Revision != rev || review.Approvals[0].Verdict != "approve" {
		t.Errorf("approval mismatch: %+v", review.Approvals[0])
	}
	if len(review.CIStatuses) != 1 {
		t.Fatalf("expected 1 ci status, got %d", len(review.CIStatuses))
	}
	if review.CIStatuses[0].Name != "build" || review.CIStatuses[0].Revision != rev || review.CIStatuses[0].State != "success" {
		t.Errorf("ci-status mismatch: %+v", review.CIStatuses[0])
	}

	// 3. spec.Fold reference fold cross-check
	var mergeOps []spec.MergeOp
	for _, o := range ops {
		var bm map[string]any
		_ = json.Unmarshal(o.Body, &bm)
		mergeOps = append(mergeOps, spec.MergeOp{
			ID:        o.ID,
			Parents:   o.Parents,
			Time:      o.Author.When.Unix(),
			ObjectID:  o.ObjectID,
			OpType:    o.OpType,
			OpVersion: o.OpVersion,
			Author: spec.MergeAuthor{
				Email: o.Author.Email,
			},
			Body: bm,
		})
	}

	var fieldRules []spec.FieldRule
	for _, r := range s.ReviewRules() {
		fieldRules = append(fieldRules, spec.FieldRule{
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

	refRes, err := spec.Fold(mergeOps, fieldRules)
	if err != nil {
		t.Fatalf("spec.Fold: %v", err)
	}

	if !reflect.DeepEqual(generic.State["revision"], refRes.State["revision"]) {
		t.Errorf("revision mismatch between engine fold and spec fold:\n engine: %v\n spec:   %v",
			generic.State["revision"], refRes.State["revision"])
	}

	engineRaw, err := json.Marshal(generic.State)
	if err != nil {
		t.Fatalf("json.Marshal(generic.State): %v", err)
	}
	specRaw, err := json.Marshal(refRes.State)
	if err != nil {
		t.Fatalf("json.Marshal(refRes.State): %v", err)
	}

	engineJSON, err := canonicaljson.Marshal(engineRaw)
	if err != nil {
		t.Fatalf("canonicaljson.Marshal(engineRaw): %v", err)
	}
	specJSON, err := canonicaljson.Marshal(specRaw)
	if err != nil {
		t.Fatalf("canonicaljson.Marshal(specRaw): %v", err)
	}

	if string(engineJSON) != string(specJSON) {
		t.Errorf("canonical JSON mismatch between engine fold and spec fold:\n engine: %s\n spec:   %s",
			string(engineJSON), string(specJSON))
	}
}

func TestFoldReviewDescriptionNoCollisionWithCIStatus(t *testing.T) {
	// Verifies WRIT-173: review description (lww) and ci-status description (keyed-lww mapped to target ci_description)
	// do not collide in typed FoldReview, generic state.Fold, and spec.Fold.
	now := time.Unix(1700000000, 0)
	objID := "r-desc-test"
	rev := "1111111111111111111111111111111111111111"

	opCreate := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   objID,
			ObjectType: "review",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Test Review","description":"Top-level review description"}`),
		},
		ID: "c1",
		Author: codec.Identity{
			Name:  "Alice",
			Email: "alice@example.test",
			When:  now,
		},
	}

	opRevision := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   objID,
			ObjectType: "review",
			OpType:     "revision",
			OpVersion:  1,
			Body:       json.RawMessage(`{"base":"0000000000000000000000000000000000000000","head":"` + rev + `"}`),
		},
		ID:      "c2",
		Parents: []string{"c1"},
		Author: codec.Identity{
			Name:  "Alice",
			Email: "alice@example.test",
			When:  now.Add(1 * time.Minute),
		},
	}

	opCI := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   objID,
			ObjectType: "review",
			OpType:     "ci-status",
			OpVersion:  1,
			Body:       json.RawMessage(`{"revision":"` + rev + `","name":"ci/build","state":"success","description":"Build succeeded in 42s"}`),
		},
		ID:      "c3",
		Parents: []string{"c2"},
		Author: codec.Identity{
			Name:  "CI Bot",
			Email: "ci@example.test",
			When:  now.Add(2 * time.Minute),
		},
	}

	opUpdate := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   objID,
			ObjectType: "review",
			OpType:     "update",
			OpVersion:  1,
			Body:       json.RawMessage(`{"description":"Updated review description"}`),
		},
		ID:      "c4",
		Parents: []string{"c3"},
		Author: codec.Identity{
			Name:  "Alice",
			Email: "alice@example.test",
			When:  now.Add(3 * time.Minute),
		},
	}

	ops := []codec.Op{opCreate, opRevision, opCI, opUpdate}

	// 1. Typed state.FoldReview
	review, err := s.FoldReview(ops)
	if err != nil {
		t.Fatalf("FoldReview failed: %v", err)
	}
	if review.Description != "Updated review description" {
		t.Errorf("Review.Description = %q, want %q", review.Description, "Updated review description")
	}
	if len(review.CIStatuses) != 1 {
		t.Fatalf("len(review.CIStatuses) = %d, want 1", len(review.CIStatuses))
	}
	if review.CIStatuses[0].Description != "Build succeeded in 42s" {
		t.Errorf("CIStatus[0].Description = %q, want %q", review.CIStatuses[0].Description, "Build succeeded in 42s")
	}

	// 2. Generic state.Fold
	generic, err := s.Fold(ops, s.ReviewRules())
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	if generic.State["description"] != "Updated review description" {
		t.Errorf("generic.State['description'] = %v, want %q", generic.State["description"], "Updated review description")
	}
	ciDescList, ok := generic.State["ci_description"].([]any)
	if !ok || len(ciDescList) != 1 {
		t.Fatalf("generic.State['ci_description'] = %v (type %T), want 1-element []any", generic.State["ci_description"], generic.State["ci_description"])
	}
	ciEntry, ok := ciDescList[0].(map[string]any)
	if !ok {
		t.Fatalf("ci_description[0] is not map[string]any: %T", ciDescList[0])
	}
	if ciEntry["value"] != "Build succeeded in 42s" {
		t.Errorf("ciEntry['value'] = %v, want %q", ciEntry["value"], "Build succeeded in 42s")
	}

	// 3. spec.Fold reference fold cross-check
	var mergeOps []spec.MergeOp
	for _, o := range ops {
		var bm map[string]any
		_ = json.Unmarshal(o.Body, &bm)
		mergeOps = append(mergeOps, spec.MergeOp{
			ID:        o.ID,
			Parents:   o.Parents,
			Time:      o.Author.When.Unix(),
			ObjectID:  o.ObjectID,
			OpType:    o.OpType,
			OpVersion: o.OpVersion,
			Author: spec.MergeAuthor{
				Email: o.Author.Email,
			},
			Body: bm,
		})
	}

	var fieldRules []spec.FieldRule
	for _, r := range s.ReviewRules() {
		fieldRules = append(fieldRules, spec.FieldRule{
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

	refRes, err := spec.Fold(mergeOps, fieldRules)
	if err != nil {
		t.Fatalf("spec.Fold failed: %v", err)
	}

	if refRes.State["description"] != "Updated review description" {
		t.Errorf("refRes.State['description'] = %v, want %q", refRes.State["description"], "Updated review description")
	}
	if !reflect.DeepEqual(generic.State["ci_description"], refRes.State["ci_description"]) {
		t.Errorf("ci_description mismatch between generic fold and spec fold:\n generic: %v\n spec:    %v",
			generic.State["ci_description"], refRes.State["ci_description"])
	}

	engineRaw, err := json.Marshal(generic.State)
	if err != nil {
		t.Fatalf("json.Marshal(generic.State): %v", err)
	}
	specRaw, err := json.Marshal(refRes.State)
	if err != nil {
		t.Fatalf("json.Marshal(refRes.State): %v", err)
	}

	engineJSON, err := canonicaljson.Marshal(engineRaw)
	if err != nil {
		t.Fatalf("canonicaljson.Marshal(engineRaw): %v", err)
	}
	specJSON, err := canonicaljson.Marshal(specRaw)
	if err != nil {
		t.Fatalf("canonicaljson.Marshal(specRaw): %v", err)
	}

	if string(engineJSON) != string(specJSON) {
		t.Errorf("canonical JSON mismatch between engine fold and spec fold:\n engine: %s\n spec:   %s",
			string(engineJSON), string(specJSON))
	}
}
