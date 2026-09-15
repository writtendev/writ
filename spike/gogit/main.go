// Command gogit-bench is the WRIT-4 spike tool: it measures whether go-git's
// local object I/O is adequate for Writ's write pattern (one op = one
// commit, one ref per collaborative object, refs/writ/* at scale) against
// a real, large repository's existing object store.
//
// It is throwaway spike tooling, not part of the engine. Run it by pointing
// --repo at a bare clone of a big OSS repo (see spike/gogit/README.md).
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

const refPrefix = "refs/writ/bench-writer/cobs/spike/"

func main() {
	repoPath := flag.String("repo", "", "path to a bare git repo to use as the guinea pig")
	nOps := flag.Int("ops", 5000, "number of op-commits to write in the write-throughput benchmark")
	nRefs := flag.Int("refs", 5000, "number of refs to create in the many-refs benchmark (in addition to --ops)")
	flag.Parse()

	if *repoPath == "" {
		fmt.Fprintln(os.Stderr, "usage: gogit-bench --repo <path to bare repo> [--ops N] [--refs N]")
		os.Exit(2)
	}

	repo, err := git.PlainOpen(*repoPath)
	must(err)

	fmt.Println("== DAG walk ==")
	benchDAGWalk(repo)

	fmt.Println()
	fmt.Println("== Op-write throughput (commit + ref per op) ==")
	tip := benchWriteThroughput(repo, *nOps)

	fmt.Println()
	fmt.Println("== Many-refs behavior ==")
	benchRefScale(repo, *repoPath, *nOps, *nRefs, tip)

	fmt.Println()
	fmt.Println("== Cleanup ==")
	cleanup(repo, *repoPath)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// --- DAG walk ---------------------------------------------------------

func benchDAGWalk(repo *git.Repository) {
	head, err := repo.Head()
	must(err)

	t0 := time.Now()
	iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	must(err)
	n := 0
	must(iter.ForEach(func(c *object.Commit) error {
		n++
		return nil
	}))
	full := time.Since(t0)
	fmt.Printf("full history walk:    %d commits in %s (%.0f commits/sec)\n", n, full.Round(time.Millisecond), float64(n)/full.Seconds())

	t1 := time.Now()
	iter2, err := repo.Log(&git.LogOptions{From: head.Hash()})
	must(err)
	n2 := 0
	must(iter2.ForEach(func(c *object.Commit) error {
		n2++
		if n2 >= 1000 {
			return storer.ErrStop
		}
		return nil
	}))
	bounded := time.Since(t1)
	fmt.Printf("first 1000 commits:   %s (%.2f ms/commit, includes walk setup)\n", bounded.Round(time.Millisecond), float64(bounded.Microseconds())/1000/float64(n2))

	// CommitObject: single random-access lookup by hash, the shape a fold
	// does when it resolves one op's parent rather than walking everything.
	t2 := time.Now()
	_, err = repo.CommitObject(head.Hash())
	must(err)
	fmt.Printf("single commit lookup: %s\n", time.Since(t2))
}

// --- Op-write throughput ----------------------------------------------

// benchWriteThroughput writes n synthetic op-commits, each with its own ref
// under refs/writ/*, chained into a single writer's history (parent ==
// previous op), the way one COB accumulates ops from one writer. It returns
// the hash of the last commit written.
func benchWriteThroughput(repo *git.Repository, n int) plumbing.Hash {
	emptyTree := &object.Tree{}
	treeHash, err := writeObject(repo.Storer, plumbing.TreeObject, emptyTree)
	must(err)

	var parent plumbing.Hash
	if head, err := repo.Head(); err == nil {
		parent = head.Hash()
	}

	latencies := make([]time.Duration, 0, n)
	start := time.Now()
	last := parent
	for i := 0; i < n; i++ {
		payload := fmt.Sprintf(`{"op_id":"bench-%d","type":"item.create","object_id":"spike-item","body":"benchmark op payload %d","ts":%d}`, i, i, time.Now().UnixNano())
		commit := &object.Commit{
			Author:    object.Signature{Name: "writ-4-bench", Email: "bench@example.invalid", When: time.Now()},
			Committer: object.Signature{Name: "writ-4-bench", Email: "bench@example.invalid", When: time.Now()},
			Message:   payload,
			TreeHash:  treeHash,
		}
		if !last.IsZero() {
			commit.ParentHashes = []plumbing.Hash{last}
		}

		t0 := time.Now()
		hash, err := writeObject(repo.Storer, plumbing.CommitObject, commit)
		must(err)
		refName := plumbing.ReferenceName(fmt.Sprintf("%s%d", refPrefix, i))
		must(repo.Storer.SetReference(plumbing.NewHashReference(refName, hash)))
		latencies = append(latencies, time.Since(t0))
		last = hash
	}
	total := time.Since(start)

	fmt.Printf("%d ops written in %s (%.0f ops/sec)\n", n, total.Round(time.Millisecond), float64(n)/total.Seconds())
	printLatencies("per-op (commit write + ref set)", latencies)
	return last
}

func writeObject(store storer.EncodedObjectStorer, typ plumbing.ObjectType, enc interface {
	Encode(plumbing.EncodedObject) error
}) (plumbing.Hash, error) {
	obj := store.NewEncodedObject()
	obj.SetType(typ)
	if err := enc.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return store.SetEncodedObject(obj)
}

// --- Many-refs behavior -------------------------------------------------

func benchRefScale(repo *git.Repository, repoPath string, opsCount, refsCount int, tip plumbing.Hash) {
	// Extend the write-throughput refs to the requested scale, continuing
	// the index range so these are genuinely additional refs (opsCount..
	// opsCount+refsCount-1) rather than overwriting the ones --ops already
	// created at indices 0..opsCount-1.
	benchWriteThroughputSilent(repo, refsCount, opsCount, tip)
	n := opsCount + refsCount

	t0 := time.Now()
	count := 0
	iter, err := repo.Storer.IterReferences()
	must(err)
	must(iter.ForEach(func(r *plumbing.Reference) error {
		if strings.HasPrefix(string(r.Name()), "refs/writ/") {
			count++
		}
		return nil
	}))
	fmt.Printf("enumerate all refs (loose, unpacked):  %s, found %d refs/writ/* of %d requested\n", time.Since(t0).Round(time.Millisecond), count, n)

	// Random-access lookup of specific refs by name.
	sample := 200
	if sample > n {
		sample = n
	}
	t1 := time.Now()
	for i := 0; i < sample; i++ {
		name := plumbing.ReferenceName(fmt.Sprintf("%s%d", refPrefix, i*n/sample))
		_, err := repo.Storer.Reference(name)
		must(err)
	}
	lookup := time.Since(t1)
	fmt.Printf("random ref lookup (loose):              %s total, %s/lookup avg (n=%d)\n", lookup.Round(time.Millisecond), (lookup / time.Duration(sample)).Round(time.Microsecond), sample)

	// Fast-forward update of a subset of existing refs — the shape of
	// appending a new op to an object another writer already touched.
	updateN := sample
	updateLatencies := make([]time.Duration, 0, updateN)
	for i := 0; i < updateN; i++ {
		name := plumbing.ReferenceName(fmt.Sprintf("%s%d", refPrefix, i))
		t := time.Now()
		must(repo.Storer.SetReference(plumbing.NewHashReference(name, tip)))
		updateLatencies = append(updateLatencies, time.Since(t))
	}
	printLatencies("ref update (loose, existing ref moved forward)", updateLatencies)

	// Now pack the refs with system git and re-measure enumeration/lookup,
	// since a real repo's git gc will do this and go-git must still see
	// packed refs correctly.
	cmd := exec.Command("git", "pack-refs", "--all")
	cmd.Dir = repoPath
	t2 := time.Now()
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Printf("git pack-refs --all failed: %v: %s\n", err, out)
		return
	}
	fmt.Printf("git pack-refs --all:                    %s\n", time.Since(t2).Round(time.Millisecond))

	repo2, err := git.PlainOpen(repoPath)
	must(err)
	t3 := time.Now()
	count2 := 0
	iter2, err := repo2.Storer.IterReferences()
	must(err)
	must(iter2.ForEach(func(r *plumbing.Reference) error {
		if strings.HasPrefix(string(r.Name()), "refs/writ/") {
			count2++
		}
		return nil
	}))
	fmt.Printf("enumerate all refs (packed):             %s, found %d refs/writ/*\n", time.Since(t3).Round(time.Millisecond), count2)

	t4 := time.Now()
	for i := 0; i < sample; i++ {
		name := plumbing.ReferenceName(fmt.Sprintf("%s%d", refPrefix, i*n/sample))
		_, err := repo2.Storer.Reference(name)
		must(err)
	}
	lookup2 := time.Since(t4)
	fmt.Printf("random ref lookup (packed):              %s total, %s/lookup avg (n=%d)\n", lookup2.Round(time.Millisecond), (lookup2 / time.Duration(sample)).Round(time.Microsecond), sample)
}

