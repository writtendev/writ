package writ

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/gitdir"
	"github.com/writtendev/writ/internal/identity"
	writsync "github.com/writtendev/writ/internal/sync"
)

// starterSchemaFileName is the working-tree source file Init writes a
// namespace-only starter into. It mirrors cmd/writ's own
// schemaSourceFileName (cmd/writ/schema.go) rather than sharing it: the two
// packages don't import each other, so this is a second spelling of the
// same spec-level file name (ARCHITECTURE.md Schema layer: "writ.schema is
// a working-tree source form"), not a second file.
const starterSchemaFileName = "writ.schema"

// ErrStarterNamespaceRequired is returned by Init when the repository has a
// work tree with no starter writ.schema yet and InitOptions.StarterNamespace
// is empty. Init performs no writes before returning this error, so a
// caller is free to resolve a namespace -- from a flag, an interactive
// prompt, or a refusal -- however it likes and call Init again with
// InitOptions.StarterNamespace set. Init does not resolve one itself:
// prompting is I/O the engine does not do, and minting one silently would
// bake an unchosen string permanently into the schema object's id and
// every wire type it declares (WRIT-199, WRIT-217).
var ErrStarterNamespaceRequired = errors.New("writ: a starter writ.schema is due and needs a namespace")

// InitOptions configures Init.
type InitOptions struct {
	// Remotes names the remotes to configure a writ fetch refspec for. Nil
	// means discover them via `git remote`, which also selects lenient
	// treatment of a remote Init merely discovered and cannot configure (a
	// url-less section, or a "-"-leading name): such a remote is skipped
	// and reported on RemoteInit.Skipped rather than aborting the run. A
	// non-nil Remotes is treated as the caller's own explicit choice, so an
	// unusable name among them aborts the run instead.
	Remotes []string

	// StarterNamespace, when non-empty, writes a namespace-only writ.schema
	// at the work tree root if none is there yet. Empty leaves a due
	// starter file unwritten and makes Init return
	// ErrStarterNamespaceRequired instead of proceeding. The caller is
	// responsible for validating it against writ.schema's namespace
	// grammar before passing it in -- Init does not re-validate.
	StarterNamespace string
}

// RemoteInit reports the outcome of configuring one remote's writ fetch
// refspec.
type RemoteInit struct {
	// Name is the remote's name.
	Name string
	// Refspec is the fetch refspec Init expects for this remote
	// (+refs/writ/*:refs/remotes/<name>/writ/*). Set for a remote Init
	// configured, whether repaired or found already correct; empty for a
	// skipped remote, a not-attempted one (NotAttempted), or the one hard
	// failure that stopped the run.
	Refspec string
	// Repaired reports whether Init had to add or correct the refspec to
	// configure this remote. Meaningful only when Refspec is set: false
	// there means it was already configured correctly. Also false, with
	// no meaning, for a remote Init never configured -- skipped, not
	// attempted (NotAttempted), or the one hard failure that stopped the
	// run.
	Repaired bool
	// Skipped reports a remote Init merely discovered
	// (InitOptions.Remotes was nil) and could not configure -- a url-less
	// section or a "-"-leading name -- and therefore left alone rather
	// than treating as a failure of the run.
	Skipped bool
	// NotAttempted reports a remote Init never even tried to configure,
	// because an earlier remote's hard failure stopped the run first.
	// Init lists every remote it discovered or was asked to configure,
	// not only the ones it got to, so a caller rendering a partial-
	// failure report can name all of them -- omitting the unattempted
	// ones would silently drop a remote that genuinely has no writ fetch
	// refspec from that report. Err is nil for a NotAttempted entry:
	// there is nothing wrong with this remote itself, Init simply never
	// reached it.
	NotAttempted bool
	// Err is non-nil when Init could not configure this remote -- whether
	// skipped or fatal to the run -- and nil when the remote was
	// configured successfully or was never attempted (NotAttempted).
	Err error
}

