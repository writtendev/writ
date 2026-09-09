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
		SubjectType: "widget",
		SubjectID:   "w-100",
		InReplyTo:   "n-200",
		Anchor:      &anc,
		Text:        "Draft line note text",
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
	if gotDraft.ID != draftID || gotDraft.Text != "Draft line note text" || gotDraft.SubjectID != "w-100" || gotDraft.InReplyTo != "n-200" || gotDraft.Anchor == nil || gotDraft.Anchor.Old.Path != "test.go" {
		t.Fatalf("unexpected draft read: %+v", gotDraft)
	}

	// 3. Update draft
	gotDraft.Text = "Updated draft line note text"
	updatedID, err := store.Drafts.Save(ctx, gotDraft)
	if err != nil {
		t.Fatalf("Drafts.Save update failed: %v", err)
	}
	if updatedID != draftID {
		t.Fatalf("expected draft ID %s, got %s", draftID, updatedID)
	}

	// 4. List drafts
	d2 := writ.Draft{
		SubjectType: "gadget",
		SubjectID:   "g-300",
		Text:        "Gadget draft",
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

	widgetDrafts, err := store.Drafts.List(ctx, writ.DraftFilter{SubjectType: "widget"})
	if err != nil {
		t.Fatalf("Drafts.List widget failed: %v", err)
	}
	if len(widgetDrafts) != 1 || widgetDrafts[0].ID != draftID {
		t.Fatalf("expected 1 widget draft with ID %s, got %+v", draftID, widgetDrafts)
	}

	// 5. Discard draft
	if err := store.Drafts.Discard(ctx, draftID); err != nil {
		t.Fatalf("Drafts.Discard failed: %v", err)
	}
	if _, err := store.Drafts.Get(ctx, draftID); err != writ.ErrNotFound {
		t.Fatalf("expected ErrNotFound after discard, got %v", err)
	}
}

// publishedTypeSchemaSrc declares the object type Drafts.Publish writes.
// Every other object type these tests use is one the test itself picked;
// this one is not. engine/drafts.go still names its object type in Go — the
// last hard-coded object type outside `schema` — so a repository that
// publishes a draft must have exactly that type declared for the write to
// be accepted. This constant exists only to satisfy that literal, and goes
// when it does.
const publishedTypeSchemaSrc = `namespace acme
description "The type a published draft lands under"

type comment {
  description "The object type engine/drafts.go writes when it publishes a draft"

  op create 1 {
    text         text     lww
    subject      untyped  create-once
    in_reply_to  untyped  create-once
    anchor       untyped  create-once
  }
}
`

// applyPublishedTypeSchema installs publishedTypeSchemaSrc, which every
// test calling Drafts.Publish needs and no other test does.
func applyPublishedTypeSchema(t *testing.T, ctx context.Context, store *writ.Store) {
	t.Helper()
	envs := compileTestSchema(t, "sch-published", publishedTypeSchemaSrc)
	if err := store.ApplySchema(ctx, envs); err != nil {
		t.Fatalf("ApplySchema (published type) failed: %v", err)
	}
}

func TestDraftPublishOnWidget(t *testing.T) {
	store, ctx, _ := openStoreWithCoreSchema(t)
	applyPublishedTypeSchema(t, ctx, store)

	// Create a widget
	widgetID, err := store.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Publish Widget Test"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(widget) failed: %v", err)
	}

	// Save draft on the widget
	draftID, err := store.Drafts.Save(ctx, writ.Draft{
		SubjectType: "widget",
		SubjectID:   widgetID,
		Text:        "Published widget note text",
	})
	if err != nil {
		t.Fatalf("Drafts.Save failed: %v", err)
	}

	// Publish draft
	publishedID, err := store.Drafts.Publish(ctx, draftID)
	if err != nil {
		t.Fatalf("Drafts.Publish failed: %v", err)
	}
	if publishedID == "" {
		t.Fatalf("expected non-empty published object id")
	}

	// Verify the published object folds with the right subject and text.
	published, err := store.Objects.Get(ctx, publishedID)
	if err != nil {
		t.Fatalf("Objects.Get(published) failed: %v", err)
	}
	if published.Fields["text"] != "Published widget note text" {
		t.Fatalf("unexpected published object: %+v", published)
	}
	subject := decodePublishedSubject(t, published.Fields["subject"])
	if subject["object_type"] != "widget" || subject["object_id"] != widgetID {
		t.Fatalf("unexpected published subject: %+v", subject)
	}

	// Verify draft is deleted
	if _, err := store.Drafts.Get(ctx, draftID); err != writ.ErrNotFound {
		t.Fatalf("expected draft to be deleted after publish, got %v", err)
	}
}