// benchWriteThroughputSilent writes n more chained op-commits with refs
// named starting at startIndex, without printing. Used to grow the ref
// count for the many-refs benchmark without duplicating the
// write-throughput report or colliding with the ref names --ops already
// created at indices [0, startIndex).
func benchWriteThroughputSilent(repo *git.Repository, n, startIndex int, parent plumbing.Hash) {
	emptyTree := &object.Tree{}
	treeHash, err := writeObject(repo.Storer, plumbing.TreeObject, emptyTree)
	must(err)

	last := parent
	for i := 0; i < n; i++ {
		commit := &object.Commit{
			Author:       object.Signature{Name: "writ-4-bench", Email: "bench@example.invalid", When: time.Now()},
			Committer:    object.Signature{Name: "writ-4-bench", Email: "bench@example.invalid", When: time.Now()},
			Message:      fmt.Sprintf(`{"op_id":"bench-ref-%d"}`, i),
			TreeHash:     treeHash,
			ParentHashes: []plumbing.Hash{last},
		}
		hash, err := writeObject(repo.Storer, plumbing.CommitObject, commit)
		must(err)
		refName := plumbing.ReferenceName(fmt.Sprintf("%s%d", refPrefix, startIndex+i))
		must(repo.Storer.SetReference(plumbing.NewHashReference(refName, hash)))
		last = hash
	}
}

