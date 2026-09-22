package writ

import (
	"context"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/projection"
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
// one), without needing a real Append to exercise it. Store.vocabularies
// itself returns a vocabSnapshot (WRIT-238); this wrapper keeps returning
// just the vocabulary, its historical shape, so every caller of this seam
// predating that change stays unmodified.
func StoreVocabularies(s *Store, ctx context.Context) (codec.Vocabularies, error) {
	snap, err := s.vocabularies(ctx)
	if err != nil {
		return nil, err
	}
	return snap.vocab, nil
}

// StoreVocabulariesForAppend exposes Store.vocabulariesForAppend (WRIT-202)
// for testing the append path's freshness window directly — the same
// entry point dag.WithProducerVocabularies' resolver calls — without
// needing a real Append to exercise it.
func StoreVocabulariesForAppend(s *Store, ctx context.Context) (codec.Vocabularies, error) {
	return s.vocabulariesForAppend(ctx)
}

// StoreRules exposes Store.rules for testing that it hands back the
// vocabSnapshot a derive just produced rather than reading the cache back
// (WRIT-238 round 1, item 4): a derive whose write-back the generation
// check skips must still return its own freshly resolved rule table to
// this call's caller, never a cache a skipped write-back left stale or, on
// a store whose cache had never been populated, nil.
func StoreRules(s *Store, ctx context.Context) (map[string][]Rule, error) {
	return s.rules(ctx)
}

// StoreInvalidateVocabularies exposes Store.invalidateVocabularies for
// testing the Store.Sync half of the WRIT-238 generation-counter fix
// directly — that it bumps vocabGen so a derive already in flight when it
// runs cannot install a pre-invalidation snapshot over it — without needing
// a real fetch to drive Store.Sync.
func StoreInvalidateVocabularies(s *Store) {
	s.invalidateVocabularies()
}

// StoreVocabGen exposes Store.vocabGen for testing (WRIT-238): the
// generation counter Store.noteAppend's "schema" branch and
// Store.invalidateVocabularies increment, and that Store.vocabularies'
// write-back compares against before installing a derive's result.
// Test-only seam for pinning exactly which call sites bump it, and that no
// others do.
func StoreVocabGen(s *Store) uint64 {
	s.vocabMu.Lock()
	defer s.vocabMu.Unlock()
	return s.vocabGen
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

// StoreHoldRefreshLock takes the Store mutex Store.Refresh and
// Store.Rebuild must acquire for their closed check, the phase that runs
// before either resolves anything (WRIT-244 moved rule resolution outside
// this lock; the closed check that gates it still takes mu first, so this
// still parks Refresh/Rebuild before any resolve), and returns the release
// (WRIT-202). Test-only seam for the Sync-invalidation test: Store.Sync
// invalidates the append-path vocabularies cache and *then* calls
// Store.Refresh, whose own unconditional rules -> vocabularies pass
// re-resolves and re-stamps the freshness window regardless — so anything
// asserted after Sync has returned is satisfied by the Refresh and says
// nothing about the invalidation. Parking Refresh on this lock opens the
// gap between Sync's fetch and its Refresh wide enough for a test to look
// into it, which is the only place the invalidation is observable at all.
// Nothing on the vocabularies path takes this mutex, so
// StoreVocabulariesForAppend still runs while it is held.
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
