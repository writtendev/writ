package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/internal/identity"
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

// ValidateRemoteName reports whether name is syntactically usable as a git
// remote name. It runs four boring checks, in order:
//
//  1. empty -- rejected outright.
//  2. "-"-leading -- rejected because a git subcommand parses a leading "-"
//     as a flag rather than a positional argument. This is the primary
//     defense against the argument-injection hole "writ sync -- <name>"
//     opened (a "-"-leading remote name reaching git fetch/push verified to
//     execute an attacker-controlled --upload-pack); passing
//     "--end-of-options" on every fetch/push/config call closes it as a
//     second, independent layer at the transport calls themselves.
//  3. containing "/" -- git's own valid_remote_nick rule, and it keeps this
//     remote's fetch-refspec destination (refs/remotes/<name>/writ/*)
//     unambiguous.
//  4. unusable as a fetch-refspec destination component -- checked via
//     go-git's own reference-name validation, which already implements
//     refname rules 1 and 3-10 (space, tab, newline, "~ ^ : ? * [ \",
//     "..", "@{", "@", ".lock", leading ".", trailing ".") per
//     "/"-separated path component. go-git is already a dependency (local
//     object I/O), so this adds none.
func ValidateRemoteName(name string) error {
	if name == "" {
		return fmt.Errorf("sync: %w: empty remote name", ErrInvalidRemoteName)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("sync: %w %q: must not start with \"-\"", ErrInvalidRemoteName, name)
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("sync: %w %q: must not contain \"/\"", ErrInvalidRemoteName, name)
	}
	if err := plumbing.ReferenceName("refs/remotes/" + name + "/writ/x").Validate(); err != nil {
		return fmt.Errorf("sync: %w %q: %v", ErrInvalidRemoteName, name, err)
	}
	return nil
}

// RemoteConfigured reports whether remote.<remote>.url is configured in
// .git/config -- the existence probe Ensure and the upfront guard in
// engine's Store.Sync both need before doing anything else. Plain "git
// remote" is not the right probe: a remote left holding only a phantom
// fetch key from the WRIT-283 bug still lists happily with no url at all.
// Exported so Store.Sync can run the same check before Ensure, Fetch, or
// Push -- see that function's doc comment for why it must.
//
// --get-all, not --get: a remote configured with two urls (push
// mirroring) makes --get exit 2, which would misreport as unconfigured.
func (c *Client) RemoteConfigured(ctx context.Context, remote string) (bool, error) {
	configKey := fmt.Sprintf("remote.%s.url", remote)
	stdout, stderr, err := c.runGit(ctx, "config", "--get-all", "--null", "--end-of-options", configKey)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			// Exit code 1 from git config --get-all means key is not set.
			return false, nil
		}
		return false, c.classifyGitError(remote, []string{"config", "--get-all", "--null", "--end-of-options", configKey}, err, stderr, stdout)
	}
	return len(bytes.TrimSpace(stdout)) > 0, nil
}

// Check inspects .git/config for the given remote's fetch refspecs and reports any drift.
func (c *Client) Check(ctx context.Context, remote string) (RefspecStatus, error) {
	if err := ValidateRemoteName(remote); err != nil {
		return RefspecStatus{}, err
	}

	expected := FetchRefspec(remote)
	configKey := fmt.Sprintf("remote.%s.fetch", remote)

	stdout, stderr, err := c.runGit(ctx, "config", "--get-all", "--null", "--end-of-options", configKey)
	var current []string
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			// Exit code 1 from git config --get-all means key is not set.
			current = []string{}
		} else {
			return RefspecStatus{}, c.classifyGitError(remote, []string{"config", "--get-all", "--null", "--end-of-options", configKey}, err, stderr, stdout)
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
//
// Ensure confirms the remote is actually configured (remote.<remote>.url is
// set) before it ever writes anything: this is what closes the phantom-
// remote bug (WRIT-283) where "writ sync nosuchremote" and "writ init
// nosuchremote" -- init calls Ensure directly, once per remote -- both left
// a url-less [remote "nosuchremote"] fetch-only section behind, and where a
// malformed name (e.g. "a b") wrote an invalid refspec that broke every
// subsequent plain "git remote" in the repo. Nothing is written for a
// remote that is not configured or whose name Check has already rejected.
func (c *Client) Ensure(ctx context.Context, remote string) (RefspecStatus, error) {
	status, err := c.Check(ctx, remote)
	if err != nil {
		return RefspecStatus{}, err
	}

	configured, err := c.RemoteConfigured(ctx, remote)
	if err != nil {
		return status, err
	}
	if !configured {
		return status, fmt.Errorf("sync: remote %q is not configured: %w", remote, ErrUnknownRemote)
	}

	if status.State == StatusValid {
		return status, nil
	}

	configKey := fmt.Sprintf("remote.%s.fetch", remote)

	// Unset existing writ refspecs with regex pattern matching ^(\+)?refs/writ/
	// or any writ-related fetch refspec.
	stdout, stderr, err := c.runGit(ctx, "config", "--unset-all", "--end-of-options", configKey, `^(\+)?refs/writ/`)
	if err != nil {
		var exitErr *exec.ExitError
		// Exit code 5 means no section/name was found to unset; exit code 1 means key not found.
		// Both are acceptable when unsetting.
		if errors.As(err, &exitErr) && (exitErr.ExitCode() == 5 || exitErr.ExitCode() == 1) {
			// No entries matched; continue to add
		} else {
			return status, c.classifyGitError(remote, []string{"config", "--unset-all", "--end-of-options", configKey, `^(\+)?refs/writ/`}, err, stderr, stdout)
		}
	}

	// Add canonical expected refspec
	stdout, stderr, err = c.runGit(ctx, "config", "--add", "--end-of-options", configKey, status.Expected)
	if err != nil {
		return status, c.classifyGitError(remote, []string{"config", "--add", "--end-of-options", configKey, status.Expected}, err, stderr, stdout)
	}

	// Verify repaired status
	newStatus, err := c.Check(ctx, remote)
	if err != nil {
		return status, err
	}
	newStatus.Repaired = true
	return newStatus, nil
}
