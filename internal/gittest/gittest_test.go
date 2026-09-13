package gittest

import (
	"bytes"
	"os/exec"
	"testing"
)

// TestDisableAutoMaintenance proves the config actually reaches a child git
// process, not just os.Getenv in this process. A wrong GIT_CONFIG_COUNT or
// mismatched index silently disables the whole fix with no error from git,
// so this asserts on the values a freshly-initialized repo's own git config
// reports back.
func TestDisableAutoMaintenance(t *testing.T) {
	DisableAutoMaintenance()

	dir := t.TempDir()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	gcAuto := gitConfigGet(t, dir, "gc.auto")
	if gcAuto != "0" {
		t.Errorf("git config --get gc.auto = %q, want %q", gcAuto, "0")
	}

	maintenanceAuto := gitConfigGet(t, dir, "maintenance.auto")
	if maintenanceAuto != "false" {
		t.Errorf("git config --get maintenance.auto = %q, want %q", maintenanceAuto, "false")
	}
}

func gitConfigGet(t *testing.T, repoDir, key string) string {
	t.Helper()
	cmd := exec.Command("git", "config", "--get", key)
	cmd.Dir = repoDir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git config --get %s: %v", key, err)
	}
	return string(bytes.TrimSpace(out.Bytes()))
}
