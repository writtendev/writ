package projection_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/engine/resolve"
	"github.com/writtendev/writ/engine/state"
)

func createCommitWithFiles(t *testing.T, repo *git.Repository, parents []plumbing.Hash, files map[string]string, message string) plumbing.Hash {
	t.Helper()
	var entries []codec.TreeEntry
	for path, content := range files {
		entries = append(entries, codec.TreeEntry{
			Name: path,
			Mode: filemode.Regular.String(),
			Data: []byte(content),
		})
	}

	c := &codec.Commit{
		Parents: make([]string, len(parents)),
		Author: codec.Identity{
			Name:  "Author",
			Email: "author@example.com",
			When:  time.Unix(1700000000, 0).UTC(),
		},
		Committer: codec.Identity{
			Name:  "Author",
			Email: "author@example.com",
			When:  time.Unix(1700000000, 0).UTC(),
		},
		Message: message,
		Tree:    entries,
	}
	for i, p := range parents {
		c.Parents[i] = p.String()
	}

	hash, err := codec.WriteCommit(context.Background(), repo.Storer, c, nil)
	if err != nil {
		t.Fatalf("createCommitWithFiles: %v", err)
	}
	return hash
}

// noteRules declares "note": a type carrying an anchor-valued target.
// anchor_resolutions is driven off every scalar target whose declared
// value_type is "anchor" (materializeAnchors), and testRules() declares
// none, so this test brings its own vocabulary — the same shape a consumer
// declares in the log.
func noteRules() map[string][]state.Rule {
	return map[string][]state.Rule{
		"note": {
			{OpType: "create", OpVersion: 1, Field: "text", Strategy: "lww", ValueType: "text", ObjectType: "note"},
			{OpType: "create", OpVersion: 1, Field: "subject", Strategy: "create-once", ObjectType: "note"},
			{OpType: "create", OpVersion: 1, Field: "anchor", Strategy: "create-once", ValueType: "anchor", ObjectType: "note"},
		},
	}
}

func makeNoteEnv(objID string, subjectObjID string, anchor *resolve.Anchor, text string) codec.Envelope {
	body := map[string]any{
		"subject": map[string]any{
			"object_type": "widget",
			"object_id":   subjectObjID,
		},
		"text": text,
	}
	if anchor != nil {
		body["anchor"] = anchor
	}

	bodyRaw, _ := json.Marshal(body)
	env := codec.Envelope{
		ObjectID:   objID,
		ObjectType: "note",
		OpType:     "create",
		OpVersion:  1,
		Body:       bodyRaw,
	}
	raw, _ := codec.EncodePayload(env)
	env.Raw = raw
	return env
}

