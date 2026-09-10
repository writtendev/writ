package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/schemasrc"
	"github.com/writtendev/writ/engine/sync"
	"github.com/writtendev/writ/internal/gitdir"
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
// run is about to write. It is called only when the caller has determined
// one is actually due — a work tree with no writ.schema yet — and it must
// be called before EnsureWriterID's first git-config write, so that a
// refusal here leaves the repository untouched rather than
// half-configured (dispatch decision on WRIT-220's plan).
//
// writ init never derives a namespace from anything: a bare directory
// name is not a public package name anyone chose, and since WRIT-199 and
// WRIT-217 the namespace is exactly that — baked into the schema object's
// id and into every wire type the schema declares, permanently.
//
//   - flagValue supplied: validated, or refused naming the flag.
//   - flagValue absent, interactive: prompted once on stderr with no
//     suggested default; an empty answer, EOF, or an invalid answer
//     refuses immediately, with no retry loop.
//   - flagValue absent, non-interactive: refused outright, naming
//     --namespace. This is the failure WRIT-220 exists to produce instead
//     of a silent default.
func resolveNamespace(flagValue string, stdin io.Reader, interactive bool, stderr io.Writer) (string, error) {
	if flagValue != "" {
		if err := schemasrc.ValidateNamespace(flagValue); err != nil {
			return "", fmt.Errorf("--namespace: %w (must match %s)", err, namespaceGrammar)
		}
		return flagValue, nil
	}

	if !interactive {
		return "", fmt.Errorf("writ.schema does not exist yet and no --namespace was given; pass --namespace <name>, matching %s", namespaceGrammar)
	}

	fmt.Fprint(stderr, "This repository has no writ.schema yet. Namespace for the starter file: ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if line == "" && errors.Is(err, io.EOF) {
		// interactive is isTerminal(stdin) && isTerminal(stderr)
		// (cmd/writ/main.go), so every non-terminal stdin — a pipe,
		// /dev/null, or whatever cron, systemd, `docker run` without
		// `-i`, and GitHub Actions `run:` steps attach — already took the
		// !interactive branch above and never reaches this prompt at all.
		// The only way to land here with line == "" and an immediate EOF
		// is a human pressing Ctrl-D at the prompt above on a real
		// terminal: input ended without the human answering. That is a
		// different fact from the human pressing enter on an empty line
		// (err == nil, line == "\n", handled below), so it gets the same
		// message a non-interactive run would have produced instead of
		// "no namespace entered", which would blame a human who did
		// answer — by declining.
		//
		// The prompt above deliberately has no trailing newline (the
		// answer is meant to be typed right after it), and Ctrl-D is the
		// only refusal path that returns with nothing having echoed one
		// (the empty-answer and invalid-answer paths below both follow a
		// human's own Return keypress) — so without this Fprintln, a
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

// initMessage renders err for writ init's own output. An identity.ConfigError
// signs its message with "(run 'writ init' to configure)", which is the right
// advice from every other command and the wrong advice from this one: it tells
// the reader to run what they are already running, and implies init failed at
// something it never attempts. writ does not write signing configuration for
// anyone — it prints the git config lines and expects the user to run them,
// which is what the reader sees directly below each of these warnings.
func initMessage(err error) string {
	var cfgErr *identity.ConfigError
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
// part worth stating out loud — EnsureWriterID and EnsureRepoID reuse what is
// already in config, so a second run never mints a second writer-id for this
// device. That would split one device's ops across two ref namespaces.
func reportPartialInit(stderr io.Writer, writerID identity.WriterID, repoID identity.RepoID, done, pending []string) {
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

	// 1. Resolve repo root via git rev-parse
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = targetDir
	out, err := cmd.Output()
	var repoRoot string
	if err == nil {
		repoRoot = strings.TrimSpace(string(out))
	} else {
		// Check if target directory is a bare repository
		cmdBare := exec.CommandContext(ctx, "git", "rev-parse", "--is-bare-repository")
		cmdBare.Dir = targetDir
		outBare, errBare := cmdBare.Output()
		if errBare == nil && strings.TrimSpace(string(outBare)) == "true" {
			cmdGitDir := exec.CommandContext(ctx, "git", "rev-parse", "--absolute-git-dir")
			cmdGitDir.Dir = targetDir
			outGitDir, errGitDir := cmdGitDir.Output()
			if errGitDir == nil {
				repoRoot = strings.TrimSpace(string(outGitDir))
			}
		}
	}

	if repoRoot == "" {
		fmt.Fprintf(stderr, "writ init: not a git repository (or any of the parent directories)\n")
		return 1
	}

	// 2. Open the repository, and work out which remotes this run is for.
	//
	// Both happen before anything is written. git config has no transaction,
	// so the only defence against a half-configured repository is to do the
	// steps that can fail while there is still nothing to undo. Opening the
	// repository is a precondition, not a later step: one writ cannot open is
	// one writ init cannot finish. This open used to happen at the end, inside
	// sync.Open, which is exactly where WRIT-93's extensions.worktreeConfig
	// failure landed — after both IDs were already persisted.
	gitInfo, err := gitdir.Resolve(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "writ init: %v\n", err)
		return 1
	}
	storer, err := gitdir.OpenStorage(gitInfo)
	if err != nil {
		fmt.Fprintf(stderr, "writ init: %v\n", err)
		return 1
	}

	// 2.5. Decide whether a starter writ.schema is due, and if so, resolve
	// its namespace now — before anything below writes to git config, so
	// that a refusal leaves the repository untouched rather than
	// half-configured. Only a work tree with no writ.schema yet ever
	// writes one (step 7): a bare repository has no working tree to put
	// one in, and an already-initialized repository has nothing left for
	// a namespace to name. Neither needs one, and neither is asked for
	// one non-interactively (dispatch decision on WRIT-220's plan).
	var starterNamespace string
	starterFileDue := false
	if gitInfo.WorkTree != "" {
		switch _, statErr := os.Stat(filepath.Join(gitInfo.WorkTree, schemaSourceFileName)); {
		case statErr == nil:
			// Exists; step 7 reports this and, if --namespace was passed
			// anyway, that it was ignored.
		case os.IsNotExist(statErr):
			starterFileDue = true
		default:
			fmt.Fprintf(stderr, "writ init: writ.schema: %v\n", statErr)
			return 1
		}
	}
	if starterFileDue {
		ns, err := resolveNamespace(opts.namespace, stdin, interactive, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "writ init: %v\n", err)
			return 1
		}
		starterNamespace = ns
	}

	remotes := fs.Args()
	if len(remotes) == 0 {
		cmdRemote := exec.CommandContext(ctx, "git", "remote")
		cmdRemote.Dir = repoRoot
		outRemote, err := cmdRemote.Output()
		if err != nil {
			fmt.Fprintf(stderr, "writ init: list remotes: %v\n", err)
			return 1
		}
		for _, line := range strings.Split(strings.TrimSpace(string(outRemote)), "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				remotes = append(remotes, trimmed)
			}
		}
	}

	// Existing chains, for writer-id collision avoidance. Best effort by
	// design: a listing that fails costs a collision check, not the run.
	var taken func(identity.WriterID) bool
	if chains, err := dag.Chains(storer); err == nil {
		existing := make(map[identity.WriterID]struct{}, len(chains))
		for _, chain := range chains {
			existing[chain.Ref.WriterID] = struct{}{}
		}
		taken = func(id identity.WriterID) bool {
			_, ok := existing[id]
			return ok
		}
	}

	// 3. From here on the command writes to git config.
	writerID, minted, err := identity.EnsureWriterID(ctx, repoRoot, taken)
	if err != nil {
		fmt.Fprintf(stderr, "writ init: ensure writer ID: %v\n", err)
		return 1
	}

	if minted {
		fmt.Fprintf(stdout, "Writer ID: %s (minted)\n", writerID)
	} else {
		fmt.Fprintf(stdout, "Writer ID: %s (already configured)\n", writerID)
	}

	repoID, repoMinted, err := identity.EnsureRepoID(ctx, repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "writ init: ensure repo ID: %v\n", err)
		return 1
	}

	if repoMinted {
		fmt.Fprintf(stdout, "Repo ID: %s (minted)\n", repoID)
	} else {
		fmt.Fprintf(stdout, "Repo ID: %s (already configured)\n", repoID)
	}

	// 4. Report the person identifier this repo will write into op payloads.
	// It is derived, not minted: writ.personId when set, else email:<user.email>.
	// Reported separately from the identity load below because a repo with no
	// signing key configured still has a person identifier, and because
	// "which person am I?" is the question a new user asks first.
	if gitCfg, cfgErr := identity.ReadGitConfig(ctx, repoRoot); cfgErr == nil {
		personID, personErr := identity.DerivePersonID(gitCfg)
		switch {
		case personErr != nil:
			fmt.Fprintf(stderr, "warning: no person identifier: %s\n", initMessage(personErr))
			fmt.Fprintf(stderr, "Configure one of:\n")
			fmt.Fprintf(stderr, "  git config %s user:<handle>\n", identity.PersonIDKey)
			fmt.Fprintf(stderr, "  git config user.email <address>\n")
		case gitCfg["writ.personid"] != "":
			fmt.Fprintf(stdout, "Person ID: %s (from %s)\n", personID, identity.PersonIDKey)
		default:
			fmt.Fprintf(stdout, "Person ID: %s (derived from user.email)\n", personID)
		}
	}

	// 5. Load identity to report author and key state.
	//
	// A missing key gets the remediation for that key. The signing block used
	// to catch every key beginning "user.", which swept user.name and
	// user.email into it: a repo with no user.email was told its SSH signing
	// was misconfigured and shown three git config lines, none of them the
	// one it needed.
	ident, err := identity.Load(ctx, repoRoot)
	if err != nil {
		var cfgErr *identity.ConfigError
		if errors.As(err, &cfgErr) {
			switch {
			case errors.Is(cfgErr.Problem, identity.ErrMissing) || errors.Is(cfgErr.Problem, identity.ErrUnsupportedFormat) || errors.Is(cfgErr.Problem, identity.ErrInvalid):
				switch cfgErr.Key {
				case "gpg.format", "user.signingKey":
					fmt.Fprintf(stderr, "warning: SSH signing key not fully configured (%s)\n", initMessage(cfgErr))
					fmt.Fprintf(stderr, "To configure SSH signing for Writ and Git:\n")
					fmt.Fprintf(stderr, "  git config gpg.format ssh\n")
					fmt.Fprintf(stderr, "  git config user.signingKey ~/.ssh/id_ed25519.pub\n")
					fmt.Fprintf(stderr, "Optionally configure verification allowed signers:\n")
					fmt.Fprintf(stderr, "  git config gpg.ssh.allowedSignersFile ~/.ssh/allowed_signers\n")
				case "user.name", "user.email":
					fmt.Fprintf(stderr, "warning: author identity not fully configured (%s)\n", initMessage(cfgErr))
					fmt.Fprintf(stderr, "To configure the identity Writ and Git author commits with:\n")
					fmt.Fprintf(stderr, "  git config user.name \"Your Name\"\n")
					fmt.Fprintf(stderr, "  git config user.email you@example.com\n")
				default:
					fmt.Fprintf(stderr, "warning: identity configuration: %s\n", initMessage(cfgErr))
				}
			default:
				fmt.Fprintf(stderr, "warning: identity configuration: %s\n", initMessage(err))
			}
		} else {
			fmt.Fprintf(stderr, "warning: identity configuration: %s\n", initMessage(err))
		}
	} else {
		if ident.Key.Literal {
			fmt.Fprintf(stdout, "Signing key: key::%s (ssh)\n", ident.Key.Value)
		} else {
			fmt.Fprintf(stdout, "Signing key: %s (ssh)\n", ident.Key.Value)
		}
	}

	// 6. Configure fetch refspecs for the remotes resolved in step 2. The
	// repository is already open, so the client is built from that storer
	// rather than opening it a second time — the second open is where a
	// failure used to arrive too late to matter.
	if len(remotes) == 0 {
		fmt.Fprintln(stdout, "No git remotes configured; fetch refspec will be added when a remote is configured.")
	} else {
		client, err := sync.OpenStorage(storer, repoRoot, identity.Identity{WriterID: writerID})
		if err != nil {
			// Not reachable from here: OpenStorage rejects only a nil storer,
			// and storer came back non-nil in step 2. Checked rather than
			// discarded so it stays honest if that ever changes.
			fmt.Fprintf(stderr, "writ init: open sync client: %v\n", err)
			return 1
		}

		for i, remote := range remotes {
			status, err := client.Ensure(ctx, remote)
			if err != nil {
				fmt.Fprintf(stderr, "writ init: remote %q: %v\n", remote, err)
				reportPartialInit(stderr, writerID, repoID, remotes[:i], remotes[i:])
				return 1
			}
			if status.Repaired {
				fmt.Fprintf(stdout, "Configured fetch refspec for remote %q (%s)\n", remote, status.Expected)
			} else {
				fmt.Fprintf(stdout, "Fetch refspec for remote %q is already configured (%s)\n", remote, status.Expected)
			}
		}
	}

	// 7. Write a starter writ.schema, working-tree repositories only. A
	// bare repository has no working tree to put a source file in
	// (gitInfo.WorkTree is "" for one — resolved in step 2, not repoRoot
	// itself, which the earlier `--is-bare-repository` branch already set
	// to the git dir); an existing writ.schema is never overwritten, no
	// matter its content. The namespace, if this run needed one, was
	// already resolved (and validated) in step 2.5, before anything above
	// was written.
	if gitInfo.WorkTree != "" {
		if err := writeStarterSchemaFile(gitInfo.WorkTree, starterNamespace, opts.namespace, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "writ init: writ.schema: %v\n", err)
		}
	}

	return 0
}

