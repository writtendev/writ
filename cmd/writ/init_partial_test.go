package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInit_InvalidNamespaceOnExistingSchemaExitsNonZero pins the widened
// half of the eager --namespace validation (round-1 medium finding): on
// main, --namespace was validated inside resolveNamespace, which only ran
// when a starter writ.schema was actually due, so an invalid --namespace on
// a repository that already has one printed
//
//	writ.schema already exists; leaving it unchanged (--namespace "a b" ignored)
//
// and exited 0. Here the flag is validated unconditionally, before
// writ.Init is even called (cmd/writ/init.go, runInit), so the same
// scenario now exits 1 with no such repository ever consulted. The
// reviewer confirmed the eager placement is load-bearing -- writ.Init does
// not re-validate StarterNamespace and cmd/writ no longer stats
// writ.schema itself before the first call -- so this test pins the
// resulting behaviour change rather than arguing the placement is wrong.
func TestInit_InvalidNamespaceOnExistingSchemaExitsNonZero(t *testing.T) {
	env := setupTestCLIEnv(t)

	// Get a writ.schema onto disk first, exactly as a real first run would.
	var first bytes.Buffer
	if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &first, &first); code != 0 {
		t.Fatalf("first init exited with %d; output: %s", code, first.String())
	}
	schemaPath := filepath.Join(env.repoDir, "writ.schema")
	before, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("reading writ.schema after first init: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "a b"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("init --namespace %q on a repository with an existing writ.schema exited %d, want 1; stdout: %s stderr: %s", "a b", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "--namespace") || !strings.Contains(stderr.String(), namespaceGrammar) {
		t.Errorf("stderr = %q, want a --namespace grammar refusal naming %q", stderr.String(), namespaceGrammar)
	}
	if strings.Contains(stdout.String(), "ignored") {
		t.Errorf("stdout = %q, want no 'ignored' message -- the run refused before reaching writ.Init at all", stdout.String())
	}

	after, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("reading writ.schema after the refused run: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("writ.schema changed after a refused --namespace: got %q, want unchanged %q", after, before)
	}
}

// TestInit_ExplicitRemoteFailureNamesEveryUnconfiguredRemote pins the
// round-1 major finding: a hard failure on an explicit remote list must
// name every remote left without a writ fetch refspec, including ones
// writ.Init never got to, not just the one that failed. Verified in the fix
// pass against main's actual output for this exact scenario (writ init
// ghost origin, ghost url-less): main and this branch both report
//
//	fetch refspec NOT configured for: ghost, origin
func TestInit_ExplicitRemoteFailureNamesEveryUnconfiguredRemote(t *testing.T) {
	env := setupTestCLIEnv(t)

	// ghost is a url-less remote section (no "url" key) -- Ensure's
	// existence/name gate rejects it, and being explicit (a positional
	// arg, not discovered) that rejection aborts the run instead of being
	// skipped (see TestInit_ExplicitBadRemoteStillAbortsAmongGoodOnes).
	setGitConfig(t, env.repoDir, "remote.ghost.prune", "true")
	addRemote(t, env.repoDir, "origin", "https://example.com/origin.git")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns", "ghost", "origin"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("init ghost origin exited with %d, want 1; stdout: %s stderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "fetch refspec NOT configured for: ghost, origin") {
		t.Errorf("stderr = %q, want it to name both ghost and origin as not configured", stderr.String())
	}
	// origin never got a chance to succeed or fail -- writ.Init stopped at
	// ghost -- so it must not appear as configured anywhere in the report.
	if strings.Contains(stderr.String(), "fetch refspec configured for: origin") {
		t.Errorf("stderr = %q, want origin not reported as configured", stderr.String())
	}
	for _, entry := range getGitConfigAll(t, env.repoDir, "remote.origin.fetch") {
		if strings.Contains(entry, "writ") {
			t.Errorf("remote.origin.fetch = %q, want no writ refspec written -- writ.Init never reached origin", entry)
		}
	}
}

