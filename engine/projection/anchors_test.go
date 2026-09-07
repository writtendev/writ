package projection_test

import (
	"bytes"
	"context"
	"encoding/json"
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

func makeCommentEnv(objID string, subjectObjID string, anchor *resolve.Anchor, text string) codec.Envelope {
	body := map[string]any{
		"subject": map[string]any{
			"object_type": "review",
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
		ObjectType: "comment",
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

	// 2. Open DAG store and append comment
	store, err := dag.OpenRepo(repo, identity.Identity{
		WriterID: identity.WriterID("0123456789abcdef"),
		Author: identity.Author{
			Name:  "Commenter",
			Email: "commenter@example.com",
		},
	})
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

	commentEnv := makeCommentEnv("comm-1", "rev-1", anchor, "Nice function!")
	_, err = store.Append(ctx, commentEnv, nil)
	if err != nil {
		t.Fatalf("store.Append comment: %v", err)
	}

	// 3. Refresh projection
	stats1, err := db.Refresh(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Refresh 1 failed: %v", err)
	}
	if stats1.AnchorsResolved != 1 {
		t.Fatalf("expected 1 anchor resolved, got %d", stats1.AnchorsResolved)
	}

	// Assert comments table has verbatim anchor JSON and no resolution state
	var storedAnchor string
	err = db.DB().QueryRow("SELECT f_anchor FROM o_comment WHERE object_id = 'comm-1'").Scan(&storedAnchor)
	if err != nil {
		t.Fatalf("query comments anchor: %v", err)
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
		startLine, endLine                           int
	)
	err = db.DB().QueryRow(`
		SELECT target_commit, side, outcome, match, path, start_line, end_line, reason
		FROM anchor_resolutions
		WHERE object_id = 'comm-1'
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
	stats2, err := db.Refresh(store, projection.WithSchema(testRules()))
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
		WHERE object_id = 'comm-1'
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
