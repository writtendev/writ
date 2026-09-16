package dag_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
)

// packAllObjectsWithFiller packs every object reachable from dir's refs —
// the real op commits, trees, and op.json blobs a dag.Store just wrote —
// plus numFiller small, otherwise-unreferenced blobs, into a single pack
// file via `git pack-objects`, the same tool a real repack leaves behind,
// and removes every loose copy afterward. It is the dag-layer analogue of
// buildLargePackedRepo in engine/codec/packsize_test.go: the filler blobs
// exist only to inflate the pack's .idx to a size actually worth decoding
// — without them, a repo built from a couple hundred tiny op commits packs
// into an .idx cheap enough that even redecoding it on every single commit
// wouldn't show up over the noise of everything else Enumerate does.
func packAllObjectsWithFiller(t *testing.T, dir string, repo *git.Repository, numFiller int) {
	t.Helper()

	var stdin bytes.Buffer
	revList := exec.Command("git", "rev-list", "--objects", "--all")
	revList.Dir = dir
	revList.Stdout = &stdin
	if err := revList.Run(); err != nil {
		t.Fatalf("git rev-list: %v", err)
	}

	for i := 0; i < numFiller; i++ {
		obj := repo.Storer.NewEncodedObject()
		obj.SetType(plumbing.BlobObject)
		w, err := obj.Writer()
		if err != nil {
			t.Fatalf("filler blob writer: %v", err)
		}
		if _, err := fmt.Fprintf(w, "writ-dag-packcache-filler-%d", i); err != nil {
			t.Fatalf("write filler blob: %v", err)
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close filler blob: %v", err)
		}
		h, err := repo.Storer.SetEncodedObject(obj)
		if err != nil {
			t.Fatalf("store filler blob: %v", err)
		}
		fmt.Fprintln(&stdin, h.String())
	}

	packDir := filepath.Join(dir, ".git", "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatalf("mkdir pack dir: %v", err)
	}
	cmd := exec.Command("git", "pack-objects", "--quiet", filepath.Join(packDir, "pack"))
	cmd.Dir = dir
	cmd.Stdin = &stdin
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git pack-objects: %v\n%s", err, out)
	}

	loose, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "[0-9a-f][0-9a-f]"))
	if err != nil {
		t.Fatalf("glob loose objects: %v", err)
	}
	for _, l := range loose {
		if err := os.RemoveAll(l); err != nil {
			t.Fatalf("remove loose objects: %v", err)
		}
	}
}

// TestEnumerate_PackCacheKeepsAllocationBounded pins WRIT-255 round 3's
// minor finding: nothing failed if EnumerateSince stopped wrapping its
// storer with packidx.WithCache before decoding op commits, silently
// falling back to decoding every packed op.json blob's whole pack index
// from scratch on every single commit instead of once per pass — the same
// unbounded-per-call cost
// codec.TestPackfileObjectSize_CachedIndexAvoidsPerCallRedecode pins at the
// codec layer, but here for the layer that actually has to wire the cache
// in: dag.Store.EnumerateSince.
func TestEnumerate_PackCacheKeepsAllocationBounded(t *testing.T) {
	dir, repo := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store, err := dag.Open(dir, ident, withVocabularies())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	const numOps = 200
	for i := 0; i < numOps; i++ {
		env := codec.Envelope{
			ObjectID:   "w-1",
			ObjectType: "widget",
			OpType:     "update",
			OpVersion:  1,
			Body:       json.RawMessage(fmt.Sprintf(`{"seq":%d,"title":"Op %d"}`, i, i)),
		}
		if _, err := store.Append(context.Background(), env, nil); err != nil {
			t.Fatalf("Append op %d failed: %v", i, err)
		}
	}

	// Pack every op commit's tree, commit, and op.json blob — plus enough
	// filler blobs to make the pack's .idx expensive to decode — into one
	// pack file, and drop the loose copies, so every op.json this pass
	// reads comes from that pack's index: the on-disk shape a real
	// repository settles into once git repacks it.
	const numFiller = 10000
	packAllObjectsWithFiller(t, dir, repo, numFiller)

	runtime.GC()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	res, err := store.Enumerate()
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}

	if len(res.Ops["w-1"]) != numOps {
		t.Fatalf("Ops[w-1] len = %d, want %d", len(res.Ops["w-1"]), numOps)
	}

	// Wired correctly (packidx.WithCache, one decode of this pack's .idx
	// plus numOps commits' worth of ordinary op decoding and producer
	// validation) this measured about 14 MB when this test was written.
	// Reverting EnumerateSince to decode the .idx fresh per commit
	// instead of once per pass — i.e. dropping the packidx.WithCache
	// wrap this test exists to pin — measured about 90 MB under the
	// same conditions: roughly numOps extra full .idx decodes stacked
	// on top. The budget sits between the two, far enough from either
	// to absorb ordinary variance without going anywhere near the
	// bugged number.
	const budget = 30 << 20 // 30 MiB
	if delta := after.TotalAlloc - before.TotalAlloc; delta > budget {
		t.Errorf("Enumerate allocated %d bytes across %d commits against one shared pack, want under %d (one pack-index decode of this size, not %d of them)",
			delta, numOps, budget, numOps)
	}
}
