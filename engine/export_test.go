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

// NeedsLogVocabularies exposes checkBeforeAppend's own decision of whether
// a multi-append sequence has any use for a log-sourced vocabularies
// resolution, for testing: a sequence made entirely of "schema" envelopes
// must report false, since "schema" always validates against the engine's
// built-in bootstrap table (spec/schema-ops.md §7), never the log.
func NeedsLogVocabularies(envs []codec.Envelope) bool {
	return needsLogVocabularies(envs)
}

// StoreVocabularies exposes Store.vocabularies for testing and benchmarking
// the producer-vocabularies cache directly (its hit/miss cost, and that a
// hit returns the exact same map instance rather than a freshly resolved
// one), without needing a real Append to exercise it.
func StoreVocabularies(s *Store, ctx context.Context) (codec.Vocabularies, error) {
	return s.vocabularies(ctx)
}
