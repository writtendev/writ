package codec_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/writtendev/writ/internal/codec"
)

// WRIT-313: a commit object over codec.MaxCommitBytes and a root tree object
// over codec.MaxTreeBytes are sized before they are loaded
// (spec/op-envelope.md §Reader validation rule 1).

func storeObject(t *testing.T, s storage.Storer, typ plumbing.ObjectType, content []byte) plumbing.Hash {
	t.Helper()
	obj := s.NewEncodedObject()
	obj.SetType(typ)
	w, err := obj.Writer()
	if err != nil {
		t.Fatalf("object writer: %v", err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatalf("write object: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close object: %v", err)
	}
	h, err := s.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store object: %v", err)
	}
	return h
}

func storeTree(t *testing.T, s storage.Storer, entries []object.TreeEntry) plumbing.Hash {
	t.Helper()
	tree := &object.Tree{Entries: entries}
	obj := s.NewEncodedObject()
	obj.SetType(plumbing.TreeObject)
	if err := tree.Encode(obj); err != nil {
		t.Fatalf("encode tree: %v", err)
	}
	h, err := s.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store tree: %v", err)
	}
	return h
}

// treeOfSize stores a tree object of exactly size bytes: an op.json entry
// plus one padding entry, whose name length is computed from the 28 bytes of
// `100644 <name>\0<20-byte SHA-1>` overhead each entry costs.
func treeOfSize(t *testing.T, s storage.Storer, size int) plumbing.Hash {
	t.Helper()
	blob := storeObject(t, s, plumbing.BlobObject, []byte("{}"))
	const opJSONEntry = len("100644 op.json") + 1 + 20
	padLen := size - opJSONEntry - len("100644 ") - 1 - 20
	if padLen < 1 {
		t.Fatalf("size %d too small for a two-entry tree", size)
	}
	h := storeTree(t, s, []object.TreeEntry{
		{Name: "op.json", Mode: filemode.Regular, Hash: blob},
		{Name: strings.Repeat("x", padLen), Mode: filemode.Regular, Hash: blob},
	})
	got, err := s.EncodedObjectSize(h)
	if err != nil || got != int64(size) {
		t.Fatalf("tree is %d bytes (err %v), want %d", got, err, size)
	}
	return h
}

// commitOfSize stores a commit object of exactly size bytes over treeHash,
// its message padded.
func commitOfSize(t *testing.T, s storage.Storer, treeHash plumbing.Hash, size int) plumbing.Hash {
	t.Helper()
	sig := object.Signature{Name: "Alice", Email: "alice@example.test", When: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	commit := &object.Commit{Author: sig, Committer: sig, TreeHash: treeHash}
	probe := &plumbing.MemoryObject{}
	probe.SetType(plumbing.CommitObject)
	if err := commit.Encode(probe); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	pad := size - int(probe.Size())
	if pad < 0 {
		t.Fatalf("size %d is smaller than an empty commit (%d)", size, probe.Size())
	}
	commit.Message = strings.Repeat("x", pad)
	obj := s.NewEncodedObject()
	obj.SetType(plumbing.CommitObject)
	if err := commit.Encode(obj); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	h, err := s.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store commit: %v", err)
	}
	if got, err := s.EncodedObjectSize(h); err != nil || got != int64(size) {
		t.Fatalf("commit is %d bytes (err %v), want %d", got, err, size)
	}
	return h
}

