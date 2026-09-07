package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/sync"
)

func dummySigner() writ.Signer {
	return codec.SignerFunc(func(ctx context.Context, payload []byte) (string, error) {
		return "dummy-signature", nil
	})
}

// applyTicketSchemaViaStore applies ticketObjectTestSchema against store
// directly (writeSchemaFile + buildSchemaPlan + Store.ApplySchema), rather
// than through the CLI's own `schema apply` (applyTicketObjectSchema in
// object_test.go): a sync test harness's git config carries fake SSH
// signing key material that could never actually sign, so every write here
// goes through store's own dummySigner instead of real identity-based
// signing.
func applyTicketSchemaViaStore(t *testing.T, ctx context.Context, store *writ.Store, dir string) {
	t.Helper()
	writeSchemaFile(t, dir, ticketObjectTestSchema)
	planRes, err := buildSchemaPlan(ctx, store, dir)
	if err != nil {
		t.Fatalf("build schema plan: %v", err)
	}
	if err := store.ApplySchema(ctx, planRes.ops); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
}

func setupSyncTestHarness(t *testing.T) (bareDir, aliceDir, bobDir string) {
	t.Helper()
	requireGit(t)
	tempDir := t.TempDir()

	globalCfgPath := filepath.Join(tempDir, "global_gitconfig")
	if err := os.WriteFile(globalCfgPath, []byte(""), 0600); err != nil {
		t.Fatalf("writing empty global config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfgPath)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	// 1. Bare remote repo
	bareDir = filepath.Join(tempDir, "remote.git")
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", bareDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init bare failed: %v (%s)", err, string(out))
	}

	// 2. Alice's clone
	aliceDir = filepath.Join(tempDir, "alice")
	cmd = exec.Command("git", "init", "--initial-branch=main", aliceDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init alice failed: %v (%s)", err, string(out))
	}
	setGitConfig(t, aliceDir, "user.name", "Alice")
	setGitConfig(t, aliceDir, "user.email", "alice@example.com")
	setGitConfig(t, aliceDir, "writ.writerId", "0123456789abcdef")
	setGitConfig(t, aliceDir, "gpg.format", "ssh")
	setGitConfig(t, aliceDir, "user.signingKey", "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGalice")
	addRemote(t, aliceDir, "origin", bareDir)

	// Commit dummy file so HEAD exists and push main branch
	dummyFile := filepath.Join(aliceDir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("# Project\n"), 0644); err != nil {
		t.Fatalf("write dummy file: %v", err)
	}
	cmd = exec.Command("git", "add", "README.md")
	cmd.Dir = aliceDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = aliceDir
	_ = cmd.Run()
	cmd = exec.Command("git", "push", "origin", "HEAD:main")
	cmd.Dir = aliceDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initial push failed: %v (%s)", err, string(out))
	}

	// 3. Bob's clone
	bobDir = filepath.Join(tempDir, "bob")
	cmd = exec.Command("git", "clone", bareDir, bobDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone bob failed: %v (%s)", err, string(out))
	}
	setGitConfig(t, bobDir, "user.name", "Bob")
	setGitConfig(t, bobDir, "user.email", "bob@example.com")
	setGitConfig(t, bobDir, "writ.writerId", "fedcba9876543210")
	setGitConfig(t, bobDir, "gpg.format", "ssh")
	setGitConfig(t, bobDir, "user.signingKey", "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGbob")

	return bareDir, aliceDir, bobDir
}