func TestAnchorResolutionAndCodeRefMove(t *testing.T) {
	ctx := context.Background()
	repo, err := git.Init(memory.NewStorage(), nil)
	if err != nil {
		t.Fatalf("git.Init: %v", err)
	}

	// 1. Create commit 1 on refs/heads/main
	fileV1 := strings.Join([]string{
		"package main",
		"func Hello() string {",
		"    return \"Hello World\"",
		"}",
	}, "\n")
	c1Hash := createCommitWithFiles(t, repo, nil, map[string]string{"main.go": fileV1}, "initial code")
	mainRef := plumbing.ReferenceName("refs/heads/main")
	_ = repo.Storer.SetReference(plumbing.NewReferenceFromStrings(mainRef.String(), c1Hash.String()))
	headRef := plumbing.ReferenceName("HEAD")
	_ = repo.Storer.SetReference(plumbing.NewSymbolicReference(headRef, mainRef))

	// 2. Open DAG store and append the note
	store, err := dag.OpenRepo(repo, identity.Identity{
		WriterID: identity.WriterID("0123456789abcdef"),
		Author: identity.Author{
			Name:  "Note Writer",
			Email: "note-writer@example.com",
		},
	}, withVocabularies(noteRules()))
	if err != nil {
		t.Fatalf("dag.OpenRepo: %v", err)
	}

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open projection failed: %v", err)
	}
	defer db.Close()

	// Capture anchor for lines [2, 3] in commit 1
	treeFiles1 := map[string][]byte{"main.go": []byte(fileV1)}
	tree1 := resolve.NewTree(treeFiles1, resolve.SHA1)
	blob1, _ := tree1.Blob("main.go")

	anchor := &resolve.Anchor{
		Version: 1,
		New: &resolve.SideAnchor{
			Commit: c1Hash.String(),
			Path:   "main.go",
			Blob:   blob1,
			Range:  &resolve.Range{Start: 2, End: 3},
			Context: &resolve.Context{
				Before: []string{"package main"},
				Lines:  []string{"func Hello() string {", "    return \"Hello World\""},
				After:  []string{"}"},
			},
		},
	}

	anchorJSON, err := json.Marshal(anchor)
	if err != nil {
		t.Fatalf("marshal anchor: %v", err)
	}

	noteEnv := makeNoteEnv("n-1", "w-1", anchor, "Nice function!")
	_, err = store.Append(ctx, noteEnv, nil)
	if err != nil {
		t.Fatalf("store.Append note: %v", err)
	}

	// 3. Refresh projection
	stats1, err := db.Refresh(store, projection.WithSchema(noteRules()))
	if err != nil {
		t.Fatalf("Refresh 1 failed: %v", err)
	}
	if stats1.AnchorsResolved != 1 {
		t.Fatalf("expected 1 anchor resolved, got %d", stats1.AnchorsResolved)
	}

	// Assert the note table has verbatim anchor JSON and no resolution state
	var storedAnchor string
	err = db.DB().QueryRow("SELECT f_anchor FROM o_note WHERE object_id = 'n-1'").Scan(&storedAnchor)
	if err != nil {
		t.Fatalf("query note anchor: %v", err)
	}

	canonStored, err := canonicaljson.Marshal([]byte(storedAnchor))
	if err != nil {
		t.Fatalf("canonicalize stored anchor: %v", err)
	}
	canonExpected, err := canonicaljson.Marshal(anchorJSON)
	if err != nil {
		t.Fatalf("canonicalize expected anchor: %v", err)
	}
	if !bytes.Equal(canonStored, canonExpected) {
		t.Fatalf("stored anchor != expected anchor:\ngot:  %s\nwant: %s", string(canonStored), string(canonExpected))
	}

	// Assert anchor_resolutions table matches resolve.Resolve(anchor, tree1)
	expectedRes1 := resolve.Resolve(*anchor, tree1)
	var (
		resCommit, side, outcome, match, path, reason string
		startLine, endLine                            int
	)
	err = db.DB().QueryRow(`
		SELECT target_commit, side, outcome, match, path, start_line, end_line, reason
		FROM anchor_resolutions
		WHERE object_id = 'n-1'
	`).Scan(&resCommit, &side, &outcome, &match, &path, &startLine, &endLine, &reason)
	if err != nil {
		t.Fatalf("query anchor_resolutions: %v", err)
	}

	if resCommit != c1Hash.String() || side != "new" || outcome != expectedRes1.New.Outcome ||
		match != expectedRes1.New.Match || path != expectedRes1.New.Path ||
		startLine != expectedRes1.New.Range.Start || endLine != expectedRes1.New.Range.End {
		t.Fatalf("anchor resolution mismatch: got commit=%s side=%s outcome=%s match=%s path=%s [%d,%d]",
			resCommit, side, outcome, match, path, startLine, endLine)
	}

	// 4. Move code ref refs/heads/main to commit 2 (with line insertion at top)
	fileV2 := strings.Join([]string{
		"// Header comment inserted",
		"package main",
		"func Hello() string {",
		"    return \"Hello World\"",
		"}",
	}, "\n")
	c2Hash := createCommitWithFiles(t, repo, []plumbing.Hash{c1Hash}, map[string]string{"main.go": fileV2}, "insert header comment")
	_ = repo.Storer.SetReference(plumbing.NewReferenceFromStrings(mainRef.String(), c2Hash.String()))

	// Refresh without any new ops: code ref moved, should re-resolve against commit 2
	stats2, err := db.Refresh(store, projection.WithSchema(noteRules()))
	if err != nil {
		t.Fatalf("Refresh 2 failed: %v", err)
	}
	if stats2.ObjectsTouched != 0 {
		t.Fatalf("expected 0 objects touched when only code ref moved, got %d", stats2.ObjectsTouched)
	}
	if stats2.AnchorsResolved != 1 {
		t.Fatalf("expected 1 anchor re-resolved for new target commit, got %d", stats2.AnchorsResolved)
	}

	// Assert old resolutions for commit 1 are pruned and new resolutions for commit 2 exist
	var countOld int
	_ = db.DB().QueryRow("SELECT COUNT(*) FROM anchor_resolutions WHERE target_commit = ?", c1Hash.String()).Scan(&countOld)
	if countOld != 0 {
		t.Fatalf("expected 0 resolutions for pruned target commit 1, got %d", countOld)
	}

	treeFiles2 := map[string][]byte{"main.go": []byte(fileV2)}
	tree2 := resolve.NewTree(treeFiles2, resolve.SHA1)
	expectedRes2 := resolve.Resolve(*anchor, tree2)

	err = db.DB().QueryRow(`
		SELECT target_commit, side, outcome, match, path, start_line, end_line, reason
		FROM anchor_resolutions
		WHERE object_id = 'n-1'
	`).Scan(&resCommit, &side, &outcome, &match, &path, &startLine, &endLine, &reason)
	if err != nil {
		t.Fatalf("query anchor_resolutions after code move: %v", err)
	}

	if resCommit != c2Hash.String() || side != "new" || outcome != expectedRes2.New.Outcome ||
		match != expectedRes2.New.Match || path != expectedRes2.New.Path ||
		startLine != expectedRes2.New.Range.Start || endLine != expectedRes2.New.Range.End {
		t.Fatalf("anchor resolution mismatch on commit 2: got commit=%s side=%s outcome=%s match=%s path=%s [%d,%d]",
			resCommit, side, outcome, match, path, startLine, endLine)
	}
	if startLine != 3 || endLine != 4 {
		t.Fatalf("expected re-anchored range [3, 4], got [%d, %d]", startLine, endLine)
	}
}

