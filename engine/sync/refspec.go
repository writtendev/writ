package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/writtendev/writ/engine/identity"
)

// RefspecState represents the health/drift state of the Writ fetch refspec.
type RefspecState string

const (
	// StatusValid indicates the fetch refspec is correctly configured.
	StatusValid RefspecState = "valid"

	// StatusMissing indicates no writ fetch refspec exists for the remote.
	StatusMissing RefspecState = "missing"

	// StatusUnforced indicates a non-forced (no leading '+') fetch refspec
	// exists: the pre-WRIT-270 canonical form, now drift that Ensure repairs.
	StatusUnforced RefspecState = "unforced"

	// StatusDuplicate indicates multiple writ fetch refspecs exist for the remote.
	StatusDuplicate RefspecState = "duplicate"

	// StatusWrongDestination indicates the fetch refspec does not target refs/remotes/<remote>/writ/*.
	StatusWrongDestination RefspecState = "wrong-destination"
)

// RefspecStatus reports the state of Writ fetch refspecs for a remote in .git/config.
type RefspecStatus struct {
	Remote      string       `json:"remote"`
	Expected    string       `json:"expected"`
	Current     []string     `json:"current"`
	WritEntries []string     `json:"writ_entries"`
	State       RefspecState `json:"state"`
	Repaired    bool         `json:"repaired"`
}

// Valid reports whether the refspec is in the desired valid state.
func (s RefspecStatus) Valid() bool {
	return s.State == StatusValid
}

// FetchRefspec returns the canonical fetch refspec for the given remote:
// +refs/writ/*:refs/remotes/<remote>/writ/*
//
// Per spec/ref-layout.md, the leading '+' forces the fetch: a peer's rewind of
// their own chain lands instead of being rejected as a non-fast-forward, so
// one bad peer can never wedge every other writer's sync (WRIT-270).
func FetchRefspec(remote string) string {
	return fmt.Sprintf("+refs/writ/*:refs/remotes/%s/writ/*", remote)
}

// PushRefspec returns the canonical push refspec for the given writer ID:
// refs/writ/<writer-id>/*:refs/writ/<writer-id>/*
//
// Per spec/ref-layout.md, push refspecs are constructed per invocation and passed
// on the command line; they are never written to .git/config.
func PushRefspec(writerID identity.WriterID) string {
	return fmt.Sprintf("refs/writ/%s/*:refs/writ/%s/*", writerID, writerID)
}

// isWritRefspec returns true if a refspec pattern pertains to the Writ namespace.
func isWritRefspec(refspec string) bool {
	clean := strings.TrimPrefix(refspec, "+")
	if strings.HasPrefix(clean, "refs/writ/") {
		return true
	}
	parts := strings.Split(clean, ":")
	for _, p := range parts {
		if strings.HasPrefix(p, "refs/writ/") || strings.Contains(p, "/writ/") {
			return true
		}
	}
	return false
}

// Check inspects .git/config for the given remote's fetch refspecs and reports any drift.
func (c *Client) Check(ctx context.Context, remote string) (RefspecStatus, error) {
	expected := FetchRefspec(remote)
	configKey := fmt.Sprintf("remote.%s.fetch", remote)

	stdout, stderr, err := c.runGit(ctx, "config", "--get-all", "--null", configKey)
	var current []string
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			// Exit code 1 from git config --get-all means key is not set.
			current = []string{}
		} else {
			return RefspecStatus{}, c.classifyGitError(remote, []string{"config", "--get-all", "--null", configKey}, err, stderr, stdout)
		}
	} else {
		records := bytes.Split(stdout, []byte{0})
		for _, rec := range records {
			if len(rec) > 0 {
				current = append(current, string(rec))
			}
		}
	}

	var writEntries []string
	for _, entry := range current {
		if isWritRefspec(entry) {
			writEntries = append(writEntries, entry)
		}
	}

	var state RefspecState
	switch {
	case len(writEntries) == 0:
		state = StatusMissing
	case len(writEntries) > 1:
		state = StatusDuplicate
	default:
		entry := writEntries[0]
		switch {
		case entry == expected:
			state = StatusValid
		case entry == strings.TrimPrefix(expected, "+"):
			state = StatusUnforced
		default:
			state = StatusWrongDestination
		}
	}

	return RefspecStatus{
		Remote:      remote,
		Expected:    expected,
		Current:     current,
		WritEntries: writEntries,
		State:       state,
		Repaired:    false,
	}, nil
}

// Ensure checks and idempotently repairs the remote's fetch refspec in .git/config.
// If the refspec already matches the canonical format, Ensure is a no-op.
// Otherwise, it unsets any existing writ refspecs using a scoped pattern and adds
// the canonical entry, preserving all unrelated refspecs (e.g. refs/heads/*).
//
// Ensure operates purely on git config and does not require a complete or signing-capable
// identity on the Client (e.g. during 'writ init', before signing keys are configured).
func (c *Client) Ensure(ctx context.Context, remote string) (RefspecStatus, error) {
	status, err := c.Check(ctx, remote)
	if err != nil {
		return RefspecStatus{}, err
	}

	if status.State == StatusValid {
		return status, nil
	}

	configKey := fmt.Sprintf("remote.%s.fetch", remote)

	// Unset existing writ refspecs with regex pattern matching ^(\+)?refs/writ/
	// or any writ-related fetch refspec.
	stdout, stderr, err := c.runGit(ctx, "config", "--unset-all", configKey, `^(\+)?refs/writ/`)
	if err != nil {
		var exitErr *exec.ExitError
		// Exit code 5 means no section/name was found to unset; exit code 1 means key not found.
		// Both are acceptable when unsetting.
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == 5 || exitErr.ExitCode() == 1) {
			// No entries matched; continue to add
		} else {
			return status, c.classifyGitError(remote, []string{"config", "--unset-all", configKey, `^(\+)?refs/writ/`}, err, stderr, stdout)
		}
	}

	// Add canonical expected refspec
	stdout, stderr, err = c.runGit(ctx, "config", "--add", configKey, status.Expected)
	if err != nil {
		return status, c.classifyGitError(remote, []string{"config", "--add", configKey, status.Expected}, err, stderr, stdout)
	}

	// Verify repaired status
	newStatus, err := c.Check(ctx, remote)
	if err != nil {
		return status, err
	}
	newStatus.Repaired = true
	return newStatus, nil
}
