package resolve_test

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/writtendev/writ/internal/resolve"
	"github.com/writtendev/writ/spec/fixtures"
)

func requireSSHKeygen(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not on PATH; fixture generation needs it to sign commits")
	}
}

func materializeTree(repo *git.Repository, commitHash string) (map[string][]byte, error) {
	commit, err := repo.CommitObject(plumbing.NewHash(commitHash))
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}

	files := make(map[string][]byte)
	err = tree.Files().ForEach(func(f *object.File) error {
		contents, err := f.Contents()
		if err != nil {
			return err
		}
		files[f.Name] = []byte(contents)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func TestForcePushedBranchHistoryResolution(t *testing.T) {
	requireSSHKeygen(t)

	descs, err := fixtures.LoadCorpus()
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}

	var desc *fixtures.Description
	for _, d := range descs {
		if d.Name == "force-pushed-branch" {
			desc = d
			break
		}
	}
	if desc == nil {
		t.Fatal("force-pushed-branch description not found in corpus")
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	manifest, err := fixtures.Generate(desc, repoDir)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var mainTip, gen0Commit string
	for _, r := range manifest.Refs {
		switch r.Name {
		case "refs/heads/main":
			mainTip = r.Commit
		case "refs/fixture-history/main/gen-0":
			gen0Commit = r.Commit
		}
	}
	if mainTip == "" || gen0Commit == "" {
		t.Fatalf("missing refs in manifest: %+v", manifest.Refs)
	}

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}

	gen0Files, err := materializeTree(repo, gen0Commit)
	if err != nil {
		t.Fatalf("materializeTree(gen0): %v", err)
	}

	mainFiles, err := materializeTree(repo, mainTip)
	if err != nil {
		t.Fatalf("materializeTree(main): %v", err)
	}

	treeGen0 := resolve.NewTree(gen0Files, resolve.SHA1)
	treeMain := resolve.NewTree(mainFiles, resolve.SHA1)

	gen0File, ok := gen0Files["state.json"]
	if !ok {
		t.Fatal("state.json not found in gen-0 tree")
	}
	gen0Blob := treeGen0.File("state.json")
	var gen0BlobOID string
	if gen0Blob != nil {
		gen0BlobOID = gen0Blob.Blob
	}

	anchor := resolve.Anchor{
		Version: 1,
		New: &resolve.SideAnchor{
			Commit: gen0Commit,
			Path:   "state.json",
			Blob:   gen0BlobOID,
			Range:  &resolve.Range{Start: 1, End: 1},
			Context: &resolve.Context{
				Before: []string{},
				Lines:  []string{string(gen0File)},
				After:  []string{},
			},
		},
	}

	// 1. Resolve against gen-0 tree -> resolves exact-path-blob
	resGen0 := resolve.Resolve(anchor, treeGen0)
	if resGen0.New == nil || resGen0.New.Outcome != "resolved" {
		t.Fatalf("expected resolved in gen-0 tree, got: %+v", resGen0.New)
	}
	if resGen0.New.Match != "exact-path-blob" || resGen0.New.Path != "state.json" {
		t.Errorf("unexpected match/path: match=%q path=%q", resGen0.New.Match, resGen0.New.Path)
	}
	if resGen0.New.Range == nil || resGen0.New.Range.Start != 1 || resGen0.New.Range.End != 1 {
		t.Errorf("unexpected range: %+v", resGen0.New.Range)
	}

	// 2. Resolve against rewritten main tree -> orphans, but original anchor is preserved
	resMain := resolve.Resolve(anchor, treeMain)
	if resMain.New == nil || resMain.New.Outcome != "orphaned" {
		t.Fatalf("expected orphaned in rewritten main tree, got: %+v", resMain.New)
	}
	if resMain.New.Reason != "no-candidate" && resMain.New.Reason != "below-threshold" {
		t.Errorf("unexpected orphan reason: %q", resMain.New.Reason)
	}
	if resMain.Anchor.New == nil || resMain.Anchor.New.Blob != gen0BlobOID {
		t.Errorf("anchor not preserved in orphan outcome: %+v", resMain.Anchor)
	}
}
