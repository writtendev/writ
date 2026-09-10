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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/schemasrc"
	"github.com/writtendev/writ/engine/sync"
	"github.com/writtendev/writ/spec"
)

func TestInit_Idempotent(t *testing.T) {
	env := setupTestCLIEnv(t)
	addRemote(t, env.repoDir, "origin", "https://example.com/repo.git")

	var stdout1, stderr1 bytes.Buffer
	code1 := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout1, &stderr1)
	if code1 != 0 {
		t.Fatalf("run init (first) exited with %d; stderr: %s", code1, stderr1.String())
	}

	fetchEntries1 := getGitConfigAll(t, env.repoDir, "remote.origin.fetch")
	var writEntries1 []string
	for _, e := range fetchEntries1 {
		if strings.Contains(e, "writ") {
			writEntries1 = append(writEntries1, e)
		}
	}
	if len(writEntries1) != 1 {
		t.Fatalf("expected 1 writ fetch refspec after first init, got %d: %v", len(writEntries1), writEntries1)
	}
	if writEntries1[0] != "refs/writ/*:refs/remotes/origin/writ/*" {
		t.Errorf("refspec = %q, want refs/writ/*:refs/remotes/origin/writ/*", writEntries1[0])
	}

	writerID1 := getGitConfigAll(t, env.repoDir, "writ.writerId")
	if len(writerID1) != 1 || len(writerID1[0]) != 16 {
		t.Fatalf("expected valid writerId, got %v", writerID1)
	}

	// Run init second time
	var stdout2, stderr2 bytes.Buffer
	code2 := run(context.Background(), []string{"init", "-C", env.repoDir}, &stdout2, &stderr2)
	if code2 != 0 {
		t.Fatalf("run init (second) exited with %d; stderr: %s", code2, stderr2.String())
	}

	fetchEntries2 := getGitConfigAll(t, env.repoDir, "remote.origin.fetch")
	var writEntries2 []string
	for _, e := range fetchEntries2 {
		if strings.Contains(e, "writ") {
			writEntries2 = append(writEntries2, e)
		}
	}
	if len(writEntries2) != 1 {
		t.Fatalf("expected 1 writ fetch refspec after second init, got %d: %v", len(writEntries2), writEntries2)
	}

	writerID2 := getGitConfigAll(t, env.repoDir, "writ.writerId")
	if len(writerID2) != 1 || writerID2[0] != writerID1[0] {
		t.Errorf("writer ID changed on second run: %v vs %v", writerID2, writerID1)
	}

	if !strings.Contains(stdout2.String(), "already configured") {
		t.Errorf("stdout2 does not mention 'already configured': %s", stdout2.String())
	}
}

// TestInit_ValidatesRepositoryBeforeWriting pins the ordering that keeps a
// failed init from leaving anything behind: opening the repository is a
// precondition, not a later step. This is a guard rather than a reproduction —
// the open now happens once, up front, and nothing may be written before it
// succeeds, so moving it back down past the writes has to fail here.
func TestInit_ValidatesRepositoryBeforeWriting(t *testing.T) {
	env := setupTestCLIEnv(t)
	addRemote(t, env.repoDir, "origin", "https://example.com/repo.git")

	// A repository git opens happily and writ cannot: writ reads git objects
	// directly and only understands sha1.
	setGitConfig(t, env.repoDir, "core.repositoryformatversion", "1")
	setGitConfig(t, env.repoDir, "extensions.objectFormat", "sha256")

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"init", "-C", env.repoDir}, &stdout, &stderr); code != 1 {
		t.Fatalf("init exited with %d, want 1; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unsupported repository format") {
		t.Errorf("stderr does not say why the repository was refused:\n%s", stderr.String())
	}

	// Nothing may have been written, and nothing may have been claimed.
	if got := getGitConfigAll(t, env.repoDir, "writ.writerId"); len(got) != 0 {
		t.Errorf("writ.writerId = %v, want nothing written by a run that could not open the repo", got)
	}
	if got := getGitConfigAll(t, env.repoDir, "writ.repoId"); len(got) != 0 {
		t.Errorf("writ.repoId = %v, want nothing written by a run that could not open the repo", got)
	}
	for _, entry := range getGitConfigAll(t, env.repoDir, "remote.origin.fetch") {
		if strings.Contains(entry, "writ") {
			t.Errorf("writ fetch refspec %q was written by a failed run", entry)
		}
	}
	if strings.Contains(stdout.String(), "minted") {
		t.Errorf("init reported minting an ID it never persisted:\n%s", stdout.String())
	}
}

// TestInit_RefusesARepositoryItCannotResolve is the WRIT-95 scenario itself:
// a first run that mints both IDs and then dies. gitdir.Resolve used to be
// called under an `err == nil` guard that dropped its error on the floor, so a
// repository writ could not resolve carried on through both config writes and
// failed at the very end, inside sync.Open's second Resolve of the same
// repository — identity persisted, no refspec, exit 1. Resolving once, up
// front, and treating the failure as fatal is what makes that unreachable.
//
// The injector is git's own worktree/gitdir split: GIT_DIR and GIT_WORK_TREE
// pointed at different directories give a work tree with no .git in it or
// above it. git rev-parse --show-toplevel answers happily, git config writes
// to GIT_DIR, and gitdir.Resolve — which walks the filesystem rather than
// asking git — finds nothing.
func TestInit_RefusesARepositoryItCannotResolve(t *testing.T) {
	requireGit(t)

	// Resolved, because git answers with the real path and macOS hands out
	// temp directories under a symlinked /var.
	tempDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temp dir: %v", err)
	}
	globalCfgPath := filepath.Join(tempDir, "global_gitconfig")
	if err := os.WriteFile(globalCfgPath, []byte(""), 0600); err != nil {
		t.Fatalf("writing empty global config: %v", err)
	}
	gitDir := filepath.Join(tempDir, "gitdir.git")
	workTree := filepath.Join(tempDir, "worktree")
	if err := os.Mkdir(workTree, 0755); err != nil {
		t.Fatalf("creating work tree: %v", err)
	}
	initCmd := exec.Command("git", "init", "--bare", gitDir)
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v (%s)", err, out)
	}

	t.Setenv("GIT_CONFIG_GLOBAL", globalCfgPath)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_DIR", gitDir)
	t.Setenv("GIT_WORK_TREE", workTree)

	// A remote, so that the old ordering reaches the second Resolve inside
	// sync.Open and fails there — which is the whole shape of the bug: the
	// run dies after both writes, at the step that should have been first.
	addRemote(t, workTree, "origin", "https://example.com/repo.git")

	// The premise: git itself is perfectly happy here, so init gets past the
	// rev-parse and reaches the open with a repoRoot writ cannot resolve.
	topLevel := exec.Command("git", "rev-parse", "--show-toplevel")
	topLevel.Dir = workTree
	if out, err := topLevel.Output(); err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	} else if got := strings.TrimSpace(string(out)); got != workTree {
		t.Fatalf("git reported toplevel %q, want %q — the test's premise does not hold", got, workTree)
	}

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"init", "-C", workTree}, &stdout, &stderr); code != 1 {
		t.Fatalf("init exited with %d, want 1; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not a git repository") {
		t.Errorf("stderr does not say why the repository was refused:\n%s", stderr.String())
	}

	// Nothing may have been written. Under the old ordering both of these
	// held a freshly minted ID by the time the run failed.
	if got := getGitConfigAll(t, workTree, "writ.writerId"); len(got) != 0 {
		t.Errorf("writ.writerId = %v, want nothing written by a run that could not resolve the repo", got)
	}
	if got := getGitConfigAll(t, workTree, "writ.repoId"); len(got) != 0 {
		t.Errorf("writ.repoId = %v, want nothing written by a run that could not resolve the repo", got)
	}
	for _, entry := range getGitConfigAll(t, workTree, "remote.origin.fetch") {
		if strings.Contains(entry, "writ") {
			t.Errorf("writ fetch refspec %q was written by a failed run", entry)
		}
	}
	if strings.Contains(stdout.String(), "minted") {
		t.Errorf("init reported minting an ID it never persisted:\n%s", stdout.String())
	}
}

