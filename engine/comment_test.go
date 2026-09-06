package writ_test

import (
	"context"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/go-git/go-git/v5"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/spec/fixtures"
)

func TestFoldCommentsThreadsFixture(t *testing.T) {
	corpus, err := fixtures.LoadCorpus()
	if err != nil {
		t.Fatalf("fixtures.LoadCorpus: %v", err)
	}

	var commentDesc *fixtures.Description
	for _, desc := range corpus {
		if desc.Name == "fold-comment-threads" {
			commentDesc = desc
			break
		}
	}
	if commentDesc == nil {
		t.Fatal("fixture description fold-comment-threads not found")
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	if _, err := fixtures.Generate(commentDesc, repoDir); err != nil {
		t.Fatalf("fixtures.Generate: %v", err)
	}

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatalf("git.PlainOpen: %v", err)
	}

	store, err := dag.OpenRepo(repo, identity.Identity{})
	if err != nil {
		t.Fatalf("dag.OpenRepo: %v", err)
	}

	enumRes, err := store.Enumerate()
	if err != nil {
		t.Fatalf("store.Enumerate: %v", err)
	}

	var allOps []codec.Op
	for _, ops := range enumRes.Ops {
		allOps = append(allOps, ops...)
	}

	// 1. Fold the comment threads
	threads, err := writ.FoldComments(allOps)
	if err != nil {
		t.Fatalf("writ.FoldComments: %v", err)
	}

	if len(threads) != 2 {
		t.Fatalf("expected 2 root threads (c-root, c-del-target), got %d", len(threads))
	}

	// Roots should be ordered by creation (t*, commit):
	// c-root was created at 00:00:00, c-del-target was created at 00:01:10
	if threads[0].ObjectID != "c-root" {
		t.Errorf("expected first root to be 'c-root', got %q", threads[0].ObjectID)
	}
	if threads[1].ObjectID != "c-del-target" {
		t.Errorf("expected second root to be 'c-del-target', got %q", threads[1].ObjectID)
	}

	// c-root assertions
	cRoot := threads[0]
	if cRoot.Comment.Deleted {
		t.Errorf("expected c-root not deleted")
	}
	if cRoot.Comment.Text != "Bob edited root comment" {
		t.Errorf("expected c-root text 'Bob edited root comment', got %q", cRoot.Comment.Text)
	}
	if cRoot.Comment.Anchor == nil {
		t.Errorf("expected c-root to have anchor")
	}
	if len(cRoot.Replies) != 1 {
		t.Fatalf("expected c-root to have 1 reply (c-reply), got %d", len(cRoot.Replies))
	}

	// c-reply assertions
	cReply := cRoot.Replies[0]
	if cReply.ObjectID != "c-reply" {
		t.Errorf("expected reply to be 'c-reply', got %q", cReply.ObjectID)
	}
	if !cReply.Comment.Deleted {
		t.Errorf("expected c-reply to be deleted (deleted: true)")
	}
	if cReply.Comment.Text != "Bob post-delete edit" {
		t.Errorf("expected c-reply text 'Bob post-delete edit', got %q", cReply.Comment.Text)
	}
	if cReply.Comment.InReplyTo != "c-root" {
		t.Errorf("expected c-reply in_reply_to 'c-root', got %q", cReply.Comment.InReplyTo)
	}
	if len(cReply.Replies) != 0 {
		t.Errorf("expected c-reply to have 0 replies, got %d", len(cReply.Replies))
	}

	// c-del-target assertions
	cDel := threads[1]
	if !cDel.Comment.Deleted {
		t.Errorf("expected c-del-target to be deleted (deleted: true)")
	}
	if cDel.Comment.Text != "Alice concurrent edit on del target" {
		t.Errorf("expected c-del-target text 'Alice concurrent edit on del target', got %q", cDel.Comment.Text)
	}
	if len(cDel.Replies) != 0 {
		t.Errorf("expected c-del-target to have 0 replies, got %d", len(cDel.Replies))
	}

	// 2. Commutativity under 100 input shuffles
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for i := 0; i < 100; i++ {
		shuffledOps := make([]codec.Op, len(allOps))
		copy(shuffledOps, allOps)
		r.Shuffle(len(shuffledOps), func(a, b int) {
			shuffledOps[a], shuffledOps[b] = shuffledOps[b], shuffledOps[a]
		})

		shuffledThreads, err := writ.FoldComments(shuffledOps)
		if err != nil {
			t.Fatalf("permutation %d failed: %v", i, err)
		}

		if !reflect.DeepEqual(shuffledThreads, threads) {
			t.Fatalf("permutation %d output differed from canonical fold output", i)
		}
	}
}

