package writ_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/writtendev/writ/engine"
)

func setupSyncHarness(t *testing.T) (bareDir, aliceDir, bobDir string) {
	t.Helper()
	tempDir := t.TempDir()

	// 1. Bare remote
	bareDir = filepath.Join(tempDir, "remote.git")
	runGitCmd(t, tempDir, "init", "--bare", "--initial-branch=main", bareDir)

	// 2. Alice's clone
	aliceDir = filepath.Join(tempDir, "alice")
	runGitCmd(t, tempDir, "init", "--initial-branch=main", aliceDir)
	runGitCmd(t, aliceDir, "config", "user.name", "Alice")
	runGitCmd(t, aliceDir, "config", "user.email", "alice@example.com")
	runGitCmd(t, aliceDir, "config", "writ.writerId", "0123456789abcdef")
	runGitCmd(t, aliceDir, "config", "gpg.format", "ssh")
	runGitCmd(t, aliceDir, "config", "user.signingKey", "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGalice")
	runGitCmd(t, aliceDir, "remote", "add", "origin", bareDir)

	// Commit dummy file so HEAD exists and push main branch
	dummyFile := filepath.Join(aliceDir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("# Project\n"), 0o644); err != nil {
		t.Fatalf("write dummy file: %v", err)
	}
	runGitCmd(t, aliceDir, "add", "README.md")
	runGitCmd(t, aliceDir, "commit", "-m", "initial commit")
	runGitCmd(t, aliceDir, "push", "origin", "HEAD:main")

	// 3. Bob's clone
	bobDir = filepath.Join(tempDir, "bob")
	runGitCmd(t, tempDir, "clone", bareDir, bobDir)
	runGitCmd(t, bobDir, "config", "user.name", "Bob")
	runGitCmd(t, bobDir, "config", "user.email", "bob@example.com")
	runGitCmd(t, bobDir, "config", "writ.writerId", "fedcba9876543210")
	runGitCmd(t, bobDir, "config", "gpg.format", "ssh")
	runGitCmd(t, bobDir, "config", "user.signingKey", "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGbob")

	return bareDir, aliceDir, bobDir
}

