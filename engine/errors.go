package writ

import (
	"errors"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/gitdir"
	"github.com/writtendev/writ/internal/projection"
	writsync "github.com/writtendev/writ/internal/sync"
)

// RejectError is returned when an op commit or payload fails validation --
// reader validation of an op that arrived, or producer validation of one
// about to be signed (spec/op-envelope.md). Its two fields, Reason and
// Err, leak nothing git-shaped.
type RejectError = codec.RejectError

// RejectSchemaViolation is the RejectReason a RejectError carries when an
// op's body violates the type it declares against the schema folded from
// the log (cmd/writ's renderObjectMutationErr). RejectReason itself, and
// RejectObjectUnavailable, are already public (see query.go); this is the
// one further member cmd/writ compares against, not the whole RejectReason
// catalogue -- unlike FailureKind and VerificationOutcome below, this is
// not one of the two closed catalogues the WRIT-296 plan's orchestrator
// decision widened to a complete export.
const RejectSchemaViolation = codec.RejectSchemaViolation

// VerificationOutcome classifies the result of verifying an op commit's
// signature (codec.Verify, surfaced as Object.Verification and
// ObjectResult.Verification, both plain strings). Exported as a complete
// catalogue, not only VerificationValid -- the one member cmd/writ
// compares against -- per WRIT-296's orchestrator decision 2: docs/cli-json.md
// already publishes every member in --json, so a consumer branching on
// e.g. "wrong-key" hardcodes a string either way, and a partial enum is a
// worse promise than a complete one.
type VerificationOutcome = codec.VerificationOutcome

const (
	// VerificationValid indicates the signature is cryptographically
	// valid and the key is authorized in the trust store.
	VerificationValid = codec.OutcomeValid

	// VerificationUnsigned indicates the commit has no signature header.
	VerificationUnsigned = codec.OutcomeUnsigned

	// VerificationWrongKey indicates the signature is cryptographically
	// valid, but the key is not authorized for the author.
	VerificationWrongKey = codec.OutcomeWrongKey

	// VerificationPayloadMutated indicates the signature does not match
	// the commit payload bytes.
	VerificationPayloadMutated = codec.OutcomePayloadMutated

	// VerificationCorruptedSignature indicates the signature header is
	// malformed or unparseable.
	VerificationCorruptedSignature = codec.OutcomeCorruptedSignature
)

// FailureKind classifies a SyncError's Kind field -- a git transport or
// remote operation failure. Exported as a complete catalogue, not only the
// five members cmd/writ compares against, for the same reason
// VerificationOutcome is: docs/cli-json.md already publishes every member
// in --json.
type FailureKind = writsync.FailureKind

const (
	// FailureKindAuth indicates authentication rejection.
	FailureKindAuth = writsync.FailureKindAuth

	// FailureKindNetwork indicates a network or connectivity failure.
	FailureKindNetwork = writsync.FailureKindNetwork

	// FailureKindRejected indicates the remote rejected a ref update
	// (non-fast-forward, hook decline).
	FailureKindRejected = writsync.FailureKindRejected

	// FailureKindNotFound indicates the remote or repository was not
	// found.
	FailureKindNotFound = writsync.FailureKindNotFound

	// FailureKindInvalidName indicates the remote name itself is
	// syntactically unusable, distinct from FailureKindNotFound, which
	// means the name is well-formed but not configured.
	FailureKindInvalidName = writsync.FailureKindInvalidName

	// FailureKindCanceled indicates the operation was canceled or timed
	// out.
	FailureKindCanceled = writsync.FailureKindCanceled

	// FailureKindUnknown indicates an unclassified failure.
	FailureKindUnknown = writsync.FailureKindUnknown
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
