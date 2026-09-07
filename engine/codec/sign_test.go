package codec_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/sshsig"
	"github.com/writtendev/writ/engine/identity"
)

func TestSigner_ConfigValidation(t *testing.T) {
	// Missing/unsupported format
	_, err := codec.NewSigner(identity.SigningKey{
		Format: "openpgp",
		Value:  "/some/path",
	})
	var cfgErr *identity.ConfigError
	if !errors.As(err, &cfgErr) || cfgErr.Key != "gpg.format" {
		t.Errorf("expected gpg.format ConfigError, got %v", err)
	}

	// Missing key value
	_, err = codec.NewSigner(identity.SigningKey{
		Format: "ssh",
		Value:  "",
	})
	if !errors.As(err, &cfgErr) || cfgErr.Key != "user.signingKey" {
		t.Errorf("expected user.signingKey ConfigError, got %v", err)
	}
}

func startTestSSHAgent(t *testing.T) (string, func()) {
	t.Helper()
	cmd := exec.Command("ssh-agent", "-s")
	cmd.Env = append(os.Environ(), "SHELL=/bin/sh")
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("ssh-agent unavailable: %v", err)
	}
	sockRe := regexp.MustCompile(`SSH_AUTH_SOCK=([^;]+);`)
	pidRe := regexp.MustCompile(`SSH_AGENT_PID=([^;]+);`)
	sockMatch := sockRe.FindSubmatch(out)
	pidMatch := pidRe.FindSubmatch(out)
	if sockMatch == nil || pidMatch == nil {
		t.Fatalf("could not parse ssh-agent output: %s", out)
	}
	sock := string(sockMatch[1])
	pid := string(pidMatch[1])

	cleanup := func() {
		killCmd := exec.Command("ssh-agent", "-k")
		killCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock, "SSH_AGENT_PID="+pid)
		_ = killCmd.Run()
	}
	return sock, cleanup
}

func TestSigner_LiteralKeyWithAgent(t *testing.T) {
	sock, cleanup := startTestSSHAgent(t)
	defer cleanup()

	tmp := t.TempDir()
	privPath := filepath.Join(tmp, "id_ed25519")
	pubPath := privPath + ".pub"

	genCmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", privPath)
	if out, err := genCmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen unavailable: %v\n%s", err, out)
	}

	addCmd := exec.Command("ssh-add", privPath)
	addCmd.Env = append(os.Environ(), "SSH_AUTH_SOCK="+sock)
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-add failed: %v\n%s", err, out)
	}

	pubBytes, err := os.ReadFile(pubPath)
	if err != nil {
		t.Fatal(err)
	}
	pubLine := strings.TrimSpace(string(pubBytes))

	// Remove private key to simulate agent-held key
	_ = os.Remove(privPath)

	t.Setenv("SSH_AUTH_SOCK", sock)

	signer, err := codec.NewSigner(identity.SigningKey{
		Format:  "ssh",
		Value:   pubLine,
		Literal: true,
	})
	if err != nil {
		t.Fatalf("NewSigner literal key failed: %v", err)
	}

	payload := []byte("test payload for literal key\n")
	sig, err := signer.Sign(context.Background(), payload)
	if err != nil {
		t.Fatalf("Sign with literal key failed: %v", err)
	}

	parsedSig, err := sshsig.ParseSignature(sig)
	if err != nil {
		t.Fatalf("ParseSignature failed: %v", err)
	}
	if err := sshsig.Verify(parsedSig, payload, "git"); err != nil {
		t.Errorf("Verify signature from literal key failed: %v", err)
	}
}

func TestSignCommit_Integration(t *testing.T) {
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

	env := codec.Envelope{
		ObjectID:   "review-01",
		ObjectType: "review",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}
	author := codec.Identity{
		Name:  "Alice",
		Email: "alice@example.com",
		When:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	commit, err := codec.BuildCommit(env, author, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if commit.Signature != "" {
		t.Error("expected unsigned commit before SignCommit")
	}

	if err := codec.SignCommit(context.Background(), signer, commit); err != nil {
		t.Fatalf("SignCommit failed: %v", err)
	}

	if commit.Signature == "" {
		t.Error("expected signature to be populated")
	}
	if commit.ID == "" {
		t.Error("expected commit ID to be populated")
	}
}
