package writ

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/sshsig"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/projection"
	writsync "github.com/writtendev/writ/engine/sync"
	"github.com/writtendev/writ/internal/gitdir"
)

type openConfig struct {
	cacheDir    string
	signer      codec.Signer
	autoRefresh bool
	gitBin      string
	targetRefs  []string
}

// Option configures an Open invocation.
type Option func(*openConfig)

// WithCacheDir configures an explicit custom directory for the SQLite projection cache.
func WithCacheDir(dir string) Option {
	return func(c *openConfig) {
		c.cacheDir = dir
	}
}

// WithSigner configures an explicit Signer for operation commits.
func WithSigner(signer Signer) Option {
	return func(c *openConfig) {
		c.signer = signer
	}
}

// WithoutAutoRefresh disables automatic projection refresh before and after operations,
// allowing hot loops to manage refresh explicitly via Store.Refresh.
func WithoutAutoRefresh() Option {
	return func(c *openConfig) {
		c.autoRefresh = false
	}
}

// WithGitBinary configures the git executable binary path used for network transport and git config.
func WithGitBinary(gitBin string) Option {
	return func(c *openConfig) {
		c.gitBin = gitBin
	}
}

// WithTargetRefs configures code branch or tag ref names to resolve anchors against.
func WithTargetRefs(refs ...string) Option {
	return func(c *openConfig) {
		c.targetRefs = append(c.targetRefs, refs...)
	}
}

