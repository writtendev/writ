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