// writeForeignCommit writes a real op commit directly via codec.WriteCommit
// and advances refName to it, without going through dag.Store.Append or
// codec.BuildCommit: both call validateProducerOp, and since WRIT-252's fix
// makes value.Validate itself reject a malformed anchor, this client can no
// longer produce one that way. A peer running an older or buggy client is
// not bound by this client's producer validation — it pushes real commits
// under its own chain ref — so this is what actually lands on the log for
// Chains/EnumerateSince (and therefore both Refresh and Rebuild) to
// discover exactly as if a real peer had pushed it, rather than a synthetic
// enumeration result.
func writeForeignCommit(t *testing.T, repo *git.Repository, refName plumbing.ReferenceName, parent plumbing.Hash, env codec.Envelope, when time.Time) plumbing.Hash {
	t.Helper()
	raw, err := codec.EncodePayload(env)
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}
	author := codec.Identity{Name: "Peer Writer", Email: "peer@example.com", When: when}
	var parents []string
	if !parent.IsZero() {
		parents = []string{parent.String()}
	}
	commit := &codec.Commit{
		Parents:   parents,
		Author:    author,
		Committer: author,
		Message:   codec.Message(env),
		Tree: []codec.TreeEntry{
			{Name: "op.json", Mode: "100644", Data: raw},
		},
	}
	hash, err := codec.WriteCommit(context.Background(), repo.Storer, commit, nil)
	if err != nil {
		t.Fatalf("WriteCommit: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, hash)); err != nil {
		t.Fatalf("SetReference %s: %v", refName, err)
	}
	return hash
}

