# Agent brief

Writ stores signed, append-only, mergeable state inside the git
repository itself, under `refs/writ/*`. Writ knows merge types and
value types, not SDLC types: the SDLC vocabulary — review, issue,
project, cycle, and the rest — is a schema declared as data by a
consumer, the same way git knows nothing about GitHub. Written in Go,
Apache-2.0, one monorepo.

Before proposing or implementing anything, read `VISION.md` (what this
is for, what it is deliberately not, and the order the work goes in)
and `ARCHITECTURE.md` (the technical record: settled decisions and the
reasoning behind them). Those two are the fence around this project.
When a proposal conflicts with them, the proposal loses or the document
is amended deliberately — never by drift.

## This file

AGENTS.md is the only agent brief here. CLAUDE.md and GEMINI.md are
one-line `@AGENTS.md` imports, so every toolchain reads the same text
and there is nothing to keep in sync. Edit AGENTS.md; leave the two
stubs alone. Same pattern as the rest of the studio.

## House rules

- Boring, small, direct. Prefer the standard library; go-git for local
  object I/O, system git for all transport. New dependencies need a
  reason.
- Treat scope growth, speculative abstraction, and framework-building
  as bugs.
- Match the style of surrounding code.
- The spec is the conformance fixtures, not the prose. A change to fold
  behaviour, the op envelope, or canonical encoding lands as one atomic
  change touching spec text, fixtures, and implementation together.
- Nothing has shipped: no tags, no users, no external implementations.
  Until v0.1.0 there is nothing to be compatible with, so backward
  compatibility is 100% tech debt — no tolerance shims, no migration
  paths, no fixtures pinning a form the format never released. When a
  decision supersedes an earlier one, delete what it replaced instead of
  bridging to it, and state the consequence plainly. `spec/identifiers.md`
  §"A bare identifier is invalid" is the standard to match. This changes
  the day we tag a release; until then, "tolerate the old form" is never
  the answer. Forward compatibility (unknown types and fields preserved
  and ignored) is a different thing and stays.
- Fold is pure and deterministic: ops in, state out, no I/O. Keep it
  that way — it is the part that has to be boring and correct.
- The SQLite projection is a droppable cache, never a source of truth.
- Unknown op types and fields are preserved and ignored, never dropped.
  Old clients must not destroy new clients' data.
- The public Go API is schema-shaped: no SHAs or refspecs leak to
  callers unless they ask, and the shapes callers see come from the
  schema in the log, not from Go structs writ ships. Anything built
  on top — including anything we host — consumes that public API
  with no reach into internals.
- The schema DSL declares types, value types, merge strategies, and
  relations, and nothing else — no computed fields, no hooks, no
  expressions, no permissions, no logic in defaults (see
  ARCHITECTURE.md §Schema layer). This fence is a house rule, not a
  suggestion; framework-building is a bug even one field at a time.
- Writ names no downstream product: no ticket, spec file, fixture,
  symbol, or documentation prose names what's built on top of writ's
  schema layer. Writ defines `writ.schema`; who authors one, and for
  what domain, is not writ's business — the way git knows nothing
  about GitHub.
- When you file a Linear ticket, set a priority and an estimate — your
  best judgment, stated once, not discussed.
- Every commit needs a `Signed-off-by` trailer (DCO, enforced by CI —
  see CONTRIBUTING.md). Run `git config core.hooksPath .githooks` once
  per worktree so it's added automatically; a commit that already
  landed without one is `git commit --amend -s` away from fixed. A
  fresh worktree doesn't inherit this config — check it before your
  first commit there, not after CI fails.
- Rebase a branch onto current `origin/main` before opening or
  force-pushing its PR — don't trust a branch just because you haven't
  touched it, or its worktree, in a while. Other tickets merge to
  `main` concurrently, so a branch (and its diff against `main`) goes
  stale fast; this applies whether a subagent is doing the rebase as
  part of implementing/reviewing/merging, or you're driving directly.

## Layout

Planned monorepo layout (see `ARCHITECTURE.md` for the rationale):

```
/spec          — convention doc, JSON schemas, conformance fixtures
/engine        — codec, dag, fold, resolve, projection, sync (public Go API)
/cmd/writ      — CLI: porcelain for humans, --json for scripts/agents
/docs
```

## Workflow