// packLoose moves the named loose objects of the repository at dir into a
// pack, leaving no loose copy, so a lookup of any of them goes through the
// packfile path.
func packLoose(t *testing.T, dir string, hashes ...plumbing.Hash) {
	t.Helper()
	var stdin bytes.Buffer
	for _, h := range hashes {
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
	for _, h := range hashes {
		hs := h.String()
		if err := os.Remove(filepath.Join(dir, ".git", "objects", hs[:2], hs[2:])); err != nil {
			t.Fatalf("remove loose %s: %v", hs, err)
		}
	}
}

func rejectReason(err error) codec.RejectReason {
	var rej *codec.RejectError
	if errors.As(err, &rej) {
		return rej.Reason
	}
	return ""
}

// TestGetCommitAndFromGitCommitSizeBoundaries pins both bounds exactly, loose
// and packed: the bound itself is inside, one byte more is out and is never
// loaded.
func TestGetCommitAndFromGitCommitSizeBoundaries(t *testing.T) {
	for _, packed := range []bool{false, true} {
		t.Run(fmt.Sprintf("packed=%v", packed), func(t *testing.T) {
			dir := t.TempDir()
			repo, err := git.PlainInit(dir, false)
			if err != nil {
				t.Fatalf("PlainInit: %v", err)
			}
			s := repo.Storer

			treeAt := treeOfSize(t, s, codec.MaxTreeBytes)
			treeOver := treeOfSize(t, s, codec.MaxTreeBytes+1)
			okTree := treeOfSize(t, s, 100)
			commitAt := commitOfSize(t, s, okTree, codec.MaxCommitBytes)
			commitOver := commitOfSize(t, s, okTree, codec.MaxCommitBytes+1)
			commitOverTreeAt := commitOfSize(t, s, treeAt, 200)
			commitOverTreeOver := commitOfSize(t, s, treeOver, 200)
			if packed {
				packLoose(t, dir, treeAt, treeOver, commitAt, commitOver, commitOverTreeAt, commitOverTreeOver)
				repo, err = git.PlainOpen(dir)
				if err != nil {
					t.Fatalf("PlainOpen: %v", err)
				}
				s = repo.Storer
			}

			if _, err := codec.GetCommit(s, commitAt); err != nil {
				t.Errorf("GetCommit at %d bytes: %v", codec.MaxCommitBytes, err)
			}
			if _, err := codec.GetCommit(s, commitOver); rejectReason(err) != codec.RejectCommitTooLarge {
				t.Errorf("GetCommit at %d bytes: %v, want commit-too-large", codec.MaxCommitBytes+1, err)
			}
			if _, err := codec.GetTree(s, treeAt); err != nil {
				t.Errorf("GetTree at %d bytes: %v", codec.MaxTreeBytes, err)
			}
			if _, err := codec.GetTree(s, treeOver); rejectReason(err) != codec.RejectTreeTooLarge {
				t.Errorf("GetTree at %d bytes: %v, want tree-too-large", codec.MaxTreeBytes+1, err)
			}

			at, err := codec.GetCommit(s, commitOverTreeAt)
			if err != nil {
				t.Fatalf("GetCommit: %v", err)
			}
			pure, err := codec.FromGitCommit(s, at)
			if err != nil {
				t.Fatalf("FromGitCommit at tree bound: %v", err)
			}
			if pure.TreeSize != codec.MaxTreeBytes || len(pure.Tree) != 2 {
				t.Errorf("tree at bound: TreeSize %d, %d entries; want %d, 2", pure.TreeSize, len(pure.Tree), codec.MaxTreeBytes)
			}
			if _, err := codec.DecodeCommit(pure); rejectReason(err) != codec.RejectExtraTreeEntry {
				t.Errorf("DecodeCommit at tree bound: %v, want extra-tree-entry", err)
			}

			over, err := codec.GetCommit(s, commitOverTreeOver)
			if err != nil {
				t.Fatalf("GetCommit: %v", err)
			}
			pure, err = codec.FromGitCommit(s, over)
			if err != nil {
				t.Fatalf("FromGitCommit over tree bound: %v", err)
			}
			if pure.TreeSize != codec.MaxTreeBytes+1 || len(pure.Tree) != 0 {
				t.Errorf("tree over bound: TreeSize %d, %d entries; want %d, 0 (never loaded)", pure.TreeSize, len(pure.Tree), codec.MaxTreeBytes+1)
			}
			if _, err := codec.DecodeCommit(pure); rejectReason(err) != codec.RejectTreeTooLarge {
				t.Errorf("DecodeCommit over tree bound: %v, want tree-too-large", err)
			}
		})
	}
}

// TestSizeBoundsWithInMemoryStorer pins the fallback sizing path, for a storer
// packfileObjectSize cannot see into.
func TestSizeBoundsWithInMemoryStorer(t *testing.T) {
	s := memory.NewStorage()
	treeOver := treeOfSize(t, s, codec.MaxTreeBytes+1)
	commitOver := commitOfSize(t, s, treeOfSize(t, s, 100), codec.MaxCommitBytes+1)

	if _, err := codec.GetCommit(s, commitOver); rejectReason(err) != codec.RejectCommitTooLarge {
		t.Errorf("GetCommit: %v, want commit-too-large", err)
	}
	if _, err := codec.GetTree(s, treeOver); rejectReason(err) != codec.RejectTreeTooLarge {
		t.Errorf("GetTree: %v, want tree-too-large", err)
	}
	if _, err := codec.GetCommit(s, plumbing.NewHash("0123456789abcdef0123456789abcdef01234567")); !errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Errorf("GetCommit of an absent hash: %v, want ErrObjectNotFound", err)
	}
}