func TestFoldCommentUnknownOps(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	ops := []codec.Op{
		{
			ID: "c1-create",
			Envelope: codec.Envelope{
				ObjectID:   "c-1",
				ObjectType: "comment",
				OpType:     "create",
				OpVersion:  1,
				Body:       []byte(`{"subject":{"object_type":"review","object_id":"r-1"},"text":"Initial text"}`),
			},
			Author: codec.Identity{When: baseTime},
		},
		{
			ID:      "c1-future",
			Parents: []string{"c1-create"},
			Envelope: codec.Envelope{
				ObjectID:   "c-1",
				ObjectType: "comment",
				OpType:     "react",
				OpVersion:  2,
				Body:       []byte(`{"reaction":"thumbs_up"}`),
			},
			Author: codec.Identity{When: baseTime.Add(time.Minute)},
		},
	}

	c, err := writ.FoldComment(ops)
	if err != nil {
		t.Fatalf("writ.FoldComment failed: %v", err)
	}

	if len(c.UnknownOps) != 1 {
		t.Fatalf("expected 1 UnknownOp on Comment, got %d", len(c.UnknownOps))
	}
	if c.UnknownOps[0].Commit != "c1-future" || c.UnknownOps[0].ObjectType != "comment" || c.UnknownOps[0].OpType != "react" || c.UnknownOps[0].OpVersion != 2 {
		t.Errorf("unexpected UnknownOp: %+v", c.UnknownOps[0])
	}

	threads, err := writ.FoldComments(ops)
	if err != nil {
		t.Fatalf("writ.FoldComments failed: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected 1 thread root, got %d", len(threads))
	}
	if len(threads[0].UnknownOps) != 1 {
		t.Fatalf("expected 1 UnknownOp on CommentThread, got %d", len(threads[0].UnknownOps))
	}
	if threads[0].UnknownOps[0].Commit != "c1-future" || threads[0].UnknownOps[0].ObjectType != "comment" {
		t.Errorf("unexpected thread UnknownOp: %+v", threads[0].UnknownOps[0])
	}
}

