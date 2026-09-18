# Decision: Go module boundary — single module vs. engine module

Spike for WRIT-61. ARCHITECTURE.md commits to two things that pull gently
against each other: "one language, one `go build ./...`" and the engine
"consumed as an ordinary pinned Go module" by anything built on top,
including a future hosted layer. This resolves that into one deliberate
choice rather than leaving it to drift once code lands (WRIT-53).

## Decision

**Single module for the whole monorepo.** One `go.mod` at the repo root,
module path `github.com/writtendev/writ`. The engine's public API lives
at the `engine` subpackage — import path `github.com/writtendev/writ/engine`
— matching the layout ARCHITECTURE.md already lays out under Repo strategy.
No `go.work` is needed yet; that tool solves multi-module local development,
and there is only one module.

## Why

**A single module keeps external consumers lean without multi-module overhead.**
Go resolves builds from the actual per-package import graph, so a service or
downstream tool importing `github.com/writtendev/writ/engine` only compiles and
links packages in `engine` and its direct dependencies (verified: `go mod why`
reports unused sibling packages as not needed, and they never get a `go.sum`
entry). What Go 1.17+ module graph pruning adds on top is narrower but still
relevant: it avoids loading `go.mod` files of unused transitive dependencies,
keeping `go.sum` for an engine consumer proportional to what `engine` actually
imports.

**`internal/` enforces the API boundary regardless of module shape.** The
house rule that "no SHAs or refspecs leak to callers unless they ask" is a
package-visibility problem, not a module-boundary problem — `engine/internal/`
keeps codec/dag/fold plumbing unreachable from outside whether `engine` sits
in its own `go.mod` or as a subtree of one. A second module buys no
additional enforcement here. *(Superseded in part by WRIT-287 — see
"Amendment" below: `internal/` moved from under `engine/` to the module
root, so the "as a subtree of one" half of this claim no longer holds.)*

**A split module is a real, ongoing cost with no current need to justify it.**
Two `go.mod`/`go.sum` pairs to keep in sync, a `go.work` file to maintain, and
two separate version-tag lineages to reason about. With viewers (TUIs) and
bridges (GitHub sync) decoupled into their own downstream repositories, the
monorepo houses strictly the Go engine library and the Git-like CLI. Splitting
the engine and CLI into separate in-repo modules today would introduce
unnecessary overhead against the one-monorepo rationale (where fold-rule
changes land as one atomic PR across spec, fixtures, engine, and CLI).

**The move is cheap to make later and expensive to unmake now.** If a real
external consumer shows up wanting independent engine versioning, extracting
`engine/go.mod` and adding a root `go.work` is a mechanical file split that
doesn't touch import paths for in-repo callers — though standing up
independent tag/versioning discipline for the engine subtree afterward is a
real, separate cost, not something the file split buys on its own.
Committing to two modules today, before any code exists, is exactly the
"speculative abstraction" the house rules
flag as scope growth to avoid without a concrete reason. *(No longer true
after WRIT-287 — see "Amendment" below: extracting `engine/go.mod` today
would first require moving the module-root `internal/` tree back underneath
`engine/`, which does touch import paths for every in-repo caller.)*

## Consequence: module path

`github.com/writtendev/writ` is the current repo location and is what
WRIT-53 should use verbatim for the root `go.mod`. (This decision was
originally written against the pre-transfer location
`github.com/mattwalters/gloss` and anticipated that a move to a dedicated
org would change the module path with it; that move has since happened —
the repo now lives at `writtendev/writ`, and the WRIT-65 rebrand carried
the module path along, exactly the mechanical rename predicted here.)
WRIT-53 needs a concrete string to put in `go.mod`, and import-path
renames are a mechanical `gofmt -r`-and-`go mod edit` exercise, not a
design question.

## What this means for WRIT-53 and repository layout

Root `go.mod` at module path `github.com/writtendev/writ`, covering `/engine`
and `/cmd/writ` per the layout ARCHITECTURE.md specifies. Downstream viewers
and bridges live in separate repositories and consume `.../writ/engine`. No
second `go.mod`, no `go.work`, until a real external consumer motivates an
independent engine module versioning split.

## Module path decision: keep `github.com/writtendev/writ` (WRIT-73)

The root module path remains `github.com/writtendev/writ`.

A vanity import path (such as `writ.dev/writ`) buys cosmetics at the ongoing
operational cost of hosting and serving a `go-import` meta tag in perpetuity.
If that domain or its redirect infrastructure ever lapses, `go get` breaks
permanently for every version ever published under that path.

Revisit rule and deadline: The module path decision is closed and should only
be revisited before the first release tag (WRIT-58). Changing a module path
after public release tags exist is a v2-shaped break across the ecosystem.


## Amendment: engine's own module option foreclosed (WRIT-287)

WRIT-287 (2026-09-18, Matt's ruling on WRIT-248 option C) moved every
`engine/*` subpackage — codec, dag, identity, order, projection, resolve,
scenario, schemasrc, state, sync, plus the packages already under
`engine/internal/` — to one module-root `/internal/` tree, so that no
exported root-package signature named a package a caller couldn't import.
That was a package-visibility fix, not a module-boundary decision, but it
has a module-boundary consequence this document priced wrong: with
`internal/` sitting above `engine/` instead of inside it, `engine/internal/`
no longer exists to carry over if `engine` is later split into its own
module. Extracting `engine/go.mod` today would need `internal/` moved back
underneath `engine/` first — a real rename touching every in-repo import of
every package that moved, not the "mechanical file split that doesn't touch
import paths for in-repo callers" this document originally described.

The decision itself — single module, no `go.work`, until a real external
consumer needs independent engine versioning — stands. What changed is the
cost estimate for ever reversing it: it is now closer to "undo WRIT-287"
than to "add a second `go.mod`." That cost is accepted here, not revisited;
the single-module decision carries no revisit deadline of its own (unlike
the module path decision above, which is pinned to WRIT-58).
