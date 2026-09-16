package dag

import (
	"errors"
	"fmt"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/packidx"
)

// Rejection records an op commit that failed reader validation.
type Rejection struct {
	CommitID string             `json:"commit_id"`
	Reason   codec.RejectReason `json:"reason"`
	Err      string             `json:"error,omitempty"`
}

// EnumerateResult is the output of an enumeration pass across all writers' chains.
type EnumerateResult struct {
	// Ops groups valid ops by envelope ObjectID.
	// Each slice is sorted lexicographically by op ID (commit SHA) for stable
	// grouping, and must be passed through Order before folding.
	Ops map[string][]codec.Op `json:"ops"`

	// Cursors maps every discovered chain ref name to its current tip SHA.
	Cursors CursorSet `json:"cursors"`

	// Rewound contains the ref names of chains whose cursor tip was not an ancestor
	// of the current tip (rollback detected).
	Rewound []string `json:"rewound,omitempty"`

	// Rejections records op commits that failed reader validation.
	Rejections []Rejection `json:"rejections,omitempty"`

	// DecodedCommits is the total number of commits decoded during this pass.
	DecodedCommits int `json:"decoded_commits"`
}

// enumerateConfig collects per-call knobs for Enumerate/EnumerateSince
// beyond the Store's own persistent configuration (signer, trustStore,
// etc., set once at Open). Unexported: this is not a general extension
// point, and neither knob below is meant for an arbitrary caller.
type enumerateConfig struct {
	skipVerify    bool
	trustStore    codec.TrustStore
	trustStoreSet bool
}

// EnumerateOption configures a single Enumerate or EnumerateSince call,
// without touching the Store's own persistent configuration.
type EnumerateOption func(*enumerateConfig)

// SkipVerification skips per-commit signature verification for this call:
// every decoded Op's Verification stays the zero value until a caller
// verifies it itself via codec.Op.Verify. Used only by writ.Objects.Get,
// which needs the outcome for just the few ops belonging to one object
// and would otherwise pay full-repo verification cost — real ed25519
// verification runs tens of microseconds per op, independent of the
// object it belongs to — on every live read (WRIT-251 round 2 perf
// finding). Every other caller of Enumerate/EnumerateSince — projection
// Refresh/Rebuild, Store.Schema — needs every op's outcome upfront and
// must not pass this.
func SkipVerification() EnumerateOption {
	return func(c *enumerateConfig) { c.skipVerify = true }
}

// WithLiveTrustStore overrides, for this call only, the trust store
// EnumerateSince verifies decoded commits against, leaving the Store's
// own configured trust store untouched for every other call. A nil ts
// here still means "no trust store" (Verify then reports wrong-key),
// exactly like an unconfigured Store — this is how a caller that reloads
// allowed_signers fresh before every pass (Store.Refresh, Store.Rebuild)
// supplies a store parsed from the file's current contents instead of
// the one dag.Open froze for the Store's whole lifetime (WRIT-251 round 2
// finding: two long-lived handles disagreeing about the file's contents
// fought over one shared projection cache forever, and a long-lived
// handle never picked up an edit).
func WithLiveTrustStore(ts codec.TrustStore) EnumerateOption {
	return func(c *enumerateConfig) {
		c.trustStore = ts
		c.trustStoreSet = true
	}
}

// Enumerate discovers all writ chains and enumerates all ops cold (equivalent to EnumerateSince(nil)).
//
// Neither Enumerate nor EnumerateSince may take Store.mu: Append holds it
// across its resolveVocabularies callback, which — for a caller wired the
// way writ.Store wires it — re-enters Enumerate/EnumerateSince on this same
// Store on a cache miss. Taking mu here would deadlock that call, silently,
// on every cold-cache non-"schema" Append. See the mu field's doc comment.
func (s *Store) Enumerate(opts ...EnumerateOption) (*EnumerateResult, error) {
	return s.EnumerateSince(nil, opts...)
}