func TestCommentsResolveWorkflow(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)

	s, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	// 1. Create a Review
	headHash := runGitCmd(t, repoDir, "rev-parse", "HEAD")[:40]
	reviewID, err := s.Reviews.Create(ctx, writ.NewReview{
		Title: "Test Review for Comments",
		Base:  headHash,
		Head:  headHash,
	})
	if err != nil {
		t.Fatalf("Create review failed: %v", err)
	}

	// 2. Add root comment
	commentID, err := s.Reviews.Comment(ctx, reviewID, writ.NewComment{
		Text: "Root comment requesting changes",
	})
	if err != nil {
		t.Fatalf("Comment failed: %v", err)
	}

	// Verify initially unresolved
	comments, err := s.Query.Comments(writ.CommentFilter{SubjectID: reviewID})
	if err != nil {
		t.Fatalf("Query.Comments failed: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Comment.IsResolved() {
		t.Errorf("expected comment to initially be unresolved")
	}
	if comments[0].Comment.Resolved != nil {
		t.Errorf("expected initial Resolved pointer to be nil, got %+v", comments[0].Comment.Resolved)
	}

	// 3. Resolve thread
	if err := s.Comments.Resolve(ctx, commentID, writ.CommentResolve{
		Resolved:   true,
		ResolvedBy: "  Email:Alice@Example.COM  ",
	}); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Verify resolved state
	comments, err = s.Query.Comments(writ.CommentFilter{SubjectID: reviewID})
	if err != nil {
		t.Fatalf("Query.Comments failed: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if !comments[0].Comment.IsResolved() {
		t.Errorf("expected comment to be resolved")
	}
	if comments[0].Comment.ResolvedBy != "email:alice@example.com" {
		t.Errorf("expected comment resolved_by 'email:alice@example.com', got %q", comments[0].Comment.ResolvedBy)
	}

	// 4. Post reply after resolve - verify thread root remains resolved
	replyID, err := s.Reviews.Comment(ctx, reviewID, writ.NewComment{
		Text:      "Reply after resolve",
		InReplyTo: commentID,
	})
	if err != nil {
		t.Fatalf("Reply comment failed: %v", err)
	}
	if replyID == "" {
		t.Fatal("expected non-empty replyID")
	}

	threads, err := s.Query.Threads("review", reviewID)
	if err != nil {
		t.Fatalf("Query.Threads failed: %v", err)
	}
	if len(threads) != 1 {
		t.Fatalf("expected 1 thread, got %d", len(threads))
	}
	if !threads[0].Comment.IsResolved() {
		t.Errorf("expected thread root to remain resolved after reply")
	}
	if len(threads[0].Replies) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(threads[0].Replies))
	}

	// 5. Explicitly unresolve thread
	if err := s.Comments.Resolve(ctx, commentID, writ.CommentResolve{
		Resolved: false,
	}); err != nil {
		t.Fatalf("Unresolve failed: %v", err)
	}

	comments, err = s.Query.Comments(writ.CommentFilter{SubjectID: reviewID})
	if err != nil {
		t.Fatalf("Query.Comments failed: %v", err)
	}
	var rootComment *writ.CommentResult
	for i := range comments {
		if comments[i].ObjectID == commentID {
			rootComment = &comments[i]
			break
		}
	}
	if rootComment == nil {
		t.Fatalf("root comment not found in results")
	}
	if rootComment.Comment.IsResolved() {
		t.Errorf("expected comment to be unresolved after explicit unresolve")
	}
	if rootComment.Comment.Resolved == nil || *rootComment.Comment.Resolved != false {
		t.Errorf("expected Resolved pointer to be &false, got %+v", rootComment.Comment.Resolved)
	}

	// 6. Test Query filters
	trueVal := true
	falseVal := false
	resolvedComments, err := s.Query.Comments(writ.CommentFilter{SubjectID: reviewID, Resolved: &trueVal})
	if err != nil {
		t.Fatalf("Query resolved comments failed: %v", err)
	}
	if len(resolvedComments) != 0 {
		t.Errorf("expected 0 resolved comments, got %d", len(resolvedComments))
	}

	unresolvedComments, err := s.Query.Comments(writ.CommentFilter{SubjectID: reviewID, Resolved: &falseVal})
	if err != nil {
		t.Fatalf("Query unresolved comments failed: %v", err)
	}
	if len(unresolvedComments) != 2 {
		t.Errorf("expected 2 unresolved comments (root + reply), got %d", len(unresolvedComments))
	}
}

