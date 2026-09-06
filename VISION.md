# Writ — Product Vision & Strategy

_Name: "Writ" (a writ is a formal written instrument — a record whose authority comes from being written down and traveling with what it governs). The project was originally named "Gloss" and was renamed when it moved to the `writtendev` org; the naming survey from the Gloss era is preserved in `docs/naming-collision-report.md`. An equivalent trademark/collision screen for "Writ" has not been run yet._

## The thesis in one paragraph

Code review — and eventually the whole software development lifecycle: tickets, projects, cycles — should live _in the git repository itself_, as signed, append-only operations stored as git objects, co-located with the code they describe. The pull request is a platform construct, not a git concept; forges typically keep the code half of every PR in refs while the conversational half lives in a platform database. Writ closes that gap: an open, documented convention for SDLC data in git, a reference engine that reads and writes it, and good clients on top. Because the transport is just git (push/pull over SSH/HTTPS with credentials users already have), it works with every git host, requires no server, and — critically — puts the entire review and ticket history one `git fetch` away from any coding agent, with no API tokens, no rate limits, and no integration work.

## Why now (and lessons from prior art)

This idea has a rich lineage, and each ancestor contributes a lesson we're grateful for:

**git-appraise (Google, ~2015)** proved the mechanics: reviews as git objects under `refs/notes/devtools/*`, one JSON object per line so git's `cat_sort_uniq` merge strategy resolves concurrent writes, zero server-side setup, works with any host. Adoption stayed niche — the era's tooling and expectations weren't there yet, and git-notes have practical limitations (notes don't fetch by default; notes attached to commits are orphaned by rebase). Lesson: the data model alone is not a product; client experience and ecosystem timing matter as much as the format.

**Gerrit NoteDb** is the production-scale existence proof. Gerrit migrated off SQL entirely; all change metadata — status, votes, comments — lives in git meta refs alongside the code, giving atomic replication/backup, automatic audit history (metadata mutations are commits), plugin extensibility without schema migrations, and offline/federated review. Battle-tested at very large scale. Lesson: git-as-review-database works in production.

**Radicle** is the most complete modern design in this space and our primary architectural inspiration — we want to credit it clearly. Issues, patches, and reviews are "collaborative objects" (COBs): CRDTs implemented as DAGs of signed operation-commits under per-object refs, with per-writer namespaces (each peer pushes to their own namespace; state is authenticated via signed ref listings) and a SQLite cache materializing current state so reads don't re-fold history. Radicle pairs this data model with a peer-to-peer network because its mission is sovereign, serverless collaboration. Writ has a narrower goal — working with the git hosting people already use — so we adopt the COB-style op-log, per-writer refs, signing, and fold-to-cache pattern, while relying on the existing git remote as the sync point instead of a P2P layer. Different missions, shared foundations.

**What's different in 2026:** the agent argument. Earlier attempts had no strong answer to "why move review data?" Today there is one: agents. An agent with one clone gets the ticket, its discussion, the linked review history, and the code — context co-located with the artifact it's editing. Review data is among the highest-value context an agent can have, and today it usually sits behind APIs, tokens, and rate limits. "Your agents read the entire SDLC history with `git fetch`" is a pitch that did not exist a decade ago. Additionally: every review action being a _signed, immutable_ git object gives attribution and tamper-evidence exactly as more review actors become non-human.

## Positioning

**Works with your existing host.** Writ is not a forge and does not compete for where the repo lives. It rides along inside any repo on any host, as just another git client pushing to its own ref namespace. Adoption cost is a `git config` change, not a migration. If you ever stop using Writ, your data is already in your repo, in a documented format — there is nothing to export and nothing to lose.

**Relationship to project-management tools.** Tools like Linear demonstrate that teams love fast, local-first, operation-synced workflows — and the industry trend of integrating code review into PM tools shows everyone can see the SDLC graph wants to be one graph. Writ approaches the same convergence from the other direction: bring the whole graph down into git, where it's versioned, offline-capable, and agent-readable. We deeply respect the polish of these products and are not attempting feature parity; the spec is SDLC-general from day one (issue/project/cycle types defined alongside review types), while the initial focus is review, with issues as the natural companion.

**Relationship to review tooling generally.** A wave of excellent commercial review tools shows how much room there is to improve on the status-quo review experience. Writ's distinct contribution is the open, host-agnostic data layer underneath: review data as a portable convention rather than a per-tool database, which any tool — including those — could in principle read and write.

## The open core and the hosted layer

The line: **everything that reads or writes the format is open; services that require running infrastructure may be offered as hosted products.** The guiding test: if something being closed would undermine a user's confidence that their data is portable and the format is neutral, it must be open.

Open (Apache-2.0, this monorepo): the spec + conformance fixtures, the engine, and the CLI. Downstream viewers (such as TUIs or web UIs) and bridges (such as GitHub sync) live in separate downstream repositories consuming the Go engine API and `--json` CLI plumbing.

