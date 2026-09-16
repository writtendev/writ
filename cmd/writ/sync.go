package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/sync"
)

type syncOpts struct {
	dir        string
	statusMode bool
	jsonMode   bool
}

func newSyncFlagSet(defaultDir string) (*flag.FlagSet, *syncOpts) {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	opts := &syncOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.BoolVar(&opts.statusMode, "status", false, "Report unpushed ops count without network transport")
	fs.BoolVar(&opts.jsonMode, "json", false, "Output result as JSON")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"sync"}, syncCmd)
	}
	return fs, opts
}

func runSync(ctx context.Context, defaultDir string, args []string, stdout, stderr io.Writer) int {
	fs, opts := newSyncFlagSet(defaultDir)
	fs.SetOutput(stderr)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	targetDir := opts.dir
	if targetDir == "" {
		targetDir = "."
	}

	store, err := writ.Open(targetDir)
	if err != nil {
		porcelainf(stderr, "writ sync: %v\n", err)
		return 5
	}
	defer store.Close()

	if store.Writer().ID == "" {
		porcelainln(stderr, "warning: no writer identity configured (run 'writ init' to configure)")
	}

	remotes := fs.Args()
	if len(remotes) == 0 {
		configuredRemotes, err := listGitRemotes(ctx, targetDir)
		if err != nil {
			porcelainf(stderr, "writ sync: list remotes: %v\n", err)
			return 1
		}
		hasOrigin := false
		for _, r := range configuredRemotes {
			if r == "origin" {
				hasOrigin = true
				break
			}
		}
		if hasOrigin {
			remotes = []string{"origin"}
		} else if len(configuredRemotes) == 1 {
			remotes = []string{configuredRemotes[0]}
		} else if len(configuredRemotes) == 0 {
			porcelainln(stderr, "writ sync: no remotes configured; specify a remote or add one with 'git remote add'")
			return 2
		} else {
			porcelainln(stderr, "writ sync: multiple remotes configured but none named 'origin'; specify a remote explicitly")
			return 2
		}
	}

	var firstExitCode int
	var syncStatuses []wire.SyncStatus
	var syncResults []wire.SyncResult

	for _, remote := range remotes {
		if opts.statusMode {
			status, err := store.SyncStatus(ctx, remote)
			if err != nil {
				code := exitCodeFor(err)
				if firstExitCode == 0 {
					firstExitCode = code
				}
				if opts.jsonMode {
					syncStatuses = append(syncStatuses, wire.FromSyncStatusFailure(remote, err, status.Unsynced))
				} else {
					printSyncError(stderr, remote, err)
				}
				continue
			}
			if opts.jsonMode {
				syncStatuses = append(syncStatuses, wire.FromSyncStatus(status))
			} else {
				porcelainln(stdout, formatSyncStatus(remote, status))
			}
		} else {
			res, err := store.Sync(ctx, remote)
			if err != nil {
				code := exitCodeFor(err)
				if firstExitCode == 0 {
					firstExitCode = code
				}
				if opts.jsonMode {
					syncResults = append(syncResults, wire.FromSyncResultFailure(remote, res, err))
				} else {
					printSyncError(stderr, remote, err)
				}
				continue
			}
			if opts.jsonMode {
				syncResults = append(syncResults, wire.FromSyncResult(remote, res))
			} else {
				porcelainln(stdout, formatSyncResult(remote, res))
			}
		}
	}

	if opts.jsonMode {
		if opts.statusMode {
			if syncStatuses == nil {
				syncStatuses = []wire.SyncStatus{}
			}
			if err := emitJSON(stdout, wire.KindSyncStatus, syncStatuses); err != nil {
				porcelainf(stderr, "writ sync: marshal json: %v\n", err)
				return 1
			}
		} else {
			if syncResults == nil {
				syncResults = []wire.SyncResult{}
			}
			if err := emitJSON(stdout, wire.KindSyncResult, syncResults); err != nil {
				porcelainf(stderr, "writ sync: marshal json: %v\n", err)
				return 1
			}
		}
	}

	return firstExitCode
}

func listGitRemotes(ctx context.Context, dir string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "remote")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var remotes []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" {
			remotes = append(remotes, trimmed)
		}
	}
	return remotes, nil
}

