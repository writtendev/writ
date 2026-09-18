package identity_test

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"testing"

	"github.com/writtendev/writ/internal/identity"
)

func TestMintWriterID_FormatAndUnique(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{16}$`)
	seen := make(map[identity.WriterID]struct{})
	const draws = 100

	for i := 0; i < draws; i++ {
		id, err := identity.MintWriterID()
		if err != nil {
			t.Fatalf("MintWriterID failed: %v", err)
		}
		if !re.MatchString(string(id)) {
			t.Fatalf("MintWriterID produced invalid ID %q", id)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("MintWriterID collision on draw %d: %q", i, id)
		}
		seen[id] = struct{}{}
	}
}

func TestEnsureWriterID_Precedence(t *testing.T) {
	t.Run("local_preset", func(t *testing.T) {
		env := setupTestEnv(t)
		const localID = "1111111111111111"
		setGitConfig(t, env.repoDir, "writ.writerId", localID)

		id, minted, err := identity.EnsureWriterID(context.Background(), env.repoDir, nil)
		if err != nil {
			t.Fatalf("EnsureWriterID unexpected error: %v", err)
		}
		if minted {
			t.Errorf("EnsureWriterID minted = true, want false for pre-set local ID")
		}
		if id != localID {
			t.Errorf("EnsureWriterID id = %q, want %q", id, localID)
		}
	})

	t.Run("global_only", func(t *testing.T) {
		env := setupTestEnv(t)
		const globalID = "2222222222222222"
		setFileConfig(t, env.globalCfgPath, "writ.writerId", globalID)

		id, minted, err := identity.EnsureWriterID(context.Background(), env.repoDir, nil)
		if err != nil {
			t.Fatalf("EnsureWriterID unexpected error: %v", err)
		}
		if minted {
			t.Errorf("EnsureWriterID minted = true, want false for global-only ID")
		}
		if id != globalID {
			t.Errorf("EnsureWriterID id = %q, want %q", id, globalID)
		}

		// Ensure it was NOT copied to local repository config
		cmd := exec.Command("git", "config", "--local", "--get", "writ.writerId")
		cmd.Dir = env.repoDir
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("git config --local writ.writerId should be unset, but got %q", string(out))
		}
	})

	t.Run("local_overrides_global", func(t *testing.T) {
		env := setupTestEnv(t)
		setFileConfig(t, env.globalCfgPath, "writ.writerId", "2222222222222222")
		setGitConfig(t, env.repoDir, "writ.writerId", "3333333333333333")

		id, minted, err := identity.EnsureWriterID(context.Background(), env.repoDir, nil)
		if err != nil {
			t.Fatalf("EnsureWriterID unexpected error: %v", err)
		}
		if minted {
			t.Errorf("EnsureWriterID minted = true, want false for local override")
		}
		if id != "3333333333333333" {
			t.Errorf("EnsureWriterID id = %q, want local value \"3333333333333333\"", id)
		}
	})

	t.Run("unset_mints_and_persists", func(t *testing.T) {
		env := setupTestEnv(t)

		id, minted, err := identity.EnsureWriterID(context.Background(), env.repoDir, nil)
		if err != nil {
			t.Fatalf("EnsureWriterID unexpected error: %v", err)
		}
		if !minted {
			t.Errorf("EnsureWriterID minted = false, want true when unset")
		}
		if len(id) != 16 {
			t.Errorf("EnsureWriterID id = %q, want length 16", id)
		}

		// Verify persisted to local repo config
		cmd := exec.Command("git", "config", "--local", "--get", "writ.writerId")
		cmd.Dir = env.repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git config --local --get writ.writerId failed: %v (%s)", err, string(out))
		}
		if got := string(out[:16]); identity.WriterID(got) != id {
			t.Errorf("persisted local writ.writerId = %q, want %q", got, id)
		}
	})
}

func TestEnsureWriterID_CollisionRetry(t *testing.T) {
	env := setupTestEnv(t)

	rejectedCount := 0
	taken := func(w identity.WriterID) bool {
		if rejectedCount < 3 {
			rejectedCount++
			return true
		}
		return false
	}

	id, minted, err := identity.EnsureWriterID(context.Background(), env.repoDir, taken)
	if err != nil {
		t.Fatalf("EnsureWriterID unexpected error: %v", err)
	}
	if !minted {
		t.Errorf("EnsureWriterID minted = false, want true")
	}
	if rejectedCount != 3 {
		t.Errorf("rejectedCount = %d, want 3", rejectedCount)
	}
	if len(id) != 16 {
		t.Errorf("EnsureWriterID id = %q, want length 16", id)
	}
}

func TestEnsureWriterID_InvalidExistingID(t *testing.T) {
	env := setupTestEnv(t)
	setGitConfig(t, env.repoDir, "writ.writerId", "INVALID-NOT-HEX!")

	_, _, err := identity.EnsureWriterID(context.Background(), env.repoDir, nil)
	if err == nil {
		t.Fatal("EnsureWriterID succeeded with invalid existing ID, want error")
	}
	if !errors.Is(err, identity.ErrInvalid) {
		t.Errorf("EnsureWriterID error = %v, want errors.Is ErrInvalid", err)
	}
}

func TestEnsureWriterID_NonRepoDirectory(t *testing.T) {
	requireGit(t)
	nonRepoDir := t.TempDir()

	_, _, err := identity.EnsureWriterID(context.Background(), nonRepoDir, nil)
	if err == nil {
		t.Fatal("EnsureWriterID on non-repo directory succeeded, want error")
	}
	var cfgErr *identity.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("EnsureWriterID on non-repo returned %T (%v), want *identity.ConfigError", err, err)
	}
}

func TestEnsureWriterID_ContextCancelled(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := identity.EnsureWriterID(ctx, env.repoDir, nil)
	if err == nil {
		t.Fatal("EnsureWriterID with cancelled context succeeded, want error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("EnsureWriterID error = %v, want errors.Is context.Canceled", err)
	}
}
