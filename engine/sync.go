package writ

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/identity"
	writsync "github.com/writtendev/writ/internal/sync"
)

// SyncResult reports aggregate statistics from a sync operation.
type SyncResult struct {
	// OpsFetched is the number of new op commits fetched from the remote.
	OpsFetched int `json:"ops_fetched"`

	// OpsPushed is the number of local op commits pushed to the remote.
	OpsPushed int `json:"ops_pushed"`

	// ObjectsTouched is the number of collaborative objects whose materialized state changed.
	ObjectsTouched int `json:"objects_touched"`

	// Unsynced is the remaining number of unpushed local ops for the remote.
	Unsynced int `json:"unsynced"`

	// Rejected is the number of op commits this sync's projection
	// refresh could not accept: a malformed peer op rejected on
	// reader validation, or an op commit naming an object absent from
	// this clone (RejectObjectUnavailable — a withheld tree or
	// op.json blob, or a missing commit; engine-local, not itself a
	// reader-validation reason). See RefreshStats.Rejections for each
	// one's specific reason — a quarantined peer op is no longer
	// silently discarded (WRIT-271).
	Rejected int `json:"rejected,omitempty"`
}

// TypeUnsynced reports the unsynced operations count for a specific collaborative object type.
type TypeUnsynced struct {
	ObjectType string `json:"object_type"`
	Unsynced   int    `json:"unsynced"`
}

// SyncStatus reports the synchronization status against a git remote.
type SyncStatus struct {
	// Remote is the name of the git remote (e.g. "origin").
	Remote string `json:"remote"`

	// Unsynced is the number of local op commits not yet pushed to the remote.
	Unsynced int `json:"unsynced"`

	// ByType breaks down unsynced ops by collaborative object type.
	ByType []TypeUnsynced `json:"by_type,omitempty"`

	// Diverged indicates that the remote chain tip is not an ancestor of the local chain tip.
	Diverged bool `json:"diverged,omitempty"`

	// LastSyncedAt is the timestamp of the last successful sync against this remote, if any.
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
}

// SyncError represents a structured failure during synchronization with a git remote.
type SyncError struct {
	Remote    string
	Kind      string
	Message   string
	Advice    string
	Retryable bool
	Unsynced  int
	Err       error
}

// Error returns the formatted sync error description.
func (e *SyncError) Error() string {
	if e.Advice != "" {
		return fmt.Sprintf("sync %s: %s: %s (%s)", e.Remote, e.Kind, e.Message, e.Advice)
	}
	return fmt.Sprintf("sync %s: %s: %s", e.Remote, e.Kind, e.Message)
}

// Unwrap returns the underlying classified error sentinel (such as ErrAuth, ErrNetwork, ErrRefRejected, ErrNonFastForward, or ErrUnknownRemote).
func (e *SyncError) Unwrap() error {
	return e.Err
}

