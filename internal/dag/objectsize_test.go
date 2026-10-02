package dag_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/dag"
)

// WRIT-313: the commit object and the root tree object are sized before they
// are loaded, so a peer cannot make Enumerate hold a giant object — or an
// N x M product of directory entries — by pointing a commit at one. Each
// case below is one reproduction from the ticket, scaled down: the object a
// pre-fix reader held whole was giantObjectBytes, and Enumerate must now
// allocate far less than that.
const (
	giantObjectBytes     = 32 << 20 // 32 MiB
	giantAllocBudget     = 4 << 20  // 4 MiB: an eighth of the object
	subtreeEntries       = 50000    // a ~1.7 MiB subtree object
	dirEntriesPerRoot    = 16
	subtreeAllocBudget   = 2 << 20 // 16 reads of it decoded would be ~100 MiB
	objectSizeCaseWriter = "0123456789abcdef"
)

func storeRawObject(t *testing.T, repo *git.Repository, typ plumbing.ObjectType, content []byte) plumbing.Hash {
	t.Helper()
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(typ)
	w, err := obj.Writer()
	if err != nil {
		t.Fatalf("object writer: %v", err)
	}
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		t.Fatalf("write object: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close object: %v", err)
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store object: %v", err)
	}
	return h
}

func storeRawTree(t *testing.T, repo *git.Repository, entries []object.TreeEntry) plumbing.Hash {
	t.Helper()
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.TreeObject)
	if err := (&object.Tree{Entries: entries}).Encode(obj); err != nil {
		t.Fatalf("encode tree: %v", err)
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store tree: %v", err)
	}
	return h
}

func storeRawCommit(t *testing.T, repo *git.Repository, tree plumbing.Hash, parent plumbing.Hash, message string) plumbing.Hash {
	t.Helper()
	sig := object.Signature{Name: "Mallory", Email: "mallory@example.test", When: time.Now().UTC()}
	commit := &object.Commit{Author: sig, Committer: sig, Message: message, TreeHash: tree}
	if !parent.IsZero() {
		commit.ParentHashes = []plumbing.Hash{parent}
	}
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.CommitObject)
	if err := commit.Encode(obj); err != nil {
		t.Fatalf("encode commit: %v", err)
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store commit: %v", err)
	}
	return h
}

// packOnly moves hash from the loose object store into a pack of its own.
func packOnly(t *testing.T, dir string, hash plumbing.Hash) {
	t.Helper()
	packDir := filepath.Join(dir, ".git", "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatalf("mkdir pack dir: %v", err)
	}
	cmd := exec.Command("git", "pack-objects", "--quiet", filepath.Join(packDir, "pack"))
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(hash.String() + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git pack-objects: %v\n%s", err, out)
	}
	hs := hash.String()
	if err := os.Remove(filepath.Join(dir, ".git", "objects", hs[:2], hs[2:])); err != nil {
		t.Fatalf("remove loose object: %v", err)
	}
}

