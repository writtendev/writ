package writ

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	writsync "github.com/writtendev/writ/engine/sync"
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

	// Rejected is the number of op commits this sync's projection refresh
	// could not accept: a malformed peer op rejected on reader
	// validation, or an op commit naming an object absent from this
	// clone (dag.RejectObjectUnavailable — e.g. excluded by a partial or
	// shallow clone's fetch filter, engine-local and not itself a
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
// On transport failure, Sync still refreshes the projection cache and returns the remaining
// unsynced count wrapped in a *SyncError.
func (s *Store) Sync(ctx context.Context, remote string) (SyncResult, error) {
	if s == nil {
		return SyncResult{}, fmt.Errorf("writ: store is nil")
	}
	if remote == "" {
		return SyncResult{}, fmt.Errorf("writ: remote cannot be empty")
	}

	// fetchErr and pushErr are tracked separately because push must not be
	// gated on fetch (or refspec-ensure) success: PushRefspec is constructed
	// per invocation and passed on the command line, never read from
	// .git/config, so pushing this writer's own ops depends on neither step.
	// A peer's broken chain or a dead remote's fetch failure must never
	// strand this writer's unpushed ops (WRIT-270). They fold into one
	// syncErr below, fetch taking precedence when both are set: it is the
	// earlier stage, and it is the one that explains a dead remote.
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

// SyncStatus reports the number of local operations not yet pushed to the remote.
func (s *Store) SyncStatus(ctx context.Context, remote string) (SyncStatus, error) {
	if s == nil {
		return SyncStatus{}, fmt.Errorf("writ: store is nil")
	}
	if remote == "" {
		return SyncStatus{}, fmt.Errorf("writ: remote cannot be empty")
	}

	var writerID identity.WriterID
	if s.hasIdentity {
		writerID = s.identity.WriterID
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
