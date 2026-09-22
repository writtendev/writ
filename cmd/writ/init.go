package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/internal/schemasrc"
)

type initOpts struct {
	dir       string
	namespace string
}

func newInitFlagSet(defaultDir string) (*flag.FlagSet, *initOpts) {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	opts := &initOpts{}
	fs.StringVar(&opts.dir, "C", defaultDir, "Run as if writ was started in `<dir>`")
	fs.StringVar(&opts.namespace, "namespace", "", "Namespace `<name>` for a starter writ.schema (required the first time one is written)")
	fs.Usage = func() {
		renderUsage(fs.Output(), []string{"init"}, initCmd)
	}
	return fs, opts
}

// namespaceGrammar is restated in every namespace refusal below, so the
// answer to "what would satisfy this" never depends on which of the three
// paths (flag, prompt, or neither) produced the refusal.
const namespaceGrammar = "^[a-z][a-z0-9-]*$, at most 64 characters, not a writ.schema reserved word"

// resolveNamespace decides the namespace for the starter writ.schema this
// run is about to write. It is called only once writ.Init has reported one
// is due and no --namespace flag supplied one
// (writ.ErrStarterNamespaceRequired) -- which writ.Init only returns
// before its own first git-config write, so a refusal here leaves the
// repository exactly as writ.Init already left it: untouched.
//
// writ init never derives a namespace from anything: a bare directory name
// is not a public package name anyone chose, and since WRIT-199 and
// WRIT-217 the namespace is exactly that -- baked into the schema object's
// id and into every wire type the schema declares, permanently.
//
//   - interactive: prompted once on stderr with no suggested default; an
//     empty answer, EOF, or an invalid answer refuses immediately, with no
//     retry loop.
//   - non-interactive: refused outright, naming --namespace. This is the
//     failure WRIT-220 exists to produce instead of a silent default.
func resolveNamespace(stdin io.Reader, interactive bool, stderr io.Writer) (string, error) {
	if !interactive {
		return "", fmt.Errorf("writ.schema does not exist yet and no --namespace was given; pass --namespace <name>, matching %s", namespaceGrammar)
	}

	fmt.Fprint(stderr, "This repository has no writ.schema yet. Namespace for the starter file: ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if line == "" && errors.Is(err, io.EOF) {
		// interactive is isTerminal(stdin) && isTerminal(stderr)
		// (cmd/writ/main.go), so every non-terminal stdin -- a pipe,
		// /dev/null, or whatever cron, systemd, `docker run` without
		// `-i`, and GitHub Actions `run:` steps attach -- already took
		// the !interactive branch above and never reaches this prompt at
		// all. The only way to land here with line == "" and an
		// immediate EOF is a human pressing Ctrl-D at the prompt above on
		// a real terminal: input ended without the human answering. That
		// is a different fact from the human pressing enter on an empty
		// line (err == nil, line == "\n", handled below), so it gets the
		// same message a non-interactive run would have produced instead
		// of "no namespace entered", which would blame a human who did
		// answer -- by declining.
		//
		// The prompt above deliberately has no trailing newline (the
		// answer is meant to be typed right after it), and Ctrl-D is the
		// only refusal path that returns with nothing having echoed one
		// (the empty-answer and invalid-answer paths below both follow a
		// human's own Return keypress) -- so without this Fprintln, a
		// Ctrl-D would run the prompt and this refusal together on one
		// line (WRIT-220 review round 2).
		fmt.Fprintln(stderr)
		return "", fmt.Errorf("writ.schema does not exist yet and no --namespace was given; pass --namespace <name>, matching %s", namespaceGrammar)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", fmt.Errorf("no namespace entered; pass --namespace <name>, matching %s", namespaceGrammar)
	}
	if err := schemasrc.ValidateNamespace(line); err != nil {
		return "", fmt.Errorf("%w (must match %s)", err, namespaceGrammar)
	}
	return line, nil
}

// initMessage renders err for writ init's own output. A *writ.ConfigError
// signs its message with "(run 'writ init' to configure)", which is the
// right advice from every other command and the wrong advice from this
// one: it tells the reader to run what they are already running, and
// implies init failed at something it never attempts. writ does not write
// signing configuration for anyone -- it prints the git config lines and
// expects the user to run them, which is what the reader sees directly
// below each of these warnings.
func initMessage(err error) string {
	var cfgErr *writ.ConfigError
	if errors.As(err, &cfgErr) {
		return cfgErr.Message()
	}
	return err.Error()
}

