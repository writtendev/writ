package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// MintWriterID generates a new 64-bit random WriterID (16 lowercase hex characters)
// using crypto/rand, round-tripped and validated through ParseWriterID.
func MintWriterID() (WriterID, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("identity: mint writer id: %w", err)
	}
	return ParseWriterID(hex.EncodeToString(buf[:]))
}

// EnsureWriterID resolves or mints a WriterID for the repository at repoDir,
// implementing spec/ref-layout.md §Sourcing precedence:
//
//  1. If writ.writerId is already present in merged git config (local or global),
//     it is validated and reused as-is without modifying repository config.
//  2. Otherwise, a new WriterID is minted (retrying if taken returns true)
//     and persisted to local repository configuration via 'git config --local writ.writerId <id>'.
//
// The returned boolean reports whether a new WriterID was minted (true) or an existing
// ID was reused (false).
func EnsureWriterID(ctx context.Context, repoDir string, taken func(WriterID) bool) (WriterID, bool, error) {
	cfg, err := readGitConfig(ctx, repoDir)
	if err != nil {
		return "", false, err
	}

	// The presence test trims and the parse does not, which is the pairing
	// Load uses: a key with nothing but whitespace in it is unconfigured and
	// gets minted into, and anything else has to parse exactly. Trimming the
	// parsed value here instead would let a padded id name a ref namespace,
	// and the two functions would then disagree about which strings are writer
	// ids.
	if rawID, ok := cfg["writ.writerid"]; ok && strings.TrimSpace(rawID) != "" {
		id, err := ParseWriterID(rawID)
		if err != nil {
			return "", false, withRemedy(err, remintRemedy)
		}
		return id, false, nil
	}

	// Mint a new ID, avoiding collisions if a predicate is provided
	var mintedID WriterID
	for {
		if ctx.Err() != nil {
			return "", false, &ConfigError{
				Problem: ctx.Err(),
			}
		}
		id, err := MintWriterID()
		if err != nil {
			return "", false, err
		}
		if taken != nil && taken(id) {
			continue
		}
		mintedID = id
		break
	}

	// Persist to local config
	cmd := exec.CommandContext(ctx, "git", "config", "--local", "writ.writerId", string(mintedID))
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", false, &ConfigError{
				Key:     "writ.writerId",
				Problem: ctx.Err(),
			}
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(string(out))
			if stderr != "" {
				return "", false, &ConfigError{
					Key:     "writ.writerId",
					Problem: fmt.Errorf("git config --local in %q: %s (%w)", repoDir, stderr, exitErr),
				}
			}
		}
		return "", false, &ConfigError{
			Key:     "writ.writerId",
			Problem: fmt.Errorf("git config --local in %q: %w", repoDir, err),
		}
	}

	return mintedID, true, nil
}
