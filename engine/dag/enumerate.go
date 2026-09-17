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
// beyond the Store's own persistent configuration (signer, etc., set once
// at Open). Unexported: this is not a general extension point, and
// neither knob below is meant for an arbitrary caller.
type enumerateConfig struct {
	// verifyMatch, when non-nil, scopes signature verification to the
	// decoded ops it reports true for; nil means every decoded op is
	// verified (the default). See VerifyOnly.
	verifyMatch func(codec.Op) bool
	// trustStore is the trust store to verify against. There is no
	// Store-level fallback (WRIT-251 round 2 finding: a trust store frozen
	// at dag.Open let two long-lived handles disagree about
	// allowed_signers's contents forever); a caller that needs
	// verification supplies one per call via WithLiveTrustStore, read
	// fresh immediately before the call. Zero value (nil) means "no trust
	// store", exactly like an unconfigured one.
	trustStore codec.TrustStore
}

// EnumerateOption configures a single Enumerate or EnumerateSince call,
// without touching the Store's own persistent configuration.
type EnumerateOption func(*enumerateConfig)

// VerifyOnly scopes signature verification, for this call only, to every
// op belonging to an ObjectID for which at least one decoded op satisfies
// match; every op whose ObjectID never has a satisfying op keeps the zero
// Verification. EnumerateSince calls match immediately after decoding
// each op and before grouping, so match can key off any field the decode
// just produced — including ObjectID and ObjectType — without the caller
// having to know the answer beforehand.
//
// The scope is ObjectID membership, not "this op itself satisfies match":
// EnumerateSince decodes tip-first, so an op nearer some chain's tip
// decodes before an ancestor deeper in that same object's history, and a
// type- or body-keyed match can see a non-matching op for an ObjectID
// before it ever sees the matching op that puts that ObjectID in scope.
// A caller matching on ObjectType, as Store.Schema does, would otherwise
// silently skip a forged, wrong-type op carrying a schema object's own ID
// whenever that op decodes ahead of the object's genuine schema-typed op
// (WRIT-251 round 3 finding — see EnumerateSince's matchedObjects/
// pendingByObject for how the backfill this requires stays a bounded,
// per-object lookup rather than a second walk of the repository). An
// ObjectID-keyed match, by contrast, never has this problem: every op for
// a given ObjectID agrees on whether match(op) is true, in any decode
// order, so membership and "this op itself satisfies match" coincide.
//
// This takes a predicate rather than a plain list of object IDs (as first
// proposed in review) because one of its two callers can't supply IDs in
// advance: writ.Store.Schema doesn't know which objects are schema-typed
// until it has decoded them, and decoding the whole repo a second time
// just to find out first would cost as much as the every-op verification
// this option exists to avoid (WRIT-251 round 2 finding). A predicate
// covers both shapes with one option:
//   - writ.Objects.Get and writ.Store.SchemaAfterApply already know the
//     one object ID they want: match is "op.ObjectID == id".
//   - writ.Store.Schema/ApplySchema don't know IDs in advance, but do
//     know the type immediately at decode time: match is
//     "op.ObjectType == \"schema\"", or "false" for ApplySchema, whose
//     result feeds only schemaFrontier and never reads Verification.
//
// Every other caller of Enumerate/EnumerateSince — projection
// Refresh/Rebuild — needs every op's outcome upfront and must not pass
// this.
func VerifyOnly(match func(codec.Op) bool) EnumerateOption {
	return func(c *enumerateConfig) { c.verifyMatch = match }
}