func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, writ.ErrAuth) || errors.Is(err, sync.ErrAuth) {
		return 6
	}
	if errors.Is(err, writ.ErrNetwork) || errors.Is(err, sync.ErrNetwork) {
		return 7
	}
	if errors.Is(err, writ.ErrUnknownRemote) || errors.Is(err, sync.ErrUnknownRemote) {
		return 3
	}
	if errors.Is(err, writ.ErrNonFastForward) || errors.Is(err, sync.ErrNonFastForward) {
		return 4
	}

	var syncErr *writ.SyncError
	if errors.As(err, &syncErr) {
		switch syncErr.Kind {
		case string(sync.FailureKindAuth):
			return 6
		case string(sync.FailureKindNetwork):
			return 7
		case string(sync.FailureKindNotFound):
			return 3
		case string(sync.FailureKindRejected):
			if errors.Is(syncErr.Err, writ.ErrNonFastForward) || errors.Is(syncErr.Err, sync.ErrNonFastForward) {
				return 4
			}
			return 1
		}
	}

	var gitErr *sync.GitError
	if errors.As(err, &gitErr) {
		switch gitErr.Kind {
		case sync.FailureKindAuth:
			return 6
		case sync.FailureKindNetwork:
			return 7
		case sync.FailureKindNotFound:
			return 3
		case sync.FailureKindRejected:
			if errors.Is(gitErr.Err, writ.ErrNonFastForward) || errors.Is(gitErr.Err, sync.ErrNonFastForward) {
				return 4
			}
			return 1
		}
	}

	if errors.Is(err, writ.ErrStoreOpen) || errors.Is(err, writ.ErrNotRepository) {
		return 5
	}
	return 1
}

func printSyncError(stderr io.Writer, remote string, err error) {
	var syncErr *writ.SyncError
	if errors.As(err, &syncErr) {
		porcelainf(stderr, "writ sync: %s: %s: %s\n", remote, syncErr.Kind, syncErr.Message)
		if syncErr.Advice != "" {
			porcelainf(stderr, "  advice: %s\n", syncErr.Advice)
		}
		if syncErr.Unsynced > 0 {
			porcelainf(stderr, "  %d %s unsynced\n", syncErr.Unsynced, plural(syncErr.Unsynced, "op", "ops"))
		}
		return
	}

	var gitErr *sync.GitError
	if errors.As(err, &gitErr) {
		msg := gitErr.Stderr
		if msg == "" && gitErr.Err != nil {
			msg = gitErr.Err.Error()
		}
		porcelainf(stderr, "writ sync: %s: %s: %s\n", remote, gitErr.Kind, msg)
		if gitErr.Advice != "" {
			porcelainf(stderr, "  advice: %s\n", gitErr.Advice)
		}
		return
	}

	porcelainf(stderr, "writ sync: %s: %v\n", remote, err)
}

func plural(n int, singular, pluralStr string) string {
	if n == 1 {
		return singular
	}
	return pluralStr
}

func formatSyncResult(remote string, res writ.SyncResult) string {
	if res.OpsFetched == 0 && res.OpsPushed == 0 && res.ObjectsTouched == 0 && res.Unsynced == 0 {
		return fmt.Sprintf("%s: up to date", remote)
	}

	var parts []string
	if res.OpsFetched > 0 {
		parts = append(parts, fmt.Sprintf("fetched %d %s", res.OpsFetched, plural(res.OpsFetched, "op", "ops")))
	}
	if res.OpsPushed > 0 {
		parts = append(parts, fmt.Sprintf("pushed %d %s", res.OpsPushed, plural(res.OpsPushed, "op", "ops")))
	}
	if res.ObjectsTouched > 0 {
		parts = append(parts, fmt.Sprintf("%d %s updated", res.ObjectsTouched, plural(res.ObjectsTouched, "object", "objects")))
	}
	if res.Unsynced > 0 {
		parts = append(parts, fmt.Sprintf("%d %s unsynced", res.Unsynced, plural(res.Unsynced, "op", "ops")))
	}

	if len(parts) == 0 {
		return fmt.Sprintf("%s: up to date", remote)
	}
	return fmt.Sprintf("%s: %s", remote, strings.Join(parts, ", "))
}

func formatSyncStatus(remote string, status writ.SyncStatus) string {
	if status.Unsynced == 0 {
		return fmt.Sprintf("%s: up to date", remote)
	}
	return fmt.Sprintf("%s: %d %s unsynced", remote, status.Unsynced, plural(status.Unsynced, "op", "ops"))
}
