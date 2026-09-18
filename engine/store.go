package writ

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/identity"
	"github.com/writtendev/writ/internal/projection"
	writsync "github.com/writtendev/writ/internal/sync"
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

// Store is the top-level handle for interacting with schema-declared
// collaborative objects stored in a git repository.
type Store struct {
	// Objects provides generic create, apply, and get operations over
	// collaborative objects of any schema-declared type.
	Objects *Objects

	// Query provides read queries over collaborative objects.
	Query *Query

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
	closed      bool
	subscribers []*subscriber
	mu          sync.Mutex

	// trustSignersPath is gpg.ssh.allowedSignersFile resolved out of git
	// config exactly once, at Open (resolveTrustSignersPath): the one
	// point in a Store's lifetime that resolving it spawns a git
	// subprocess. Empty means unconfigured, or the resolve itself failed
	// (ruling 2 — never a reason to refuse opening). currentTrustStore
	// reads this path's file fresh on every call, but never re-resolves
	// the path itself (WRIT-251 round 2 perf finding: re-resolving it on
	// every Refresh/Rebuild/Get/Schema cost a git config --list
	// subprocess each time, about 13x main's whole no-op Query.Object
	// cost on a 3,000-op repo).
	trustSignersPath string
	// trustMu guards trustCache.
	trustMu sync.Mutex
	// trustCache memoizes the trust store parsed from trustSignersPath's
	// last observed content digest, so currentTrustStore only re-parses
	// when the file's content actually changed.
	trustCache trustCacheEntry

	// vocabMu guards vocabCache/vocabChains/vocabFingerprint/vocabObservedAt,
	// the memoised resolution of VocabulariesFromSchemas behind a dag.Chains
	// fingerprint (see Store.vocabularies and Store.noteAppend). Separate
	// from mu: resolving vocabularies must not contend with Refresh/
	// Rebuild's projection lock.
	vocabMu    sync.Mutex
	vocabCache codec.Vocabularies
	// vocabChains is the exact dag.Chains snapshot vocabCache was resolved
	// against, kept (not just its fingerprint string) so Store.noteAppend
	// can patch the one entry a local non-"schema" append just moved and
	// recompute the fingerprint from it, instead of paying for a fresh
	// Chains call plus a full Schema/Enumerate re-resolve on every append.
	vocabChains      map[string]dag.DiscoveredChain
	vocabFingerprint string
	// vocabObservedAt is the last time vocabCache was actually validated
	// against ground truth — a real dag.Chains pass, in Store.vocabularies,
	// win or lose against the fingerprint compare — not merely the last
	// time this cache was touched. It holds the clock at the point that
	// derive *completed*, not the clock read before its ref walk: the
	// window has to be armed from completion, or a derive costing longer
	// than vocabFreshnessWindow would arm nothing at all and the append
	// path would be back to a full ref walk per Append (WRIT-202 review
	// round 4 measured exactly that — see Store.vocabularies). The
	// consequence, documented everywhere the bound is stated rather than
	// engineered away: on the full-resolve branch this stamp trails the ref
	// read by a whole Schema()/Enumerate fold, so what the append path
	// enforces is the window plus at most one ground-truth resolve.
	// Store.noteAppend's non-"schema" branch
	// rolls the chain snapshot forward without ever calling dag.Chains, so
	// it must never update this stamp; only vocabularies' own two branches
	// (fingerprint hit and full resolve) do, because both actually
	// consulted the refs. Store.vocabulariesForAppend reads it to decide
	// whether the append path may skip dag.Chains entirely for a short,
	// documented window (vocabFreshnessWindow in schema.go).
	vocabObservedAt time.Time
	// now is the clock Store.vocabularies/vocabulariesForAppend read
	// through the s.clock() helper below. nil means time.Now; a test
	// injects a fake one via the export_test.go SetStoreClock seam so
	// freshness-window tests are deterministic rather than racing wall
	// time. Not an Open option deliberately (WRIT-202): the window itself
	// is one fixed, documented constant, not something a caller configures.
	now func() time.Time

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

// clock returns the time source Store.vocabularies and vocabulariesForAppend
// read for vocabObservedAt: s.now when a test has injected one via the
// export_test.go SetStoreClock seam, time.Now otherwise. No Open option
// wraps this on purpose (WRIT-202) — the freshness window is one fixed,
// documented constant, not caller-configurable.
func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
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
		if sub.stop != nil {
			sub.stop()
		}
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

// resolveRulesForProjection checks s.closed and, if the store is open,
// resolves the fold-rule index a Refresh/Rebuild pass needs — with s.mu
// released across the resolve. Store.rules delegates to Store.vocabularies,
// which costs a full dag.Chains ref walk plus, on a cache miss, a
// Schema()/Enumerate fold: none of that may run with mu held, or it blocks
// Close, Watch, and any concurrent Refresh/Rebuild for the duration. verb
// names the caller ("refresh" or "rebuild") for the resolve-failure error;
// the closed error is worded the same for both and matches the one the
// caller's own second closed-check (taken under mu, after this returns)
// reports if Close races the resolve.
func (s *Store) resolveRulesForProjection(ctx context.Context, verb string) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return fmt.Errorf("writ: store is closed")
	}

	if _, err := s.rules(ctx); err != nil {
		return fmt.Errorf("writ: %s projection: resolve rules: %w", verb, err)
	}
	return nil
}

// Refresh brings the projection cache up to date with the latest DAG operations and target code tips.
//
// Rule resolution runs before s.mu is taken for the projection pass itself:
// see resolveRulesForProjection. Only the projection pass and the emit that
// follows it hold mu.
func (s *Store) Refresh(ctx context.Context) (RefreshStats, error) {
	if s == nil {
		return RefreshStats{}, fmt.Errorf("writ: store is nil")
	}

	if err := s.resolveRulesForProjection(ctx, "refresh"); err != nil {
		return RefreshStats{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return RefreshStats{}, fmt.Errorf("writ: store is closed")
	}

	s.vocabMu.Lock()
	rules := s.ruleCache
	s.vocabMu.Unlock()

	// Trust store read fresh for this pass (WRIT-251 round 2 finding):
	// see currentTrustStore's doc comment for why this must not be the
	// one Open froze when s was constructed.
	ts, digest := s.currentTrustStore()
	opts := []projection.Option{projection.WithSchema(rules), projection.WithLiveTrustStore(ts, digest)}
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
// Local-only state (read marks, sync cursors) is preserved. The cache file may also simply be deleted.
//
// Rule resolution runs before s.mu is taken for the projection pass itself:
// see resolveRulesForProjection. Only the projection pass and the emit that
// follows it hold mu.
func (s *Store) Rebuild(ctx context.Context) (RefreshStats, error) {
	if s == nil {
		return RefreshStats{}, fmt.Errorf("writ: store is nil")
	}

	if err := s.resolveRulesForProjection(ctx, "rebuild"); err != nil {
		return RefreshStats{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return RefreshStats{}, fmt.Errorf("writ: store is closed")
	}

	s.vocabMu.Lock()
	rules := s.ruleCache
	s.vocabMu.Unlock()

	// Trust store read fresh for this pass (WRIT-251 round 2 finding):
	// see currentTrustStore's doc comment for why this must not be the
	// one Open froze when s was constructed.
	ts, digest := s.currentTrustStore()
	opts := []projection.Option{projection.WithSchema(rules), projection.WithLiveTrustStore(ts, digest)}
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
