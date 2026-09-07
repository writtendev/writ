package writ_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/writtendev/writ/engine"
)

func TestQueryFullSuite(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)
	s, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// 1. Create multiple reviews
	r1, err := s.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "First Feature Review", "description": "Alpha feature"},
	})
	if err != nil {
		t.Fatalf("Create r1: %v", err)
	}

	r2, err := s.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Second Bugfix Review", "description": "Beta bugfix"},
	})
	if err != nil {
		t.Fatalf("Create r2: %v", err)
	}

	if err := s.Objects.Apply(ctx, r2, writ.NewOp{
		Type:   "set-status",
		Fields: map[string]any{"status": "closed", "reason": "superseded"},
	}); err != nil {
		t.Fatalf("SetStatus r2: %v", err)
	}

	// 2. Create multiple issues
	i1, err := s.Objects.Create(ctx, "issue", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Issue One", "description": "Important issue"},
	})
	if err != nil {
		t.Fatalf("Create i1: %v", err)
	}
	if err := s.Objects.Apply(ctx, i1, writ.NewOp{
		Type:   "assign",
		Fields: map[string]any{"add": []string{"user:alice"}},
	}); err != nil {
		t.Fatalf("Assign i1: %v", err)
	}
	if err := s.Objects.Apply(ctx, i1, writ.NewOp{
		Type:   "label",
		Fields: map[string]any{"add": []string{"frontend"}},
	}); err != nil {
		t.Fatalf("Label i1: %v", err)
	}

	i2, err := s.Objects.Create(ctx, "issue", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Issue Two", "description": "Backend issue"},
	})
	if err != nil {
		t.Fatalf("Create i2: %v", err)
	}
	if err := s.Objects.Apply(ctx, i2, writ.NewOp{
		Type:   "set-state",
		Fields: map[string]any{"state": "closed", "reason": "fixed"},
	}); err != nil {
		t.Fatalf("SetState i2: %v", err)
	}
	if err := s.Objects.Apply(ctx, i2, writ.NewOp{
		Type:   "assign",
		Fields: map[string]any{"add": []string{"user:bob"}},
	}); err != nil {
		t.Fatalf("Assign i2: %v", err)
	}

	// 3. Comments on r1
	c1, err := s.Objects.Create(ctx, "comment", writ.NewOp{
		Type: "create",
		Fields: map[string]any{
			"text":    "Comment 1 on r1",
			"subject": map[string]string{"object_type": "review", "object_id": r1},
		},
	})
	if err != nil {
		t.Fatalf("Comment r1: %v", err)
	}
	c2, err := s.Objects.Create(ctx, "comment", writ.NewOp{
		Type: "create",
		Fields: map[string]any{
			"text":        "Reply to comment 1",
			"subject":     map[string]string{"object_type": "review", "object_id": r1},
			"in_reply_to": c1,
		},
	})
	if err != nil {
		t.Fatalf("Reply r1: %v", err)
	}

	// Edit c1
	if err := s.Objects.Apply(ctx, c1, writ.NewOp{
		Type:   "edit",
		Fields: map[string]any{"text": "Edited Comment 1 on r1"},
	}); err != nil {
		t.Fatalf("Edit c1: %v", err)
	}

	// Test Query.Objects, filtered by type
	allReviews, err := s.Query.Objects(writ.ObjectFilter{Type: []string{"review"}, OrderBy: writ.OrderByCreatedAtAsc})
	if err != nil {
		t.Fatalf("Query.Objects(review): %v", err)
	}
	if len(allReviews) != 2 {
		t.Errorf("expected 2 reviews, got %d", len(allReviews))
	}

	// Test Objects.Get point lookup and folded field values
	objR1, err := s.Objects.Get(ctx, r1)
	if err != nil {
		t.Fatalf("Objects.Get(r1): %v", err)
	}
	if objR1.Fields["title"] != "First Feature Review" {
		t.Errorf("got title %q", objR1.Fields["title"])
	}

	objR2, err := s.Objects.Get(ctx, r2)
	if err != nil {
		t.Fatalf("Objects.Get(r2): %v", err)
	}
	if objR2.Fields["status"] != "closed" {
		t.Errorf("expected r2 status closed, got %v", objR2.Fields["status"])
	}

	// Test Objects.Get not found
	_, err = s.Objects.Get(ctx, "non-existent-id")
	if !errors.Is(err, writ.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing review, got: %v", err)
	}

	// Test Objects.Get: i1 never had a set-state op, so it folds with the
	// empty default state (no legacy "open" blessing).
	objI1, err := s.Objects.Get(ctx, i1)
	if err != nil {
		t.Fatalf("Objects.Get(i1): %v", err)
	}
	if objI1.Fields["state"] != nil && objI1.Fields["state"] != "" {
		t.Errorf("expected i1 to have no state, got %v", objI1.Fields["state"])
	}
	assignees, _ := objI1.Fields["assignees"].([]string)
	if len(assignees) != 1 || assignees[0] != "user:alice" {
		t.Errorf("expected i1 assignees [user:alice], got %v", objI1.Fields["assignees"])
	}

	objI2, err := s.Objects.Get(ctx, i2)
	if err != nil {
		t.Fatalf("Objects.Get(i2): %v", err)
	}
	if objI2.Fields["state"] != "closed" {
		t.Errorf("got state %q, want 'closed'", objI2.Fields["state"])
	}

	// Test Query.Objects cross-type
	objects, err := s.Query.Objects(writ.ObjectFilter{})
	if err != nil {
		t.Fatalf("Query.Objects: %v", err)
	}
	if len(objects) != 6 { // 2 reviews + 2 issues + 2 comments
		t.Errorf("expected 6 objects total, got %d", len(objects))
	}

	// Test comment content and threading via Fields, since Query.Threads
	// (a per-type reader) no longer exists.
	objC1, err := s.Objects.Get(ctx, c1)
	if err != nil {
		t.Fatalf("Objects.Get(c1): %v", err)
	}
	if objC1.Fields["text"] != "Edited Comment 1 on r1" {
		t.Errorf("expected edited text, got %v", objC1.Fields["text"])
	}
	objC2, err := s.Objects.Get(ctx, c2)
	if err != nil {
		t.Fatalf("Objects.Get(c2): %v", err)
	}
	// in_reply_to declares no normalizing value type, so create-once's
	// byte-exact-preservation rule (spec/fold.md §5.2) returns it as raw
	// JSON bytes rather than a decoded string.
	inReplyToRaw, _ := objC2.Fields["in_reply_to"].(json.RawMessage)
	var inReplyTo string
	if err := json.Unmarshal(inReplyToRaw, &inReplyTo); err != nil {
		t.Fatalf("unmarshal in_reply_to: %v", err)
	}
	if inReplyTo != c1 {
		t.Errorf("expected c2.in_reply_to = %s, got %v", c1, inReplyTo)
	}
}

