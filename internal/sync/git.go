package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// buildEnv constructs the subprocess environment inheriting os.Environ(),
// forcing LC_ALL=C for locale-independent porcelain/error output, and appending
// any configured custom environment variables.
func (c *Client) buildEnv() []string {
	env := append(os.Environ(), "LC_ALL=C")
	if len(c.env) > 0 {
		env = append(env, c.env...)
	}
	return env
}

// runGit executes the git binary in the client's repository directory.
// It returns captured stdout, stderr, and any execution error.
func (c *Client) runGit(ctx context.Context, args ...string) ([]byte, []byte, error) {
	bin := c.gitBin
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = c.repoDir
	cmd.Env = c.buildEnv()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// ClassifyGitError constructs a *GitError from an exec failure, matching sentinels
// and failure kinds for known failure modes like auth, network, rejection, or not found.
func ClassifyGitError(remote string, args []string, err error, stderr []byte, stdout []byte) *GitError {
	stderrStr := string(stderr)
	stdoutStr := string(stdout)
	combined := stderrStr + "\n" + stdoutStr

	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}

	var (
		kind     FailureKind
		sentinel error
		advice   string
	)

	switch {
	case errors.Is(err, context.Canceled):
		kind = FailureKindCanceled
		sentinel = context.Canceled
		advice = "operation was canceled"

	case errors.Is(err, context.DeadlineExceeded):
		kind = FailureKindCanceled
		sentinel = context.DeadlineExceeded
		advice = "operation timed out"

	// 1. Authentication / credentials
	case strings.Contains(combined, "Permission denied (publickey") ||
		strings.Contains(combined, "Authentication failed") ||
		strings.Contains(combined, "could not read Username") ||
		strings.Contains(combined, "terminal prompts disabled") ||
		strings.Contains(combined, "403 Forbidden") ||
		strings.Contains(combined, "HTTP 401") ||
		strings.Contains(combined, "401 Unauthorized") ||
		strings.Contains(combined, "could not read Password"):
		kind = FailureKindAuth
		sentinel = ErrAuth
		if remote != "" {
			advice = fmt.Sprintf("credentials rejected by %s; check your ssh agent or credential helper", remote)
		} else {
			advice = "credentials rejected; check your ssh agent or credential helper"
		}

	// 2. Network / host unreachable
	case strings.Contains(combined, "Could not resolve host") ||
		strings.Contains(combined, "Connection refused") ||
		strings.Contains(combined, "Connection timed out") ||
		strings.Contains(combined, "Network is unreachable") ||
		strings.Contains(combined, "SSL certificate problem") ||
		strings.Contains(combined, "unable to access") ||
		strings.Contains(combined, "Failed to connect to"):
		kind = FailureKindNetwork
		sentinel = ErrNetwork
		if remote != "" {
			advice = fmt.Sprintf("network or host unreachable for %s; check connection and remote URL", remote)
		} else {
			advice = "network or host unreachable; check connection and remote URL"
		}

	// 3. Not found / repository missing
	case strings.Contains(combined, "Repository not found") ||
		strings.Contains(combined, "repository not found") ||
		strings.Contains(combined, "does not appear to be a git repository") ||
		strings.Contains(combined, "fatal: No such remote") ||
		(remote != "" && strings.Contains(combined, "fatal: '"+remote+"' does not exist")) ||
		(strings.Contains(combined, "remote") && strings.Contains(combined, "not found")):
		kind = FailureKindNotFound
		sentinel = ErrUnknownRemote
		if remote != "" {
			advice = fmt.Sprintf("remote %q not found or repository does not exist", remote)
		} else {
			advice = "repository not found or remote does not exist"
		}

	// 4. Ref update rejected. The advice is path-aware: on the push path
	// (args[0] == "push"), a fetch already succeeded on this sync — a forced
	// fetch refspec (WRIT-270) lands a peer's own rewind rather than
	// rejecting it — so "fetch latest ops before pushing" is simply wrong
	// there. What actually happened is that the remote's copy of this
	// writer's own chain is not an ancestor of the local tip: something
	// wrote into refs/writ/<this-writer-id>/*, or the remote itself was
	// restored/rewound, and fetching does not fix either.
	case strings.Contains(combined, "non-fast-forward") ||
		strings.Contains(combined, "fetch first"):
		kind = FailureKindRejected
		sentinel = ErrNonFastForward
		if len(args) > 0 && args[0] == "push" {
			if remote != "" {
				advice = fmt.Sprintf("remote %s rejected non-fast-forward update; your writer namespace was written to by something other than this chain, or the remote was restored/rewound -- fetching will not fix it", remote)
			} else {
				advice = "rejected non-fast-forward update; your writer namespace was written to by something other than this chain, or the remote was restored/rewound -- fetching will not fix it"
			}
		} else if remote != "" {
			advice = fmt.Sprintf("remote %s rejected non-fast-forward update; fetch latest ops before pushing", remote)
		} else {
			advice = "rejected non-fast-forward update; fetch latest ops before pushing"
		}

	case strings.Contains(combined, "[remote rejected]") ||
		strings.Contains(combined, "[rejected]") ||
		strings.Contains(combined, "pre-receive hook declined") ||
		strings.Contains(combined, "deny updating a hidden ref") ||
		strings.Contains(combined, "hook declined"):
		kind = FailureKindRejected
		sentinel = ErrRefRejected
		if remote != "" {
			advice = fmt.Sprintf("remote %s rejected ref update; check server policy or repository permissions", remote)
		} else {
			advice = "remote rejected ref update; check server policy or repository permissions"
		}

	default:
		kind = FailureKindUnknown
		sentinel = err
		advice = ""
	}

	return &GitError{
		Remote:   remote,
		Args:     args,
		ExitCode: exitCode,
		Stderr:   strings.TrimSpace(stderrStr),
		Err:      sentinel,
		Kind:     kind,
		Advice:   advice,
	}
}

// classifyGitError constructs a *GitError from an exec failure.
func (c *Client) classifyGitError(remote string, args []string, err error, stderr []byte, stdout []byte) *GitError {
	return ClassifyGitError(remote, args, err, stderr, stdout)
}
