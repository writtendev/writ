package writ_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/writtendev/writ/engine"
)

func TestReadStateLifecycle(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// 1. Create two reviews
	rev1, err := store.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Review One"},
	})
	if err != nil {
		t.Fatalf("Create rev1 failed: %v", err)
	}
	rev2, err := store.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Review Two"},
	})
	if err != nil {
		t.Fatalf("Create rev2 failed: %v", err)
	}

	// 2. Both reviews should initially be unread
	unread, err := store.ReadState.Unread(ctx)
	if err != nil {
		t.Fatalf("Unread all failed: %v", err)
	}
	sort.Strings(unread)
	expectedUnread := []string{rev1, rev2}
	sort.Strings(expectedUnread)
	if len(unread) != 2 || unread[0] != expectedUnread[0] || unread[1] != expectedUnread[1] {
		t.Fatalf("expected unread [%s, %s], got %+v", rev1, rev2, unread)
	}

	// 3. Mark rev1 as read
	if err := store.ReadState.Mark(ctx, rev1); err != nil {
		t.Fatalf("Mark rev1 failed: %v", err)
	}

	// Unread should now return only rev2
	unreadAfterMark, err := store.ReadState.Unread(ctx)
	if err != nil {
		t.Fatalf("Unread after mark failed: %v", err)
	}
	if len(unreadAfterMark) != 1 || unreadAfterMark[0] != rev2 {
		t.Fatalf("expected unread [%s], got %+v", rev2, unreadAfterMark)
	}

	// Querying specific IDs
	unreadSpecific, err := store.ReadState.Unread(ctx, rev1, rev2)
	if err != nil {
		t.Fatalf("Unread specific failed: %v", err)
	}
	if len(unreadSpecific) != 1 || unreadSpecific[0] != rev2 {
		t.Fatalf("expected unread [%s], got %+v", rev2, unreadSpecific)
	}

	// 4. Update rev1 (advancing its updated_at timestamp)
	time.Sleep(1100 * time.Millisecond) // Ensure Unix timestamp increments
	newTitle := "Review One (Updated)"
	if err := store.Objects.Apply(ctx, rev1, writ.NewOp{
		Type:   "update",
		Fields: map[string]any{"title": newTitle},
	}); err != nil {
		t.Fatalf("Update rev1 failed: %v", err)
	}

	// Now rev1 should be unread again because updated_at > last_read_at
	unreadAfterUpdate, err := store.ReadState.Unread(ctx)
	if err != nil {
		t.Fatalf("Unread after update failed: %v", err)
	}
	sort.Strings(unreadAfterUpdate)
	if len(unreadAfterUpdate) != 2 || unreadAfterUpdate[0] != expectedUnread[0] || unreadAfterUpdate[1] != expectedUnread[1] {
		t.Fatalf("expected both unread after update, got %+v", unreadAfterUpdate)
	}

	// 5. Mark both as read
	if err := store.ReadState.Mark(ctx, rev1); err != nil {
		t.Fatalf("Mark rev1 failed: %v", err)
	}
	if err := store.ReadState.Mark(ctx, rev2); err != nil {
		t.Fatalf("Mark rev2 failed: %v", err)
	}

	unreadClean, err := store.ReadState.Unread(ctx)
	if err != nil {
		t.Fatalf("Unread clean failed: %v", err)
	}
	if len(unreadClean) != 0 {
		t.Fatalf("expected 0 unread, got %+v", unreadClean)
	}

	// 6. Clear read mark on rev1
	if err := store.ReadState.Clear(ctx, rev1); err != nil {
		t.Fatalf("Clear rev1 failed: %v", err)
	}

	unreadAfterClear, err := store.ReadState.Unread(ctx)
	if err != nil {
		t.Fatalf("Unread after clear failed: %v", err)
	}
	if len(unreadAfterClear) != 1 || unreadAfterClear[0] != rev1 {
		t.Fatalf("expected unread [%s] after clear, got %+v", rev1, unreadAfterClear)
	}
}
