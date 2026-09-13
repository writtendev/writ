// Package gittest provides test-harness setup shared by every package that
// shells out to system git during tests.
package gittest

import (
	"fmt"
	"os"
	"path/filepath"
)

// AutoMaintenanceConfig is the git config that keeps detached auto-maintenance
// out of a repository. It is exported so a test creating a repository system
// git did not create — go-git's PlainInit ignores templates — can write the
// same two keys into it.
const AutoMaintenanceConfig = "[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n"

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
	os.Setenv("GIT_CONFIG_COUNT", "2")
	os.Setenv("GIT_CONFIG_KEY_0", "gc.auto")
	os.Setenv("GIT_CONFIG_VALUE_0", "0")
	os.Setenv("GIT_CONFIG_KEY_1", "maintenance.auto")
	os.Setenv("GIT_CONFIG_VALUE_1", "false")

	dir, err := os.MkdirTemp("", "writ-gittest-template")
	if err != nil {
		panic(fmt.Sprintf("gittest: creating git template dir: %v", err))
	}
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(AutoMaintenanceConfig), 0o644); err != nil {
		panic(fmt.Sprintf("gittest: writing git template config: %v", err))
	}
	os.Setenv("GIT_TEMPLATE_DIR", dir)

	return func() { os.RemoveAll(dir) }
}
