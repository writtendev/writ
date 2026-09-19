package sync_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

			// A remote must actually be configured (remote.origin.url set)
			// before Ensure will write anything to it (WRIT-283): without
			// this, every case below now fails at the existence check this
			// test predates.
			cmd := exec.Command("git", "config", "remote.origin.url", "https://example.test/origin.git")
			cmd.Dir = dir
			if err := cmd.Run(); err != nil {
				t.Fatalf("set remote.origin.url: %v", err)
			}

			// Add standard heads refspec and unrelated entries
			cmd = exec.Command("git", "config", "--add", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
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

// TestValidateRemoteName pins the boring checks ValidateRemoteName runs, in
// the order it runs them: empty, "-"-leading (the primary defense against
// the argument-injection hole "writ sync -- --upload-pack=<script>" verified
// to execute, WRIT-283), and go-git's own reference-name validation (with
// the lone-"@"-component override) as the catch-all.
//
// "/"-containing names ("team/fork", "a/@", "@/a") are pinned valid --
// round-2 review finding: git itself accepts them ("git remote add" is the
// oracle, verified against git 2.50.1) and a round-1 ban on the rationale
// of git's valid_remote_nick rule rejected a class of remotes git supports,
// stranding a writer's ops. "@" alone is pinned valid for the same reason:
// go-git's reference-name validator rejects a lone "@" component where git
// does not, and ValidateRemoteName routes around that one divergence.
func TestValidateRemoteName(t *testing.T) {
	valid := []string{
		"origin", "up-stream", "a.b",
		"team/fork", "@", "a/@", "@/a", "a/b/c", "üñîçødé",
	}
	for _, name := range valid {
		t.Run(fmt.Sprintf("valid_%q", name), func(t *testing.T) {
			if err := writsync.ValidateRemoteName(name); err != nil {
				t.Errorf("ValidateRemoteName(%q) = %v, want nil", name, err)
			}
		})
	}

	invalid := []string{
		"", "-x", "--upload-pack=/bin/sh", "a b",
		".", "..", "a..b", "x.lock", "he^ad", "q?", "a@{0}",
		"a//b", "/a", "a/", "a/.lock", "@{",
	}
	for _, name := range invalid {
		t.Run(fmt.Sprintf("invalid_%q", name), func(t *testing.T) {
			err := writsync.ValidateRemoteName(name)
			if err == nil {
				t.Fatalf("ValidateRemoteName(%q) = nil, want error", name)
			}
			if !errors.Is(err, writsync.ErrInvalidRemoteName) {
				t.Errorf("ValidateRemoteName(%q) error = %v, want errors.Is(err, ErrInvalidRemoteName)", name, err)
			}
		})
	}
}

// TestRefspec_EnsureRejectsUnconfiguredRemote is the regression pin for the
// WRIT-283 phantom-remote bug: Ensure on a remote with no remote.<name>.*
// section at all must fail without writing anything -- not even a
// url-less [remote "zzz"] fetch-only section.
func TestRefspec_EnsureRejectsUnconfiguredRemote(t *testing.T) {
	dir, _ := initTestRepo(t)
	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")

	client, err := writsync.Open(dir, ident)
	if err != nil {
		t.Fatalf("Open client: %v", err)
	}

	configPath := filepath.Join(dir, ".git", "config")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read .git/config before Ensure: %v", err)
	}

	if _, err := client.Ensure(context.Background(), "zzz"); err == nil {
		t.Fatalf("Ensure(zzz) succeeded on a repo with no remote.zzz.* section at all")
	} else if !errors.Is(err, writsync.ErrUnknownRemote) {
		t.Errorf("Ensure(zzz) error = %v, want errors.Is(err, ErrUnknownRemote)", err)
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read .git/config after Ensure: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf(".git/config changed after Ensure rejected an unconfigured remote:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestRefspec_RemoteConfiguredAcceptsURLOrPushurl pins the round-1 review
// fix for RemoteConfigured (WRIT-283): a remote reads as configured if
// either remote.<name>.url or remote.<name>.pushurl is set. A push-only
// remote (pushurl with no url) is legitimate, git-supported configuration
// -- "git remote" lists it and "git push" works against it -- and probing
// url alone made it read as nonexistent, stranding ops it could otherwise
// still push (the round-1 finding). This runs alongside, not instead of,
// TestRefspec_EnsureRejectsUnconfiguredRemote: both properties -- widened
// acceptance for a real remote, and a byte-identical .git/config for a
// truly unconfigured one -- must hold at once.
func TestRefspec_RemoteConfiguredAcceptsURLOrPushurl(t *testing.T) {
	tests := []struct {
		name       string
		configArgs [][]string
	}{
		{
			name: "url only",
			configArgs: [][]string{
				{"config", "remote.r.url", "https://example.test/r.git"},
			},
		},
		{
			name: "pushurl only, no url",
			configArgs: [][]string{
				{"config", "remote.r.pushurl", "https://example.test/r.git"},
			},
		},
		{
			name: "both url and pushurl",
			configArgs: [][]string{
				{"config", "remote.r.url", "https://example.test/r-fetch.git"},
				{"config", "remote.r.pushurl", "https://example.test/r-push.git"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := initTestRepo(t)
			ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")

			for _, args := range tc.configArgs {
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				if err := cmd.Run(); err != nil {
					t.Fatalf("git %v: %v", args, err)
				}
			}

			client, err := writsync.Open(dir, ident)
			if err != nil {
				t.Fatalf("Open client: %v", err)
			}

			configured, err := client.RemoteConfigured(context.Background(), "r")
			if err != nil {
				t.Fatalf("RemoteConfigured: %v", err)
			}
			if !configured {
				t.Errorf("RemoteConfigured(%q) = false, want true", tc.name)
			}

			// Ensure must also succeed end-to-end against this remote --
			// the phantom-section bug this probe exists to prevent is only
			// closed if a remote that RemoteConfigured accepts is one
			// Ensure is actually willing to write refspecs for.
			if _, err := client.Ensure(context.Background(), "r"); err != nil {
				t.Errorf("Ensure(%q) after RemoteConfigured=true: %v", tc.name, err)
			}
		})
	}
}

// writeArgvStub writes a shell script standing in for the git binary: it
// appends every argument it is invoked with, one per line, to outPath, and
// exits 0 without doing anything else. Client.Fetch and Client.Push only
// need dag.Chains(c.storer) (a go-git read, not a subprocess) to succeed
// around the one runGit call each makes, which an empty repo already
// satisfies.
func writeArgvStub(t *testing.T, outPath string) string {
	t.Helper()
	dir := t.TempDir()
	stubPath := filepath.Join(dir, "git-stub")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" >> %q\nexit 0\n", outPath)
	if err := os.WriteFile(stubPath, []byte(script), 0755); err != nil {
		t.Fatalf("write git stub: %v", err)
	}
	return stubPath
}

func readArgvLog(t *testing.T, outPath string) []string {
	t.Helper()
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// TestSync_ArgvIncludesEndOfOptions pins the argument-injection fix (WRIT-283):
// --end-of-options must precede the remote name positional on the actual
// Fetch and Push transport calls, not just in the code as read -- a stub
// git binary records the real argv so this can't silently regress.
func TestSync_ArgvIncludesEndOfOptions(t *testing.T) {
	assertEndOfOptionsPrecedesRemote := func(t *testing.T, argv []string, remote string) {
		t.Helper()
		eooIdx, remoteIdx := -1, -1
		for i, a := range argv {
			if a == "--end-of-options" && eooIdx == -1 {
				eooIdx = i
			}
			if a == remote && remoteIdx == -1 {
				remoteIdx = i
			}
		}
		if eooIdx == -1 {
			t.Fatalf("argv %v does not include --end-of-options", argv)
		}
		if remoteIdx == -1 {
			t.Fatalf("argv %v does not include the remote %q", argv, remote)
		}
		if remoteIdx < eooIdx {
			t.Fatalf("argv %v: remote %q (index %d) precedes --end-of-options (index %d)", argv, remote, remoteIdx, eooIdx)
		}
	}

	t.Run("fetch", func(t *testing.T) {
		dir, _ := initTestRepo(t)
		ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
		outPath := filepath.Join(t.TempDir(), "argv.log")
		stub := writeArgvStub(t, outPath)

		client, err := writsync.Open(dir, ident, writsync.WithGitBinary(stub))
		if err != nil {
			t.Fatalf("Open client: %v", err)
		}
		if _, err := client.Fetch(context.Background(), "origin"); err != nil {
			t.Fatalf("Fetch failed: %v", err)
		}

		argv := readArgvLog(t, outPath)
		if len(argv) == 0 || argv[0] != "fetch" {
			t.Fatalf("argv = %v, want it to start with \"fetch\"", argv)
		}
		assertEndOfOptionsPrecedesRemote(t, argv, "origin")
	})

	t.Run("push", func(t *testing.T) {
		dir, _ := initTestRepo(t)
		ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
		outPath := filepath.Join(t.TempDir(), "argv.log")
		stub := writeArgvStub(t, outPath)

		client, err := writsync.Open(dir, ident, writsync.WithGitBinary(stub))
		if err != nil {
			t.Fatalf("Open client: %v", err)
		}
		if _, err := client.Push(context.Background(), "origin"); err != nil {
			t.Fatalf("Push failed: %v", err)
		}

		argv := readArgvLog(t, outPath)
		// "push" must stay args[0]: ClassifyGitError (internal/sync/git.go)
		// branches on args[0] == "push" to pick push-path advice.
		if len(argv) == 0 || argv[0] != "push" {
			t.Fatalf("argv = %v, want it to start with \"push\"", argv)
		}
		assertEndOfOptionsPrecedesRemote(t, argv, "origin")
	})
}
