package writ_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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

