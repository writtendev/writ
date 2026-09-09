package writ_test

import (
	"sort"
	"testing"
	"time"

	"github.com/writtendev/writ/engine"
)

func TestReadStateLifecycle(t *testing.T) {
	store, ctx, _ := openStoreWithCoreSchema(t)

	// The schema object installing the vocabulary is an object like any
	// other, and starts out unread. Mark it read so every assertion below
	// is about the two widgets alone.
	if err := store.ReadState.Mark(ctx, coreSchemaObjectID); err != nil {
		t.Fatalf("Mark schema object failed: %v", err)
	}

	// 1. Create two widgets
	w1, err := store.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Widget One"},
	})
	if err != nil {
		t.Fatalf("Create w1 failed: %v", err)
	}
	w2, err := store.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Widget Two"},
	})
	if err != nil {
		t.Fatalf("Create w2 failed: %v", err)
	}

	// 2. Both widgets should initially be unread
	unread, err := store.ReadState.Unread(ctx)
	if err != nil {
		t.Fatalf("Unread all failed: %v", err)
	}
	sort.Strings(unread)
	expectedUnread := []string{w1, w2}
	sort.Strings(expectedUnread)
	if len(unread) != 2 || unread[0] != expectedUnread[0] || unread[1] != expectedUnread[1] {
		t.Fatalf("expected unread [%s, %s], got %+v", w1, w2, unread)
	}

	// 3. Mark w1 as read
	if err := store.ReadState.Mark(ctx, w1); err != nil {
		t.Fatalf("Mark w1 failed: %v", err)
	}

	// Unread should now return only w2
	unreadAfterMark, err := store.ReadState.Unread(ctx)
	if err != nil {
		t.Fatalf("Unread after mark failed: %v", err)
	}
	if len(unreadAfterMark) != 1 || unreadAfterMark[0] != w2 {
		t.Fatalf("expected unread [%s], got %+v", w2, unreadAfterMark)
	}

	// Querying specific IDs
	unreadSpecific, err := store.ReadState.Unread(ctx, w1, w2)
	if err != nil {
		t.Fatalf("Unread specific failed: %v", err)
	}
	if len(unreadSpecific) != 1 || unreadSpecific[0] != w2 {
		t.Fatalf("expected unread [%s], got %+v", w2, unreadSpecific)
	}

	// 4. Update w1 (advancing its updated_at timestamp)
	time.Sleep(1100 * time.Millisecond) // Ensure Unix timestamp increments
	newTitle := "Widget One (Updated)"
	if err := store.Objects.Apply(ctx, w1, writ.NewOp{
		Type:   "update",
		Fields: map[string]any{"title": newTitle},
	}); err != nil {
		t.Fatalf("Update w1 failed: %v", err)
	}

	// Now w1 should be unread again because updated_at > last_read_at
	unreadAfterUpdate, err := store.ReadState.Unread(ctx)
	if err != nil {
		t.Fatalf("Unread after update failed: %v", err)
	}
	sort.Strings(unreadAfterUpdate)
	if len(unreadAfterUpdate) != 2 || unreadAfterUpdate[0] != expectedUnread[0] || unreadAfterUpdate[1] != expectedUnread[1] {
		t.Fatalf("expected both unread after update, got %+v", unreadAfterUpdate)
	}

	// 5. Mark both as read
	if err := store.ReadState.Mark(ctx, w1); err != nil {
		t.Fatalf("Mark w1 failed: %v", err)
	}
	if err := store.ReadState.Mark(ctx, w2); err != nil {
		t.Fatalf("Mark w2 failed: %v", err)
	}

	unreadClean, err := store.ReadState.Unread(ctx)
	if err != nil {
		t.Fatalf("Unread clean failed: %v", err)
	}
	if len(unreadClean) != 0 {
		t.Fatalf("expected 0 unread, got %+v", unreadClean)
	}

	// 6. Clear read mark on w1
	if err := store.ReadState.Clear(ctx, w1); err != nil {
		t.Fatalf("Clear w1 failed: %v", err)
	}

	unreadAfterClear, err := store.ReadState.Unread(ctx)
	if err != nil {
		t.Fatalf("Unread after clear failed: %v", err)
	}
	if len(unreadAfterClear) != 1 || unreadAfterClear[0] != w1 {
		t.Fatalf("expected unread [%s] after clear, got %+v", w1, unreadAfterClear)
	}
}
