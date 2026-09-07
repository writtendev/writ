package writ_test

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/resolve"
)

func TestDraftsLifecycle(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// 1. Save draft
	anc := resolve.Anchor{
		Version: 1,
		Old: &resolve.SideAnchor{
			Commit: "0000000000000000000000000000000000000001",
			Path:   "test.go",
		},
	}
	d := writ.Draft{
		SubjectType: "review",
		SubjectID:   "rev-100",
		InReplyTo:   "comm-200",
		Anchor:      &anc,
		Text:        "Draft line comment text",
	}

	draftID, err := store.Drafts.Save(ctx, d)
	if err != nil {
		t.Fatalf("Drafts.Save failed: %v", err)
	}
	if draftID == "" {
		t.Fatalf("expected non-empty draft ID")
	}

	// 2. Get draft
	gotDraft, err := store.Drafts.Get(ctx, draftID)
	if err != nil {
		t.Fatalf("Drafts.Get failed: %v", err)
	}
	if gotDraft.ID != draftID || gotDraft.Text != "Draft line comment text" || gotDraft.SubjectID != "rev-100" || gotDraft.InReplyTo != "comm-200" || gotDraft.Anchor == nil || gotDraft.Anchor.Old.Path != "test.go" {
		t.Fatalf("unexpected draft read: %+v", gotDraft)
	}

	// 3. Update draft
	gotDraft.Text = "Updated draft line comment text"
	updatedID, err := store.Drafts.Save(ctx, gotDraft)
	if err != nil {
		t.Fatalf("Drafts.Save update failed: %v", err)
	}
	if updatedID != draftID {
		t.Fatalf("expected draft ID %s, got %s", draftID, updatedID)
	}

	// 4. List drafts
	d2 := writ.Draft{
		SubjectType: "issue",
		SubjectID:   "iss-300",
		Text:        "Issue draft",
	}
	_, err = store.Drafts.Save(ctx, d2)
	if err != nil {
		t.Fatalf("Drafts.Save d2 failed: %v", err)
	}

	allDrafts, err := store.Drafts.List(ctx, writ.DraftFilter{})
	if err != nil {
		t.Fatalf("Drafts.List all failed: %v", err)
	}
	if len(allDrafts) != 2 {
		t.Fatalf("expected 2 drafts, got %d", len(allDrafts))
	}

	reviewDrafts, err := store.Drafts.List(ctx, writ.DraftFilter{SubjectType: "review"})
	if err != nil {
		t.Fatalf("Drafts.List review failed: %v", err)
	}
	if len(reviewDrafts) != 1 || reviewDrafts[0].ID != draftID {
		t.Fatalf("expected 1 review draft with ID %s, got %+v", draftID, reviewDrafts)
	}

	// 5. Discard draft
	if err := store.Drafts.Discard(ctx, draftID); err != nil {
		t.Fatalf("Drafts.Discard failed: %v", err)
	}
	if _, err := store.Drafts.Get(ctx, draftID); err != writ.ErrNotFound {
		t.Fatalf("expected ErrNotFound after discard, got %v", err)
	}
}

func TestDraftPublishReview(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Create a review
	reviewID, err := store.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Publish Review Test"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(review) failed: %v", err)
	}

	// Save draft on the review
	draftID, err := store.Drafts.Save(ctx, writ.Draft{
		SubjectType: "review",
		SubjectID:   reviewID,
		Text:        "Published review comment text",
	})
	if err != nil {
		t.Fatalf("Drafts.Save failed: %v", err)
	}

	// Publish draft
	commentID, err := store.Drafts.Publish(ctx, draftID)
	if err != nil {
		t.Fatalf("Drafts.Publish failed: %v", err)
	}
	if commentID == "" {
		t.Fatalf("expected non-empty commentID")
	}

	// Verify the published comment folds with the right subject and text.
	comment, err := store.Objects.Get(ctx, commentID)
	if err != nil {
		t.Fatalf("Objects.Get(comment) failed: %v", err)
	}
	if comment.ObjectType != "comment" || comment.Fields["text"] != "Published review comment text" {
		t.Fatalf("unexpected published comment: %+v", comment)
	}
	subject := decodeCommentSubject(t, comment.Fields["subject"])
	if subject["object_type"] != "review" || subject["object_id"] != reviewID {
		t.Fatalf("unexpected comment subject: %+v", subject)
	}

	// Verify draft is deleted
	if _, err := store.Drafts.Get(ctx, draftID); err != writ.ErrNotFound {
		t.Fatalf("expected draft to be deleted after publish, got %v", err)
	}
}

func TestDraftPublishIssue(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Create an issue
	issueID, err := store.Objects.Create(ctx, "issue", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Publish Issue Test"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(issue) failed: %v", err)
	}

	// Save draft on the issue
	draftID, err := store.Drafts.Save(ctx, writ.Draft{
		SubjectType: "issue",
		SubjectID:   issueID,
		Text:        "Published issue comment text",
	})
	if err != nil {
		t.Fatalf("Drafts.Save failed: %v", err)
	}

	// Publish draft
	commentID, err := store.Drafts.Publish(ctx, draftID)
	if err != nil {
		t.Fatalf("Drafts.Publish failed: %v", err)
	}
	if commentID == "" {
		t.Fatalf("expected non-empty commentID")
	}

	// Verify the published comment folds with the right subject and text.
	comment, err := store.Objects.Get(ctx, commentID)
	if err != nil {
		t.Fatalf("Objects.Get(comment) failed: %v", err)
	}
	if comment.ObjectType != "comment" || comment.Fields["text"] != "Published issue comment text" {
		t.Fatalf("unexpected published comment: %+v", comment)
	}
	subject := decodeCommentSubject(t, comment.Fields["subject"])
	if subject["object_type"] != "issue" || subject["object_id"] != issueID {
		t.Fatalf("unexpected comment subject: %+v", subject)
	}

	// Verify draft is deleted
	if _, err := store.Drafts.Get(ctx, draftID); err != writ.ErrNotFound {
		t.Fatalf("expected draft to be deleted after publish, got %v", err)
	}
}