// EnumerateSince walks every local and remote-tracking writ chain from the provided
// cursors, decodes new commits through codec, and groups valid ops by ObjectID.
//
// Must not take Store.mu — see Enumerate's doc comment.
func (s *Store) EnumerateSince(cursors CursorSet, opts ...EnumerateOption) (*EnumerateResult, error) {
	cfg := enumerateConfig{}
	for _, opt := range opts {
		opt(&cfg)
	}
	trustStore := s.trustStore
	if cfg.trustStoreSet {
		trustStore = cfg.trustStore
	}
	// Step 1: Single IterReferences pass
	chains, err := Chains(s.storer)
	if err != nil {
		return nil, fmt.Errorf("dag: enumerate chains: %w", err)
	}

	result := &EnumerateResult{
		Ops:     make(map[string][]codec.Op),
		Cursors: make(CursorSet, len(chains)),
	}

	stopBoundary := make(map[plumbing.Hash]bool)
	var startTips []plumbing.Hash
	rewoundMap := make(map[string]bool)

	// Step 2: Analyze cursors and identify start tips & stop boundaries
	for refName, chainInfo := range chains {
		currentTip := chainInfo.Tip
		result.Cursors[refName] = currentTip.String()

		cursorSHA, hasCursor := cursors.Get(refName)
		if !hasCursor || cursorSHA == "" {
			// Cold chain: walk all history from currentTip
			startTips = append(startTips, currentTip)
			continue
		}

		cursorHash := plumbing.NewHash(cursorSHA)
		if cursorHash == currentTip {
			// Tip has not moved. Seed stop boundary.
			stopBoundary[cursorHash] = true
			continue
		}

		// Tip moved. Check if cursorHash is an ancestor of currentTip.
		ancestor, err := isAncestor(s.storer, currentTip, cursorHash)
		if err != nil {
			ancestor = false
		}

		if ancestor {
			// Fast-forward: walk new ops, stop at cursorHash
			startTips = append(startTips, currentTip)
			stopBoundary[cursorHash] = true
		} else {
			// Rewound / rollback detected!
			rewoundMap[refName] = true
			startTips = append(startTips, currentTip)
		}
	}

	for r := range rewoundMap {
		result.Rewound = append(result.Rewound, r)
	}
	sort.Strings(result.Rewound)

	// Step 3: Walk commit ancestry from startTips, stopping at stopBoundary
	visited := make(map[plumbing.Hash]bool)
	var queue []plumbing.Hash

	for _, tip := range startTips {
		if stopBoundary[tip] {
			visited[tip] = true
			continue
		}
		if !visited[tip] {
			visited[tip] = true
			queue = append(queue, tip)
		}
	}

	var commitsToDecode []*object.Commit

	for len(queue) > 0 {
		currHash := queue[0]
		queue = queue[1:]

		commitObj, err := object.GetCommit(s.storer, currHash)
		if err != nil {
			result.Rejections = append(result.Rejections, Rejection{
				CommitID: currHash.String(),
				Reason:   codec.RejectMissingOpJSON,
				Err:      err.Error(),
			})
			continue
		}

		commitsToDecode = append(commitsToDecode, commitObj)

		for _, pHash := range commitObj.ParentHashes {
			if visited[pHash] {
				continue
			}
			if stopBoundary[pHash] {
				visited[pHash] = true
				continue
			}
			visited[pHash] = true
			queue = append(queue, pHash)
		}
	}

	result.DecodedCommits = len(commitsToDecode)

	// Step 4: Decode commits and handle rejections
	//
	// One packidx cache for this whole pass: every commitsToDecode entry
	// below can hit the same on-disk packs' op.json blobs, and without
	// this, each one re-lists the pack directory and re-decodes every
	// searched pack's whole .idx from scratch (WRIT-255 round 2 — about
	// 40 ms and 28 MB per call on a single 1,000,000-object pack, paid
	// again on every commit). The wrapper is local to this call and
	// discarded when it returns, never stored on Store: a fetch or
	// repack between calls can change the pack set, and a fresh
	// EnumerateSince call must see that fresh, not through a cache built
	// before it happened.
	cachedStorer := packidx.WithCache(s.storer)
	for _, commitObj := range commitsToDecode {
		// pureCommit.Payload comes from FromGitCommit (gogit.go), which
		// builds it by re-encoding go-git's already-parsed *object.Commit
		// (EncodeWithoutSignature) rather than reading the commit's raw
		// object bytes: object.Commit doesn't model every header a raw
		// commit can carry, so this is not guaranteed byte-identical to the
		// original object. It is still not a caller-supplied value, though:
		// codec/verify.go's caller-supplied-Payload trust point is not
		// reachable from this path.
		pureCommit, err := codec.FromGitCommit(cachedStorer, commitObj)
		if err != nil {
			result.Rejections = append(result.Rejections, Rejection{
				CommitID: commitObj.Hash.String(),
				Reason:   codec.RejectMissingOpJSON,
				Err:      err.Error(),
			})
			continue
		}

		op, err := codec.DecodeCommit(pureCommit)
		if err != nil {
			var rej *codec.RejectError
			reason := codec.RejectReason("unknown")
			if errors.As(err, &rej) {
				reason = rej.Reason
			}
			result.Rejections = append(result.Rejections, Rejection{
				CommitID: commitObj.Hash.String(),
				Reason:   reason,
				Err:      err.Error(),
			})
			continue
		}

		// Ingest-time verification (spec/signing.md): the outcome travels
		// with the op as data and never gates whether it folds (ruling 1,
		// WRIT-251). Not a Rejection — WRIT-271 owns surfacing those.
		// Skipped when cfg.skipVerify is set (SkipVerification): op still
		// retains the pure commit it decoded from (codec.Op.sourceCommit),
		// so a caller that skipped this can verify just the ops it needs
		// afterward via codec.Op.Verify.
		if !cfg.skipVerify {
			op.Verification = codec.Verify(pureCommit, trustStore)
		}

		result.Ops[op.ObjectID] = append(result.Ops[op.ObjectID], op)
	}

	// Step 5: Group and sort ops by op ID
	for objID := range result.Ops {
		sort.Slice(result.Ops[objID], func(i, j int) bool {
			return result.Ops[objID][i].ID < result.Ops[objID][j].ID
		})
	}

	sort.Slice(result.Rejections, func(i, j int) bool {
		if result.Rejections[i].CommitID != result.Rejections[j].CommitID {
			return result.Rejections[i].CommitID < result.Rejections[j].CommitID
		}
		return result.Rejections[i].Reason < result.Rejections[j].Reason
	})

	return result, nil
}

// isAncestor reports whether candidate is reachable from tip.
func isAncestor(s storage.Storer, tip, candidate plumbing.Hash) (bool, error) {
	if tip == candidate {
		return true, nil
	}
	if tip.IsZero() || candidate.IsZero() {
		return false, nil
	}
	visited := map[plumbing.Hash]bool{tip: true}
	queue := []plumbing.Hash{tip}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		commit, err := object.GetCommit(s, curr)
		if err != nil {
			continue
		}
		for _, p := range commit.ParentHashes {
			if p == candidate {
				return true, nil
			}
			if !visited[p] {
				visited[p] = true
				queue = append(queue, p)
			}
		}
	}
	return false, nil
}
