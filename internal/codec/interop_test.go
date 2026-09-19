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

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/codec/sshsig"
	"github.com/writtendev/writ/internal/identity"
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

// TestInterop_PrincipalCaseMismatch pins WRIT-278: sshsig principal matching
// must be case-sensitive, like OpenSSH's match_pattern_list(x, list, 0)
// (dolower=0), not the case-folded comparison writ used before this fix.
//
// The oracle here is deliberately ssh-keygen -Y verify, not git verify-commit.
// git derives the checked principal from the signing key itself via
// ssh-keygen -Y find-principals and never compares it against the commit
// author's email at all, so git verify-commit accepts this commit either
// way and would make this test pass vacuously in both directions, proving
// nothing about the principal comparison. ssh-keygen -Y verify -I <email> -n
// git is the actual per-principal check codec.Verify models (spec/signing.md
// "Principal": the commit author's email against the trust store), so it is
// the only oracle that can tell a correct case-sensitive comparison apart
// from the bug this ticket fixes.
func TestInterop_PrincipalCaseMismatch(t *testing.T) {
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

	// Trust store contains only the lowercase principal, mirroring an
	// allowed_signers line an operator actually writes.
	lowerPrincipal := "alice@example.com"
	allowedSignersPath := filepath.Join(tmp, "allowed_signers")
	if err := os.WriteFile(allowedSignersPath, []byte(lowerPrincipal+" "+pubLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ts, err := sshsig.ParseAllowedSigners(strings.NewReader(lowerPrincipal + " " + pubLine + "\n"))
	if err != nil {
		t.Fatalf("ParseAllowedSigners: %v", err)
	}

	signer, err := codec.NewSigner(identity.SigningKey{
		Format: "ssh",
		Value:  privPath,
	})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	fixedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	env := codec.Envelope{
		ObjectID:   "w-01",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}

	// buildAndSign builds and signs a commit authored under authorEmail,
	// writes its payload and signature to temp files, and returns their
	// paths alongside the built commit.
	buildAndSign := func(t *testing.T, authorEmail string) (commit *codec.Commit, payloadPath, sigPath string) {
		t.Helper()
		author := codec.Identity{
			Name:  "Alice Example",
			Email: authorEmail,
			When:  fixedTime,
		}
		c, err := codec.BuildCommit(env, author, nil, widgetVocabulary())
		if err != nil {
			t.Fatalf("BuildCommit: %v", err)
		}
		if err := codec.SignCommit(context.Background(), signer, c); err != nil {
			t.Fatalf("SignCommit: %v", err)
		}

		payloadPath = filepath.Join(t.TempDir(), "payload")
		if err := os.WriteFile(payloadPath, c.Payload, 0o600); err != nil {
			t.Fatal(err)
		}
		sigPath = filepath.Join(t.TempDir(), "sig")
		if err := os.WriteFile(sigPath, []byte(c.Signature), 0o600); err != nil {
			t.Fatal(err)
		}
		return c, payloadPath, sigPath
	}

	sshKeygenVerify := func(t *testing.T, identityStr, payloadPath, sigPath string) bool {
		t.Helper()
		cmd := exec.Command("ssh-keygen", "-Y", "verify",
			"-f", allowedSignersPath,
			"-I", identityStr,
			"-n", "git",
			"-s", sigPath,
		)
		f, err := os.Open(payloadPath)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		cmd.Stdin = f
		err = cmd.Run()
		return err == nil
	}

	// Mismatched case: author email differs from the trust store's
	// principal only in case. OpenSSH rejects it; codec.Verify must too.
	mixedCasePrincipal := "Alice@Example.COM"
	mixedCommit, mixedPayload, mixedSig := buildAndSign(t, mixedCasePrincipal)

	if sshKeygenVerify(t, mixedCasePrincipal, mixedPayload, mixedSig) {
		t.Fatalf("ssh-keygen -Y verify unexpectedly accepted principal %q against a %q allowed_signers line", mixedCasePrincipal, lowerPrincipal)
	}
	verMixed := codec.Verify(*mixedCommit, ts)
	if verMixed.Outcome != codec.OutcomeWrongKey {
		t.Fatalf("codec.Verify(%q) = %+v, want outcome %q", mixedCasePrincipal, verMixed, codec.OutcomeWrongKey)
	}

	// Control: exact-case principal. Both ssh-keygen and codec.Verify
	// must accept it, confirming the mismatch above is about case and
	// nothing else (key, namespace, payload all held constant).
	exactCommit, exactPayload, exactSig := buildAndSign(t, lowerPrincipal)

	if !sshKeygenVerify(t, lowerPrincipal, exactPayload, exactSig) {
		t.Fatalf("ssh-keygen -Y verify unexpectedly rejected exact-case principal %q", lowerPrincipal)
	}
	verExact := codec.Verify(*exactCommit, ts)
	if verExact.Outcome != codec.OutcomeValid || !verExact.Valid {
		t.Fatalf("codec.Verify(%q) = %+v, want outcome %q", lowerPrincipal, verExact, codec.OutcomeValid)
	}
}

// TestInterop_MatchPatternSemantics extends TestInterop_PrincipalCaseMismatch's
// ssh-keygen oracle to every WRIT-302 divergence between writ's pre-fix
// path.Match-based matcher and OpenSSH's actual match_pattern /
// match_pattern_list: '*' crossing a '/', a bracket class parsed as a
// character class, a backslash parsed as an escape, an unbalanced '['
// silently treated as no-match, namespaces= gaining globbing and negation,
// and '?' consuming a rune instead of a byte.
//
// The oracle is ssh-keygen -Y verify, never git verify-commit, for the same
// reason TestInterop_PrincipalCaseMismatch's doc comment gives: git derives
// the checked principal from the signing key itself and never compares it
// against the commit author's email, so it would pass vacuously regardless
// of which matcher writ uses. Namespace cases (5, 6) work here too, since
// -n git is fixed by spec/signing.md and only the namespaces= pattern in
// the allowed_signers line varies.
func TestInterop_MatchPatternSemantics(t *testing.T) {
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

	signer, err := codec.NewSigner(identity.SigningKey{
		Format: "ssh",
		Value:  privPath,
	})
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}

	fixedTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	env := codec.Envelope{
		ObjectID:   "w-01",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}

	cases := []struct {
		name        string
		principals  string // the allowed_signers line's principal field
		options     string // e.g. `namespaces="g*"`; empty when unused
		authorEmail string // author.email, and the -I identity ssh-keygen checks
		wantValid   bool
		wantOutcome codec.VerificationOutcome
	}{
		{
			name:        "wildcard crosses slash",
			principals:  "*@example.test",
			authorEmail: "alice/laptop@example.test",
			wantValid:   true,
			wantOutcome: codec.OutcomeValid,
		},
		{
			name:        "bracket class is literal",
			principals:  "ali[cd]e@*.test",
			authorEmail: "alice@example.test",
			wantValid:   false,
			wantOutcome: codec.OutcomeWrongKey,
		},
		{
			name:        "backslash is literal",
			principals:  `alice\@*.test`,
			authorEmail: "alice@example.test",
			wantValid:   false,
			wantOutcome: codec.OutcomeWrongKey,
		},
		{
			name:        "unbalanced bracket is not a parse error",
			principals:  "ali[ce*@example.test",
			authorEmail: "ali[cex@example.test",
			wantValid:   true,
			wantOutcome: codec.OutcomeValid,
		},
		{
			name:        "namespaces= globs",
			principals:  "alice@example.test",
			options:     `namespaces="g*"`,
			authorEmail: "alice@example.test",
			wantValid:   true,
			wantOutcome: codec.OutcomeValid,
		},
		{
			name:        "namespaces= negation",
			principals:  "alice@example.test",
			options:     `namespaces="!git,*"`,
			authorEmail: "alice@example.test",
			wantValid:   false,
			wantOutcome: codec.OutcomeWrongKey,
		},
		{
			name:        "byte-wise not rune-wise",
			principals:  "alic?@example.test",
			authorEmail: "alicé@example.test",
			wantValid:   false,
			wantOutcome: codec.OutcomeWrongKey,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := tc.principals
			if tc.options != "" {
				line += " " + tc.options
			}
			line += " " + pubLine + "\n"

			allowedSignersPath := filepath.Join(t.TempDir(), "allowed_signers")
			if err := os.WriteFile(allowedSignersPath, []byte(line), 0o600); err != nil {
				t.Fatal(err)
			}
			ts, err := sshsig.ParseAllowedSigners(strings.NewReader(line))
			if err != nil {
				t.Fatalf("ParseAllowedSigners: %v", err)
			}

			author := codec.Identity{
				Name:  "Alice Example",
				Email: tc.authorEmail,
				When:  fixedTime,
			}
			c, err := codec.BuildCommit(env, author, nil, widgetVocabulary())
			if err != nil {
				t.Fatalf("BuildCommit: %v", err)
			}
			if err := codec.SignCommit(context.Background(), signer, c); err != nil {
				t.Fatalf("SignCommit: %v", err)
			}

			payloadPath := filepath.Join(t.TempDir(), "payload")
			if err := os.WriteFile(payloadPath, c.Payload, 0o600); err != nil {
				t.Fatal(err)
			}
			sigPath := filepath.Join(t.TempDir(), "sig")
			if err := os.WriteFile(sigPath, []byte(c.Signature), 0o600); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command("ssh-keygen", "-Y", "verify",
				"-f", allowedSignersPath,
				"-I", tc.authorEmail,
				"-n", "git",
				"-s", sigPath,
			)
			f, err := os.Open(payloadPath)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			cmd.Stdin = f
			gotValid := cmd.Run() == nil
			if gotValid != tc.wantValid {
				t.Fatalf("ssh-keygen -Y verify(%q) = %v, want %v", tc.authorEmail, gotValid, tc.wantValid)
			}

			ver := codec.Verify(*c, ts)
			if ver.Outcome != tc.wantOutcome {
				t.Fatalf("codec.Verify(%q) = %+v, want outcome %q", tc.authorEmail, ver, tc.wantOutcome)
			}
		})
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
