package dag_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
)

func TestEnumerate_IncrementalCost(t *testing.T) {
	dir, _ := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store, err := dag.Open(dir, ident, withVocabularies())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// 1. Append 50 ops
	for i := 0; i < 50; i++ {
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

	// Cold enumeration
	res1, err := store.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}
	if res1.DecodedCommits != 50 {
		t.Fatalf("res1.DecodedCommits = %d, want 50", res1.DecodedCommits)
	}
	if len(res1.Ops["w-1"]) != 50 {
		t.Fatalf("len(res1.Ops[w-1]) = %d, want 50", len(res1.Ops["w-1"]))
	}
	if len(res1.Rewound) != 0 {
		t.Fatalf("unexpected rewound chains: %v", res1.Rewound)
	}
	if len(res1.Rejections) != 0 {
		t.Fatalf("unexpected rejections: %v", res1.Rejections)
	}

	// 2. Append 3 more ops
	for i := 50; i < 53; i++ {
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

	// Warm enumeration from cursors
	res2, err := store.EnumerateSince(res1.Cursors)
	if err != nil {
		t.Fatalf("EnumerateSince failed: %v", err)
	}
	if res2.DecodedCommits != 3 {
		t.Fatalf("res2.DecodedCommits = %d, want 3 (O(new ops))", res2.DecodedCommits)
	}
	if len(res2.Ops["w-1"]) != 3 {
		t.Fatalf("len(res2.Ops[w-1]) = %d, want 3", len(res2.Ops["w-1"]))
	}

	// 3. Enumerate again with no new ops
	res3, err := store.EnumerateSince(res2.Cursors)
	if err != nil {
		t.Fatalf("EnumerateSince (no-op) failed: %v", err)
	}
	if res3.DecodedCommits != 0 {
		t.Fatalf("res3.DecodedCommits = %d, want 0", res3.DecodedCommits)
	}
	if len(res3.Ops) != 0 {
		t.Fatalf("len(res3.Ops) = %d, want 0", len(res3.Ops))
	}
}

func TestEnumerate_RewindDetection(t *testing.T) {
	dir, repo := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store, err := dag.Open(dir, ident, withVocabularies())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Append 3 ops
	var ops []*codec.Op
	for i := 0; i < 3; i++ {
		env := codec.Envelope{
			ObjectID:   "w-1",
			ObjectType: "widget",
			OpType:     "update",
			OpVersion:  1,
			Body:       json.RawMessage(fmt.Sprintf(`{"seq":%d,"title":"Op %d"}`, i, i)),
		}
		op, err := store.Append(context.Background(), env, nil)
		if err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}
		ops = append(ops, op)
	}

	res1, err := store.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}
	if res1.DecodedCommits != 3 {
		t.Fatalf("res1.DecodedCommits = %d, want 3", res1.DecodedCommits)
	}

	// Force-move chain ref backwards to op 0 (ops[0].ID)
	refName := dag.LocalRefName(ident.WriterID, "widget")
	err = repo.Storer.SetReference(plumbing.NewHashReference(refName, plumbing.NewHash(ops[0].ID)))
	if err != nil {
		t.Fatalf("SetReference failed: %v", err)
	}

	// EnumerateSince with old cursor at op 2
	res2, err := store.EnumerateSince(res1.Cursors)
	if err != nil {
		t.Fatalf("EnumerateSince failed: %v", err)
	}

	// Rewind must be reported
	if len(res2.Rewound) != 1 || res2.Rewound[0] != refName.String() {
		t.Fatalf("expected rewound ref %s, got %v", refName, res2.Rewound)
	}
	// Full walk of rewound chain was performed -> 1 op (ops[0]) returned
	if len(res2.Ops["w-1"]) != 1 || res2.Ops["w-1"][0].ID != ops[0].ID {
		t.Fatalf("expected op 0 returned on rewound chain, got %v", res2.Ops["w-1"])
	}
}