func TestDraftsNeverReachSharedRefs(t *testing.T) {
	bareDir, aliceDir, bobDir := setupSyncHarness(t)
	ctx := context.Background()

	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Alice failed: %v", err)
	}
	defer sA.Close()

	// Alice creates a review
	reviewID, err := sA.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Draft Leak Test Review"},
	})
	if err != nil {
		t.Fatalf("Alice Objects.Create(review) failed: %v", err)
	}

	// Alice saves a draft containing a unique sentinel string
	const sentinel = "SENTINEL_DRAFT_SECRET_NEVER_LEAK_12345"
	draftID, err := sA.Drafts.Save(ctx, writ.Draft{
		SubjectType: "review",
		SubjectID:   reviewID,
		Text:        sentinel,
	})
	if err != nil {
		t.Fatalf("Alice Drafts.Save failed: %v", err)
	}

	// Alice syncs to bare remote
	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync failed: %v", err)
	}

	// Bob syncs from bare remote
	sB, err := writ.Open(bobDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Bob failed: %v", err)
	}
	defer sB.Close()

	if _, err := sB.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Bob Sync failed: %v", err)
	}

	// Walk every git ref reachable from refs/writ/** in Alice, Bob, and bare remote
	assertSentinelNotInWritRefs(t, aliceDir, sentinel)
	assertSentinelNotInWritRefs(t, bobDir, sentinel)
	assertSentinelNotInWritRefs(t, bareDir, sentinel)

	// Now Alice publishes the draft
	commentID, err := sA.Drafts.Publish(ctx, draftID)
	if err != nil {
		t.Fatalf("Alice Drafts.Publish failed: %v", err)
	}
	if commentID == "" {
		t.Fatalf("expected non-empty commentID")
	}

	// Verify draft is deleted from local DB
	if _, err := sA.Drafts.Get(ctx, draftID); err != writ.ErrNotFound {
		t.Fatalf("expected draft to be deleted from Alice local DB, got %v", err)
	}

	// Sync Alice again -> now the published comment reaches the remote
	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync after publish failed: %v", err)
	}
	if _, err := sB.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Bob Sync after publish failed: %v", err)
	}

	// Bob now sees the published comment
	bobComment, err := sB.Objects.Get(ctx, commentID)
	if err != nil {
		t.Fatalf("Bob Objects.Get(comment) failed: %v", err)
	}
	if bobComment.Fields["text"] != sentinel {
		t.Fatalf("Bob did not receive published comment: %+v", bobComment)
	}
}

// decodeCommentSubject decodes a comment's "subject" field, which the
// generic fold returns as raw JSON bytes (json.RawMessage) rather than a
// decoded map: "subject" declares no value_type, so create-once's
// byte-exact-preservation rule (spec/fold.md §5.2) keeps it verbatim.
func decodeCommentSubject(t *testing.T, raw any) map[string]any {
	t.Helper()
	b, ok := raw.(json.RawMessage)
	if !ok {
		if bs, ok2 := raw.([]byte); ok2 {
			b = bs
		} else {
			t.Fatalf("comment subject field is %T, want json.RawMessage", raw)
		}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal comment subject: %v", err)
	}
	return m
}

func assertSentinelNotInWritRefs(t *testing.T, repoDir, sentinel string) {
	t.Helper()
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatalf("git.PlainOpen %s: %v", repoDir, err)
	}

	refs, err := repo.References()
	if err != nil {
		t.Fatalf("repo.References %s: %v", repoDir, err)
	}
	defer refs.Close()

	err = refs.ForEach(func(ref *plumbing.Reference) error {
		refName := ref.Name().String()
		if !strings.Contains(refName, "refs/writ/") && !strings.Contains(refName, "writ") {
			return nil
		}

		// Walk commit history from ref tip
		commitIter, err := repo.Log(&git.LogOptions{From: ref.Hash()})
		if err != nil {
			return nil
		}
		defer commitIter.Close()

		return commitIter.ForEach(func(c *object.Commit) error {
			if strings.Contains(c.Message, sentinel) {
				t.Fatalf("sentinel found in commit %s message under ref %s in repo %s", c.Hash, refName, repoDir)
			}

			// Check file contents in commit tree
			tree, err := c.Tree()
			if err != nil {
				return nil
			}

			return tree.Files().ForEach(func(f *object.File) error {
				reader, err := f.Reader()
				if err != nil {
					return nil
				}
				defer reader.Close()

				content, err := io.ReadAll(reader)
				if err != nil {
					return nil
				}

				if strings.Contains(string(content), sentinel) {
					t.Fatalf("sentinel found in file %s at commit %s under ref %s in repo %s", f.Name, c.Hash, refName, repoDir)
				}
				return nil
			})
		})
	})
	if err != nil {
		t.Fatalf("ForEach ref %s: %v", repoDir, err)
	}
}