func TestEnumerate_GiantObjectsAreSizedBeforeLoading(t *testing.T) {
	cases := []struct {
		name string
		// build writes the hostile shape, with parent as the op behind it,
		// and returns the tip commit and the one giant object to pack in the
		// packed variant.
		build       func(t *testing.T, repo *git.Repository, parent plumbing.Hash) (tip, giant plumbing.Hash)
		wantReason  codec.RejectReason
		wantDecoded int
		budget      uint64
	}{
		{
			// Shape (a): a commit whose `tree` header names a giant blob.
			name: "tree_header_naming_giant_blob",
			build: func(t *testing.T, repo *git.Repository, parent plumbing.Hash) (plumbing.Hash, plumbing.Hash) {
				blob := storeRawObject(t, repo, plumbing.BlobObject, bytes.Repeat([]byte("x"), giantObjectBytes))
				return storeRawCommit(t, repo, blob, parent, "writ: create widget/giant\n"), blob
			},
			wantReason:  codec.RejectTreeTooLarge,
			wantDecoded: 1,
			budget:      giantAllocBudget,
		},
		{
			// Shape (b): N directory entries naming one M-entry subtree.
			name: "many_directory_entries_naming_one_subtree",
			build: func(t *testing.T, repo *git.Repository, parent plumbing.Hash) (plumbing.Hash, plumbing.Hash) {
				blob := storeRawObject(t, repo, plumbing.BlobObject, []byte("x"))
				var sub []object.TreeEntry
				for i := 0; i < subtreeEntries; i++ {
					sub = append(sub, object.TreeEntry{Name: fmt.Sprintf("f%06d", i), Mode: filemode.Regular, Hash: blob})
				}
				subtree := storeRawTree(t, repo, sub)
				var root []object.TreeEntry
				for i := 0; i < dirEntriesPerRoot; i++ {
					root = append(root, object.TreeEntry{Name: fmt.Sprintf("d%02d", i), Mode: filemode.Dir, Hash: subtree})
				}
				tree := storeRawTree(t, repo, root)
				return storeRawCommit(t, repo, tree, parent, "writ: create widget/fanout\n"), subtree
			},
			// Sixteen directories and no op.json: rule 1 reads the root tree
			// only, so no subtree is ever loaded.
			wantReason:  codec.RejectMissingOpJSON,
			wantDecoded: 1,
			budget:      subtreeAllocBudget,
		},
		{
			// A giant commit object: a message of giantObjectBytes.
			name: "giant_commit_message",
			build: func(t *testing.T, repo *git.Repository, parent plumbing.Hash) (plumbing.Hash, plumbing.Hash) {
				blob := storeRawObject(t, repo, plumbing.BlobObject, []byte("{}"))
				tree := storeRawTree(t, repo, []object.TreeEntry{{Name: "op.json", Mode: filemode.Regular, Hash: blob}})
				tip := storeRawCommit(t, repo, tree, parent, strings.Repeat("x", giantObjectBytes))
				return tip, tip
			},
			wantReason: codec.RejectCommitTooLarge,
			// Never loaded, so never decoded: not counted.
			wantDecoded: 0,
			budget:      giantAllocBudget,
		},
	}

	for _, tc := range cases {
		for _, packed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/packed=%v", tc.name, packed), func(t *testing.T) {
				dir, repo := initTestRepo(t)
				ident := testIdentity(objectSizeCaseWriter, "Alice", "alice@example.test")
				store, err := dag.Open(dir, ident, withVocabularies())
				if err != nil {
					t.Fatalf("Open failed: %v", err)
				}
				op1, err := store.Append(context.Background(), codec.Envelope{
					ObjectID: "w-1", ObjectType: "widget", OpType: "create", OpVersion: 1,
					Body: json.RawMessage(`{"title":"Widget 1"}`),
				}, nil)
				if err != nil {
					t.Fatalf("Append failed: %v", err)
				}

				tip, giant := tc.build(t, repo, plumbing.NewHash(op1.ID))
				if packed {
					packOnly(t, dir, giant)
				}
				refName := dag.LocalRefName(ident.WriterID, "widget")
				if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, tip)); err != nil {
					t.Fatalf("advance ref: %v", err)
				}

				// A fresh Store, so a pack written after the first Open is seen.
				store, err = dag.Open(dir, ident, withVocabularies())
				if err != nil {
					t.Fatalf("reopen failed: %v", err)
				}

				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				res, err := store.Enumerate()
				runtime.ReadMemStats(&after)
				if err != nil {
					t.Fatalf("Enumerate failed: %v", err)
				}

				if len(res.Rejections) != 1 {
					t.Fatalf("rejections = %v, want exactly one", res.Rejections)
				}
				if rej := res.Rejections[0]; rej.CommitID != tip.String() || rej.Reason != tc.wantReason {
					t.Errorf("rejection = %+v, want %s on %s", rej, tc.wantReason, tip)
				}
				if res.DecodedCommits != tc.wantDecoded {
					t.Errorf("DecodedCommits = %d, want %d", res.DecodedCommits, tc.wantDecoded)
				}
				// op1 sits behind the rejected tip: the walk stops there, and
				// for the unloaded commit it never even learns the parent.
				if len(res.Ops["w-1"]) != 0 {
					t.Errorf("Ops[w-1] = %v, want none behind the rejected tip", res.Ops["w-1"])
				}
				if delta := after.TotalAlloc - before.TotalAlloc; delta > tc.budget {
					t.Errorf("Enumerate allocated %d bytes, want under %d", delta, tc.budget)
				}
			})
		}
	}
}

// TestAppendRefusesOverBoundCausalParents pins the producer side of the
// commit bound: an Append whose causal frontier would push the commit past
// codec.MaxCommitBytes is refused, not written.
func TestAppendRefusesOverBoundCausalParents(t *testing.T) {
	dir, repo := initTestRepo(t)
	ident := testIdentity(objectSizeCaseWriter, "Alice", "alice@example.test")
	store, err := dag.Open(dir, ident, withVocabularies())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	ctx := context.Background()
	op1, err := store.Append(ctx, codec.Envelope{
		ObjectID: "w-1", ObjectType: "widget", OpType: "create", OpVersion: 1,
		Body: json.RawMessage(`{"title":"Widget 1"}`),
	}, nil)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Each causal parent must be a real op commit with an op.json in its
	// tree; distinct commits over one op tree are enough.
	opCommit, err := repo.CommitObject(plumbing.NewHash(op1.ID))
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}
	var causal []string
	for i := 0; i < 1400; i++ {
		causal = append(causal, storeRawCommit(t, repo, opCommit.TreeHash, plumbing.ZeroHash, fmt.Sprintf("parent %d\n", i)).String())
	}

	before := snapshotRefs(t, repo)
	_, err = store.Append(ctx, codec.Envelope{
		ObjectID: "w-1", ObjectType: "widget", OpType: "update", OpVersion: 1,
		Body: json.RawMessage(`{"title":"Widget 2"}`),
	}, causal)
	if err == nil {
		t.Fatalf("Append with 1400 causal parents succeeded, want commit-too-large")
	}
	var rej *codec.RejectError
	if !errors.As(err, &rej) || rej.Reason != codec.RejectCommitTooLarge {
		t.Fatalf("Append err = %v, want commit-too-large", err)
	}
	after := snapshotRefs(t, repo)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Errorf("refs moved on a refused append: %v -> %v", before, after)
	}
}
