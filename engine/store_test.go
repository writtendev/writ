package writ_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
)

func dummySigner() writ.Signer {
	return codec.SignerFunc(func(ctx context.Context, payload []byte) (string, error) {
		return "dummy-signature", nil
	})
}

func setupConfiguredRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	runGitCmd(t, dir, "init")
	runGitCmd(t, dir, "config", "user.name", "Alice Test")
	runGitCmd(t, dir, "config", "user.email", "alice@example.com")
	runGitCmd(t, dir, "config", "writ.writerId", "0123456789abcdef")
	runGitCmd(t, dir, "config", "gpg.format", "ssh")
	runGitCmd(t, dir, "config", "user.signingKey", "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGdummy")

	// Commit dummy file so HEAD exists
	dummyFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write dummy file: %v", err)
	}
	runGitCmd(t, dir, "add", "README.md")
	runGitCmd(t, dir, "commit", "-m", "initial commit")

	return dir, dir
}

func runGitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\nOutput: %s", args, err, string(out))
	}
	return string(out)
}

// coreSchemaSrc is the vocabulary the store-backed tests in this package
// write objects under. Writ hard-codes exactly one object type, `schema`;
// every other type an object can have is declared as data by a schema
// object in the repository's own log, so a test that writes an object
// installs a vocabulary for it first, exactly as a consumer would. Nothing
// here is privileged: these three types are declared the same way a
// waypoint or a standup is elsewhere in this package.
const coreSchemaSrc = `namespace acme
description "The vocabulary this package's store-backed tests write under"

type widget {
  description "A titled object carrying revisions, approvals and assignees"

  op create 1, update 1 {
    title        string(200)  lww
    description  text         lww
  }

  op set-status 1 {
    status  enum(draft, open, closed, merged)  lww
    reason  string(200)                        lww
  }

  op revision 1 {
    base  git-oid  append
    head  git-oid  append
  }

  op approval 1 {
    revision  git-oid                               keyed-lww  key(subject person-ref, revision git-oid)
    subject   person-ref                            keyed-lww  key(subject person-ref, revision git-oid)
    verdict   enum(approve, request-changes, none)  keyed-lww  key(subject person-ref, revision git-oid)
    message   text                                  keyed-lww  key(subject person-ref, revision git-oid)
  }

  op assign 1 {
    add     [person-ref]  set-observed-remove  target(assignees)
    remove  [person-ref]  set-observed-remove  target(assignees)
  }
}

type gadget {
  description "A titled object carrying a state, assignees and tags"

  op create 1, update 1 {
    title        string(200)  lww
    description  text         lww
  }

  op set-state 1 {
    state   string(60)   lww
    reason  string(200)  lww
  }

  op assign 1 {
    add     [person-ref]  set-observed-remove  target(assignees)
    remove  [person-ref]  set-observed-remove  target(assignees)
  }

  op tag 1 {
    add     [object-ref]  set-observed-remove  target(tags)
    remove  [object-ref]  set-observed-remove  target(tags)
  }
}

type note {
  description "A body of text pointing at another object"

  op create 1 {
    text         text     lww
    subject      untyped  create-once
    in_reply_to  untyped  create-once
    anchor       untyped  create-once
  }

  op edit 1 {
    text  text  lww
  }

  op delete 1 {
    deleted  bool  tombstone
  }
}
`

// coreSchemaObjectID is the object id coreSchemaSrc is installed under. A
// schema object is an object like any other — it is listed, queried and
// counted alongside the objects it declares the types of — so a test
// asserting over every object in a store names it here rather than
// pretending the vocabulary arrived from nowhere.
const coreSchemaObjectID = "sch-acme"

// applyCoreSchema installs coreSchemaSrc into store's log.
func applyCoreSchema(t testing.TB, ctx context.Context, store *writ.Store) {
	t.Helper()
	if err := store.ApplySchema(ctx, compileTestSchema(t, coreSchemaObjectID, coreSchemaSrc)); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
}

// openStoreWithCoreSchema opens a freshly configured repository with
// coreSchemaSrc already installed — the setup every test that writes an
// object of a declared type needs. Tests that need Open options of their
// own call writ.Open themselves and applyCoreSchema after it.
func openStoreWithCoreSchema(t *testing.T) (*writ.Store, context.Context, string) {
	t.Helper()
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	applyCoreSchema(t, ctx, store)
	return store, ctx, dir
}

