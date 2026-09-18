package codec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/writtendev/writ/internal/identity"
)

// Signer signs commit payload bytes, returning an armored SSH signature string.
type Signer interface {
	Sign(ctx context.Context, payload []byte) (string, error)
}

// SignerFunc adapts an ordinary function to the Signer interface.
type SignerFunc func(ctx context.Context, payload []byte) (string, error)

// Sign calls f(ctx, payload).
func (f SignerFunc) Sign(ctx context.Context, payload []byte) (string, error) {
	return f(ctx, payload)
}

// SSHSigner implements Signer using ssh-keygen -Y sign -n git.
type SSHSigner struct {
	key identity.SigningKey
}

// NewSigner constructs a Signer for the given identity.SigningKey.
func NewSigner(key identity.SigningKey) (Signer, error) {
	if key.Format == "" || strings.ToLower(key.Format) != "ssh" {
		return nil, &identity.ConfigError{
			Key:     "gpg.format",
			Value:   key.Format,
			Problem: identity.ErrUnsupportedFormat,
		}
	}

	if key.Value == "" {
		return nil, &identity.ConfigError{
			Key:     "user.signingKey",
			Problem: identity.ErrMissing,
		}
	}

	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		return nil, fmt.Errorf("codec: ssh-keygen not found on PATH: %w", err)
	}

	if err := checkOpenSSHVersion(); err != nil {
		return nil, err
	}

	return &SSHSigner{key: key}, nil
}

var openSSHVersionRegex = regexp.MustCompile(`OpenSSH_(\d+)\.(\d+)`)

func checkOpenSSHVersion() error {
	// Try ssh -V first
	cmd := exec.Command("ssh", "-V")
	out, err := cmd.CombinedOutput()
	if err == nil || len(out) > 0 {
		matches := openSSHVersionRegex.FindSubmatch(out)
		if len(matches) >= 3 {
			major, _ := strconv.Atoi(string(matches[1]))
			minor, _ := strconv.Atoi(string(matches[2]))
			if major < 8 || (major == 8 && minor < 2) {
				return fmt.Errorf("codec: OpenSSH 8.2+ required for SSH signing, found OpenSSH %d.%d", major, minor)
			}
			return nil
		}
	}

	// Fallback to checking ssh-keygen help/usage text for -Y sign support
	helpCmd := exec.Command("ssh-keygen", "-h")
	helpOut, _ := helpCmd.CombinedOutput()
	if strings.Contains(string(helpOut), "-Y sign") {
		return nil
	}

	// Also check ssh-keygen with no args (exits 1 on OpenSSH and prints usage)
	noArgCmd := exec.Command("ssh-keygen")
	noArgOut, _ := noArgCmd.CombinedOutput()
	if strings.Contains(string(noArgOut), "-Y sign") {
		return nil
	}

	return errors.New("codec: OpenSSH 8.2+ required for SSH signing (-Y sign unsupported)")
}

// Sign executes ssh-keygen -Y sign -n git against payload and returns the armored signature.
func (s *SSHSigner) Sign(ctx context.Context, payload []byte) (string, error) {
	tmpDir, err := os.MkdirTemp("", "writ-sign-")
	if err != nil {
		return "", fmt.Errorf("codec: create signing scratch dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	var keyPath string
	if s.key.Literal {
		keyPath = filepath.Join(tmpDir, "key.pub")
		if err := os.WriteFile(keyPath, []byte(s.key.Value+"\n"), 0o600); err != nil {
			return "", fmt.Errorf("codec: write literal signing key: %w", err)
		}
	} else {
		keyPath = s.key.Value
		if strings.HasPrefix(keyPath, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				keyPath = filepath.Join(home, keyPath[2:])
			}
		}
	}

	dataFile := filepath.Join(tmpDir, "payload")
	if err := os.WriteFile(dataFile, payload, 0o600); err != nil {
		return "", fmt.Errorf("codec: write signing payload: %w", err)
	}

	cmd := exec.CommandContext(ctx, "ssh-keygen", "-Y", "sign", "-f", keyPath, "-n", "git", dataFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("codec: ssh-keygen -Y sign: %w\n%s", err, strings.TrimSpace(string(out)))
	}

	sigBytes, err := os.ReadFile(dataFile + ".sig")
	if err != nil {
		return "", fmt.Errorf("codec: read signature output: %w", err)
	}

	return string(sigBytes), nil
}

// SignCommit signs a Commit using s, recording the signature and setting c.ID to the signed commit SHA.
func SignCommit(ctx context.Context, s Signer, c *Commit) error {
	if c == nil {
		return errors.New("codec: nil commit")
	}

	if len(c.Payload) == 0 {
		gitCommit, err := ToGitCommit(*c)
		if err != nil {
			return fmt.Errorf("codec: prepare commit payload: %w", err)
		}
		payloadObj := &plumbing.MemoryObject{}
		if err := gitCommit.EncodeWithoutSignature(payloadObj); err != nil {
			return fmt.Errorf("codec: encode commit payload: %w", err)
		}
		r, err := payloadObj.Reader()
		if err != nil {
			return fmt.Errorf("codec: read commit payload: %w", err)
		}
		payload, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			return fmt.Errorf("codec: read commit payload bytes: %w", err)
		}
		c.Payload = payload
	}

	sig, err := s.Sign(ctx, c.Payload)
	if err != nil {
		return err
	}
	c.Signature = sig

	gitCommit, err := ToGitCommit(*c)
	if err != nil {
		return fmt.Errorf("codec: finalize signed commit: %w", err)
	}
	cObj := &plumbing.MemoryObject{}
	cObj.SetType(plumbing.CommitObject)
	if err := gitCommit.Encode(cObj); err != nil {
		return fmt.Errorf("codec: encode signed commit: %w", err)
	}
	c.ID = cObj.Hash().String()
	return nil
}
