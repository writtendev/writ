package dag

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/engine/codec"
)

var (
	// ErrInvalidParent is returned when a causal parent commit cannot be found in the repository.
	ErrInvalidParent = errors.New("dag: invalid causal parent")

	// ErrNonOpParent is returned when a causal parent commit does not contain op.json in its tree.
	ErrNonOpParent = errors.New("dag: parent is not a valid op commit (missing op.json)")

	// ErrCASExhausted is returned when atomic ref updates exceed the maximum retry count.
	ErrCASExhausted = errors.New("dag: CAS ref update retry limit exceeded")
)

const maxCASRetries = 16

// Append commits an operation onto the local writer's chain for env.ObjectType
// via a compare-and-swap ref update. parents[0] is automatically set to the writer's
// previous chain tip when non-empty, followed by any caller-supplied causal parent SHAs.
func (s *Store) Append(ctx context.Context, env codec.Envelope, causalParents []string) (*codec.Op, error) {
	if !objectTypeRegexp.MatchString(env.ObjectType) || len(env.ObjectType) > 64 {
		return nil, fmt.Errorf("dag: invalid object type %q", env.ObjectType)
	}

	// 1. Validate caller-supplied causal parents
	for _, p := range causalParents {
		pHash := plumbing.NewHash(p)
		if pHash.IsZero() {
			return nil, fmt.Errorf("%w: invalid hash %q", ErrInvalidParent, p)
		}
		commitObj, err := object.GetCommit(s.storer, pHash)
		if err != nil {
			return nil, fmt.Errorf("%w: commit %s: %v", ErrInvalidParent, p, err)
		}
		tree, err := commitObj.Tree()
		if err != nil {
			return nil, fmt.Errorf("%w: tree for commit %s: %v", ErrInvalidParent, p, err)
		}
		if _, err := tree.FindEntry("op.json"); err != nil {
			return nil, fmt.Errorf("%w: commit %s lacks op.json in tree", ErrNonOpParent, p)
		}
	}

	// 2. Serialize in-process appends
	s.mu.Lock()
	defer s.mu.Unlock()

	// Resolve the producer vocabularies once per Append, not once per
	// BuildCommit: BuildCommit runs inside the CAS loop below, up to
	// maxCASRetries times on one contended append, and a hook re-resolved
	// there would repeat the same log walk on every attempt
	// (BenchmarkAppendResolvesVocabulariesOnce pins this).
	//
	// Never for a "schema" op: spec/schema-ops.md §7's bootstrap exception
	// means object_type "schema" always validates against the engine's
	// built-in table, never the log (codec.validateProducerOp's
	// unconditional top-level check does not even consult vocabularies for
	// it), so resolving them here would only make schema bootstrap depend
	// on a log walk it has no use for — exactly the coupling the ruling
	// that carved out tier 4 (a permanent write outage is the failure mode
	// to avoid) would not tolerate for tier 1 either: a schema object that
	// fails to resolve must never block writing the very "schema" ops that
	// could fix it.
	var vocabularies codec.Vocabularies
	if s.resolveVocabularies != nil && env.ObjectType != "schema" {
		var err error
		vocabularies, err = s.resolveVocabularies()
		if err != nil {
			return nil, fmt.Errorf("dag: resolve producer vocabularies: %w", err)
		}
	}

	refName := LocalRefName(s.identity.WriterID, env.ObjectType)

	// 3. CAS loop
	for attempt := 0; attempt < maxCASRetries; attempt++ {
		oldRef, err := s.storer.Reference(refName)
		if err != nil && !errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, fmt.Errorf("dag: lookup ref %s: %w", refName, err)
		}

		var parents []string
		if oldRef != nil {
			parents = make([]string, 0, 1+len(causalParents))
			parents = append(parents, oldRef.Hash().String())
			parents = append(parents, causalParents...)
		} else {
			parents = make([]string, len(causalParents))
			copy(parents, causalParents)
		}

		author := codec.Identity{
			Name:  s.identity.Author.Name,
			Email: s.identity.Author.Email,
			When:  s.now(),
		}

		commit, err := codec.BuildCommit(env, author, parents, vocabularies)
		if err != nil {
			return nil, fmt.Errorf("dag: build commit: %w", err)
		}

		commitHash, err := codec.WriteCommit(ctx, s.storer, commit, s.signer)
		if err != nil {
			return nil, fmt.Errorf("dag: write commit: %w", err)
		}

		newRef := plumbing.NewHashReference(refName, commitHash)
		err = s.storer.CheckAndSetReference(newRef, oldRef)
		if err == nil {
			if s.chainObserver != nil {
				s.chainObserver(env.ObjectType, commitHash)
			}
			return &codec.Op{
				Envelope:  env,
				ID:        commitHash.String(),
				Parents:   parents,
				Author:    commit.Author,
				Committer: commit.Committer,
				Message:   commit.Message,
				Signature: commit.Signature,
			}, nil
		}

		if !errors.Is(err, storage.ErrReferenceHasChanged) {
			return nil, fmt.Errorf("dag: set ref %s: %w", refName, err)
		}
	}

	return nil, ErrCASExhausted
}
