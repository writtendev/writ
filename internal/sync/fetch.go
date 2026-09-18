package sync

import (
	"context"
	"fmt"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/internal/dag"
)

// ChainUpdate represents a discovered change to a local or remote-tracking writ chain.
type ChainUpdate struct {
	Ref dag.ChainRef  `json:"ref"`
	Old plumbing.Hash `json:"old"`
	New plumbing.Hash `json:"new"`
}

// IsCreate reports whether this update created a new chain reference.
func (u ChainUpdate) IsCreate() bool {
	return u.Old == plumbing.ZeroHash && u.New != plumbing.ZeroHash
}

// IsDelete reports whether this update removed a chain reference.
func (u ChainUpdate) IsDelete() bool {
	return u.Old != plumbing.ZeroHash && u.New == plumbing.ZeroHash
}

// IsUpdate reports whether this update moved an existing chain reference forward or backward.
func (u ChainUpdate) IsUpdate() bool {
	return u.Old != plumbing.ZeroHash && u.New != plumbing.ZeroHash && u.Old != u.New
}

// FetchResult holds the outcome of a git fetch operation.
type FetchResult struct {
	Remote    string        `json:"remote"`
	Updates   []ChainUpdate `json:"updates"`
	RawStderr string        `json:"raw_stderr,omitempty"`
}

// Fetch executes git fetch <remote> using system git and computes ref updates by
// diffing discovered chains before and after the operation.
//
// System git relies on the fetch refspec configured in .git/config (verified by Ensure).
// No implicit --prune or --force is passed.
func (c *Client) Fetch(ctx context.Context, remote string) (*FetchResult, error) {
	if remote == "" {
		return nil, fmt.Errorf("sync: remote name cannot be empty")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	before, err := dag.Chains(c.storer)
	if err != nil {
		return nil, fmt.Errorf("sync: read chains before fetch: %w", err)
	}

	stdout, stderr, err := c.runGit(ctx, "fetch", remote)
	if err != nil {
		return nil, c.classifyGitError(remote, []string{"fetch", remote}, err, stderr, stdout)
	}

	after, err := dag.Chains(c.storer)
	if err != nil {
		return nil, fmt.Errorf("sync: read chains after fetch: %w", err)
	}

	updates := diffChains(before, after)

	return &FetchResult{
		Remote:    remote,
		Updates:   updates,
		RawStderr: string(stderr),
	}, nil
}

// diffChains compares two snapshots of discovered writ chains and returns sorted updates.
func diffChains(before, after map[string]dag.DiscoveredChain) []ChainUpdate {
	var updates []ChainUpdate

	for name, afterChain := range after {
		beforeChain, exists := before[name]
		if !exists {
			updates = append(updates, ChainUpdate{
				Ref: afterChain.Ref,
				Old: plumbing.ZeroHash,
				New: afterChain.Tip,
			})
		} else if beforeChain.Tip != afterChain.Tip {
			updates = append(updates, ChainUpdate{
				Ref: afterChain.Ref,
				Old: beforeChain.Tip,
				New: afterChain.Tip,
			})
		}
	}

	for name, beforeChain := range before {
		if _, exists := after[name]; !exists {
			updates = append(updates, ChainUpdate{
				Ref: beforeChain.Ref,
				Old: beforeChain.Tip,
				New: plumbing.ZeroHash,
			})
		}
	}

	sort.Slice(updates, func(i, j int) bool {
		return updates[i].Ref.Name.String() < updates[j].Ref.Name.String()
	})

	return updates
}
