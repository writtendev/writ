// WRIT-342: sync and init parse flags and positionals interleaved like
// every other verb, and sync refuses a malformed remote name before it
// syncs any remote.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/engine"
)

// createUnsyncedTicket declares the ticket type, syncs it, then creates one
// ticket that stays unsynced, so a sync of origin has a push to perform.
func createUnsyncedTicket(t *testing.T, ctx context.Context, aliceDir string) {
	t.Helper()
	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Alice failed: %v", err)
	}
	applyTicketSchemaViaStore(t, ctx, sA, aliceDir)
	sA.Close()
	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{"-C", aliceDir, "sync"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Alice schema sync exited with %d; stderr: %s", code, stderr.String())
	}
	sA, err = writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Reopen Alice failed: %v", err)
	}
	defer sA.Close()
	if _, err := sA.Objects.Create(ctx, "acme.ticket", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Unsynced"},
	}); err != nil {
		t.Fatalf("Alice create object: %v", err)
	}
}

// remoteWritRefs lists the refs under refs/writ/ in the bare remote.
func remoteWritRefs(t *testing.T, bareDir string) string {
	t.Helper()
	cmd := exec.Command("git", "for-each-ref", "refs/writ/")
	cmd.Dir = bareDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git for-each-ref in remote: %v", err)
	}
	return string(out)
}

// "sync origin --json" is one sync.result envelope and exit 0, not a push
// followed by exit 2 with "--json" read as a second remote name.
func TestSync_FlagsMayFollowTheRemote(t *testing.T) {
	_, aliceDir, _ := setupSyncTestHarness(t)
	ctx := context.Background()
	createUnsyncedTicket(t, ctx, aliceDir)

	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{"-C", aliceDir, "sync", "origin", "--status", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sync origin --status --json exited with %d; stderr: %s", code, stderr.String())
	}
	var statuses []struct {
		Remote   string `json:"remote"`
		Unsynced int    `json:"unsynced"`
	}
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindSyncStatus, &statuses)
	if len(statuses) != 1 || statuses[0].Remote != "origin" || statuses[0].Unsynced != 1 {
		t.Errorf("sync origin --status --json data = %+v, want one origin entry with 1 unsynced", statuses)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(ctx, []string{"-C", aliceDir, "sync", "origin", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sync origin --json exited with %d; stderr: %s", code, stderr.String())
	}
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	var env struct {
		Kind string `json:"kind"`
		Data []struct {
			Remote    string `json:"remote"`
			OpsPushed int    `json:"ops_pushed"`
		} `json:"data"`
	}
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("decode sync origin --json: %v (raw: %s)", err, stdout.String())
	}
	if dec.More() {
		t.Errorf("stdout carries more than one JSON document: %s", stdout.String())
	}
	if env.Kind != wire.KindSyncResult || len(env.Data) != 1 || env.Data[0].Remote != "origin" || env.Data[0].OpsPushed != 1 {
		t.Errorf("sync origin --json = %+v, want one sync.result entry for origin pushing 1 op", env)
	}
}

// A malformed remote name anywhere in the list is refused before any remote
// is synced: a valid remote listed first is neither pushed nor reported.
func TestSync_InvalidRemoteNameRefusedBeforeAnySync(t *testing.T) {
	bareDir, aliceDir, _ := setupSyncTestHarness(t)
	ctx := context.Background()
	createUnsyncedTicket(t, ctx, aliceDir)
	before := remoteWritRefs(t, bareDir)

	var stdout, stderr bytes.Buffer
	if code := run(ctx, []string{"-C", aliceDir, "sync", "origin", "a b"}, &stdout, &stderr); code != 2 {
		t.Fatalf("sync origin \"a b\" exited with %d (want 2); stderr: %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "origin") || strings.Contains(stderr.String(), "origin") {
		t.Errorf("origin was attempted or reported; stdout: %q, stderr: %q", stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(ctx, []string{"-C", aliceDir, "sync", "origin", "a b", "--json"}, &stdout, &stderr); code != 2 {
		t.Fatalf("sync origin \"a b\" --json exited with %d (want 2); stderr: %s", code, stderr.String())
	}
	var results []struct {
		Remote  string `json:"remote"`
		Failure *struct {
			Kind string `json:"kind"`
		} `json:"failure"`
	}
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindSyncResult, &results)
	if len(results) != 1 || results[0].Remote != "a b" || results[0].Failure == nil || results[0].Failure.Kind != "invalid-name" {
		t.Errorf("sync origin \"a b\" --json data = %+v, want only \"a b\", failing invalid-name", results)
	}

	if after := remoteWritRefs(t, bareDir); after != before {
		t.Errorf("origin's refs/writ changed although the sync was refused:\nbefore: %s\nafter:  %s", before, after)
	}
}

// "init origin --json" is an init.result envelope, not "--json" read as a
// second explicit remote.
func TestInit_FlagsMayFollowTheRemote(t *testing.T) {
	env := setupTestCLIEnv(t)
	addRemote(t, env.repoDir, "origin", "https://example.com/origin.git")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir, "--namespace", "testns", "origin", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init origin --json exited with %d; stderr: %s", code, stderr.String())
	}
	var result wire.InitResult
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindInitResult, &result)
	if result.Outcome != "complete" || len(result.Remotes) != 1 || result.Remotes[0].Remote != "origin" {
		t.Errorf("init origin --json = %+v, want a complete run configuring only origin", result)
	}
}
