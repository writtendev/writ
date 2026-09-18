package sync

import (
	"errors"
	"fmt"
	"strings"
)

// FailureKind classifies a git transport or remote operation failure.
type FailureKind string

const (
	// FailureKindAuth indicates authentication rejection.
	FailureKindAuth FailureKind = "auth"

	// FailureKindNetwork indicates a network or connectivity failure.
	FailureKindNetwork FailureKind = "network"

	// FailureKindRejected indicates the remote rejected a ref update (non-fast-forward, hook decline).
	FailureKindRejected FailureKind = "rejected"

	// FailureKindNotFound indicates the remote or repository was not found.
	FailureKindNotFound FailureKind = "not-found"

	// FailureKindCanceled indicates the operation was canceled or timed out.
	FailureKindCanceled FailureKind = "canceled"

	// FailureKindUnknown indicates an unclassified failure.
	FailureKindUnknown FailureKind = "unknown"
)

var (
	// ErrNonFastForward indicates that a push or fetch update was rejected because
	// it was not a fast-forward.
	ErrNonFastForward = errors.New("non-fast-forward update rejected")

	// ErrUnknownRemote indicates that the requested git remote is not configured
	// or could not be found.
	ErrUnknownRemote = errors.New("unknown remote")

	// ErrAuth indicates that git remote authentication failed.
	ErrAuth = errors.New("authentication failed")

	// ErrNetwork indicates that the git remote is unreachable over the network.
	ErrNetwork = errors.New("network unreachable")

	// ErrRefRejected indicates that the remote rejected one or more ref updates.
	ErrRefRejected = errors.New("remote ref rejected")
)

// GitError records a failure when executing a system git command with structured classification.
type GitError struct {
	Remote   string
	Args     []string
	ExitCode int
	Stderr   string
	Err      error
	Kind     FailureKind
	Advice   string
}

// Retryable reports whether this failure kind is transient and safe to retry automatically.
func (e *GitError) Retryable() bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	case FailureKindNetwork, FailureKindCanceled:
		return true
	default:
		return false
	}
}

func (e *GitError) Error() string {
	var parts []string
	if e.Remote != "" {
		parts = append(parts, fmt.Sprintf("git (remote %s) %s (exit %d)", e.Remote, strings.Join(e.Args, " "), e.ExitCode))
	} else {
		parts = append(parts, fmt.Sprintf("git %s (exit %d)", strings.Join(e.Args, " "), e.ExitCode))
	}
	if e.Err != nil && !errors.Is(e.Err, e) {
		parts = append(parts, e.Err.Error())
	}
	if e.Stderr != "" {
		trimmed := strings.TrimSpace(e.Stderr)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, ": ")
}

// Unwrap returns the underlying classified error (such as ErrAuth, ErrNetwork, ErrNonFastForward, ErrRefRejected, or ErrUnknownRemote).
func (e *GitError) Unwrap() error {
	return e.Err
}
