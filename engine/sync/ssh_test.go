package sync_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	writsync "github.com/writtendev/writ/engine/sync"
)

// createSSHShim writes an executable script suitable for GIT_SSH_COMMAND
// that intercepts ssh execution and invokes the target git-upload-pack /
// git-receive-pack command locally. This proves system git takes its real SSH
// transport codepath without requiring a live sshd in CI environments.
func createSSHShim(t *testing.T) string {
	t.Helper()
	shimFile := filepath.Join(t.TempDir(), "git-ssh-shim.sh")
	script := "#!/bin/sh\nfor a in \"$@\"; do last=\"$a\"; done\neval \"$last\"\n"
	if err := os.WriteFile(shimFile, []byte(script), 0o755); err != nil {
		t.Fatalf("write ssh shim: %v", err)
	}
	return shimFile
}

func TestSSH_TransportViaShim(t *testing.T) {
	bareDir, bareRepo := initBareRepo(t)
	aliceDir, _ := initTestRepo(t)
	bobDir, bobRepo := initTestRepo(t)

	shimPath := createSSHShim(t)
	sshRemoteURL := "ssh://localhost" + bareDir

	aliceID := "0123456789abcdef"
	bobID := "fedcba9876543210"

	aliceIdent := testIdentity(aliceID, "Alice", "alice@example.com")
	bobIdent := testIdentity(bobID, "Bob", "bob@example.com")

	aliceStore := mustOpenStore(t, aliceDir, aliceIdent)
	bobStore := mustOpenStore(t, bobDir, bobIdent)

	// Configure ssh remote URL on both repos
	for _, dir := range []string{aliceDir, bobDir} {
		cmd := exec.Command("git", "remote", "add", "origin", sshRemoteURL)
		cmd.Dir = dir
		if err := cmd.Run(); err != nil {
			t.Fatalf("git remote add: %v", err)
		}
	}

	// Open clients configured with GIT_SSH_COMMAND environment
	aliceSync, err := writsync.Open(aliceDir, aliceIdent, writsync.WithEnv([]string{"GIT_SSH_COMMAND=" + shimPath}))
	if err != nil {
		t.Fatalf("open alice sync: %v", err)
	}
	bobSync, err := writsync.Open(bobDir, bobIdent, writsync.WithEnv([]string{"GIT_SSH_COMMAND=" + shimPath}))
	if err != nil {
		t.Fatalf("open bob sync: %v", err)
	}

	ctx := context.Background()

	// 1. Ensure refspecs on both
	if _, err := aliceSync.Ensure(ctx, "origin"); err != nil {
		t.Fatalf("alice Ensure: %v", err)
	}
	if _, err := bobSync.Ensure(ctx, "origin"); err != nil {
		t.Fatalf("bob Ensure: %v", err)
	}

	// 2. Alice appends and pushes over SSH
	aliceOpID := appendTestOp(t, aliceStore, "widget", "w-ssh", "create", map[string]any{"title": "SSH Widget"})
	pushRes, err := aliceSync.Push(ctx, "origin")
	if err != nil {
		t.Fatalf("alice Push over SSH failed: %v", err)
	}
	if len(pushRes.PushedRefs) == 0 {
		t.Fatalf("expected pushed refs in push result")
	}

	// 3. Verify bare repo received the ref
	bareRefs := snapshotAllRefs(t, bareRepo)
	aliceRefName := "refs/writ/" + aliceID + "/widget"
	if tip, ok := bareRefs[aliceRefName]; !ok || tip != aliceOpID {
		t.Fatalf("bare repo missing %s, refs: %v", aliceRefName, bareRefs)
	}

	// 4. Bob fetches over SSH
	fetchRes, err := bobSync.Fetch(ctx, "origin")
	if err != nil {
		t.Fatalf("bob Fetch over SSH failed: %v", err)
	}
	if len(fetchRes.Updates) == 0 {
		t.Fatalf("expected chain updates in fetch result")
	}

	// 5. Verify Bob has Alice's remote tracking ref
	bobRefs := snapshotAllRefs(t, bobRepo)
	trackingRef := "refs/remotes/origin/writ/" + aliceID + "/widget"
	if tip, ok := bobRefs[trackingRef]; !ok || tip != aliceOpID {
		t.Fatalf("bob missing tracking ref %s = %s, refs: %v", trackingRef, aliceOpID, bobRefs)
	}

	// 6. Verify Bob's store can enumerate Alice's op
	enumRes, err := bobStore.Enumerate()
	if err != nil {
		t.Fatalf("bobStore.Enumerate failed: %v", err)
	}
	if len(enumRes.Ops["w-ssh"]) != 1 {
		t.Fatalf("expected w-ssh op in Bob's enumeration: %v", enumRes.Ops)
	}
}

func TestSync_ZeroCredentialCode(t *testing.T) {
	// Assert that nothing under engine/sync parses or manages credentials, passwords, tokens, or private keys.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	forbiddenKeywords := []string{
		"password",
		"token",
		"privatekey",
		"private_key",
		"passphrase",
		"credential",
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(entry.Name())
		if err != nil {
			t.Fatalf("read file %s: %v", entry.Name(), err)
		}
		contentLower := strings.ToLower(string(data))
		for _, kw := range forbiddenKeywords {
			if strings.Contains(contentLower, kw) {
				// Allow "credential" in comments discussing zero credential code or git auth advice
				if kw == "credential" && (strings.Contains(contentLower, "zero credential") || strings.Contains(contentLower, "credential helper")) {
					continue
				}
				// Allow "password" in git error classification pattern matching
				if kw == "password" && strings.Contains(contentLower, "could not read password") {
					continue
				}
				t.Fatalf("file %s contains forbidden credential management term %q (zero credential code in Writ)", entry.Name(), kw)
			}
		}
	}
}