// TestFromGitCommitTreeHashNamingBlob pins shape (a): a commit whose tree
// hash names a blob far over the tree bound is sized and never loaded, as a
// tree or to probe its type; one at or under the bound keeps the old path
// (present but not a tree: no entries, missing-op-json).
func TestFromGitCommitTreeHashNamingBlob(t *testing.T) {
	s := memory.NewStorage()
	big := storeObject(t, s, plumbing.BlobObject, bytes.Repeat([]byte("x"), codec.MaxTreeBytes+1))
	small := storeObject(t, s, plumbing.BlobObject, []byte("tiny"))

	for _, tc := range []struct {
		name string
		tree plumbing.Hash
		want codec.RejectReason
		size int64
	}{
		{"over the bound", big, codec.RejectTreeTooLarge, codec.MaxTreeBytes + 1},
		{"under the bound", small, codec.RejectMissingOpJSON, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commit := commitOfSize(t, s, tc.tree, 200)
			gc, err := codec.GetCommit(s, commit)
			if err != nil {
				t.Fatalf("GetCommit: %v", err)
			}
			pure, err := codec.FromGitCommit(s, gc)
			if err != nil {
				t.Fatalf("FromGitCommit: %v", err)
			}
			if pure.TreeSize != tc.size || len(pure.Tree) != 0 {
				t.Errorf("TreeSize %d with %d entries, want %d with none", pure.TreeSize, len(pure.Tree), tc.size)
			}
			if _, err := codec.DecodeCommit(pure); rejectReason(err) != tc.want {
				t.Errorf("DecodeCommit: %v, want %s", err, tc.want)
			}
		})
	}
}

// TestFromGitCommitLoneSubdirectoryIsMissingOpJSON pins the deleted
// op-json-subdirectory reason: a root tree whose only entry is a directory
// holding op.json is missing-op-json, and the subtree is never read — here it
// is not even in the store.
func TestFromGitCommitLoneSubdirectoryIsMissingOpJSON(t *testing.T) {
	s := memory.NewStorage()
	absent := plumbing.NewHash("1111111111111111111111111111111111111111")
	tree := storeTree(t, s, []object.TreeEntry{{Name: "subdir", Mode: filemode.Dir, Hash: absent}})
	gc, err := codec.GetCommit(s, commitOfSize(t, s, tree, 200))
	if err != nil {
		t.Fatalf("GetCommit: %v", err)
	}
	pure, err := codec.FromGitCommit(s, gc)
	if err != nil {
		t.Fatalf("FromGitCommit: %v", err)
	}
	if _, err := codec.DecodeCommit(pure); rejectReason(err) != codec.RejectMissingOpJSON {
		t.Errorf("DecodeCommit: %v, want missing-op-json", err)
	}
}