// --- Cleanup --------------------------------------------------------------

// cleanup removes every ref this run created, so the guinea-pig clone is
// left as it was found and can be reused for another pass.
func cleanup(repo *git.Repository, repoPath string) {
	iter, err := repo.Storer.IterReferences()
	must(err)
	var names []plumbing.ReferenceName
	must(iter.ForEach(func(r *plumbing.Reference) error {
		if strings.HasPrefix(string(r.Name()), "refs/writ/") {
			names = append(names, r.Name())
		}
		return nil
	}))
	for _, name := range names {
		must(repo.Storer.RemoveReference(name))
	}
	cmd := exec.Command("git", "pack-refs", "--all", "--prune")
	cmd.Dir = repoPath
	_ = cmd.Run()
	fmt.Printf("removed %d bench refs\n", len(names))
}

// --- reporting --------------------------------------------------------

func printLatencies(label string, ds []time.Duration) {
	if len(ds) == 0 {
		return
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p := func(pct float64) time.Duration {
		idx := int(math.Ceil(pct*float64(len(sorted)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	fmt.Printf("%s: p50=%s p90=%s p99=%s max=%s\n", label, p(0.50).Round(time.Microsecond), p(0.90).Round(time.Microsecond), p(0.99).Round(time.Microsecond), sorted[len(sorted)-1].Round(time.Microsecond))
}
