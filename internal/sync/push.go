package sync

import (
	"context"
	"fmt"
	"strings"

	"github.com/writtendev/writ/internal/dag"
)

// PushStatus represents the status of an individual ref in porcelain push output.
type PushStatus string

const (
	// PushStatusOK indicates the ref was updated via fast-forward.
	PushStatusOK PushStatus = "ok"

	// PushStatusNew indicates a new ref was created on the remote.
	PushStatusNew PushStatus = "new"

	// PushStatusUpToDate indicates the ref was already up to date on the remote.
	PushStatusUpToDate PushStatus = "up-to-date"

	// PushStatusDeleted indicates the ref was deleted on the remote.
	PushStatusDeleted PushStatus = "deleted"

	// PushStatusForced indicates the ref was forcibly updated.
	PushStatusForced PushStatus = "forced"

	// PushStatusRejected indicates the ref update was rejected.
	PushStatusRejected PushStatus = "rejected"
)

// PushedRef records the porcelain push outcome for a single ref mapping.
type PushedRef struct {
	Flag    string     `json:"flag"`
	FromRef string     `json:"from_ref"`
	ToRef   string     `json:"to_ref"`
	Summary string     `json:"summary"`
	Status  PushStatus `json:"status"`
}

// PushResult holds the outcome of a git push operation.
type PushResult struct {
	Remote     string        `json:"remote"`
	Refspec    string        `json:"refspec"`
	PushedRefs []PushedRef   `json:"pushed_refs"`
	Updates    []ChainUpdate `json:"updates"`
	RawStdout  string        `json:"raw_stdout,omitempty"`
	RawStderr  string        `json:"raw_stderr,omitempty"`
}

// Push executes git push --porcelain <remote> <push-refspec> using system git,
// pushing only the local writer's namespace (refs/writ/<writer-id>/*).
//
// Push never uses --force; operations fast-forward by construction.
// A non-nil *PushResult may be returned alongside a non-nil error if git exits nonzero after a partial push.
func (c *Client) Push(ctx context.Context, remote string) (*PushResult, error) {
	if remote == "" {
		return nil, fmt.Errorf("sync: remote name cannot be empty")
	}
	if c.identity.WriterID == "" {
		return nil, fmt.Errorf("sync: writer-id cannot be empty")
	}

	refspec := PushRefspec(c.identity.WriterID)

	c.mu.Lock()
	defer c.mu.Unlock()

	before, err := dag.Chains(c.storer)
	if err != nil {
		return nil, fmt.Errorf("sync: read chains before push: %w", err)
	}

	stdout, stderr, runErr := c.runGit(ctx, "push", "--porcelain", remote, refspec)

	pushedRefs := parsePushPorcelain(string(stdout))

	after, err := dag.Chains(c.storer)
	if err != nil {
		after = before
	}

	updates := diffChains(before, after)

	pushRes := &PushResult{
		Remote:     remote,
		Refspec:    refspec,
		PushedRefs: pushedRefs,
		Updates:    updates,
		RawStdout:  string(stdout),
		RawStderr:  string(stderr),
	}

	if runErr != nil {
		return pushRes, c.classifyGitError(remote, []string{"push", "--porcelain", remote, refspec}, runErr, stderr, stdout)
	}

	return pushRes, nil
}

// parsePushPorcelain parses the porcelain v1 output produced by git push --porcelain.
func parsePushPorcelain(stdout string) []PushedRef {
	var result []PushedRef
	lines := strings.Split(stdout, "\n")

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if len(line) < 2 {
			continue
		}
		if strings.HasPrefix(line, "To ") || line == "Done" || strings.HasPrefix(line, "Everything up-to-date") {
			continue
		}
		if strings.HasPrefix(line, "error:") || strings.HasPrefix(line, "hint:") {
			continue
		}

		flag := string(line[0])
		rest := strings.TrimSpace(line[1:])
		if rest == "" {
			continue
		}

		var refPart, summary string
		fields := strings.Split(rest, "\t")
		if len(fields) >= 2 {
			refPart = fields[0]
			summary = strings.TrimSpace(strings.Join(fields[1:], " "))
		} else {
			f := strings.Fields(rest)
			if len(f) >= 2 {
				refPart = f[0]
				summary = strings.Join(f[1:], " ")
			} else if len(f) == 1 {
				refPart = f[0]
			}
		}

		fromRef, toRef, found := strings.Cut(refPart, ":")
		if !found {
			fromRef = refPart
			toRef = refPart
		}

		var status PushStatus
		switch flag {
		case "*":
			status = PushStatusNew
		case "=":
			status = PushStatusUpToDate
		case " ":
			status = PushStatusOK
		case "-":
			status = PushStatusDeleted
		case "+":
			status = PushStatusForced
		case "!":
			status = PushStatusRejected
		default:
			status = PushStatus(flag)
		}

		result = append(result, PushedRef{
			Flag:    flag,
			FromRef: fromRef,
			ToRef:   toRef,
			Summary: summary,
			Status:  status,
		})
	}

	return result
}
