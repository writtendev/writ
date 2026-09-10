// Command writ is the porcelain CLI for humans, with --json output for
// scripts and agents. See ARCHITECTURE.md and VISION.md for the shape it
// is expected to grow into.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/writtendev/writ/internal/version"
)

func main() {
	ctx := context.Background()
	// Interactivity needs both ends of the prompt exchange: a prompt is
	// written to stderr and read back from stdin, so a char-device stdin
	// with a redirected stderr (e.g. `writ init 2>init.log` on a terminal)
	// would otherwise decide "interactive" and then block on a prompt the
	// user never sees — an apparent hang (WRIT-220 review round 1).
	interactive := isCharDevice(os.Stdin) && isCharDevice(os.Stderr)
	os.Exit(runStdin(ctx, os.Args[1:], os.Stdin, interactive, os.Stdout, os.Stderr))
}

// isCharDevice reports whether f is a character device — a real terminal,
// as opposed to a pipe, a redirected file, or /dev/null. It is the
// standard-library check for "is this interactive": no dependency beyond
// os itself, so mattn/go-isatty (present only as an indirect dependency of
// something else in the module graph) stays indirect rather than being
// promoted for this. A Stat error (should not happen for os.Stdin) is
// treated as non-interactive, the safe default for a check that exists to
// avoid prompting somewhere nothing can read the prompt.
func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// run is the entry point every existing call site (hundreds of tests, and
// any future non-interactive caller) uses: no stdin, not interactive. It
// exists so that adding a prompt to one command (`writ init`'s namespace,
// WRIT-220) does not require threading a stdin reader through a command
// tree and a test suite that mostly never needs one.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runStdin(ctx, args, strings.NewReader(""), false, stdout, stderr)
}

func runStdin(ctx context.Context, args []string, stdin io.Reader, interactive bool, stdout, stderr io.Writer) int {
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
		return runInit(ctx, defaultDir, args[1:], stdin, interactive, stdout, stderr)
	case "object":
		return runObject(ctx, defaultDir, args[1:], stdout, stderr)
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
