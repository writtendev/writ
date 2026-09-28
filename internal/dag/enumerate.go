package dag

import (
	"errors"
	"fmt"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/packidx"
)

// Rejection records an op commit that failed reader validation.
type Rejection struct {
	CommitID string             `json:"commit_id"`
	Reason   codec.RejectReason `json:"reason"`
	Err      string             `json:"error,omitempty"`
}

// RejectObjectUnavailable reports that an op-commit chain references an
// object absent from this clone: either a tree or op.json blob missing
// from this clone's object store — most commonly one withheld by a
// partial clone's fetch filter — or a commit — a chain tip, or a
// parent reached through ParentHashes — missing from this clone's
// object store. No filter produces the second shape: a partial clone's
// fetch filter withholds blobs and trees, never commits. It is
// engine-local, not part of spec/op-envelope.md's closed
// reader-validation rejection set: a reader working from a complete
// clone never produces it, and whether an engine-local reason like this
// belongs in the spec instead is a normative question this ticket
// (WRIT-271) deliberately leaves open for Matt rather than deciding
// here. Before this reason existed, an absent object was misreported as
// a malformed op (missing-op-json or non-canonical-payload) — the same
// category error, for a different commit, that WRIT-255 round 2 review
// found in packedObjectSize.
const RejectObjectUnavailable codec.RejectReason = "object-unavailable"