func TestSync_RoundTripAndRefold(t *testing.T) {
	_, aliceDir, bobDir := setupSyncTestHarness(t)
	ctx := context.Background()

	// Alice declares the ticket type, so Bob's fold below has a vocabulary
	// to fold "ticket" objects through.
	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Alice failed: %v", err)
	}
	applyTicketSchemaViaStore(t, ctx, sA, aliceDir)
	sA.Close()

	// Alice syncs the schema op via CLI
	var stdoutSchemaA, stderrSchemaA bytes.Buffer
	codeSchemaA := run(ctx, []string{"-C", aliceDir, "sync"}, &stdoutSchemaA, &stderrSchemaA)
	if codeSchemaA != 0 {
		t.Fatalf("Alice schema sync exited with %d; stderr: %s", codeSchemaA, stderrSchemaA.String())
	}
	if !strings.Contains(stdoutSchemaA.String(), "origin: pushed") {
		t.Errorf("Alice schema stdout does not mention pushed ops: %s", stdoutSchemaA.String())
	}
	var stdoutSchemaB, stderrSchemaB bytes.Buffer
	codeSchemaB := run(ctx, []string{"-C", bobDir, "sync"}, &stdoutSchemaB, &stderrSchemaB)
	if codeSchemaB != 0 {
		t.Fatalf("Bob schema sync exited with %d; stderr: %s", codeSchemaB, stderrSchemaB.String())
	}
	if !strings.Contains(stdoutSchemaB.String(), "origin: fetched") {
		t.Errorf("Bob schema stdout does not mention fetched ops: %s", stdoutSchemaB.String())
	}

	// Alice creates a ticket
	sA, err = writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Reopen Alice failed: %v", err)
	}
	objectID, err := sA.Objects.Create(ctx, "ticket", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Round Trip Sync Ticket"},
	})
	if err != nil {
		sA.Close()
		t.Fatalf("Alice create object: %v", err)
	}
	sA.Close()

	// Alice syncs via CLI
	var stdoutA, stderrA bytes.Buffer
	codeA := run(ctx, []string{"-C", aliceDir, "sync"}, &stdoutA, &stderrA)
	if codeA != 0 {
		t.Fatalf("Alice sync exited with %d; stderr: %s", codeA, stderrA.String())
	}
	if !strings.Contains(stdoutA.String(), "origin: pushed 1 op") {
		t.Errorf("Alice stdout does not mention pushed 1 op: %s", stdoutA.String())
	}

	// Bob syncs via CLI
	var stdoutB, stderrB bytes.Buffer
	codeB := run(ctx, []string{"-C", bobDir, "sync"}, &stdoutB, &stderrB)
	if codeB != 0 {
		t.Fatalf("Bob sync exited with %d; stderr: %s", codeB, stderrB.String())
	}
	if !strings.Contains(stdoutB.String(), "origin: fetched 1 op") {
		t.Errorf("Bob stdout does not mention fetched 1 op: %s", stdoutB.String())
	}

	// Verify Bob's projection has Alice's ticket (refold happened)
	sB, err := writ.Open(bobDir)
	if err != nil {
		t.Fatalf("Open Bob failed: %v", err)
	}
	defer sB.Close()

	if _, err := sB.Query.Object(objectID); err != nil {
		t.Fatalf("Bob Query.Object failed: %v", err)
	}
	objB, err := sB.Objects.Get(ctx, objectID)
	if err != nil {
		t.Fatalf("Bob Objects.Get failed: %v", err)
	}
	if objB.Fields["title"] != "Round Trip Sync Ticket" {
		t.Errorf("Bob object title = %v, want 'Round Trip Sync Ticket'", objB.Fields["title"])
	}
}

func TestSync_Idempotent(t *testing.T) {
	_, aliceDir, _ := setupSyncTestHarness(t)
	ctx := context.Background()

	// Initial sync
	var stdout1, stderr1 bytes.Buffer
	code1 := run(ctx, []string{"-C", aliceDir, "sync"}, &stdout1, &stderr1)
	if code1 != 0 {
		t.Fatalf("Alice first sync exited with %d; stderr: %s", code1, stderr1.String())
	}
	if !strings.Contains(stdout1.String(), "origin: up to date") {
		t.Errorf("First sync with no ops should be up to date, got: %s", stdout1.String())
	}

	// Second sync with no changes
	var stdout2, stderr2 bytes.Buffer
	code2 := run(ctx, []string{"-C", aliceDir, "sync"}, &stdout2, &stderr2)
	if code2 != 0 {
		t.Fatalf("Alice second sync exited with %d; stderr: %s", code2, stderr2.String())
	}
	if strings.TrimSpace(stdout2.String()) != "origin: up to date" {
		t.Errorf("Second sync stdout = %q, want 'origin: up to date'", strings.TrimSpace(stdout2.String()))
	}
}

