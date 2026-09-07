package writ

import (
	"context"

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
