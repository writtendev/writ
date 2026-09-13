package gittest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDisableAutoMaintenanceAcrossRepoBoundary pushes into a bare remote and
// asserts that receive-pack started no detached maintenance child.
//
// The cross-repository case is the one worth guarding. Git strips
// GIT_CONFIG_COUNT from the environment when it spawns a command into another
// repository, so config delivered that way never reaches receive-pack, and a
// guard that only runs git inside one repository passes with that hole wide
// open. Counting maintenance children is also the only sound assertion:
// receive-pack logs trace2 def_param for the config of the directory it
// started in — the pusher's repository — so seeing gc.auto there says nothing
// about what the remote it writes into is configured with.
func TestDisableAutoMaintenanceAcrossRepoBoundary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping test")
	}
	t.Cleanup(DisableAutoMaintenance())

	dir := t.TempDir()
	remoteDir := filepath.Join(dir, "remote.git")
	workDir := filepath.Join(dir, "work")
	traceDir := filepath.Join(dir, "trace")
	if err := os.Mkdir(traceDir, 0o755); err != nil {
		t.Fatalf("mkdir trace: %v", err)
	}

	runGit(t, dir, "init", "--bare", "--initial-branch=main", remoteDir)
	runGit(t, dir, "init", "--initial-branch=main", workDir)
	runGit(t, workDir, "config", "user.name", "Tester")
	runGit(t, workDir, "config", "user.email", "tester@example.com")
	runGit(t, workDir, "remote", "add", "origin", remoteDir)
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("# t\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, workDir, "add", "README.md")
	runGit(t, workDir, "commit", "-m", "initial commit")

	// The remote's own config file is what receive-pack reads, so assert on
	// that specifically — --local, because a plain --get would also report
	// values this process supplied through the environment, which is exactly
	// what does not survive the repository boundary.
	if got := gitConfigLocal(t, remoteDir, "gc.auto"); got != "0" {
		t.Errorf("remote's own gc.auto = %q, want %q", got, "0")
	}
	if got := gitConfigLocal(t, remoteDir, "maintenance.auto"); got != "false" {
		t.Errorf("remote's own maintenance.auto = %q, want %q", got, "false")
	}

	push := exec.Command("git", "push", "origin", "HEAD:main")
	push.Dir = workDir
	push.Env = append(os.Environ(), "GIT_TRACE2_EVENT="+traceDir)
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("git push: %v\n%s", err, out)
	}

	if n := maintenanceChildren(t, traceDir); n != 0 {
		t.Errorf("push started %d git maintenance child process(es), want 0", n)
	}
}

// maintenanceChildren counts the "git maintenance" children recorded in a
// trace2 event directory. The count comes from the child_start events of the
// processes that spawn them, which are written before the push returns, so it
// does not race the detached child's own trace file.
func maintenanceChildren(t *testing.T, traceDir string) int {
	t.Helper()
	entries, err := os.ReadDir(traceDir)
	if err != nil {
		t.Fatalf("read trace dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no trace2 events recorded; the count below would be vacuous")
	}
	n := 0
	for _, entry := range entries {
		raw, err := os.ReadFile(filepath.Join(traceDir, entry.Name()))
		if err != nil {
			t.Fatalf("read trace file: %v", err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if line == "" {
				continue
			}
			var ev struct {
				Event string   `json:"event"`
				Argv  []string `json:"argv"`
			}
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				continue
			}
			if ev.Event == "child_start" && len(ev.Argv) > 1 && ev.Argv[1] == "maintenance" {
				n++
			}
		}
	}
	return n
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitConfigLocal(t *testing.T, repoDir, key string) string {
	t.Helper()
	cmd := exec.Command("git", "config", "--local", "--get", key)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git config --local --get %s in %s: %v", key, repoDir, err)
	}
	return strings.TrimSpace(string(out))
}
