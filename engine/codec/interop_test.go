package codec_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/sshsig"
	"github.com/writtendev/writ/engine/identity"
)

// TestInterop_EngineToSystemGit builds and signs an op commit through the engine,
// writes it into a git repository via go-git, and verifies it with system git verify-commit.
func TestInterop_EngineToSystemGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not found on PATH")
	}

	tmp := t.TempDir()
	keyDir := filepath.Join(tmp, "keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	privPath := filepath.Join(keyDir, "id_signer")
	pubPath := privPath + ".pub"

	genCmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", privPath)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen unavailable: %v\n%s", err, out)
	}

	pubBytes, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	pubLine := strings.TrimSpace(string(pubBytes))

	repoDir := filepath.Join(tmp, "repo")
	repo, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}

	principal := "alice@example.com"
	allowedSignersPath := filepath.Join(tmp, "allowed_signers")
	if err := os.WriteFile(allowedSignersPath, []byte(principal+" "+pubLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Configure git repo for verify-commit
	runGit(t, repoDir, "config", "gpg.format", "ssh")
	runGit(t, repoDir, "config", "gpg.ssh.allowedSignersFile", allowedSignersPath)

	signer, err := codec.NewSigner(identity.SigningKey{
		Format: "ssh",
		Value:  privPath,
	})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	fixedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	author := codec.Identity{
		Name:  "Alice Example",
		Email: principal,
		When:  fixedTime,
	}
	env := codec.Envelope{
		ObjectID:   "w-01",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}

	commit, err := codec.BuildCommit(env, author, nil, widgetVocabulary())
	if err != nil {
		t.Fatalf("BuildCommit: %v", err)
	}

	if err := codec.SignCommit(context.Background(), signer, commit); err != nil {
		t.Fatalf("SignCommit: %v", err)
	}

	// Write commit object into go-git repo
	gitCommit, err := codec.ToGitCommit(*commit)
	if err != nil {
		t.Fatalf("ToGitCommit: %v", err)
	}

	// Write blob and tree to repo storer
	blobObj := repo.Storer.NewEncodedObject()
	blobObj.SetType(plumbing.BlobObject)
	blobW, err := blobObj.Writer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blobW.Write(commit.Tree[0].Data); err != nil {
		t.Fatal(err)
	}
	_ = blobW.Close()
	blobHash, err := repo.Storer.SetEncodedObject(blobObj)
	if err != nil {
		t.Fatal(err)
	}

	tree := &object.Tree{
		Entries: []object.TreeEntry{
			{
				Name: "op.json",
				Mode: filemode.Regular,
				Hash: blobHash,
			},
		},
	}
	treeObj := repo.Storer.NewEncodedObject()
	treeObj.SetType(plumbing.TreeObject)
	if err := tree.Encode(treeObj); err != nil {
		t.Fatalf("encode tree: %v", err)
	}
	if _, err := repo.Storer.SetEncodedObject(treeObj); err != nil {
		t.Fatalf("store tree: %v", err)
	}
	// Build tree in repo
	cObj := repo.Storer.NewEncodedObject()
	cObj.SetType(plumbing.CommitObject)
	if err := gitCommit.Encode(cObj); err != nil {
		t.Fatalf("encode gitCommit: %v", err)
	}
	commitHash, err := repo.Storer.SetEncodedObject(cObj)
	if err != nil {
		t.Fatalf("store commit: %v", err)
	}

	// Move branch ref to commit
	branch := plumbing.NewBranchReferenceName("main")
	if err := repo.Storer.SetReference(plumbing.NewHashReference(branch, commitHash)); err != nil {
		t.Fatalf("set branch ref: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, branch)); err != nil {
		t.Fatalf("set HEAD: %v", err)
	}

	// Run system git verify-commit
	cmd := exec.Command("git", "verify-commit", commitHash.String())
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git verify-commit failed: %v\n%s", err, out)
	}
}

// TestInterop_SystemGitToEngine creates a commit using system git commit -S and verifies it in pure Go.
func TestInterop_SystemGitToEngine(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not found on PATH")
	}

	tmp := t.TempDir()
	keyDir := filepath.Join(tmp, "keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	privPath := filepath.Join(keyDir, "id_signer")
	pubPath := privPath + ".pub"

	genCmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", privPath)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen unavailable: %v\n%s", err, out)
	}

	pubBytes, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	pubLine := strings.TrimSpace(string(pubBytes))

	repoDir := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "init", "-q")
	runGit(t, repoDir, "config", "user.name", "Alice Example")
	runGit(t, repoDir, "config", "user.email", "alice@example.com")
	runGit(t, repoDir, "config", "gpg.format", "ssh")
	runGit(t, repoDir, "config", "user.signingkey", pubPath)

	if err := os.WriteFile(filepath.Join(repoDir, "op.json"), []byte(`{"body":{},"object_id":"w-01","object_type":"widget","op_type":"create","op_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "op.json")
	runGit(t, repoDir, "commit", "-q", "-S", "-m", "writ: create widget/w-01\n")

	headOut := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "HEAD"))
	commitHash := plumbing.NewHash(headOut)

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	gitCommit, err := repo.CommitObject(commitHash)
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}

	c, err := codec.FromGitCommit(repo.Storer, gitCommit)
	if err != nil {
		t.Fatalf("FromGitCommit: %v", err)
	}

	ts, err := sshsig.ParseAllowedSigners(strings.NewReader("alice@example.com " + pubLine + "\n"))
	if err != nil {
		t.Fatalf("ParseAllowedSigners: %v", err)
	}

	ver := codec.Verify(c, ts)
	if !ver.Valid || ver.Outcome != codec.OutcomeValid {
		t.Fatalf("codec.Verify expected valid, got %+v", ver)
	}
}

// TestDeterminism ensures that signing the same op commit twice with the same ed25519 key
// produces the exact same commit SHA and payload.
func TestDeterminism(t *testing.T) {
	tmp := t.TempDir()
	privPath := filepath.Join(tmp, "id_ed25519")

	genCmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", privPath)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen unavailable: %v\n%s", err, out)
	}

	signer, err := codec.NewSigner(identity.SigningKey{
		Format: "ssh",
		Value:  privPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	fixedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	author := codec.Identity{
		Name:  "Alice Example",
		Email: "alice@example.com",
		When:  fixedTime,
	}
	env := codec.Envelope{
		ObjectID:   "w-01",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}

	commit1, err := codec.BuildCommit(env, author, nil, widgetVocabulary())
	if err != nil {
		t.Fatal(err)
	}
	if err := codec.SignCommit(context.Background(), signer, commit1); err != nil {
		t.Fatal(err)
	}

	commit2, err := codec.BuildCommit(env, author, nil, widgetVocabulary())
	if err != nil {
		t.Fatal(err)
	}
	if err := codec.SignCommit(context.Background(), signer, commit2); err != nil {
		t.Fatal(err)
	}

	if commit1.ID != commit2.ID {
		t.Fatalf("commit IDs differed: %s != %s", commit1.ID, commit2.ID)
	}
	if commit1.Signature != commit2.Signature {
		t.Fatalf("commit signatures differed: %s != %s", commit1.Signature, commit2.Signature)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
