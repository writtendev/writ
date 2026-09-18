package identity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ReadGitConfig executes git config --list --null in repoDir and parses the
// output into a map of lowercase keys to values.
func ReadGitConfig(ctx context.Context, repoDir string) (map[string]string, error) {
	return readGitConfig(ctx, repoDir)
}

// readGitConfig executes git config --list --null in repoDir and parses the
// output into a map of lowercase keys to values, with later entries overriding
// earlier ones (matching git config precedence: local overrides global, etc.).
func readGitConfig(ctx context.Context, repoDir string) (map[string]string, error) {
	cmd := exec.CommandContext(ctx, "git", "config", "--list", "--null")
	cmd.Dir = repoDir

	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, &ConfigError{
				Problem: ctx.Err(),
			}
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if stderr != "" {
				return nil, &ConfigError{
					Problem: fmt.Errorf("git config in %q: %s (%w)", repoDir, stderr, exitErr),
				}
			}
		}
		return nil, &ConfigError{
			Problem: fmt.Errorf("git config in %q: %w", repoDir, err),
		}
	}

	return parseGitConfigNull(out), nil
}

// parseGitConfigNull parses the NUL-separated records output by
// git config --list --null.
func parseGitConfigNull(data []byte) map[string]string {
	res := make(map[string]string)
	records := bytes.Split(data, []byte{0})
	for _, rec := range records {
		if len(rec) == 0 {
			continue
		}
		idx := bytes.IndexByte(rec, '\n')
		if idx < 0 {
			key := strings.ToLower(string(rec))
			res[key] = ""
		} else {
			key := strings.ToLower(string(rec[:idx]))
			val := string(rec[idx+1:])
			res[key] = val
		}
	}
	return res
}
