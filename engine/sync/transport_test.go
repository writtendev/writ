package sync_test

import (
	"context"
	"errors"
	"os/exec"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine/identity"
	writsync "github.com/writtendev/writ/engine/sync"
)

func TestPush_LocalWriterOnly(t *testing.T) {
	bareDir, bareRepo := initBareRepo(t)
	localDir, localRepo := initTestRepo(t)

	aliceID := "0123456789abcdef"
	bobID := "fedcba9876543210"

	aliceIdent := testIdentity(aliceID, "Alice", "alice@example.com")
	aliceStore := mustOpenStore(t, localDir, aliceIdent)

	// Append an op on Alice's widget chain
	aliceOpID := appendTestOp(t, aliceStore, "widget", "w-1", "create", map[string]any{"title": "Alice Widget"})

	// Also manually seed a foreign ref under refs/writ/<bobID>/widget in local repo
	foreignRefName := plumbing.ReferenceName("refs/writ/" + bobID + "/widget")
	dummyCommit := plumbing.NewHash(aliceOpID)
	if err := localRepo.Storer.SetReference(plumbing.NewHashReference(foreignRefName, dummyCommit)); err != nil {
		t.Fatalf("set foreign ref: %v", err)
	}

	// Add remote
	cmd := exec.Command("git", "remote", "add", "origin", bareDir)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	client, err := writsync.Open(localDir, aliceIdent)
	if err != nil {
		t.Fatalf("writsync.Open: %v", err)
	}

	pushRes, err := client.Push(context.Background(), "origin")
	if err != nil {
		t.Fatalf("client.Push failed: %v", err)
	}

	if pushRes.Remote != "origin" {
		t.Fatalf("pushRes.Remote = %q, want %q", pushRes.Remote, "origin")
	}

	// Verify on bare remote: Alice's ref is present, Bob's foreign ref is absent
	bareRefs := snapshotAllRefs(t, bareRepo)
	expectedAliceRef := "refs/writ/" + aliceID + "/widget"
	expectedBobRef := "refs/writ/" + bobID + "/widget"

	if _, ok := bareRefs[expectedAliceRef]; !ok {
		t.Fatalf("expected alice ref %s to exist on bare remote, refs: %v", expectedAliceRef, bareRefs)
	}
	if _, ok := bareRefs[expectedBobRef]; ok {
		t.Fatalf("foreign ref %s must NOT exist on bare remote, refs: %v", expectedBobRef, bareRefs)
	}
}

func TestFetch_BringsAllWritersAndPreservesUnpushed(t *testing.T) {
	bareDir, _ := initBareRepo(t)
	aliceDir, _ := initTestRepo(t)
	bobDir, bobRepo := initTestRepo(t)

	aliceID := "0123456789abcdef"
	bobID := "fedcba9876543210"

	aliceIdent := testIdentity(aliceID, "Alice", "alice@example.com")
	bobIdent := testIdentity(bobID, "Bob", "bob@example.com")

	aliceStore := mustOpenStore(t, aliceDir, aliceIdent)
	bobStore := mustOpenStore(t, bobDir, bobIdent)

	// Configure remotes
	for _, pair := range []struct {
		dir   string
		ident identity.Identity
	}{{aliceDir, aliceIdent}, {bobDir, bobIdent}} {
		cmd := exec.Command("git", "remote", "add", "origin", bareDir)
		cmd.Dir = pair.dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git remote add: %v", err)
		}
	}

	aliceSync, err := writsync.Open(aliceDir, aliceIdent)
	if err != nil {
		t.Fatalf("open alice sync: %v", err)
	}
	bobSync, err := writsync.Open(bobDir, bobIdent)
	if err != nil {
		t.Fatalf("open bob sync: %v", err)
	}

	// 1. Ensure refspecs in both repos
	ctx := context.Background()
	if _, err := aliceSync.Ensure(ctx, "origin"); err != nil {
		t.Fatalf("alice Ensure: %v", err)
	}
	if _, err := bobSync.Ensure(ctx, "origin"); err != nil {
		t.Fatalf("bob Ensure: %v", err)
	}

	// 2. Alice creates an op and pushes it
	aliceOpID := appendTestOp(t, aliceStore, "widget", "w-alice", "create", map[string]any{"title": "Alice's Widget"})
	if _, err := aliceSync.Push(ctx, "origin"); err != nil {
		t.Fatalf("alice Push: %v", err)
	}

	// 3. Bob creates a local unpushed op
	bobOpID := appendTestOp(t, bobStore, "widget", "w-bob", "create", map[string]any{"title": "Bob's Widget"})

	// 4. Bob fetches from origin
	fetchRes, err := bobSync.Fetch(ctx, "origin")
	if err != nil {
		t.Fatalf("bob Fetch: %v", err)
	}

	if len(fetchRes.Updates) == 0 {
		t.Fatalf("expected at least 1 chain update in fetch result")
	}

	// 5. Assert Bob's remote tracking ref for Alice was created/updated
	bobRefs := snapshotAllRefs(t, bobRepo)
	aliceTrackingRef := "refs/remotes/origin/writ/" + aliceID + "/widget"
	if tip, ok := bobRefs[aliceTrackingRef]; !ok || tip != aliceOpID {
		t.Fatalf("expected %s = %s, got %s (refs: %v)", aliceTrackingRef, aliceOpID, tip, bobRefs)
	}

	// 6. Assert Bob's local unpushed chain is untouched
	bobLocalRef := "refs/writ/" + bobID + "/widget"
	if tip, ok := bobRefs[bobLocalRef]; !ok || tip != bobOpID {
		t.Fatalf("expected %s = %s, got %s", bobLocalRef, bobOpID, tip)
	}

	// 7. Enumerate on Bob's store yields both Alice's and Bob's ops
	enumRes, err := bobStore.Enumerate()
	if err != nil {
		t.Fatalf("bobStore.Enumerate: %v", err)
	}

	if len(enumRes.Ops["w-alice"]) != 1 {
		t.Fatalf("expected w-alice op in Bob's enumeration: %v", enumRes.Ops)
	}
	if len(enumRes.Ops["w-bob"]) != 1 {
		t.Fatalf("expected w-bob op in Bob's enumeration: %v", enumRes.Ops)
	}
}