// Sync ensures fetch refspecs in .git/config, fetches remote operations, pushes local operations,
// and refreshes the projection cache.
//
// On any failure -- whether the remote turns out to be syntactically invalid
// or unconfigured, or the fetch/push transport itself fails -- Sync still
// refreshes the projection cache and returns the remaining unsynced count
// wrapped in a *SyncError. The one exception is an empty remote name, which
// reports Unsynced: 0 rather than the real count; see SyncStatus, which
// carves the same case out of the same guarantee for the same reason.
func (s *Store) Sync(ctx context.Context, remote string) (SyncResult, error) {
	if s == nil {
		return SyncResult{}, fmt.Errorf("writ: store is nil")
	}

	// An empty remote name gets no guard of its own here: ValidateRemoteName
	// rejects it with ErrInvalidRemoteName exactly like every other
	// syntactically unusable name, so it reports the same invalid-name kind
	// and the same exit 2. A bare error returned ahead of that check (what
	// this used to do) surfaced as exit 1 / kind "unknown" -- the code a
	// caller reads as a transport failure and retries.
	//
	// A remote that is syntactically invalid, or well-formed but not
	// configured at all (no remote.<remote>.url or remote.<remote>.pushurl),
	// is checked once, upfront,
	// before Ensure, Fetch, or Push ever run. This is a deliberate carve-out
	// from the "push is never gated on fetch" rule below: that rule exists
	// so a peer's broken chain or a dead-but-configured remote's fetch
	// failure can never strand this writer's unpushed ops (WRIT-270), but a
	// remote that is not configured at all is not a fetch failure -- there
	// is nowhere to push either, so gating both here does not weaken that
	// rule. It also closes the WRIT-283 phantom-remote bug at its source:
	// s.syncClient.Ensure would otherwise write a url-less config section
	// for a name that was never a real remote.
	if err := s.checkRemoteAvailable(ctx, remote); err != nil {
		// Sync's doc comment promises the projection cache is refreshed and
		// the true remaining unsynced count is returned on every failure
		// path, not just a transport failure below -- and both are purely
		// local work that does not need this remote to be reachable, or
		// even syntactically valid, to run: Refresh rebuilds the cache from
		// this repo's own git objects, and countUnsynced's ComputeStatus
		// walks local chain refs against this remote's last-fetched
		// tracking frontier. Skipping them here left a --json caller
		// reading "unsynced":0 for a remote that in fact had unpushed ops
		// (round-1 review finding).
		//
		// An empty remote name is the documented exception: countUnsynced's
		// ComputeStatus rejects it outright, so the count below is 0 rather
		// than the real one. See SyncStatus's doc comment for why that is
		// left alone rather than special-cased here.
		s.invalidateVocabularies()
		refreshStats, refreshErr := s.Refresh(ctx)
		unsynced, _ := s.countUnsynced(ctx, remote)

		objectsTouched := 0
		rejected := 0
		if refreshErr == nil {
			objectsTouched = refreshStats.ObjectsTouched
			rejected = len(refreshStats.Rejections)
		}

		var syncErr *SyncError
		if errors.As(err, &syncErr) {
			syncErr.Unsynced = unsynced
		}

		return SyncResult{
			ObjectsTouched: objectsTouched,
			Unsynced:       unsynced,
			Rejected:       rejected,
		}, err
	}

	// fetchErr and pushErr are tracked separately because push must not be
	// gated on fetch (or refspec-ensure) success: PushRefspec is constructed
	// per invocation and passed on the command line, never read from
	// .git/config, so pushing this writer's own ops depends on neither step.
	// A peer's broken chain or a dead remote's fetch failure must never
	// strand this writer's unpushed ops (WRIT-270). They fold into one
	// syncErr below, fetch taking precedence when both are set: it is the
	// earlier stage, and it is the one that explains a dead remote.
	//
	// This is why checkRemoteAvailable above has to run before either step
	// rather than folding into fetchErr the way Ensure's own failures do:
	// letting an unconfigured remote merely set fetchErr would still let
	// Push below run against it, since Push is unconditional on fetchErr.
	var fetchErr, pushErr error

	// 1. Ensure fetch refspec
	if _, err := s.syncClient.Ensure(ctx, remote); err != nil {
		fetchErr = fmt.Errorf("ensure refspecs: %w", err)
	}

	// 2. Fetch remote operations
	var fetchRes *writsync.FetchResult
	var stopTipsBeforeFetch []plumbing.Hash
	chainsBefore, _ := dag.Chains(s.storer)
	for _, c := range chainsBefore {
		if c.Tip != plumbing.ZeroHash {
			stopTipsBeforeFetch = append(stopTipsBeforeFetch, c.Tip)
		}
	}

	if fetchErr == nil {
		var err error
		fetchRes, err = s.syncClient.Fetch(ctx, remote)
		if err != nil {
			fetchErr = err
		}
	}

	opsFetched := 0
	if fetchRes != nil {
		opsFetched = writsync.CountChainUpdates(s.storer, fetchRes.Updates, stopTipsBeforeFetch)
	}

	// 3. Push local operations if identity is configured. Unconditional on
	// fetchErr: see the note above the fetchErr/pushErr declarations.
	opsPushed := 0
	var pushRes *writsync.PushResult
	if s.hasIdentity && s.identity.WriterID != "" {
		var stopTipsBeforePush []plumbing.Hash
		chainsBeforePush, err := dag.Chains(s.storer)
		if err == nil {
			for _, c := range chainsBeforePush {
				if c.Ref.Remote == remote && c.Tip != plumbing.ZeroHash {
					stopTipsBeforePush = append(stopTipsBeforePush, c.Tip)
				}
			}
		}
		pushRes, pushErr = s.syncClient.Push(ctx, remote)
		if pushRes != nil {
			opsPushed = writsync.CountChainUpdates(s.storer, pushRes.Updates, stopTipsBeforePush)
		}
	}

	syncErr := fetchErr
	if syncErr == nil {
		syncErr = pushErr
	}

	// Invalidate the producer-vocabularies cache once the fetch step has
	// completed, unconditionally rather than conditioned on fetchRes having
	// updates (WRIT-202 item 3): a fetch can move a peer's chain in a way
	// Store.noteAppend's own rolled-forward bookkeeping never observes, so
	// waiting for vocabFreshnessWindow to expire on its own would let
	// vocabulariesForAppend serve a snapshot this fetch just made stale.
	// One map clear against a network round trip is not worth conditioning
	// on the fetch result's shape — the conditional version is the one that
	// gets a corner case wrong.
	s.invalidateVocabularies()

	// 4. Refresh projection (runs even if transport errored)
	refreshStats, refreshErr := s.Refresh(ctx)

	// Record sync cursors in local DB if sync succeeded
	if syncErr == nil {
		now := time.Now().UTC()
		chains, err := dag.Chains(s.storer)
		if err == nil {
			for refName, chain := range chains {
				if chain.Ref.Remote == remote || (chain.Ref.Remote == "" && s.hasIdentity && chain.Ref.WriterID == s.identity.WriterID) {
					_ = s.projection.SetSyncCursor(remote, refName, chain.Tip.String(), now)
				}
			}
		}
	}

	// 5. Compute remaining unsynced count
	unsynced, _ := s.countUnsynced(ctx, remote)

	objectsTouched := 0
	rejected := 0
	if refreshErr == nil {
		objectsTouched = refreshStats.ObjectsTouched
		rejected = len(refreshStats.Rejections)
	}

	result := SyncResult{
		OpsFetched:     opsFetched,
		OpsPushed:      opsPushed,
		ObjectsTouched: objectsTouched,
		Unsynced:       unsynced,
		Rejected:       rejected,
	}

	if syncErr != nil {
		return result, s.wrapSyncError(remote, syncErr, unsynced)
	}

	if refreshErr != nil {
		return result, fmt.Errorf("writ: refresh after sync: %w", refreshErr)
	}

	return result, nil
}

