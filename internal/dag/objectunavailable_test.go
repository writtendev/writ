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
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/dag"
)

// writeCommitWithMissingOpJSONBlob writes a commit directly into repo's
// object store — bypassing dag.Store.Append, which would never produce
// this shape — whose tree names an op.json blob that is never itself
// written to the storer. This is the shape a partial clone
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

// writeCommitWithAbsentParentCommit writes a chain-tip commit that reuses
// an existing, valid tree (treeHash) — so the tip's own op.json decodes
// cleanly — but names a parent commit that is never written to repo's
// object store. This is the other shape a chain can reach past what
// this clone holds: unlike writeCommitWithMissingOpJSONBlob's withheld
// tree/blob, no partial-clone fetch filter can produce an absent
// commit — a filter withholds blobs and trees, never commits.
func writeCommitWithAbsentParentCommit(repo *git.Repository, treeHash, parent plumbing.Hash) (plumbing.Hash, error) {
	sig := object.Signature{Name: "Mallory", Email: "mallory@example.test", When: time.Now().UTC()}
	commit := &object.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      "writ: create widget/absentparent\n",
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

// TestEnumerate_AbsentParentCommitIsObjectUnavailable pins the other of
// RejectObjectUnavailable's two production sites (WRIT-299): a chain
// tip whose own op decodes cleanly but whose parent commit — reached
// through commitObj.ParentHashes rather than the op.json/tree path
// TestEnumerate_ObjectUnavailableDistinctFromMalformed exercises — is
// absent from this clone's object store. Before the widened godoc, only
// the withheld-blob shape was named as RejectObjectUnavailable's cause;
// this pins that the absent-parent shape classifies the same way, not
// as a reader-validation reason, even though no partial-clone fetch
// filter can produce it.
func TestEnumerate_AbsentParentCommitIsObjectUnavailable(t *testing.T) {
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

	op1Commit, err := object.GetCommit(repo.Storer, plumbing.NewHash(op1.ID))
	if err != nil {
		t.Fatalf("GetCommit(op1) failed: %v", err)
	}

	// A well-formed hash that is never written to repo's object store.
	absentParent := plumbing.NewHash("abcdef0123456789abcdef0123456789abcdef01")
	tipHash, err := writeCommitWithAbsentParentCommit(repo, op1Commit.TreeHash, absentParent)
	if err != nil {
		t.Fatalf("writeCommitWithAbsentParentCommit failed: %v", err)
	}
	refName := plumbing.ReferenceName("refs/writ/0123456789abcdef/widget")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, tipHash)); err != nil {
		t.Fatalf("advance ref to tip commit: %v", err)
	}

	res, err := store.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}

	if len(res.Rejections) != 1 {
		t.Fatalf("rejections = %v, want exactly one", res.Rejections)
	}
	rej := res.Rejections[0]
	if rej.CommitID != absentParent.String() {
		t.Errorf("rejection commit = %s, want %s (the absent parent, not the tip)", rej.CommitID, absentParent.String())
	}
	if rej.Reason != dag.RejectObjectUnavailable {
		t.Errorf("rejection reason = %q, want %q", rej.Reason, dag.RejectObjectUnavailable)
	}
	if rej.Reason == codec.RejectMissingOpJSON {
		t.Errorf("rejection reason = %q, must not be a reader-validation reason for a merely-absent commit", rej.Reason)
	}

	// The tip's own op — a distinct commit from op1, decoded off op1's
	// reused tree — still lands in Ops despite its parent's rejection.
	if len(res.Ops["w-1"]) != 1 || res.Ops["w-1"][0].ID != tipHash.String() {
		t.Fatalf("Ops[w-1] = %v, want exactly [%s]", res.Ops["w-1"], tipHash.String())
	}
}

