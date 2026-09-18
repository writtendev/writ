package sync_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	writsync "github.com/writtendev/writ/internal/sync"
)

type vectorsJSON struct {
	Refspecs struct {
		Fetch string `json:"fetch"`
		Push  string `json:"push"`
	} `json:"refspecs"`
}

func TestRefspec_VectorsMatchSpec(t *testing.T) {
	specPath := filepath.Join("..", "..", "spec", "testdata", "ref-names", "vectors.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("failed to read spec vectors: %v", err)
	}

	var vec vectorsJSON
	if err := json.Unmarshal(data, &vec); err != nil {
		t.Fatalf("unmarshal vectors.json: %v", err)
	}

	// 1. Fetch refspec template check
	remote := "origin"
	expectedFetch := strings.ReplaceAll(vec.Refspecs.Fetch, "<remote>", remote)
	actualFetch := writsync.FetchRefspec(remote)
	if actualFetch != expectedFetch {
		t.Fatalf("FetchRefspec(%q) = %q, want %q", remote, actualFetch, expectedFetch)
	}

	upstreamRemote := "upstream"
	expectedUpstreamFetch := strings.ReplaceAll(vec.Refspecs.Fetch, "<remote>", upstreamRemote)
	actualUpstreamFetch := writsync.FetchRefspec(upstreamRemote)
	if actualUpstreamFetch != expectedUpstreamFetch {
		t.Fatalf("FetchRefspec(%q) = %q, want %q", upstreamRemote, actualUpstreamFetch, expectedUpstreamFetch)
	}

	// 2. Push refspec template check
	writerID := "0123456789abcdef"
	expectedPush := strings.ReplaceAll(vec.Refspecs.Push, "<writer-id>", writerID)
	actualPush := writsync.PushRefspec(testIdentity(writerID, "A", "a@a.com").WriterID)
	if actualPush != expectedPush {
		t.Fatalf("PushRefspec(%q) = %q, want %q", writerID, actualPush, expectedPush)
	}
}

func TestRefspec_EnsureIdempotentRepair(t *testing.T) {
	tests := []struct {
		name          string
		initialConfig []string
		initialState  writsync.RefspecState
	}{
		{
			name:          "missing writ refspec",
			initialConfig: []string{},
			initialState:  writsync.StatusMissing,
		},
		{
			name: "valid (forced) writ refspec already present",
			initialConfig: []string{
				"+refs/writ/*:refs/remotes/origin/writ/*",
			},
			initialState: writsync.StatusValid,
		},
		{
			// The pre-WRIT-270 canonical form: a real old clone's
			// .git/config, now drift that Ensure must repair.
			name: "unforced writ refspec (old clone, no leading plus)",
			initialConfig: []string{
				"refs/writ/*:refs/remotes/origin/writ/*",
			},
			initialState: writsync.StatusUnforced,
		},
		{
			name: "duplicate writ refspecs",
			initialConfig: []string{
				"+refs/writ/*:refs/remotes/origin/writ/*",
				"+refs/writ/*:refs/remotes/origin/writ/*",
			},
			initialState: writsync.StatusDuplicate,
		},
		{
			name: "wrong destination namespace",
			initialConfig: []string{
				"+refs/writ/*:refs/writ/*",
			},
			initialState: writsync.StatusWrongDestination,
		},
		{
			name: "wrong destination remote",
			initialConfig: []string{
				"+refs/writ/*:refs/remotes/other/writ/*",
			},
			initialState: writsync.StatusWrongDestination,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := initTestRepo(t)
			ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")

			// Add standard heads refspec and unrelated entries
			cmd := exec.Command("git", "config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
			cmd.Dir = dir
			if err := cmd.Run(); err != nil {
				t.Fatalf("add heads refspec: %v", err)
			}
			cmd = exec.Command("git", "config", "--add", "remote.origin.fetch", "+refs/custom/*:refs/remotes/origin/custom/*")
			cmd.Dir = dir
			if err := cmd.Run(); err != nil {
				t.Fatalf("add custom refspec: %v", err)
			}

			// Add initial config for test case
			for _, entry := range tc.initialConfig {
				cmd = exec.Command("git", "config", "--add", "remote.origin.fetch", entry)
				cmd.Dir = dir
				if err := cmd.Run(); err != nil {
					t.Fatalf("add test entry: %v", err)
				}
			}

			client, err := writsync.Open(dir, ident)
			if err != nil {
				t.Fatalf("Open client: %v", err)
			}

			// 1. Check initial state
			ctx := context.Background()
			initialStatus, err := client.Check(ctx, "origin")
			if err != nil {
				t.Fatalf("Check failed: %v", err)
			}
			if initialStatus.State != tc.initialState {
				t.Fatalf("Check initial state = %q, want %q", initialStatus.State, tc.initialState)
			}

			// 2. Ensure refspecs are repaired
			repairedStatus, err := client.Ensure(ctx, "origin")
			if err != nil {
				t.Fatalf("Ensure failed: %v", err)
			}
			if repairedStatus.State != writsync.StatusValid {
				t.Fatalf("Ensure state = %q, want %q", repairedStatus.State, writsync.StatusValid)
			}
			if tc.initialState != writsync.StatusValid && !repairedStatus.Repaired {
				t.Fatalf("expected Repaired=true for initial state %q", tc.initialState)
			}
			if tc.initialState == writsync.StatusValid && repairedStatus.Repaired {
				t.Fatalf("expected Repaired=false when already valid")
			}

			// 3. Ensure idempotency: running Ensure a second time is a no-op
			secondStatus, err := client.Ensure(ctx, "origin")
			if err != nil {
				t.Fatalf("second Ensure failed: %v", err)
			}
			if secondStatus.State != writsync.StatusValid {
				t.Fatalf("second Ensure state = %q, want %q", secondStatus.State, writsync.StatusValid)
			}
			if secondStatus.Repaired {
				t.Fatalf("second Ensure must not repair (Repaired = false)")
			}

			// 4. Assert non-writ refspecs survived completely intact
			cmd = exec.Command("git", "config", "--get-all", "remote.origin.fetch")
			cmd.Dir = dir
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("git config --get-all: %v", err)
			}
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")

			var foundHeads, foundCustom, foundWrit int
			for _, line := range lines {
				switch line {
				case "+refs/heads/*:refs/remotes/origin/*":
					foundHeads++
				case "+refs/custom/*:refs/remotes/origin/custom/*":
					foundCustom++
				case "+refs/writ/*:refs/remotes/origin/writ/*":
					foundWrit++
				}
			}

			if foundHeads != 1 {
				t.Fatalf("expected 1 heads refspec, got %d (all lines: %v)", foundHeads, lines)
			}
			if foundCustom != 1 {
				t.Fatalf("expected 1 custom refspec, got %d (all lines: %v)", foundCustom, lines)
			}
			if foundWrit != 1 {
				t.Fatalf("expected exactly 1 writ refspec, got %d (all lines: %v)", foundWrit, lines)
			}
		})
	}
}
