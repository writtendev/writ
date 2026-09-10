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

	"golang.org/x/term"

	"github.com/writtendev/writ/internal/version"
)

func main() {
	ctx := context.Background()
	// Interactivity needs both ends of the prompt exchange: a prompt is
	// written to stderr and read back from stdin, so a terminal stdin with
	// a redirected stderr (e.g. `writ init 2>init.log` on a terminal) would
	// otherwise decide "interactive" and then block on a prompt the user
	// never sees — an apparent hang (WRIT-220 review round 1).
	interactive := isTerminal(os.Stdin) && isTerminal(os.Stderr)
	os.Exit(runStdin(ctx, os.Args[1:], os.Stdin, interactive, os.Stdout, os.Stderr))
}

// isTerminal reports whether f is a real interactive terminal, as opposed
// to a pipe, a redirected regular file, or /dev/null. /dev/null is itself
// a character device (crw-rw-rw-), so a check that only asks "is this a
// character device" (os.ModeCharDevice) cannot tell it apart from a real
// terminal: `writ init >/dev/null 2>&1` run from a terminal would still
// classify as interactive, write the prompt into the void, and then block
// forever reading an answer nobody can see (WRIT-220 review round 2).
// Correctly answering "is this a terminal" requires an ioctl
// (TIOCGWINSZ/TCGETS) the standard library does not expose, so this uses
// golang.org/x/term's IsTerminal rather than os.ModeCharDevice, or
// os.SameFile against a stat of os.DevNull — the latter would fix only the
// /dev/null case and leave every other non-tty character device
// misclassified. golang.org/x/sys, term's only dependency, is already an
// indirect requirement of this module, and golang.org/x/text and
// golang.org/x/crypto are already direct ones, so this promotes an
// existing transitive dependency rather than adding a new one;
// mattn/go-isatty, present only as an indirect dependency of something
// else in the module graph, stays indirect rather than becoming a second
// answer to the same question.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
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