Potential hosted layer (separate, later): an **awareness service** — git has no push notifications, so a relay that watches remotes and fans out "refs changed" events over SSE/websocket can turn seconds-of-polling into sub-second multiplayer liveness; plus notifications, digests, chat integrations. And a **coordination service** — anything requiring a designated actor: merge queues, branch protection, approval policy, CI enforcement, org-wide search, identity mapping from signing keys to directory identities. Architecturally, any hosted service is _just another git client_ — an actor with a signing key that fetches, folds, and pushes ops like everyone else, consuming the public engine through its ordinary public API. No privileged database, no private forks of the format. Anyone else can build and host equivalent services on the same open floor; we intend to earn that business on quality, not lock-in.

A relay is never a source of truth — if it's unavailable, clients degrade to polling and git remains canonical.

**Licensing:** Apache-2.0 (the patent grant matters for enterprise adoption; it's also the git-appraise precedent). Deliberately not a source-available license: the open layer is client-side by design, and the format's credibility as a neutral convention depends on it being unambiguously free. DCO on every commit to keep provenance clean.

## Performance story

Writ clients don't need optimistic UI — they're local-first, which is stronger. A write is an appended op in the local object store plus a synchronous SQLite projection update: single-digit milliseconds, truthful, nothing pending, nothing to roll back. Writes cannot be rejected on the merits: per-writer refs mean your push touches only your namespace; concurrent edits coexist as sibling ops and reconcile deterministically at fold time. Remaining failures are _sync_ failures (remote down, credentials expired), handled with a quiet "n ops unsynced" indicator and background retry. Honest tradeoffs: cold start (clone vs. an API call), casual mobile/browser reach, and teammate-visibility latency — the last is what an event relay addresses. Scorecard: your own interactions are local and instant; offline is categorically strong; multiplayer liveness benefits from a relay.

## Scope architecture: one repo, one home

Reviews home with the code they describe. Team-level objects — projects, cycles, tickets with no repo at all — still need exactly one home, because per-writer refs stay conflict-free only when everyone collaborating on an object pushes to and fetches from the same remote. But that home is not a role writ assigns: every object homes in the repo the client is standing in when it writes the object. There is no configured elsewhere, no transparent second store, no routing. A team that wants all its tickets in one place gets that by running writ against one repo — an operational choice made above writ, not writ's architecture. Object IDs stay globally unique and `<repo-id>#<object-id>` stays representable, so "ticket in repo A fixed by review in repo B" is still expressible; what's gone is any machinery inside writ that resolves that reference, or decides where an object lives, on your behalf. Cross-repo assembly — pointing at a set of repos, naming one as home for team-level objects, aggregating reads across all of them — belongs to written, the layer above; that vocabulary belongs to written alone and has left writ's spec.

## Risks and open questions

1. **Ref-namespace host compatibility (foundational; spike first).** The host-agnostic thesis assumes major hosts accept pushes to custom ref namespaces (`refs/writ/*`) and don't GC or hide them destructively. Some hosts restrict certain namespaces (e.g., `refs/pull/*` is read-only on GitHub); behavior varies. One-day spike across GitHub, GitLab, Bitbucket, Gitea/Forgejo, Codeberg, plus bare-SSH. If a major host is restrictive, fallback designs exist (branch-namespace encoding) but must be known early.
2. **Comment anchoring across force-pushes** — the genuinely hard problem of the space. Anchor to blob/hunk content, not line numbers; degrade gracefully to "orphaned but preserved."
3. **Canonicalization** — signatures and content-addressing require byte-stable encoding; this is where the spec earns its keep.
4. **Repo growth / GC story** — op history grows forever by design; the spec needs a compaction/archival answer eventually, not immediately.
5. **Multi-device self-conflict** — writer-id = (user, device); the fold merges your own devices' ops like anyone else's.
6. **Additional implementations** — if the convention succeeds, others will implement it. That's the goal: the conformance fixtures keep implementations compatible, and a second independent implementation would be welcome proof the spec stands on its own. `spec/` graduates to a neutral home when that community exists.
7. **Naming collision checks** outstanding on "Writ."

## Non-goals

Not a forge; not git hosting. No P2P networking (see Radicle for that mission, done properly). No PM-tool feature parity in the near term. No binary storage formats for the canonical data (binary = no diff, no merge, no delta compression; git _is_ the append-only log — commits are the entries, refs are the head pointers). Columnar/DuckDB exports are a derived local projection, never committed. No anonymous or in-git public issue intake: writing an operation requires push access to `refs/writ/<writer-id>/*`, which public bug reporters lack. Writ provides no authorization model or anonymous write path at the format level; public intake is externalized to an intake bot or bridge, or teams keep forge issues as their public front door.

## Sequencing conviction

Spec + fixtures → engine → CLI → downstream bridges & viewers, with the ref-namespace spike before everything. Rationale: the moment `writ` can fold and manipulate SDLC history inside git objects in your own repo, the thesis is demonstrable with real data — and that solid foundation is worth more than beautiful chrome on an empty log.