func TestDraftPublishOnGadget(t *testing.T) {
	store, ctx, _ := openStoreWithCoreSchema(t)
	applyPublishedTypeSchema(t, ctx, store)

	// Create a gadget
	gadgetID, err := store.Objects.Create(ctx, "gadget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Publish Gadget Test"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(gadget) failed: %v", err)
	}

	// Save draft on the gadget
	draftID, err := store.Drafts.Save(ctx, writ.Draft{
		SubjectType: "gadget",
		SubjectID:   gadgetID,
		Text:        "Published gadget note text",
	})
	if err != nil {
		t.Fatalf("Drafts.Save failed: %v", err)
	}

	// Publish draft
	publishedID, err := store.Drafts.Publish(ctx, draftID)
	if err != nil {
		t.Fatalf("Drafts.Publish failed: %v", err)
	}
	if publishedID == "" {
		t.Fatalf("expected non-empty published object id")
	}

	// Verify the published object folds with the right subject and text.
	published, err := store.Objects.Get(ctx, publishedID)
	if err != nil {
		t.Fatalf("Objects.Get(published) failed: %v", err)
	}
	if published.Fields["text"] != "Published gadget note text" {
		t.Fatalf("unexpected published object: %+v", published)
	}
	subject := decodePublishedSubject(t, published.Fields["subject"])
	if subject["object_type"] != "gadget" || subject["object_id"] != gadgetID {
		t.Fatalf("unexpected published subject: %+v", subject)
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

	applyCoreSchema(t, ctx, sA)
	applyPublishedTypeSchema(t, ctx, sA)

	// Alice creates a widget
	widgetID, err := sA.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Draft Leak Test Widget"},
	})
	if err != nil {
		t.Fatalf("Alice Objects.Create(widget) failed: %v", err)
	}

	// Alice saves a draft containing a unique sentinel string
	const sentinel = "SENTINEL_DRAFT_SECRET_NEVER_LEAK_12345"
	draftID, err := sA.Drafts.Save(ctx, writ.Draft{
		SubjectType: "widget",
		SubjectID:   widgetID,
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
	publishedID, err := sA.Drafts.Publish(ctx, draftID)
	if err != nil {
		t.Fatalf("Alice Drafts.Publish failed: %v", err)
	}
	if publishedID == "" {
		t.Fatalf("expected non-empty published object id")
	}

	// Verify draft is deleted from local DB
	if _, err := sA.Drafts.Get(ctx, draftID); err != writ.ErrNotFound {
		t.Fatalf("expected draft to be deleted from Alice local DB, got %v", err)
	}

	// Sync Alice again -> now the published object reaches the remote
	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync after publish failed: %v", err)
	}
	if _, err := sB.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Bob Sync after publish failed: %v", err)
	}

	// Bob now sees the published object
	bobPublished, err := sB.Objects.Get(ctx, publishedID)
	if err != nil {
		t.Fatalf("Bob Objects.Get(published) failed: %v", err)
	}
	if bobPublished.Fields["text"] != sentinel {
		t.Fatalf("Bob did not receive the published object: %+v", bobPublished)
	}
}

// TestDraftPublish_UnknownSubjectRefused verifies Publish refuses to
// publish a draft whose subject does not exist, rather than writing a
// permanently signed op against a dangling subject (round 1 MAJOR-1(a)).
// The draft must survive the failed publish so the caller can retry once
// the subject exists.
func TestDraftPublish_UnknownSubjectRefused(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	draftID, err := store.Drafts.Save(ctx, writ.Draft{
		SubjectType: "widget",
		SubjectID:   "does-not-exist",
		Text:        "A note on a subject that was never created",
	})
	if err != nil {
		t.Fatalf("Drafts.Save failed: %v", err)
	}

	if _, err := store.Drafts.Publish(ctx, draftID); err != writ.ErrNotFound {
		t.Fatalf("Drafts.Publish on unknown subject: got err %v, want ErrNotFound", err)
	}

	// The draft must still be there to retry from: Publish must not discard
	// it on a failed publish.
	if _, err := store.Drafts.Get(ctx, draftID); err != nil {
		t.Fatalf("expected draft to survive a refused publish, got %v", err)
	}
}

