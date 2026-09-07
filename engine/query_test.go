package writ_test

import (
	"context"
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
	r1, err := s.Reviews.Create(ctx, writ.NewReview{
		Title:       "First Feature Review",
		Description: "Alpha feature",
	})
	if err != nil {
		t.Fatalf("Create r1: %v", err)
	}

	r2, err := s.Reviews.Create(ctx, writ.NewReview{
		Title:       "Second Bugfix Review",
		Description: "Beta bugfix",
	})
	if err != nil {
		t.Fatalf("Create r2: %v", err)
	}

	if err := s.Reviews.SetStatus(ctx, r2, writ.ReviewStatus{Status: "closed", Reason: "superseded"}); err != nil {
		t.Fatalf("SetStatus r2: %v", err)
	}

	// 2. Create multiple issues
	i1, err := s.Issues.Create(ctx, writ.NewIssue{
		Title:       "Issue One",
		Description: "Important issue",
	})
	if err != nil {
		t.Fatalf("Create i1: %v", err)
	}
	if err := s.Issues.Assign(ctx, i1, []string{"user:alice"}, nil); err != nil {
		t.Fatalf("Assign i1: %v", err)
	}
	if err := s.Issues.Label(ctx, i1, []string{"frontend"}, nil); err != nil {
		t.Fatalf("Label i1: %v", err)
	}

	i2, err := s.Issues.Create(ctx, writ.NewIssue{
		Title:       "Issue Two",
		Description: "Backend issue",
	})
	if err != nil {
		t.Fatalf("Create i2: %v", err)
	}
	if err := s.Issues.SetState(ctx, i2, writ.IssueState{State: "closed", Reason: "fixed"}); err != nil {
		t.Fatalf("SetState i2: %v", err)
	}
	if err := s.Issues.Assign(ctx, i2, []string{"user:bob"}, nil); err != nil {
		t.Fatalf("Assign i2: %v", err)
	}

	// 3. Comments on r1
	c1, err := s.Reviews.Comment(ctx, r1, writ.NewComment{Text: "Comment 1 on r1"})
	if err != nil {
		t.Fatalf("Comment r1: %v", err)
	}
	c2, err := s.Reviews.Comment(ctx, r1, writ.NewComment{Text: "Reply to comment 1", InReplyTo: c1})
	if err != nil {
		t.Fatalf("Reply r1: %v", err)
	}

	// Edit c1
	if err := s.Comments.Edit(ctx, c1, "Edited Comment 1 on r1"); err != nil {
		t.Fatalf("Edit c1: %v", err)
	}

	// Test Query.Reviews
	closedReviews, err := s.Query.Reviews(writ.ReviewFilter{Status: []string{"closed"}})
	if err != nil {
		t.Fatalf("Query.Reviews closed: %v", err)
	}
	if len(closedReviews) != 1 || closedReviews[0].ObjectID != r2 {
		t.Errorf("expected closed review r2, got %+v", closedReviews)
	}

	allReviews, err := s.Query.Reviews(writ.ReviewFilter{OrderBy: writ.OrderByCreatedAtAsc})
	if err != nil {
		t.Fatalf("Query.Reviews all: %v", err)
	}
	if len(allReviews) != 2 {
		t.Errorf("expected 2 reviews, got %d", len(allReviews))
	}

	// Test Query.Review point lookup
	resR1, err := s.Query.Review(r1)
	if err != nil {
		t.Fatalf("Query.Review(r1): %v", err)
	}
	if resR1.Review.Title != "First Feature Review" {
		t.Errorf("got title %q", resR1.Review.Title)
	}

	// Test Query.Review not found
	_, err = s.Query.Review("non-existent-id")
	if !errors.Is(err, writ.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing review, got: %v", err)
	}

	// Test Query.Issues: i1 never had a set-state op, so it folds with the
	// empty default state (no legacy "open" blessing).
	openIssues, err := s.Query.Issues(writ.IssueFilter{State: []string{""}})
	if err != nil {
		t.Fatalf("Query.Issues empty state: %v", err)
	}
	if len(openIssues) != 1 || openIssues[0].ObjectID != i1 {
		t.Errorf("expected issue i1, got %+v", openIssues)
	}

	// Test Query.Issue point lookup
	resI2, err := s.Query.Issue(i2)
	if err != nil {
		t.Fatalf("Query.Issue(i2): %v", err)
	}
	if resI2.Issue.State != "closed" {
		t.Errorf("got state %q, want 'closed'", resI2.Issue.State)
	}

	// Test Query.Issue not found
	_, err = s.Query.Issue("non-existent-id")
	if !errors.Is(err, writ.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing issue, got: %v", err)
	}

	// Test Query.Comments
	comments, err := s.Query.Comments(writ.CommentFilter{SubjectID: r1})
	if err != nil {
		t.Fatalf("Query.Comments: %v", err)
	}
	if len(comments) != 2 {
		t.Errorf("expected 2 comments on r1, got %d", len(comments))
	}

	// Test Query.Threads
	threads, err := s.Query.Threads("review", r1)
	if err != nil {
		t.Fatalf("Query.Threads: %v", err)
	}
	if len(threads) != 1 || len(threads[0].Replies) != 1 || threads[0].Replies[0].ObjectID != c2 {
		t.Errorf("unexpected thread tree: %+v", threads)
	}

	// Test Query.Objects
	objects, err := s.Query.Objects(writ.ObjectFilter{})
	if err != nil {
		t.Fatalf("Query.Objects: %v", err)
	}
	if len(objects) != 6 { // 2 reviews + 2 issues + 2 comments
		t.Errorf("expected 6 objects total, got %d", len(objects))
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
	id, err := s.Reviews.Create(ctx, writ.NewReview{Title: "Manual Refresh Review"})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Without refresh, projection has not folded the new review yet
	_, err = s.Query.Review(id)
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

	// Now Query.Review finds it
	res, err := s.Query.Review(id)
	if err != nil {
		t.Fatalf("Query.Review after manual refresh failed: %v", err)
	}
	if res.Review.Title != "Manual Refresh Review" {
		t.Errorf("got title %q", res.Review.Title)
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

	r1, err := s.Reviews.Create(ctx, writ.NewReview{Title: "Zebra Crossing Review"})
	if err != nil {
		t.Fatalf("Create review failed: %v", err)
	}
	c1, err := s.Reviews.Comment(ctx, r1, writ.NewComment{Text: "first comment"})
	if err != nil {
		t.Fatalf("Comment failed: %v", err)
	}
	if err := s.Comments.Delete(ctx, c1); err != nil {
		t.Fatalf("Comments.Delete failed: %v", err)
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