// TestInit_PartialFailureIsReportedAndRecovers covers the failure that cannot
// be hoisted ahead of the writes: the refspec write itself. git config has no
// transaction, so the repository really is left half-configured — identity in
// config, refspec absent, which reads as clean and is not. The run has to say
// so, and re-running has to finish the job without minting a second writer-id
// for this device, which would split its ops across two ref namespaces.
func TestInit_PartialFailureIsReportedAndRecovers(t *testing.T) {
	env := setupTestCLIEnv(t)

	// Mint the IDs on a run with no remote to configure, so the failure below
	// lands where the ticket found it: after identity is in config.
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr); code != 0 {
		t.Fatalf("first init exited with %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "(minted)") {
		t.Fatalf("first init did not mint the IDs:\n%s", stdout.String())
	}
	writerID := getGitConfigAll(t, env.repoDir, "writ.writerId")
	repoID := getGitConfigAll(t, env.repoDir, "writ.repoId")
	if len(writerID) != 1 || len(repoID) != 1 {
		t.Fatalf("expected one writerId and one repoId, got %v and %v", writerID, repoID)
	}

	addRemote(t, env.repoDir, "origin", "https://example.com/repo.git")

	// Hold git's config lock so the refspec write — and only the refspec
	// write — fails. Reads do not take the lock, so everything ahead of it
	// still succeeds, which is the shape of the failure being reported.
	lockPath := filepath.Join(env.repoDir, ".git", "config.lock")
	if err := os.WriteFile(lockPath, nil, 0600); err != nil {
		t.Fatalf("holding the config lock: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"init", "-C", env.repoDir}, &stdout, &stderr); code != 1 {
		t.Fatalf("init exited with %d, want 1, while the config was locked; stderr: %s", code, stderr.String())
	}

	failed := stderr.String()
	for _, want := range []string{
		"half-configured",
		"writ.writerId " + writerID[0],
		"writ.repoId " + repoID[0],
		"NOT configured for: origin",
		"re-run writ init",
	} {
		if !strings.Contains(failed, want) {
			t.Errorf("failure report does not say %q:\n%s", want, failed)
		}
	}
	// The IDs were read back, not minted again.
	if strings.Contains(stdout.String(), "(minted)") {
		t.Errorf("a re-run said (minted) for IDs it read out of config:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "already configured") {
		t.Errorf("a re-run did not report the IDs as already configured:\n%s", stdout.String())
	}
	for _, entry := range getGitConfigAll(t, env.repoDir, "remote.origin.fetch") {
		if strings.Contains(entry, "writ") {
			t.Errorf("refspec %q exists despite the write failing", entry)
		}
	}

	// Re-running once the error is fixed finishes the job, exactly as the
	// report promised.
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("releasing the config lock: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"init", "-C", env.repoDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("re-run exited with %d; stderr: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "(minted)") {
		t.Errorf("the recovery run minted an ID:\n%s", stdout.String())
	}
	if got := getGitConfigAll(t, env.repoDir, "writ.writerId"); len(got) != 1 || got[0] != writerID[0] {
		t.Errorf("writ.writerId = %v, want the original %v: a second writer-id splits this device across two ref namespaces", got, writerID)
	}
	if got := getGitConfigAll(t, env.repoDir, "writ.repoId"); len(got) != 1 || got[0] != repoID[0] {
		t.Errorf("writ.repoId = %v, want the original %v", got, repoID)
	}
	var writEntries []string
	for _, entry := range getGitConfigAll(t, env.repoDir, "remote.origin.fetch") {
		if strings.Contains(entry, "writ") {
			writEntries = append(writEntries, entry)
		}
	}
	if len(writEntries) != 1 || writEntries[0] != "refs/writ/*:refs/remotes/origin/writ/*" {
		t.Errorf("writ refspecs = %v, want exactly the canonical one", writEntries)
	}
}

// TestInit_AlreadyInitialisedIsACleanNoOp pins the other end of the same
// promise: on a repository that is already set up, init changes nothing and
// warns about nothing.
func TestInit_AlreadyInitialisedIsACleanNoOp(t *testing.T) {
	env := setupTestCLIEnv(t)
	setupSigningKey(t, env.repoDir)
	addRemote(t, env.repoDir, "origin", "https://example.com/repo.git")

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr); code != 0 {
		t.Fatalf("first init exited with %d; stderr: %s", code, stderr.String())
	}
	before := gitConfigSnapshot(t, env.repoDir)

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"init", "-C", env.repoDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("second init exited with %d; stderr: %s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("second init on a configured repo warned about something:\n%s", stderr.String())
	}
	if strings.Contains(stdout.String(), "minted") {
		t.Errorf("second init claimed to mint something:\n%s", stdout.String())
	}
	if after := gitConfigSnapshot(t, env.repoDir); after != before {
		t.Errorf("second init changed git config:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// gitConfigSnapshot returns the repository's local git config verbatim, for
// comparing a repository against itself across a command.
func gitConfigSnapshot(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "config", "--local", "--list")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --local --list in %s: %v", dir, err)
	}
	return string(out)
}

func TestInit_DriftRepair(t *testing.T) {
	env := setupTestCLIEnv(t)
	addRemote(t, env.repoDir, "origin", "https://example.com/repo.git")

	// Pre-seed forced writ refspec alongside head and custom refspecs
	setGitConfig(t, env.repoDir, "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	cmd := exec.Command("git", "config", "--add", "remote.origin.fetch", "+refs/writ/*:refs/remotes/origin/writ/*")
	cmd.Dir = env.repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("seed forced refspec: %v", err)
	}
	cmd = exec.Command("git", "config", "--add", "remote.origin.fetch", "refs/custom/*:refs/remotes/origin/custom/*")
	cmd.Dir = env.repoDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("seed custom refspec: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run init exited with %d; stderr: %s", code, stderr.String())
	}

	fetchEntries := getGitConfigAll(t, env.repoDir, "remote.origin.fetch")
	expectedEntries := []string{
		"+refs/heads/*:refs/remotes/origin/*",
		"refs/custom/*:refs/remotes/origin/custom/*",
		"refs/writ/*:refs/remotes/origin/writ/*",
	}

	if len(fetchEntries) != len(expectedEntries) {
		t.Fatalf("fetch entries = %v, want %v", fetchEntries, expectedEntries)
	}
	for i, exp := range expectedEntries {
		if fetchEntries[i] != exp {
			t.Errorf("fetchEntries[%d] = %q, want %q", i, fetchEntries[i], exp)
		}
	}
}

func TestInit_WriterIDPrecedence(t *testing.T) {
	t.Run("local_preset", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		setGitConfig(t, env.repoDir, "writ.writerId", "1111111111111111")

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
		}
		got := getGitConfigAll(t, env.repoDir, "writ.writerId")
		if len(got) != 1 || got[0] != "1111111111111111" {
			t.Errorf("writerId = %v, want [\"1111111111111111\"]", got)
		}
		if !strings.Contains(stdout.String(), "1111111111111111") {
			t.Errorf("stdout does not contain local writer ID: %s", stdout.String())
		}
	})

	t.Run("unset_mints", func(t *testing.T) {
		env := setupTestCLIEnv(t)

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
		}
		got := getGitConfigAll(t, env.repoDir, "writ.writerId")
		re := regexp.MustCompile(`^[0-9a-f]{16}$`)
		if len(got) != 1 || !re.MatchString(got[0]) {
			t.Errorf("minted writerId = %v, want valid 16 hex chars", got)
		}
		if !strings.Contains(stdout.String(), "minted") {
			t.Errorf("stdout does not indicate minted: %s", stdout.String())
		}
	})

	t.Run("global_only", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		setFileConfig(t, env.globalCfgPath, "writ.writerId", "2222222222222222")

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
		}

		// Verify global ID was used
		if !strings.Contains(stdout.String(), "2222222222222222") {
			t.Errorf("stdout does not contain global writer ID: %s", stdout.String())
		}

		// Verify not written to local config
		cmd := exec.Command("git", "config", "--local", "--get", "writ.writerId")
		cmd.Dir = env.repoDir
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("git config --local writ.writerId should not be set, but got %q", string(out))
		}
	})
}

func TestInit_RepoIDPrecedence(t *testing.T) {
	t.Run("local_preset", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		setGitConfig(t, env.repoDir, "writ.repoId", "a1b2c3d4e5f60718293a4b5c6d7e8f90")

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
		}
		got := getGitConfigAll(t, env.repoDir, "writ.repoId")
		if len(got) != 1 || got[0] != "a1b2c3d4e5f60718293a4b5c6d7e8f90" {
			t.Errorf("repoId = %v, want [\"a1b2c3d4e5f60718293a4b5c6d7e8f90\"]", got)
		}
		if !strings.Contains(stdout.String(), "a1b2c3d4e5f60718293a4b5c6d7e8f90") {
			t.Errorf("stdout does not contain local repo ID: %s", stdout.String())
		}
		if !strings.Contains(stdout.String(), "already configured") {
			t.Errorf("stdout does not indicate already configured: %s", stdout.String())
		}
	})

	t.Run("unset_mints", func(t *testing.T) {
		env := setupTestCLIEnv(t)

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
		}
		got := getGitConfigAll(t, env.repoDir, "writ.repoId")
		re := regexp.MustCompile(`^[0-9a-f]{32}$`)
		if len(got) != 1 || !re.MatchString(got[0]) {
			t.Errorf("minted repoId = %v, want valid 32 hex chars", got)
		}
		if !strings.Contains(stdout.String(), "Repo ID:") || !strings.Contains(stdout.String(), "(minted)") {
			t.Errorf("stdout does not indicate Repo ID minted: %s", stdout.String())
		}
	})
}

func TestInit_SigningKeyGuidance(t *testing.T) {
	env := setupTestCLIEnv(t)
	// The author identity has to be configured for Load to get as far as the
	// signing keys: without it this test asserted the signing remediation
	// against a repository whose actual complaint was user.name.
	setGitConfig(t, env.repoDir, "user.name", "Alice")
	setGitConfig(t, env.repoDir, "user.email", "alice@example.com")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init exited with %d (want 0 for missing signing key); stderr: %s", code, stderr.String())
	}

	errStr := stderr.String()
	if !strings.Contains(errStr, "git config gpg.format ssh") {
		t.Errorf("stderr does not advise 'git config gpg.format ssh': %s", errStr)
	}
	if !strings.Contains(errStr, "git config user.signingKey") {
		t.Errorf("stderr does not advise 'git config user.signingKey': %s", errStr)
	}
	if !strings.Contains(errStr, "git config gpg.ssh.allowedSignersFile") {
		t.Errorf("stderr does not mention allowedSignersFile: %s", errStr)
	}
}

// TestInit_GPGFormatSpellingAgreesWithTheWritePath pins the two halves of the
// gpg.format check against each other. identity.Load compares the value
// case-insensitively, the way git does; engine/open.go compared the loaded
// field against "ssh" exactly. A repository configured gpg.format = SSH
// therefore passed init — which reported a signing key and exited 0 — and then
// had no signer at the first write. Reporting clean and failing later is the
// exact complaint WRIT-94 is about, so the spellings init accepts and the
// spellings a write accepts have to be the same set.
func TestInit_GPGFormatSpellingAgreesWithTheWritePath(t *testing.T) {
	for _, format := range []string{"ssh", "SSH", "Ssh"} {
		t.Run(format, func(t *testing.T) {
			env := setupTestCLIEnv(t)
			setupSigningKey(t, env.repoDir)
			setGitConfig(t, env.repoDir, "gpg.format", format)

			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr); code != 0 {
				t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "Signing key:") {
				t.Fatalf("init did not report a signing key for gpg.format = %q:\n%s\n%s", format, stdout.String(), stderr.String())
			}

			commitFile(t, env.repoDir, "README.md", "# Hello", "initial commit")

			stdout.Reset()
			stderr.Reset()
			if code := run(context.Background(), []string{
				"schema", "apply", "-C", env.repoDir,
			}, &stdout, &stderr); code != 0 {
				t.Fatalf("a signed write refused a repository init reported as configured (gpg.format = %q):\n%s", format, stderr.String())
			}
		})
	}
}

// TestInit_AuthorIdentityGuidance pins the other half of that split. user.name
// and user.email are matched by the same "user." prefix as user.signingKey, so
// they were routed into the SSH signing remediation: a repository whose only
// problem was a blank address was told its signing key was misconfigured and
// shown three git config lines, not one of which was user.email.
func TestInit_AuthorIdentityGuidance(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{name: "user.name", key: "user.name"},
		{name: "user.email", key: "user.email"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestCLIEnv(t)
			setGitConfig(t, env.repoDir, "user.name", "Alice")
			setGitConfig(t, env.repoDir, "user.email", "alice@example.com")
			setGitConfig(t, env.repoDir, "writ.personId", "user:alice")
			setupSigningKey(t, env.repoDir)
			// Whitespace, not unset: since WRIT-131 these are the same state,
			// and this is the one the ticket was found in.
			setGitConfig(t, env.repoDir, tc.key, "   ")

			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr); code != 0 {
				t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
			}

			got := stderr.String()
			if !strings.Contains(got, tc.key) {
				t.Errorf("init did not name the unconfigured key %q:\n%s", tc.key, got)
			}
			if !strings.Contains(got, "git config user.name") || !strings.Contains(got, "git config user.email") {
				t.Errorf("init did not print the remediation for the key it named:\n%s", got)
			}
			if strings.Contains(got, "git config gpg.format ssh") || strings.Contains(got, "git config user.signingKey") {
				t.Errorf("init answered a missing %s with the SSH signing remediation:\n%s", tc.key, got)
			}
		})
	}
}

// TestInit_NeverAdvisesRunningInit pins the one piece of advice writ init must
// never print: itself. The remediation is carried by identity.ConfigError and
// is right from every other command; printed by init it tells the reader to
// run what they are running, and implies init failed at something it never
// attempts. The git config lines init prints below each warning are the actual
// remediation, so the assertions here also check those survived.
func TestInit_NeverAdvisesRunningInit(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, repoDir string)
	}{
		{
			name:  "nothing configured at all",
			setup: func(t *testing.T, repoDir string) {},
		},
		{
			name: "gpg.format set to something writ cannot use",
			setup: func(t *testing.T, repoDir string) {
				setGitConfig(t, repoDir, "user.name", "Alice")
				setGitConfig(t, repoDir, "user.email", "alice@example.com")
				setGitConfig(t, repoDir, "gpg.format", "openpgp")
			},
		},
		{
			name: "no person identifier to derive",
			setup: func(t *testing.T, repoDir string) {
				setGitConfig(t, repoDir, "user.email", "   ")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestCLIEnv(t)
			tc.setup(t, env.repoDir)

			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr); code != 0 {
				t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
			}

			out := stdout.String() + stderr.String()
			if strings.Contains(out, "writ init") {
				t.Errorf("writ init advised the reader to run writ init:\n%s", out)
			}
			if !strings.Contains(out, "git config") {
				t.Errorf("init dropped the git config remediation entirely:\n%s", out)
			}
		})
	}

	// The other half of the rule: suppressed for init, kept everywhere else.
	// A verb that needs a signed write on an unconfigured repo is exactly
	// where "run 'writ init' to configure" is the right thing to say.
	t.Run("but another verb still says it", func(t *testing.T) {
		env := setupTestCLIEnv(t)

		// The contract init suppresses is ConfigError's own: the same
		// repository state, read through the engine, still carries the
		// remediation. Asserted directly, because no CLI verb renders a
		// ConfigError today — engine/open.go flattens the ones Load returns
		// into ErrNoIdentity — so the schema apply check below passes through
		// a hardcoded string and would survive Error() dropping the hint.
		_, loadErr := identity.Load(context.Background(), env.repoDir)
		if loadErr == nil {
			t.Fatal("identity.Load succeeded on an unconfigured repo")
		}
		var cfgErr *identity.ConfigError
		if !errors.As(loadErr, &cfgErr) {
			t.Fatalf("identity.Load error is %T, want *identity.ConfigError", loadErr)
		}
		if !strings.Contains(cfgErr.Error(), "run 'writ init' to configure") {
			t.Errorf("ConfigError.Error dropped the remediation init suppresses: %q", cfgErr.Error())
		}
		if strings.Contains(cfgErr.Message(), "writ init") {
			t.Errorf("ConfigError.Message kept the advice init must not print: %q", cfgErr.Message())
		}

		// A write needs a writ.schema to plan from before it ever reaches
		// the identity check; write the same namespace-only starter `writ
		// init` itself would, without running init.
		writeSchemaFile(t, env.repoDir, "namespace testns\n")

		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"schema", "apply", "-C", env.repoDir}, &stdout, &stderr); code == 0 {
			t.Fatalf("schema apply should refuse on an unconfigured repo; stdout: %s", stdout.String())
		}
		if got := stderr.String(); !strings.Contains(got, "run 'writ init' to configure") {
			t.Errorf("schema apply dropped the remediation:\n%s", got)
		}
	})
}

// TestInit_UnconfiguredSigningReadsAsUnset is the message half of the same
// report: a repo with no signing configuration was told its gpg.format was an
// unsupported format, which reads as writ having found something broken rather
// than something absent.
func TestInit_UnconfiguredSigningReadsAsUnset(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		setGitConfig(t, env.repoDir, "user.name", "Alice")
		setGitConfig(t, env.repoDir, "user.email", "alice@example.com")

		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr); code != 0 {
			t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
		}
		got := stderr.String()
		if !strings.Contains(got, "missing git config \"gpg.format\"") {
			t.Errorf("init should report gpg.format as missing, got:\n%s", got)
		}
		if strings.Contains(got, "unsupported") {
			t.Errorf("init called an unconfigured gpg.format unsupported:\n%s", got)
		}
	})

	t.Run("set to openpgp", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		setGitConfig(t, env.repoDir, "user.name", "Alice")
		setGitConfig(t, env.repoDir, "user.email", "alice@example.com")
		setGitConfig(t, env.repoDir, "gpg.format", "openpgp")

		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr); code != 0 {
			t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
		}
		got := stderr.String()
		if !strings.Contains(got, "unsupported git config \"gpg.format\"=\"openpgp\"") {
			t.Errorf("init should quote the configured format back, got:\n%s", got)
		}
		if strings.Contains(got, "missing") {
			t.Errorf("init called a configured gpg.format missing:\n%s", got)
		}
	})
}

func TestInit_E2E_PlainGitFetch(t *testing.T) {
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
	bareDir := filepath.Join(tempDir, "bare.git")
	_, err := git.PlainInit(bareDir, true)
	if err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}

	// 2. Clone A
	cloneADir := filepath.Join(tempDir, "cloneA")
	cmd := exec.Command("git", "clone", bareDir, cloneADir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone A failed: %v (%s)", err, string(out))
	}
	const writerIDA = "aaaaaaaaaaaaaaaa"
	setGitConfig(t, cloneADir, "writ.writerId", writerIDA)
	setGitConfig(t, cloneADir, "user.name", "Alice")
	setGitConfig(t, cloneADir, "user.email", "alice@example.com")
	setGitConfig(t, cloneADir, "gpg.format", "ssh")
	setGitConfig(t, cloneADir, "user.signingKey", "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyBlob")

	identA := identity.Identity{
		WriterID: identity.WriterID(writerIDA),
		Author:   identity.Author{Name: "Alice", Email: "alice@example.com"},
		Key:      identity.SigningKey{Format: "ssh", Value: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyBlob", Literal: true},
	}

	// This exercises plain-git-fetch/ref mechanics only — a raw dag.Append
	// beneath any schema layer. Writ hard-codes no object type but
	// "schema", so the producer vocabulary the append validates against is
	// supplied here directly, in place of the log walk a full store wires
	// up: one neutral consumer type, declared with the one field the body
	// carries.
	vocabularies := codec.Vocabularies{
		"widget": {
			Declared:       true,
			SchemaObjectID: "0123456789abcdef",
			OpTypes:        map[codec.OpVersionKey]bool{{OpType: "create", OpVersion: 1}: true},
			Fields: map[codec.OpVersionKey][]spec.FieldRule{
				{OpType: "create", OpVersion: 1}: {{
					OpType:    "create",
					OpVersion: 1,
					Field:     "title",
					Strategy:  "lww",
					ValueType: "string",
				}},
			},
		},
	}
	storeA, err := dag.Open(cloneADir, identA,
		dag.WithNow(func() time.Time {
			return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
		}),
		dag.WithProducerVocabularies(func() (codec.Vocabularies, error) {
			return vocabularies, nil
		}))
	if err != nil {
		t.Fatalf("dag.Open clone A: %v", err)
	}

	bodyBytes, _ := json.Marshal(map[string]any{"title": "Initial"})
	envOp := codec.Envelope{
		ObjectID:   "wid-1234567890abcdef",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       bodyBytes,
	}
	opA, err := storeA.Append(context.Background(), envOp, nil)
	if err != nil {
		t.Fatalf("storeA.Append: %v", err)
	}

	syncA, err := sync.Open(cloneADir, identA)
	if err != nil {
		t.Fatalf("sync.Open clone A: %v", err)
	}
	pushResult, err := syncA.Push(context.Background(), "origin")
	if err != nil {
		t.Fatalf("syncA.Push: %v", err)
	}
	if len(pushResult.PushedRefs) == 0 {
		t.Fatalf("syncA.Push did not push any refs")
	}

	// 3. Clone B
	cloneBDir := filepath.Join(tempDir, "cloneB")
	cmd = exec.Command("git", "clone", bareDir, cloneBDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone B failed: %v (%s)", err, string(out))
	}
	setGitConfig(t, cloneBDir, "user.name", "Bob")
	setGitConfig(t, cloneBDir, "user.email", "bob@example.com")

	// 4. In Clone B, run writ init
	var stdoutB, stderrB bytes.Buffer
	codeB := run(context.Background(), []string{"init", "-C", cloneBDir, "--namespace", "testns"}, &stdoutB, &stderrB)
	if codeB != 0 {
		t.Fatalf("writ init in clone B exited with %d; stderr: %s", codeB, stderrB.String())
	}

	// 5. In Clone B, run plain git fetch origin
	cmdFetch := exec.Command("git", "fetch", "origin")
	cmdFetch.Dir = cloneBDir
	if out, err := cmdFetch.CombinedOutput(); err != nil {
		t.Fatalf("plain git fetch origin in clone B failed: %v (%s)", err, string(out))
	}

	// 6. Assert remote tracking ref exists in Clone B and points to opA.ID
	repoB, err := git.PlainOpen(cloneBDir)
	if err != nil {
		t.Fatalf("open clone B repo: %v", err)
	}
	remoteRefName := plumbing.ReferenceName("refs/remotes/origin/writ/" + writerIDA + "/widget")
	refB, err := repoB.Reference(remoteRefName, true)
	if err != nil {
		t.Fatalf("Clone B missing remote tracking ref %s: %v", remoteRefName, err)
	}
	if refB.Hash().String() != opA.ID {
		t.Errorf("Clone B ref %s hash = %s, want %s", remoteRefName, refB.Hash(), opA.ID)
	}

	// 7. Assert Clone B's own refs/writ/* namespace is completely untouched
	iterB, err := repoB.References()
	if err != nil {
		t.Fatalf("iter references clone B: %v", err)
	}
	defer iterB.Close()
	_ = iterB.ForEach(func(ref *plumbing.Reference) error {
		if strings.HasPrefix(ref.Name().String(), "refs/writ/") {
			t.Errorf("Clone B has unexpected local writ ref: %s", ref.Name())
		}
		return nil
	})
}

func TestInit_MultiRemoteAndPositional(t *testing.T) {
	t.Run("all_remotes_by_default", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		addRemote(t, env.repoDir, "origin", "https://example.com/origin.git")
		addRemote(t, env.repoDir, "upstream", "https://example.com/upstream.git")

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
		}

		originFetch := getGitConfigAll(t, env.repoDir, "remote.origin.fetch")
		upstreamFetch := getGitConfigAll(t, env.repoDir, "remote.upstream.fetch")

		if len(originFetch) == 0 || !strings.Contains(originFetch[len(originFetch)-1], "refs/writ/*:refs/remotes/origin/writ/*") {
			t.Errorf("origin fetch refspec missing: %v", originFetch)
		}
		if len(upstreamFetch) == 0 || !strings.Contains(upstreamFetch[len(upstreamFetch)-1], "refs/writ/*:refs/remotes/upstream/writ/*") {
			t.Errorf("upstream fetch refspec missing: %v", upstreamFetch)
		}
	})

	t.Run("positional_narrowing", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		addRemote(t, env.repoDir, "origin", "https://example.com/origin.git")
		addRemote(t, env.repoDir, "upstream", "https://example.com/upstream.git")

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns", "origin"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
		}

		originFetch := getGitConfigAll(t, env.repoDir, "remote.origin.fetch")
		upstreamFetch := getGitConfigAll(t, env.repoDir, "remote.upstream.fetch")

		if len(originFetch) == 0 || !strings.Contains(originFetch[len(originFetch)-1], "refs/writ/*:refs/remotes/origin/writ/*") {
			t.Errorf("origin fetch refspec missing: %v", originFetch)
		}
		for _, e := range upstreamFetch {
			if strings.Contains(e, "writ") {
				t.Errorf("upstream unexpectedly configured when narrowing to origin: %v", upstreamFetch)
			}
		}
	})
}

func TestInit_NoRemotes(t *testing.T) {
	env := setupTestCLIEnv(t)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init with no remotes exited with %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No git remotes configured") {
		t.Errorf("stdout does not note missing remotes: %s", stdout.String())
	}
	writerID := getGitConfigAll(t, env.repoDir, "writ.writerId")
	if len(writerID) != 1 || len(writerID[0]) != 16 {
		t.Errorf("writerId not minted: %v", writerID)
	}
}

func TestInit_BareRepository(t *testing.T) {
	requireGit(t)
	tempDir := t.TempDir()
	bareDir := filepath.Join(tempDir, "bare.git")
	_, err := git.PlainInit(bareDir, true)
	if err != nil {
		t.Fatalf("PlainInit bare: %v", err)
	}

	globalCfgPath := filepath.Join(tempDir, "global_gitconfig")
	if err := os.WriteFile(globalCfgPath, []byte(""), 0600); err != nil {
		t.Fatalf("writing empty global config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfgPath)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	// No --namespace: a bare repository writes no starter file, so it needs
	// no namespace and must keep succeeding non-interactively with none
	// supplied (dispatch decision on WRIT-220's plan).
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", bareDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init on bare repo failed with %d; stderr: %s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "--namespace") {
		t.Errorf("bare repo init asked for a namespace it will never use:\n%s", stderr.String())
	}

	writerID := getGitConfigAll(t, bareDir, "writ.writerId")
	if len(writerID) != 1 || len(writerID[0]) != 16 {
		t.Errorf("bare repo writerId not minted: %v", writerID)
	}

	// A bare repository has no working tree to put writ.schema in
	// (WRIT-191 correction 4): the starter file is a working-tree source
	// form and gates on one existing.
	if _, err := os.Stat(filepath.Join(bareDir, "writ.schema")); !os.IsNotExist(err) {
		t.Errorf("expected no writ.schema in a bare repository, stat err = %v", err)
	}
}

// TestInit_WritesStarterSchemaFile pins WRIT-191's `writ init` starter file:
// written once, for a work tree, never overwritten, and round-trips its own
// tooling (parses, formats identically, compiles to exactly one create op).
func TestInit_WritesStarterSchemaFile(t *testing.T) {
	env := setupTestCLIEnv(t)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run init exited with %d; stderr: %s", code, stderr.String())
	}

	path := filepath.Join(env.repoDir, "writ.schema")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading starter writ.schema: %v", err)
	}

	// The namespace is exactly the one supplied on the command line: writ
	// init derives nothing (WRIT-220).
	if want := "namespace testns\n"; string(content) != want {
		t.Errorf("starter writ.schema = %q, want %q", string(content), want)
	}
	if !strings.Contains(stdout.String(), "Wrote starter") {
		t.Errorf("expected stdout to report the starter file was written, got: %s", stdout.String())
	}

	f, err := schemasrc.Parse("writ.schema", content)
	if err != nil {
		t.Fatalf("schemasrc.Parse(starter file) failed: %v", err)
	}
	formatted, err := schemasrc.Format("writ.schema", content)
	if err != nil {
		t.Fatalf("schemasrc.Format(starter file) failed: %v", err)
	}
	if !bytes.Equal(formatted, content) {
		t.Errorf("schemasrc.Format is not byte-identical to the starter file:\nformatted: %q\noriginal:  %q", formatted, content)
	}
	envs, err := schemasrc.Compile(f, "sch-test")
	if err != nil {
		t.Fatalf("schemasrc.Compile(starter file) failed: %v", err)
	}
	if len(envs) != 1 || envs[0].OpType != "create" {
		t.Fatalf("expected exactly one create op from the starter file, got %d ops: %+v", len(envs), envs)
	}

	// Running init again must never overwrite an existing writ.schema, no
	// matter its content.
	customContent := []byte("namespace mycustom\n")
	if err := os.WriteFile(path, customContent, 0o644); err != nil {
		t.Fatalf("writing custom writ.schema: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"init", "-C", env.repoDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("second run init exited with %d; stderr: %s", code, stderr.String())
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading writ.schema after second init: %v", err)
	}
	if !bytes.Equal(after, customContent) {
		t.Errorf("writ init overwrote an existing writ.schema: got %q, want unchanged %q", after, customContent)
	}
	if !strings.Contains(stdout.String(), "already exists") {
		t.Errorf("expected stdout to report the existing file was left alone, got: %s", stdout.String())
	}
}

func TestInit_NonRepo(t *testing.T) {
	requireGit(t)
	nonRepoDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", nonRepoDir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("init on non-repo exited with %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "not a git repository") {
		t.Errorf("stderr does not mention not a git repo: %s", stderr.String())
	}
}

func TestRoot_Dispatch(t *testing.T) {
	t.Run("no_args", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), nil, &stdout, &stderr)
		if code != 2 {
			t.Errorf("no args exit code = %d, want 2", code)
		}
		if !strings.Contains(stderr.String(), "Usage:") {
			t.Errorf("stderr does not contain usage: %s", stderr.String())
		}
	})

	t.Run("unknown_command", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"foobar"}, &stdout, &stderr)
		if code != 2 {
			t.Errorf("unknown command exit code = %d, want 2", code)
		}
		if !strings.Contains(stderr.String(), "unknown command") {
			t.Errorf("stderr does not mention unknown command: %s", stderr.String())
		}
	})

	t.Run("help_flags", func(t *testing.T) {
		flags := []string{"-h", "-help", "--help", "help"}
		for _, f := range flags {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{f}, &stdout, &stderr)
			if code != 0 {
				t.Errorf("%s exit code = %d, want 0", f, code)
			}
			if !strings.Contains(stdout.String(), "Usage:") {
				t.Errorf("%s stdout does not contain usage: %s", f, stdout.String())
			}
		}
	})

	t.Run("init_help", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"init", "-h"}, &stdout, &stderr)
		if code != 0 {
			t.Errorf("init -h exit code = %d, want 0", code)
		}
		if !strings.Contains(stderr.String(), "Usage: writ init") {
			t.Errorf("init -h output does not contain usage: %s", stderr.String())
		}
	})

	t.Run("root_C_flag", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		addRemote(t, env.repoDir, "origin", "https://example.com/repo.git")

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-C", env.repoDir, "init", "--namespace", "testns"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("root -C flag exit code = %d, want 0; stderr: %s", code, stderr.String())
		}
	})

	t.Run("root_C_flag_with_help", func(t *testing.T) {
		env := setupTestCLIEnv(t)

		for _, flag := range []string{"-h", "--help", "help"} {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"-C", env.repoDir, flag}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("root -C flag with %s exit code = %d, want 0; stderr: %s", flag, code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "Usage:") {
				t.Errorf("stdout does not contain usage for %s: %s", flag, stdout.String())
			}
		}
	})

	t.Run("root_C_flag_missing_arg", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"-C"}, &stdout, &stderr)
		if code != 2 {
			t.Errorf("root -C without arg exit code = %d, want 2", code)
		}
	})
}

func TestInit_WorktreeConfigAndExtensions(t *testing.T) {
	t.Run("worktreeConfig_format0", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		addRemote(t, env.repoDir, "origin", "https://example.com/repo.git")

		// Set extensions.worktreeConfig and repositoryformatversion 0
		setGitConfig(t, env.repoDir, "core.repositoryformatversion", "0")
		setGitConfig(t, env.repoDir, "extensions.worktreeConfig", "true")

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("writ init failed on repo with extensions.worktreeConfig: exit code %d; stderr: %s", code, stderr.String())
		}
	})

	t.Run("sparse_checkout", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		addRemote(t, env.repoDir, "origin", "https://example.com/repo.git")

		cmd := exec.Command("git", "sparse-checkout", "init")
		cmd.Dir = env.repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git sparse-checkout init failed: %v (%s)", err, string(out))
		}

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("writ init failed on sparse-checkout repo: exit code %d; stderr: %s", code, stderr.String())
		}
	})
}

// TestInit_PersonID covers the three states of the person identifier writ init
// reports: derived from user.email, overridden by writ.personId, and not
// derivable at all. The last one is a warning rather than a failure — init is
// still worth completing — but it has to say which key to set, because there is
// no fallback: a writer-id has no scheme and is not a person identifier.
func TestInit_PersonID(t *testing.T) {
	t.Run("derived from user.email", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		setGitConfig(t, env.repoDir, "user.email", "Alice@Example.COM")

		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr); code != 0 {
			t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
		}
		if want := "Person ID: email:alice@example.com (derived from user.email)"; !strings.Contains(stdout.String(), want) {
			t.Errorf("init output missing %q:\n%s", want, stdout.String())
		}
	})

	t.Run("writ.personId override", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		setGitConfig(t, env.repoDir, "writ.personId", "  User:Alice  ")

		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr); code != 0 {
			t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
		}
		if want := "Person ID: user:alice (from writ.personId)"; !strings.Contains(stdout.String(), want) {
			t.Errorf("init output missing %q:\n%s", want, stdout.String())
		}
	})

	t.Run("nothing to derive from", func(t *testing.T) {
		env := setupTestCLIEnv(t)
		setGitConfig(t, env.repoDir, "user.email", "   ")

		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr); code != 0 {
			t.Fatalf("init exited with %d; stderr: %s", code, stderr.String())
		}
		if strings.Contains(stdout.String(), "Person ID:") {
			t.Errorf("init reported a person ID it could not derive:\n%s", stdout.String())
		}
		if !strings.Contains(stderr.String(), "writ.personId") {
			t.Errorf("init should say which key to set, got stderr:\n%s", stderr.String())
		}
		// The ErrMissing arm of ConfigError.Error must carry the wrapped
		// guidance through rather than short-circuiting on the sentinel. init
		// derives from git config directly, so it is the one command that
		// still reaches this arm now that identity.Load rejects a
		// whitespace-only user.email outright (WRIT-131).
		if !strings.Contains(stderr.String(), "user:alice") {
			t.Errorf("init should carry the wrapped example through, got stderr:\n%s", stderr.String())
		}
	})
}

// TestInit_NamespaceRequiredNonInteractive pins WRIT-220's core refusal: a
// work tree with no writ.schema yet, run non-interactively with no
// --namespace, refuses outright rather than deriving one — and refuses
// before anything is written, leaving the repository exactly as it found
// it (the ordering step 2.5 exists for).
func TestInit_NamespaceRequiredNonInteractive(t *testing.T) {
	env := setupTestCLIEnv(t)
	addRemote(t, env.repoDir, "origin", "https://example.com/repo.git")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("init with no namespace exited with %d, want 1; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--namespace") {
		t.Errorf("stderr does not name --namespace:\n%s", stderr.String())
	}

	if got := getGitConfigAll(t, env.repoDir, "writ.writerId"); len(got) != 0 {
		t.Errorf("writ.writerId = %v, want nothing written by a run refused for lack of a namespace", got)
	}
	if got := getGitConfigAll(t, env.repoDir, "writ.repoId"); len(got) != 0 {
		t.Errorf("writ.repoId = %v, want nothing written by a run refused for lack of a namespace", got)
	}
	for _, entry := range getGitConfigAll(t, env.repoDir, "remote.origin.fetch") {
		if strings.Contains(entry, "writ") {
			t.Errorf("writ fetch refspec %q was written by a run refused for lack of a namespace", entry)
		}
	}
	if _, err := os.Stat(filepath.Join(env.repoDir, "writ.schema")); !os.IsNotExist(err) {
		t.Errorf("expected no writ.schema written by a refused run, stat err = %v", err)
	}
}

// TestInit_NamespaceInteractivePrompt covers the middle path: no
// --namespace, but stdin is a terminal. The prompt is shown once on
// stderr with no suggested default, and the answer is used verbatim.
func TestInit_NamespaceInteractivePrompt(t *testing.T) {
	env := setupTestCLIEnv(t)

	var stdout, stderr bytes.Buffer
	code := runStdin(context.Background(), []string{"init", "-C", env.repoDir}, strings.NewReader("acme\n"), true, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("interactive init exited with %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Namespace") {
		t.Errorf("interactive init did not show a prompt:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "acme") {
		t.Errorf("prompt echoed a suggested namespace; WRIT-220 requires no default:\n%s", stderr.String())
	}

	content, err := os.ReadFile(filepath.Join(env.repoDir, "writ.schema"))
	if err != nil {
		t.Fatalf("reading starter writ.schema: %v", err)
	}
	if want := "namespace acme\n"; string(content) != want {
		t.Errorf("starter writ.schema = %q, want %q", string(content), want)
	}
}

// TestInit_NamespaceInteractiveRejections covers the three ways an
// interactive prompt can fail to produce a namespace: an empty line, EOF
// with nothing typed, and an answer that fails the grammar. None of them
// retry — one bad answer is a refusal, exactly like a bad --namespace.
func TestInit_NamespaceInteractiveRejections(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"empty_answer", "\n"},
		{"eof_nothing_typed", ""},
		{"invalid_answer", "Not-Legal\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestCLIEnv(t)

			var stdout, stderr bytes.Buffer
			code := runStdin(context.Background(), []string{"init", "-C", env.repoDir}, strings.NewReader(tc.input), true, &stdout, &stderr)
			if code != 1 {
				t.Fatalf("init exited with %d, want 1; stderr: %s", code, stderr.String())
			}
			if _, err := os.Stat(filepath.Join(env.repoDir, "writ.schema")); !os.IsNotExist(err) {
				t.Errorf("expected no writ.schema written after a refused prompt, stat err = %v", err)
			}
			if got := getGitConfigAll(t, env.repoDir, "writ.writerId"); len(got) != 0 {
				t.Errorf("writ.writerId = %v, want nothing written by a refused prompt", got)
			}
		})
	}
}

// TestInit_NamespaceRejectionTable pins ValidateNamespace's grammar as
// seen through the CLI: each of these fails --namespace with exit 1,
// echoes the offending input rather than mangling it, and writes nothing.
// The embedded-newline case is the injection guard WRIT-220's plan calls
// out by name: validating by synthesizing "namespace "+name+"\n" and
// parsing it back would let this exact input inject a second declaration
// into the starter file instead of being refused.
func TestInit_NamespaceRejectionTable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		namespace string
	}{
		{"uppercase", "Api"},
		{"leading_digit", "9x"},
		{"reserved_keyword", "type"},
		{"embedded_space", "a b"},
		{"embedded_newline", "acme\ntype x {}"},
		{"over_length_limit", strings.Repeat("a", 65)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := setupTestCLIEnv(t)

			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", tc.namespace}, &stdout, &stderr)
			if code != 1 {
				t.Fatalf("init --namespace %q exited with %d, want 1; stderr: %s", tc.namespace, code, stderr.String())
			}
			// %q is how the refusal echoes the input back: verbatim for
			// everything printable, %q-escaped (literal backslash-n, not a
			// real newline) for the injection case, which is what keeps a
			// multi-line refusal from reading as multiple lines of its own.
			wantEcho := fmt.Sprintf("%q", tc.namespace)
			if !strings.Contains(stderr.String(), wantEcho) {
				t.Errorf("refusal for %q does not echo the input (want %s), got:\n%s", tc.namespace, wantEcho, stderr.String())
			}
			if _, err := os.Stat(filepath.Join(env.repoDir, "writ.schema")); !os.IsNotExist(err) {
				t.Errorf("expected no writ.schema written for rejected namespace %q, stat err = %v", tc.namespace, err)
			}
			if got := getGitConfigAll(t, env.repoDir, "writ.writerId"); len(got) != 0 {
				t.Errorf("writ.writerId = %v, want nothing written for rejected namespace %q", got, tc.namespace)
			}
		})
	}

	t.Run("empty", func(t *testing.T) {
		env := setupTestCLIEnv(t)

		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"init", "-C", env.repoDir}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("init with an empty namespace exited with %d, want 1; stderr: %s", code, stderr.String())
		}
		if _, err := os.Stat(filepath.Join(env.repoDir, "writ.schema")); !os.IsNotExist(err) {
			t.Errorf("expected no writ.schema written for an empty namespace, stat err = %v", err)
		}
	})
}

// TestInit_TwoRepositoriesSameBasenameRequireExplicitNamespaces is
// WRIT-220's own acceptance case: two repositories that would previously
// have derived the same namespace from a shared directory basename
// ("api") no longer can without a human choosing it. Both refuse
// non-interactively with no namespace; given two different explicit
// namespaces, they produce two different starter files despite the
// identical basename — the collision spec/identifiers.md used to describe
// as reachable "with no writer ever choosing it" now requires exactly
// that choice.
func TestInit_TwoRepositoriesSameBasenameRequireExplicitNamespaces(t *testing.T) {
	requireGit(t)
	root := t.TempDir()

	globalCfgPath := filepath.Join(root, "global_gitconfig")
	if err := os.WriteFile(globalCfgPath, []byte(""), 0600); err != nil {
		t.Fatalf("writing empty global config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalCfgPath)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	aDir := filepath.Join(root, "a", "api")
	bDir := filepath.Join(root, "b", "api")
	for _, dir := range []string{aDir, bDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
		cmd := exec.Command("git", "init")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init in %s: %v (%s)", dir, err, out)
		}
	}

	// Same basename, no namespace, non-interactive: both refuse.
	for _, dir := range []string{aDir, bDir} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{"init", "-C", dir}, &stdout, &stderr); code != 1 {
			t.Fatalf("init in %s with no namespace exited with %d, want 1; stderr: %s", dir, code, stderr.String())
		}
		if _, err := os.Stat(filepath.Join(dir, "writ.schema")); !os.IsNotExist(err) {
			t.Errorf("expected no writ.schema written in %s without a namespace, stat err = %v", dir, err)
		}
	}

	// Same basename, two different explicit namespaces: two different
	// starter files, chosen rather than derived.
	var stdoutA, stderrA bytes.Buffer
	if code := run(context.Background(), []string{"init", "-C", aDir, "--namespace", "repo-a"}, &stdoutA, &stderrA); code != 0 {
		t.Fatalf("init in %s with --namespace repo-a exited with %d; stderr: %s", aDir, code, stderrA.String())
	}
	var stdoutB, stderrB bytes.Buffer
	if code := run(context.Background(), []string{"init", "-C", bDir, "--namespace", "repo-b"}, &stdoutB, &stderrB); code != 0 {
		t.Fatalf("init in %s with --namespace repo-b exited with %d; stderr: %s", bDir, code, stderrB.String())
	}

	contentA, err := os.ReadFile(filepath.Join(aDir, "writ.schema"))
	if err != nil {
		t.Fatalf("reading %s/writ.schema: %v", aDir, err)
	}
	contentB, err := os.ReadFile(filepath.Join(bDir, "writ.schema"))
	if err != nil {
		t.Fatalf("reading %s/writ.schema: %v", bDir, err)
	}
	if want := "namespace repo-a\n"; string(contentA) != want {
		t.Errorf("%s/writ.schema = %q, want %q", aDir, contentA, want)
	}
	if want := "namespace repo-b\n"; string(contentB) != want {
		t.Errorf("%s/writ.schema = %q, want %q", bDir, contentB, want)
	}
	if bytes.Equal(contentA, contentB) {
		t.Errorf("two repositories sharing basename %q converged on one namespace without a human typing the same string twice", "api")
	}
}
