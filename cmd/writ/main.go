// Command writ is the porcelain CLI for humans, with --json output for
// scripts and agents. See ARCHITECTURE.md and VISION.md for the shape it
// is expected to grow into.
package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/writtendev/writ/internal/version"
)

func main() {
	ctx := context.Background()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		renderUsage(stderr, nil, rootCommand)
		return 2
	}

	// Handle help flags at root
	switch args[0] {
	case "-h", "-help", "--help":
		renderUsage(stdout, nil, rootCommand)
		return 0
	}

	// Support root -C flag, e.g. "writ -C <dir> init"
	var defaultDir string
	if args[0] == "-C" {
		if len(args) < 2 {
			fmt.Fprintln(stderr, "writ: option -C requires an argument")
			return 2
		}
		defaultDir = args[1]
		args = args[2:]
		if len(args) == 0 {
			renderUsage(stderr, nil, rootCommand)
			return 2
		}
	}

	switch args[0] {
	case "-h", "-help", "--help":
		renderUsage(stdout, nil, rootCommand)
		return 0
	case "help":
		return runHelp(args[1:], stdout, stderr)
	case "completion":
		return runCompletion(args[1:], stdout, stderr)
	case "init":
		return runInit(ctx, defaultDir, args[1:], stdout, stderr)
	case "comment":
		return runComment(ctx, defaultDir, args[1:], stdout, stderr)
	case "doc":
		return runDoc(ctx, defaultDir, args[1:], stdout, stderr)
	case "issue":
		return runIssue(ctx, defaultDir, args[1:], stdout, stderr)
	case "object":
		return runObject(ctx, defaultDir, args[1:], stdout, stderr)
	case "review":
		return runReview(ctx, defaultDir, args[1:], stdout, stderr)
	case "state":
		return runState(ctx, defaultDir, args[1:], stdout, stderr)
	case "label":
		return runLabel(ctx, defaultDir, args[1:], stdout, stderr)
	case "settings":
		return runSettings(ctx, defaultDir, args[1:], stdout, stderr)
	case "schema":
		return runSchema(ctx, defaultDir, args[1:], stdout, stderr)
	case "sync":
		return runSync(ctx, defaultDir, args[1:], stdout, stderr)
	case "version":
		if len(args) > 1 {
			switch args[1] {
			case "-h", "-help", "--help":
				renderUsage(stdout, []string{"version"}, versionCmd)
				return 0
			}
		}
		fmt.Fprintf(stdout, "writ %s\n", version.Version)
		return 0
	default:
		fmt.Fprintf(stderr, "writ: unknown command %q\n\n", args[0])
		renderUsage(stderr, nil, rootCommand)
		return 2
	}
}
