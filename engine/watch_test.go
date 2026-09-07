package writ_test

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/writtendev/writ/engine"
)

func TestWatchLocalWritesEmit(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	events := store.Watch(ctx)

	// 1. Create review, then push a revision (appends create + revision)
	reviewID, err := store.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Add Authentication"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(review) failed: %v", err)
	}
	if err := store.Objects.Apply(ctx, reviewID, writ.NewOp{
		Type: "revision",
		Fields: map[string]any{
			"base": "0000000000000000000000000000000000000001",
			"head": "0000000000000000000000000000000000000002",
		},
	}); err != nil {
		t.Fatalf("Objects.Apply(revision) failed: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Kind != writ.EventCreated {
			t.Errorf("expected EventCreated, got %q", ev.Kind)
		}
		if ev.ObjectType != "review" {
			t.Errorf("expected ObjectType 'review', got %q", ev.ObjectType)
		}
		if ev.ObjectID != reviewID {
			t.Errorf("expected ObjectID %q, got %q", reviewID, ev.ObjectID)
		}
		expectedOpTypes := []string{"create"}
		if !reflect.DeepEqual(ev.OpTypes, expectedOpTypes) {
			t.Errorf("expected OpTypes %v, got %v", expectedOpTypes, ev.OpTypes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for review create event")
	}

	// 1b. The revision push is its own change event.
	select {
	case ev := <-events:
		if ev.Kind != writ.EventChanged {
			t.Errorf("expected EventChanged, got %q", ev.Kind)
		}
		if ev.ObjectID != reviewID {
			t.Errorf("expected ObjectID %q, got %q", reviewID, ev.ObjectID)
		}
		expectedOpTypes := []string{"revision"}
		if !reflect.DeepEqual(ev.OpTypes, expectedOpTypes) {
			t.Errorf("expected OpTypes %v, got %v", expectedOpTypes, ev.OpTypes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for review revision event")
	}

	// 2. Add comment to review (appends create op for comment object)
	commentID, err := store.Objects.Create(ctx, "comment", writ.NewOp{
		Type: "create",
		Fields: map[string]any{
			"text":    "Please check auth header format",
			"subject": map[string]string{"object_type": "review", "object_id": reviewID},
		},
	})
	if err != nil {
		t.Fatalf("Objects.Create(comment) failed: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Kind != writ.EventCreated {
			t.Errorf("expected EventCreated, got %q", ev.Kind)
		}
		if ev.ObjectType != "comment" {
			t.Errorf("expected ObjectType 'comment', got %q", ev.ObjectType)
		}
		if ev.ObjectID != commentID {
			t.Errorf("expected ObjectID %q, got %q", commentID, ev.ObjectID)
		}
		expectedOpTypes := []string{"create"}
		if !reflect.DeepEqual(ev.OpTypes, expectedOpTypes) {
			t.Errorf("expected OpTypes %v, got %v", expectedOpTypes, ev.OpTypes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for comment create event")
	}

	// 3. Update review title (appends update op for review object)
	err = store.Objects.Apply(ctx, reviewID, writ.NewOp{
		Type:   "update",
		Fields: map[string]any{"title": "Add Authentication Provider"},
	})
	if err != nil {
		t.Fatalf("Objects.Apply(update) failed: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Kind != writ.EventChanged {
			t.Errorf("expected EventChanged, got %q", ev.Kind)
		}
		if ev.ObjectType != "review" {
			t.Errorf("expected ObjectType 'review', got %q", ev.ObjectType)
		}
		if ev.ObjectID != reviewID {
			t.Errorf("expected ObjectID %q, got %q", reviewID, ev.ObjectID)
		}
		expectedOpTypes := []string{"update"}
		if !reflect.DeepEqual(ev.OpTypes, expectedOpTypes) {
			t.Errorf("expected OpTypes %v, got %v", expectedOpTypes, ev.OpTypes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for review update event")
	}
}

func TestWatchPostFetchRefoldsEmit(t *testing.T) {
	_, aliceDir, bobDir := setupSyncHarness(t)
	ctx := context.Background()

	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Alice failed: %v", err)
	}
	defer sA.Close()

	sB, err := writ.Open(bobDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Bob failed: %v", err)
	}
	defer sB.Close()

	// Alice creates review and syncs to origin
	reviewID, err := sA.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Alice's Review"},
	})
	if err != nil {
		t.Fatalf("Alice Objects.Create(review) failed: %v", err)
	}

	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync failed: %v", err)
	}

	// Bob subscribes before sync
	events := sB.Watch(ctx)

	// Bob syncs from origin
	if _, err := sB.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Bob Sync failed: %v", err)
	}

	// Bob should receive event for the review fetched
	select {
	case ev := <-events:
		if ev.Kind != writ.EventCreated {
			t.Errorf("expected EventCreated on Bob, got %q", ev.Kind)
		}
		if ev.ObjectID != reviewID {
			t.Errorf("expected ObjectID %q, got %q", reviewID, ev.ObjectID)
		}
		if ev.ObjectType != "review" {
			t.Errorf("expected ObjectType 'review', got %q", ev.ObjectType)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for Bob sync event")
	}
}

func TestWatchNothingMissedAfterSubscribe(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	events := store.Watch(ctx)

	const n = 10
	var createdIDs []string
	for i := 0; i < n; i++ {
		id, err := store.Objects.Create(ctx, "issue", writ.NewOp{
			Type:   "create",
			Fields: map[string]any{"title": fmt.Sprintf("Issue %d", i)},
		})
		if err != nil {
			t.Fatalf("Objects.Create(issue) %d failed: %v", i, err)
		}
		createdIDs = append(createdIDs, id)
	}

	for i := 0; i < n; i++ {
		select {
		case ev := <-events:
			if ev.ObjectID != createdIDs[i] {
				t.Fatalf("event %d: expected ObjectID %q, got %q", i, createdIDs[i], ev.ObjectID)
			}
			if ev.Kind != writ.EventCreated {
				t.Fatalf("event %d: expected EventCreated, got %q", i, ev.Kind)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for event %d", i)
		}
	}
}

func TestWatchConcurrentSubscribeAndRefreshRace(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	var wg sync.WaitGroup
	const readers = 5
	channels := make([]<-chan writ.Event, readers)

	// Race Watch registration with in-flight Refresh calls
	for i := 0; i < readers; i++ {
		idx := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			channels[idx] = store.Watch(ctx)
		}()
	}

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = store.Refresh(ctx)
		}()
	}

	wg.Wait()

	// Subsequent write must be delivered to all subscribers
	id, err := store.Objects.Create(ctx, "issue", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Raced Issue"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(issue) failed: %v", err)
	}

	for i, ch := range channels {
		select {
		case ev := <-ch:
			if ev.ObjectID != id {
				t.Errorf("reader %d: expected ObjectID %q, got %q", i, id, ev.ObjectID)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("reader %d: timed out waiting for event", i)
		}
	}
}

func TestWatchEventPrecedesVisibility(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	events := store.Watch(ctx)

	id, err := store.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Visibility Test Review"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(review) failed: %v", err)
	}

	select {
	case ev := <-events:
		// Immediately query for that object
		res, err := store.Query.Object(ev.ObjectID)
		if err != nil {
			t.Fatalf("Query.Object immediately on event failed: %v", err)
		}
		if res.ObjectID != id {
			t.Fatalf("Query.Object returned wrong ObjectID: %q", res.ObjectID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestWatchSlowConsumerOverflowAndReset(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Subscriber that does not read
	events := store.Watch(ctx)

	// Write 128 + 50 = 178 issues
	const totalWrites = 178
	for i := 0; i < totalWrites; i++ {
		_, err := store.Objects.Create(ctx, "issue", writ.NewOp{
			Type:   "create",
			Fields: map[string]any{"title": fmt.Sprintf("Overflow Issue %d", i)},
		})
		if err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}
	}

	// Projection is intact and query succeeds
	issues, err := store.Query.Objects(writ.ObjectFilter{Type: []string{"issue"}})
	if err != nil {
		t.Fatalf("Query.Objects(issue) failed: %v", err)
	}
	if len(issues) != totalWrites {
		t.Fatalf("expected %d issues in projection, got %d", totalWrites, len(issues))
	}

	// Drain 1 item from the channel to create room
	select {
	case <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out reading first event")
	}

	// Perform another write so emit sees available buffer capacity and delivers reset
	_, err = store.Objects.Create(ctx, "issue", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Post-overflow Issue"},
	})
	if err != nil {
		t.Fatalf("post-overflow write failed: %v", err)
	}

	// Drain remaining events until we see EventReset
	resetReceived := false
	for i := 0; i < 150; i++ {
		select {
		case ev := <-events:
			if ev.Kind == writ.EventReset {
				resetReceived = true
				break
			}
		case <-time.After(2 * time.Second):
			break
		}
		if resetReceived {
			break
		}
	}

	if !resetReceived {
		t.Fatal("expected EventReset on overflow recovery, but did not receive one")
	}
}

func TestWatchRebuildEmitsReset(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// Create an initial review
	_, err = store.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Rebuild Review"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(review) failed: %v", err)
	}

	events := store.Watch(ctx)

	// Explicit Rebuild
	_, err = store.Rebuild(ctx)
	if err != nil {
		t.Fatalf("Rebuild failed: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Kind != writ.EventReset {
			t.Fatalf("expected EventReset on Rebuild, got %q", ev.Kind)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for reset event on Rebuild")
	}

	// Chain deletion triggers rebuild on Refresh
	cmd := exec.Command("git", "update-ref", "-d", "refs/writ/0123456789abcdef/review")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("delete ref failed: %v, out: %s", err, string(out))
	}

	_, err = store.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh after deleted ref failed: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Kind != writ.EventReset {
			t.Fatalf("expected EventReset on deleted ref refresh, got %q", ev.Kind)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for reset event after ref deletion")
	}
}