// TestDraftPublish_UnknownInReplyToRefused mirrors the subject check above
// for the in_reply_to existence check (round 1 MAJOR-1(b)).
func TestDraftPublish_UnknownInReplyToRefused(t *testing.T) {
	store, ctx, _ := openStoreWithCoreSchema(t)

	widgetID, err := store.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Reply Existence Test"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(widget) failed: %v", err)
	}

	draftID, err := store.Drafts.Save(ctx, writ.Draft{
		SubjectType: "widget",
		SubjectID:   widgetID,
		InReplyTo:   "does-not-exist",
		Text:        "Reply to an object that was never created",
	})
	if err != nil {
		t.Fatalf("Drafts.Save failed: %v", err)
	}

	if _, err := store.Drafts.Publish(ctx, draftID); err != writ.ErrNotFound {
		t.Fatalf("Drafts.Publish on unknown in_reply_to: got err %v, want ErrNotFound", err)
	}

	if _, err := store.Drafts.Get(ctx, draftID); err != nil {
		t.Fatalf("expected draft to survive a refused publish, got %v", err)
	}
}

// TestDraftPublish_EmptySubjectTypeResolvesRealType verifies that Publish
// resolves the subject's actual object type through the generic Query.Object
// lookup rather than coercing an unset SubjectType to a fixed literal (round
// 1 MAJOR-1(c)). A draft saved without an explicit SubjectType against a
// gadget must publish against a gadget, not against whatever type the code
// happened to name first.
func TestDraftPublish_EmptySubjectTypeResolvesRealType(t *testing.T) {
	store, ctx, _ := openStoreWithCoreSchema(t)
	applyPublishedTypeSchema(t, ctx, store)

	gadgetID, err := store.Objects.Create(ctx, "gadget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Untyped Draft Subject Test"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(gadget) failed: %v", err)
	}

	draftID, err := store.Drafts.Save(ctx, writ.Draft{
		SubjectID: gadgetID,
		Text:      "A note with no explicit subject type",
	})
	if err != nil {
		t.Fatalf("Drafts.Save failed: %v", err)
	}

	publishedID, err := store.Drafts.Publish(ctx, draftID)
	if err != nil {
		t.Fatalf("Drafts.Publish failed: %v", err)
	}

	published, err := store.Objects.Get(ctx, publishedID)
	if err != nil {
		t.Fatalf("Objects.Get(published) failed: %v", err)
	}
	subject := decodePublishedSubject(t, published.Fields["subject"])
	if subject["object_type"] != "gadget" || subject["object_id"] != gadgetID {
		t.Fatalf("unexpected published subject: %+v, want object_type=gadget object_id=%s", subject, gadgetID)
	}
}

// TestDraftPublish_AnySchemaDeclaredType verifies Publish has no allowlist
// of subject types at all: a draft against a schema-declared type nothing
// in Go has ever heard of publishes exactly like any other (round 1 MEDIUM
// finding on engine/drafts.go:159).
func TestDraftPublish_AnySchemaDeclaredType(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	envs := compileTestSchema(t, "sch-standup", testSchemaSrc)
	if err := store.ApplySchema(ctx, envs); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	applyPublishedTypeSchema(t, ctx, store)

	standupID, err := store.Objects.Create(ctx, "standup", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Daily Standup"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(standup) failed: %v", err)
	}

	draftID, err := store.Drafts.Save(ctx, writ.Draft{
		SubjectType: "standup",
		SubjectID:   standupID,
		Text:        "A note on a type nothing in Go names",
	})
	if err != nil {
		t.Fatalf("Drafts.Save failed: %v", err)
	}

	publishedID, err := store.Drafts.Publish(ctx, draftID)
	if err != nil {
		t.Fatalf("Drafts.Publish failed: %v", err)
	}

	published, err := store.Objects.Get(ctx, publishedID)
	if err != nil {
		t.Fatalf("Objects.Get(published) failed: %v", err)
	}
	subject := decodePublishedSubject(t, published.Fields["subject"])
	if subject["object_type"] != "standup" || subject["object_id"] != standupID {
		t.Fatalf("unexpected published subject: %+v, want object_type=standup object_id=%s", subject, standupID)
	}
}

// decodePublishedSubject decodes a published object's "subject" field,
// which the generic fold returns as raw JSON bytes (json.RawMessage) rather
// than a decoded map: "subject" declares no value_type, so create-once's
// byte-exact-preservation rule (spec/fold.md §5.2) keeps it verbatim.
func decodePublishedSubject(t *testing.T, raw any) map[string]any {
	t.Helper()
	b, ok := raw.(json.RawMessage)
	if !ok {
		if bs, ok2 := raw.([]byte); ok2 {
			b = bs
		} else {
			t.Fatalf("published subject field is %T, want json.RawMessage", raw)
		}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal published subject: %v", err)
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
