// Package gittest provides test-harness setup shared by every package that
// shells out to system git during tests.
package gittest

import "os"

// DisableAutoMaintenance stops git's detached auto-maintenance from writing
// into a test repository's .git after the test returns and t.TempDir cleanup
// has started. Set as GIT_CONFIG_* rather than repo config so it reaches
// every git subprocess — including the fetch/push children the engine spawns
// and receive-pack on a bare test remote — whichever way the repo was made.
//
// GIT_CONFIG_COUNT and GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n require git >=
// 2.31 (2021); CI runs a far newer git than that.
//
// Set with os.Setenv, not t.Setenv, so it is process-wide for the whole test
// binary and stays valid if a test ever calls t.Parallel(). This function
// owns GIT_CONFIG_COUNT for the entire binary — nothing else in the process
// should set it.
func DisableAutoMaintenance() {
	os.Setenv("GIT_CONFIG_COUNT", "2")
	os.Setenv("GIT_CONFIG_KEY_0", "gc.auto")
	os.Setenv("GIT_CONFIG_VALUE_0", "0")
	os.Setenv("GIT_CONFIG_KEY_1", "maintenance.auto")
	os.Setenv("GIT_CONFIG_VALUE_1", "false")
}
