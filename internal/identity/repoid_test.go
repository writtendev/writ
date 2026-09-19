package identity_test

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/writtendev/writ/internal/identity"
)

func TestParseRepoID_Valid(t *testing.T) {
	valid := []string{
		"a1b2c3d4e5f60718293a4b5c6d7e8f90",
		"00000000000000000000000000000000",
		"ffffffffffffffffffffffffffffffff",
		"0123456789abcdef0123456789abcdef",
	}

	for _, s := range valid {
		t.Run(s, func(t *testing.T) {
			id, err := identity.ParseRepoID(s)
			if err != nil {
				t.Fatalf("ParseRepoID(%q) error: %v", s, err)
			}
			if string(id) != s {
				t.Errorf("got %q, want %q", id, s)
			}
		})
	}
}

func TestParseRepoID_Invalid(t *testing.T) {
	invalid := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"too short (16 hex)", "a1b2c3d4e5f60718"},
		{"too short (31 hex)", "a1b2c3d4e5f60718293a4b5c6d7e8f9"},
		{"too long (33 hex)", "a1b2c3d4e5f60718293a4b5c6d7e8f90a"},
		{"uppercase", "A1B2C3D4E5F60718293A4B5C6D7E8F90"},
		{"non-hex", "g1b2c3d4e5f60718293a4b5c6d7e8f90"},
		{"with spaces", " a1b2c3d4e5f60718293a4b5c6d7e8f90"},
	}

	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := identity.ParseRepoID(tc.input)
			if err == nil {
				t.Fatalf("ParseRepoID(%q) expected error, got nil", tc.input)
			}
			if !errors.Is(err, identity.ErrInvalid) {
				t.Errorf("expected ErrInvalid, got %v", err)
			}
			var cfgErr *identity.ConfigError
			if errors.As(err, &cfgErr) {
				if cfgErr.Key != "writ.repoId" {
					t.Errorf("cfgErr.Key = %q, want 'writ.repoId'", cfgErr.Key)
				}
			}
		})
	}
}

func TestMintRepoID(t *testing.T) {
	id1, err := identity.MintRepoID()
	if err != nil {
		t.Fatalf("MintRepoID 1: %v", err)
	}
	id2, err := identity.MintRepoID()
	if err != nil {
		t.Fatalf("MintRepoID 2: %v", err)
	}
	if id1 == id2 {
		t.Errorf("minted IDs collision: %s == %s", id1, id2)
	}
	if _, err := identity.ParseRepoID(string(id1)); err != nil {
		t.Errorf("minted id1 invalid: %v", err)
	}
}

func TestLoadRepoID_Unset(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	id, err := identity.LoadRepoID(context.Background(), dir)
	if err != nil {
		t.Fatalf("LoadRepoID on clean repo: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty RepoID for unconfigured repo, got %q", id)
	}
}

func TestEnsureRepoID_GlobalNotAdopted(t *testing.T) {
	// A writ.repoId set only in global config must not be adopted by a
	// fresh repository: EnsureRepoID mints its own, local, designator
	// instead. Global sourcing is correct for writerId/personId (one
	// identity across a person's repositories) but wrong for repoId, whose
	// whole job is telling repositories apart — two repos sharing one
	// designator makes <repo-id>#<object-id> stop disambiguating anything.
	env := setupTestEnv(t)
	setFileConfig(t, env.globalCfgPath, "writ.repoId", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")

	ctx := context.Background()

	loaded, err := identity.LoadRepoID(ctx, env.repoDir)
	if err != nil {
		t.Fatalf("LoadRepoID: %v", err)
	}
	if loaded == "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("LoadRepoID adopted the global writ.repoId %q; repo-id sourcing must be local-only", loaded)
	}
	if loaded != "" {
		t.Fatalf("LoadRepoID = %q, want empty (no local writ.repoId configured)", loaded)
	}

	id, minted, err := identity.EnsureRepoID(ctx, env.repoDir)
	if err != nil {
		t.Fatalf("EnsureRepoID: %v", err)
	}
	if !minted {
		t.Errorf("expected minted=true (global writ.repoId must not count as already configured), got false")
	}
	if id == "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("EnsureRepoID adopted the global writ.repoId %q instead of minting its own", id)
	}

	// The minted ID must have been written to *local* config, not left
	// unrecorded (which would re-mint a different ID on every call).
	cmd := exec.Command("git", "config", "--local", "--get", "writ.repoId")
	cmd.Dir = env.repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --local --get writ.repoId: %v", err)
	}
	localVal := strings.TrimSpace(string(out))
	if localVal != string(id) {
		t.Errorf("local writ.repoId = %q, want minted id %q", localVal, id)
	}
}

func TestEnsureRepoID_Lifecycle(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	ctx := context.Background()

	// 1. EnsureRepoID on clean repo mints new ID
	id1, minted, err := identity.EnsureRepoID(ctx, dir)
	if err != nil {
		t.Fatalf("EnsureRepoID mint: %v", err)
	}
	if !minted {
		t.Errorf("expected minted=true, got false")
	}
	if id1 == "" {
		t.Fatal("expected non-empty minted ID")
	}

	// 2. LoadRepoID returns minted ID
	loadedID, err := identity.LoadRepoID(ctx, dir)
	if err != nil {
		t.Fatalf("LoadRepoID: %v", err)
	}
	if loadedID != id1 {
		t.Errorf("loaded %q, want %q", loadedID, id1)
	}

	// 3. Second EnsureRepoID reuses existing ID
	id2, minted2, err := identity.EnsureRepoID(ctx, dir)
	if err != nil {
		t.Fatalf("EnsureRepoID reuse: %v", err)
	}
	if minted2 {
		t.Errorf("expected minted=false on second call, got true")
	}
	if id2 != id1 {
		t.Errorf("reused %q != first %q", id2, id1)
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %s (%v)", dir, out, err)
	}
}