func TestEnumerate_PackedRefs(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	dir, repo := initTestRepo(t)
	ident1 := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	ident2 := testIdentity("fedcba9876543210", "Bob", "bob@example.test")

	store1, _ := dag.Open(dir, ident1, withVocabularies())
	store2, _ := dag.Open(dir, ident2, withVocabularies())

	for i := 0; i < 5; i++ {
		env := codec.Envelope{
			ObjectID:   "w-1",
			ObjectType: "widget",
			OpType:     "update",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"Packed"}`),
		}
		_, _ = store1.Append(context.Background(), env, nil)
		_, _ = store2.Append(context.Background(), env, nil)
	}

	// Run git pack-refs --all
	cmd := exec.Command("git", "-C", dir, "pack-refs", "--all")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git pack-refs failed: %v (output: %s)", err, string(out))
	}

	// Re-open store and enumerate packed refs
	storePacked, err := dag.OpenRepo(repo, ident1, withVocabularies())
	if err != nil {
		t.Fatalf("OpenRepo failed: %v", err)
	}

	res, err := storePacked.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate on packed refs failed: %v", err)
	}

	if len(res.Ops["w-1"]) != 10 {
		t.Fatalf("len(res.Ops[w-1]) = %d, want 10", len(res.Ops["w-1"]))
	}
	if len(res.Cursors) != 2 {
		t.Fatalf("len(res.Cursors) = %d, want 2", len(res.Cursors))
	}
}

func TestEnumerate_TwoReposGitFetch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	// Remote repo (server)
	remoteDir := t.TempDir()
	cmdInit := exec.Command("git", "init", "--bare", remoteDir)
	if out, err := cmdInit.CombinedOutput(); err != nil {
		t.Fatalf("git init bare failed: %v (%s)", err, out)
	}

	// Writer 1 clone
	w1Dir := t.TempDir()
	cmdClone := exec.Command("git", "clone", remoteDir, w1Dir)
	if out, err := cmdClone.CombinedOutput(); err != nil {
		t.Fatalf("git clone w1 failed: %v (%s)", err, out)
	}

	// Writer 2 clone
	w2Dir := t.TempDir()
	cmdClone2 := exec.Command("git", "clone", remoteDir, w2Dir)
	if out, err := cmdClone2.CombinedOutput(); err != nil {
		t.Fatalf("git clone w2 failed: %v (%s)", err, out)
	}

	// Writer 1 appends ops and pushes to origin
	ident1 := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store1, err := dag.Open(w1Dir, ident1, withVocabularies())
	if err != nil {
		t.Fatalf("Open w1 failed: %v", err)
	}

	env1 := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"From Alice"}`),
	}
	op1, err := store1.Append(context.Background(), env1, nil)
	if err != nil {
		t.Fatalf("w1 append failed: %v", err)
	}

	cmdPush1 := exec.Command("git", "-C", w1Dir, "push", "origin", "refs/writ/0123456789abcdef/widget:refs/writ/0123456789abcdef/widget")
	if out, err := cmdPush1.CombinedOutput(); err != nil {
		t.Fatalf("w1 push failed: %v (%s)", err, out)
	}

	// Writer 2 configures the normative fetch refspec: refs/writ/*:refs/remotes/origin/writ/*
	cmdConfig := exec.Command("git", "-C", w2Dir, "config", "--add", "remote.origin.fetch", "refs/writ/*:refs/remotes/origin/writ/*")
	if out, err := cmdConfig.CombinedOutput(); err != nil {
		t.Fatalf("git config remote.origin.fetch failed: %v (%s)", err, out)
	}

	// Writer 2 fetches
	cmdFetch := exec.Command("git", "-C", w2Dir, "fetch", "origin")
	if out, err := cmdFetch.CombinedOutput(); err != nil {
		t.Fatalf("git fetch failed: %v (%s)", err, out)
	}

	// Writer 2 opens DAG store and enumerates
	ident2 := testIdentity("fedcba9876543210", "Bob", "bob@example.test")
	store2, err := dag.Open(w2Dir, ident2, withVocabularies())
	if err != nil {
		t.Fatalf("Open w2 failed: %v", err)
	}

	res, err := store2.Enumerate()
	if err != nil {
		t.Fatalf("w2 Enumerate failed: %v", err)
	}

	// Must discover Alice's remote-tracking chain and ops cold!
	if len(res.Ops["w-1"]) != 1 || res.Ops["w-1"][0].ID != op1.ID {
		t.Fatalf("expected Alice's op %s in w2 enumeration, got %v", op1.ID, res.Ops["w-1"])
	}
	expectedRemoteRef := "refs/remotes/origin/writ/0123456789abcdef/widget"
	if res.Cursors[expectedRemoteRef] != op1.ID {
		t.Fatalf("cursor %s = %s, want %s", expectedRemoteRef, res.Cursors[expectedRemoteRef], op1.ID)
	}
}
