package writ

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/projection"
	writsync "github.com/writtendev/writ/engine/sync"
)

// Writer represents the active writer identity.
type Writer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	// PersonID is this writer's person identifier per spec/identifiers.md:
	// writ.personId when configured, otherwise email:<normalized user.email>.
	// ID partitions the git refspace; PersonID names the collaborative actor,
	// and the two are never interchangeable — a writer-id has no scheme.
	PersonID string `json:"person_id,omitempty"`
	// PersonIDErr says why PersonID is empty, and is nil when it is not.
	// Callers that need a person identifier report this rather than inventing
	// their own diagnosis: "writ.personId is not a person identifier" and
	// "nothing to derive one from" are different problems with different fixes.
	PersonIDErr error `json:"-"`
}

// Signer is an alias for codec.Signer.
type Signer = codec.Signer

// Store is the top-level handle for interacting with collaborative SDLC objects
// stored in a git repository.
type Store struct {
	// Reviews provides review creation, revision push, approval, and status operations.
	Reviews *Reviews

	// Issues provides issue creation, state transitions, assignments, labels, and links.
	Issues *Issues

	// Comments provides comment edits, deletions, and reply operations.
	Comments *Comments

	// Objects provides generic create, apply, and get operations over
	// collaborative objects of any schema-declared type — the schema-shaped
	// replacement for the typed per-type services above.
	Objects *Objects

	// Drafts provides local comment draft creation, updates, listing, discarding, and publishing.
	Drafts *Drafts

	// ReadState provides local read/unread tracking across collaborative objects.
	ReadState *ReadState

	// Query provides read queries over reviews, issues, comments, threads, and objects.
	Query *Query

	// WorkflowStates provides workflow state creation, updates, and default seeding.
	WorkflowStates *WorkflowStates

	// Labels provides label creation and updates.
	Labels *Labels

	// Documents provides document and section operations.
	Documents *Documents

	// Settings provides workspace settings retrieval and updates.
	Settings *SettingsService

	gitInfo     GitDirInfo
	storer      storage.Storer
	dagStore    *dag.Store
	projection  *projection.DB
	syncClient  *writsync.Client
	identity    identity.Identity
	hasIdentity bool
	identErr    error
	signer      codec.Signer
	hasSigner   bool
	signerErr   error
	autoRefresh bool
	targetRefs  []string
	localRepoID string
	closed      bool
	subscribers []*subscriber
	mu          sync.Mutex

	// vocabMu guards vocabCache/vocabChains/vocabFingerprint, the memoised
	// resolution of VocabulariesFromSchemas behind a dag.Chains fingerprint
	// (see Store.vocabularies and Store.noteAppend). Separate from mu:
	// resolving vocabularies must not contend with Refresh/Rebuild's
	// projection lock.
	vocabMu    sync.Mutex
	vocabCache codec.Vocabularies
	// vocabChains is the exact dag.Chains snapshot vocabCache was resolved
	// against, kept (not just its fingerprint string) so Store.noteAppend
	// can patch the one entry a local non-"schema" append just moved and
	// recompute the fingerprint from it, instead of paying for a fresh
	// Chains call plus a full Schema/Enumerate re-resolve on every append.
	vocabChains      map[string]dag.DiscoveredChain
	vocabFingerprint string

	// ruleCache is the fold-rule counterpart to vocabCache: the built-in
	// vocabulary overlaid by whatever the log declares (RulesFromSchemas),
	// log wins per type. It is recomputed in the same cache-miss branch as
	// vocabCache and typesCache (Store.vocabularies), behind the same
	// dag.Chains fingerprint, so none of the three ever costs a second
	// Schema()/Enumerate fold — that part is genuinely shared. What is not
	// shared: vocabCache, ruleCache, and typesCache are three separate calls
	// into resolveSchemaTypes (VocabulariesFromSchemas, RulesFromSchemas,
	// and a direct call, respectively), each re-walking the same already-
	// folded schemas slice — three passes, not one thin projection of a
	// single pass. resolveSchemaTypes is pure and cheap relative to the
	// Schema()/Enumerate it is fed from (WRIT-192 round 2's benchmarks: a
	// third pass costs low single-digit percent at the miss, nothing
	// measurable at the hit), which is why this was left as three calls
	// rather than restructured into one — but that is a cost judgement, not
	// a description of what the code does.
	ruleCache map[string][]Rule
	// typesCache is resolveSchemaTypes's own result, recomputed in the same
	// cache-miss branch as vocabCache and ruleCache: Store.Types builds
	// SchemaType values straight from it (Name, Fields, Ops, Description,
	// Deprecated), which fields/ops-only ruleCache cannot carry.
	typesCache resolvedSchemaTypes
}

