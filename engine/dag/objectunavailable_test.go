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

// writeCommitWithPresentWrongTypeOpJSON writes a chain-tip commit whose
// op.json tree entry names a git object that IS present in the store but
// is not a blob — either because the entry's own mode says it isn't a
// regular file (mode passed as filemode.Dir), or because a regular-mode
// entry's hash happens to name a tree instead of a blob. Both are reader-
// validation rejections that predate WRIT-271 (invalid-op-json-mode and
// non-canonical-payload respectively): go-git's typed object lookups
// report plumbing.ErrObjectNotFound for this "present but wrong type"
// case exactly as they do for genuine absence, so these must not
// classify as object-unavailable (WRIT-271 round 1 review).
func writeCommitWithPresentWrongTypeOpJSON(repo *git.Repository, parent plumbing.Hash, opJSONMode filemode.FileMode) (plumbing.Hash, error) {
	// An arbitrary present tree object for op.json to (wrongly) name.
	innerTree := &object.Tree{}
	innerObj := repo.Storer.NewEncodedObject()
	innerObj.SetType(plumbing.TreeObject)
	if err := innerTree.Encode(innerObj); err != nil {
		return plumbing.ZeroHash, err
	}
	innerHash, err := repo.Storer.SetEncodedObject(innerObj)
	if err != nil {
		return plumbing.ZeroHash, err
	}

	tree := &object.Tree{Entries: []object.TreeEntry{
		{Name: "op.json", Mode: opJSONMode, Hash: innerHash},
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
		Message:      "writ: create widget/wrongtype\n",
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

// writeBlob writes an arbitrary blob object into repo's object store and
// returns its hash. Used to build tree/commit shapes whose op.json entry
// or TreeHash names a git object that IS present but is a blob, not a
// tree — the WRIT-271 round 2 review case: object.GetTree and
// commit.Tree() are typed lookups that report plumbing.ErrObjectNotFound
// for this "present but wrong type" case exactly as they do for genuine
// absence, and fromGitCommit's presence probe for it must not depend on
// what kind of present object was found.
func writeBlob(repo *git.Repository, data []byte) (plumbing.Hash, error) {
	blobObj := repo.Storer.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	w, err := blobObj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return repo.Storer.SetEncodedObject(blobObj)
}

// writeCommitWithEntryNamingPresentBlob writes a chain-tip commit whose
// tree has exactly entries, unmodified — letting the caller point any
// entry's hash at a blob that IS present in the store instead of a tree.
func writeCommitWithEntryNamingPresentBlob(repo *git.Repository, parent plumbing.Hash, entries []object.TreeEntry) (plumbing.Hash, error) {
	tree := &object.Tree{Entries: entries}
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
		Message:      "writ: create widget/wrongtype\n",
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

// writeCommitWithTreeHashNamingPresentBlob writes a chain-tip commit whose
// TreeHash itself names a blob that IS present in the store but is never
// a tree at all — the shape that exercises fromGitCommit's commit.Tree()
// call rather than its op.json or subtree handling.
func writeCommitWithTreeHashNamingPresentBlob(repo *git.Repository, parent, blobHash plumbing.Hash) (plumbing.Hash, error) {
	sig := object.Signature{Name: "Mallory", Email: "mallory@example.test", When: time.Now().UTC()}
	commit := &object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      "writ: create widget/wrongtype\n",
		TreeHash:     blobHash,
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

// TestEnumerate_InvalidModePresentTreeNotObjectUnavailable pins the first
// row of WRIT-271 round 1's finding: an op.json entry with a non-regular
// mode (040000) naming a tree that IS present in the store violates
// spec/op-envelope.md rule 1 (op.json must be mode 100644) and must be
// reported invalid-op-json-mode, not object-unavailable — the repository
// is complete, so telling the operator the object is missing from this
// clone is wrong.
func TestEnumerate_InvalidModePresentTreeNotObjectUnavailable(t *testing.T) {
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

	badHash, err := writeCommitWithPresentWrongTypeOpJSON(repo, plumbing.NewHash(op1.ID), filemode.Dir)
	if err != nil {
		t.Fatalf("writeCommitWithPresentWrongTypeOpJSON failed: %v", err)
	}
	refName := plumbing.ReferenceName("refs/writ/0123456789abcdef/widget")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, badHash)); err != nil {
		t.Fatalf("advance ref to bad commit: %v", err)
	}

	res, err := store.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}
	if len(res.Rejections) != 1 {
		t.Fatalf("rejections = %v, want exactly one", res.Rejections)
	}
	if rej := res.Rejections[0]; rej.Reason != codec.RejectInvalidOpJSONMode {
		t.Errorf("rejection reason = %q, want %q", rej.Reason, codec.RejectInvalidOpJSONMode)
	}
}

// TestEnumerate_ValidModeHashNamesTreeNotObjectUnavailable pins the second
// row of WRIT-271 round 1's finding: an op.json entry with the correct
// mode (100644) whose hash happens to name a present tree instead of a
// blob must be reported non-canonical-payload, not object-unavailable.
func TestEnumerate_ValidModeHashNamesTreeNotObjectUnavailable(t *testing.T) {
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

	badHash, err := writeCommitWithPresentWrongTypeOpJSON(repo, plumbing.NewHash(op1.ID), filemode.Regular)
	if err != nil {
		t.Fatalf("writeCommitWithPresentWrongTypeOpJSON failed: %v", err)
	}
	refName := plumbing.ReferenceName("refs/writ/0123456789abcdef/widget")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, badHash)); err != nil {
		t.Fatalf("advance ref to bad commit: %v", err)
	}

	res, err := store.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}
	if len(res.Rejections) != 1 {
		t.Fatalf("rejections = %v, want exactly one", res.Rejections)
	}
	if rej := res.Rejections[0]; rej.Reason != codec.RejectNonCanonicalPayload {
		t.Errorf("rejection reason = %q, want %q", rej.Reason, codec.RejectNonCanonicalPayload)
	}
}