The pipeline is four composable skills, reachable here at
`.agents/skills/` — each entry there is a relative symlink up to the
canonical copy in the parent studio repo, which is where they are
actually maintained, so every project runs the same pipeline and there
is no per-project fork to keep in sync. Edit them there, not here.
Those symlinks are untracked on purpose: they resolve only in a
checkout sitting inside the parent repo, so a standalone clone of
writ has no `.agents/skills/` at all. That is expected — the pipeline
is workspace tooling, not something writ ships. If you cloned this
repo on its own and the skills described below are missing, that is
why.

The four: `implement-ticket` takes one Linear WRIT ticket to a
CI-green draft PR in a detached git worktree; `adversarial-review`
runs reviewer/fixer rounds on an open PR to a mergeable or capped
verdict; `merge-queue` rebases and squash-merges every eligible,
approved PR, in an order chosen to minimize conflicts, resolving
mechanical rebase conflicts itself and surfacing anything needing new
logic; `dispatch` orchestrates a batch of tickets through all three.
The first three stand alone for a single ticket or PR a human is
already driving; `dispatch` is for running the queue. Read a skill's
`SKILL.md` before changing what its stage produces; read `dispatch`'s
before changing how runs are queued.

## Dispatch

The per-repo configuration those four skills read. Every value they
would otherwise have to hardcode lives here.

- **Linear team key**: `WRIT` (ticket ids are `WRIT-<n>`).
- **Check command**: `make build test api-check cli-docs-check` — must
  pass locally before any push, by an implementer, a fixer, or a human.
- **Base branch**: `main`.
- **Worktrees**: `.claude/worktrees/` — one detached worktree per
  ticket, named for the ticket.
- **Run manifest**: `.claude/worktrees/dispatch-manifest.md`.

Statuses are Linear's stock ones — `Todo` → `In Progress` →
`In Review` → `Done` — with two workspace labels doing the rest:
`approved-to-merge` on a ticket in `In Review` means a human has approved
its merge and it is in the merge queue; `needs-attention` means it
needs a human and keeps whatever status it already had. `Backlog` is
off-limits to dispatch: promoting a ticket to `Todo` is the only
signal that it is available to work.

### Review invariants

The house rules above that a reviewer of a writ change is adversarial
about, each with what makes a diff a finding against it. A diff that
breaks one is a major finding, not a nit.

`## House rules` is the canonical statement of every rule named here.
This list points at them rather than restating them, so there is one
copy to keep current and nothing to drift; where the two look like
they disagree, the house rule wins. `VISION.md` and `ARCHITECTURE.md`
are the long form behind both.

- **Fold purity.** I/O, a clock, randomness, or any ambient state
  reaching the fold path is a finding, however convenient it is.
- **Unknown op types and fields preserved and ignored.** Round-trip
  loss of anything unrecognized is a finding — old clients must not
  destroy new clients' data.
- **The SQLite projection as a droppable cache.** Anything answerable
  only from the projection, or wrong after dropping and rebuilding it,
  is a finding.
- **The public Go API staying schema-shaped.** Git plumbing — SHAs,
  refspecs — reaching callers who did not ask for it is a finding, and
  so is a caller-visible shape that comes from a Go struct writ ships
  rather than from the schema in the log. Reaching into internals from
  anything built on top, including anything we host, is a finding too.
- **The schema DSL fence.** A computed field, hook, expression,
  permission, or logic in a default is a finding no matter how small
  the increment — framework-building is a bug even one field at a time.
- **No downstream product named.** A ticket, spec file, fixture,
  symbol, or line of documentation prose naming what is built on top
  of writ's schema layer is a finding.
- **Spec changes landing atomically.** Implementation moving without
  conformance fixtures, or fixtures without spec text, is a finding —
  the spec is the fixtures, not the prose.
- **No backward-compatibility work before v0.1.0.** A tolerance shim,
  migration path, or fixture pinning a form the format never released
  is a finding; a superseded decision gets its old form deleted, not
  bridged to. Forward compatibility is a different thing and stays.
- **No scope growth, speculative abstraction, or framework-building.**
  A new dependency without a stated reason is a finding.
- **A `Signed-off-by` trailer on every commit** (DCO, enforced by CI).
  A fresh worktree does not inherit `git config core.hooksPath
  .githooks` — set it before the first commit there.
- **A branch rebased onto current `origin/main`** before its PR is
  opened or force-pushed. A stale base makes a review of the diff mean
  less than it looks.

Build and test commands beyond the check command above will be
documented here once more code exists.