// TestCommentsResolveWhitespaceResolvedByOmitsKey verifies that empty or
// whitespace-only ResolvedBy is rejected by Comments.Resolve when Resolved is true,
// and that non-empty ResolvedBy is rejected when Resolved is false.
func TestCommentsResolveWhitespaceResolvedByOmitsKey(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)

	s, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	headHash := runGitCmd(t, repoDir, "rev-parse", "HEAD")[:40]
	reviewID, err := s.Reviews.Create(ctx, writ.NewReview{
		Title: "Whitespace ResolvedBy Review",
		Base:  headHash,
		Head:  headHash,
	})
	if err != nil {
		t.Fatalf("Create review failed: %v", err)
	}

	commentID, err := s.Reviews.Comment(ctx, reviewID, writ.NewComment{Text: "Root comment"})
	if err != nil {
		t.Fatalf("Comment failed: %v", err)
	}

	// Whitespace-only ResolvedBy on Resolved: true must be rejected.
	if err := s.Comments.Resolve(ctx, commentID, writ.CommentResolve{
		Resolved:   true,
		ResolvedBy: "   ",
	}); err == nil {
		t.Fatal("expected error for whitespace ResolvedBy on resolved: true, got nil")
	}

	// Empty ResolvedBy on Resolved: true must be rejected.
	if err := s.Comments.Resolve(ctx, commentID, writ.CommentResolve{
		Resolved:   true,
		ResolvedBy: "",
	}); err == nil {
		t.Fatal("expected error for empty ResolvedBy on resolved: true, got nil")
	}

	// Non-empty ResolvedBy on Resolved: false must be rejected.
	if err := s.Comments.Resolve(ctx, commentID, writ.CommentResolve{
		Resolved:   false,
		ResolvedBy: "user:alice",
	}); err == nil {
		t.Fatal("expected error for non-empty ResolvedBy on resolved: false, got nil")
	}

	// Ensure no resolve ops were written to the DAG store.
	ident, _ := identity.ParseWriterID("0123456789abcdef")
	dagStore, err := dag.Open(repoDir, identity.Identity{WriterID: ident})
	if err != nil {
		t.Fatalf("dag.Open failed: %v", err)
	}
	enumRes, err := dagStore.Enumerate()
	if err != nil {
		t.Fatalf("dagStore.Enumerate failed: %v", err)
	}

	for _, op := range enumRes.Ops[commentID] {
		if op.OpType == "resolve" {
			t.Errorf("expected no resolve op to be written, got: %+v", op)
		}
	}

	comments, err := s.Query.Comments(writ.CommentFilter{SubjectID: reviewID})
	if err != nil {
		t.Fatalf("Query.Comments failed: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Comment.IsResolved() {
		t.Errorf("expected comment to remain unresolved")
	}
}

// personIDAtLimit and personIDOverLimit bracket the person identifier length
// bound declared by spec/schemas/identifiers.schema.json: an email-shaped
// *value* exactly at the 320-code-point limit, and one a single character over.
// The bound is on the value, not the whole identifier, so the at-limit
// identifier is 326 characters overall — inside the derived 353 whole-string
// bound, which is the arithmetic a reader of the schema has to be able to redo.
func personIDAtLimit(t *testing.T) string {
	t.Helper()
	value := strings.Repeat("a", 64) + "@" + strings.Repeat("b", 251) + ".com"
	if len(value) != 320 {
		t.Fatalf("test setup: at-limit value is %d characters, want 320", len(value))
	}
	s := "email:" + value
	if len(s) != 326 {
		t.Fatalf("test setup: at-limit identifier is %d characters, want 326", len(s))
	}
	return s
}

func personIDOverLimit(t *testing.T) string {
	t.Helper()
	value := strings.Repeat("a", 64) + "@" + strings.Repeat("b", 252) + ".com"
	if len(value) != 321 {
		t.Fatalf("test setup: over-limit value is %d characters, want 321", len(value))
	}
	return "email:" + value
}

