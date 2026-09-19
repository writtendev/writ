package codec_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"golang.org/x/crypto/ssh"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/codec/sshsig"
	"github.com/writtendev/writ/internal/identity"
)

func TestVerify_Outcomes(t *testing.T) {
	tmp := t.TempDir()
	privPath := filepath.Join(tmp, "id_ed25519")

	genCmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", privPath)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen unavailable: %v\n%s", err, out)
	}

	pubBytes, err := os.ReadFile(privPath + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	pubLine := strings.TrimSpace(string(pubBytes))

	signer, err := codec.NewSigner(identity.SigningKey{
		Format: "ssh",
		Value:  privPath,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	author := codec.Identity{
		Name:  "Alice Example",
		Email: "alice@example.test",
		When:  now,
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
		t.Fatal(err)
	}

	tsContent := "alice@example.test " + pubLine + "\n"
	ts, err := sshsig.ParseAllowedSigners(strings.NewReader(tsContent))
	if err != nil {
		t.Fatal(err)
	}

	// 1. Unsigned
	verUnsigned := codec.Verify(*commit, ts)
	if verUnsigned.Outcome != codec.OutcomeUnsigned || verUnsigned.Valid {
		t.Errorf("expected unsigned outcome, got %+v", verUnsigned)
	}

	// Sign the commit
	if err := codec.SignCommit(context.Background(), signer, commit); err != nil {
		t.Fatal(err)
	}

	// 2. Valid
	verValid := codec.Verify(*commit, ts)
	if verValid.Outcome != codec.OutcomeValid || !verValid.Valid {
		t.Errorf("expected valid outcome, got %+v", verValid)
	}
	if !strings.HasPrefix(verValid.KeyFingerprint, "SHA256:") {
		t.Errorf("expected SHA256:... fingerprint, got %q", verValid.KeyFingerprint)
	}

	// 3. Wrong key / unauthorized principal
	tsWrong, _ := sshsig.ParseAllowedSigners(strings.NewReader("bob@example.test " + pubLine + "\n"))
	verWrong := codec.Verify(*commit, tsWrong)
	if verWrong.Outcome != codec.OutcomeWrongKey || verWrong.Valid {
		t.Errorf("expected wrong-key outcome, got %+v", verWrong)
	}
	if verWrong.KeyFingerprint != verValid.KeyFingerprint {
		t.Errorf("wrong-key should still report fingerprint: %q", verWrong.KeyFingerprint)
	}

	// 4. No trust store (nil)
	verNilTS := codec.Verify(*commit, nil)
	if verNilTS.Outcome != codec.OutcomeWrongKey || verNilTS.Valid {
		t.Errorf("expected wrong-key for nil trust store, got %+v", verNilTS)
	}

	// 5. Payload mutated
	mutatedCommit := *commit
	mutatedCommit.Payload = []byte("mutated payload bytes")
	verMutated := codec.Verify(mutatedCommit, ts)
	if verMutated.Outcome != codec.OutcomePayloadMutated || verMutated.Valid {
		t.Errorf("expected payload-mutated outcome, got %+v", verMutated)
	}

	// 6. Corrupted signature
	corruptedCommit := *commit
	corruptedCommit.Signature = "-----BEGIN SSH SIGNATURE-----\ngarbage\n-----END SSH SIGNATURE-----"
	verCorrupted := codec.Verify(corruptedCommit, ts)
	if verCorrupted.Outcome != codec.OutcomeCorruptedSignature || verCorrupted.Valid {
		t.Errorf("expected corrupted-signature outcome, got %+v", verCorrupted)
	}
}

// TestVerify_SignedCommitWithUnmodeledHeaders pins WRIT-286: a genuinely
// SSH-signed commit carrying change-id and encoding headers — extra
// headers object.Commit does not model with a field of its own,
// change-id — must still verify as valid, because FromGitCommit's
// payload comes from commit.EncodeWithoutSignature's raw-source-bytes
// path (see the comment on FromGitCommit's call site in
// internal/dag/enumerate.go), not from re-encoding go-git's parsed
// struct.
//
// The fixture is deliberately change-id *and* encoding together, not
// encoding alone. git writes `encoding` into the same header slot
// go-git's struct encoder does, so a commit with only an encoding header
// produces byte-identical payloads on both the raw-bytes and
// struct-encoder paths — a test built on that fixture alone would pass
// even if EncodeWithoutSignature's raw-bytes path never ran, pinning
// nothing (measured during WRIT-286 planning). change-id is a header
// go-git's struct encoder (commit.go's encode()) always re-emits *after*
// encoding, while this fixture — like real git — writes it *before*
// encoding; only the raw-bytes path preserves that order, which is what
// makes this fixture actually bite a matchesSource() regression.
func TestVerify_SignedCommitWithUnmodeledHeaders(t *testing.T) {
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

	if err := os.WriteFile(filepath.Join(repoDir, "op.json"), []byte(`{"body":{},"object_id":"w-01","object_type":"widget","op_type":"create","op_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "op.json")
	treeHash := strings.TrimSpace(runGit(t, repoDir, "write-tree"))

	when := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	stamp := fmt.Sprintf("%d +0000", when.Unix())

	// Composed by hand, in the order system git itself would write it:
	// change-id ahead of encoding (see the doc comment above for why
	// that order matters). ssh-keygen -Y sign below signs exactly these
	// bytes, so the commit is genuinely signed, not simulated.
	headers := "tree " + treeHash + "\n" +
		"author Alice Example <alice@example.com> " + stamp + "\n" +
		"committer Alice Example <alice@example.com> " + stamp + "\n" +
		"change-id I0123456789abcdef0123456789abcdef01234567\n" +
		"encoding ISO-8859-1\n"
	message := "writ: create widget/w-01\n"
	unsigned := headers + "\n" + message

	payloadPath := filepath.Join(tmp, "payload")
	if err := os.WriteFile(payloadPath, []byte(unsigned), 0o600); err != nil {
		t.Fatal(err)
	}

	signCmd := exec.Command("ssh-keygen", "-Y", "sign", "-f", privPath, "-n", "git", payloadPath)
	if out, err := signCmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen -Y sign: %v\n%s", err, out)
	}
	sigBytes, err := os.ReadFile(payloadPath + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	sigLines := strings.Split(strings.TrimSuffix(string(sigBytes), "\n"), "\n")
	gpgsig := "gpgsig " + strings.Join(sigLines, "\n ")

	full := headers + gpgsig + "\n\n" + message

	commitPath := filepath.Join(tmp, "commit-object")
	if err := os.WriteFile(commitPath, []byte(full), 0o600); err != nil {
		t.Fatal(err)
	}
	commitHashStr := strings.TrimSpace(runGit(t, repoDir, "hash-object", "-w", "-t", "commit", commitPath))

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		t.Fatalf("PlainOpen: %v", err)
	}
	commitObj, err := object.GetCommit(repo.Storer, plumbing.NewHash(commitHashStr))
	if err != nil {
		t.Fatalf("GetCommit: %v", err)
	}

	c, err := codec.FromGitCommit(repo.Storer, commitObj)
	if err != nil {
		t.Fatalf("FromGitCommit: %v", err)
	}

	ts, err := sshsig.ParseAllowedSigners(strings.NewReader("alice@example.com " + pubLine + "\n"))
	if err != nil {
		t.Fatalf("ParseAllowedSigners: %v", err)
	}

	ver := codec.Verify(c, ts)
	if ver.Outcome != codec.OutcomeValid || !ver.Valid {
		t.Fatalf("codec.Verify = %+v, want outcome %q", ver, codec.OutcomeValid)
	}

	// Non-vacuity guard (WRIT-286): the assertion above only pins
	// anything if the struct-encoder path would actually produce
	// different bytes for this commit than the raw-bytes path did;
	// otherwise it would pass whether or not EncodeWithoutSignature took
	// the raw-bytes path at all — exactly how the ticket's original
	// encoding-only fixture went vacuous. Build a shallow *object.Commit
	// carrying the same payload-affecting exported fields as commitObj,
	// with its unexported src left at its zero value (nil, since a
	// package outside object can't set it) — precisely what
	// EncodeWithoutSignature falls back to struct-encoding when
	// matchesSource() is false — and assert its payload differs from
	// c.Payload. A future simplification of this fixture down to a bare
	// `encoding` header (byte-identical on both paths, per the doc
	// comment above) would fail this assertion loudly instead of leaving
	// the test silently vacuous.
	shallow := &object.Commit{
		Hash:         commitObj.Hash,
		Author:       commitObj.Author,
		Committer:    commitObj.Committer,
		Message:      commitObj.Message,
		TreeHash:     commitObj.TreeHash,
		ParentHashes: commitObj.ParentHashes,
		Encoding:     commitObj.Encoding,
		ExtraHeaders: commitObj.ExtraHeaders,
	}
	structObj := &plumbing.MemoryObject{}
	if err := shallow.EncodeWithoutSignature(structObj); err != nil {
		t.Fatalf("shallow EncodeWithoutSignature: %v", err)
	}
	r, err := structObj.Reader()
	if err != nil {
		t.Fatal(err)
	}
	structPayload, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(structPayload, c.Payload) {
		t.Fatalf("non-vacuity guard failed: struct-encoder payload matches the raw payload byte-for-byte (%d bytes) — this fixture no longer bites a matchesSource() regression", len(c.Payload))
	}
}

func TestLoadTrustStore(t *testing.T) {
	// Empty path returns nil, nil
	ts, err := codec.LoadTrustStore("")
	if err != nil || ts != nil {
		t.Errorf("expected nil, nil for empty path, got %v, %v", ts, err)
	}

	// Valid path
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	sshPub, _ := ssh.NewPublicKey(pub)
	pubLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))

	tmp := t.TempDir()
	path := filepath.Join(tmp, "allowed_signers")
	if err := os.WriteFile(path, []byte("alice@example.com "+pubLine+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	loadedTS, err := codec.LoadTrustStore(path)
	if err != nil || loadedTS == nil {
		t.Fatalf("LoadTrustStore failed: %v", err)
	}
	if !loadedTS.IsAuthorized(sshPub, "alice@example.com", "git", time.Now()) {
		t.Error("loaded trust store failed authorization check")
	}
}
