package writ_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/writtendev/writ/engine"
)

func TestQueryFullSuite(t *testing.T) {
	s, ctx, _ := openStoreWithCoreSchema(t)

	// 1. Create multiple widgets
	w1, err := s.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "First Feature Widget", "description": "Alpha feature"},
	})
	if err != nil {
		t.Fatalf("Create w1: %v", err)
	}

	w2, err := s.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Second Bugfix Widget", "description": "Beta bugfix"},
	})
	if err != nil {
		t.Fatalf("Create w2: %v", err)
	}

	if err := s.Objects.Apply(ctx, w2, writ.NewOp{
		Type:   "set-status",
		Fields: map[string]any{"status": "closed", "reason": "superseded"},
	}); err != nil {
		t.Fatalf("SetStatus w2: %v", err)
	}

	// 2. Create multiple gadgets
	g1, err := s.Objects.Create(ctx, "gadget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Gadget One", "description": "Important gadget"},
	})
	if err != nil {
		t.Fatalf("Create g1: %v", err)
	}
	if err := s.Objects.Apply(ctx, g1, writ.NewOp{
		Type:   "assign",
		Fields: map[string]any{"add": []string{"user:alice"}},
	}); err != nil {
		t.Fatalf("Assign g1: %v", err)
	}
	if err := s.Objects.Apply(ctx, g1, writ.NewOp{
		Type:   "tag",
		Fields: map[string]any{"add": []string{"frontend"}},
	}); err != nil {
		t.Fatalf("Tag g1: %v", err)
	}

	g2, err := s.Objects.Create(ctx, "gadget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Gadget Two", "description": "Backend gadget"},
	})
	if err != nil {
		t.Fatalf("Create g2: %v", err)
	}
	if err := s.Objects.Apply(ctx, g2, writ.NewOp{
		Type:   "set-state",
		Fields: map[string]any{"state": "closed", "reason": "fixed"},
	}); err != nil {
		t.Fatalf("SetState g2: %v", err)
	}
	if err := s.Objects.Apply(ctx, g2, writ.NewOp{
		Type:   "assign",
		Fields: map[string]any{"add": []string{"user:bob"}},
	}); err != nil {
		t.Fatalf("Assign g2: %v", err)
	}

	// 3. Notes on w1
	n1, err := s.Objects.Create(ctx, "note", writ.NewOp{
		Type: "create",
		Fields: map[string]any{
			"text":    "Note 1 on w1",
			"subject": map[string]string{"object_type": "widget", "object_id": w1},
		},
	})
	if err != nil {
		t.Fatalf("Note w1: %v", err)
	}
	n2, err := s.Objects.Create(ctx, "note", writ.NewOp{
		Type: "create",
		Fields: map[string]any{
			"text":        "Reply to note 1",
			"subject":     map[string]string{"object_type": "widget", "object_id": w1},
			"in_reply_to": n1,
		},
	})
	if err != nil {
		t.Fatalf("Reply w1: %v", err)
	}

	// Edit n1
	if err := s.Objects.Apply(ctx, n1, writ.NewOp{
		Type:   "edit",
		Fields: map[string]any{"text": "Edited Note 1 on w1"},
	}); err != nil {
		t.Fatalf("Edit n1: %v", err)
	}

	// Test Query.Objects, filtered by type
	allWidgets, err := s.Query.Objects(writ.ObjectFilter{Type: []string{"widget"}, OrderBy: writ.OrderByCreatedAtAsc})
	if err != nil {
		t.Fatalf("Query.Objects(widget): %v", err)
	}
	if len(allWidgets) != 2 {
		t.Errorf("expected 2 widgets, got %d", len(allWidgets))
	}

	// Test Objects.Get point lookup and folded field values
	objW1, err := s.Objects.Get(ctx, w1)
	if err != nil {
		t.Fatalf("Objects.Get(w1): %v", err)
	}
	if objW1.Fields["title"] != "First Feature Widget" {
		t.Errorf("got title %q", objW1.Fields["title"])
	}

	objW2, err := s.Objects.Get(ctx, w2)
	if err != nil {
		t.Fatalf("Objects.Get(w2): %v", err)
	}
	if objW2.Fields["status"] != "closed" {
		t.Errorf("expected w2 status closed, got %v", objW2.Fields["status"])
	}

	// Test Objects.Get not found
	_, err = s.Objects.Get(ctx, "non-existent-id")
	if !errors.Is(err, writ.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing object, got: %v", err)
	}

	// Test Objects.Get: g1 never had a set-state op, so it folds with the
	// empty default state — the schema declares no default, and nothing
	// invents one.
	objG1, err := s.Objects.Get(ctx, g1)
	if err != nil {
		t.Fatalf("Objects.Get(g1): %v", err)
	}
	if objG1.Fields["state"] != nil && objG1.Fields["state"] != "" {
		t.Errorf("expected g1 to have no state, got %v", objG1.Fields["state"])
	}
	assignees, _ := objG1.Fields["assignees"].([]string)
	if len(assignees) != 1 || assignees[0] != "user:alice" {
		t.Errorf("expected g1 assignees [user:alice], got %v", objG1.Fields["assignees"])
	}

	objG2, err := s.Objects.Get(ctx, g2)
	if err != nil {
		t.Fatalf("Objects.Get(g2): %v", err)
	}
	if objG2.Fields["state"] != "closed" {
		t.Errorf("got state %q, want 'closed'", objG2.Fields["state"])
	}

	// Test Query.Objects cross-type. The schema object declaring all three
	// types is itself an object, and is counted here like any other.
	objects, err := s.Query.Objects(writ.ObjectFilter{})
	if err != nil {
		t.Fatalf("Query.Objects: %v", err)
	}
	if len(objects) != 7 { // 2 widgets + 2 gadgets + 2 notes + 1 schema
		t.Errorf("expected 7 objects total, got %d", len(objects))
	}

	// Test note content and threading via Fields, since the per-type
	// thread reader no longer exists.
	objN1, err := s.Objects.Get(ctx, n1)
	if err != nil {
		t.Fatalf("Objects.Get(n1): %v", err)
	}
	if objN1.Fields["text"] != "Edited Note 1 on w1" {
		t.Errorf("expected edited text, got %v", objN1.Fields["text"])
	}
	objN2, err := s.Objects.Get(ctx, n2)
	if err != nil {
		t.Fatalf("Objects.Get(n2): %v", err)
	}
	// in_reply_to declares no normalizing value type, so create-once's
	// byte-exact-preservation rule (spec/fold.md §5.2) returns it as raw
	// JSON bytes rather than a decoded string.
	inReplyToRaw, _ := objN2.Fields["in_reply_to"].(json.RawMessage)
	var inReplyTo string
	if err := json.Unmarshal(inReplyToRaw, &inReplyTo); err != nil {
		t.Fatalf("unmarshal in_reply_to: %v", err)
	}
	if inReplyTo != n1 {
		t.Errorf("expected n2.in_reply_to = %s, got %v", n1, inReplyTo)
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

	// The vocabulary goes in, and is projected, before the measurement
	// below: the schema object is an object too, so folding it now is what
	// leaves the explicit Refresh with exactly one object to touch.
	applyCoreSchema(t, ctx, s)
	if _, err := s.Refresh(ctx); err != nil {
		t.Fatalf("Refresh after ApplySchema failed: %v", err)
	}

	// Write without auto-refresh
	id, err := s.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Manual Refresh Widget"},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Without refresh, the projection has not folded the new widget yet —
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
	// type-table columns (o_widget.f_title here), not from ObjectResult's
	// own objects-table-only metadata — so, unlike res above, this actually
	// asserts a folded field value made it into the cache (round 1 minor
	// finding: no engine-level test asserted a folded field value out of
	// the projection any more once title assertions moved onto Objects.Get,
	// which deliberately bypasses it).
	byText, err := s.Query.Objects(writ.ObjectFilter{Text: "Manual Refresh Widget"})
	if err != nil {
		t.Fatalf("Query.Objects(Text) after manual refresh failed: %v", err)
	}
	if len(byText) != 1 || byText[0].ObjectID != id {
		t.Errorf("Query.Objects(Text=%q) = %+v, want exactly [%s] — the projection's materialized title must match", "Manual Refresh Widget", byText, id)
	}

	// Objects.Get, which folds from the DAG directly, finds it regardless.
	obj, err := s.Objects.Get(ctx, id)
	if err != nil {
		t.Fatalf("Objects.Get failed: %v", err)
	}
	if obj.Fields["title"] != "Manual Refresh Widget" {
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

	applyCoreSchema(t, ctx, s)

	w1, err := s.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Zebra Crossing Widget"},
	})
	if err != nil {
		t.Fatalf("Create widget failed: %v", err)
	}
	n1, err := s.Objects.Create(ctx, "note", writ.NewOp{
		Type: "create",
		Fields: map[string]any{
			"text":    "first note",
			"subject": map[string]string{"object_type": "widget", "object_id": w1},
		},
	})
	if err != nil {
		t.Fatalf("Note failed: %v", err)
	}
	// The delete op body carries no content: tombstone semantics come from
	// the op type itself (spec/fold.md §5.8), not a field in its body.
	if err := s.Objects.Apply(ctx, n1, writ.NewOp{
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
	// note, exactly as it does on a freshly built descriptor.
	objects, err := s2.Query.Objects(writ.ObjectFilter{})
	if err != nil {
		t.Fatalf("Query.Objects (default) failed: %v", err)
	}
	for _, o := range objects {
		if o.ObjectID == n1 {
			t.Errorf("Query.Objects (default) on a warm reopen included the soft-deleted note %s: %+v", n1, objects)
		}
	}
	foundWidget := false
	for _, o := range objects {
		if o.ObjectID == w1 {
			foundWidget = true
		}
	}
	if !foundWidget {
		t.Errorf("Query.Objects (default) on a warm reopen did not include the widget %s: %+v", w1, objects)
	}

	// IncludeDeleted: true must still surface it.
	withDeleted, err := s2.Query.Objects(writ.ObjectFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("Query.Objects (IncludeDeleted) failed: %v", err)
	}
	foundDeleted := false
	for _, o := range withDeleted {
		if o.ObjectID == n1 {
			foundDeleted = true
		}
	}
	if !foundDeleted {
		t.Errorf("Query.Objects (IncludeDeleted: true) on a warm reopen did not include the soft-deleted note %s: %+v", n1, withDeleted)
	}

	// Text search over the descriptor-driven clause must still match.
	textResults, err := s2.Query.Objects(writ.ObjectFilter{Text: "Zebra"})
	if err != nil {
		t.Fatalf("Query.Objects (Text) failed: %v", err)
	}
	if len(textResults) != 1 || textResults[0].ObjectID != w1 {
		t.Errorf("Query.Objects (Text: Zebra) on a warm reopen = %+v, want [%s]", textResults, w1)
	}
}
