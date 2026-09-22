// WRIT-304: `writ init --json` coverage, kept in its own file rather than
// init_test.go (which WRIT-296 requires to pass unedited, and already has
// its own round-1/2/3 narrative) or init_partial_test.go (WRIT-296's own,
// same unedited constraint -- both are additive-only targets, but a fresh
// file reads better than interleaving this ticket's --json coverage into
// either one's existing story).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
)

// TestInit_JSONComplete pins the clean-run envelope: every remote
// configured, both ids freshly minted, the starter schema written --
// outcome "complete".
func TestInit_JSONComplete(t *testing.T) {
	env := setupTestCLIEnv(t)
	addRemote(t, env.repoDir, "origin", "https://example.com/origin.git")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init --json exited with %d; stderr: %s", code, stderr.String())
	}

	var result wire.InitResult
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindInitResult, &result)

	if result.Outcome != "complete" {
		t.Errorf("outcome = %q, want %q", result.Outcome, "complete")
	}
	if !result.WriterIDMinted || result.WriterID == "" {
		t.Errorf("writer_id/writer_id_minted = %q/%v, want a minted id", result.WriterID, result.WriterIDMinted)
	}
	if !result.RepoIDMinted || result.RepoID == "" {
		t.Errorf("repo_id/repo_id_minted = %q/%v, want a minted id", result.RepoID, result.RepoIDMinted)
	}
	if len(result.Remotes) != 1 {
		t.Fatalf("remotes = %+v, want exactly one entry", result.Remotes)
	}
	if r := result.Remotes[0]; r.Remote != "origin" || r.Status != "configured" || r.Refspec == "" {
		t.Errorf("remotes[0] = %+v, want origin/configured with a refspec", r)
	}
	if result.StarterSchema == nil || !result.StarterSchema.Written {
		t.Errorf("starter_schema = %+v, want written", result.StarterSchema)
	}
}

// TestInit_JSONPartialGhostRemote reuses
// TestInit_DiscoveredGhostRemoteDoesNotStrandGoodOnes's fixture: a
// url-less remote section `git remote` lists but Ensure cannot configure.
// It still exits 0 -- nothing was ever configured for it -- but --json
// must say "partial" and classify the skip as "unknown-remote", not
// silently agree with an exit code that cannot carry the distinction.
func TestInit_JSONPartialGhostRemote(t *testing.T) {
	env := setupTestCLIEnv(t)
	addRemote(t, env.repoDir, "origin", "https://example.com/origin.git")
	setGitConfig(t, env.repoDir, "remote.ghost.prune", "true")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init --json exited with %d, want 0; stderr: %s", code, stderr.String())
	}

	var result wire.InitResult
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindInitResult, &result)

	if result.Outcome != "partial" {
		t.Errorf("outcome = %q, want %q", result.Outcome, "partial")
	}
	byName := map[string]wire.InitRemote{}
	for _, r := range result.Remotes {
		byName[r.Remote] = r
	}
	ghost, ok := byName["ghost"]
	if !ok {
		t.Fatalf("remotes = %+v, missing ghost", result.Remotes)
	}
	if ghost.Status != "skipped" || ghost.Reason == nil || ghost.Reason.Code != "unknown-remote" {
		t.Errorf("ghost = %+v, want status skipped, reason.code unknown-remote", ghost)
	}
	if origin, ok := byName["origin"]; !ok || origin.Status != "configured" {
		t.Errorf("origin = %+v (ok=%v), want status configured", origin, ok)
	}
}

// TestInit_JSONPartialDashLeadingRemote reuses
// TestInit_DiscoveredDashLeadingRemoteDoesNotStrandGoodOnes's fixture: a
// "-"-leading remote name git itself accepts (`git remote add --`) but
// Init's name gate rejects. This is the other half of the two skip
// reasons WRIT-283 round 4 distinguished on exit code alone -- --json
// must classify it "invalid-name", distinct from ghost's "unknown-remote"
// above.
func TestInit_JSONPartialDashLeadingRemote(t *testing.T) {
	env := setupTestCLIEnv(t)
	addRemote(t, env.repoDir, "origin", "https://example.com/origin.git")
	cmd := exec.Command("git", "remote", "add", "--", "-x", "https://example.com/dash.git")
	cmd.Dir = env.repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add -- -x failed: %v (%s)", err, string(out))
	}

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init --json exited with %d, want 0; stderr: %s", code, stderr.String())
	}

	var result wire.InitResult
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindInitResult, &result)

	if result.Outcome != "partial" {
		t.Errorf("outcome = %q, want %q", result.Outcome, "partial")
	}
	var dash *wire.InitRemote
	for i := range result.Remotes {
		if result.Remotes[i].Remote == "-x" {
			dash = &result.Remotes[i]
		}
	}
	if dash == nil {
		t.Fatalf("remotes = %+v, missing -x", result.Remotes)
	}
	if dash.Status != "skipped" || dash.Reason == nil || dash.Reason.Code != "invalid-name" {
		t.Errorf("-x = %+v, want status skipped, reason.code invalid-name", *dash)
	}
}