func TestFetch_RollbackRejected(t *testing.T) {
	bareDir, _ := initBareRepo(t)
	localDir, localRepo := initTestRepo(t)

	aliceID := "0123456789abcdef"
	aliceIdent := testIdentity(aliceID, "Alice", "alice@example.com")
	aliceStore := mustOpenStore(t, localDir, aliceIdent)

	cmd := exec.Command("git", "remote", "add", "origin", bareDir)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	client, err := writsync.Open(localDir, aliceIdent)
	if err != nil {
		t.Fatalf("writsync.Open: %v", err)
	}

	ctx := context.Background()
	if _, err := client.Ensure(ctx, "origin"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// Alice creates op1 and op2 and pushes both
	op1 := appendTestOp(t, aliceStore, "widget", "w-1", "create", map[string]any{"title": "Op 1"})
	op2 := appendTestOp(t, aliceStore, "widget", "w-1", "update", map[string]any{"title": "Op 2"})

	if _, err := client.Push(ctx, "origin"); err != nil {
		t.Fatalf("Push op1 & op2: %v", err)
	}

	// Fetch to establish remote-tracking ref
	if _, err := client.Fetch(ctx, "origin"); err != nil {
		t.Fatalf("Initial Fetch: %v", err)
	}

	trackingRef := "refs/remotes/origin/writ/" + aliceID + "/widget"
	refsBefore := snapshotAllRefs(t, localRepo)
	if refsBefore[trackingRef] != op2 {
		t.Fatalf("expected tracking ref %s = %s, got %s", trackingRef, op2, refsBefore[trackingRef])
	}

	// Force bare remote ref backwards to op1
	cmd = exec.Command("git", "update-ref", "refs/writ/"+aliceID+"/widget", op1)
	cmd.Dir = bareDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("force update-ref on bare: %v", err)
	}

	// Now Fetch again: MUST reject non-fast-forward rollback
	_, err = client.Fetch(ctx, "origin")
	if err == nil {
		t.Fatalf("expected Fetch to fail with non-fast-forward rejection, but succeeded")
	}

	if !errors.Is(err, writsync.ErrNonFastForward) {
		t.Fatalf("expected ErrNonFastForward, got: %v", err)
	}

	// Assert remote tracking ref is unchanged at op2
	refsAfter := snapshotAllRefs(t, localRepo)
	if refsAfter[trackingRef] != op2 {
		t.Fatalf("tracking ref must remain at %s, but changed to %s", op2, refsAfter[trackingRef])
	}
}

