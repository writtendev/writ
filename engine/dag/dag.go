// Package dag implements the DAG store for Writ operations, handling append
// operations onto local writer chains and enumeration across all writer chains.
package dag

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/internal/gitdir"
)

// Signer is an alias for codec.Signer.
type Signer = codec.Signer

// SignerFunc is an alias for codec.SignerFunc.
type SignerFunc = codec.SignerFunc

// Option configures a Store instance during Open.
type Option func(*Store)

// WithSigner sets the cryptographic signer used for op commits.
func WithSigner(signer Signer) Option {
	return func(s *Store) {
		s.signer = signer
	}
}

// WithNow sets the time function used for commit timestamps (used in tests).
func WithNow(now func() time.Time) Option {
	return func(s *Store) {
		if now != nil {
			s.now = now
		}
	}
}

// WithProducerVocabularies configures the resolver Append consults for the
// log-sourced producer vocabularies (spec/op-envelope.md §Producer
// validation, tier 2): resolved once per Append, before the CAS retry
// loop, not once per BuildCommit — BuildCommit runs inside that loop's up
// to maxCASRetries attempts, and a hook invoked there would re-resolve on
// every contended attempt.
//
// A nil resolver is the default: engine/scenario's test runner installs
// none, so Append always passes nil vocabularies to BuildCommit, meaning
// "the log declares nothing" — legal, and correct for a harness that only
// ever writes embedded types.
func WithProducerVocabularies(resolve func() (codec.Vocabularies, error)) Option {
	return func(s *Store) {
		s.resolveVocabularies = resolve
	}
}

// WithChainObserver configures a callback Append invokes once, after a
// successful append, naming the object type just appended and the new tip
// commit hash of the local writer chain that moved.
//
// It exists solely so a caller (writ.Store) can roll a log-derived cache —
// the producer-vocabularies fingerprint over dag.Chains — forward
// incrementally instead of re-deriving it with a fresh IterReferences pass
// on every append: Append already knows exactly which ref moved and to
// what, and handing that to the observer directly is cheaper than making
// the caller re-discover it. Nothing else should depend on this hook; it
// is not a general append-notification mechanism.
//
// A nil observer is the default and costs nothing: engine/scenario's test
// runner installs none.
func WithChainObserver(observe func(objectType string, newTip plumbing.Hash)) Option {
	return func(s *Store) {
		s.chainObserver = observe
	}
}

// Store provides atomic op appends onto local writer chains and multi-writer
// chain enumeration over a git repository.
type Store struct {
	repoDir             string
	storer              storage.Storer
	identity            identity.Identity
	signer              Signer
	now                 func() time.Time
	resolveVocabularies func() (codec.Vocabularies, error)
	chainObserver       func(objectType string, newTip plumbing.Hash)
	// mu serializes Append (spec/schema-ops.md §7's CAS loop is not
	// otherwise safe for concurrent callers). It is held across
	// resolveVocabularies's callback for every non-"schema" op (Append),
	// and that callback re-enters this same Store's Enumerate/
	// EnumerateSince (writ.Store.vocabularies -> writ.Store.Schema ->
	// dag.Store.Enumerate) when its cache misses. That re-entrant call is
	// safe only because Enumerate and EnumerateSince never take mu
	// themselves — if either ever does, every cold-cache non-"schema"
	// Append deadlocks. Nothing else enforces this; keep it that way, or
	// restructure Append so resolveVocabularies runs outside the lock.
	mu sync.Mutex
}

// Open opens a git repository at repoDir and initializes a Store with the given identity.
func Open(repoDir string, ident identity.Identity, opts ...Option) (*Store, error) {
	info, err := gitdir.Resolve(repoDir)
	if err != nil {
		return nil, fmt.Errorf("dag: open repo %s: %w", repoDir, err)
	}
	storer, err := gitdir.OpenStorage(info)
	if err != nil {
		return nil, fmt.Errorf("dag: open repo %s: %w", repoDir, err)
	}
	s := &Store{
		repoDir:  repoDir,
		storer:   storer,
		identity: ident,
		now:      func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// OpenStorage initializes a Store with a storage.Storer.
func OpenStorage(s storage.Storer, ident identity.Identity, opts ...Option) (*Store, error) {
	if s == nil {
		return nil, fmt.Errorf("dag: nil storer")
	}
	store := &Store{
		storer:   s,
		identity: ident,
		now:      func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(store)
	}
	return store, nil
}

// OpenRepo initializes a Store with an existing go-git repository instance.
func OpenRepo(repo *git.Repository, ident identity.Identity, opts ...Option) (*Store, error) {
	if repo == nil {
		return nil, fmt.Errorf("dag: nil repo")
	}
	return OpenStorage(repo.Storer, ident, opts...)
}

// Storer returns the underlying storage.Storer.
func (s *Store) Storer() storage.Storer {
	return s.storer
}

// Identity returns the configured writer identity.
func (s *Store) Identity() identity.Identity {
	return s.identity
}

// WriterID returns the writer ID for the local store.
func (s *Store) WriterID() identity.WriterID {
	return s.identity.WriterID
}