func TestStoreSyncLifecycle(t *testing.T) {
	_, aliceDir, bobDir := setupSyncHarness(t)
	ctx := context.Background()

	// 1. Open Store A (Alice)
	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Alice failed: %v", err)
	}
	defer sA.Close()

	// 2. Open Store B (Bob)
	sB, err := writ.Open(bobDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Bob failed: %v", err)
	}
	defer sB.Close()

	// The vocabulary is itself an object, and syncs like one. Alice writes
	// it and both sides sync it up front, so every op count below is about
	// the objects the test writes, not about the schema arriving.
	applyCoreSchema(t, ctx, sA)
	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync of the schema failed: %v", err)
	}
	if _, err := sB.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Bob Sync of the schema failed: %v", err)
	}

	// Initial status
	statusA, err := sA.SyncStatus(ctx, "origin")
	if err != nil {
		t.Fatalf("Alice SyncStatus before write: %v", err)
	}
	if statusA.Unsynced != 0 {
		t.Errorf("Alice expected 0 unsynced before write, got %d", statusA.Unsynced)
	}

	// Alice creates a widget
	widgetID, err := sA.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Sync Feature Widget"},
	})
	if err != nil {
		t.Fatalf("Alice create widget: %v", err)
	}

	// Status now shows 1 unsynced op
	statusA, err = sA.SyncStatus(ctx, "origin")
	if err != nil {
		t.Fatalf("Alice SyncStatus after write: %v", err)
	}
	if statusA.Unsynced != 1 {
		t.Errorf("Alice expected 1 unsynced after write, got %d", statusA.Unsynced)
	}

	// Alice syncs with origin
	syncResA, err := sA.Sync(ctx, "origin")
	if err != nil {
		t.Fatalf("Alice Sync failed: %v", err)
	}
	if syncResA.OpsPushed != 1 {
		t.Errorf("Alice expected 1 op pushed, got %d", syncResA.OpsPushed)
	}
	if syncResA.Unsynced != 0 {
		t.Errorf("Alice expected 0 unsynced after sync, got %d", syncResA.Unsynced)
	}

	// Bob syncs with origin and receives Alice's widget
	syncResB, err := sB.Sync(ctx, "origin")
	if err != nil {
		t.Fatalf("Bob Sync failed: %v", err)
	}
	if syncResB.OpsFetched != 1 {
		t.Errorf("Bob expected 1 op fetched, got %d", syncResB.OpsFetched)
	}
	if syncResB.ObjectsTouched != 1 {
		t.Errorf("Bob expected 1 object touched, got %d", syncResB.ObjectsTouched)
	}

	// Bob queries and approves Alice's widget
	objB, err := sB.Objects.Get(ctx, widgetID)
	if err != nil {
		t.Fatalf("Bob Objects.Get failed: %v", err)
	}
	if objB.Fields["title"] != "Sync Feature Widget" {
		t.Errorf("Bob got title %q", objB.Fields["title"])
	}

	// Objects.Get folds straight from the DAG and never touches the
	// projection, so it alone can't confirm Sync's own Refresh actually
	// materialized the fetched op into Bob's cache — Query.Objects' text
	// filter, served from the projection's generated type-table columns,
	// does (round 1 minor finding: post-fetch state had come to be checked
	// only through Objects.Get across this file).
	byText, err := sB.Query.Objects(writ.ObjectFilter{Text: "Sync Feature Widget"})
	if err != nil {
		t.Fatalf("Bob Query.Objects(Text) failed: %v", err)
	}
	if len(byText) != 1 || byText[0].ObjectID != widgetID {
		t.Fatalf("Bob Query.Objects(Text=%q) = %+v, want exactly [%s]", "Sync Feature Widget", byText, widgetID)
	}

	// Push a revision and approve
	headHash := runGitCmd(t, bobDir, "rev-parse", "HEAD")[:40]
	if err := sB.Objects.Apply(ctx, widgetID, writ.NewOp{
		Type:   "revision",
		Fields: map[string]any{"base": headHash, "head": headHash},
	}); err != nil {
		t.Fatalf("Bob revision apply failed: %v", err)
	}
	if err := sB.Objects.Apply(ctx, widgetID, writ.NewOp{
		Type: "approval",
		Fields: map[string]any{
			"subject":  "email:bob@example.com",
			"revision": headHash,
			"verdict":  "approve",
			"message":  "Looks great from Bob!",
		},
	}); err != nil {
		t.Fatalf("Bob approval apply failed: %v", err)
	}

	// Bob pushes approval to origin
	syncResB2, err := sB.Sync(ctx, "origin")
	if err != nil {
		t.Fatalf("Bob Sync 2 failed: %v", err)
	}
	if syncResB2.OpsPushed != 2 { // revision + approval
		t.Errorf("Bob expected 2 ops pushed, got %d", syncResB2.OpsPushed)
	}

	// 3. Alice syncs and observes Bob's approval
	syncResA2, err := sA.Sync(ctx, "origin")
	if err != nil {
		t.Fatalf("Alice Sync 2 failed: %v", err)
	}
	if syncResA2.OpsFetched != 2 {
		t.Errorf("Alice expected 2 ops fetched, got %d", syncResA2.OpsFetched)
	}

	// approval's fields declare no target (spec/schema-ops.md), so each one
	// folds under its own field name as a keyed-lww register — a []any of
	// {"key": [...], "value": ...} entries keyed by (subject, revision) —
	// not a struct-shaped collection. Aggregating those entries into one
	// was a typed reducer's own construction, not a generic fold property.
	objA2, err := sA.Objects.Get(ctx, widgetID)
	if err != nil {
		t.Fatalf("Alice Objects.Get after fetch failed: %v", err)
	}
	verdicts, _ := objA2.Fields["verdict"].([]any)
	if len(verdicts) != 1 {
		t.Fatalf("Alice unexpected verdict entries: %+v", objA2.Fields["verdict"])
	}
	entry, _ := verdicts[0].(map[string]any)
	if entry["value"] != "approve" {
		t.Errorf("Alice unexpected verdict: %+v", entry)
	}
}