// TestWriteCommitRefusesOverBoundCommit pins the producer side: a commit with
// more causal parents than fit under codec.MaxCommitBytes is refused before it
// is stored, while one just under is written.
func TestWriteCommitRefusesOverBoundCommit(t *testing.T) {
	alice := codec.Identity{Name: "Alice", Email: "alice@example.test", When: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tree := []codec.TreeEntry{{Name: "op.json", Mode: "100644", Data: []byte("{}")}}
	build := func(parents int) *codec.Commit {
		c := &codec.Commit{Author: alice, Committer: alice, Message: "writ: create widget/w-1\n", Tree: tree}
		for i := 0; i < parents; i++ {
			c.Parents = append(c.Parents, plumbing.ComputeHash(plumbing.BlobObject, []byte(fmt.Sprint(i))).String())
		}
		return c
	}

	s := memory.NewStorage()
	// Each parent line is 48 bytes; the rest of this commit is well under 200.
	if _, err := codec.WriteCommit(context.Background(), s, build(1300), nil); err != nil {
		t.Fatalf("WriteCommit of 1300 parents: %v", err)
	}

	_, err := codec.WriteCommit(context.Background(), s, build(1400), nil)
	if rejectReason(err) != codec.RejectCommitTooLarge {
		t.Fatalf("WriteCommit of 1400 parents: %v, want commit-too-large", err)
	}
	for hash, obj := range s.Objects {
		if obj.Type() == plumbing.CommitObject && obj.Size() > codec.MaxCommitBytes {
			t.Errorf("stored over-bound commit %s (%d bytes)", hash, obj.Size())
		}
	}
}

// TestFromGitCommitNeverRetainsMoreThanTheBound is the property test the
// ticket's ruling asks for: for random legal root trees — random entry counts,
// modes, and names, with directory entries naming large subtrees — FromGitCommit
// never returns more tree entries than fit in codec.MaxTreeBytes, and a tree
// over the bound is never loaded at all. (An entry naming a subtree is
// reported as one entry; nothing beneath it is ever read — TreeEntry has no
// field to hold it.)
func TestFromGitCommitNeverRetainsMoreThanTheBound(t *testing.T) {
	// Smallest possible entry: `40000 a\0<20 bytes>`.
	const minEntry = len("40000 a") + 1 + 20
	maxEntries := codec.MaxTreeBytes / minEntry

	s := memory.NewStorage()
	var bigSubtree []object.TreeEntry
	blob := storeObject(t, s, plumbing.BlobObject, []byte("x"))
	for i := 0; i < 20000; i++ {
		bigSubtree = append(bigSubtree, object.TreeEntry{Name: fmt.Sprintf("f%06d", i), Mode: filemode.Regular, Hash: blob})
	}
	subtree := storeTree(t, s, bigSubtree)

	modes := []filemode.FileMode{filemode.Regular, filemode.Executable, filemode.Symlink, filemode.Dir, filemode.Submodule}
	rng := rand.New(rand.NewSource(313))
	for i := 0; i < 300; i++ {
		n := rng.Intn(400)
		names := map[string]bool{}
		for len(names) < n {
			b := make([]byte, 1+rng.Intn(12))
			for j := range b {
				b[j] = byte('a' + rng.Intn(26))
			}
			names[string(b)] = true
		}
		sorted := make([]string, 0, n)
		for name := range names {
			sorted = append(sorted, name)
		}
		sort.Strings(sorted)

		var entries []object.TreeEntry
		for _, name := range sorted {
			mode := modes[rng.Intn(len(modes))]
			hash := blob
			if mode == filemode.Dir {
				hash = subtree
			}
			entries = append(entries, object.TreeEntry{Name: name, Mode: mode, Hash: hash})
		}
		treeHash := storeTree(t, s, entries)
		size, err := s.EncodedObjectSize(treeHash)
		if err != nil {
			t.Fatalf("EncodedObjectSize: %v", err)
		}

		gc, err := codec.GetCommit(s, commitOfSize(t, s, treeHash, 200))
		if err != nil {
			t.Fatalf("GetCommit: %v", err)
		}
		pure, err := codec.FromGitCommit(s, gc)
		if err != nil {
			t.Fatalf("FromGitCommit (%d entries, %d bytes): %v", len(entries), size, err)
		}

		if len(pure.Tree) > maxEntries {
			t.Fatalf("returned %d tree entries, more than the %d that fit in %d bytes", len(pure.Tree), maxEntries, codec.MaxTreeBytes)
		}
		if size > codec.MaxTreeBytes {
			if len(pure.Tree) != 0 || pure.TreeSize != size {
				t.Fatalf("%d-byte tree: %d entries, TreeSize %d; want none loaded and TreeSize %d", size, len(pure.Tree), pure.TreeSize, size)
			}
			continue
		}
		if len(pure.Tree) != len(entries) {
			t.Fatalf("%d-byte tree: %d entries returned, want %d", size, len(pure.Tree), len(entries))
		}
		for j, e := range pure.Tree {
			if e.Name != entries[j].Name || e.Hash != entries[j].Hash.String() {
				t.Fatalf("entry %d: got %+v, want %+v", j, e, entries[j])
			}
		}
	}
}