// Close closes the underlying projection database and releases associated resources.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	for _, sub := range s.subscribers {
		close(sub.ch)
	}
	s.subscribers = nil

	var errs []error
	if s.projection != nil {
		if err := s.projection.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Ref returns the fully-qualified reference string (<local-repo-id>#<object-id>) for a local
// object ID when a local repo-id is known, or the bare objectID otherwise.
func (s *Store) Ref(objectID string) string {
	if s == nil || objectID == "" {
		return objectID
	}
	if s.localRepoID != "" {
		return s.localRepoID + "#" + objectID
	}
	return objectID
}

// Refresh brings the projection cache up to date with the latest DAG operations and target code tips.
func (s *Store) Refresh(ctx context.Context) (RefreshStats, error) {
	if s == nil {
		return RefreshStats{}, fmt.Errorf("writ: store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return RefreshStats{}, fmt.Errorf("writ: store is closed")
	}

	rules, err := s.rules(ctx)
	if err != nil {
		return RefreshStats{}, fmt.Errorf("writ: refresh projection: resolve rules: %w", err)
	}

	opts := []projection.Option{projection.WithSchema(rules)}
	if len(s.targetRefs) > 0 {
		opts = append(opts, projection.WithTargetRefs(s.targetRefs...))
	}

	stats, err := s.projection.Refresh(s.dagStore, opts...)
	if err != nil {
		return RefreshStats{}, fmt.Errorf("writ: refresh projection: %w", err)
	}

	s.emitLocked(stats)

	return RefreshStats(stats), nil
}

// Rebuild completely discards and recreates the folded projection cache from a cold walk of all writ chains.
// Local-only state (drafts, read marks, sync cursors) is preserved. The cache file may also simply be deleted.
func (s *Store) Rebuild(ctx context.Context) (RefreshStats, error) {
	if s == nil {
		return RefreshStats{}, fmt.Errorf("writ: store is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return RefreshStats{}, fmt.Errorf("writ: store is closed")
	}

	rules, err := s.rules(ctx)
	if err != nil {
		return RefreshStats{}, fmt.Errorf("writ: rebuild projection: resolve rules: %w", err)
	}

	opts := []projection.Option{projection.WithSchema(rules)}
	if len(s.targetRefs) > 0 {
		opts = append(opts, projection.WithTargetRefs(s.targetRefs...))
	}

	stats, err := s.projection.Rebuild(s.dagStore, opts...)
	if err != nil {
		return RefreshStats{}, fmt.Errorf("writ: rebuild projection: %w", err)
	}

	s.emitLocked(stats)

	return RefreshStats(stats), nil
}

// Writer returns the active writer identity.
func (s *Store) Writer() Writer {
	if s == nil {
		return Writer{}
	}
	return Writer{
		ID:          string(s.identity.WriterID),
		Name:        s.identity.Author.Name,
		Email:       s.identity.Author.Email,
		PersonID:    s.identity.PersonID,
		PersonIDErr: s.identity.PersonIDErr,
	}
}

func (s *Store) ensureWritable() error {
	if !s.hasIdentity || s.identErr != nil {
		return ErrNoIdentity
	}
	if !s.hasSigner || s.signerErr != nil {
		return ErrNoSigningKey
	}
	return nil
}

func (s *Store) maybeAutoRefresh(ctx context.Context) error {
	if !s.autoRefresh {
		return nil
	}
	_, err := s.Refresh(ctx)
	return err
}
