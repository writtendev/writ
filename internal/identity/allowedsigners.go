package identity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// AllowedSignersFile resolves gpg.ssh.allowedSignersFile out of git config
// in repoDir, independently of Load: Load returns early (missing
// writ.writerId, user.name, user.email, gpg.format, or user.signingKey)
// before it ever reads gpg.ssh.allowedSignersFile, so a repository with a
// read-only identity — no writer id, no signing key, nothing to write with
// — would silently report no trust store at all through Load's
// Identity.AllowedSigners, even when gpg.ssh.allowedSignersFile is set.
// Verification runs on every read, including a reader with no writer
// identity, so it needs a path that does not depend on Load succeeding.
//
// The returned path is trimmed the same way Load's own read is (a
// whitespace-only value is nothing configured, not a garbage path), and a
// leading "~/" is expanded against the user's home directory, matching
// codec/sign.go's SSHSigner.Sign. An empty result means unset; resolving
// or reading the file is the caller's job (Open never refuses to open on
// its account — WRIT-251 ruling 2).
func AllowedSignersFile(ctx context.Context, repoDir string) (string, error) {
	cfg, err := readGitConfig(ctx, repoDir)
	if err != nil {
		return "", err
	}
	return allowedSignersFromConfig(cfg), nil
}

// allowedSignersFromConfig is the shared logic behind AllowedSignersFile and
// Load's own Identity.AllowedSigners field: one trim-and-expand rule, so
// the two never drift onto different answers for the same config map.
func allowedSignersFromConfig(cfg map[string]string) string {
	path := strings.TrimSpace(cfg["gpg.ssh.allowedsignersfile"])
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	return path
}