// Open opens a git repository at the given path (which may be a repository root,
// subdirectory, linked worktree, or bare repository) and initializes a Store handle.
//
// Open is fully offline and performs no network I/O or automatic projection refresh.
func Open(path string, opts ...Option) (*Store, error) {
	gitInfo, err := ResolveGitDir(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoreOpen, err)
	}

	cfg := &openConfig{
		autoRefresh: true,
		gitBin:      "git",
	}
	for _, opt := range opts {
		opt(cfg)
	}

	repoDir := gitInfo.WorkTree
	if repoDir == "" {
		repoDir = gitInfo.GitDir
	}

	storer, err := gitdir.OpenStorage(gitdir.Info{
		WorkTree:  gitInfo.WorkTree,
		GitDir:    gitInfo.GitDir,
		CommonDir: gitInfo.CommonDir,
	})
	if err != nil {
		return nil, fmt.Errorf("writ: open git repo %s: %w: %w", repoDir, ErrStoreOpen, err)
	}

	// Read writer identity (non-fatal if unconfigured)
	ident, identErr := identity.Load(context.Background(), repoDir)
	var hasIdentity bool
	var hasSigner bool
	var signer codec.Signer
	var signerErr error

	if identErr == nil {
		hasIdentity = true
		if cfg.signer != nil {
			signer = cfg.signer
			hasSigner = true
		} else if ident.Key.Value != "" && strings.EqualFold(ident.Key.Format, "ssh") {
			s, err := codec.NewSigner(ident.Key)
			if err == nil {
				signer = s
				hasSigner = true
			} else {
				signerErr = err
			}
		} else {
			signerErr = ErrNoSigningKey
		}
	} else {
		// Check if error was solely due to signing key / gpg format
		var cfgErr *identity.ConfigError
		if errors.As(identErr, &cfgErr) && (cfgErr.Key == "user.signingKey" || cfgErr.Key == "gpg.format") {
			// Writer ID and user name/email are valid and retained in ident
			hasIdentity = true
			identErr = nil // Clear ident error so identity check passes
			if cfg.signer != nil {
				signer = cfg.signer
				hasSigner = true
			} else {
				signerErr = ErrNoSigningKey
			}
		} else {
			hasIdentity = false
			identErr = ErrNoIdentity
			signerErr = ErrNoSigningKey
		}
	}

	// Open DAG store. The producer-vocabularies resolver and the chain
	// observer both close over s, which dagStore has to be constructed
	// before: forward-declare it so the closures capture the variable, not
	// a snapshot of a nil value — by the time Append ever calls either,
	// s below has long since been assigned.
	var s *Store
	dagOpts := []dag.Option{
		dag.WithProducerVocabularies(func() (codec.Vocabularies, error) {
			if s == nil {
				return nil, nil
			}
			// vocabulariesForAppend, not vocabularies: the append path may
			// serve a snapshot up to vocabFreshnessWindow plus one
			// ground-truth resolve stale rather than paying for a
			// dag.Chains ref walk on every single Append (WRIT-202).
			// Every other caller of vocabularies — rules,
			// declaredTypes, and Types/Refresh/Rebuild through them — stays
			// on the ground-truth path unchanged.
			return s.vocabulariesForAppend(context.Background())
		}),
		// Rolls the vocabularies cache's fingerprint forward after a local
		// append instead of leaving every append to look like an
		// invalidating change (Store.noteAppend, Store.vocabularies).
		dag.WithChainObserver(func(objectType string, newTip plumbing.Hash) {
			if s == nil {
				return
			}
			s.noteAppend(objectType, newTip)
		}),
	}
	if hasSigner {
		dagOpts = append(dagOpts, dag.WithSigner(signer))
	}

	dagStore, err := dag.OpenStorage(storer, ident, dagOpts...)
	if err != nil {
		return nil, fmt.Errorf("writ: open dag store: %w: %w", ErrStoreOpen, err)
	}

	// Trust store (WRIT-251 rulings 2 and 4): the allowed_signers *path*
	// is resolved out of git config exactly once, here, via
	// resolveTrustSignersPath — independently of ident/identErr above,
	// for the reason identity.AllowedSignersFile's own doc comment gives
	// (Load's early returns make Identity.AllowedSigners unreliable for a
	// read-only repository). Every later read of the trust store
	// (Store.currentTrustStore, used by Refresh/Rebuild/Get/Schema) reuses
	// this same path and only re-reads and re-hashes the file's content —
	// never git config again: re-resolving the path on every call spawned
	// a git config --list subprocess on every one of those, about 13x
	// main's whole no-op Query.Object cost on a 3,000-op repo (WRIT-251
	// round 2 perf finding). Open never refuses to open on account of any
	// of this: an unset, missing, or unparseable file is treated as no
	// trust store configured (ruling 2).
	trustSignersPath := resolveTrustSignersPath(repoDir)

	// Open projection SQLite cache
	cacheDir := cfg.cacheDir
	if cacheDir == "" {
		cacheDir = filepath.Join(gitInfo.CommonDir, "writ")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("writ: create projection cache dir %s: %w: %w", cacheDir, ErrStoreOpen, err)
	}

	dbPath := filepath.Join(cacheDir, "projection.db")
	localPath := filepath.Join(cacheDir, "local.db")
	projDB, err := projection.Open(dbPath, projection.WithLocalPath(localPath))
	if err != nil {
		return nil, fmt.Errorf("writ: open projection db %s: %w: %w", dbPath, ErrStoreOpen, err)
	}

	// Open sync client
	syncClient, err := writsync.OpenStorage(storer, repoDir, ident, writsync.WithGitBinary(cfg.gitBin))
	if err != nil {
		_ = projDB.Close()
		return nil, fmt.Errorf("writ: open sync client: %w: %w", ErrStoreOpen, err)
	}

	// Load repo ID
	localRepoID, _ := identity.LoadRepoID(context.Background(), repoDir)

	s = &Store{
		gitInfo:          gitInfo,
		storer:           storer,
		dagStore:         dagStore,
		projection:       projDB,
		syncClient:       syncClient,
		identity:         ident,
		hasIdentity:      hasIdentity,
		identErr:         identErr,
		signer:           signer,
		hasSigner:        hasSigner,
		signerErr:        signerErr,
		autoRefresh:      cfg.autoRefresh,
		targetRefs:       cfg.targetRefs,
		localRepoID:      string(localRepoID),
		trustSignersPath: trustSignersPath,
	}

	// Ensure the projection's generated tables exist before any caller can
	// read from it, whether or not autoRefresh ever runs a Refresh: Open
	// creates only substrate tables, and a fresh checkout's generated tables
	// come solely from an ApplySchema call.
	//
	// Resolving the schema costs a cold dag.Store.Enumerate the very first
	// time it runs in a process (Store.vocabularies' cache starts empty),
	// which is linear in the DAG's total op count — unavoidable the one
	// time there is truly nothing to reuse. But projDB.Open just reloaded
	// this cache's generated-table list from meta with no DAG access at all
	// (loadPersistedTables): a reopened cache already has its tables,
	// correctly shaped for whatever schema last applied, so paying for a
	// full resolve here on every single process start — including a pure
	// read on an otherwise warm cache — was pure waste (MAJOR-2, WRIT-189
	// round 1: 6.3x slower Open at 2,000 refs, paid by every CLI
	// invocation). Only a genuinely fresh cache, which has no generated
	// tables yet, pays for it here; a real schema change since the cache
	// was last written is caught lazily, the same way any other log change
	// is — by autoRefresh, or an explicit Store.Refresh call, whichever
	// runs first.
	if !projDB.HasGeneratedTables() {
		initialRules, err := s.rules(context.Background())
		if err != nil {
			_ = projDB.Close()
			return nil, fmt.Errorf("writ: resolve schema: %w", err)
		}
		if err := projDB.ApplySchema(initialRules); err != nil {
			_ = projDB.Close()
			return nil, fmt.Errorf("writ: apply schema: %w", err)
		}
	}

	s.Objects = &Objects{store: s}
	s.ReadState = &ReadState{store: s}
	s.Query = &Query{store: s}

	return s, nil
}

