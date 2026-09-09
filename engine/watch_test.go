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

	// The vocabulary goes in before the subscription: installing it writes
	// a schema object, and that object's own event is not what this test
	// is about.
	applyCoreSchema(t, ctx, store)

	events := store.Watch(ctx)

	// 1. Create widget, then push a revision (appends create + revision)
	widgetID, err := store.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Add Authentication"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(widget) failed: %v", err)
	}
	if err := store.Objects.Apply(ctx, widgetID, writ.NewOp{
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
		if ev.ObjectType != "widget" {
			t.Errorf("expected ObjectType 'widget', got %q", ev.ObjectType)
		}
		if ev.ObjectID != widgetID {
			t.Errorf("expected ObjectID %q, got %q", widgetID, ev.ObjectID)
		}
		expectedOpTypes := []string{"create"}
		if !reflect.DeepEqual(ev.OpTypes, expectedOpTypes) {
			t.Errorf("expected OpTypes %v, got %v", expectedOpTypes, ev.OpTypes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for widget create event")
	}

	// 1b. The revision push is its own change event.
	select {
	case ev := <-events:
		if ev.Kind != writ.EventChanged {
			t.Errorf("expected EventChanged, got %q", ev.Kind)
		}
		if ev.ObjectID != widgetID {
			t.Errorf("expected ObjectID %q, got %q", widgetID, ev.ObjectID)
		}
		expectedOpTypes := []string{"revision"}
		if !reflect.DeepEqual(ev.OpTypes, expectedOpTypes) {
			t.Errorf("expected OpTypes %v, got %v", expectedOpTypes, ev.OpTypes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for widget revision event")
	}

	// 2. Add a note against the widget (appends a create op for the note)
	noteID, err := store.Objects.Create(ctx, "note", writ.NewOp{
		Type: "create",
		Fields: map[string]any{
			"text":    "Please check auth header format",
			"subject": map[string]string{"object_type": "widget", "object_id": widgetID},
		},
	})
	if err != nil {
		t.Fatalf("Objects.Create(note) failed: %v", err)
	}

	select {
	case ev := <-events:
		if ev.Kind != writ.EventCreated {
			t.Errorf("expected EventCreated, got %q", ev.Kind)
		}
		if ev.ObjectType != "note" {
			t.Errorf("expected ObjectType 'note', got %q", ev.ObjectType)
		}
		if ev.ObjectID != noteID {
			t.Errorf("expected ObjectID %q, got %q", noteID, ev.ObjectID)
		}
		expectedOpTypes := []string{"create"}
		if !reflect.DeepEqual(ev.OpTypes, expectedOpTypes) {
			t.Errorf("expected OpTypes %v, got %v", expectedOpTypes, ev.OpTypes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for note create event")
	}

	// 3. Update the widget title (appends an update op for the widget)
	err = store.Objects.Apply(ctx, widgetID, writ.NewOp{
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
		if ev.ObjectType != "widget" {
			t.Errorf("expected ObjectType 'widget', got %q", ev.ObjectType)
		}
		if ev.ObjectID != widgetID {
			t.Errorf("expected ObjectID %q, got %q", widgetID, ev.ObjectID)
		}
		expectedOpTypes := []string{"update"}
		if !reflect.DeepEqual(ev.OpTypes, expectedOpTypes) {
			t.Errorf("expected OpTypes %v, got %v", expectedOpTypes, ev.OpTypes)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for widget update event")
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

	applyCoreSchema(t, ctx, sA)
	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync of the schema failed: %v", err)
	}
	if _, err := sB.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Bob Sync of the schema failed: %v", err)
	}

	// Alice creates a widget and syncs to origin
	widgetID, err := sA.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Alice's Widget"},
	})
	if err != nil {
		t.Fatalf("Alice Objects.Create(widget) failed: %v", err)
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

	// Bob should receive an event for the widget fetched
	select {
	case ev := <-events:
		if ev.Kind != writ.EventCreated {
			t.Errorf("expected EventCreated on Bob, got %q", ev.Kind)
		}
		if ev.ObjectID != widgetID {
			t.Errorf("expected ObjectID %q, got %q", widgetID, ev.ObjectID)
		}
		if ev.ObjectType != "widget" {
			t.Errorf("expected ObjectType 'widget', got %q", ev.ObjectType)
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

	applyCoreSchema(t, ctx, store)

	events := store.Watch(ctx)

	const n = 10
	var createdIDs []string
	for i := 0; i < n; i++ {
		id, err := store.Objects.Create(ctx, "gadget", writ.NewOp{
			Type:   "create",
			Fields: map[string]any{"title": fmt.Sprintf("Gadget %d", i)},
		})
		if err != nil {
			t.Fatalf("Objects.Create(gadget) %d failed: %v", i, err)
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

	applyCoreSchema(t, ctx, store)

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
	id, err := store.Objects.Create(ctx, "gadget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Raced Gadget"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(gadget) failed: %v", err)
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

	applyCoreSchema(t, ctx, store)

	events := store.Watch(ctx)

	id, err := store.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Visibility Test Widget"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(widget) failed: %v", err)
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

		// The event fires only after the projection transaction commits
		// (that's the property this test names), so the folded title —
		// not just the objects-table id — must already be queryable too:
		// Query.Objects' text filter is served from the projection's
		// generated type-table columns, not ObjectResult's own metadata
		// (round 1 minor finding: this assertion had been downgraded to
		// "an id comes back", which a projection that materialized only
		// the objects row and no type-table columns would still pass).
		byText, err := store.Query.Objects(writ.ObjectFilter{Text: "Visibility Test Widget"})
		if err != nil {
			t.Fatalf("Query.Objects(Text) immediately on event failed: %v", err)
		}
		if len(byText) != 1 || byText[0].ObjectID != id {
			t.Fatalf("Query.Objects(Text=%q) = %+v, want exactly [%s] immediately on event", "Visibility Test Widget", byText, id)
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

	applyCoreSchema(t, ctx, store)

	// Subscriber that does not read
	events := store.Watch(ctx)

	// Write 128 + 50 = 178 gadgets
	const totalWrites = 178
	for i := 0; i < totalWrites; i++ {
		_, err := store.Objects.Create(ctx, "gadget", writ.NewOp{
			Type:   "create",
			Fields: map[string]any{"title": fmt.Sprintf("Overflow Gadget %d", i)},
		})
		if err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}
	}

	// Projection is intact and query succeeds
	gadgets, err := store.Query.Objects(writ.ObjectFilter{Type: []string{"gadget"}})
	if err != nil {
		t.Fatalf("Query.Objects(gadget) failed: %v", err)
	}
	if len(gadgets) != totalWrites {
		t.Fatalf("expected %d gadgets in projection, got %d", totalWrites, len(gadgets))
	}

	// Drain 1 item from the channel to create room
	select {
	case <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out reading first event")
	}

	// Perform another write so emit sees available buffer capacity and delivers reset
	_, err = store.Objects.Create(ctx, "gadget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Post-overflow Gadget"},
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

	applyCoreSchema(t, ctx, store)

	// Create an initial widget
	_, err = store.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Rebuild Widget"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(widget) failed: %v", err)
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
	cmd := exec.Command("git", "update-ref", "-d", "refs/writ/0123456789abcdef/widget")
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

	applyCoreSchema(t, ctx, store)

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
	_, err = store.Objects.Create(ctx, "gadget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Post-cancel Gadget"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(gadget) after cancel failed: %v", err)
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

	// Installing the vocabulary and folding it happen before the
	// subscription, so the only event this test can see is its own write's.
	applyCoreSchema(t, ctx, store)
	if _, err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh after ApplySchema failed: %v", err)
	}

	events := store.Watch(ctx)

	// Write without auto-refresh produces no event immediately
	widgetID, err := store.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Manual Refresh Widget"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(widget) failed: %v", err)
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
		if ev.ObjectID != widgetID {
			t.Fatalf("expected ObjectID %q, got %q", widgetID, ev.ObjectID)
		}
		if ev.Kind != writ.EventCreated {
			t.Fatalf("expected EventCreated, got %q", ev.Kind)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for event after explicit Refresh")
	}
}
