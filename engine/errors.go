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

	// ErrInvalidRemoteName indicates that a remote name is syntactically
	// unusable (empty, "-"-leading, or otherwise not a valid
	// fetch-refspec destination component) rather than merely
	// unconfigured.
	ErrInvalidRemoteName = writsync.ErrInvalidRemoteName

	// ErrAuth indicates that git remote authentication or credentials failed.
	ErrAuth = writsync.ErrAuth

	// ErrNetwork indicates that the git remote is unreachable over the network.
	ErrNetwork = writsync.ErrNetwork

	// ErrRefRejected indicates that the remote rejected one or more ref updates.
	ErrRefRejected = writsync.ErrRefRejected

	// ErrNotRepository is returned (wrapped) by ResolveGitDir and Open when
	// the given path is not inside a git repository.
	ErrNotRepository = gitdir.ErrNotRepository

	// ErrRejectedOps is returned wrapped alongside ErrNotFound by
	// Objects.Get when the enumeration pass that found no ops for the
	// requested object id also failed to read at least one op commit
	// in the repository. errors.Is(err, ErrNotFound) still holds, so a
	// caller that only checks for ErrNotFound today is unaffected; a
	// caller that also checks for ErrRejectedOps learns that the
	// object's absence is not certain.
	//
	// The rejections behind this sentinel are repository-wide, not
	// attributable to the object id passed to Get: a commit that fails
	// reader validation does so before its payload — the bytes carrying
	// object_id — ever decodes, so there is no way to know which object,
	// if any, it concerned. This sentinel says only that the repository
	// holds op commits that could not be read, which means the requested
	// id's absence cannot be trusted as definitive. It does not, and
	// cannot, say those commits concern the requested object.
	ErrRejectedOps = errors.New("this object's absence is not certain")

	// ErrStoreOpen is returned (wrapped) by Open for every error caused by
	// the repository or its local state failing to open: path resolution,
	// git storage, DAG store, projection cache directory or database, and
	// sync client. It does not cover schema resolution/apply failures —
	// those are log-content errors, not open failures.
	ErrStoreOpen = errors.New("writ: cannot open store")
)