// TestAnchorResolutionMalformedAnchorsDoNotBrickRefresh is WRIT-252's own
// regression test: a peer-pushed anchor that fails to decode, or whose
// range/context arithmetic is inconsistent, must resolve to a "malformed"
// orphan rather than panicking the resolver ladder or aborting the refresh
// transaction. Both ticket repros are exercised: a short-range elided
// context (the panic) and version encoded as a JSON string (the transaction
// abort). Both ops are written as real commits under a foreign peer's chain
// ref via writeForeignCommit, bypassing dag.Store.Append's producer
// validation — the only way to get a malformed anchor onto the log now that
// value.Validate rejects it at write time (engine/internal/value's own
// WRIT-252 change), exactly as a peer running an older or buggy client
// would.
func TestAnchorResolutionMalformedAnchorsDoNotBrickRefresh(t *testing.T) {
	repo, err := git.Init(memory.NewStorage(), nil)
	if err != nil {
		t.Fatalf("git.Init: %v", err)
	}

	fileV1 := "package main\n"
	c1Hash := createCommitWithFiles(t, repo, nil, map[string]string{"main.go": fileV1}, "initial code")
	mainRef := plumbing.ReferenceName("refs/heads/main")
	_ = repo.Storer.SetReference(plumbing.NewReferenceFromStrings(mainRef.String(), c1Hash.String()))
	headRef := plumbing.ReferenceName("HEAD")
	_ = repo.Storer.SetReference(plumbing.NewSymbolicReference(headRef, mainRef))

	store, err := dag.OpenRepo(repo, identity.Identity{
		WriterID: identity.WriterID("0123456789abcdef"),
		Author: identity.Author{
			Name:  "Note Writer",
			Email: "note-writer@example.com",
		},
	}, withVocabularies(noteRules()))
	if err != nil {
		t.Fatalf("dag.OpenRepo: %v", err)
	}

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open projection failed: %v", err)
	}
	defer db.Close()

	base := time.Unix(1700000000, 0).UTC()

	// Ticket repro 1: elided context (omitted > 0) on a range shorter than
	// 64 lines. This is the exact shape that indexed a negative offset into
	// the resolver ladder before WRIT-252 (ladder.go's Rung 3/4 loops).
	ctxLines64 := make([]any, 64)
	for i := range ctxLines64 {
		ctxLines64[i] = fmt.Sprintf("ctx%d", i)
	}
	shortRangeElidedAnchor := map[string]any{
		"version": 1,
		"new": map[string]any{
			"commit": c1Hash.String(),
			"path":   "main.go",
			"blob":   "1111111111111111111111111111111111111111",
			"range":  map[string]any{"start": 1, "end": 1},
			"context": map[string]any{
				"before":  []any{},
				"lines":   ctxLines64,
				"omitted": 1,
				"after":   []any{},
			},
		},
	}

	// Ticket repro 2: version encoded as a JSON string rather than an
	// integer. ParseAnchor fails to decode this at all, which used to abort
	// materializeAnchors and, with it, the whole refresh transaction.
	versionStringAnchor := map[string]any{
		"version": "1",
		"new": map[string]any{
			"commit": c1Hash.String(),
			"path":   "main.go",
			"blob":   "1111111111111111111111111111111111111111",
		},
	}

	bodyFor := func(anchor map[string]any) []byte {
		body := map[string]any{
			"subject": map[string]any{
				"object_type": "widget",
				"object_id":   "w-1",
			},
			"text":   "hostile anchor",
			"anchor": anchor,
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		return raw
	}

	envFor := func(objID string, bodyRaw []byte) codec.Envelope {
		env := codec.Envelope{
			ObjectID:   objID,
			ObjectType: "note",
			OpType:     "create",
			OpVersion:  1,
			Body:       bodyRaw,
		}
		raw, err := codec.EncodePayload(env)
		if err != nil {
			t.Fatalf("EncodePayload: %v", err)
		}
		env.Raw = raw
		return env
	}

	peerRef := plumbing.ReferenceName("refs/writ/fedcba9876543210/note")
	c1 := writeForeignCommit(t, repo, peerRef, plumbing.ZeroHash, envFor("n-short-range-elided", bodyFor(shortRangeElidedAnchor)), base)
	writeForeignCommit(t, repo, peerRef, c1, envFor("n-version-string", bodyFor(versionStringAnchor)), base.Add(time.Second))

	// Refresh #1: must not panic and must not return an error (both repros
	// used to do one or the other).
	stats1, err := db.Refresh(store, projection.WithSchema(noteRules()))
	if err != nil {
		t.Fatalf("Refresh 1 failed: %v", err)
	}
	if stats1.AnchorsResolved != 2 {
		t.Fatalf("expected 2 anchors resolved, got %d", stats1.AnchorsResolved)
	}

	// Refresh #2: re-resolving on a no-op delta must also not panic or
	// error (materializeAnchors treats existing per-target-commit
	// resolutions as already-done, but this is the transaction the ticket's
	// "Refresh #1, #2 and Rebuild all fail" repro named explicitly).
	if _, err := db.Refresh(store, projection.WithSchema(noteRules())); err != nil {
		t.Fatalf("Refresh 2 failed: %v", err)
	}

	// Rebuild: same requirement, via the cold-rebuild path instead of the
	// incremental one.
	if _, err := db.Rebuild(store, projection.WithSchema(noteRules())); err != nil {
		t.Fatalf("Rebuild failed: %v", err)
	}

	// The ops table is populated: materializeAnchors failing used to roll
	// back the whole transaction, discarding the ops insert along with it.
	var opCount int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM ops").Scan(&opCount); err != nil {
		t.Fatalf("query ops count: %v", err)
	}
	if opCount != 2 {
		t.Fatalf("expected 2 ops rows, got %d", opCount)
	}

	// Each object row materialized with its other fields intact: the
	// anchor degrading does not take the rest of the object down with it.
	for _, tc := range []struct {
		objID string
		text  string
	}{
		{"n-short-range-elided", "hostile anchor"},
		{"n-version-string", "hostile anchor"},
	} {
		var gotText string
		if err := db.DB().QueryRow("SELECT f_text FROM o_note WHERE object_id = ?", tc.objID).Scan(&gotText); err != nil {
			t.Fatalf("query note %s: %v", tc.objID, err)
		}
		if gotText != tc.text {
			t.Errorf("note %s: f_text = %q, want %q", tc.objID, gotText, tc.text)
		}
	}

	// anchor_resolutions holds outcome=orphaned, reason=malformed for both.
	for _, objID := range []string{"n-short-range-elided", "n-version-string"} {
		var outcome, reason string
		err := db.DB().QueryRow(
			"SELECT outcome, reason FROM anchor_resolutions WHERE object_id = ? AND side = 'new'", objID,
		).Scan(&outcome, &reason)
		if err != nil {
			t.Fatalf("query anchor_resolutions for %s: %v", objID, err)
		}
		if outcome != "orphaned" || reason != "malformed" {
			t.Errorf("object %s: anchor_resolutions = (outcome=%s, reason=%s), want (orphaned, malformed)", objID, outcome, reason)
		}
	}
}