// TestInit_NoRemotesMessagePrecedesStarterSchemaLine pins main's ordering
// between the "no remotes configured" line and the starter writ.schema
// outcome (round-2 medium finding): both are stdout, so this is a real
// reorder to catch, not stream interleaving. The round-1 fix pass moved
// the starter-schema render into renderInitResult, which runs before
// runInit prints the remote summary -- inverting main's step 6 (remotes)
// then step 7 (starter schema) order. Verified by reverting the fix
// (calling renderStarterSchemaOutcome from inside renderInitResult again,
// ahead of the remote summary): this test fails with the starter-schema
// line first.
func TestInit_NoRemotesMessagePrecedesStarterSchemaLine(t *testing.T) {
	env := setupTestCLIEnv(t)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init exited with %d, want 0; stdout: %s stderr: %s", code, stdout.String(), stderr.String())
	}

	out := stdout.String()
	remotesLine := "No git remotes configured; fetch refspec will be added when a remote is configured."
	starterLine := "Wrote starter "
	remotesIdx := strings.Index(out, remotesLine)
	starterIdx := strings.Index(out, starterLine)
	if remotesIdx < 0 {
		t.Fatalf("stdout = %q, want it to contain %q", out, remotesLine)
	}
	if starterIdx < 0 {
		t.Fatalf("stdout = %q, want it to contain %q", out, starterLine)
	}
	if remotesIdx > starterIdx {
		t.Errorf("stdout = %q, want %q before %q, matching main's order", out, remotesLine, starterLine)
	}
}

// TestInit_SkippedRemoteSummaryPrecedesStarterSchemaLine pins the same
// ordering for the skipped-remote summary (round-2 medium finding, second
// half): a discovered url-less remote is skipped and reportSkippedRemotes
// names it on stderr, and on main that always printed before the starter
// writ.schema line on stdout. stdout and stderr are combined into one
// writer here, as the round-2 review did, so the assertion is about
// program order, not which stream a line lands on.
func TestInit_SkippedRemoteSummaryPrecedesStarterSchemaLine(t *testing.T) {
	env := setupTestCLIEnv(t)
	setGitConfig(t, env.repoDir, "remote.ghost.prune", "true")

	var combined bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &combined, &combined)
	if code != 0 {
		t.Fatalf("init exited with %d, want 0; output: %s", code, combined.String())
	}

	out := combined.String()
	summaryLine := "discovered remote(s) could not be configured"
	starterLine := "Wrote starter "
	summaryIdx := strings.Index(out, summaryLine)
	starterIdx := strings.Index(out, starterLine)
	if summaryIdx < 0 {
		t.Fatalf("output = %q, want it to contain %q", out, summaryLine)
	}
	if starterIdx < 0 {
		t.Fatalf("output = %q, want it to contain %q", out, starterLine)
	}
	if summaryIdx > starterIdx {
		t.Errorf("output = %q, want %q before %q, matching main's order", out, summaryLine, starterLine)
	}
}

// The three tests below pin the round-3 medium finding: renderInitResult
// ran renderIdentityState unconditionally on every writ.Init result, and a
// zero-valued InitResult (IdentityErr == nil, SigningKey == "") -- exactly
// what every early return in writ.Init leaves behind, since none of them
// reach step 6 (identity.Load) -- read as "identity determined, no key",
// printing a fabricated "Signing key:  (ssh)" line main never printed. The
// fix guards renderIdentityState on result.RepoID != "", the field writ.Init
// sets immediately before steps 5 and 6 run unconditionally to completion
// (cmd/writ/init.go's renderIdentityState godoc walks the reasoning).
//
// Each test below was confirmed to fail -- with a "Signing key:  (ssh)"
// line appearing where the assertion says it must not -- by temporarily
// reverting renderIdentityState to drop the RepoID guard, then reverted
// back once confirmed.