func TestOpenMatrix(t *testing.T) {
	// 1. Normal working tree
	repoDir, _ := setupConfiguredRepo(t)
	s1, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open normal repo failed: %v", err)
	}
	defer s1.Close()

	if w := s1.Writer(); w.ID != "0123456789abcdef" || w.Name != "Alice Test" || w.Email != "alice@example.com" {
		t.Errorf("unexpected writer: %+v", w)
	}

	// 2. Subdirectory of a working tree
	subDir := filepath.Join(repoDir, "sub", "deep", "dir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subDir: %v", err)
	}
	s2, err := writ.Open(subDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open subdir failed: %v", err)
	}
	defer s2.Close()

	if w := s2.Writer(); w.ID != "0123456789abcdef" {
		t.Errorf("unexpected writer from subdir: %+v", w)
	}

	// 3. Linked worktree
	wtDir := filepath.Join(t.TempDir(), "linked-wt")
	runGitCmd(t, repoDir, "worktree", "add", wtDir, "-b", "wt-branch")

	s3, err := writ.Open(wtDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open linked worktree failed: %v", err)
	}
	defer s3.Close()

	// 4. Bare repository
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	runGitCmd(t, t.TempDir(), "clone", "--bare", repoDir, bareDir)

	s4, err := writ.Open(bareDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open bare repo failed: %v", err)
	}
	defer s4.Close()

	// 5. Unconfigured repository (opens read-only, first write fails with ErrNoIdentity)
	unconfDir := t.TempDir()
	runGitCmd(t, unconfDir, "init")

	s5, err := writ.Open(unconfDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open unconfigured repo failed: %v", err)
	}
	defer s5.Close()

	// Query should succeed
	results, err := s5.Query.Objects(writ.ObjectFilter{Type: []string{"acme.widget"}})
	if err != nil {
		t.Fatalf("Query on unconfigured repo failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 widgets, got %d", len(results))
	}

	// Write should fail with ErrNoIdentity, before any vocabulary lookup
	// the undeclared type would otherwise fail first.
	_, err = s5.Objects.Create(context.Background(), "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Unconfigured write"},
	})
	if !errors.Is(err, writ.ErrNoIdentity) {
		t.Errorf("expected ErrNoIdentity, got: %v", err)
	}
}

func TestStoreCloseAndRefresh(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)
	s, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	stats, err := s.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}
	if stats.Rebuilt {
		t.Errorf("expected incremental refresh, got rebuilt")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	// Safe to close again
	if err := s.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
}

func TestStoreMissingSigningKey(t *testing.T) {
	dir := t.TempDir()
	runGitCmd(t, dir, "init")
	runGitCmd(t, dir, "config", "user.name", "Alice Test")
	runGitCmd(t, dir, "config", "user.email", "alice@example.com")
	runGitCmd(t, dir, "config", "writ.writerId", "0123456789abcdef")
	// Omit user.signingKey

	dummyFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(dummyFile, []byte("# Test\n"), 0o644); err != nil {
		t.Fatalf("write dummy file: %v", err)
	}
	runGitCmd(t, dir, "add", "README.md")
	runGitCmd(t, dir, "commit", "-m", "initial commit")

	ctx := context.Background()

	// 1. Without WithSigner: Writer() returns identity, but writes return ErrNoSigningKey
	s1, err := writ.Open(dir)
	if err != nil {
		t.Fatalf("Open without signer failed: %v", err)
	}
	defer s1.Close()

	if w := s1.Writer(); w.ID != "0123456789abcdef" || w.Name != "Alice Test" {
		t.Errorf("unexpected writer: %+v", w)
	}

	_, err = s1.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Should fail signing key"},
	})
	if !errors.Is(err, writ.ErrNoSigningKey) {
		t.Errorf("expected ErrNoSigningKey, got: %v", err)
	}

	// 2. With WithSigner: write succeeds
	s2, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open with custom signer failed: %v", err)
	}
	defer s2.Close()

	applyCoreSchema(t, ctx, s2)

	id, err := s2.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Should succeed with custom signer"},
	})
	if err != nil {
		t.Fatalf("Objects.Create with custom signer failed: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty widget ID")
	}
}