// TestCommentsResolvePersonIDLengthBound is the upper-bound counterpart to
// TestCommentsResolveWhitespaceResolvedByOmitsKey. The engine must not append an
// op the person-id schema would reject (maxLength: 320): op commits are signed,
// immutable and append-only, so an over-length identifier would be permanent
// unreclaimable weight. Rejection must be an error rather than a truncation —
// two distinct identifiers truncated to the same bytes would collapse into one
// person.
func TestCommentsResolvePersonIDLengthBound(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)

	s, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	headHash := runGitCmd(t, repoDir, "rev-parse", "HEAD")[:40]
	reviewID, err := s.Reviews.Create(ctx, writ.NewReview{
		Title: "Person ID Length Bound Review",
		Base:  headHash,
		Head:  headHash,
	})
	if err != nil {
		t.Fatalf("Create review failed: %v", err)
	}

	atLimit := personIDAtLimit(t)
	overLimit := personIDOverLimit(t)

	// An identifier exactly at the bound is accepted.
	acceptedID, err := s.Reviews.Comment(ctx, reviewID, writ.NewComment{Text: "At the bound"})
	if err != nil {
		t.Fatalf("Comment failed: %v", err)
	}
	if err := s.Comments.Resolve(ctx, acceptedID, writ.CommentResolve{
		Resolved:   true,
		ResolvedBy: atLimit,
	}); err != nil {
		t.Fatalf("Resolve with a 320-code-point value failed: %v", err)
	}

	// One character over, and the write is refused.
	rejectedID, err := s.Reviews.Comment(ctx, reviewID, writ.NewComment{Text: "Over the bound"})
	if err != nil {
		t.Fatalf("Comment failed: %v", err)
	}
	err = s.Comments.Resolve(ctx, rejectedID, writ.CommentResolve{
		Resolved:   true,
		ResolvedBy: overLimit,
	})
	if err == nil {
		t.Fatal("expected Resolve to reject a 321-code-point value, got nil error")
	}
	if !strings.Contains(err.Error(), "resolved_by") || !strings.Contains(err.Error(), "320") {
		t.Errorf("expected an error naming resolved_by and the 320-character limit, got %q", err)
	}

	// The rejected write must have left nothing behind: no resolve op at all,
	// truncated or otherwise.
	ident, _ := identity.ParseWriterID("0123456789abcdef")
	dagStore, err := dag.Open(repoDir, identity.Identity{WriterID: ident})
	if err != nil {
		t.Fatalf("dag.Open failed: %v", err)
	}
	enumRes, err := dagStore.Enumerate()
	if err != nil {
		t.Fatalf("dagStore.Enumerate failed: %v", err)
	}
	for _, op := range enumRes.Ops[rejectedID] {
		if op.OpType == "resolve" {
			t.Errorf("expected no resolve op for the rejected identifier, got body %s", op.Body)
		}
	}

	// The accepted one round-trips unchanged — no truncation on the way in.
	comments, err := s.Query.Comments(writ.CommentFilter{SubjectID: reviewID})
	if err != nil {
		t.Fatalf("Query.Comments failed: %v", err)
	}
	var found bool
	for _, c := range comments {
		if c.ObjectID != acceptedID {
			continue
		}
		found = true
		if c.Comment.ResolvedBy != atLimit {
			t.Errorf("expected the 320-code-point value to round-trip unchanged, got %d characters", len(c.Comment.ResolvedBy))
		}
	}
	if !found {
		t.Errorf("accepted comment %s not found in query results", acceptedID)
	}
}