// TestInit_NonRepoPrintsNoFabricatedIdentityLine pins the finding's own
// reported repro: a directory that is not a git repository at all. writ.Init
// returns before ever resolving a repository, so nothing is determined --
// stdout must be completely empty, matching main exactly (verified by
// running a main binary against the same scenario: main's stdout is also
// empty).
func TestInit_NonRepoPrintsNoFabricatedIdentityLine(t *testing.T) {
	requireGit(t)
	nonRepoDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", nonRepoDir}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("init on non-repo exited with %d, want 1", code)
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty -- writ.Init never resolved a repository, so nothing was determined to print", stdout.String())
	}
	if !strings.Contains(stderr.String(), "not a git repository") {
		t.Errorf("stderr does not mention not a git repo: %s", stderr.String())
	}
}

// TestInit_WriterIDMintFailurePrintsNoFabricatedIdentityLine pins a second,
// distinct early-return path: writ.Init opens the repository, discovers no
// remotes, and only then fails -- at step 4, minting and persisting a writer
// ID -- because .git itself is not writable. This is a different return
// site than the non-repo case (identity.EnsureWriterID's own git config
// write, not resolveRepoRoot), reached only once discoverRemotes has
// already succeeded, so it exercises a materially different InitResult
// zero-state (Remotes considered, WriterID/RepoID both still unset).
func TestInit_WriterIDMintFailurePrintsNoFabricatedIdentityLine(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions do not block writes")
	}
	env := setupTestCLIEnv(t)

	// No writ.writerId configured yet, so EnsureWriterID must mint one and
	// persist it via `git config --local` -- the write that a read-only
	// .git turns into a permission-denied failure. `git config --add`
	// writes via lock-and-rename, which needs write permission on the
	// containing directory, not the file it replaces, so .git itself (not
	// just .git/config) has to be restricted.
	gitDir := filepath.Join(env.repoDir, ".git")
	if err := os.Chmod(gitDir, 0o555); err != nil {
		t.Fatalf("chmod .git read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)

	if err := os.Chmod(gitDir, 0o755); err != nil {
		t.Fatalf("restore .git permissions: %v", err)
	}

	if code != 1 {
		t.Fatalf("init with a read-only .git exited with %d, want 1; stdout: %s stderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "ensure writer ID") {
		t.Fatalf("stderr = %q, want it to name the writer-ID step as where writ.Init stopped", stderr.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty -- writ.Init never minted a writer ID, so nothing about identity was determined to print", stdout.String())
	}
}

// TestInit_RepoIDMintFailurePrintsWriterIDButNoFabricatedIdentityLine is the
// test that rules out the simpler fix of guarding renderIdentityState on
// WriterID != "" instead of RepoID != "": with writ.writerId already
// configured, EnsureWriterID succeeds and sets result.WriterID without
// writing anything (step 4's first half), and only the very next call,
// EnsureRepoID, fails to mint and persist a repo ID against the same
// read-only .git (step 4's second half) -- returning before step 5 or 6
// ever runs. A WriterID-only guard would treat this InitResult as "identity
// determined" and print the fabricated line anyway; the fix must still
// suppress it here even though WriterID is genuinely known and printed.
func TestInit_RepoIDMintFailurePrintsWriterIDButNoFabricatedIdentityLine(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions do not block writes")
	}
	env := setupTestCLIEnv(t)
	setGitConfig(t, env.repoDir, "writ.writerId", "aaaaaaaaaaaaaaaa")

	gitDir := filepath.Join(env.repoDir, ".git")
	if err := os.Chmod(gitDir, 0o555); err != nil {
		t.Fatalf("chmod .git read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(gitDir, 0o755) })

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr)

	if err := os.Chmod(gitDir, 0o755); err != nil {
		t.Fatalf("restore .git permissions: %v", err)
	}

	if code != 1 {
		t.Fatalf("init with a read-only .git exited with %d, want 1; stdout: %s stderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "ensure repo ID") {
		t.Fatalf("stderr = %q, want it to name the repo-ID step as where writ.Init stopped", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Writer ID: aaaaaaaaaaaaaaaa (already configured)") {
		t.Errorf("stdout = %q, want the already-configured writer ID reported -- that much writ.Init genuinely determined", stdout.String())
	}
	if strings.Contains(stdout.String(), "Signing key:") {
		t.Errorf("stdout = %q, want no Signing key line -- writ.Init never reached the identity step", stdout.String())
	}
}
