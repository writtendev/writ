package writ_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine"
)

// setupBareInitRepo creates a fresh, otherwise-unconfigured git repository
// (no writer id, no signing key) for exercising writ.Init directly.
func setupBareInitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitCmd(t, dir, "init")
	runGitCmd(t, dir, "config", "user.name", "Alice Test")
	runGitCmd(t, dir, "config", "user.email", "alice@example.com")
	return dir
}

func TestInitMintsWriterAndRepoID(t *testing.T) {
	dir := setupBareInitRepo(t)

	result, err := writ.Init(context.Background(), dir, writ.InitOptions{StarterNamespace: "acme"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !result.WriterIDMinted {
		t.Errorf("WriterIDMinted = false, want true on a fresh repository")
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(result.WriterID) {
		t.Errorf("WriterID = %q, want 16 lowercase hex characters", result.WriterID)
	}
	if !result.RepoIDMinted {
		t.Errorf("RepoIDMinted = false, want true on a fresh repository")
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(result.RepoID) {
		t.Errorf("RepoID = %q, want 32 lowercase hex characters", result.RepoID)
	}
	if result.PersonID != "email:alice@example.com" {
		t.Errorf("PersonID = %q, want email:alice@example.com", result.PersonID)
	}
	if result.IdentityErr == nil {
		t.Errorf("IdentityErr = nil, want a signing-key error on a repository with no signing key configured")
	}
}

func TestInitReusesExistingIdentity(t *testing.T) {
	dir := setupBareInitRepo(t)
	runGitCmd(t, dir, "config", "writ.writerId", "1111111111111111")
	runGitCmd(t, dir, "config", "writ.repoId", "22222222222222222222222222222222")

	result, err := writ.Init(context.Background(), dir, writ.InitOptions{StarterNamespace: "acme"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if result.WriterIDMinted || result.WriterID != "1111111111111111" {
		t.Errorf("WriterID = %q (minted %v), want the pre-configured id reused", result.WriterID, result.WriterIDMinted)
	}
	if result.RepoIDMinted || result.RepoID != "22222222222222222222222222222222" {
		t.Errorf("RepoID = %q (minted %v), want the pre-configured id reused", result.RepoID, result.RepoIDMinted)
	}

	// Calling Init again must not mint a second id: re-running is safe.
	second, err := writ.Init(context.Background(), dir, writ.InitOptions{})
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if second.WriterIDMinted || second.WriterID != result.WriterID {
		t.Errorf("second Init WriterID = %q (minted %v), want the same id reused", second.WriterID, second.WriterIDMinted)
	}
}

func TestInitStarterNamespaceRequiredWhenDue(t *testing.T) {
	dir := setupBareInitRepo(t)

	result, err := writ.Init(context.Background(), dir, writ.InitOptions{})
	if !errors.Is(err, writ.ErrStarterNamespaceRequired) {
		t.Fatalf("Init err = %v, want ErrStarterNamespaceRequired", err)
	}

	// Nothing may have been written: a caller resolving the namespace
	// (interactively or otherwise) and retrying must find the repository
	// exactly as it left it.
	if result.WriterID != "" {
		t.Errorf("WriterID = %q, want unset -- Init must not write before resolving the starter namespace", result.WriterID)
	}
	if got := getConfigAll(t, dir, "writ.writerId"); len(got) != 0 {
		t.Errorf("writ.writerId = %v, want nothing written", got)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "writ.schema")); !os.IsNotExist(statErr) {
		t.Errorf("expected no writ.schema written, stat err = %v", statErr)
	}
}

func TestInitWritesStarterSchemaWhenNamespaceGiven(t *testing.T) {
	dir := setupBareInitRepo(t)

	result, err := writ.Init(context.Background(), dir, writ.InitOptions{StarterNamespace: "acme"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !result.StarterSchemaWritten {
		t.Fatalf("StarterSchemaWritten = false, want true")
	}
	content, readErr := os.ReadFile(result.StarterSchemaPath)
	if readErr != nil {
		t.Fatalf("reading starter writ.schema: %v", readErr)
	}
	if want := "namespace acme\n"; string(content) != want {
		t.Errorf("starter writ.schema = %q, want %q", content, want)
	}

	// A second Init call must never overwrite it, whatever namespace is
	// passed.
	second, err := writ.Init(context.Background(), dir, writ.InitOptions{StarterNamespace: "different"})
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if second.StarterSchemaWritten {
		t.Errorf("second Init overwrote an existing writ.schema")
	}
	if !second.StarterSchemaExisted {
		t.Errorf("StarterSchemaExisted = false, want true on a repository with one already")
	}
	after, readErr := os.ReadFile(result.StarterSchemaPath)
	if readErr != nil {
		t.Fatalf("reading writ.schema after second Init: %v", readErr)
	}
	if string(after) != string(content) {
		t.Errorf("writ.schema changed: got %q, want unchanged %q", after, content)
	}
}

func TestInitBareRepoNeedsNoNamespace(t *testing.T) {
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "--bare")

	result, err := writ.Init(context.Background(), dir, writ.InitOptions{})
	if err != nil {
		t.Fatalf("Init on a bare repository: %v", err)
	}
	if result.WorkTree != "" {
		t.Errorf("WorkTree = %q, want empty for a bare repository", result.WorkTree)
	}
	if result.StarterSchemaPath != "" {
		t.Errorf("StarterSchemaPath = %q, want empty -- a bare repository has no work tree to put one in", result.StarterSchemaPath)
	}
	if !result.WriterIDMinted {
		t.Errorf("WriterIDMinted = false, want true -- a bare repository still gets a writer id")
	}
}

func TestInitExplicitBadRemoteAborts(t *testing.T) {
	dir := setupBareInitRepo(t)

	result, err := writ.Init(context.Background(), dir, writ.InitOptions{
		Remotes:          []string{"nosuchremote"},
		StarterNamespace: "acme",
	})
	if err == nil {
		t.Fatal("Init with an explicit, unconfigured remote succeeded, want an error")
	}
	if len(result.Remotes) != 1 || result.Remotes[0].Skipped {
		t.Errorf("Remotes = %+v, want exactly one hard-failed (not skipped) entry", result.Remotes)
	}
	if got := getConfigAll(t, dir, "remote.nosuchremote.fetch"); len(got) != 0 {
		t.Errorf("remote.nosuchremote.fetch = %v, want no phantom section written", got)
	}
	// Identity is still persisted: a remote failure is reported as partial
	// state, not rolled back.
	if result.WriterID == "" {
		t.Errorf("WriterID unset after a remote failure, want identity to have already been persisted")
	}
}

func TestInitDiscoveredBadRemoteSkippedNotStranding(t *testing.T) {
	dir := setupBareInitRepo(t)
	runGitCmd(t, dir, "remote", "add", "origin", "https://example.com/origin.git")
	runGitCmd(t, dir, "config", "remote.ghost.prune", "true")

	result, err := writ.Init(context.Background(), dir, writ.InitOptions{StarterNamespace: "acme"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	var origin, ghost *writ.RemoteInit
	for i := range result.Remotes {
		switch result.Remotes[i].Name {
		case "origin":
			origin = &result.Remotes[i]
		case "ghost":
			ghost = &result.Remotes[i]
		}
	}
	if origin == nil || origin.Err != nil || origin.Refspec == "" {
		t.Errorf("origin = %+v, want a successfully configured entry", origin)
	}
	if ghost == nil || !ghost.Skipped {
		t.Errorf("ghost = %+v, want a skipped entry -- a url-less discovered remote must not strand origin", ghost)
	}
}

// getConfigAll returns every value git config reports for key in dir's
// local configuration, or nil if the key is unset.
func getConfigAll(t *testing.T, dir, key string) []string {
	t.Helper()
	cmd := exec.Command("git", "config", "--local", "--get-all", key)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var vals []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line != "" {
			vals = append(vals, line)
		}
	}
	return vals
}