func TestSync_StatusOffline(t *testing.T) {
	_, aliceDir, _ := setupSyncTestHarness(t)
	ctx := context.Background()

	// Alice declares and syncs the ticket type first, so it is not itself
	// the unsynced op this test measures below.
	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Alice failed: %v", err)
	}
	applyTicketSchemaViaStore(t, ctx, sA, aliceDir)
	sA.Close()
	var stdoutSchema, stderrSchema bytes.Buffer
	if code := run(ctx, []string{"-C", aliceDir, "sync"}, &stdoutSchema, &stderrSchema); code != 0 {
		t.Fatalf("Alice schema sync exited with %d; stderr: %s", code, stderrSchema.String())
	}

	// Alice creates a ticket
	sA, err = writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Reopen Alice failed: %v", err)
	}
	_, err = sA.Objects.Create(ctx, "ticket", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Offline Status Ticket"},
	})
	if err != nil {
		sA.Close()
		t.Fatalf("Alice create object: %v", err)
	}

	// Check status via public engine API
	expectedStatus, err := sA.SyncStatus(ctx, "origin")
	if err != nil {
		sA.Close()
		t.Fatalf("Alice engine SyncStatus: %v", err)
	}
	sA.Close()

	if expectedStatus.Unsynced != 1 {
		t.Fatalf("expectedStatus.Unsynced = %d, want 1", expectedStatus.Unsynced)
	}

	// Point origin to a nonexistent path to prove status is offline
	setGitConfig(t, aliceDir, "remote.origin.url", "/nonexistent/remote/path.git")

	// CLI --status should succeed offline and match engine status
	var stdoutStat, stderrStat bytes.Buffer
	codeStat := run(ctx, []string{"-C", aliceDir, "sync", "--status"}, &stdoutStat, &stderrStat)
	if codeStat != 0 {
		t.Fatalf("sync --status exited with %d; stderr: %s", codeStat, stderrStat.String())
	}
	if strings.TrimSpace(stdoutStat.String()) != "origin: 1 op unsynced" {
		t.Errorf("sync --status output = %q, want 'origin: 1 op unsynced'", strings.TrimSpace(stdoutStat.String()))
	}

	// Plain sync should fail against nonexistent remote
	var stdoutFail, stderrFail bytes.Buffer
	codeFail := run(ctx, []string{"-C", aliceDir, "sync"}, &stdoutFail, &stderrFail)
	if codeFail == 0 {
		t.Fatalf("sync against broken remote unexpectedly succeeded: %s", stdoutFail.String())
	}
	if codeFail != 3 && codeFail != 1 {
		t.Errorf("sync exit code = %d, want 3 or 1", codeFail)
	}
}

func TestSync_RefspecSelfHealing(t *testing.T) {
	bareDir, _, _ := setupSyncTestHarness(t)
	ctx := context.Background()

	// Clone a fresh repo that never ran writ init
	tempDir := t.TempDir()
	freshDir := filepath.Join(tempDir, "fresh")
	cmd := exec.Command("git", "clone", bareDir, freshDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone fresh failed: %v (%s)", err, string(out))
	}
	setGitConfig(t, freshDir, "user.name", "Fresh")
	setGitConfig(t, freshDir, "user.email", "fresh@example.com")
	setGitConfig(t, freshDir, "writ.writerId", "1111222233334444")
	setGitConfig(t, freshDir, "gpg.format", "ssh")
	setGitConfig(t, freshDir, "user.signingKey", "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGfresh")

	// Ensure no fetch refspec exists initially
	initialFetch := getGitConfigAll(t, freshDir, "remote.origin.fetch")
	for _, f := range initialFetch {
		if strings.Contains(f, "writ") {
			t.Fatalf("unexpected writ fetch refspec before sync: %v", initialFetch)
		}
	}

	// Run sync
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"-C", freshDir, "sync"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("sync in fresh clone exited with %d; stderr: %s", code, stderr.String())
	}

	// Verify writ fetch refspec was self-healed/configured
	afterFetch := getGitConfigAll(t, freshDir, "remote.origin.fetch")
	hasWritRefspec := false
	for _, f := range afterFetch {
		if strings.Contains(f, "refs/writ/*:refs/remotes/origin/writ/*") {
			hasWritRefspec = true
			break
		}
	}
	if !hasWritRefspec {
		t.Errorf("writ fetch refspec was not configured by sync: %v", afterFetch)
	}
}