func TestWithoutAutoRefresh(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)

	s, err := writ.Open(repoDir,
		writ.WithSigner(dummySigner()),
		writ.WithoutAutoRefresh(),
	)
	if err != nil {
		t.Fatalf("Open WithoutAutoRefresh failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// Write without auto-refresh
	id, err := s.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Manual Refresh Review"},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Without refresh, the projection has not folded the new review yet —
	// Query.Object is served from the projection, unlike Objects.Get.
	_, err = s.Query.Object(id)
	if !errors.Is(err, writ.ErrNotFound) {
		t.Errorf("expected ErrNotFound before manual refresh, got: %v", err)
	}

	// Explicit refresh
	stats, err := s.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	if stats.ObjectsTouched != 1 {
		t.Errorf("expected 1 object touched, got %d", stats.ObjectsTouched)
	}

	// Now Query.Object finds it
	res, err := s.Query.Object(id)
	if err != nil {
		t.Fatalf("Query.Object after manual refresh failed: %v", err)
	}
	if res.ObjectID != id {
		t.Errorf("got object id %q, want %q", res.ObjectID, id)
	}

	// Query.Objects' text filter is served from the projection's generated
	// type-table columns (o_review.f_title here), not from ObjectResult's
	// own objects-table-only metadata — so, unlike res above, this actually
	// asserts a folded field value made it into the cache (round 1 minor
	// finding: no engine-level test asserted a folded field value out of
	// the projection any more once title assertions moved onto Objects.Get,
	// which deliberately bypasses it).
	byText, err := s.Query.Objects(writ.ObjectFilter{Text: "Manual Refresh Review"})
	if err != nil {
		t.Fatalf("Query.Objects(Text) after manual refresh failed: %v", err)
	}
	if len(byText) != 1 || byText[0].ObjectID != id {
		t.Errorf("Query.Objects(Text=%q) = %+v, want exactly [%s] — the projection's materialized title must match", "Manual Refresh Review", byText, id)
	}

	// Objects.Get, which folds from the DAG directly, finds it regardless.
	obj, err := s.Objects.Get(ctx, id)
	if err != nil {
		t.Fatalf("Objects.Get failed: %v", err)
	}
	if obj.Fields["title"] != "Manual Refresh Review" {
		t.Errorf("got title %q", obj.Fields["title"])
	}
}

// TestQueryObjects_WarmReopenWithoutAutoRefresh is WRIT-192 round 2's
// MAJOR-1: engine/open.go deliberately skips ApplySchema on Open when the
// projection cache already has generated tables (WRIT-189 round 1's lazy-Open
// optimization) — a reopened warm cache starts out with a name-only
// descriptor (table names only, no target plans) until this process's own
// first Refresh. With WithoutAutoRefresh, no Refresh ever runs automatically,
// so every Query.Objects call in that state used to build its !IncludeDeleted
// and Text clauses from an empty descriptor: objectsNotDeletedClause emitted
// no clause at all (a soft-deleted object surfaced in a default listing) and
// objectsTextClause fell to "AND 0" (a text search that matched with a fully
// resolved descriptor returned nothing). Both are regressions against `main`
// — a warm reopen with WithoutAutoRefresh is a legitimate, optimized flow,
// and both filters must answer correctly without a DAG walk.
func TestQueryObjects_WarmReopenWithoutAutoRefresh(t *testing.T) {
	ctx := context.Background()
	dir, _ := setupConfiguredRepo(t)
	cacheDir := t.TempDir()

	s, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(cacheDir))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	r1, err := s.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Zebra Crossing Review"},
	})
	if err != nil {
		t.Fatalf("Create review failed: %v", err)
	}
	c1, err := s.Objects.Create(ctx, "comment", writ.NewOp{
		Type: "create",
		Fields: map[string]any{
			"text":    "first comment",
			"subject": map[string]string{"object_type": "review", "object_id": r1},
		},
	})
	if err != nil {
		t.Fatalf("Comment failed: %v", err)
	}
	// The delete op body carries no content (spec/comments.md): tombstone
	// semantics come from the op type itself, not a field in its body.
	if err := s.Objects.Apply(ctx, c1, writ.NewOp{
		Type: "delete",
	}); err != nil {
		t.Fatalf("Objects.Apply(delete) failed: %v", err)
	}

	if _, err := s.Refresh(ctx); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen the same cache dir: generated tables already on disk, so Open
	// skips ApplySchema (HasGeneratedTables is true), and WithoutAutoRefresh
	// means nothing ever calls Refresh in this process either — the
	// name-only-descriptor state the finding describes.
	s2, err := writ.Open(dir,
		writ.WithSigner(dummySigner()),
		writ.WithCacheDir(cacheDir),
		writ.WithoutAutoRefresh(),
	)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer s2.Close()

	// Default listing (!IncludeDeleted) must exclude the soft-deleted
	// comment, exactly as it does on a freshly built descriptor.
	objects, err := s2.Query.Objects(writ.ObjectFilter{})
	if err != nil {
		t.Fatalf("Query.Objects (default) failed: %v", err)
	}
	for _, o := range objects {
		if o.ObjectID == c1 {
			t.Errorf("Query.Objects (default) on a warm reopen included the soft-deleted comment %s: %+v", c1, objects)
		}
	}
	foundReview := false
	for _, o := range objects {
		if o.ObjectID == r1 {
			foundReview = true
		}
	}
	if !foundReview {
		t.Errorf("Query.Objects (default) on a warm reopen did not include the review %s: %+v", r1, objects)
	}

	// IncludeDeleted: true must still surface it.
	withDeleted, err := s2.Query.Objects(writ.ObjectFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("Query.Objects (IncludeDeleted) failed: %v", err)
	}
	foundDeleted := false
	for _, o := range withDeleted {
		if o.ObjectID == c1 {
			foundDeleted = true
		}
	}
	if !foundDeleted {
		t.Errorf("Query.Objects (IncludeDeleted: true) on a warm reopen did not include the soft-deleted comment %s: %+v", c1, withDeleted)
	}

	// Text search over the descriptor-driven clause must still match.
	textResults, err := s2.Query.Objects(writ.ObjectFilter{Text: "Zebra"})
	if err != nil {
		t.Fatalf("Query.Objects (Text) failed: %v", err)
	}
	if len(textResults) != 1 || textResults[0].ObjectID != r1 {
		t.Errorf("Query.Objects (Text: Zebra) on a warm reopen = %+v, want [%s]", textResults, r1)
	}
}
