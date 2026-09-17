package dag_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
)

// writeCommitWithMissingOpJSONBlob writes a commit directly into repo's
// object store — bypassing dag.Store.Append, which would never produce
// this shape — whose tree names an op.json blob that is never itself
// written to the storer. This is the shape a partial or shallow clone
// leaves behind: the commit and tree objects are present (they were
// fetched), but a blob the fetch's object-negotiation left out was not.
func writeCommitWithMissingOpJSONBlob(repo *git.Repository, parent plumbing.Hash) (plumbing.Hash, error) {
	missingBlob := plumbing.NewHash("0123456789abcdef0123456789abcdef01234567")

	tree := &object.Tree{Entries: []object.TreeEntry{
		{Name: "op.json", Mode: filemode.Regular, Hash: missingBlob},
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
		Message:      "writ: create widget/unavailable\n",
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

// TestEnumerate_ObjectUnavailableDistinctFromMalformed pins WRIT-271's
// absent-vs-malformed split at the dag layer: a chain whose tip's op.json
// blob is absent from the object store — the shape a partial or shallow
// clone leaves behind — is reported with reason object-unavailable, not
// missing-op-json (a genuine tree-shape violation) and not
// non-canonical-payload (a genuinely malformed payload). Before this fix,
// codec.FromGitCommit's readOpJSONBlob swallowed the open()/Reader()
// failure to nil, nil, so this exact shape decoded as an empty payload and
// was reported as non-canonical-payload — an absent object misreported as
// a malformed op.
func TestEnumerate_ObjectUnavailableDistinctFromMalformed(t *testing.T) {
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

	unavailableHash, err := writeCommitWithMissingOpJSONBlob(repo, plumbing.NewHash(op1.ID))
	if err != nil {
		t.Fatalf("writeCommitWithMissingOpJSONBlob failed: %v", err)
	}
	refName := plumbing.ReferenceName("refs/writ/0123456789abcdef/widget")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, unavailableHash)); err != nil {
		t.Fatalf("advance ref to unavailable commit: %v", err)
	}

	res, err := store.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}

	if len(res.Rejections) != 1 {
		t.Fatalf("rejections = %v, want exactly one", res.Rejections)
	}
	rej := res.Rejections[0]
	if rej.CommitID != unavailableHash.String() {
		t.Errorf("rejection commit = %s, want %s", rej.CommitID, unavailableHash.String())
	}
	if rej.Reason != dag.RejectObjectUnavailable {
		t.Errorf("rejection reason = %q, want %q", rej.Reason, dag.RejectObjectUnavailable)
	}
	if rej.Reason == codec.RejectMissingOpJSON || rej.Reason == codec.RejectNonCanonicalPayload {
		t.Errorf("rejection reason = %q, must not be a reader-validation reason for a merely-absent object", rej.Reason)
	}

	// The valid sibling op is unaffected.
	if len(res.Ops["w-1"]) != 1 || res.Ops["w-1"][0].ID != op1.ID {
		t.Fatalf("Ops[w-1] = %v, want exactly [%s]", res.Ops["w-1"], op1.ID)
	}
}
