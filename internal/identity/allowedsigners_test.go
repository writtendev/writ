package identity_test

import (
	"context"
	"testing"

	"github.com/writtendev/writ/internal/identity"
)

// TestAllowedSignersFile_ReadOnlyRepo pins the regression WRIT-251's plan
// calls out by name: Load returns early on a repository with no
// writ.writerId/user.name/user.email/gpg.format/user.signingKey, before it
// ever reads gpg.ssh.allowedSignersFile, so a caller that only reads
// Identity.AllowedSigners would see nothing configured even when it is.
// AllowedSignersFile must not share that blind spot -- signature
// verification runs on every read, including one from a reader with no
// writer identity at all.
func TestAllowedSignersFile_ReadOnlyRepo(t *testing.T) {
	env := setupTestEnv(t)
	setGitConfig(t, env.repoDir, "gpg.ssh.allowedSignersFile", "/path/to/allowed_signers")

	// Confirm the read-only premise: Load fails on this repo (no
	// writ.writerId, let alone gpg.format/user.signingKey).
	if _, err := identity.Load(context.Background(), env.repoDir); err == nil {
		t.Fatal("Load unexpectedly succeeded on a repo with no writer identity configured at all")
	}

	path, err := identity.AllowedSignersFile(context.Background(), env.repoDir)
	if err != nil {
		t.Fatalf("AllowedSignersFile: %v", err)
	}
	if path != "/path/to/allowed_signers" {
		t.Errorf("AllowedSignersFile = %q, want %q", path, "/path/to/allowed_signers")
	}
}

// TestAllowedSignersFile_Unset mirrors TestLoad_ValidLiteralKey's own
// empty-when-unset assertion, for the standalone helper.
func TestAllowedSignersFile_Unset(t *testing.T) {
	env := setupTestEnv(t)

	path, err := identity.AllowedSignersFile(context.Background(), env.repoDir)
	if err != nil {
		t.Fatalf("AllowedSignersFile: %v", err)
	}
	if path != "" {
		t.Errorf("AllowedSignersFile = %q, want empty when unset", path)
	}
}

// TestAllowedSignersFile_WhitespaceOnly pins the same "nothing configured"
// reading TestLoad_WhitespaceOnlyAllowedSigners pins for Load's own field.
func TestAllowedSignersFile_WhitespaceOnly(t *testing.T) {
	env := setupTestEnv(t)
	setGitConfig(t, env.repoDir, "gpg.ssh.allowedSignersFile", "   ")

	path, err := identity.AllowedSignersFile(context.Background(), env.repoDir)
	if err != nil {
		t.Fatalf("AllowedSignersFile: %v", err)
	}
	if path != "" {
		t.Errorf("AllowedSignersFile = %q, want empty: a set key with nothing in it is nothing configured", path)
	}
}