// TestEnumerate_ObjectUnavailableDistinctFromMalformed pins WRIT-271's
// absent-vs-malformed split at the dag layer: a chain whose tip's op.json
// blob is absent from the object store — the shape a partial
// clone leaves behind — is reported with reason object-unavailable, not
// missing-op-json (a genuine tree-shape violation) and not
// non-canonical-payload (a genuinely malformed payload). Before this fix,
// codec.FromGitCommit's readOpJSONBlob swallowed the open()/Reader()
// failure to nil, nil, so this exact shape decoded as an empty payload and
// was reported as non-canonical-payload — an absent object misreported as
// a malformed op.
//
// It also pins the WRIT-289 orchestrator decision on top of the ruling,
// for the one object-unavailable shape it lets expand: the tip's own
// commit object is present, and its root tree passes every tree-shape
// check — exactly one entry, named op.json, a regular-file blob at mode
// 100644 — missing only that entry's own blob, so it is not known to be
// a non-op. op1, sitting behind the object-unavailable tip, is therefore
// still held. Every other shape does not expand — an absent root tree or
// one present without a top-level op.json entry
// (TestEnumerate_RefOnAbsentRootTreeHistoryStopsAtTip and
// TestEnumerate_AbsentSubtreeNoRootOpJSONStopsAtTip, enumerate_test.go),
// or a root tree that already fails some other tree-shape check — an
// extra entry, op.json as a directory, or the wrong mode
// (TestEnumerate_RootOpJSONBlobAbsentTreeShapes, this file).
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

	// op1 sits behind the rejected (object-unavailable) tip on the same
	// chain. WRIT-289's orchestrator decision: this shape — root tree
	// passing every tree-shape rule, missing only its own op.json blob —
	// does not stop the walk, so the tip's parents are still expanded and
	// op1 is held.
	if len(res.Ops["w-1"]) != 1 || res.Ops["w-1"][0].ID != op1.ID {
		t.Fatalf("Ops[w-1] = %v, want exactly [%s] (op1 sits behind the object-unavailable tip, but a missing op.json blob alone does not stop the walk)", res.Ops["w-1"], op1.ID)
	}
}