func TestPush_NonFastForwardRejected(t *testing.T) {
	bareDir, _ := initBareRepo(t)
	localDir, localRepo := initTestRepo(t)

	aliceID := "0123456789abcdef"
	aliceIdent := testIdentity(aliceID, "Alice", "alice@example.com")
	aliceStore := mustOpenStore(t, localDir, aliceIdent)

	cmd := exec.Command("git", "remote", "add", "origin", bareDir)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	client, err := writsync.Open(localDir, aliceIdent)
	if err != nil {
		t.Fatalf("writsync.Open: %v", err)
	}

	ctx := context.Background()

	// Append op1 and op2, push both
	op1 := appendTestOp(t, aliceStore, "widget", "w-1", "create", map[string]any{"title": "Op 1"})
	_ = appendTestOp(t, aliceStore, "widget", "w-1", "update", map[string]any{"title": "Op 2"})

	if _, err := client.Push(ctx, "origin"); err != nil {
		t.Fatalf("Push op1 & op2: %v", err)
	}

	// Force local ref backwards to op1
	localRefName := plumbing.ReferenceName("refs/writ/" + aliceID + "/widget")
	if err := localRepo.Storer.SetReference(plumbing.NewHashReference(localRefName, plumbing.NewHash(op1))); err != nil {
		t.Fatalf("set local ref: %v", err)
	}

	// Try pushing backwards without force
	_, err = client.Push(ctx, "origin")
	if err == nil {
		t.Fatalf("expected Push to fail on non-fast-forward, but succeeded")
	}

	if !errors.Is(err, writsync.ErrNonFastForward) {
		t.Fatalf("expected ErrNonFastForward, got: %v", err)
	}
}

func TestTransport_UnknownRemote(t *testing.T) {
	localDir, _ := initTestRepo(t)
	aliceID := "0123456789abcdef"
	aliceIdent := testIdentity(aliceID, "Alice", "alice@example.com")
	aliceStore := mustOpenStore(t, localDir, aliceIdent)
	_ = appendTestOp(t, aliceStore, "widget", "w-1", "create", map[string]any{"title": "Op 1"})

	client, err := writsync.Open(localDir, aliceIdent)
	if err != nil {
		t.Fatalf("writsync.Open: %v", err)
	}

	ctx := context.Background()

	_, err = client.Fetch(ctx, "nonexistent-remote")
	if err == nil {
		t.Fatalf("expected fetch unknown remote to fail")
	}
	if !errors.Is(err, writsync.ErrUnknownRemote) {
		t.Fatalf("expected ErrUnknownRemote, got: %v", err)
	}

	_, err = client.Push(ctx, "nonexistent-remote")
	if err == nil {
		t.Fatalf("expected push unknown remote to fail")
	}
	if !errors.Is(err, writsync.ErrUnknownRemote) {
		t.Fatalf("expected ErrUnknownRemote, got: %v", err)
	}
}

func TestTransport_ContextCancellation(t *testing.T) {
	bareDir, _ := initBareRepo(t)
	localDir, _ := initTestRepo(t)

	aliceID := "0123456789abcdef"
	aliceIdent := testIdentity(aliceID, "Alice", "alice@example.com")

	cmd := exec.Command("git", "remote", "add", "origin", bareDir)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	client, err := writsync.Open(localDir, aliceIdent)
	if err != nil {
		t.Fatalf("writsync.Open: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled immediately

	_, err = client.Fetch(ctx, "origin")
	if err == nil {
		t.Fatalf("expected fetch to fail with canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}

	_, err = client.Push(ctx, "origin")
	if err == nil {
		t.Fatalf("expected push to fail with canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestPush_MultipleObjectTypes(t *testing.T) {
	bareDir, bareRepo := initBareRepo(t)
	localDir, _ := initTestRepo(t)

	aliceID := "0123456789abcdef"
	aliceIdent := testIdentity(aliceID, "Alice", "alice@example.com")
	aliceStore := mustOpenStore(t, localDir, aliceIdent)

	cmd := exec.Command("git", "remote", "add", "origin", bareDir)
	cmd.Dir = localDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	client, err := writsync.Open(localDir, aliceIdent)
	if err != nil {
		t.Fatalf("writsync.Open: %v", err)
	}

	// Create ops on two distinct object types: widget and gadget
	appendTestOp(t, aliceStore, "widget", "w-1", "create", map[string]any{"title": "Widget 1"})
	appendTestOp(t, aliceStore, "gadget", "g-1", "create", map[string]any{"title": "Gadget 1"})

	pushRes, err := client.Push(context.Background(), "origin")
	if err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	if len(pushRes.PushedRefs) < 2 {
		t.Fatalf("expected at least 2 pushed refs, got %d: %v", len(pushRes.PushedRefs), pushRes.PushedRefs)
	}

	bareRefs := snapshotAllRefs(t, bareRepo)
	widgetRef := "refs/writ/" + aliceID + "/widget"
	gadgetRef := "refs/writ/" + aliceID + "/gadget"

	if _, ok := bareRefs[widgetRef]; !ok {
		t.Fatalf("expected bare to have %s, got refs: %v", widgetRef, bareRefs)
	}
	if _, ok := bareRefs[gadgetRef]; !ok {
		t.Fatalf("expected bare to have %s, got refs: %v", gadgetRef, bareRefs)
	}
}
