package sync

import (
	"fmt"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/identity"
)

// TypeUnsynced reports the number of unsynced operations for a specific collaborative object type.
type TypeUnsynced struct {
	ObjectType string `json:"object_type"`
	Unsynced   int    `json:"unsynced"`
}

// Status represents the computed sync status for a local writer against a remote.
type Status struct {
	Remote   string         `json:"remote"`
	Unsynced int            `json:"unsynced"`
	ByType   []TypeUnsynced `json:"by_type,omitempty"`
	Diverged bool           `json:"diverged,omitempty"`
}

// ComputeStatus computes the reachability set-difference between the local writer's chain tips
// and the remote's tracking frontier, detecting diverged chains and computing per-type breakdowns.
func ComputeStatus(s storage.Storer, writerID identity.WriterID, remote string) (Status, error) {
	if s == nil {
		return Status{Remote: remote}, nil
	}
	if remote == "" {
		return Status{}, fmt.Errorf("sync: remote name cannot be empty")
	}
	if writerID == "" {
		return Status{Remote: remote, Unsynced: 0}, nil
	}

	chains, err := dag.Chains(s)
	if err != nil {
		return Status{}, fmt.Errorf("sync: read chains: %w", err)
	}

	// 1. Collect local writer tips and remote tracking frontier tips
	localChains := make(map[string]plumbing.Hash)
	remoteSelfChains := make(map[string]plumbing.Hash)
	var remoteFrontierTips []plumbing.Hash

	for _, chain := range chains {
		if chain.Tip == plumbing.ZeroHash {
			continue
		}
		if chain.Ref.Remote == remote {
			remoteFrontierTips = append(remoteFrontierTips, chain.Tip)
			if chain.Ref.WriterID == writerID {
				remoteSelfChains[chain.Ref.ObjectType] = chain.Tip
			}
		} else if chain.Ref.Remote == "" && chain.Ref.WriterID == writerID {
			localChains[chain.Ref.ObjectType] = chain.Tip
		}
	}

	// 2. Build the stop set from the remote tracking frontier
	stopSet := make(map[plumbing.Hash]bool)
	var queue []plumbing.Hash
	for _, tip := range remoteFrontierTips {
		if !stopSet[tip] {
			stopSet[tip] = true
			queue = append(queue, tip)
		}
	}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		commit, err := object.GetCommit(s, curr)
		if err != nil {
			continue
		}
		for _, p := range commit.ParentHashes {
			if p != plumbing.ZeroHash && !stopSet[p] {
				stopSet[p] = true
				queue = append(queue, p)
			}
		}
	}

	// 3. Check for divergence on local chains
	diverged := false
	for objType, localTip := range localChains {
		remoteTip, hasRemote := remoteSelfChains[objType]
		if hasRemote && remoteTip != localTip {
			if !isAncestor(s, remoteTip, localTip) {
				diverged = true
			}
		}
	}

	// 4. Compute overall unsynced count across all local chains
	visited := make(map[plumbing.Hash]bool)
	unsyncedTotal := 0
	var localQueue []plumbing.Hash

	for _, tip := range localChains {
		if !stopSet[tip] && !visited[tip] {
			visited[tip] = true
			localQueue = append(localQueue, tip)
			unsyncedTotal++
		}
	}

	for len(localQueue) > 0 {
		curr := localQueue[0]
		localQueue = localQueue[1:]

		commit, err := object.GetCommit(s, curr)
		if err != nil {
			continue
		}
		for _, p := range commit.ParentHashes {
			if p != plumbing.ZeroHash && !stopSet[p] && !visited[p] {
				visited[p] = true
				localQueue = append(localQueue, p)
				unsyncedTotal++
			}
		}
	}

	// 5. Compute per-type unsynced breakdown
	var byType []TypeUnsynced
	var sortedTypes []string
	for objType := range localChains {
		sortedTypes = append(sortedTypes, objType)
	}
	sort.Strings(sortedTypes)

	for _, objType := range sortedTypes {
		tip := localChains[objType]
		typeVisited := make(map[plumbing.Hash]bool)
		typeUnsynced := 0
		var typeQueue []plumbing.Hash

		if !stopSet[tip] {
			typeVisited[tip] = true
			typeQueue = append(typeQueue, tip)
			typeUnsynced++
		}

		for len(typeQueue) > 0 {
			curr := typeQueue[0]
			typeQueue = typeQueue[1:]

			commit, err := object.GetCommit(s, curr)
			if err != nil {
				continue
			}
			for _, p := range commit.ParentHashes {
				if p != plumbing.ZeroHash && !stopSet[p] && !typeVisited[p] {
					typeVisited[p] = true
					typeQueue = append(typeQueue, p)
					typeUnsynced++
				}
			}
		}

		byType = append(byType, TypeUnsynced{
			ObjectType: objType,
			Unsynced:   typeUnsynced,
		})
	}

	return Status{
		Remote:   remote,
		Unsynced: unsyncedTotal,
		ByType:   byType,
		Diverged: diverged,
	}, nil
}