// TestEnumerate_ObjectUnavailableMidChainDoesNotStopWalk pins the WRIT-289
// orchestrator decision squarely on the case that matters for it: an
// object-unavailable commit sitting *between* two valid ops, not standing
// alone as the chain tip. Chain shape: op1 (create) <- unavailable (its
// op.json blob absent) <- op2 (update, the tip). Both op1 and op2 must be
// held, and the unavailable commit must be reported as exactly one
// rejection — proving the walk crossed it in both directions (it reached
// op1 behind it, and dag.Store.Append, which never validates its own
// chain tip's op.json, was able to build op2 on top of it).
func TestEnumerate_ObjectUnavailableMidChainDoesNotStopWalk(t *testing.T) {
	dir, repo := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store, err := dag.Open(dir, ident, withVocabularies())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	op1, err := store.Append(context.Background(), codec.Envelope{
		ObjectID: "w-1", ObjectType: "widget", OpType: "create", OpVersion: 1,
		Body: json.RawMessage(`{"title":"Widget 1"}`),
	}, nil)
	if err != nil {
		t.Fatalf("Append op1 failed: %v", err)
	}

	unavailableHash, err := writeCommitWithMissingOpJSONBlob(repo, plumbing.NewHash(op1.ID))
	if err != nil {
		t.Fatalf("writeCommitWithMissingOpJSONBlob failed: %v", err)
	}
	refName := plumbing.ReferenceName("refs/writ/0123456789abcdef/widget")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, unavailableHash)); err != nil {
		t.Fatalf("advance ref to unavailable commit: %v", err)
	}

	op2, err := store.Append(context.Background(), codec.Envelope{
		ObjectID: "w-1", ObjectType: "widget", OpType: "update", OpVersion: 1,
		Body: json.RawMessage(`{"title":"Widget 1 v2"}`),
	}, nil)
	if err != nil {
		t.Fatalf("Append op2 failed: %v", err)
	}

	res, err := store.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}

	if len(res.Rejections) != 1 {
		t.Fatalf("rejections = %v, want exactly one", res.Rejections)
	}
	if rej := res.Rejections[0]; rej.CommitID != unavailableHash.String() || rej.Reason != dag.RejectObjectUnavailable {
		t.Errorf("rejection = %+v, want commit %s reason %q", rej, unavailableHash.String(), dag.RejectObjectUnavailable)
	}

	ops := res.Ops["w-1"]
	if len(ops) != 2 {
		t.Fatalf("Ops[w-1] = %v, want 2 (op1 and op2; the object-unavailable commit between them does not stop the walk in either direction)", ops)
	}
	byID := make(map[string]bool, len(ops))
	for _, op := range ops {
		byID[op.ID] = true
	}
	if !byID[op1.ID] || !byID[op2.ID] {
		t.Fatalf("Ops[w-1] = %v, want exactly [%s, %s]", ops, op1.ID, op2.ID)
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

// TestEnumerate_RootOpJSONBlobAbsentTreeShapes pins the round 3
// orchestrator decision's narrowing of rootOpJSONBlobAbsent (WRIT-289): a
// RejectObjectUnavailable commit's parents are expanded only when its
// root tree passes every op-envelope tree-shape rule this reader can
// check without the blob's own bytes — exactly one entry, named
// "op.json", a regular-file blob at mode 100644 — and is missing only
// that entry's own blob. Round 2's cut checked only "an op.json entry
// exists and its blob is absent," which let each of the four bad shapes
// below — every one a reader-validation rejection in its own right, once
// the blob is actually read — expand anyway: a real --filter=blob:none
// or --filter=tree:1 clone with a writ ref pointed at ordinary code
// history carrying one of these shapes at every commit walked the whole
// history instead of stopping at the tip.
//
// Each row builds a rootOpJSONBlobAbsentChainDepth-commit chain of the
// same tree shape and points a writ ref at the tip — mirroring what an
// attacker, or a filtered clone of an unlucky ordinary repository, could
// produce. The four bad shapes must each stop the walk at the tip
// (DecodedCommits == 1, only the tip). The fifth, valid shape — the one
// true exception the ruling's orchestrator decision allows — must walk
// the whole chain (DecodedCommits == rootOpJSONBlobAbsentChainDepth),
// pinning that the narrowing does not overcorrect and stop there too.
const rootOpJSONBlobAbsentChainDepth = 300

func TestEnumerate_RootOpJSONBlobAbsentTreeShapes(t *testing.T) {
	absentOpJSONBlob := plumbing.NewHash("0123456789abcdef0123456789abcdef01234567")
	absentOpJSONBlob2 := plumbing.NewHash("fedcba9876543210fedcba9876543210fedcba98")
	absentSubtree := plumbing.NewHash("1111111111111111111111111111111111111111")

	tests := []struct {
		name         string
		wantDecoded  int
		buildEntries func(repo *git.Repository) ([]object.TreeEntry, error)
	}{
		{
			// Reviewer shape 1: op.json plus another top-level entry.
			// extra-tree-entry, whatever the other entry's own blob
			// holds — here a blob that IS present, the shape a
			// blob:limit filter that let a small README through but
			// withheld a large op.json would leave behind.
			name:        "op_json_plus_extra_entry",
			wantDecoded: 1,
			buildEntries: func(repo *git.Repository) ([]object.TreeEntry, error) {
				readmeBlob, err := writeBlob(repo, []byte("hello"))
				if err != nil {
					return nil, err
				}
				return []object.TreeEntry{
					{Name: "README.md", Mode: filemode.Regular, Hash: readmeBlob},
					{Name: "op.json", Mode: filemode.Regular, Hash: absentOpJSONBlob},
				}, nil
			},
		},
		{
			// Reviewer shape 2: op.json present as a directory, its
			// subtree absent. invalid-op-json-mode territory — op.json is
			// not a blob at all.
			name:        "op_json_as_absent_directory",
			wantDecoded: 1,
			buildEntries: func(repo *git.Repository) ([]object.TreeEntry, error) {
				return []object.TreeEntry{
					{Name: "op.json", Mode: filemode.Dir, Hash: absentSubtree},
				}, nil
			},
		},
		{
			// Reviewer shape 3: op.json at mode 100755. invalid-op-json-mode.
			name:        "op_json_invalid_mode",
			wantDecoded: 1,
			buildEntries: func(repo *git.Repository) ([]object.TreeEntry, error) {
				return []object.TreeEntry{
					{Name: "op.json", Mode: filemode.Executable, Hash: absentOpJSONBlob},
				}, nil
			},
		},
		{
			// Reviewer shape 4: op.json blob absent AND another object
			// (a subtree) also absent. spec/ref-layout.md's exception
			// applies only when the op.json blob is the *sole*
			// locally-absent object; this tree has two.
			name:        "op_json_absent_plus_other_object_absent",
			wantDecoded: 1,
			buildEntries: func(repo *git.Repository) ([]object.TreeEntry, error) {
				return []object.TreeEntry{
					{Name: "op.json", Mode: filemode.Regular, Hash: absentOpJSONBlob},
					{Name: "src", Mode: filemode.Dir, Hash: absentSubtree},
				}, nil
			},
		},
		{
			// The one true exception: exactly one entry, named op.json,
			// a regular-file blob at mode 100644, and that blob is the
			// only thing locally absent. The walk must keep going.
			name:        "op_json_blob_only_absent",
			wantDecoded: rootOpJSONBlobAbsentChainDepth,
			buildEntries: func(repo *git.Repository) ([]object.TreeEntry, error) {
				return []object.TreeEntry{
					{Name: "op.json", Mode: filemode.Regular, Hash: absentOpJSONBlob2},
				}, nil
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, repo := initTestRepo(t)
			ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
			store, err := dag.Open(dir, ident, withVocabularies())
			if err != nil {
				t.Fatalf("Open failed: %v", err)
			}

			entries, err := tc.buildEntries(repo)
			if err != nil {
				t.Fatalf("buildEntries failed: %v", err)
			}

			tree := &object.Tree{Entries: entries}
			treeObj := repo.Storer.NewEncodedObject()
			treeObj.SetType(plumbing.TreeObject)
			if err := tree.Encode(treeObj); err != nil {
				t.Fatalf("encode tree: %v", err)
			}
			treeHash, err := repo.Storer.SetEncodedObject(treeObj)
			if err != nil {
				t.Fatalf("store tree: %v", err)
			}

			// Every commit in the chain reuses the same tree — an
			// attacker-chosen chain of any length can point every commit's
			// op.json at the same withheld blob, so sharing the tree here
			// is not a simplification that understates the walk's cost;
			// it is the realistic shape.
			sig := object.Signature{Name: "Mallory", Email: "mallory@example.test", When: time.Now().UTC()}
			tip := plumbing.ZeroHash
			for i := 0; i < rootOpJSONBlobAbsentChainDepth; i++ {
				commit := &object.Commit{
					Author:    sig,
					Committer: sig,
					Message:   "writ: create widget/chain\n",
					TreeHash:  treeHash,
				}
				if !tip.IsZero() {
					commit.ParentHashes = []plumbing.Hash{tip}
				}
				commitObj := repo.Storer.NewEncodedObject()
				commitObj.SetType(plumbing.CommitObject)
				if err := commit.Encode(commitObj); err != nil {
					t.Fatalf("encode commit %d: %v", i, err)
				}
				h, err := repo.Storer.SetEncodedObject(commitObj)
				if err != nil {
					t.Fatalf("store commit %d: %v", i, err)
				}
				tip = h
			}

			refName := plumbing.ReferenceName("refs/writ/0123456789abcdef/widget")
			if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, tip)); err != nil {
				t.Fatalf("set widget ref to chain tip: %v", err)
			}

			res, err := store.Enumerate()
			if err != nil {
				t.Fatalf("Enumerate failed: %v", err)
			}

			if res.DecodedCommits != tc.wantDecoded {
				t.Errorf("DecodedCommits = %d, want %d", res.DecodedCommits, tc.wantDecoded)
			}
			if len(res.Rejections) != tc.wantDecoded {
				t.Errorf("len(Rejections) = %d, want %d", len(res.Rejections), tc.wantDecoded)
			}
			for _, rej := range res.Rejections {
				if rej.Reason != dag.RejectObjectUnavailable {
					t.Errorf("rejection reason = %q, want %q", rej.Reason, dag.RejectObjectUnavailable)
				}
			}
			if len(res.Ops) != 0 {
				t.Errorf("Ops = %v, want none (every commit in the chain is a rejection, never a held op)", res.Ops)
			}
		})
	}
}