// TestCommentsResolvePersonIDBoundAppliesAfterNFC pins the rule
// spec/identifiers.md §Length bounds states: the bound applies to the
// normalized value, not the value as written. NFC composition shrinks, so an
// identifier that is 321 code points as the caller spells it and 320 once
// composed is inside the bound and MUST be accepted. Checking the raw input
// would refuse it, and two readers checking at different points would disagree
// about the same identifier.
//
// This is the axis TestCommentsResolvePersonIDBoundCountsCodePoints cannot
// reach: there the value is precomposed U+00E9, which is already NFC, so
// normalization does not move the count at all.
func TestCommentsResolvePersonIDBoundAppliesAfterNFC(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)

	s, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	headHash := runGitCmd(t, repoDir, "rev-parse", "HEAD")[:40]
	reviewID, err := s.Reviews.Create(ctx, writ.NewReview{
		Title: "Decomposed Person ID Review",
		Base:  headHash,
		Head:  headHash,
	})
	if err != nil {
		t.Fatalf("Create review failed: %v", err)
	}

	// 319 precomposed e-acute plus one written as e + U+0301: 321 code points
	// as spelled, 320 once composed.
	raw := strings.Repeat("\u00e9", 319) + "e\u0301"
	if n := utf8.RuneCountInString(raw); n != 321 {
		t.Fatalf("test setup: raw value is %d code points, want 321", n)
	}
	composed := writ.NormalizePerson("email:" + raw)
	if n := utf8.RuneCountInString(composed); n != len("email:")+320 {
		t.Fatalf("test setup: normalized identifier is %d code points, want %d",
			utf8.RuneCountInString(composed), len("email:")+320)
	}

	commentID, err := s.Reviews.Comment(ctx, reviewID, writ.NewComment{Text: "Composes to the bound"})
	if err != nil {
		t.Fatalf("Comment failed: %v", err)
	}
	if err := s.Comments.Resolve(ctx, commentID, writ.CommentResolve{
		Resolved:   true,
		ResolvedBy: "email:" + raw,
	}); err != nil {
		t.Fatalf("Resolve with a 321-code-point value composing to 320 failed: %v — "+
			"the bound applies after normalization, not before: %v", err, err)
	}

	// What was written is the normalized form, and the schema accepts it: the
	// op body carries 320 code points, never the 321 the caller spelled.
	ident, _ := identity.ParseWriterID("0123456789abcdef")
	dagStore, err := dag.Open(repoDir, identity.Identity{WriterID: ident})
	if err != nil {
		t.Fatalf("dag.Open failed: %v", err)
	}
	enumRes, err := dagStore.Enumerate()
	if err != nil {
		t.Fatalf("dagStore.Enumerate failed: %v", err)
	}
	var resolveOps int
	for _, op := range enumRes.Ops[commentID] {
		if op.OpType != "resolve" {
			continue
		}
		resolveOps++
		if err := codec.ValidateBody(op.Envelope, nil); err != nil {
			t.Errorf("the accepted identifier produced an op the comment schema rejects: %v", err)
		}
	}
	if resolveOps != 1 {
		t.Fatalf("expected 1 resolve op, got %d", resolveOps)
	}

	// And it is the same person as the fully precomposed spelling.
	comments, err := s.Query.Comments(writ.CommentFilter{SubjectID: reviewID})
	if err != nil {
		t.Fatalf("Query.Comments failed: %v", err)
	}
	want := writ.NormalizePerson("email:" + strings.Repeat("\u00e9", 320))
	var found bool
	for _, c := range comments {
		if c.ObjectID != commentID {
			continue
		}
		found = true
		if c.Comment.ResolvedBy != want {
			t.Errorf("the decomposed spelling folded to %q, want %q — "+
				"the two spellings must be one person", c.Comment.ResolvedBy, want)
		}
	}
	if !found {
		t.Errorf("comment %s not found in query results", commentID)
	}
}

