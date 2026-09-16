package dag_test

import (
	"bytes"
	"context"
	"encoding/json"
	"runtime"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
)

// oversizedOpJSONBytes is well over codec.MaxPayloadBytes, so a reader that
// regressed to io.ReadAll (rather than the io.LimitReader FromGitCommit
// uses) would have to hold every one of these bytes, not just the capped
// prefix TestEnumerate_RejectsOversizedPayloadWithBoundedAllocation checks for.
const oversizedOpJSONBytes = 8 << 20 // 8 MiB

// writeOversizedOpCommit writes a commit directly into repo's object store —
// bypassing dag.Store.Append and its BuildCommit producer check, which
// would refuse to write this payload — with parent as its sole parent and
// an op.json blob of size bytes. It simulates an oversized op that arrived
// from a peer (e.g. via a push or fetch) rather than one this store wrote
// itself.
func writeOversizedOpCommit(repo *git.Repository, parent plumbing.Hash, size int) (plumbing.Hash, error) {
	blobObj := repo.Storer.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	w, err := blobObj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(bytes.Repeat([]byte("x"), size)); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	blobHash, err := repo.Storer.SetEncodedObject(blobObj)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	tree := &object.Tree{Entries: []object.TreeEntry{
		{Name: "op.json", Mode: filemode.Regular, Hash: blobHash},
	}}
	treeObj := repo.Storer.NewEncodedObject()
	treeObj.SetType(plumbing.TreeObject)
	if err := tree.Encode(treeObj); err != nil {
		return plumbing.ZeroHash, err
	}
	treeHash, err := repo.Storer.SetEncodedObject(treeObj)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	sig := object.Signature{Name: "Mallory", Email: "mallory@example.test", When: time.Now().UTC()}
	commit := &object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      "writ: create widget/oversized\n",
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{parent},
	}
	commitObj := repo.Storer.NewEncodedObject()
	commitObj.SetType(plumbing.CommitObject)
	if err := commit.Encode(commitObj); err != nil {
		return plumbing.ZeroHash, err
	}
	return repo.Storer.SetEncodedObject(commitObj)
}

// TestEnumerate_RejectsOversizedPayloadWithBoundedAllocation pins the
// reader side of WRIT-255: an op.json far over codec.MaxPayloadBytes is a
// recorded rejection (payload-too-large), not a decode panic or an insert
// failure, a valid sibling op on the same chain still enumerates, and
// FromGitCommit's io.LimitReader keeps the cost of reading it bounded
// rather than proportional to the oversized blob.
func TestEnumerate_RejectsOversizedPayloadWithBoundedAllocation(t *testing.T) {
	dir, repo := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store, err := dag.Open(dir, ident, withVocabularies())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	env := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Widget 1"}`),
	}
	op1, err := store.Append(context.Background(), env, nil)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	oversizedHash, err := writeOversizedOpCommit(repo, plumbing.NewHash(op1.ID), oversizedOpJSONBytes)
	if err != nil {
		t.Fatalf("writeOversizedOpCommit failed: %v", err)
	}
	refName := plumbing.ReferenceName("refs/writ/0123456789abcdef/widget")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, oversizedHash)); err != nil {
		t.Fatalf("advance ref to oversized commit: %v", err)
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
	rej := res.Rejections[0]
	if rej.CommitID != oversizedHash.String() {
		t.Errorf("rejection commit = %s, want %s", rej.CommitID, oversizedHash.String())
	}
	if rej.Reason != codec.RejectPayloadTooLarge {
		t.Errorf("rejection reason = %q, want %q", rej.Reason, codec.RejectPayloadTooLarge)
	}

	// The valid sibling op is unaffected by the oversized op on the same chain.
	if len(res.Ops["w-1"]) != 1 || res.Ops["w-1"][0].ID != op1.ID {
		t.Fatalf("Ops[w-1] = %v, want exactly [%s]", res.Ops["w-1"], op1.ID)
	}

	// Bounded allocation: reading the oversized blob through
	// io.LimitReader costs at most codec.MaxPayloadBytes+1, not the 8 MiB
	// blob it was truncated from. A generous multiple of the cap still
	// catches a regression back to io.ReadAll by a wide margin.
	if delta := after.TotalAlloc - before.TotalAlloc; delta > 4*codec.MaxPayloadBytes {
		t.Errorf("Enumerate allocated %d bytes, want well under the %d-byte oversized blob (budget 4x MaxPayloadBytes = %d)",
			delta, oversizedOpJSONBytes, 4*codec.MaxPayloadBytes)
	}
}