// resolveTrustSignersPath resolves gpg.ssh.allowedSignersFile out of git
// config, via identity.AllowedSignersFile — the one point in a Store's
// whole lifetime that this ever spawns a git subprocess for the trust
// store. Open calls this exactly once, at construction, and caches the
// result on the Store as trustSignersPath; every later read
// (Store.currentTrustStore, called on every Refresh, Rebuild, Get, and
// Schema) reuses that path and only re-reads and re-hashes the file's
// content. Before this, currentTrustStore re-resolved the path on every
// call, which meant a git config --list subprocess on every one of them —
// about 13x main's whole no-op Query.Object cost on a 3,000-op repo
// (WRIT-251 round 2 perf finding). An empty result means unconfigured, or
// the resolve itself failed — never a reason to refuse opening (ruling 2);
// the caller treats both the same as "no trust store".
func resolveTrustSignersPath(repoDir string) string {
	path, err := identity.AllowedSignersFile(context.Background(), repoDir)
	if err != nil {
		return ""
	}
	return path
}

// loadTrustStoreFromPath reads and parses signersPath fresh from disk,
// returning the trust store to verify against (nil if unconfigured,
// missing, or unparseable — ruling 2, never a reason to refuse anything)
// and a digest that changes exactly when the file's meaningful contents
// do: empty when unconfigured, a distinct "unreadable:<path>" hash when
// configured but unreadable or unparseable, and sha256(raw bytes)
// otherwise. Does no subprocess work — signersPath is resolved once, by
// resolveTrustSignersPath.
func loadTrustStoreFromPath(signersPath string) (codec.TrustStore, string) {
	if signersPath == "" {
		return nil, ""
	}
	raw, readErr := os.ReadFile(signersPath)
	if readErr != nil {
		return nil, unreadableTrustDigest(signersPath)
	}
	ts, parseErr := sshsig.ParseAllowedSigners(bytes.NewReader(raw))
	if parseErr != nil {
		return nil, unreadableTrustDigest(signersPath)
	}
	// ts is never a nil *sshsig.TrustStore on a successful parse, so
	// wrapping it in the codec.TrustStore interface here never hits the
	// typed-nil trap dag.WithLiveTrustStore's doc comment warns about.
	return ts, trustStoreDigest(raw)
}

// trustCacheEntry memoizes the trust store parsed from signersPath's last
// observed content digest, so a pass that finds the file unchanged since
// the previous one pays only for a read and a hash, never a re-parse
// (WRIT-251 round 2 perf finding).
type trustCacheEntry struct {
	digest string
	ts     codec.TrustStore
}

// currentTrustStore re-reads s's configured allowed_signers file fresh
// from disk on every call, for a caller that needs today's trust store
// and digest, not one frozen when this Store was constructed (WRIT-251
// round 2 finding: two long-lived handles disagreeing about the file's
// contents fought over one shared projection cache forever, and a
// long-lived handle never picked up an edit). The path itself
// (s.trustSignersPath) is resolved once, at Open — see
// resolveTrustSignersPath for why re-resolving it here on every call was
// the expensive mistake this fixes. Only the file's content is re-read
// and re-hashed every pass; re-parsing it is skipped whenever the hash
// matches the previous pass's, via s.trustCache.
func (s *Store) currentTrustStore() (codec.TrustStore, string) {
	if s.trustSignersPath == "" {
		return nil, ""
	}
	raw, readErr := os.ReadFile(s.trustSignersPath)
	if readErr != nil {
		return nil, unreadableTrustDigest(s.trustSignersPath)
	}
	digest := trustStoreDigest(raw)

	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	if s.trustCache.digest == digest {
		return s.trustCache.ts, digest
	}
	ts, parseErr := sshsig.ParseAllowedSigners(bytes.NewReader(raw))
	if parseErr != nil {
		digest = unreadableTrustDigest(s.trustSignersPath)
		s.trustCache = trustCacheEntry{digest: digest}
		return nil, digest
	}
	s.trustCache = trustCacheEntry{digest: digest, ts: ts}
	return ts, digest
}

// trustStoreDigest fingerprints an allowed_signers file's raw bytes, so
// projection.WithLiveTrustStore can fold a change to the file into
// ApplySchema's schema_digest comparison (WRIT-251 ruling 4): editing the
// trust store then trips needs_rebuild on the next Refresh, the same way a
// real schema change does.
func trustStoreDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// unreadableTrustDigest fingerprints an allowed_signers path that Open
// could not read or parse, distinctly from both "unconfigured" (empty
// digest) and any digest a readable file's bytes would produce — so
// fixing the file later still changes the digest and trips a rebuild,
// even though the broken file was treated as no trust store at all
// (ruling 2, extended to this case per WRIT-251's plan).
func unreadableTrustDigest(path string) string {
	sum := sha256.Sum256([]byte("unreadable:" + path))
	return hex.EncodeToString(sum[:])
}