func TestSync_ExitCodeClassification(t *testing.T) {
	t.Run("unit_exitCodeFor", func(t *testing.T) {
		tests := []struct {
			name     string
			err      error
			wantCode int
		}{
			{name: "nil", err: nil, wantCode: 0},
			{name: "writ.ErrAuth", err: writ.ErrAuth, wantCode: 6},
			{name: "sync.ErrAuth", err: sync.ErrAuth, wantCode: 6},
			{name: "writ.SyncError auth", err: &writ.SyncError{Kind: "auth", Err: writ.ErrAuth}, wantCode: 6},
			{name: "sync.GitError auth", err: &sync.GitError{Kind: sync.FailureKindAuth, Err: sync.ErrAuth}, wantCode: 6},
			{name: "writ.ErrNetwork", err: writ.ErrNetwork, wantCode: 7},
			{name: "sync.ErrNetwork", err: sync.ErrNetwork, wantCode: 7},
			{name: "writ.SyncError network", err: &writ.SyncError{Kind: "network", Err: writ.ErrNetwork}, wantCode: 7},
			{name: "sync.GitError network", err: &sync.GitError{Kind: sync.FailureKindNetwork, Err: sync.ErrNetwork}, wantCode: 7},
			{name: "writ.ErrUnknownRemote", err: writ.ErrUnknownRemote, wantCode: 3},
			{name: "sync.ErrUnknownRemote", err: sync.ErrUnknownRemote, wantCode: 3},
			{name: "wrapped UnknownRemote", err: fmt.Errorf("fetch failed: %w", writ.ErrUnknownRemote), wantCode: 3},
			{name: "git error UnknownRemote", err: &sync.GitError{Err: sync.ErrUnknownRemote, Kind: sync.FailureKindNotFound}, wantCode: 3},
			{name: "writ.ErrNonFastForward", err: writ.ErrNonFastForward, wantCode: 4},
			{name: "sync.ErrNonFastForward", err: sync.ErrNonFastForward, wantCode: 4},
			{name: "wrapped NonFastForward", err: fmt.Errorf("push failed: %w", writ.ErrNonFastForward), wantCode: 4},
			{name: "git error NonFastForward", err: &sync.GitError{Err: sync.ErrNonFastForward, Kind: sync.FailureKindRejected}, wantCode: 4},
			{name: "git error generic", err: &sync.GitError{Err: errors.New("exec error"), Kind: sync.FailureKindUnknown}, wantCode: 1},
			{name: "generic transport error", err: errors.New("something went wrong"), wantCode: 1},
			{name: "not a git repo", err: errors.New("writ: not a git repository (or any parent up to mount point): /tmp/dir"), wantCode: 5},
			{name: "stat path error", err: errors.New("writ: stat path \"/nonexistent/path\": no such file or directory"), wantCode: 5},
			{name: "git.ErrRepositoryNotExists", err: git.ErrRepositoryNotExists, wantCode: 5},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got := exitCodeFor(tc.err)
				if got != tc.wantCode {
					t.Errorf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.wantCode)
				}
			})
		}
	})

	t.Run("e2e_auth_failure", func(t *testing.T) {
		_, aliceDir, _ := setupSyncTestHarness(t)
		// Create an SSH wrapper script that outputs publickey denial
		shimDir := t.TempDir()
		shimPath := filepath.Join(shimDir, "fake_ssh")
		shimScript := "#!/bin/sh\necho \"git@github.com: Permission denied (publickey).\" >&2\nexit 255\n"
		if err := os.WriteFile(shimPath, []byte(shimScript), 0755); err != nil {
			t.Fatalf("write ssh shim: %v", err)
		}
		t.Setenv("GIT_SSH_COMMAND", shimPath)

		// Point remote to an SSH URL
		addRemote(t, aliceDir, "sshremote", "git@github.com:example/repo.git")

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-C", aliceDir, "sync", "sshremote"}, &stdout, &stderr)
		if code != 6 {
			t.Errorf("sync with auth failure exit code = %d (want 6); stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "auth") {
			t.Errorf("stderr does not mention auth kind: %s", stderr.String())
		}
		if !strings.Contains(stderr.String(), "advice:") {
			t.Errorf("stderr does not include advice: %s", stderr.String())
		}
	})

	t.Run("e2e_network_failure", func(t *testing.T) {
		_, aliceDir, _ := setupSyncTestHarness(t)
		// Add an unroutable HTTP remote
		addRemote(t, aliceDir, "netremote", "http://127.0.0.1:9999/unreachable.git")

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-C", aliceDir, "sync", "netremote"}, &stdout, &stderr)
		if code != 7 {
			t.Errorf("sync with network failure exit code = %d (want 7); stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "network") {
			t.Errorf("stderr does not mention network kind: %s", stderr.String())
		}
	})

	t.Run("e2e_unknown_remote", func(t *testing.T) {
		_, aliceDir, _ := setupSyncTestHarness(t)
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-C", aliceDir, "sync", "nosuchremote"}, &stdout, &stderr)
		if code != 3 {
			t.Errorf("sync nosuchremote exit code = %d (want 3); stderr: %s", code, stderr.String())
		}
	})

	t.Run("e2e_non_git_repo", func(t *testing.T) {
		nonRepoDir := t.TempDir()
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-C", nonRepoDir, "sync"}, &stdout, &stderr)
		if code != 5 {
			t.Errorf("sync on non-git dir exit code = %d (want 5); stderr: %s", code, stderr.String())
		}
	})

	t.Run("e2e_nonexistent_dir", func(t *testing.T) {
		nonexistentDir := filepath.Join(t.TempDir(), "does_not_exist")
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-C", nonexistentDir, "sync"}, &stdout, &stderr)
		if code != 5 {
			t.Errorf("sync on nonexistent dir exit code = %d (want 5); stderr: %s", code, stderr.String())
		}
	})

	t.Run("e2e_usage_errors", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		ctx := context.Background()

		// 1. Bad flag
		var stdout1, stderr1 bytes.Buffer
		code1 := run(ctx, []string{"-C", env.repoDir, "sync", "--badflag"}, &stdout1, &stderr1)
		if code1 != 2 {
			t.Errorf("bad flag exit code = %d, want 2", code1)
		}

		// 2. No configured remotes and no positional arg
		var stdout2, stderr2 bytes.Buffer
		code2 := run(ctx, []string{"-C", env.repoDir, "sync"}, &stdout2, &stderr2)
		if code2 != 2 {
			t.Errorf("no remotes exit code = %d, want 2; stderr: %s", code2, stderr2.String())
		}

		// 3. Multiple remotes and none named origin
		addRemote(t, env.repoDir, "upstream", "https://example.com/upstream.git")
		addRemote(t, env.repoDir, "mirror", "https://example.com/mirror.git")
		var stdout3, stderr3 bytes.Buffer
		code3 := run(ctx, []string{"-C", env.repoDir, "sync"}, &stdout3, &stderr3)
		if code3 != 2 {
			t.Errorf("multiple remotes without origin exit code = %d, want 2; stderr: %s", code3, stderr3.String())
		}
	})
}