// InitResult reports what Init found and did. It is populated up to the
// point of any failure, so a caller can render a partial-failure report
// naming exactly what is now configured and what is not.
type InitResult struct {
	// WriterID is this device's writer id: read from existing git config,
	// or minted and persisted to local config when absent. WriterIDMinted
	// reports which.
	WriterID       string
	WriterIDMinted bool

	// RepoID is this repository's designator, resolved the same way as
	// WriterID but from local config only.
	RepoID       string
	RepoIDMinted bool

	// PersonID is the local writer's person identifier (writ.personId, or
	// derived from user.email). PersonIDFromKey reports which. PersonIDErr
	// is set, and PersonID left empty, when neither yields a conforming
	// identifier -- not fatal to Init, since reading a repository needs no
	// person identifier.
	PersonID        string
	PersonIDFromKey bool
	PersonIDErr     error

	// SigningKey is the SSH signing key configured for commit signing;
	// SigningKeyLiteral reports whether it is a literal key
	// (user.signingKey's key:: form) rather than a path. IdentityErr is
	// set, and both left empty, when identity or signing configuration is
	// incomplete -- not fatal to Init, since a repository can be read, and
	// partially configured, without a signing key.
	SigningKey        string
	SigningKeyLiteral bool
	IdentityErr       error

	// Remotes reports the outcome for each remote Init discovered or was
	// asked to configure, in that order -- including any it never
	// attempted because an earlier remote's hard failure stopped the run
	// first (RemoteInit.NotAttempted). Empty means no remotes were
	// configured, discovered, or requested.
	Remotes []RemoteInit

	// StarterSchemaPath is the working-tree writ.schema path Init
	// considered, set whenever the repository has a work tree.
	StarterSchemaPath string
	// StarterSchemaWritten reports whether Init wrote a starter
	// writ.schema (namespace-only, InitOptions.StarterNamespace).
	StarterSchemaWritten bool
	// StarterSchemaExisted reports that a writ.schema was already present,
	// so Init left it unchanged.
	StarterSchemaExisted bool
	// StarterSchemaErr is set when Init could not write the starter file.
	// It does not fail Init overall: by the point this step runs, the rest
	// of the repository setup has already completed.
	StarterSchemaErr error
}