// reportPartialInit names the state a failed run leaves the repository in.
//
// Everything that can be hoisted ahead of the first write has been, but git
// config has no transaction and the refspec writes are still real writes that
// can fail one remote in. There is nothing to roll back to, so the honest
// alternative is to say which half stuck rather than let a non-zero exit imply
// the repository is untouched: identity in config, refspec absent, is exactly
// the state that reads as clean and is not.
//
// Naming writ init as the remedy here is not the circular advice this command
// stopped giving: that told a reader init had failed to configure signing,
// which init never attempts. This is a run that genuinely stopped half-way,
// and re-running genuinely finishes it. Re-running is also safe, which is the
// part worth stating out loud -- writ.Init reuses whatever identity is
// already in config, so a second run never mints a second writer-id for this
// device. That would split one device's ops across two ref namespaces.
func reportPartialInit(stderr io.Writer, writerID, repoID string, done, pending []string) {
	fmt.Fprintf(stderr, "writ init: stopped part-way; the repository is half-configured\n")
	fmt.Fprintf(stderr, "  in git config now: writ.writerId %s, writ.repoId %s\n", writerID, repoID)
	if len(done) > 0 {
		fmt.Fprintf(stderr, "  fetch refspec configured for: %s\n", strings.Join(done, ", "))
	}
	if len(pending) > 0 {
		fmt.Fprintf(stderr, "  fetch refspec NOT configured for: %s\n", strings.Join(pending, ", "))
	}
	fmt.Fprintf(stderr, "  re-run writ init after fixing the error above: it reuses both IDs and writes only what is missing\n")
}

// reportSkippedRemotes names the discovered remotes writ init could not
// configure, at the end of a run that otherwise finished: unlike
// reportPartialInit, nothing here stopped -- every other remote got its
// fetch refspec, and identity and the starter schema file (if due) ran to
// completion. This is what runInit prints instead of aborting when a
// discovered remote's existence/name gate rejects it (a url-less section,
// or a "-"-leading name) rather than one the caller asked for by name. A
// remote writ merely discovered being unusable is reported here, not
// remedied: writ was never asked to touch it, so there is no advice to
// give about it.
func reportSkippedRemotes(stderr io.Writer, configured, skippedReasons []string) {
	fmt.Fprintf(stderr, "writ init: %d of %d discovered remote(s) could not be configured\n", len(skippedReasons), len(configured)+len(skippedReasons))
	if len(configured) > 0 {
		fmt.Fprintf(stderr, "  fetch refspec configured for: %s\n", strings.Join(configured, ", "))
	}
	fmt.Fprintf(stderr, "  fetch refspec NOT configured for: %s\n", strings.Join(skippedReasons, "; "))
}

func runInit(ctx context.Context, defaultDir string, args []string, stdin io.Reader, interactive bool, stdout, stderr io.Writer) int {
	fs, opts := newInitFlagSet(defaultDir)
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

	// A --namespace flag is validated against the exact grammar a
	// `namespace` declaration enforces at parse time as soon as it is
	// given, before writ.Init is even called: an invalid flag should
	// never reach it. Validating unconditionally -- rather than only once
	// a starter file turns out to be due -- is a small, deliberate
	// widening: it costs nothing when a starter file is due, and catches a
	// typo immediately rather than only on a repository that happens to
	// have none yet.
	namespace := ""
	if opts.namespace != "" {
		if err := schemasrc.ValidateNamespace(opts.namespace); err != nil {
			fmt.Fprintf(stderr, "writ init: --namespace: %v (must match %s)\n", err, namespaceGrammar)
			return 1
		}
		namespace = opts.namespace
	}

	// explicit remotes (positional args) keep the original abort-on-first-
	// error behaviour; nil tells writ.Init to discover them via `git
	// remote`, which is also what selects lenient treatment of an unusable
	// one (WRIT-283) -- see writ.InitOptions.Remotes.
	var remotes []string
	if fs.NArg() > 0 {
		remotes = fs.Args()
	}

	result, err := writ.Init(ctx, targetDir, writ.InitOptions{
		Remotes:          remotes,
		StarterNamespace: namespace,
	})
	if errors.Is(err, writ.ErrStarterNamespaceRequired) {
		ns, nsErr := resolveNamespace(stdin, interactive, stderr)
		if nsErr != nil {
			fmt.Fprintf(stderr, "writ init: %v\n", nsErr)
			return 1
		}
		result, err = writ.Init(ctx, targetDir, writ.InitOptions{
			Remotes:          remotes,
			StarterNamespace: ns,
		})
	}

	renderInitResult(stdout, stderr, opts.namespace, result)

	if err != nil {
		fmt.Fprintf(stderr, "writ init: %v\n", err)
		// A hard remote failure is the one case with real partial state to
		// report: identity already in config, one or more refspecs not.
		// Every other failure kind returns before writ.Init attempts any
		// remote, so result.Remotes is empty for them -- a non-empty
		// Remotes here means exactly the remote-failure case. pending
		// includes both the remote that failed and every remote after it
		// writ.Init never got to (RemoteInit.NotAttempted): main named all
		// of them, and reconstructing that list is exactly what
		// NotAttempted exists for.
		if len(result.Remotes) > 0 {
			var configured, pending []string
			for _, r := range result.Remotes {
				if r.Err == nil && !r.NotAttempted {
					configured = append(configured, r.Name)
				} else {
					pending = append(pending, r.Name)
				}
			}
			reportPartialInit(stderr, result.WriterID, result.RepoID, configured, pending)
		}
		return 1
	}

	if len(result.Remotes) == 0 {
		fmt.Fprintln(stdout, "No git remotes configured; fetch refspec will be added when a remote is configured.")
	}
	var configured, skippedReasons []string
	for _, r := range result.Remotes {
		if r.Skipped {
			skippedReasons = append(skippedReasons, fmt.Sprintf("%s (%v)", r.Name, r.Err))
		} else {
			configured = append(configured, r.Name)
		}
	}
	if len(skippedReasons) > 0 {
		reportSkippedRemotes(stderr, configured, skippedReasons)
	}

	// The starter writ.schema outcome renders last, after the remote
	// summary above -- matching main's step 6 (remotes) then step 7
	// (starter schema) ordering. writ.Init only reaches that step on
	// success (a hard remote failure returns before it), so this is a
	// no-op in every path that already returned above.
	renderStarterSchemaOutcome(stdout, stderr, opts.namespace, result)

	return 0
}