// TestStoreSync_RewindThroughFetch pins WRIT-270's core fix end-to-end: a
// peer force-pushing a rewind of their own chain (a backup restore, an
// unpushed-history rebase, or a plain --force) is a normal, non-hostile
// event, and it must land through an ordinary Store.Sync fetch instead of
// wedging it. engine/projection/refresh_test.go's TestRollbackTriggersRebuild
// reaches the projection's Rewound handling only by hand-setting a local
// ref; that path now runs in the normal case rather than never, so it
// deserves coverage that actually fetches from a real remote.
func TestStoreSync_RewindThroughFetch(t *testing.T) {
	bareDir, aliceDir, bobDir := setupSyncHarness(t)
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

	// Alice creates a widget (op1) and pushes it.
	widgetID, err := sA.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Pre-rewind Title"},
	})
	if err != nil {
		t.Fatalf("Alice create widget: %v", err)
	}
	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync after create failed: %v", err)
	}

	// Capture Alice's chain tip (op1) before it moves any further, so the
	// remote can be forced back to it below -- simulating an out-of-band
	// rewind of Alice's OWN chain (a backup restore or a plain --force),
	// the same shape a real peer's rewind produces. This never touches
	// Alice's local store.
	aliceChainRef := "refs/writ/0123456789abcdef/acme.widget"
	op1 := strings.TrimSpace(runGitCmd(t, aliceDir, "rev-parse", aliceChainRef))

	// Alice updates the widget (op2) and pushes again.
	if err := sA.Objects.Apply(ctx, widgetID, writ.NewOp{
		Type:   "update",
		Fields: map[string]any{"title": "Later Title (about to be rewound)"},
	}); err != nil {
		t.Fatalf("Alice update widget: %v", err)
	}
	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync after update failed: %v", err)
	}

	// Bob syncs now, BEFORE the rewind, so his local remote-tracking ref for
	// Alice's chain is actually established at op2. Without this, the
	// fetch below would be creating Bob's tracking ref for the first time
	// rather than moving it backward -- a fast-forward (from nothing) in
	// either the forced or the pre-WRIT-270 unforced case, which would
	// prove nothing about the refspec change under test.
	if _, err := sB.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Bob Sync before rewind failed: %v", err)
	}
	if obj, err := sB.Objects.Get(ctx, widgetID); err != nil || obj.Fields["title"] != "Later Title (about to be rewound)" {
		t.Fatalf("Bob pre-rewind state = %+v, err %v, want title %q", obj, err, "Later Title (about to be rewound)")
	}

	// Force the bare remote's copy of Alice's chain back to op1.
	runGitCmd(t, bareDir, "update-ref", aliceChainRef, op1)

	// Bob syncs again. With the forced fetch refspec (WRIT-270), this MUST
	// succeed and land the rewind rather than fail non-fast-forward.
	syncRes, err := sB.Sync(ctx, "origin")
	if err != nil {
		t.Fatalf("Bob Sync after peer rewind failed (forced fetch must land it): %v", err)
	}
	if syncRes.Unsynced != 0 {
		t.Errorf("Bob Unsynced = %d, want 0", syncRes.Unsynced)
	}

	// The projection must have rebuilt to reflect the rewound state: only
	// op1 (the create) is reachable from Alice's chain now, so reads
	// through the projection see the pre-rewind title, not the update that
	// was rewound away.
	obj, err := sB.Objects.Get(ctx, widgetID)
	if err != nil {
		t.Fatalf("Bob Objects.Get after rewind fetch failed: %v", err)
	}
	if obj.Fields["title"] != "Pre-rewind Title" {
		t.Errorf("Bob Fields[title] = %v, want %q (the rewind must have landed)", obj.Fields["title"], "Pre-rewind Title")
	}

	byText, err := sB.Query.Objects(writ.ObjectFilter{Text: "Pre-rewind Title"})
	if err != nil {
		t.Fatalf("Bob Query.Objects(Text) after rewind fetch failed: %v", err)
	}
	if len(byText) != 1 || byText[0].ObjectID != widgetID {
		t.Fatalf("Bob Query.Objects(Text=%q) after rewind = %+v, want exactly [%s]", "Pre-rewind Title", byText, widgetID)
	}
}

