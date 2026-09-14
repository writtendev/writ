// Package gittest provides test-harness setup shared by every package that
// shells out to system git during tests.
package gittest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// autoMaintenanceConfig is the git config that keeps detached auto-maintenance
// out of a repository, and the only spelling of it: the template file, the
// environment block, and WriteAutoMaintenanceConfig are all derived from this
// slice, so a key added here reaches every repository however it was created.
var autoMaintenanceConfig = [][2]string{
	{"gc.auto", "0"},
	{"maintenance.auto", "false"},
}

// WriteAutoMaintenanceConfig writes the auto-maintenance config into the local
// config of the already-initialised repository at gitDir — for a bare
// repository, the repository directory itself.
//
// Tests need this for repositories system git did not create: go-git's
// PlainInit ignores GIT_TEMPLATE_DIR, so such a repository misses the config
// DisableAutoMaintenance installs through the template. receive-pack reads the
// config of the repository it is pushed into, not the pusher's, so without
// these keys every push into such a remote leaves a detached git maintenance
// child still able to write there after the test returns.
func WriteAutoMaintenanceConfig(gitDir string) error {
	for _, kv := range autoMaintenanceConfig {
		cmd := exec.Command("git", "config", kv[0], kv[1])
		cmd.Dir = gitDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git config %s %s in %s: %w (%s)", kv[0], kv[1], gitDir, err, out)
		}
	}
	return nil
}

// DisableAutoMaintenance stops git's detached auto-maintenance
// (git maintenance run --auto --detach) from writing into a test repository
// after the test returns and t.TempDir cleanup has started. It returns a
// cleanup function the caller runs once the test binary is done.
//
// It uses two channels, because neither reaches the other's repositories:
//
//   - GIT_TEMPLATE_DIR puts the config in the config file of every repository
//     system git creates in this process, bare remotes included. This is the
//     channel that reaches receive-pack and upload-pack: GIT_CONFIG_COUNT is
//     in git's local_repo_env, so git strips it when it spawns a command into
//     another repository, and a bare remote's receive-pack — the thing that
//     kicks maintenance after a push — would otherwise see nothing at all.
//   - GIT_CONFIG_COUNT covers git commands run inside a repository this
//     process did not create with git, for as long as they stay in it.
//     GIT_CONFIG_COUNT and GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n require git >=
//     2.31 (2021); CI runs a far newer git than that.
//
// Set with os.Setenv, not t.Setenv, so it is process-wide for the whole test
// binary and stays valid if a test ever calls t.Parallel(). This function owns
// GIT_CONFIG_COUNT and GIT_TEMPLATE_DIR for the entire binary — nothing else
// in the process should set either.
func DisableAutoMaintenance() (cleanup func()) {
	os.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(len(autoMaintenanceConfig)))
	var template strings.Builder
	for i, kv := range autoMaintenanceConfig {
		os.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", i), kv[0])
		os.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", i), kv[1])
		section, key, _ := strings.Cut(kv[0], ".")
		fmt.Fprintf(&template, "[%s]\n\t%s = %s\n", section, key, kv[1])
	}

	dir, err := os.MkdirTemp("", "writ-gittest-template")
	if err != nil {
		panic(fmt.Sprintf("gittest: creating git template dir: %v", err))
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(template.String()), 0o644); err != nil {
		panic(fmt.Sprintf("gittest: writing git template config: %v", err))
	}
	os.Setenv("GIT_TEMPLATE_DIR", dir)

	return func() { os.RemoveAll(dir) }
}