// renderInitResult prints writ.Init's result exactly as runInit's own
// former inline steps did, in the same order: writer id, repo id, person
// id, signing key, then one line per remote. It runs regardless of whether
// Init succeeded, since a partial result is exactly what a partial-failure
// report needs to name. The starter writ.schema outcome is rendered
// separately, by renderStarterSchemaOutcome, so that runInit can place it
// after the "no remotes configured" / skipped-remote summary lines it
// prints itself -- main wrote the starter file only after the remote step
// had already printed those, and this keeps that order.
// namespaceFlag is the raw --namespace flag value (possibly empty), used
// only to report that it was ignored when writ.schema already existed.
func renderInitResult(stdout, stderr io.Writer, namespaceFlag string, result writ.InitResult) {
	if result.WriterID != "" {
		if result.WriterIDMinted {
			fmt.Fprintf(stdout, "Writer ID: %s (minted)\n", result.WriterID)
		} else {
			fmt.Fprintf(stdout, "Writer ID: %s (already configured)\n", result.WriterID)
		}
	}

	if result.RepoID != "" {
		if result.RepoIDMinted {
			fmt.Fprintf(stdout, "Repo ID: %s (minted)\n", result.RepoID)
		} else {
			fmt.Fprintf(stdout, "Repo ID: %s (already configured)\n", result.RepoID)
		}
	}

	switch {
	case result.PersonIDErr != nil:
		fmt.Fprintf(stderr, "warning: no person identifier: %s\n", initMessage(result.PersonIDErr))
		fmt.Fprintf(stderr, "Configure one of:\n")
		fmt.Fprintf(stderr, "  git config %s user:<handle>\n", writ.PersonIDKey)
		fmt.Fprintf(stderr, "  git config user.email <address>\n")
	case result.PersonIDFromKey:
		fmt.Fprintf(stdout, "Person ID: %s (from %s)\n", result.PersonID, writ.PersonIDKey)
	case result.PersonID != "":
		fmt.Fprintf(stdout, "Person ID: %s (derived from user.email)\n", result.PersonID)
	}

	renderIdentityState(stdout, stderr, result)

	for _, r := range result.Remotes {
		switch {
		case r.NotAttempted:
			// writ.Init never reached this remote -- an earlier one's hard
			// failure stopped the run first. Nothing to say about it here;
			// runInit's reportPartialInit names it among what is not
			// configured.
		case r.Err == nil:
			if r.Repaired {
				fmt.Fprintf(stdout, "Configured fetch refspec for remote %q (%s)\n", r.Name, r.Refspec)
			} else {
				fmt.Fprintf(stdout, "Fetch refspec for remote %q is already configured (%s)\n", r.Name, r.Refspec)
			}
		case r.Skipped:
			fmt.Fprintf(stderr, "writ init: remote %q: %v (skipped; not one you asked for)\n", r.Name, r.Err)
		default:
			// The one hard-failing remote, if any, is always the entry
			// right before the first NotAttempted one (writ.Init returns
			// as soon as it hits one) and is reported by runInit itself
			// via the returned error, not here -- printing it here too
			// would duplicate the line.
		}
	}
}