// TestStoreSync_BrokenPeerDoesNotStrandLocalWrites pins WRIT-270's central
// acceptance criterion: a peer rewinding their own chain must never strand
// a different writer's own, unrelated, already-pending ops. Pre-WRIT-270,
// Bob's fetch failed non-fast-forward against Alice's rewound chain on
// every retry, and push was gated on fetch succeeding (engine/sync.go), so
// Bob's own widget could never reach the remote no matter how many times he
// retried "writ sync" -- exactly the "wedges every writer's sync" failure
// this ticket exists to close.
func TestStoreSync_BrokenPeerDoesNotStrandLocalWrites(t *testing.T) {
	bareDir, aliceDir, bobDir := setupSyncHarness(t)
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

	// Alice creates a widget (op1) and pushes it.
	aliceWidgetID, err := sA.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Alice Widget"},
	})
	if err != nil {
		t.Fatalf("Alice create widget: %v", err)
	}
	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync after create failed: %v", err)
	}

	aliceChainRef := "refs/writ/0123456789abcdef/acme.widget"
	op1 := strings.TrimSpace(runGitCmd(t, aliceDir, "rev-parse", aliceChainRef))

	if err := sA.Objects.Apply(ctx, aliceWidgetID, writ.NewOp{
		Type:   "update",
		Fields: map[string]any{"title": "Alice Widget, Later"},
	}); err != nil {
		t.Fatalf("Alice update widget: %v", err)
	}
	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync after update failed: %v", err)
	}

	// Bob syncs now, BEFORE the damage, so his local remote-tracking ref
	// for Alice's chain is actually established at the pre-damage tip.
	// Without this, the rewind below would be creating Bob's tracking ref
	// for the first time rather than moving it backward -- a fast-forward
	// in either the forced or the pre-WRIT-270 unforced case, which would
	// prove nothing about the fix under test.
	if _, err := sB.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Bob Sync before peer damage failed: %v", err)
	}

	// Damage Alice's chain on the remote out-of-band, the same shape a real
	// peer's own rewind produces.
	runGitCmd(t, bareDir, "update-ref", aliceChainRef, op1)

	// Bob, unaware of the damage, creates his own widget locally: an
	// unrelated, unpushed op of his own.
	if _, err := sB.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Bob Widget"},
	}); err != nil {
		t.Fatalf("Bob create widget: %v", err)
	}

	statusBefore, err := sB.SyncStatus(ctx, "origin")
	if err != nil {
		t.Fatalf("Bob SyncStatus before sync: %v", err)
	}
	if statusBefore.Unsynced != 1 {
		t.Fatalf("Bob Unsynced before sync = %d, want 1", statusBefore.Unsynced)
	}

	// Bob syncs. His own op must reach the remote regardless of Alice's
	// broken chain.
	syncRes, err := sB.Sync(ctx, "origin")
	if err != nil {
		t.Fatalf("Bob Sync must not fail merely because a peer's chain is broken: %v", err)
	}
	if syncRes.OpsPushed != 1 {
		t.Errorf("Bob OpsPushed = %d, want 1 (his own widget must reach the remote)", syncRes.OpsPushed)
	}
	if syncRes.Unsynced != 0 {
		t.Errorf("Bob Unsynced = %d, want 0", syncRes.Unsynced)
	}

	// Confirm directly on the bare remote: Bob's ref actually landed there,
	// not merely reported as pushed by a stale local count.
	bobChainRef := "refs/writ/fedcba9876543210/acme.widget"
	if tip := strings.TrimSpace(runGitCmd(t, bareDir, "rev-parse", bobChainRef)); tip == "" {
		t.Fatalf("Bob's widget ref did not land on the remote")
	}
}

