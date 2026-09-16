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

	// Trust store (WRIT-251 rulings 2 and 4): resolved through
	// identity.AllowedSignersFile, independently of ident/identErr above —
	// that helper's own doc comment explains why Load's early returns make
	// Identity.AllowedSigners unreliable for a read-only repository. Open
	// never refuses to open on account of any of this: an unset, missing,
	// or unparseable file is treated as no trust store configured, and
	// trustDigest still changes when the file does, so fixing it later
	// trips a projection rebuild (ApplySchema) instead of leaving stale
	// wrong-key rows behind.
	var trustStore codec.TrustStore
	var trustDigest string
	if signersPath, pathErr := identity.AllowedSignersFile(context.Background(), repoDir); pathErr == nil && signersPath != "" {
		if raw, readErr := os.ReadFile(signersPath); readErr == nil {
			if ts, parseErr := sshsig.ParseAllowedSigners(bytes.NewReader(raw)); parseErr == nil {
				// ts is never a nil *sshsig.TrustStore on a successful parse,
				// so wrapping it in the codec.TrustStore interface here never
				// hits the typed-nil trap dag.WithTrustStore's doc comment
				// warns about.
				trustStore = ts
				trustDigest = trustStoreDigest(raw)
			} else {
				trustDigest = unreadableTrustDigest(signersPath)
			}
		} else {
			trustDigest = unreadableTrustDigest(signersPath)
		}
	}
	if trustStore != nil {
		dagOpts = append(dagOpts, dag.WithTrustStore(trustStore))
	}

	dagStore, err := dag.OpenStorage(storer, ident, dagOpts...)
	if err != nil {
		return nil, fmt.Errorf("writ: open dag store: %w: %w", ErrStoreOpen, err)
	}

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
	projDB, err := projection.Open(dbPath, projection.WithLocalPath(localPath), projection.WithTrustStoreDigest(trustDigest))
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
		gitInfo:     gitInfo,
		storer:      storer,
		dagStore:    dagStore,
		projection:  projDB,
		syncClient:  syncClient,
		identity:    ident,
		hasIdentity: hasIdentity,
		identErr:    identErr,
		signer:      signer,
		hasSigner:   hasSigner,
		signerErr:   signerErr,
		autoRefresh: cfg.autoRefresh,
		targetRefs:  cfg.targetRefs,
		localRepoID: string(localRepoID),
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

// trustStoreDigest fingerprints an allowed_signers file's raw bytes, so
// projection.WithTrustStoreDigest can fold a change to the file into
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