// Init resolves or mints this repository's writer id and repo id, reports
// the local person and signing identity, configures a writ fetch refspec
// for each requested remote, and, if due, writes a namespace-only starter
// writ.schema -- the one-time repository setup `writ init` performs.
//
// path may be a repository root, a subdirectory, a linked worktree, or a
// bare repository, resolved the same way system git resolves
// `git rev-parse --show-toplevel`, with a bare-repository fallback, so
// Init agrees with git about which repository a path names.
//
// Init returns InitResult populated up to the point of any failure: git
// config has no transaction, so a write that fails partway leaves the
// repository in a state a caller needs to describe accurately, not merely
// signal failure for.
//
// A starter writ.schema is due only for a repository with a work tree
// (never for a bare repository) that has none yet. If one is due and
// opts.StarterNamespace is empty, Init performs no writes at all and
// returns ErrStarterNamespaceRequired before touching git config --
// resolving a namespace is the caller's job, not Init's; see
// InitOptions.StarterNamespace.
func Init(ctx context.Context, path string, opts InitOptions) (InitResult, error) {
	var result InitResult

	// 1. Resolve the repository root exactly as system git resolves it.
	// Preserved verbatim from cmd/writ's own former implementation of this
	// step -- collapsing it into gitdir.Resolve alone is a plausible
	// cleanup with real behavioural risk (bare repos, `.git` files, linked
	// worktrees, GIT_DIR) that this function does not take on.
	repoRoot, err := resolveRepoRoot(ctx, path)
	if err != nil {
		return result, err
	}

	// 2. Open the repository. Opening is a precondition, not a later step:
	// nothing below may be written before it succeeds, so a repository
	// Init cannot open fails here, before steps 3 onward ever run.
	gitInfo, err := gitdir.Resolve(repoRoot)
	if err != nil {
		return result, err
	}
	storer, err := gitdir.OpenStorage(gitInfo)
	if err != nil {
		return result, fmt.Errorf("open git repo %s: %w", repoRoot, err)
	}

	// 2.5. Decide whether a starter writ.schema is due. Only a work tree
	// with no writ.schema yet ever gets one: a bare repository has no
	// working tree to put one in, and an already-initialized repository
	// has nothing left for a namespace to name. This runs before any write
	// below, so a refusal here (opts.StarterNamespace empty when one is
	// due) leaves the repository untouched.
	starterDue := false
	if gitInfo.WorkTree != "" {
		starterPath := filepath.Join(gitInfo.WorkTree, starterSchemaFileName)
		switch _, statErr := os.Stat(starterPath); {
		case statErr == nil:
			// Exists; not due. Step 8 below reports this.
		case os.IsNotExist(statErr):
			starterDue = true
		default:
			return result, fmt.Errorf("writ.schema: %w", statErr)
		}
	}
	if starterDue && opts.StarterNamespace == "" {
		return result, ErrStarterNamespaceRequired
	}

	// 3. Discover the remotes this run is for, before anything below
	// writes to git config: a `git remote` failure aborts here, with the
	// repository untouched, rather than after identity is already
	// persisted. explicit distinguishes a caller-supplied list from one
	// Init discovered on its own: a bad *explicit* name is the caller's
	// mistake to fix, and aborts the run (step 7); a bad *discovered*
	// name is not something the caller asked Init to touch, so it is
	// skipped and reported instead of stranding every other remote
	// (WRIT-283).
	explicit := opts.Remotes != nil
	remotes := opts.Remotes
	if !explicit {
		discovered, err := discoverRemotes(ctx, repoRoot)
		if err != nil {
			return result, fmt.Errorf("list remotes: %w", err)
		}
		remotes = discovered
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

	// 4. From here on Init writes to git config.
	writerID, writerMinted, err := identity.EnsureWriterID(ctx, repoRoot, taken)
	if err != nil {
		return result, fmt.Errorf("ensure writer ID: %w", err)
	}
	result.WriterID = string(writerID)
	result.WriterIDMinted = writerMinted

	repoID, repoMinted, err := identity.EnsureRepoID(ctx, repoRoot)
	if err != nil {
		return result, fmt.Errorf("ensure repo ID: %w", err)
	}
	result.RepoID = string(repoID)
	result.RepoIDMinted = repoMinted

	// 5. The person identifier this repo will write into op payloads:
	// writ.personId when set, else derived from user.email. Not fatal --
	// reads need no person identifier -- so a derivation failure is
	// reported on PersonIDErr rather than returned.
	if gitCfg, cfgErr := identity.ReadGitConfig(ctx, repoRoot); cfgErr == nil {
		personID, personErr := identity.DerivePersonID(gitCfg)
		switch {
		case personErr != nil:
			result.PersonIDErr = personErr
		case gitCfg["writ.personid"] != "":
			result.PersonID = personID
			result.PersonIDFromKey = true
		default:
			result.PersonID = personID
		}
	} else {
		result.PersonIDErr = cfgErr
	}

	// 6. Load identity to report author and key state. Also not fatal: a
	// repository can be read, and partially configured, without a signing
	// key.
	if ident, err := identity.Load(ctx, repoRoot); err != nil {
		result.IdentityErr = err
	} else {
		result.SigningKey = ident.Key.Value
		result.SigningKeyLiteral = ident.Key.Literal
	}

	// 7. Configure fetch refspecs for the remotes discovered in step 3.
	if len(remotes) > 0 {
		client, err := writsync.OpenStorage(storer, repoRoot, identity.Identity{WriterID: writerID})
		if err != nil {
			// Not reachable from here: OpenStorage rejects only a nil
			// storer, and storer came back non-nil in step 2. Checked
			// rather than discarded so it stays honest if that ever
			// changes.
			return result, fmt.Errorf("open sync client: %w", err)
		}

		for i, remote := range remotes {
			status, err := client.Ensure(ctx, remote)
			if err != nil {
				if !explicit && (errors.Is(err, writsync.ErrUnknownRemote) || errors.Is(err, writsync.ErrInvalidRemoteName)) {
					result.Remotes = append(result.Remotes, RemoteInit{Name: remote, Skipped: true, Err: err})
					continue
				}
				result.Remotes = append(result.Remotes, RemoteInit{Name: remote, Err: err})
				// Every remote after this one was never attempted: Ensure
				// is never called for it, so nothing above would otherwise
				// record it at all, and a caller naming "what's not
				// configured" from result.Remotes would silently drop it
				// (round-1 major finding). Recording it here, rather than
				// leaving the gap for the caller to reconstruct, is the
				// only place Init itself knows the full discovered/
				// requested list.
				for _, unattempted := range remotes[i+1:] {
					result.Remotes = append(result.Remotes, RemoteInit{Name: unattempted, NotAttempted: true})
				}
				return result, fmt.Errorf("remote %q: %w", remote, err)
			}
			result.Remotes = append(result.Remotes, RemoteInit{
				Name:     remote,
				Refspec:  status.Expected,
				Repaired: status.Repaired,
			})
		}
	}

	// 8. Write a starter writ.schema, working-tree repositories only. An
	// existing writ.schema is never overwritten, no matter its content.
	// The namespace, if a starter file is due, was already validated by
	// the caller (opts.StarterNamespace) and refused above (step 2.5) if a
	// due file had none.
	if gitInfo.WorkTree != "" {
		writeStarterSchema(gitInfo.WorkTree, opts.StarterNamespace, &result)
	}

	return result, nil
}

// resolveRepoRoot resolves path to its repository root the way system git
// resolves it for `git rev-parse --show-toplevel`, with a fallback for a
// bare repository (which has no "toplevel" in git's sense). Moved as-is
// from cmd/writ's former implementation of this step.
func resolveRepoRoot(ctx context.Context, path string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = path
	if out, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(out)), nil
	}

	cmdBare := exec.CommandContext(ctx, "git", "rev-parse", "--is-bare-repository")
	cmdBare.Dir = path
	if outBare, errBare := cmdBare.Output(); errBare == nil && strings.TrimSpace(string(outBare)) == "true" {
		cmdGitDir := exec.CommandContext(ctx, "git", "rev-parse", "--absolute-git-dir")
		cmdGitDir.Dir = path
		if outGitDir, errGitDir := cmdGitDir.Output(); errGitDir == nil {
			return strings.TrimSpace(string(outGitDir)), nil
		}
	}

	return "", errors.New("not a git repository (or any of the parent directories)")
}