// TestStoreSync_FetchFailureDoesNotBlockPush isolates WRIT-270 change B: push
// must not be gated on fetch (or refspec-Ensure) succeeding. The other two
// new tests above don't pin this on their own --
// TestStoreSync_BrokenPeerDoesNotStrandLocalWrites' fetch actually succeeds
// under the forced refspec (change A), so Bob's push there would also run
// under the old `if fetchErr == nil && s.hasIdentity && ...` gate; reverting
// just that gate leaves `go test ./engine/... ./cmd/...` fully green (round
// 1 finding). This test instead makes the *fetch phase itself* fail --
// before Fetch ever runs -- for a reason a push to Alice's own namespace
// does not share: PushRefspec is built per invocation and passed on the
// command line, never read from .git/config (spec/ref-layout.md §Exact
// refspecs), so an unwritable .git/config breaks refspec Ensure without
// touching push at all.
func TestStoreSync_FetchFailureDoesNotBlockPush(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions do not block writes")
	}

	bareDir, aliceDir, _ := setupSyncHarness(t)
	ctx := context.Background()

	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Alice failed: %v", err)
	}
	defer sA.Close()

	applyCoreSchema(t, ctx, sA)

	// First sync succeeds normally: Ensure adds the canonical forced
	// refspec, establishing a clean baseline before the induced failure.
	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync of the schema failed: %v", err)
	}

	// An op that must still reach the remote despite the fetch phase
	// failing below.
	if _, err := sA.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Should Still Push"},
	}); err != nil {
		t.Fatalf("Alice create widget: %v", err)
	}

	// Drift the refspec back to the pre-WRIT-270 unforced form directly
	// (bypassing Ensure), so the next Sync's Ensure step has real repair
	// work to do, then make .git read-only-and-executable so that repair
	// write fails. A refspec already in StatusValid makes Ensure a no-op
	// that never touches the config, so the drift is required to force the
	// write. Restricting .git itself, not just .git/config, is required:
	// `git config --add` rewrites the file via a lock-and-rename, which
	// only needs write permission on the containing directory, not on the
	// file it replaces -- chmod-ing the file alone doesn't stop it.
	gitDir := filepath.Join(aliceDir, ".git")
	runGitCmd(t, aliceDir, "config", "--unset-all", "remote.origin.fetch", `^(\+)?refs/writ/`)
	runGitCmd(t, aliceDir, "config", "--add", "remote.origin.fetch", "refs/writ/*:refs/remotes/origin/writ/*")
	if err := os.Chmod(gitDir, 0o555); err != nil {
		t.Fatalf("chmod .git read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

	syncRes, err := sA.Sync(ctx, "origin")
	if err == nil {
		t.Fatalf("expected Sync to report the Ensure/fetch failure, got nil error")
	}
	var syncErr *writ.SyncError
	if !errors.As(err, &syncErr) {
		t.Fatalf("expected error to be *writ.SyncError, got %T: %v", err, err)
	}

	// The push must have landed anyway: this is exactly what change B adds,
	// and what reverting `if fetchErr == nil && s.hasIdentity && ...` breaks.
	if syncRes.OpsPushed != 1 {
		t.Errorf("OpsPushed = %d, want 1 (push must not be gated on fetch/Ensure succeeding)", syncRes.OpsPushed)
	}

	if err := os.Chmod(gitDir, 0o755); err != nil {
		t.Fatalf("restore .git permissions: %v", err)
	}

	// Confirm directly on the bare remote that Alice's op actually landed
	// there, not merely reported as pushed by a stale local count.
	aliceChainRef := "refs/writ/0123456789abcdef/acme.widget"
	if tip := strings.TrimSpace(runGitCmd(t, bareDir, "rev-parse", aliceChainRef)); tip == "" {
		t.Fatalf("Alice's widget ref did not land on the remote")
	}
}

func TestStoreSync_PreReceiveHookFailureAndRetry(t *testing.T) {
	bareDir, aliceDir, bobDir := setupSyncHarness(t)
	ctx := context.Background()

	// The failing pre-receive hook, written below once the schema is
	// already on the remote
	hooksDir := filepath.Join(bareDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hookPath := filepath.Join(hooksDir, "pre-receive")
	hookScript := "#!/bin/sh\necho \"pre-receive hook declined update\" >&2\nexit 1\n"

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

	// The vocabulary is pushed and fetched before the hook goes in, so the
	// op counts below are about the one widget op, not about the schema.
	applyCoreSchema(t, ctx, sA)
	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync of the schema failed: %v", err)
	}
	if _, err := sB.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Bob Sync of the schema failed: %v", err)
	}

	// Install a failing pre-receive hook on the bare remote
	if err := os.WriteFile(hookPath, []byte(hookScript), 0755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	// Alice creates a widget
	widgetID, err := sA.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Hook Failure Widget"},
	})
	if err != nil {
		t.Fatalf("Alice create widget: %v", err)
	}

	// Initial status shows 1 unsynced op
	statusBefore, err := sA.SyncStatus(ctx, "origin")
	if err != nil {
		t.Fatalf("SyncStatus before: %v", err)
	}
	if statusBefore.Unsynced != 1 {
		t.Fatalf("expected 1 unsynced before sync, got %d", statusBefore.Unsynced)
	}

	// Alice attempts to sync, which must fail due to the hook
	syncRes, err := sA.Sync(ctx, "origin")
	if err == nil {
		t.Fatalf("expected Sync to fail with pre-receive hook, but got nil error")
	}

	var syncErr *writ.SyncError
	if !errors.As(err, &syncErr) {
		t.Fatalf("expected error to be *writ.SyncError, got %T: %v", err, err)
	}
	if syncErr.Kind != "rejected" {
		t.Errorf("syncErr.Kind = %q, want %q", syncErr.Kind, "rejected")
	}
	if !errors.Is(err, writ.ErrRefRejected) {
		t.Errorf("errors.Is(err, writ.ErrRefRejected) = false, want true")
	}
	if syncErr.Unsynced != 1 {
		t.Errorf("syncErr.Unsynced = %d, want 1", syncErr.Unsynced)
	}
	if syncRes.Unsynced != 1 {
		t.Errorf("syncRes.Unsynced = %d, want 1", syncRes.Unsynced)
	}

	// Status still truthfully reports 1 unsynced op and no ops lost
	statusAfter, err := sA.SyncStatus(ctx, "origin")
	if err != nil {
		t.Fatalf("SyncStatus after failed sync: %v", err)
	}
	if statusAfter.Unsynced != 1 {
		t.Errorf("statusAfter.Unsynced = %d, want 1", statusAfter.Unsynced)
	}

	// Remove hook to unblock remote
	if err := os.Remove(hookPath); err != nil {
		t.Fatalf("remove hook: %v", err)
	}

	// Retry sync — must succeed and push all surviving local ops
	retryRes, err := sA.Sync(ctx, "origin")
	if err != nil {
		t.Fatalf("Sync retry failed: %v", err)
	}
	if retryRes.OpsPushed != 1 {
		t.Errorf("retry OpsPushed = %d, want 1", retryRes.OpsPushed)
	}
	if retryRes.Unsynced != 0 {
		t.Errorf("retry Unsynced = %d, want 0", retryRes.Unsynced)
	}

	// Bob syncs and verifies the widget landed intact
	syncResB, err := sB.Sync(ctx, "origin")
	if err != nil {
		t.Fatalf("Bob sync failed: %v", err)
	}
	if syncResB.OpsFetched != 1 {
		t.Errorf("Bob OpsFetched = %d, want 1", syncResB.OpsFetched)
	}

	objB, err := sB.Objects.Get(ctx, widgetID)
	if err != nil {
		t.Fatalf("Bob Objects.Get failed: %v", err)
	}
	if objB.Fields["title"] != "Hook Failure Widget" {
		t.Errorf("Bob Fields[title] = %v, want 'Hook Failure Widget'", objB.Fields["title"])
	}
}

