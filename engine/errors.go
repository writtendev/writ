package writ

import (
	"errors"

	"github.com/writtendev/writ/internal/gitdir"
	"github.com/writtendev/writ/internal/projection"
	writsync "github.com/writtendev/writ/internal/sync"
)

var (
	// ErrNoIdentity is returned when a write operation is attempted but no writer identity
	// is configured in git config (run 'writ init' to configure).
	ErrNoIdentity = errors.New("identity: no writer identity configured (run 'writ init' to configure)")

	// ErrNoSigningKey is returned when a write operation is attempted but no SSH signing key
	// is configured in git config (run 'writ init' to configure).
	ErrNoSigningKey = errors.New("identity: no signing key configured (run 'writ init' to configure)")

	// ErrNotFound is returned when an object is not found.
	ErrNotFound = projection.ErrNotFound

	// ErrNonFastForward indicates that a push or fetch update was rejected because
	// it was not a fast-forward.
	ErrNonFastForward = writsync.ErrNonFastForward

	// ErrUnknownRemote indicates that the requested git remote is not configured
	// or could not be found.
	ErrUnknownRemote = writsync.ErrUnknownRemote

	// ErrAuth indicates that git remote authentication or credentials failed.
	ErrAuth = writsync.ErrAuth

	// ErrNetwork indicates that the git remote is unreachable over the network.
	ErrNetwork = writsync.ErrNetwork

	// ErrRefRejected indicates that the remote rejected one or more ref updates.
	ErrRefRejected = writsync.ErrRefRejected

	// ErrNotRepository is returned (wrapped) by ResolveGitDir and Open when
	// the given path is not inside a git repository.
	ErrNotRepository = gitdir.ErrNotRepository

	// ErrStoreOpen is returned (wrapped) by Open for every error caused by
	// the repository or its local state failing to open: path resolution,
	// git storage, DAG store, projection cache directory or database, and
	// sync client. It does not cover schema resolution/apply failures —
	// those are log-content errors, not open failures.
	ErrStoreOpen = errors.New("writ: cannot open store")
)
