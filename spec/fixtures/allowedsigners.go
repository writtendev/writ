package fixtures

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/writtendev/writ/internal/codec/sshsig"
)

// AllowedSignersContent returns an OpenSSH allowed_signers formatted string
// mapping each embedded fixture identity (keys/*.pub) to its email principal.
func AllowedSignersContent() (string, error) {
	var buf strings.Builder

	entries, err := keyFS.ReadDir("keys")
	if err != nil {
		return "", fmt.Errorf("fixtures: read embedded keys: %w", err)
	}

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".pub") {
			continue
		}
		data, err := keyFS.ReadFile(filepath.Join("keys", e.Name()))
		if err != nil {
			return "", fmt.Errorf("fixtures: read embedded pubkey %s: %w", e.Name(), err)
		}
		pubLine := strings.TrimSpace(string(data))
		// e.g. alice_ed25519.pub -> alice
		idName := strings.TrimSuffix(e.Name(), "_ed25519.pub")
		id, ok := identities[idName]
		if ok {
			buf.WriteString(fmt.Sprintf("%s %s\n", id.Email, pubLine))
		}
	}

	return buf.String(), nil
}

// NewTrustStore constructs an sshsig.TrustStore populated with all embedded fixture public keys.
func NewTrustStore() (*sshsig.TrustStore, error) {
	content, err := AllowedSignersContent()
	if err != nil {
		return nil, err
	}
	return sshsig.ParseAllowedSigners(strings.NewReader(content))
}
