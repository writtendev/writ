package dag

import "testing"

// SetMaxCommitParentsForTest overrides maxCommitParents for the duration of
// a test, restoring the original value on cleanup.
func SetMaxCommitParentsForTest(t testing.TB, limit int) {
	t.Helper()
	old := maxCommitParents
	maxCommitParents = limit
	t.Cleanup(func() {
		maxCommitParents = old
	})
}