// writeStarterSchemaFile writes a namespace-only writ.schema at the work
// tree root when one is not already there. Writ declares no types of its
// own (spec/schema-source.md; AGENTS.md), so the starter file is a
// namespace line and nothing else — no types, no vocabulary — and never
// overwrites a file that already exists. namespace is the value step 2.5
// already resolved and validated when a starter file was due; it is empty
// (and unused) when one was not, which is exactly the case where the file
// already exists here too — enforced below, since step 2.5's stat and this
// function's own stat are two different moments and nothing stops the file
// from being removed in between. flagValue is the raw --namespace the user
// passed, if any, purely to report that it was ignored when there was
// nothing for it to name.
func writeStarterSchemaFile(workTree, namespace, flagValue string, stdout, stderr io.Writer) error {
	path := filepath.Join(workTree, schemaSourceFileName)
	if _, err := os.Stat(path); err == nil {
		if flagValue != "" {
			fmt.Fprintf(stdout, "writ.schema already exists; leaving it unchanged (--namespace %q ignored)\n", flagValue)
		} else {
			fmt.Fprintf(stdout, "writ.schema already exists; leaving it unchanged\n")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	// namespace is only ever empty when a starter file was not due (the
	// stat above would then have found the file step 2.5 also saw and
	// already returned). Reaching here with an empty namespace means the
	// file was removed between step 2.5's stat and this one — a
	// concurrent `git checkout`, `clean`, or `stash` in the same work
	// tree — and writing it anyway would produce a writ.schema with an
	// empty namespace that every later `schema plan`/`apply` refuses.
	// That is a programming error, not a user error one more validation
	// message would help with, so it fails loudly instead of printing
	// "Wrote starter" over a file the caller cannot read back.
	if namespace == "" {
		panic("writ init: writeStarterSchemaFile: namespace must not be empty when a starter file is due")
	}

	content := "namespace " + namespace + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Wrote starter %s\n", path)
	return nil
}
