package writ_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/writtendev/writ/engine"
)

// TestCheckTrustStore pins CheckTrustStore's three-way classification
// (WRIT-311 §2.3), lifted verbatim from cmd/writ/store.go's former
// checkTrustStore: unresolvable path, unconfigured, unreadable/unparseable,
// and a real, parseable allowed_signers file.
func TestCheckTrustStore(t *testing.T) {
	ctx := context.Background()

	t.Run("not a git repository reports OK, quietly", func(t *testing.T) {
		// An unresolvable git directory must not make CheckTrustStore's
		// caller (cmd/writ's maybePrintTrustHint) print a hint about a
		// path that isn't a repository writ opened at all.
		if got := writ.CheckTrustStore(ctx, t.TempDir()); got != writ.TrustStoreOK {
			t.Errorf("CheckTrustStore(non-repo) = %q, want %q", got, writ.TrustStoreOK)
		}
	})

	t.Run("no gpg.ssh.allowedSignersFile configured", func(t *testing.T) {
		dir := t.TempDir()
		runGitCmd(t, dir, "init")
		if got := writ.CheckTrustStore(ctx, dir); got != writ.TrustStoreUnconfigured {
			t.Errorf("CheckTrustStore(no allowedSignersFile) = %q, want %q", got, writ.TrustStoreUnconfigured)
		}
	})

	t.Run("gpg.ssh.allowedSignersFile set but file missing", func(t *testing.T) {
		dir := t.TempDir()
		runGitCmd(t, dir, "init")
		missing := filepath.Join(dir, "does-not-exist")
		runGitCmd(t, dir, "config", "gpg.ssh.allowedSignersFile", missing)
		if got := writ.CheckTrustStore(ctx, dir); got != writ.TrustStoreUnreadable {
			t.Errorf("CheckTrustStore(missing allowedSignersFile) = %q, want %q", got, writ.TrustStoreUnreadable)
		}
	})

	t.Run("gpg.ssh.allowedSignersFile set and readable", func(t *testing.T) {
		dir := t.TempDir()
		runGitCmd(t, dir, "init")
		_, pubLine := genSSHKey(t, dir, "id_trust")
		allowedPath := filepath.Join(dir, "allowed_signers")
		writeAllowedSigners(t, allowedPath, "alice@example.com", pubLine)
		runGitCmd(t, dir, "config", "gpg.ssh.allowedSignersFile", allowedPath)
		if got := writ.CheckTrustStore(ctx, dir); got != writ.TrustStoreOK {
			t.Errorf("CheckTrustStore(valid allowedSignersFile) = %q, want %q", got, writ.TrustStoreOK)
		}
	})
}