// renderStarterSchemaOutcome prints the starter writ.schema outcome from
// writ.Init's result: written, already existed (naming a discarded
// --namespace flag if one was given), or failed. Split out of
// renderInitResult so runInit can call it after the remote summary lines
// it prints itself, matching the order writ.Init's own steps run in --
// remotes (step 6) before the starter schema (step 7).
func renderStarterSchemaOutcome(stdout, stderr io.Writer, namespaceFlag string, result writ.InitResult) {
	switch {
	case result.StarterSchemaWritten:
		fmt.Fprintf(stdout, "Wrote starter %s\n", result.StarterSchemaPath)
	case result.StarterSchemaExisted:
		if namespaceFlag != "" {
			fmt.Fprintf(stdout, "writ.schema already exists; leaving it unchanged (--namespace %q ignored)\n", namespaceFlag)
		} else {
			fmt.Fprintf(stdout, "writ.schema already exists; leaving it unchanged\n")
		}
	case result.StarterSchemaErr != nil:
		fmt.Fprintf(stderr, "writ init: writ.schema: %v\n", result.StarterSchemaErr)
	}
}

// renderIdentityState prints writ.Init's signing-identity result exactly as
// runInit's own former step did: a missing key gets the remediation for
// that key, and gpg.format/user.signingKey are kept separate from
// user.name/user.email so a repository missing only one is not shown the
// other's advice.
//
// result.IdentityErr's zero value (nil) is ambiguous on its own: it means
// both "identity loaded cleanly" and "writ.Init never reached the identity
// step at all". Every early return in writ.Init before step 6 --
// resolveRepoRoot, gitdir.Resolve/OpenStorage, the writ.schema due-check,
// discoverRemotes, EnsureWriterID, EnsureRepoID -- leaves IdentityErr nil
// and SigningKey "" without step 6 (identity.Load) ever having run.
// Rendering that pair unconditionally printed a fabricated
// "Signing key:  (ssh)" for every one of those failures, not just the
// non-repo case that surfaced it (round-3 finding).
//
// result.RepoID is what disambiguates the two: writ.Init sets it in step 4,
// immediately before steps 5 and 6 run unconditionally to completion --
// neither can fail the call, so whatever they determine lands on
// PersonIDErr/IdentityErr rather than on a return -- and every early return
// before step 4 finishes leaves RepoID "". So RepoID != "" is true exactly
// when step 6 ran, which is what makes IdentityErr's nil trustworthy.
// WriterID alone would not do: EnsureWriterID can succeed and set WriterID
// while the very next call, EnsureRepoID, fails and returns before RepoID
// is set -- and before step 6 ever runs.
func renderIdentityState(stdout, stderr io.Writer, result writ.InitResult) {
	if result.RepoID == "" {
		// writ.Init never reached the identity step: nothing was
		// determined, so there is nothing honest to print.
		return
	}

	err := result.IdentityErr
	if err == nil {
		if result.SigningKeyLiteral {
			fmt.Fprintf(stdout, "Signing key: key::%s (ssh)\n", result.SigningKey)
		} else {
			fmt.Fprintf(stdout, "Signing key: %s (ssh)\n", result.SigningKey)
		}
		return
	}

	var cfgErr *writ.ConfigError
	if !errors.As(err, &cfgErr) {
		fmt.Fprintf(stderr, "warning: identity configuration: %s\n", initMessage(err))
		return
	}
	if !errors.Is(cfgErr.Problem, writ.ErrMissingConfig) && !errors.Is(cfgErr.Problem, writ.ErrUnsupportedFormat) && !errors.Is(cfgErr.Problem, writ.ErrInvalidConfig) {
		fmt.Fprintf(stderr, "warning: identity configuration: %s\n", initMessage(err))
		return
	}
	switch cfgErr.Key {
	case "gpg.format", "user.signingKey":
		fmt.Fprintf(stderr, "warning: SSH signing key not fully configured (%s)\n", initMessage(err))
		fmt.Fprintf(stderr, "To configure SSH signing for Writ and Git:\n")
		fmt.Fprintf(stderr, "  git config gpg.format ssh\n")
		fmt.Fprintf(stderr, "  git config user.signingKey ~/.ssh/id_ed25519.pub\n")
		fmt.Fprintf(stderr, "Optionally configure verification allowed signers:\n")
		fmt.Fprintf(stderr, "  git config gpg.ssh.allowedSignersFile ~/.ssh/allowed_signers\n")
	case "user.name", "user.email":
		fmt.Fprintf(stderr, "warning: author identity not fully configured (%s)\n", initMessage(err))
		fmt.Fprintf(stderr, "To configure the identity Writ and Git author commits with:\n")
		fmt.Fprintf(stderr, "  git config user.name \"Your Name\"\n")
		fmt.Fprintf(stderr, "  git config user.email you@example.com\n")
	default:
		fmt.Fprintf(stderr, "warning: identity configuration: %s\n", initMessage(err))
	}
}