func TestSync_JSONOutput(t *testing.T) {
	_, aliceDir, _ := setupSyncTestHarness(t)
	ctx := context.Background()

	// Alice declares and syncs the ticket type first, so it is not itself
	// the unsynced op the assertions below count.
	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Alice failed: %v", err)
	}
	applyTicketSchemaViaStore(t, ctx, sA, aliceDir)
	sA.Close()
	var stdoutSchema, stderrSchema bytes.Buffer
	if code := run(ctx, []string{"-C", aliceDir, "sync"}, &stdoutSchema, &stderrSchema); code != 0 {
		t.Fatalf("Alice schema sync exited with %d; stderr: %s", code, stderrSchema.String())
	}

	// Alice creates a ticket
	sA, err = writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Reopen Alice failed: %v", err)
	}
	_, err = sA.Objects.Create(ctx, "ticket", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "JSON Output Ticket"},
	})
	if err != nil {
		sA.Close()
		t.Fatalf("Alice create object: %v", err)
	}
	sA.Close()

	// 1. Sync with --status --json
	var stdoutStatus, stderrStatus bytes.Buffer
	codeStatus := run(ctx, []string{"-C", aliceDir, "sync", "--status", "--json"}, &stdoutStatus, &stderrStatus)
	if codeStatus != 0 {
		t.Fatalf("sync --status --json exited with %d; stderr: %s", codeStatus, stderrStatus.String())
	}

	type statusEnvelope struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Data          []struct {
			Remote   string `json:"remote"`
			Unsynced int    `json:"unsynced"`
		} `json:"data"`
	}
	var envStatus statusEnvelope
	if err := json.Unmarshal(stdoutStatus.Bytes(), &envStatus); err != nil {
		t.Fatalf("unmarshal --status --json: %v (raw: %s)", err, stdoutStatus.String())
	}
	if envStatus.SchemaVersion != 1 || envStatus.Kind != "sync.status" {
		t.Errorf("unexpected envelope header: schema_version=%d, kind=%q", envStatus.SchemaVersion, envStatus.Kind)
	}
	if len(envStatus.Data) != 1 {
		t.Fatalf("expected 1 remote in status json, got %d", len(envStatus.Data))
	}
	if envStatus.Data[0].Remote != "origin" || envStatus.Data[0].Unsynced != 1 {
		t.Errorf("status json remote = %+v, want {Remote: origin, Unsynced: 1}", envStatus.Data[0])
	}

	// 2. Sync with --json
	var stdoutSync, stderrSync bytes.Buffer
	codeSync := run(ctx, []string{"-C", aliceDir, "sync", "--json"}, &stdoutSync, &stderrSync)
	if codeSync != 0 {
		t.Fatalf("sync --json exited with %d; stderr: %s", codeSync, stderrSync.String())
	}

	type syncEnvelope struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Data          []struct {
			Remote         string `json:"remote"`
			OpsFetched     int    `json:"ops_fetched"`
			OpsPushed      int    `json:"ops_pushed"`
			ObjectsTouched int    `json:"objects_touched"`
			Unsynced       int    `json:"unsynced"`
		} `json:"data"`
	}
	var envSync syncEnvelope
	if err := json.Unmarshal(stdoutSync.Bytes(), &envSync); err != nil {
		t.Fatalf("unmarshal sync --json: %v (raw: %s)", err, stdoutSync.String())
	}
	if envSync.SchemaVersion != 1 || envSync.Kind != "sync.result" {
		t.Errorf("unexpected envelope header: schema_version=%d, kind=%q", envSync.SchemaVersion, envSync.Kind)
	}
	if len(envSync.Data) != 1 {
		t.Fatalf("expected 1 remote in sync json, got %d", len(envSync.Data))
	}
	r := envSync.Data[0]
	if r.Remote != "origin" || r.OpsPushed != 1 || r.Unsynced != 0 {
		t.Errorf("sync json remote = %+v, want {Remote: origin, OpsPushed: 1, Unsynced: 0}", r)
	}

	// 3. Multi-remote partial failure with --json
	addRemote(t, aliceDir, "badremote", "http://127.0.0.1:9999/unreachable.git")
	var stdoutMulti, stderrMulti bytes.Buffer
	codeMulti := run(ctx, []string{"-C", aliceDir, "sync", "--json", "origin", "badremote"}, &stdoutMulti, &stderrMulti)
	if codeMulti != 7 {
		t.Fatalf("sync multi-remote exited with %d (want 7); stderr: %s", codeMulti, stderrMulti.String())
	}
	type multiEnvelope struct {
		SchemaVersion int    `json:"schema_version"`
		Kind          string `json:"kind"`
		Data          []struct {
			Remote   string `json:"remote"`
			Unsynced int    `json:"unsynced"`
			Failure  *struct {
				Kind      string `json:"kind"`
				Message   string `json:"message"`
				Advice    string `json:"advice"`
				Retryable bool   `json:"retryable"`
			} `json:"failure,omitempty"`
		} `json:"data"`
	}
	var envMulti multiEnvelope
	if err := json.Unmarshal(stdoutMulti.Bytes(), &envMulti); err != nil {
		t.Fatalf("unmarshal multi-remote sync --json: %v (raw: %s)", err, stdoutMulti.String())
	}
	if len(envMulti.Data) != 2 {
		t.Fatalf("expected 2 remotes in data, got %d", len(envMulti.Data))
	}
	if envMulti.Data[0].Remote != "origin" || envMulti.Data[0].Failure != nil {
		t.Errorf("expected origin to have no failure, got %+v", envMulti.Data[0])
	}
	if envMulti.Data[1].Remote != "badremote" || envMulti.Data[1].Failure == nil || envMulti.Data[1].Failure.Kind != "network" {
		t.Errorf("expected badremote to have network failure, got %+v", envMulti.Data[1])
	}
}

