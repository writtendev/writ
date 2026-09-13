package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping test")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not on PATH; skipping test")
	}
}

type testCLIEnv struct {
	repoDir       string
	globalCfgPath string
}

func setupTestCLIEnv(t *testing.T) testCLIEnv {
	t.Helper()
	requireGit(t)

	tempDir := t.TempDir()
	globalCfgPath := filepath.Join(tempDir, "global_gitconfig")
	if err := os.WriteFile(globalCfgPath, []byte(""), 0600); err != nil {
		t.Fatalf("writing empty global config: %v", err)
	}

	repoDir := filepath.Join(tempDir, "repo")
	if err := os.Mkdir(repoDir, 0755); err != nil {
		t.Fatalf("creating repo dir: %v", err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v (%s)", err, string(out))
	}

	t.Setenv("GIT_CONFIG_GLOBAL", globalCfgPath)
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	return testCLIEnv{
		repoDir:       repoDir,
		globalCfgPath: globalCfgPath,
	}
}

// initBareRepo creates a bare repository at dir with go-git.
//
// go-git's PlainInit does not read GIT_TEMPLATE_DIR, so the repository misses
// the auto-maintenance config gittest.DisableAutoMaintenance installs through
// the template, and its config has to be written in directly. receive-pack
// reads the config of the repository it is pushed into, not the pusher's:
// without these keys every push into such a remote leaves a detached
// git maintenance child still able to write there after the test returns.
func initBareRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := git.PlainInit(dir, true); err != nil {
		t.Fatalf("PlainInit bare %s: %v", dir, err)
	}
	setGitConfig(t, dir, "gc.auto", "0")
	setGitConfig(t, dir, "maintenance.auto", "false")
}

func setGitConfig(t *testing.T, dir, key, val string) {
	t.Helper()
	cmd := exec.Command("git", "config", key, val)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config %s %s failed: %v (%s)", key, val, err, string(out))
	}
}

func setFileConfig(t *testing.T, filePath, key, val string) {
	t.Helper()
	cmd := exec.Command("git", "config", "--file", filePath, key, val)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config --file %s %s %s failed: %v (%s)", filePath, key, val, err, string(out))
	}
}

func addRemote(t *testing.T, dir, name, url string) {
	t.Helper()
	cmd := exec.Command("git", "remote", "add", name, url)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add %s %s failed: %v (%s)", name, url, err, string(out))
	}
}

func getGitConfigAll(t *testing.T, dir, key string) []string {
	t.Helper()
	cmd := exec.Command("git", "config", "--get-all", key)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if err != nil && len(out) == 0 {
			return nil
		}
		_ = exitErr
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var res []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			res = append(res, trimmed)
		}
	}
	return res
}

func setupSigningKey(t *testing.T, dir string) {
	t.Helper()
	requireGit(t)

	keyDir := t.TempDir()
	keyPath := filepath.Join(keyDir, "id_ed25519")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", keyPath, "-C", "test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen ed25519: %v (%s)", err, string(out))
	}

	setGitConfig(t, dir, "gpg.format", "ssh")
	setGitConfig(t, dir, "user.signingKey", keyPath)
	if len(getGitConfigAll(t, dir, "user.name")) == 0 {
		setGitConfig(t, dir, "user.name", "Alice")
	}
	if len(getGitConfigAll(t, dir, "user.email")) == 0 {
		setGitConfig(t, dir, "user.email", "alice@example.com")
	}
}

func commitFile(t *testing.T, dir, filename, content, message string) string {
	t.Helper()
	fullPath := filepath.Join(dir, filename)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		t.Fatalf("mkdir for commitFile: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("writeFile for commitFile: %v", err)
	}
	cmd := exec.Command("git", "add", filename)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add in commitFile: %v (%s)", err, string(out))
	}
	cmd = exec.Command("git", "-c", "user.name=Alice", "-c", "user.email=alice@example.com", "commit", "-m", message)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit in commitFile: %v (%s)", err, string(out))
	}
	cmd = exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in commitFile: %v", err)
	}
	return strings.TrimSpace(string(out))
}