// checkRemoteAvailable validates remote's name and confirms it is
// configured (remote.<remote>.url or remote.<remote>.pushurl set) before
// Sync does anything else. It never writes to .git/config -- RemoteConfigured
// is a read-only probe -- so a rejection here leaves .git/config untouched,
// unlike the old
// Ensure-only guard this replaces as the entry point (Ensure and
// cmd/writ/init.go's direct Ensure calls still run the same checks
// themselves; this is what additionally keeps Push from being attempted
// against a remote that was never configured at all).
func (s *Store) checkRemoteAvailable(ctx context.Context, remote string) error {
	if err := writsync.ValidateRemoteName(remote); err != nil {
		return &SyncError{
			Remote:  remote,
			Kind:    string(writsync.FailureKindInvalidName),
			Message: err.Error(),
			Err:     writsync.ErrInvalidRemoteName,
		}
	}

	configured, err := s.syncClient.RemoteConfigured(ctx, remote)
	if err != nil {
		return s.wrapSyncError(remote, err, 0)
	}
	if !configured {
		return &SyncError{
			Remote:  remote,
			Kind:    string(writsync.FailureKindNotFound),
			Message: fmt.Sprintf("remote %q is not configured", remote),
			Err:     writsync.ErrUnknownRemote,
		}
	}
	return nil
}

func (s *Store) wrapSyncError(remote string, err error, unsynced int) error {
	if err == nil {
		return nil
	}
	var gitErr *writsync.GitError
	if errors.As(err, &gitErr) {
		msg := gitErr.Stderr
		if msg == "" && gitErr.Err != nil {
			msg = gitErr.Err.Error()
		}
		if msg == "" {
			msg = "git transport failed"
		}
		return &SyncError{
			Remote:    remote,
			Kind:      string(gitErr.Kind),
			Message:   msg,
			Advice:    gitErr.Advice,
			Retryable: gitErr.Retryable(),
			Unsynced:  unsynced,
			Err:       gitErr.Err,
		}
	}

	return &SyncError{
		Remote:    remote,
		Kind:      string(writsync.FailureKindUnknown),
		Message:   err.Error(),
		Advice:    "",
		Retryable: false,
		Unsynced:  unsynced,
		Err:       err,
	}
}

