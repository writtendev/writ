package dag_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/sshsig"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
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

// TestEnumerateSince_WithSeen_DeepCausalParent is WRIT-273's regression:
// EnumerateSince's Step 3 walk stopped only at each chain's own cursor tip
// (stopBoundary), so a new op on one chain whose causal parent sits deep
// inside a *different* chain's history re-walked that whole chain from the
// causal parent back to its root, even though every one of those commits
// was already decoded and projected by an earlier pass. Builds a 50-op
// widget chain (chain A), takes a cold EnumerateSince to seed cursors and
// collect chain A's op IDs, then appends one waypoint op (chain B) whose
// causal parent is chain A's 25th op — a mid-chain commit, not its tip.
//
// Without dag.WithSeen, EnumerateSince(cursors) still walks from that
// causal parent all the way back to chain A's root (25 ancestors + the new
// op itself = 26 DecodedCommits) even though chain A's own cursor did not
// move — pinning the defect this test guards against. With
// dag.WithSeen(...) scoped to the first pass's op IDs, the walk stops the
// moment it reaches an already-seen commit: DecodedCommits == 1 and Ops
// contains only the new object.
func TestEnumerateSince_WithSeen_DeepCausalParent(t *testing.T) {
	dir, _ := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store, err := dag.Open(dir, ident, withVocabularies())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Chain A: 50 ops on widget/w-1.
	var chainAIDs []string
	for i := 0; i < 50; i++ {
		env := codec.Envelope{
			ObjectID:   "w-1",
			ObjectType: "widget",
			OpType:     "update",
			OpVersion:  1,
			Body:       json.RawMessage(fmt.Sprintf(`{"seq":%d,"title":"Op %d"}`, i, i)),
		}
		op, err := store.Append(context.Background(), env, nil)
		if err != nil {
			t.Fatalf("Append chain A op %d failed: %v", i, err)
		}
		chainAIDs = append(chainAIDs, op.ID)
	}

	// Cold pass: seeds cursors and stands in for the projection's ops
	// table already holding every one of chain A's op IDs.
	res1, err := store.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate failed: %v", err)
	}
	if res1.DecodedCommits != 50 {
		t.Fatalf("res1.DecodedCommits = %d, want 50", res1.DecodedCommits)
	}
	seen := make(map[string]bool, len(res1.Ops["w-1"]))
	for _, op := range res1.Ops["w-1"] {
		seen[op.ID] = true
	}

	// Chain B: one waypoint op whose causal parent is chain A's 25th op
	// (index 24), a mid-chain commit — not chain A's tip.
	midParent := chainAIDs[24]
	envWaypoint := codec.Envelope{
		ObjectID:   "wp-1",
		ObjectType: "waypoint",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"text":"deep parent"}`),
	}
	opWaypoint, err := store.Append(context.Background(), envWaypoint, []string{midParent})
	if err != nil {
		t.Fatalf("Append waypoint failed: %v", err)
	}

	// Without WithSeen: re-walks chain A's ancestry from the causal parent
	// back to its root (25 ancestors, indices 0..24) plus the new op.
	resNoSeen, err := store.EnumerateSince(res1.Cursors)
	if err != nil {
		t.Fatalf("EnumerateSince (no WithSeen) failed: %v", err)
	}
	if resNoSeen.DecodedCommits != 26 {
		t.Fatalf("resNoSeen.DecodedCommits = %d, want 26 (defect: re-walked chain A's ancestry)", resNoSeen.DecodedCommits)
	}

	// With WithSeen over the first pass's op IDs: the walk stops at
	// midParent instead of expanding into it.
	resSeen, err := store.EnumerateSince(res1.Cursors, dag.WithSeen(func(opID string) bool { return seen[opID] }))
	if err != nil {
		t.Fatalf("EnumerateSince (WithSeen) failed: %v", err)
	}
	if resSeen.DecodedCommits != 1 {
		t.Fatalf("resSeen.DecodedCommits = %d, want 1", resSeen.DecodedCommits)
	}
	if len(resSeen.Ops) != 1 {
		t.Fatalf("len(resSeen.Ops) = %d, want 1", len(resSeen.Ops))
	}
	if _, ok := resSeen.Ops["wp-1"]; !ok {
		t.Fatalf("resSeen.Ops missing wp-1: %+v", resSeen.Ops)
	}
	if got := resSeen.Ops["wp-1"][0].ID; got != opWaypoint.ID {
		t.Fatalf("resSeen.Ops[wp-1][0].ID = %s, want %s", got, opWaypoint.ID)
	}
}

// TestEnumerateSince_WithSeen_RewindStillDetected confirms dag.WithSeen
// (Step 3, the walk) cannot mask a rollback (Step 2, cursor analysis): the
// two are disjoint by construction — WithSeen is consulted only inside
// Step 3's queue, never in Step 2's isAncestor/stopBoundary-seeding logic —
// but this pins that as an observable guarantee rather than leaving it as
// an unverified property of the code's shape.
func TestEnumerateSince_WithSeen_RewindStillDetected(t *testing.T) {
	dir, repo := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store, err := dag.Open(dir, ident, withVocabularies())
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Modeled on TestEnumerate_RewindDetection: append 3 ops, then
	// force-move the chain ref backwards to op 0 -- a rewind, not a
	// fast-forward.
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
	// Ops already recorded by the pass that inserted them, standing in for
	// the projection's ops table.
	seen := make(map[string]bool, len(res1.Ops["w-1"]))
	for _, op := range res1.Ops["w-1"] {
		seen[op.ID] = true
	}

	refName := dag.LocalRefName(ident.WriterID, "widget")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, plumbing.NewHash(ops[0].ID))); err != nil {
		t.Fatalf("SetReference failed: %v", err)
	}

	res2, err := store.EnumerateSince(res1.Cursors, dag.WithSeen(func(opID string) bool { return seen[opID] }))
	if err != nil {
		t.Fatalf("EnumerateSince (WithSeen, rewound) failed: %v", err)
	}
	if len(res2.Rewound) != 1 || res2.Rewound[0] != refName.String() {
		t.Fatalf("res2.Rewound = %v, want [%s] (WithSeen must not mask rollback detection)", res2.Rewound, refName.String())
	}
	// Step 2's rewind detection (isAncestor, stopBoundary seeding) runs
	// entirely before Step 3 ever consults cfg.seen, so Rewound is
	// reported identically with or without WithSeen -- this is what lets
	// projection.Refresh's incremental path fall through to a full
	// rebuild (discarding this enumRes) exactly as it does without the
	// predicate. Here op 0 happens to already be in seen, so Step 3
	// legitimately treats it as an already-projected stop point and skips
	// re-decoding it (unlike TestEnumerate_RewindDetection's no-WithSeen
	// case, which re-decodes it) -- that difference is expected and
	// harmless, since Rewound alone is what the caller acts on.
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

	// Writer 2 configures the normative fetch refspec: +refs/writ/*:refs/remotes/origin/writ/*
	cmdConfig := exec.Command("git", "-C", w2Dir, "config", "--add", "remote.origin.fetch", "+refs/writ/*:refs/remotes/origin/writ/*")
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

// TestEnumerateSince_Verification pins WRIT-251's ingest wiring at the dag
// layer directly: EnumerateSince calls codec.Verify per decoded commit
// against whatever trust store the Store was opened with, the outcome
// rides on codec.Op.Verification, and none of it touches Rejections --
// which WRIT-271 owns, not this ticket.
func TestEnumerateSince_Verification(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not found on PATH")
	}

	dir, _ := initTestRepo(t)
	keyDir := t.TempDir()
	privPath := filepath.Join(keyDir, "id_alice")
	genCmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", privPath)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen unavailable: %v\n%s", err, out)
	}
	pubBytes, err := os.ReadFile(privPath + ".pub")
	if err != nil {
		t.Fatalf("read generated public key: %v", err)
	}
	pubLine := strings.TrimSpace(string(pubBytes))

	signer, err := codec.NewSigner(identity.SigningKey{Format: "ssh", Value: privPath})
	if err != nil {
		t.Fatalf("codec.NewSigner: %v", err)
	}

	allowedPath := filepath.Join(keyDir, "allowed_signers")
	if err := os.WriteFile(allowedPath, []byte("alice@example.test "+pubLine+"\n"), 0o600); err != nil {
		t.Fatalf("write allowed_signers: %v", err)
	}
	ts, err := sshsig.ParseAllowedSignersFile(allowedPath)
	if err != nil {
		t.Fatalf("ParseAllowedSignersFile: %v", err)
	}

	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")

	// Append one signed op through a Store with no trust store configured.
	unconfigured, err := dag.Open(dir, ident, withVocabularies(), dag.WithSigner(signer))
	if err != nil {
		t.Fatalf("Open (unconfigured) failed: %v", err)
	}
	env := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Signed"}`),
	}
	if _, err := unconfigured.Append(context.Background(), env, nil); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	resUnconfigured, err := unconfigured.Enumerate()
	if err != nil {
		t.Fatalf("Enumerate (unconfigured) failed: %v", err)
	}
	if len(resUnconfigured.Rejections) != 0 {
		t.Fatalf("Rejections (unconfigured) = %v, want none", resUnconfigured.Rejections)
	}
	ops := resUnconfigured.Ops["w-1"]
	if len(ops) != 1 {
		t.Fatalf("w-1 ops (unconfigured) = %d, want 1", len(ops))
	}
	if ops[0].Verification.Outcome != codec.OutcomeWrongKey {
		t.Errorf("Verification.Outcome (unconfigured trust store) = %q, want %q", ops[0].Verification.Outcome, codec.OutcomeWrongKey)
	}

	// A second Store, same repo, opened with no trust store of its own:
	// this call supplies one per call via WithLiveTrustStore instead (the
	// only way a trust store reaches EnumerateSince — WRIT-251 round 2
	// deleted the Open-time WithTrustStore option), and the same commit
	// now verifies as valid, with Rejections still empty.
	configured, err := dag.Open(dir, ident, withVocabularies(), dag.WithSigner(signer))
	if err != nil {
		t.Fatalf("Open (configured) failed: %v", err)
	}
	resConfigured, err := configured.Enumerate(dag.WithLiveTrustStore(ts))
	if err != nil {
		t.Fatalf("Enumerate (configured) failed: %v", err)
	}
	if len(resConfigured.Rejections) != len(resUnconfigured.Rejections) {
		t.Fatalf("Rejections changed with a trust store configured: %v vs %v", resConfigured.Rejections, resUnconfigured.Rejections)
	}
	configuredOps := resConfigured.Ops["w-1"]
	if len(configuredOps) != 1 {
		t.Fatalf("w-1 ops (configured) = %d, want 1", len(configuredOps))
	}
	if configuredOps[0].Verification.Outcome != codec.OutcomeValid {
		t.Errorf("Verification.Outcome (configured trust store) = %q, want %q", configuredOps[0].Verification.Outcome, codec.OutcomeValid)
	}
	if configuredOps[0].ID != ops[0].ID {
		t.Fatalf("the two enumerations disagree on the op id: %s vs %s", configuredOps[0].ID, ops[0].ID)
	}
}
