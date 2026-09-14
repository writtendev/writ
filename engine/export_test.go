package writ

import (
	"context"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
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

// parkingStorer wraps the Storer Store.vocabularies runs its dag.Chains
// scan against, so a test can hold that scan still between the moment it
// reads the refs and the moment it reaches vocabMu. It intercepts exactly
// one IterReferences call — the token in armed — and is transparent for
// every other caller and every later call, so nothing else in the Store
// deadlocks behind it.
type parkingStorer struct {
	storage.Storer
	armed   chan struct{}
	parked  chan struct{}
	release chan struct{}
}

func (p *parkingStorer) IterReferences() (storer.ReferenceIter, error) {
	iter, err := p.Storer.IterReferences()
	if err != nil {
		return nil, err
	}
	select {
	case <-p.armed:
	default:
		return iter, nil
	}

	// Drain the live iterator before parking, and hand back a replay of
	// what it held: the caller must go on to fingerprint the refs as they
	// stood *before* whatever the test lands while this call is parked,
	// which is the whole point of the interleaving being reproduced.
	var refs []*plumbing.Reference
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		refs = append(refs, ref)
		return nil
	})
	iter.Close()
	if err != nil {
		return nil, err
	}

	close(p.parked)
	<-p.release
	return storer.NewReferenceSliceIter(refs), nil
}

// StoreParkNextChainsScan parks the next dag.Chains ref scan Store.vocabularies
// performs (WRIT-202 review round 5), after it has read the refs and before it
// can take vocabMu, and returns the three handles a test needs to drive that
// interleaving: parked closes once the scan is holding its pre-change ref
// snapshot, release lets it continue, and restore puts the real storer back.
//
// Test-only seam, and deliberately not a hook in the write path: it swaps the
// storage.Storer the Store reads through, a field that already exists, and the
// production code it parks is unaware of it. Only Store.vocabularies and
// Store.Sync read s.storer; Store.Schema, ApplySchema, and every Append go
// through s.dagStore's own storer, so a parked scan blocks nothing but itself.
func StoreParkNextChainsScan(s *Store) (parked <-chan struct{}, release func(), restore func()) {
	armed := make(chan struct{}, 1)
	armed <- struct{}{}
	parkedCh := make(chan struct{})
	releaseCh := make(chan struct{})

	real := s.storer
	s.storer = &parkingStorer{Storer: real, armed: armed, parked: parkedCh, release: releaseCh}

	var released bool
	return parkedCh, func() {
			if !released {
				released = true
				close(releaseCh)
			}
		}, func() {
			s.storer = real
		}
}