// WithLiveTrustStore supplies the trust store this call verifies decoded
// commits against, read fresh by the caller immediately before the call —
// never one frozen at dag.Open (WRIT-251 round 2 finding: freezing it let
// two long-lived handles disagree about allowed_signers's contents
// forever, fighting over one shared projection cache, and let a
// long-lived handle never pick up an edit). A nil ts, or omitting this
// option entirely, both mean "no trust store": Verify then reports
// wrong-key for an otherwise-valid signature, never a reason to refuse
// anything.
func WithLiveTrustStore(ts codec.TrustStore) EnumerateOption {
	return func(c *enumerateConfig) {
		c.trustStore = ts
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

	// matchedObjects and pendingByObject exist only to make VerifyOnly's
	// membership scoping (see that option's doc comment) hold regardless
	// of decode order; cfg.verifyMatch == nil (verify everything) never
	// touches either, so Refresh/Rebuild's full-verify pass pays nothing
	// extra for this.
	//
	// commitsToDecode is walked tip-first (Step 3's BFS starts at each
	// chain's current tip and visits parents only after their children),
	// so an op nearer a chain's tip decodes before an ancestor deeper in
	// that same object's history. A predicate keyed on the op's own type
	// or body, rather than a caller-known ObjectID, can therefore see a
	// non-matching op for some ObjectID before it ever sees the matching
	// op that puts that ObjectID in scope (WRIT-251 round 3: Store.Schema
	// matches "op.ObjectType == \"schema\"", so a forged, wrong-type op
	// carrying a schema object's ID decodes — and, before this fix, was
	// left unverified — ahead of that object's own genuine schema-typed
	// op, usually its oldest and so its tip-furthest one). matchedObjects
	// records, the moment any op for an ObjectID satisfies cfg.verifyMatch,
	// that every op sharing that ObjectID is in scope. pendingByObject
	// records exactly the ops skipped before that moment, by their
	// position in result.Ops[objectID], so the backfill pass below can
	// verify them once the object is confirmed — without a second walk of
	// the repository: only the specific commits named there are re-fetched
	// by ID, through this same cachedStorer, and only for objects that
	// end up matched at all.
	var matchedObjects map[string]bool
	var pendingByObject map[string][]int
	if cfg.verifyMatch != nil {
		matchedObjects = make(map[string]bool)
		pendingByObject = make(map[string][]int)
	}

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
		// Scoped to match's ops when cfg.verifyMatch is set (VerifyOnly);
		// nil means every decoded op is verified, still using this pass's
		// own pureCommit and never a value retained across calls.
		switch {
		case cfg.verifyMatch == nil:
			op.Verification = codec.Verify(pureCommit, cfg.trustStore)
		case cfg.verifyMatch(op):
			op.Verification = codec.Verify(pureCommit, cfg.trustStore)
			matchedObjects[op.ObjectID] = true
		case matchedObjects[op.ObjectID]:
			// A prior op decoded earlier in this same pass already put
			// this ObjectID in scope (see matchedObjects' doc comment
			// above), so this one is too even though it does not itself
			// satisfy cfg.verifyMatch.
			op.Verification = codec.Verify(pureCommit, cfg.trustStore)
		default:
			// Not yet known to be in scope. Recorded for the backfill
			// pass below in case a later-decoded op for this same
			// ObjectID does satisfy cfg.verifyMatch.
			pendingByObject[op.ObjectID] = append(pendingByObject[op.ObjectID], len(result.Ops[op.ObjectID]))
		}

		result.Ops[op.ObjectID] = append(result.Ops[op.ObjectID], op)
	}

	// Backfill: verify every op this pass deferred (the switch's default
	// case above) whose ObjectID a later-decoded op confirmed was in
	// scope after all. Each is a single commit lookup by the ID already
	// decoded onto it, through the same cachedStorer this whole pass
	// uses — not a second walk of the ancestry, and bounded by that one
	// object's own out-of-scope-looking op count, not the size of the
	// repository (WRIT-251 round 3).
	for objID := range matchedObjects {
		for _, idx := range pendingByObject[objID] {
			result.Ops[objID][idx].Verification = verifyCommitByID(cachedStorer, result.Ops[objID][idx].ID, cfg.trustStore)
		}
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

// verifyCommitByID re-derives one already-decoded commit's pure form and
// verifies it, for EnumerateSince's backfill pass only: id is always a
// commit this same call already decoded successfully once (it comes from
// an Op already sitting in result.Ops), so a failure here is not expected
// in practice — a zero Verification (Outcome "") on the rare error path
// is distinguishable from every real outcome and is preferable to
// silently reporting a wrong one. This is one targeted object lookup, not
// a walk: it costs what one commit and its tree already known to exist
// cost, the same as any other single entry of the loop above.
func verifyCommitByID(s storage.Storer, id string, ts codec.TrustStore) codec.Verification {
	commitObj, err := object.GetCommit(s, plumbing.NewHash(id))
	if err != nil {
		return codec.Verification{}
	}
	pureCommit, err := codec.FromGitCommit(s, commitObj)
	if err != nil {
		return codec.Verification{}
	}
	return codec.Verify(pureCommit, ts)
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