// TestStoreSync_InvalidatesVocabulariesForAppendRegardlessOfWindow pins
// WRIT-202 item 3: Store.Sync must invalidate the append-path
// producer-vocabularies cache explicitly once its fetch step completes,
// rather than leaving vocabFreshnessWindow to expire on its own. Bob's
// clock is frozen at the exact instant his cache was warmed and never
// advances a single tick for the rest of the test — if Sync relied on the
// window expiring instead of invalidating explicitly, Bob's next append
// would still see the pre-Alice vocabulary and refuse an op of the type
// Alice just declared and pushed.
//
// The awkward shape below — Sync driven from a goroutine while the test
// holds the mutex Refresh needs — is what it takes for this to be a real
// regression net rather than a green test that proves nothing. Sync's step
// 4 calls Store.Refresh unconditionally, Refresh calls rules ->
// vocabularies, and that pass re-resolves and re-stamps vocabObservedAt
// whether or not step 3.5 invalidated anything. Read the append path after
// Sync returns and you are reading the Refresh's work: the round-1
// reviewer confirmed by mutation that the straightforward version of this
// test stays green with s.invalidateVocabularies() deleted from Sync
// outright. The invalidation is observable only in the gap between the
// fetch and the Refresh, so this test parks the Refresh and looks into it.
func TestStoreSync_InvalidatesVocabulariesForAppendRegardlessOfWindow(t *testing.T) {
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

	// Based on the real clock rather than an arbitrary fixed date, for the
	// same reason schema_test.go's WRIT-202 tests are: Store.Open's own
	// initial-rules resolve may already have stamped vocabObservedAt with
	// the real time.Now, and a frozen instant far from "now" would make
	// the window's Sub arithmetic go negative and read as perpetually
	// fresh instead of testing anything.
	frozen := time.Now()
	writ.SetStoreClock(sB, func() time.Time { return frozen })

	// Warm Bob's append-path cache at exactly the frozen instant, before
	// Alice's change exists.
	before, err := writ.StoreVocabulariesForAppend(sB, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (warm) failed: %v", err)
	}
	if v, ok := before["acme.gizmo"]; ok && v.Declared {
		t.Fatalf("acme.gizmo unexpectedly already declared before Alice wrote it")
	}

	// Alice declares a new type and pushes it.
	if err := sA.ApplySchema(ctx, compileTestSchema(t, "sch-peer", `namespace acme
description "peer schema for the sync-invalidation test"

type gizmo {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
		t.Fatalf("Alice ApplySchema failed: %v", err)
	}
	if _, err := sA.Sync(ctx, "origin"); err != nil {
		t.Fatalf("Alice Sync failed: %v", err)
	}

	// Bob's clock has not moved a single tick since his cache was warmed
	// above — the window alone would still call this fresh. Hold the mutex
	// Refresh must acquire, so Bob's Sync runs its fetch, invalidates, and
	// then parks before it can re-resolve anything of its own.
	release := writ.StoreHoldRefreshLock(sB)
	syncDone := make(chan error, 1)
	go func() {
		_, err := sB.Sync(ctx, "origin")
		syncDone <- err
	}()

	// Poll rather than assert once: the fetch is a real git subprocess, so
	// "Sync has reached the invalidation" is not an instant this test can
	// name. Sync cannot finish while the lock is held — Refresh is
	// unconditional and has no early exit before it — so the only two
	// outcomes are "the invalidation ran" and "the deadline expired". Bob's
	// clock is frozen, so no amount of real time spent polling can let the
	// window expire on its own and hand this test a false pass.
	sawGizmo := false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		vocab, err := writ.StoreVocabulariesForAppend(sB, ctx)
		if err == nil {
			if v, ok := vocab["acme.gizmo"]; ok && v.Declared {
				sawGizmo = true
				break
			}
		}
		time.Sleep(2 * time.Millisecond)
	}

	release()
	if err := <-syncDone; err != nil {
		t.Fatalf("Bob Sync failed: %v", err)
	}
	if !sawGizmo {
		t.Fatalf("Bob's vocabulariesForAppend never saw acme.gizmo in the window between Sync's fetch and its Refresh: Sync did not invalidate the append-path cache, so the frozen-clock freshness window kept serving the pre-fetch snapshot")
	}

	// And the post-Sync state is right too — this part would pass on its
	// own either way (Refresh re-resolves regardless), so it is a sanity
	// check, not the assertion that carries the test.
	after, err := writ.StoreVocabulariesForAppend(sB, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend after Sync failed: %v", err)
	}
	if v, declared := after["acme.gizmo"]; !declared || !v.Declared {
		t.Fatalf("Bob's vocabulariesForAppend does not see acme.gizmo after Sync returned")
	}
}
