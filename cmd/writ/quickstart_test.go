package main

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
)

// quickstartTestSchema is the writ.schema this test writes and applies,
// mirroring docs/quickstart.md's own first step: a schema-authoring
// tutorial before an object one. "quickstart.ticket" is the neutral example type
// AGENTS.md and spec/schema-source.md use — writ names no downstream
// product.
const quickstartTestSchema = `namespace quickstart

type ticket {
  op create 1, update 1 {
    title string lww
  }
}
`

func TestQuickstart(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	env := setupTestCLIEnv(t)
	aliceDir := env.repoDir
	setupSigningKey(t, aliceDir)

	// Step 1: Initial commit in repo
	commitFile(t, aliceDir, "README.md", "# My Project\n", "Initial commit")

	// Step 2: writ init
	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"init", "-C", aliceDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("step 2 (writ init) failed with %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Writer ID:") {
		t.Errorf("step 2 output missing Writer ID: %s", stdout.String())
	}

	// Step 3: Declare the ticket type in writ.schema and apply it
	writeSchemaFile(t, aliceDir, quickstartTestSchema)

	stdout.Reset()
	stderr.Reset()
	code = run(ctx, []string{"schema", "apply", "-C", aliceDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("step 3 (schema apply) failed with %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Created schema object") {
		t.Errorf("step 3 output missing schema creation: %s", stdout.String())
	}

	// Step 4: Create a ticket
	stdout.Reset()
	stderr.Reset()
	code = run(ctx, []string{
		"object", "create", "-C", aliceDir, "quickstart.ticket", "create",
		"-field", "title=Add main entry point",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("step 4 (object create) failed with %d; stderr: %s", code, stderr.String())
	}
	objectID := strings.TrimSpace(stdout.String())
	if len(objectID) != 32 {
		t.Fatalf("unexpected object id format: %q", objectID)
	}

	// Step 5: Apply an update
	stdout.Reset()
	stderr.Reset()
	code = run(ctx, []string{
		"object", "apply", "-C", aliceDir, objectID, "update",
		"-field", "title=Add main entry point (ready for review)",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("step 5 (object apply) failed with %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), objectID+": applied update") {
		t.Errorf("step 5 output missing apply confirmation: %s", stdout.String())
	}

	// Step 6: Set up remote and sync
	bareDir := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}
	addRemote(t, aliceDir, "origin", bareDir)

	stdout.Reset()
	stderr.Reset()
	code = run(ctx, []string{"sync", "-C", aliceDir, "origin"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("step 6 (writ sync) failed with %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "origin: pushed") {
		t.Errorf("step 6 output missing pushed ops: %s", stdout.String())
	}

	// Step 7: Collaborator clones and syncs
	bobDir := filepath.Join(t.TempDir(), "collab")
	cmd := exec.Command("git", "clone", bareDir, bobDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone to bobDir: %v (%s)", err, string(out))
	}
	setGitConfig(t, bobDir, "user.name", "Bob")
	setGitConfig(t, bobDir, "user.email", "bob@example.com")
	setupSigningKey(t, bobDir)

	stdout.Reset()
	stderr.Reset()
	code = run(ctx, []string{"init", "-C", bobDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("bob init failed with %d; stderr: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run(ctx, []string{"sync", "-C", bobDir, "origin"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("bob sync failed with %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "origin: fetched") {
		t.Errorf("bob sync output missing fetched ops: %s", stdout.String())
	}

	// Collaborator lists tickets
	stdout.Reset()
	stderr.Reset()
	code = run(ctx, []string{"object", "list", "-C", bobDir, "quickstart.ticket"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("bob object list failed with %d; stderr: %s", code, stderr.String())
	}
	listOut := stdout.String()
	if !strings.Contains(listOut, objectID[:8]) {
		t.Errorf("bob object list missing object ID %s: %s", objectID[:8], listOut)
	}

	// Collaborator views the ticket
	stdout.Reset()
	stderr.Reset()
	code = run(ctx, []string{"object", "show", "-C", bobDir, objectID}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("bob object show failed with %d; stderr: %s", code, stderr.String())
	}
	showOut := stdout.String()
	if !strings.Contains(showOut, "Add main entry point (ready for review)") {
		t.Errorf("bob object show missing updated title: %s", showOut)
	}
}