func TestWatchLifecycle(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// 1. Context cancellation unsubscribes and closes channel
	subCtx, cancel := context.WithCancel(ctx)
	events1 := store.Watch(subCtx)

	cancel()

	select {
	case _, ok := <-events1:
		if ok {
			t.Fatal("expected events1 channel to be closed on ctx cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for events1 close on ctx cancel")
	}

	// Further writes succeed without panicking
	_, err = store.Objects.Create(ctx, "issue", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Post-cancel Issue"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(issue) after cancel failed: %v", err)
	}

	// 2. Store.Close closes all remaining subscribers
	events2 := store.Watch(ctx)
	events3 := store.Watch(ctx)

	if err := store.Close(); err != nil {
		t.Fatalf("store.Close failed: %v", err)
	}

	select {
	case _, ok := <-events2:
		if ok {
			t.Fatal("expected events2 to be closed on store.Close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for events2 close")
	}

	select {
	case _, ok := <-events3:
		if ok {
			t.Fatal("expected events3 to be closed on store.Close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for events3 close")
	}

	// Watch on closed store returns closed channel
	events4 := store.Watch(ctx)
	select {
	case _, ok := <-events4:
		if ok {
			t.Fatal("expected events4 from closed store to be closed")
		}
	default:
		t.Fatal("expected events4 to be closed immediately")
	}
}

func TestWatchWithoutAutoRefresh(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(repoDir, writ.WithSigner(dummySigner()), writ.WithoutAutoRefresh())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	events := store.Watch(ctx)

	// Write without auto-refresh produces no event immediately
	reviewID, err := store.Objects.Create(ctx, "review", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Manual Refresh Review"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(review) failed: %v", err)
	}

	select {
	case ev := <-events:
		t.Fatalf("unexpected event before Refresh: %+v", ev)
	case <-time.After(200 * time.Millisecond):
		// Expected: no event yet
	}

	// Explicit Refresh emits the event
	_, err = store.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	select {
	case ev := <-events:
		if ev.ObjectID != reviewID {
			t.Fatalf("expected ObjectID %q, got %q", reviewID, ev.ObjectID)
		}
		if ev.Kind != writ.EventCreated {
			t.Fatalf("expected EventCreated, got %q", ev.Kind)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event after explicit Refresh")
	}
}
