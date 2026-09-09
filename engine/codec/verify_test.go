package codec_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/sshsig"
	"github.com/writtendev/writ/engine/identity"
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