// discoverRemotes lists the remotes `git remote` reports for repoRoot.
func discoverRemotes(ctx context.Context, repoRoot string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "remote")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var remotes []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			remotes = append(remotes, trimmed)
		}
	}
	return remotes, nil
}

// writeStarterSchema writes a namespace-only writ.schema at workTree's root
// when one is not already there, recording the outcome on result. It never
// overwrites a file that already exists, and re-stats immediately before
// writing rather than trusting Init's earlier due-check (step 2.5) --
// narrowing, not closing, the window a concurrent `git checkout`, `clean`,
// or `stash` in the same work tree could open between the two. Finding the
// file gone again here (namespace empty, file absent) is silently treated
// as nothing to do rather than failing the whole run over a race outside
// Init's control.
func writeStarterSchema(workTree, namespace string, result *InitResult) {
	path := filepath.Join(workTree, starterSchemaFileName)
	result.StarterSchemaPath = path

	switch _, err := os.Stat(path); {
	case err == nil:
		result.StarterSchemaExisted = true
		return
	case !os.IsNotExist(err):
		result.StarterSchemaErr = err
		return
	}

	if namespace == "" {
		return
	}

	content := "namespace " + namespace + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		result.StarterSchemaErr = err
		return
	}
	result.StarterSchemaWritten = true
}