// EnumerateResult is the output of an enumeration pass across all writers' chains.
type EnumerateResult struct {
	// Ops groups valid ops by envelope ObjectID.
	// Each slice is sorted lexicographically by op ID (commit SHA) for stable
	// grouping, and must be passed through Order before folding.
	Ops map[string][]codec.Op `json:"ops"`

	// Cursors maps every discovered chain ref name to its current tip SHA.
	Cursors CursorSet `json:"cursors"`

	// Rewound contains the ref names of chains whose stored cursor was not
	// reachable from the current tip under the reader walk (WRIT-289): a
	// rollback, or a tip advanced across a commit that fails reader
	// validation — isAncestor's walk stops there exactly as EnumerateSince's
	// own Step 3 does, so the cursor is no longer found even though the old
	// SHA is still, technically, a git ancestor. A tip advanced across an
	// object-unavailable commit whose root tree passes every tree-shape
	// rule and is missing only its own op.json blob is not, by itself, a
	// reason to report Rewound: isAncestor keeps walking through exactly
	// that shape, the same as Step 3 (see decodeOpCommit,
	// RejectObjectUnavailable, and rootOpJSONBlobAbsent). Every other
	// object-unavailable shape — an absent root tree, a root tree present
	// without a top-level op.json entry, or a root tree that already fails
	// a tree-shape rule on what this reader can see — stops isAncestor's
	// walk there too, so a tip advanced across one of those does report
	// Rewound.
	Rewound []string `json:"rewound,omitempty"`

	// Rejections records commits that failed reader validation, or whose
	// root tree or op.json object this clone could not read (WRIT-289, and
	// the orchestrator decision on top of it — see decodeOpCommit,
	// RejectObjectUnavailable, and rootOpJSONBlobAbsent). A rejection ends
	// the walk along that path, with one narrow exception: a commit whose
	// root tree passes every op-envelope tree-shape rule this reader can
	// check without the blob's own bytes (exactly one entry, named
	// op.json, a regular-file blob at mode 100644) and is missing only
	// that entry's own blob is not known to be a non-op, so its parents
	// are still followed, and each such commit reached this way
	// contributes its own rejection. Walking past a run of these costs
	// exactly what walking an equally long chain of valid ops costs — and
	// any push-capable peer can already produce a valid-op chain with no
	// depth bound of its own — so this exception adds no attacker leverage
	// beyond what the format already allows; see rootOpJSONBlobAbsent for
	// why the exception is this narrow and no narrower.
	Rejections []Rejection `json:"rejections,omitempty"`

	// DecodedCommits is the total number of commits decodeOpCommit was
	// called on during this pass, whether it kept the commit as an op or
	// rejected it, for a reader-validation reason or as object-unavailable.
	// It does not count a commit the walk never reached at all: one behind
	// a rejection that stops the walk along that path (WRIT-289), or one
	// object.GetCommit itself failed to fetch. A commit behind an
	// object-unavailable rejection is reached and counted only for the one
	// shape that does not stop the walk — root tree passing every
	// tree-shape rule, missing only its own op.json blob (see
	// rootOpJSONBlobAbsent); every other object-unavailable shape stops the
	// walk exactly like a reader-validation rejection.
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
	// seen, when non-nil, is consulted by Step 3's walk wherever
	// stopBoundary is: a commit it reports true for is marked visited but
	// neither decoded nor expanded to its parents, exactly like a
	// stopBoundary tip. Nil (the default) keeps today's behaviour of
	// walking every commit back to the stored cursors. See WithSeen.
	seen func(opID string) bool
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

// WithSeen scopes Step 3's walk to stop at any commit match reports true
// for, exactly as the walk already stops at a stopBoundary cursor tip: the
// commit is marked visited but neither decoded nor expanded to its
// parents. Nil (the default, and the zero value) keeps today's behaviour
// of walking every commit back to the stored cursors. match is called
// with a commit's hex SHA — the same string codec.DecodeCommit reports as
// that commit's op.ID, so a caller keying off op IDs it already holds
// needs no translation.
//
// Caller invariant: every commit the reader walk would reach from a
// commit match accepts — not "every ancestor" in the plain git sense, but
// everything Step 3's own stopping rule (WRIT-289) would expand parents
// into from it — must itself already be recorded by the caller. Violate
// this and the walk silently drops that unrecorded commit, and everything
// the walk would have reached from it, from the result — there is nothing
// in EnumerateResult to say so, because from this call's point of view the
// predicate simply said "already have it".
//
// projection.Refresh's incremental path satisfies the invariant: its
// predicate is backed by the ops table's op_id primary key, and every op
// in that table had everything the reader walk reaches from it walked by
// whichever pass first inserted it, because rebuildWithConfig always
// truncates ops and chain_tips together before a cold EnumerateSince(nil,
// …) repopulates them, and both a rewound chain and a disappeared chain
// force that same full rebuild (Step 2 above, which this option never
// touches) instead of ever reaching an incremental pass with a gap in
// what ops records. What the walk reaches from a given commit is fixed by
// decodeOpCommit's verdict on every commit along the way, and that verdict
// never changes after the fact for a commit whose op.json this clone
// already had.
//
// A RejectObjectUnavailable commit's verdict is the one exception: it can
// change later, in either direction, when the object this clone was
// missing arrives (a partial clone's later unfiltered fetch, most
// commonly):
//   - the commit was walked past — root tree passing every tree-shape
//     rule, missing only its own op.json blob (see rootOpJSONBlobAbsent)
//     — and ops behind it were recorded, but the blob that arrives turns
//     out to fail reader validation itself (non-canonical, wrong schema,
//     a committer mismatch); a cold walk now stops at that commit, so
//     those recorded ops are no longer something a cold walk would hold;
//   - the commit stopped the walk — an absent root tree, an unknowable
//     op-shape — and nothing behind it was recorded, but the tree that
//     arrives turns out op-shaped after all; a cold walk now reaches ops
//     behind it that no earlier pass ever recorded.
//
// Either way is a real gap against this option's invariant, and neither
// is something WithSeen's predicate can detect by itself — it only ever
// answers "already recorded", never "recorded under a verdict that has
// since changed". projection.Refresh closes both directions (WRIT-366)
// by persisting the set of commits a pass recorded as
// RejectObjectUnavailable and, before every incremental pass, rechecking
// each one against the store's current object availability
// (Store.StillObjectUnavailable): a changed verdict routes that pass to a
// full Rebuild instead of trusting the stored cursors past it, so the
// invariant holds again by the next call. A caller of this option that
// does not perform an equivalent recheck must not treat the invariant as
// holding across an object-unavailable repair.
func WithSeen(match func(opID string) bool) EnumerateOption {
	return func(c *enumerateConfig) { c.seen = match }
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

	// One packidx cache for this whole pass: every commit decoded below —
	// by Step 2's isAncestor calls and Step 3's walk alike — can hit the
	// same on-disk packs' op.json blobs, and without this, each one
	// re-lists the pack directory and re-decodes every searched pack's
	// whole .idx from scratch (WRIT-255 round 2 — about 40 ms and 28 MB
	// per call on a single 1,000,000-object pack, paid again on every
	// commit). The wrapper is local to this call and discarded when it
	// returns, never stored on Store: a fetch or repack between calls can
	// change the pack set, and a fresh EnumerateSince call must see that
	// fresh, not through a cache built before it happened. Needed as of
	// WRIT-289 in Step 2 too, not just the decode pass below: isAncestor
	// now decodes commits to find the reader-validation stopping point,
	// the same way Step 3's walk does.
	cachedStorer := packidx.WithCache(s.storer)

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
		ancestor, err := isAncestor(cachedStorer, currentTip, cursorHash)
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
	// or, when cfg.seen is set, at any commit it already knows about (see
	// WithSeen). stop is the one place that decides, so the start-tip loop
	// and the parent-expansion loop below never drift apart on what "stop"
	// means.
	stop := func(h plumbing.Hash) bool {
		return stopBoundary[h] || (cfg.seen != nil && cfg.seen(h.String()))
	}

	visited := make(map[plumbing.Hash]bool)
	var queue []plumbing.Hash

	for _, tip := range startTips {
		if stop(tip) {
			visited[tip] = true
			continue
		}
		if !visited[tip] {
			visited[tip] = true
			queue = append(queue, tip)
		}
	}

	// decodedCommit pairs a kept commit's pure form with its decoded op, so
	// Step 4 below can verify and group each one without decoding it a
	// second time.
	type decodedCommit struct {
		pure codec.Commit
		op   codec.Op
	}
	var decoded []decodedCommit

	for len(queue) > 0 {
		currHash := queue[0]
		queue = queue[1:]

		commitObj, err := object.GetCommit(s.storer, currHash)
		if err != nil {
			reason := codec.RejectMissingOpJSON
			if errors.Is(err, plumbing.ErrObjectNotFound) && objectAbsent(s.storer, currHash) {
				reason = RejectObjectUnavailable
			}
			result.Rejections = append(result.Rejections, Rejection{
				CommitID: currHash.String(),
				Reason:   reason,
				Err:      err.Error(),
			})
			continue
		}

		// decodeOpCommit is the single definition of "is an op" (WRIT-289):
		// a commit it rejects for a reader-validation reason ends the walk
		// right here — a chain is a chain, and a break in it is the end of
		// it — so its parents are never enqueued below. An op behind such a
		// break is still enumerated if some other path of valid ops reaches
		// it (e.g. a causal parent edge from a different chain). The one
		// exception is a RejectObjectUnavailable commit whose root tree
		// passes every op-envelope tree-shape rule this reader can check
		// without the blob's own bytes — exactly one entry, named op.json,
		// a regular-file blob at mode 100644 — and is missing only that
		// entry's own blob: the shape a blob-filtered clone's fetch filter
		// leaves behind. This commit's own object and root tree were read
		// fine, so it is not known to be a non-op, and its parents are
		// already known from commitObj (fetched above, independent of the
		// tree), so the walk still enqueues them below (see
		// rootOpJSONBlobAbsent). Every other RejectObjectUnavailable
		// commit stops here exactly like a reader-validation rejection: an
		// absent root tree; a root tree present without a top-level
		// op.json entry, even if some other object it names is also
		// absent; or a root tree that already fails a tree-shape rule on
		// what this reader can see — an extra entry beside op.json
		// whatever its own blob holds, op.json present as a directory, or
		// op.json at some mode other than 100644 — which already settles
		// the verdict without needing the blob at all (orchestrator
		// decision, WRIT-289 round 3 review, narrowing round 2's cut
		// further: round 2 only checked that an op.json entry existed and
		// its blob was absent, so a tree with one of these other visible
		// defects still returned true, walking unbounded into ordinary
		// code history that happened to carry a root-level op.json under a
		// --filter=blob:none or --filter=tree:1 clone).
		result.DecodedCommits++
		pure, op, rej := decodeOpCommit(cachedStorer, commitObj)
		if rej == nil {
			decoded = append(decoded, decodedCommit{pure: pure, op: op})
		} else {
			result.Rejections = append(result.Rejections, *rej)
			if rej.Reason != RejectObjectUnavailable || !rootOpJSONBlobAbsent(cachedStorer, commitObj) {
				continue
			}
		}

		for _, pHash := range commitObj.ParentHashes {
			if visited[pHash] {
				continue
			}
			if stop(pHash) {
				visited[pHash] = true
				continue
			}
			visited[pHash] = true
			queue = append(queue, pHash)
		}
	}

	// Step 4: verify and group the commits Step 3 kept. No second decode —
	// decodeOpCommit above already produced each one's pure Commit and Op.
	//
	// matchedObjects and pendingByObject exist only to make VerifyOnly's
	// membership scoping (see that option's doc comment) hold regardless
	// of decode order; cfg.verifyMatch == nil (verify everything) never
	// touches either, so Refresh/Rebuild's full-verify pass pays nothing
	// extra for this.
	//
	// decoded is walked tip-first (Step 3's BFS starts at each chain's
	// current tip and visits parents only after their children, and only
	// once a commit passes decodeOpCommit), so an op nearer a chain's tip
	// decodes before an ancestor deeper in
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

	for _, dc := range decoded {
		// pureCommit.Payload comes from FromGitCommit (gogit.go), which
		// builds it with commit.EncodeWithoutSignature — see
		// decodeOpCommit's doc comment for why it is trustworthy as the
		// signed payload. dc.pure and dc.op are exactly what decodeOpCommit
		// produced for this commit in Step 3 above; nothing here re-decodes
		// or re-fetches it.
		pureCommit := dc.pure
		op := dc.op

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

// StillObjectUnavailable re-decodes each commit in ids against s's current
// object store and reports, per commit, whether decodeOpCommit's verdict
// for it is still exactly RejectObjectUnavailable — the same predicate
// EnumerateSince's Step 3 and isAncestor apply during a walk, run here
// against a caller-supplied list of commits outside one (WRIT-366).
// projection.Refresh calls this, before its incremental pass, on the
// commits a prior pass recorded as object-unavailable: unlike every other
// rejection reason, that verdict is not permanent (a blob or tree a
// partial clone's fetch filter withheld can arrive later), and an
// incremental pass that never re-decodes a commit sitting behind its
// stored cursor would otherwise hold whatever it decided about that
// commit's shape the first time it saw it, forever (see WithSeen's doc
// comment for the two directions this can go wrong).
//
// A commit reported true is unchanged: either this clone still cannot
// read whatever object was missing, or it cannot even fetch the commit at
// all any more — either way, not a reason to trust the stored cursors
// past it. A commit reported false has a different verdict now: it
// decodes as a valid op, or it fails reader validation for some other
// reason (a repaired blob that turns out malformed, say) — either way,
// the caller must fall back to a full Rebuild rather than keep treating
// that commit, and everything reachable from it, the way an earlier pass
// did.
//
// One packidx cache is shared across every id in ids (see EnumerateSince's
// cachedStorer comment for why this matters — WRIT-255 round 2): this is
// meant to be called with a whole batch of recorded commits, not one at a
// time.
func (s *Store) StillObjectUnavailable(ids []string) (map[string]bool, error) {
	cachedStorer := packidx.WithCache(s.storer)
	result := make(map[string]bool, len(ids))
	for _, id := range ids {
		hash := plumbing.NewHash(id)
		commitObj, err := object.GetCommit(cachedStorer, hash)
		if err != nil {
			if errors.Is(err, plumbing.ErrObjectNotFound) {
				result[id] = true
				continue
			}
			return nil, fmt.Errorf("dag: recheck object-unavailable commit %s: %w", id, err)
		}
		_, _, rej := decodeOpCommit(cachedStorer, commitObj)
		result[id] = rej != nil && rej.Reason == RejectObjectUnavailable
	}
	return result, nil
}

// decodeOpCommit is the single definition of "is an op" (WRIT-289): it
// wraps codec.FromGitCommit and codec.DecodeCommit — reader-validation
// rules 1-4, spec/op-envelope.md §Reader validation — and maps a failure at
// either step to the Rejection a caller reports. A non-nil *Rejection whose
// Reason is anything other than RejectObjectUnavailable means commitObj
// failed reader validation itself; a caller enumerating ancestry MUST NOT
// expand such a commit's parents (spec/ref-layout.md §Reader enumeration:
// "a chain is a chain; a break in it is the end of it"). A RejectObjectUnavailable
// *Rejection does not by itself mean this: commitObj itself was read fine (a
// caller always already has it, and its ParentHashes, before calling this),
// only some object its tree names was not found in this clone's object
// store. Whether that makes commitObj's parents worth expanding anyway
// depends on exactly which object is missing — decodeOpCommit does not
// decide that; a caller enumerating ancestry checks rootOpJSONBlobAbsent
// (orchestrator decision, WRIT-289 round 1 and round 2 review: the round 1
// cut expanded on every RejectObjectUnavailable regardless of which object
// was missing, which let a tree-filtered clone's ref tip walk unbounded
// into ordinary code history — see rootOpJSONBlobAbsent's doc comment for
// the narrower rule). A signature-verification outcome and an unknown
// object type, op type, op version, or field are not reasons decodeOpCommit
// ever rejects — both are ops, and neither one stops the walk.
func decodeOpCommit(st storage.Storer, commitObj *object.Commit) (codec.Commit, codec.Op, *Rejection) {
	// pureCommit.Payload comes from FromGitCommit (gogit.go), which builds
	// it with commit.EncodeWithoutSignature. commitObj always comes from a
	// caller's own object.GetCommit, so go-git still holds the encoded
	// object it was decoded from, and EncodeWithoutSignature streams those
	// raw bytes verbatim, dropping only the gpgsig/gpgsig-sha256 header
	// lines and their continuations (stripObjectSignatures). The payload is
	// therefore the original object's bytes minus the signature header
	// block — exactly the bytes the signature was computed over, headers
	// object.Commit gives no field of its own included — which is what
	// spec/signing.md §Signed Payload requires. go-git re-encodes the
	// parsed struct only when matchesSource() is false: an
	// in-memory-constructed commit, or one whose exported fields were
	// mutated after decode. Neither happens on this path. The payload is
	// also not a caller-supplied value: codec/verify.go's
	// caller-supplied-Payload trust point is not reachable from here.
	pureCommit, err := codec.FromGitCommit(st, commitObj)
	if err != nil {
		reason := codec.RejectMissingOpJSON
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			reason = RejectObjectUnavailable
		}
		return codec.Commit{}, codec.Op{}, &Rejection{
			CommitID: commitObj.Hash.String(),
			Reason:   reason,
			Err:      err.Error(),
		}
	}

	op, err := codec.DecodeCommit(pureCommit)
	if err != nil {
		var rej *codec.RejectError
		reason := codec.RejectReason("unknown")
		if errors.As(err, &rej) {
			reason = rej.Reason
		}
		return codec.Commit{}, codec.Op{}, &Rejection{
			CommitID: commitObj.Hash.String(),
			Reason:   reason,
			Err:      err.Error(),
		}
	}

	return pureCommit, op, nil
}

// objectAbsent reports whether hash names no object at all in s — as
// opposed to naming an object of the wrong type. go-git's typed lookups
// (object.GetCommit, object.GetTree, object.GetBlob, all reached from
// this package and from codec.FromGitCommit) report
// plumbing.ErrObjectNotFound for both cases: filesystem.ObjectStorage's
// EncodedObject returns that same sentinel when it finds the object but
// its type doesn't match the one requested (WRIT-271 round 1 review — a
// present-but-malformed op.json entry, e.g. mode 040000 naming a tree
// that is in the store, was misclassified as RejectObjectUnavailable
// because of this). A caller deciding between "genuinely absent from
// this clone" and "present but the wrong object type" probes with
// plumbing.AnyObject, which skips the type check entirely.
func objectAbsent(s storage.Storer, hash plumbing.Hash) bool {
	_, err := s.EncodedObject(plumbing.AnyObject, hash)
	return errors.Is(err, plumbing.ErrObjectNotFound)
}

// rootOpJSONBlobAbsent is the narrow test that decides whether a
// RejectObjectUnavailable commit's parents are still worth expanding
// (orchestrator decision, WRIT-289 round 3 review, narrowing the round 2
// exception further): true only when commitObj's root tree is itself
// readable and passes every one of spec/op-envelope.md §Reader
// validation rule 1's tree-shape checks — the checks that do not need the
// blob's own bytes — against what this clone can see:
//   - the tree has exactly one entry;
//   - that entry is named "op.json";
//   - that entry's mode is a regular file, 100644 (not a directory, not
//     executable, not a symlink);
//
// and, beyond the tree-shape rules themselves, that entry's own blob is
// the one and only thing locally absent. A tree meeting all of this is
// not known to be a non-op — the same commit read through a complete
// clone might decode cleanly, and its payload's byte-equality and schema
// rules simply cannot be checked without the blob — so a caller
// enumerating ancestry may still expand its parents, already known from
// commitObj's own ParentHashes independent of the tree.
//
// Because the tree has exactly one entry whenever this returns true,
// "that entry's own blob is the only thing locally absent" follows for
// free: there is nothing else in the tree left to be absent or present.
//
// False for every other RejectObjectUnavailable cause, each of which the
// tree alone already proves is not an op, or which this clone cannot even
// read enough of to tell:
//   - the root tree itself is absent: a reader that cannot read a tree
//     cannot tell whether it would have named an op.json entry at all —
//     the shape a tree-filtered clone's ref tip has on ordinary code
//     history, at every commit in that history were the walk to keep
//     going;
//   - the tree already fails a tree-shape rule on what this reader can
//     see — an extra entry beside op.json (however that entry's own blob
//     reads, present or absent), op.json present as a directory instead
//     of a blob, or op.json present at some mode other than 100644 — a
//     reader-validation rejection (extra-tree-entry, op-json-subdirectory,
//     or invalid-op-json-mode) waiting only on the blob read that never
//     gets to run; the tree already settles the verdict without it (round
//     3 review: the round 2 cut only checked that an entry named op.json
//     existed and its blob was absent, so a tree with other visible
//     defects — an extra entry, op.json as a directory, or the wrong mode
//     — still returned true here, walking unbounded into code history
//     that happens to carry a root-level op.json under a
//     --filter=blob:none or --filter=tree:1 clone);
//   - the tree is present but names no top-level "op.json" entry at all,
//     even when some other object it names (a subtree, most commonly) is
//     also locally absent: rule 1 already rejects that shape on the
//     entries this reader can see — missing-op-json, or
//     op-json-subdirectory if the absent subtree itself turns out to hold
//     an op.json a complete reader would find (this reader cannot tell
//     which); either way it is a known non-op, so there is nothing left
//     to learn by reading further.
//
// commitObj.Tree() re-reads the root tree object decodeOpCommit's own
// codec.FromGitCommit call already read moments earlier; both go through
// st (the cachedStorer callers pass), so the second read is a cache hit,
// not a second trip to the pack.
func rootOpJSONBlobAbsent(st storage.Storer, commitObj *object.Commit) bool {
	tree, err := commitObj.Tree()
	if err != nil {
		return false
	}
	if len(tree.Entries) != 1 {
		return false
	}
	entry := tree.Entries[0]
	if entry.Name != "op.json" || entry.Mode != filemode.Regular {
		return false
	}
	return objectAbsent(st, entry.Hash)
}

// isAncestor reports whether candidate is reachable from tip by walking
// only through commits that pass decodeOpCommit, or that decodeOpCommit
// rejects as RejectObjectUnavailable with rootOpJSONBlobAbsent true
// (WRIT-289, and the orchestrator decision on top of it): the rollback
// check uses the same reader walk EnumerateSince's own Step 3 does, so an
// incremental refresh can never disagree with a cold rebuild about which
// chain is a fast-forward. tip == candidate is always true, even when tip
// itself is not an op — the trivial "the cursor hasn't moved" case needs
// no decode. Beyond that, a commit's parents are examined once that commit
// itself passes decodeOpCommit, or is RejectObjectUnavailable with its
// root tree passing every tree-shape rule and missing only its own
// op.json blob (see rootOpJSONBlobAbsent — the same shape Step 3's walk
// still expands); any other rejection stops the walk on that path, tip
// included, exactly like Step 3's walk. candidate itself need not be an op: reaching it as some
// kept commit's parent is enough, the same way a stopBoundary cursor need
// not itself have been re-decoded by this call.
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
		if _, _, rej := decodeOpCommit(s, commit); rej != nil && (rej.Reason != RejectObjectUnavailable || !rootOpJSONBlobAbsent(s, commit)) {
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