// CountChainUpdates counts unique commits introduced by a set of chain updates,
// using stopTips as the boundary to avoid counting causal parents that already existed.
func CountChainUpdates(s storage.Storer, updates []ChainUpdate, stopTips []plumbing.Hash) int {
	if s == nil || len(updates) == 0 {
		return 0
	}

	stopSet := make(map[plumbing.Hash]bool)
	var stopQueue []plumbing.Hash

	for _, tip := range stopTips {
		if tip != plumbing.ZeroHash && !stopSet[tip] {
			stopSet[tip] = true
			stopQueue = append(stopQueue, tip)
		}
	}
	for _, u := range updates {
		if u.Old != plumbing.ZeroHash && !stopSet[u.Old] {
			stopSet[u.Old] = true
			stopQueue = append(stopQueue, u.Old)
		}
	}

	for len(stopQueue) > 0 {
		curr := stopQueue[0]
		stopQueue = stopQueue[1:]

		commit, err := object.GetCommit(s, curr)
		if err != nil {
			continue
		}
		for _, p := range commit.ParentHashes {
			if p != plumbing.ZeroHash && !stopSet[p] {
				stopSet[p] = true
				stopQueue = append(stopQueue, p)
			}
		}
	}

	visited := make(map[plumbing.Hash]bool)
	count := 0
	var queue []plumbing.Hash

	for _, u := range updates {
		if u.New != plumbing.ZeroHash && !stopSet[u.New] && !visited[u.New] {
			visited[u.New] = true
			queue = append(queue, u.New)
			count++
		}
	}

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		commit, err := object.GetCommit(s, curr)
		if err != nil {
			continue
		}
		for _, p := range commit.ParentHashes {
			if p != plumbing.ZeroHash && !stopSet[p] && !visited[p] {
				visited[p] = true
				queue = append(queue, p)
				count++
			}
		}
	}

	return count
}

// CountCommitsBetween counts the number of commits reachable from newHash stopping at oldHash and optional stopTips.
// It uses reachability traversal rather than first-parent or author heuristics.
func CountCommitsBetween(s storage.Storer, oldHash, newHash plumbing.Hash, stopTips ...plumbing.Hash) int {
	if s == nil || newHash == plumbing.ZeroHash || oldHash == newHash {
		return 0
	}

	stopSet := make(map[plumbing.Hash]bool)
	var stopQueue []plumbing.Hash

	if oldHash != plumbing.ZeroHash {
		stopSet[oldHash] = true
		stopQueue = append(stopQueue, oldHash)
	}
	for _, tip := range stopTips {
		if tip != plumbing.ZeroHash && !stopSet[tip] {
			stopSet[tip] = true
			stopQueue = append(stopQueue, tip)
		}
	}

	for len(stopQueue) > 0 {
		curr := stopQueue[0]
		stopQueue = stopQueue[1:]

		commit, err := object.GetCommit(s, curr)
		if err != nil {
			continue
		}
		for _, p := range commit.ParentHashes {
			if p != plumbing.ZeroHash && !stopSet[p] {
				stopSet[p] = true
				stopQueue = append(stopQueue, p)
			}
		}
	}

	if stopSet[newHash] {
		return 0
	}

	visited := make(map[plumbing.Hash]bool)
	count := 0
	queue := []plumbing.Hash{newHash}
	visited[newHash] = true
	count++

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		commit, err := object.GetCommit(s, curr)
		if err != nil {
			continue
		}
		for _, p := range commit.ParentHashes {
			if p != plumbing.ZeroHash && !stopSet[p] && !visited[p] {
				visited[p] = true
				queue = append(queue, p)
				count++
			}
		}
	}

	return count
}

// isAncestor reports whether ancestor is reachable from descendant in the commit graph.
func isAncestor(s storage.Storer, ancestor, descendant plumbing.Hash) bool {
	if s == nil || ancestor == plumbing.ZeroHash || descendant == plumbing.ZeroHash {
		return false
	}
	if ancestor == descendant {
		return true
	}

	visited := make(map[plumbing.Hash]bool)
	queue := []plumbing.Hash{descendant}
	visited[descendant] = true

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		if curr == ancestor {
			return true
		}

		commit, err := object.GetCommit(s, curr)
		if err != nil {
			continue
		}
		for _, p := range commit.ParentHashes {
			if p != plumbing.ZeroHash && !visited[p] {
				visited[p] = true
				queue = append(queue, p)
			}
		}
	}

	return false
}
