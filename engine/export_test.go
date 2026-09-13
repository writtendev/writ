package writ

import (
	"context"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/projection"
)

// StoreDAGStore returns the underlying dag.Store for testing.
func StoreDAGStore(s *Store) *dag.Store {
	return s.dagStore
}

// StoreProjection returns the underlying projection.DB for testing.
func StoreProjection(s *Store) *projection.DB {
	return s.projection
}

// StoreVocabularies exposes Store.vocabularies for testing and benchmarking
// the producer-vocabularies cache directly (its hit/miss cost, and that a
// hit returns the exact same map instance rather than a freshly resolved
// one), without needing a real Append to exercise it.
func StoreVocabularies(s *Store, ctx context.Context) (codec.Vocabularies, error) {
	return s.vocabularies(ctx)
}

// StoreVocabulariesForAppend exposes Store.vocabulariesForAppend (WRIT-202)
// for testing the append path's freshness window directly — the same
// entry point dag.WithProducerVocabularies' resolver calls — without
// needing a real Append to exercise it.
func StoreVocabulariesForAppend(s *Store, ctx context.Context) (codec.Vocabularies, error) {
	return s.vocabulariesForAppend(ctx)
}

// SetStoreClock injects a fake clock for Store.vocabularies/
// vocabulariesForAppend to read via Store.clock (WRIT-202), so a test can
// freeze or advance time deterministically rather than racing
// vocabFreshnessWindow's wall-clock duration. Test-only seam: there is no
// exported Open option for this, by design (see vocabFreshnessWindow's
// doc comment in schema.go).
func SetStoreClock(s *Store, now func() time.Time) {
	s.now = now
}

// StoreHoldRefreshLock takes the Store mutex Store.Refresh must acquire
// before it can resolve anything, and returns the release (WRIT-202).
// Test-only seam for the Sync-invalidation test: Store.Sync invalidates
// the append-path vocabularies cache and *then* calls Store.Refresh, whose
// own unconditional rules -> vocabularies pass re-resolves and re-stamps
// the freshness window regardless — so anything asserted after Sync has
// returned is satisfied by the Refresh and says nothing about the
// invalidation. Parking Refresh on this lock opens the gap between Sync's
// fetch and its Refresh wide enough for a test to look into it, which is
// the only place the invalidation is observable at all. Nothing on the
// vocabularies path takes this mutex, so StoreVocabulariesForAppend still
// runs while it is held.
func StoreHoldRefreshLock(s *Store) (release func()) {
	s.mu.Lock()
	return s.mu.Unlock
}