// TestInit_JSONStoppedOnLockedConfig is the ticket's headline case: a run
// that stopped part-way -- identity already minted and in git config, the
// refspec write itself failing -- must still emit an envelope on stdout
// (the one departure from every other --json verb, which emit nothing on
// failure), with outcome "stopped" and the failing remote's status
// "failed". Without this, nothing proves the emit-on-failure rule
// (cmd/writ/init.go, "emit iff the run reached git config") actually
// fires.
func TestInit_JSONStoppedOnLockedConfig(t *testing.T) {
	env := setupTestCLIEnv(t)

	// Mint the IDs on a run with no remote to configure, so the failure
	// below lands where the ticket found it: after identity is in config.
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout, &stderr); code != 0 {
		t.Fatalf("first init exited with %d; stderr: %s", code, stderr.String())
	}
	writerID := getGitConfigAll(t, env.repoDir, "writ.writerId")
	repoID := getGitConfigAll(t, env.repoDir, "writ.repoId")
	if len(writerID) != 1 || len(repoID) != 1 {
		t.Fatalf("expected one writerId and one repoId, got %v and %v", writerID, repoID)
	}

	addRemote(t, env.repoDir, "origin", "https://example.com/repo.git")

	// Hold git's config lock so the refspec write -- and only the refspec
	// write -- fails.
	lockPath := filepath.Join(env.repoDir, ".git", "config.lock")
	if err := os.WriteFile(lockPath, nil, 0600); err != nil {
		t.Fatalf("holding the config lock: %v", err)
	}
	defer os.Remove(lockPath)

	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--json"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("init --json exited with %d, want 1, while the config was locked; stderr: %s", code, stderr.String())
	}

	if stdout.Len() == 0 {
		t.Fatalf("stdout is empty; want an init.result envelope for a run that reached git config")
	}
	var result wire.InitResult
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindInitResult, &result)

	if result.Outcome != "stopped" {
		t.Errorf("outcome = %q, want %q", result.Outcome, "stopped")
	}
	if result.WriterID != writerID[0] {
		t.Errorf("writer_id = %q, want %q", result.WriterID, writerID[0])
	}
	if result.RepoID != repoID[0] {
		t.Errorf("repo_id = %q, want %q", result.RepoID, repoID[0])
	}
	if len(result.Remotes) != 1 || result.Remotes[0].Remote != "origin" || result.Remotes[0].Status != "failed" {
		t.Errorf("remotes = %+v, want exactly one failed origin entry", result.Remotes)
	}
	if result.Remotes[0].Reason == nil || result.Remotes[0].Reason.Message == "" {
		t.Errorf("remotes[0].reason = %+v, want a populated reason", result.Remotes[0].Reason)
	}
}

// TestInit_JSONNotAGitRepoEmitsNothing pins the other side of the
// emit-on-failure rule: a run that never reached git config (not a git
// repository at all) must leave stdout empty under --json, exactly like
// every other --json verb's own failure path.
func TestInit_JSONNotAGitRepoEmitsNothing(t *testing.T) {
	requireGit(t)
	nonRepoDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", nonRepoDir, "--json"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("init --json on non-repo exited with %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty -- writ.Init never reached git config", stdout.String())
	}
}

// TestInit_JSONSecondRunReportsAlreadyConfigured pins the idempotent
// re-run: no id re-minted, the remote reported "already-configured", and
// the starter schema reported "existed" rather than written again.
func TestInit_JSONSecondRunReportsAlreadyConfigured(t *testing.T) {
	env := setupTestCLIEnv(t)
	addRemote(t, env.repoDir, "origin", "https://example.com/origin.git")

	var stdout1, stderr1 bytes.Buffer
	if code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns"}, &stdout1, &stderr1); code != 0 {
		t.Fatalf("first init exited with %d; stderr: %s", code, stderr1.String())
	}

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("second init --json exited with %d; stderr: %s", code, stderr.String())
	}

	var result wire.InitResult
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindInitResult, &result)

	if result.WriterIDMinted {
		t.Errorf("writer_id_minted = true on a re-run, want false")
	}
	if result.RepoIDMinted {
		t.Errorf("repo_id_minted = true on a re-run, want false")
	}
	if len(result.Remotes) != 1 || result.Remotes[0].Status != "already-configured" {
		t.Errorf("remotes = %+v, want a single already-configured entry", result.Remotes)
	}
	if result.StarterSchema == nil || !result.StarterSchema.Existed || result.StarterSchema.Written {
		t.Errorf("starter_schema = %+v, want existed and not written", result.StarterSchema)
	}
}

// TestInit_JSONStdoutIsOnlyJSON is the single most likely regression this
// ticket introduces: every porcelain fmt.Fprintf(stdout, ...) call in
// runInit must be suppressed under --json, leaving stdout as exactly one
// JSON document and nothing else.
func TestInit_JSONStdoutIsOnlyJSON(t *testing.T) {
	env := setupTestCLIEnv(t)
	addRemote(t, env.repoDir, "origin", "https://example.com/origin.git")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init --json exited with %d; stderr: %s", code, stderr.String())
	}

	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var env2 wire.Envelope
	if err := dec.Decode(&env2); err != nil {
		t.Fatalf("stdout does not decode as a single JSON document: %v\nstdout: %s", err, stdout.String())
	}
	if dec.More() {
		t.Errorf("stdout carries more than one JSON document:\n%s", stdout.String())
	}

	for _, prose := range []string{"Writer ID:", "Repo ID:", "Configured fetch refspec", "Wrote starter", "already configured"} {
		if strings.Contains(stdout.String(), prose) {
			t.Errorf("stdout contains porcelain prose %q under --json:\n%s", prose, stdout.String())
		}
	}
}