func TestSync_UnconfiguredWriterWarning(t *testing.T) {
	bareDir, _, _ := setupSyncTestHarness(t)
	ctx := context.Background()

	// Clone repo without writer ID configured
	tempDir := t.TempDir()
	readOnlyDir := filepath.Join(tempDir, "readonly")
	cmd := exec.Command("git", "clone", bareDir, readOnlyDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone readonly failed: %v (%s)", err, string(out))
	}

	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"-C", readOnlyDir, "sync"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("sync with no writer ID exited with %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "warning: no writer identity configured (run 'writ init' to configure)") {
		t.Errorf("stderr does not contain writer identity warning: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "origin: up to date") {
		t.Errorf("stdout = %q, want 'origin: up to date'", stdout.String())
	}
}

func TestSync_RemoteResolution(t *testing.T) {
	env := setupTestCLIEnv(t)
	ctx := context.Background()

	addRemote(t, env.repoDir, "origin", "https://example.com/origin.git")
	addRemote(t, env.repoDir, "upstream", "https://example.com/upstream.git")

	t.Run("default_resolves_to_origin", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(ctx, []string{"-C", env.repoDir, "sync", "--status"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("sync --status exited with %d; stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "origin:") {
			t.Errorf("stdout does not target origin by default: %s", stdout.String())
		}
		if strings.Contains(stdout.String(), "upstream:") {
			t.Errorf("stdout unexpectedly contains upstream when origin is default: %s", stdout.String())
		}
	})

	t.Run("sole_remote_resolves_when_no_origin", func(t *testing.T) {
		soleEnv := setupTestCLIEnv(t)
		addRemote(t, soleEnv.repoDir, "custom", "https://example.com/custom.git")

		var stdout, stderr bytes.Buffer
		code := run(ctx, []string{"-C", soleEnv.repoDir, "sync", "--status"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("sync --status exited with %d; stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "custom:") {
			t.Errorf("stdout does not target sole remote 'custom': %s", stdout.String())
		}
	})

	t.Run("positional_multiple_remotes", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(ctx, []string{"-C", env.repoDir, "sync", "--status", "origin", "upstream"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("sync --status origin upstream exited with %d; stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "origin:") || !strings.Contains(stdout.String(), "upstream:") {
			t.Errorf("stdout does not contain both remotes: %s", stdout.String())
		}
	})
}

func TestSync_Help(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"sync", flag}, &stdout, &stderr)
		if code != 0 {
			t.Errorf("sync %s exit code = %d, want 0", flag, code)
		}
		errStr := stderr.String()
		if !strings.Contains(errStr, "Usage: writ sync") {
			t.Errorf("help output does not contain usage: %s", errStr)
		}
		if !strings.Contains(errStr, "Exit codes:") {
			t.Errorf("help output does not document exit codes: %s", errStr)
		}
	}
}
