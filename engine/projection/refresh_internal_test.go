package projection

import (
	"database/sql"
	"errors"
	"testing"
)

// TestIncrementalSeenPredicateSurfacesGenuineError pins round 1's WRIT-273
// finding: newIncrementalSeenPredicate's seen func can only return bool, so
// an ordinary "not seen" miss (sql.ErrNoRows) and a genuine query failure
// would otherwise look identical to EnumerateSince, which treats either as
// license to walk that commit's ancestry cold instead of trusting the
// already-projected stop point (see incrementalSeenOption's doc comment).
// That fallback is safe but silent, so a real failure — sustained
// SQLITE_BUSY past busy_timeout, here stood in for by closing the database
// out from under an already-prepared statement — must surface through the
// close func instead of masquerading as a normal cache miss.
func TestIncrementalSeenPredicateSurfacesGenuineError(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Ordinary miss: no op_id has been recorded yet. sql.ErrNoRows must
	// not surface as an error -- that is the expected "not seen" outcome,
	// matching refresh.go's dbType lookup and projection.go's
	// loadMetaBool.
	stmt, err := db.db.Prepare("SELECT 1 FROM ops WHERE op_id = ?")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	seen, closeSeen := newIncrementalSeenPredicate(stmt)
	if seen("nonexistent") {
		t.Fatal("seen reported true for an op_id never recorded")
	}
	if err := closeSeen(); err != nil {
		t.Fatalf("closeSeen reported an error for an ordinary not-seen miss: %v", err)
	}

	// Genuine failure: close the underlying database out from under a
	// freshly prepared statement, so its QueryRow fails with a real
	// driver error rather than sql.ErrNoRows.
	stmt2, err := db.db.Prepare("SELECT 1 FROM ops WHERE op_id = ?")
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	seen2, closeSeen2 := newIncrementalSeenPredicate(stmt2)
	if err := db.db.Close(); err != nil {
		t.Fatalf("closing underlying database: %v", err)
	}

	if seen2("whatever") {
		t.Fatal("seen must return false even on a genuine query failure -- its bool signature cannot report an error itself")
	}
	err = closeSeen2()
	if err == nil {
		t.Fatal("closeSeen silently swallowed the genuine database failure instead of surfacing it")
	}
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("closeSeen reported the genuine failure as an ordinary not-seen miss: %v", err)
	}
}