// TestCommentsResolvePersonIDBoundCountsCodePoints pins the unit the person-id
// bound is measured in. JSON Schema maxLength counts code points, so the engine
// guard has to as well: 320 "é" characters are 640 bytes, so a byte-counting
// guard would refuse an identifier spec/schemas/identifiers.schema.json accepts,
// and the engine would disagree with the conformance corpus on every non-ASCII
// person identifier. The ASCII bracket in
// TestCommentsResolvePersonIDLengthBound cannot see this: there bytes and code
// points coincide.
func TestCommentsResolvePersonIDBoundCountsCodePoints(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)

	s, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	headHash := runGitCmd(t, repoDir, "rev-parse", "HEAD")[:40]
	reviewID, err := s.Reviews.Create(ctx, writ.NewReview{
		Title: "Multi-byte Person ID Review",
		Base:  headHash,
		Head:  headHash,
	})
	if err != nil {
		t.Fatalf("Create review failed: %v", err)
	}

	atLimitValue := strings.Repeat("é", 320)
	overLimitValue := strings.Repeat("é", 321)
	if len(atLimitValue) != 640 || len(overLimitValue) != 642 {
		t.Fatalf("test setup: values are %d and %d bytes, want 640 and 642 — a byte-counting guard must see both as over-length",
			len(atLimitValue), len(overLimitValue))
	}
	// Spelled out because this test depends on it: U+00E9 is precomposed and
	// already NFC, so normalization does not change the count and the code
	// points here are the code points the bound sees. The decomposed spelling,
	// where it does change, is TestCommentsResolvePersonIDBoundAppliesAfterNFC.
	if n := utf8.RuneCountInString(atLimitValue); n != 320 {
		t.Fatalf("test setup: at-limit value is %d code points, want 320", n)
	}
	atLimit := "email:" + atLimitValue
	overLimit := "email:" + overLimitValue

	// 320 code points is at the bound and accepted, however many bytes that is.
	acceptedID, err := s.Reviews.Comment(ctx, reviewID, writ.NewComment{Text: "At the bound"})
	if err != nil {
		t.Fatalf("Comment failed: %v", err)
	}
	if err := s.Comments.Resolve(ctx, acceptedID, writ.CommentResolve{
		Resolved:   true,
		ResolvedBy: atLimit,
	}); err != nil {
		t.Fatalf("Resolve with a 320-code-point (640-byte) identifier failed: %v", err)
	}

	// One code point over, and the write is refused — counted in code points,
	// so the error names 321 rather than the byte length.
	rejectedID, err := s.Reviews.Comment(ctx, reviewID, writ.NewComment{Text: "Over the bound"})
	if err != nil {
		t.Fatalf("Comment failed: %v", err)
	}
	err = s.Comments.Resolve(ctx, rejectedID, writ.CommentResolve{
		Resolved:   true,
		ResolvedBy: overLimit,
	})
	if err == nil {
		t.Fatal("expected Resolve to reject a 321-code-point identifier, got nil error")
	}
	if !strings.Contains(err.Error(), "321") || !strings.Contains(err.Error(), "320") {
		t.Errorf("expected an error counting 321 code points against the 320-character limit, got %q", err)
	}

	// What the guard accepted, comment.schema.json accepts too: the whole point
	// of counting the same unit is that the engine and the conformance corpus
	// never disagree about a person identifier.
	ident, _ := identity.ParseWriterID("0123456789abcdef")
	dagStore, err := dag.Open(repoDir, identity.Identity{WriterID: ident})
	if err != nil {
		t.Fatalf("dag.Open failed: %v", err)
	}
	enumRes, err := dagStore.Enumerate()
	if err != nil {
		t.Fatalf("dagStore.Enumerate failed: %v", err)
	}
	var resolveOps int
	for _, op := range enumRes.Ops[acceptedID] {
		if op.OpType != "resolve" {
			continue
		}
		resolveOps++
		if err := codec.ValidateBody(op.Envelope, nil); err != nil {
			t.Errorf("the accepted 320-code-point identifier produced an op the comment schema rejects: %v", err)
		}
	}
	if resolveOps != 1 {
		t.Fatalf("expected 1 resolve op on the accepted comment, got %d", resolveOps)
	}

	// The accepted identifier round-trips unchanged — not truncated to 320
	// bytes, which would corrupt it mid-rune.
	comments, err := s.Query.Comments(writ.CommentFilter{SubjectID: reviewID})
	if err != nil {
		t.Fatalf("Query.Comments failed: %v", err)
	}
	var found bool
	for _, c := range comments {
		if c.ObjectID != acceptedID {
			continue
		}
		found = true
		if c.Comment.ResolvedBy != atLimit {
			t.Errorf("expected the 320-code-point identifier to round-trip unchanged, got %d bytes", len(c.Comment.ResolvedBy))
		}
	}
	if !found {
		t.Errorf("accepted comment %s not found in query results", acceptedID)
	}
}