// TestEnumerate_InvalidModePresentBlobNotObjectUnavailable pins WRIT-271
// round 2's finding: an op.json entry with a non-regular mode (040000)
// whose hash names a *blob* that IS present in the store must be reported
// invalid-op-json-mode, not object-unavailable. This is the case round
// 1's TestEnumerate_InvalidModePresentTreeNotObjectUnavailable failed to
// cover — it points its bad entry at a present tree, the one shape
// object.GetTree succeeds on, so it passed while this bug (an unprobed
// object.GetTree call in fromGitCommit's filemode.Dir branch) survived.
func TestEnumerate_InvalidModePresentBlobNotObjectUnavailable(t *testing.T) {
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

	blobHash, err := writeBlob(repo, []byte("not a tree"))
	if err != nil {
		t.Fatalf("writeBlob failed: %v", err)
	}

	badHash, err := writeCommitWithEntryNamingPresentBlob(repo, plumbing.NewHash(op1.ID), []object.TreeEntry{
		{Name: "op.json", Mode: filemode.Dir, Hash: blobHash},
	})
	if err != nil {
		t.Fatalf("writeCommitWithEntryNamingPresentBlob failed: %v", err)
	}
	refName := plumbing.ReferenceName("refs/writ/0123456789abcdef/widget")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, badHash)); err != nil {
		t.Fatalf("advance ref to bad commit: %v", err)
	}

	res, err := store.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}
	if len(res.Rejections) != 1 {
		t.Fatalf("rejections = %v, want exactly one", res.Rejections)
	}
	if rej := res.Rejections[0]; rej.Reason != codec.RejectInvalidOpJSONMode {
		t.Errorf("rejection reason = %q, want %q", rej.Reason, codec.RejectInvalidOpJSONMode)
	}
}

// TestEnumerate_ExtraTreeEntryPresentBlobNotObjectUnavailable pins
// WRIT-271 round 2's finding: an "extra" tree entry (mode 040000) besides
// op.json, whose hash names a *blob* that IS present in the store, must
// be reported extra-tree-entry, not object-unavailable — the same
// unprobed object.GetTree call as
// TestEnumerate_InvalidModePresentBlobNotObjectUnavailable, exercised
// through a second tree entry instead of op.json itself.
func TestEnumerate_ExtraTreeEntryPresentBlobNotObjectUnavailable(t *testing.T) {
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

	opJSONBlobHash, err := writeBlob(repo, []byte(`{}`))
	if err != nil {
		t.Fatalf("writeBlob failed: %v", err)
	}
	extraBlobHash, err := writeBlob(repo, []byte("not a tree"))
	if err != nil {
		t.Fatalf("writeBlob failed: %v", err)
	}

	badHash, err := writeCommitWithEntryNamingPresentBlob(repo, plumbing.NewHash(op1.ID), []object.TreeEntry{
		{Name: "extra", Mode: filemode.Dir, Hash: extraBlobHash},
		{Name: "op.json", Mode: filemode.Regular, Hash: opJSONBlobHash},
	})
	if err != nil {
		t.Fatalf("writeCommitWithEntryNamingPresentBlob failed: %v", err)
	}
	refName := plumbing.ReferenceName("refs/writ/0123456789abcdef/widget")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, badHash)); err != nil {
		t.Fatalf("advance ref to bad commit: %v", err)
	}

	res, err := store.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}
	if len(res.Rejections) != 1 {
		t.Fatalf("rejections = %v, want exactly one", res.Rejections)
	}
	if rej := res.Rejections[0]; rej.Reason != codec.RejectExtraTreeEntry {
		t.Errorf("rejection reason = %q, want %q", rej.Reason, codec.RejectExtraTreeEntry)
	}
}

// TestEnumerate_MissingOpJSONPresentBlobTreeHashNotObjectUnavailable pins
// WRIT-271 round 2's finding: a commit whose TreeHash names a *blob* that
// IS present in the store, rather than a tree at all, must be reported
// missing-op-json, not object-unavailable — fromGitCommit's commit.Tree()
// call was the other typed lookup round 2 found unprobed.
func TestEnumerate_MissingOpJSONPresentBlobTreeHashNotObjectUnavailable(t *testing.T) {
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

	blobHash, err := writeBlob(repo, []byte("not a tree"))
	if err != nil {
		t.Fatalf("writeBlob failed: %v", err)
	}

	badHash, err := writeCommitWithTreeHashNamingPresentBlob(repo, plumbing.NewHash(op1.ID), blobHash)
	if err != nil {
		t.Fatalf("writeCommitWithTreeHashNamingPresentBlob failed: %v", err)
	}
	refName := plumbing.ReferenceName("refs/writ/0123456789abcdef/widget")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, badHash)); err != nil {
		t.Fatalf("advance ref to bad commit: %v", err)
	}

	res, err := store.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}
	if len(res.Rejections) != 1 {
		t.Fatalf("rejections = %v, want exactly one", res.Rejections)
	}
	if rej := res.Rejections[0]; rej.Reason != codec.RejectMissingOpJSON {
		t.Errorf("rejection reason = %q, want %q", rej.Reason, codec.RejectMissingOpJSON)
	}
}
