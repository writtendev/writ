package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var repoIDRegexp = regexp.MustCompile(`^[0-9a-f]{32}$`)

// RepoID is an immutable 128-bit identifier (32 lowercase hex characters)
// uniquely identifying a git repository within a Writ workspace.
type RepoID string

// ParseRepoID parses and validates s as a RepoID matching ^[0-9a-f]{32}$.
//
// Like ParseWriterID, it says what the value had to look like and names no
// remedy; the two callers that read writ.repoId out of git config attach one.
func ParseRepoID(s string) (RepoID, error) {
	if !repoIDRegexp.MatchString(s) {
		return "", &ConfigError{
			Key:     "writ.repoId",
			Value:   s,
			Problem: fmt.Errorf("%w: expected 32 lowercase hex characters", ErrInvalid),
		}
	}
	return RepoID(s), nil
}

// MintRepoID generates a new 128-bit random RepoID (32 lowercase hex characters)
// using crypto/rand, round-tripped and validated through ParseRepoID.
func MintRepoID() (RepoID, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("identity: mint repo id: %w", err)
	}
	return ParseRepoID(hex.EncodeToString(buf[:]))
}

// LoadRepoID reads the repository designator out of git config in repoDir.
// If writ.repoId is unset or empty, it returns ("", nil) so repositories
// that have never run 'writ init' can still open without error.
func LoadRepoID(ctx context.Context, repoDir string) (RepoID, error) {
	cfg, err := readGitConfig(ctx, repoDir)
	if err != nil {
		return "", err
	}

	rawID, ok := cfg["writ.repoid"]
	if !ok || strings.TrimSpace(rawID) == "" {
		return "", nil
	}

	// This was a dead end: `writ.repoId = notahexrepoid` failed writ init with
	// "invalid configuration" and no next step at all — the same defect
	// WRIT-143 fixed one key over. EnsureRepoID mints only into an absent key,
	// so the key has to be cleared before init can replace it.
	id, err := ParseRepoID(rawID)
	if err != nil {
		return "", withRemedy(err, remintRemedy)
	}
	return id, nil
}

// EnsureRepoID resolves or mints a RepoID for the repository at repoDir:
//
//  1. If writ.repoId is already present in merged git config, it is validated
//     and reused as-is without modifying repository config.
//  2. Otherwise, a new RepoID is minted and persisted to local repository
//     configuration via 'git config --local writ.repoId <id>'.
//
// The returned boolean reports whether a new RepoID was minted (true) or an existing
// ID was reused (false).
func EnsureRepoID(ctx context.Context, repoDir string) (RepoID, bool, error) {
	cfg, err := readGitConfig(ctx, repoDir)
	if err != nil {
		return "", false, err
	}

	if rawID, ok := cfg["writ.repoid"]; ok && strings.TrimSpace(rawID) != "" {
		id, err := ParseRepoID(rawID)
		if err != nil {
			return "", false, withRemedy(err, remintRemedy)
		}
		return id, false, nil
	}

	id, err := MintRepoID()
	if err != nil {
		return "", false, err
	}

	// Persist to local config
	cmd := exec.CommandContext(ctx, "git", "config", "--local", "writ.repoId", string(id))
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return "", false, &ConfigError{
				Key:     "writ.repoId",
				Problem: ctx.Err(),
			}
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(string(out))
			if stderr != "" {
				return "", false, &ConfigError{
					Key:     "writ.repoId",
					Problem: fmt.Errorf("git config --local in %q: %s (%w)", repoDir, stderr, exitErr),
				}
			}
		}
		return "", false, &ConfigError{
			Key:     "writ.repoId",
			Problem: fmt.Errorf("git config --local in %q: %w", repoDir, err),
		}
	}

	return id, true, nil
}