// SyncStatus reports the number of local operations not yet pushed to the
// remote. It is offline: unlike Sync, it never confirms the remote is
// actually configured (remote.<remote>.url or remote.<remote>.pushurl set),
// only that its name is syntactically valid -- ComputeStatus works entirely
// from local chain refs and this remote's last-fetched tracking frontier,
// so it does not need the remote to exist, let alone be reachable.
//
// The syntax check still runs, though: a syntactically invalid remote name
// (e.g. "a b", "--upload-pack=...") is a usage error under "writ sync",
// exit 2, and SyncStatus rejecting the same names is what keeps
// "writ sync --status" reporting the same contract instead of a clean
// success for an argument the very next "writ sync" call would refuse
// (round-1 review finding).
//
// The rejection still carries the true unsynced count, same as every
// failure path of Sync itself: ComputeStatus is pure local work -- chain
// refs and this remote's last-fetched tracking frontier, nothing that
// needs the remote to be configured or reachable -- so there is no reason
// for a rejected name like "a b" to report a fictional zero while
// "writ sync" (also rejecting it) reports the real count for the identical
// repository (round-3 review finding: the two used to disagree).
//
// One name is carved out of that guarantee: an empty one reports
// Unsynced: 0, not the real count. ComputeStatus rejects an empty remote
// outright, because chain.Ref.Remote == "" is its own sentinel for a local
// chain -- without that guard a writer's local chains would read as the
// remote tracking frontier. So the empty string is the one input where
// ComputeStatus does need a well-formed name, and the 0 is not special-
// cased away: an empty name is a usage error caught before any remote is
// involved, so there is no remote for a count to be about. Both modes
// agree on the 0, so round-3's property (the two must not disagree) still
// holds. Every other syntactically invalid name still carries the real
// count.
func (s *Store) SyncStatus(ctx context.Context, remote string) (SyncStatus, error) {
	if s == nil {
		return SyncStatus{}, fmt.Errorf("writ: store is nil")
	}
	// As in Sync, an empty remote name is left to ValidateRemoteName below
	// rather than short-circuited here, so it reports invalid-name like
	// every other syntactically unusable name.
	var writerID identity.WriterID
	if s.hasIdentity {
		writerID = s.identity.WriterID
	}

	if err := writsync.ValidateRemoteName(remote); err != nil {
		unsynced := 0
		if status, statusErr := writsync.ComputeStatus(s.storer, writerID, remote); statusErr == nil {
			unsynced = status.Unsynced
		}
		return SyncStatus{}, &SyncError{
			Remote:   remote,
			Kind:     string(writsync.FailureKindInvalidName),
			Message:  err.Error(),
			Unsynced: unsynced,
			Err:      writsync.ErrInvalidRemoteName,
		}
	}

	status, err := writsync.ComputeStatus(s.storer, writerID, remote)
	if err != nil {
		return SyncStatus{}, err
	}

	cursors, err := s.projection.SyncCursors(remote)
	var lastSyncedAt *time.Time
	if err == nil && len(cursors) > 0 {
		var latest time.Time
		for _, c := range cursors {
			if c.LastSyncedAt.After(latest) {
				latest = c.LastSyncedAt
			}
		}
		if !latest.IsZero() {
			lastSyncedAt = &latest
		}
	}

	var byType []TypeUnsynced
	if len(status.ByType) > 0 {
		byType = make([]TypeUnsynced, len(status.ByType))
		for i, bt := range status.ByType {
			byType[i] = TypeUnsynced{
				ObjectType: bt.ObjectType,
				Unsynced:   bt.Unsynced,
			}
		}
	}

	return SyncStatus{
		Remote:       remote,
		Unsynced:     status.Unsynced,
		ByType:       byType,
		Diverged:     status.Diverged,
		LastSyncedAt: lastSyncedAt,
	}, nil
}

func (s *Store) countUnsynced(ctx context.Context, remote string) (int, error) {
	if !s.hasIdentity || s.identity.WriterID == "" {
		return 0, nil
	}

	status, err := writsync.ComputeStatus(s.storer, s.identity.WriterID, remote)
	if err != nil {
		return 0, err
	}

	return status.Unsynced, nil
}
