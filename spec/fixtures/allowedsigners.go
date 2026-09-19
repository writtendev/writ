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

// TrustStoreFor builds the sshsig.TrustStore a description's fixture
// commits are verified against: NewTrustStore's embedded default for a
// description with no trust_store: override, or -- when one is given --
// a store built solely from those rules (WRIT-302). Each call starts
// from nothing and reads only desc's own field, so a pattern one
// description's trust_store: adds can never reclassify another
// description's commits.
func TrustStoreFor(desc *Description) (*sshsig.TrustStore, error) {
	if len(desc.TrustStore) == 0 {
		return NewTrustStore()
	}

	var buf strings.Builder
	for _, rule := range desc.TrustStore {
		id, err := lookupIdentity(rule.Key)
		if err != nil {
			return nil, fmt.Errorf("fixtures: description %q trust_store: %w", desc.Name, err)
		}
		pubLine, err := pubKeyLine(id)
		if err != nil {
			return nil, fmt.Errorf("fixtures: description %q trust_store: %w", desc.Name, err)
		}

		buf.WriteString(strings.Join(rule.Principals, ","))
		if len(rule.Namespaces) > 0 {
			fmt.Fprintf(&buf, ` namespaces="%s"`, strings.Join(rule.Namespaces, ","))
		}
		buf.WriteString(" " + pubLine + "\n")
	}

	return sshsig.ParseAllowedSigners(strings.NewReader(buf.String()))
}

// pubKeyLine returns id's embedded public key as an authorized_keys-style
// line ("<type> <base64>"), the form an allowed_signers line embeds after
// its principal field and options.
func pubKeyLine(id identity) (string, error) {
	data, err := keyFS.ReadFile(filepath.Join("keys", id.KeyFile+".pub"))
	if err != nil {
		return "", fmt.Errorf("read embedded pubkey for %s: %w", id.Name, err)
	}
	return strings.TrimSpace(string(data)), nil
}
