# Writ — Architecture & Design Decisions

_Companion to VISION.md. This is the technical record: what we're building, how it's shaped, and — most importantly — why each decision went the way it did, so contributors (human or agent) don't have to relitigate settled questions without new information._

## Core model

Writ is an **event-sourcing engine that uses git as its storage and transport substrate**. Git supplies a content-addressed object store, a sync protocol, and credential/transport infrastructure; Writ supplies the semantics. A **collaborative object** is any object whose type a schema declares (see §Schema layer): a DAG of small, signed, immutable **operations**, each stored as a git commit under a dedicated ref. Current state is never stored authoritatively; it is _derived_ by deterministically folding an object's operations. Concurrent writes don't conflict — they coexist as sibling ops in the DAG and are reconciled at fold time by spec-defined rules.

Design lineage, with gratitude: the op-log + per-writer refs + signing + fold-to-SQLite pattern is inspired by Radicle's collaborative objects (COBs); the refs-not-notes and meta-ref patterns are validated by Gerrit NoteDb; the line-oriented mergeability insight comes from git-appraise. Where Radicle pairs this data model with a peer-to-peer network in service of its sovereignty mission, Writ targets hub-and-spoke sync through whatever git remote a team already uses — a narrower goal that lets us omit the networking layer entirely.

### Ref layout

Per-writer namespaces are load-bearing: a writer only ever pushes to their own namespace, so pushes cannot non-fast-forward against another writer — the entire class of push conflicts disappears, which is what keeps the sync layer simple. `writer-id` is (user, device), sourced from git config (`.git/config` → `~/.gitconfig` → minted if absent; bots and CI writers supply a stable ID), so multi-device self-races dissolve into the DAG instead of ref conflicts without unbounded ref growth per clone event.

Within a writer's namespace, ops are stored as **append chains, not per-object refs**: `refs/writ/<writer-id>/<type>` points at that writer's latest op of that type, each op commit's git parent being their previous one. Which object an op belongs to rides the payload (`object_id`), never the ref name. Commit parents are the single home for edges (per the envelope spec, WRIT-6), and every parent is a true happens-before edge (decided, WRIT-71): parents[0] is the writer's chain predecessor — itself a genuine causal edge, since a writer-id is one sequential device — and any further parents are causal references to other ops. Spine sequencing is a producer-side rule that keeps earlier ops reachable from the writer's single ref for git GC; readers enumerate simply by walking ancestry from every fetched writ ref and grouping on `object_id`, which requires neither spine identification nor writer attribution. An object's op-DAG is then simply the commit graph restricted to ops carrying its `object_id`, with edges given by ancestry; no explicit intra-object parent list exists anywhere. Two consequences worth naming: any op someone built on stays reachable from the referencing writer's ref even if its origin ref rolls back, and fold behavior for ancestry referencing commits the fetcher lacks must be defined deterministically in the spec (`spec/fold.md`). The earlier per-object sketch (`.../cobs/<type>/<object-id>`) put ref count at O(writers × objects); since each comment is an object, an imported five-year repo is plausibly 500k refs, and git re-advertises every ref on every fetch (~120 bytes each, even unchanged), turning quiet background sync into tens of MB per no-op fetch — a cost the many-refs precedents (Gerrit, Radicle) escape only by controlling their own servers, which Writ never will. Chains bound refs at O(writers × devices × types), preserve conflict-free push and GC reachability, and make rollback detection a single cursor per chain; the cost is that reading one object without a projection means walking chains, and per-object legibility moves from `for-each-ref` into `writ` plumbing. Confirmed by measurement (`docs/spikes/writ-69-ref-scaling/`, WRIT-69): advertisement bytes are exactly linear in ref count (~119 B/ref; ~12 MB per no-op fetch at 100k refs), client-side ref processing goes superlinear past ~30k, and GitHub's write path fails outright first — a single 9,000-ref push 500s with an internal error, and chunked pushes degrade to ~70 ms/ref (22 minutes for 20k refs). Per-object refs stay comfortable only below ~10k total — one to two orders of magnitude under their own realistic scale — so the spec freezes on chains. Reading an object means walking _all_ writers' chains, grouping by the envelope's object id, and folding. Normative detail: `spec/ref-layout.md`.

We use plain refs rather than git-notes: notes don't fetch by default, and notes attached to commits are orphaned when commits are rewritten by rebase — limitations git-appraise's design had to work around. A one-time `writ init` writes fetch/push refspecs into `.git/config` (`remote.<remote>.fetch = refs/writ/*:refs/remotes/<remote>/writ/*`) so ordinary `git fetch` carries writ data into remote-tracking refs without colliding with local unpushed ops; that config edit is the entire deployment story.

### The op envelope

Every op carries the same logical envelope — op id, parent op ids (DAG edges), object id, object type, op type + version, author, timestamp, signature, type-specific body — split across **two carriers with exactly one home per field** (amended with WRIT-6, which spec'd the envelope): the op commit itself carries op id (the commit's SHA), parent op ids (parent SHAs), author, timestamp, and signature; a canonical JSON blob at a fixed path in the commit tree carries object id, object type, op type + version, and the body. Nothing is mirrored between carriers — mirroring creates two sources of truth that can disagree (payload parents vs. commit parents), and the edges are incoherent anyway: a payload can't contain its own content-derived op id, nor a signature covering the payload the signature lives inside. The accepted cost is that the payload alone isn't self-describing; a conforming reader always needs the commit. Normative detail: `spec/op-envelope.md`. Two rules with teeth:

- **Canonicalization:** byte-stable encoding (canonical JSON, `spec/canonicalization.md`) because signatures and content-addressing demand it. This is spec-level, fixture-enforced.
- **Unknown-op tolerance:** implementations MUST preserve and ignore op types/fields they don't understand — never drop (`spec/forward-compatibility.md`). Old clients must not destroy new clients' data. This is what lets the schema evolve without flag days. Fold surfaces uninterpretable ops explicitly in materialized state rather than silently skipping them, so clients can detect when newer operations have affected an object.

Signing rides git's existing commit-signature machinery (SSH signing preferred — users already have the key). Every op is attributable and tamper-evident, which matters increasingly as agents become review actors.

### Object types (decided, WRIT-184)

Writ hard-codes exactly one object type: `schema`. That is the bootstrap and the only permitted exception — every other type, `review`, `comment`, `issue`, `project`, `cycle`, `document`, `label`, `workflow-state`, `settings` included, is declared by a `schema` object written into the log, not baked into writ's spec (see §Schema layer below). Object IDs and cross-references are globally unique (`<repo-id>#<object-id>` or bare `<object-id>` for repo-local references, where IDs are 128-bit random lowercase hex strings; decided and spec'd in `spec/identifiers.md`, WRIT-16), with the qualified form an opaque pointer to an object that may live in another repo, so "issue in repo A fixed by review in repo B" is representable regardless of which layer defines "issue" or "review" — the one-graph query is the point. Every object, whatever its schema-declared type, homes in the single repo the client is operating on when it's created (see §Object homing below).

The SDLC vocabulary this hard-coding replaces still ships today, unmoved, across eight files: `review` (base/head, revisions, status, approvals, ci-statuses) in `spec/review-ops.md`; `comment` (threaded, anchored) in `spec/comments.md`; `issue` in `spec/issue-ops.md`; `project` and `cycle` in `spec/project-cycle.md`; `document` in `spec/documents.md` (see §Document concurrency model); `label` in `spec/label-ops.md`; `workflow-state` in `spec/workflow-state-ops.md`; `settings` in `spec/settings-ops.md`. Those files remain the normative reference for that vocabulary until WRIT-194 deletes it from the spec, once a consumer can declare the same types as a `schema` object instead.

### Schema layer (decided, WRIT-184)

Writ's scope test changes: no longer "does this data explain how the software got made?" — that is a question for whatever is declared above writ — but **"is this git-shaped or merge-shaped?"** Anchors and git object ids are git-shaped and stay in writ's spec, as value types in WRIT-185's closed catalogue. Issue states, review verdicts, and the rest of the SDLC vocabulary are not git-shaped — they are ordinary schema-declared data — and go.

A schema needs two orthogonal axes to describe a field, and writ settles both:

- **Merge strategies are closed and already correct.** The nine-strategy catalogue in `spec/fold.md` §5 ships as-is; nothing here adds a strategy. In particular, no counter joins the catalogue — no field needs one, and it is the one CRDT that cannot be derived from the DAG's total order. Fractional indexing, the case a counter might tempt someone to reach for, is already solved as an LWW scalar (`spec/ordering.md`, `engine/order`), not a list CRDT.
- **Value types are a second, orthogonal axis.** A merge strategy alone is not a schema: `title: lww` says how concurrent writes to `title` reconcile, not that `title` is a string. Value types close that gap: a closed catalogue of 12, orthogonal to the strategy catalogue, specified in `spec/value-types.md` (WRIT-185).

`schema` is the one object type writ hard-codes (§Object types above) because a schema has to exist before anything else can be typed; every other type a repository uses is data written by a `schema` object, folded like any other object (WRIT-186). Schema evolution needs no migration planner: field rules are already keyed by `(op_type, op_version, field)`, so changing a field's merge strategy is a version bump, not an edit. But the bump MUST declare a distinct `target` (`spec/fold.md` §5) when it changes `strategy`, and MUST still agree on `lattice` if it keeps the same target: the generic fold groups matched rules by target key alone, not by `op_version`, and instantiates one accumulator from whichever rule a caller's slice lists first, so reusing a target across a strategy change is order-dependent rather than mixed-version support, and so is reusing one across a `lattice` change, since the `lattice` accumulator reads that attribute at fold time — `engine/schema.go`'s resolver rejects both. Old ops keep folding under their own version's rules because each version's rule set lands under its own target. There is no destructive schema change to plan for.

**A field attribute cannot be cleared, and that's a decision, not an oversight (decided, WRIT-200).** `define-field`'s body carries `value_type`, `enum`, `max_length`, `key`, `key_types`, `lattice`, and `target` only when set, and each folds as its own independent `keyed-lww` register — overwritten only when a later op's body actually carries that key. Append-only cuts both ways here: it's exactly what makes the vocabulary unable to represent "this attribute is now absent." The asymmetry is worth stating plainly: **widening** a field — declaring `max_length` where none stood, say — is an ordinary redeclaration under the same `op_version` and applies cleanly, because the new op's body carries the key. **Narrowing** — tightening `string(200)` to `string`, or an `enum` back to a bare `string` — has no representation: an op whose body omits the key doesn't clear the old one, it leaves it standing forever, and WRIT-191's round-3 review caught this producing exactly that corruption (a stale `enum` a real schema author had already removed from their source, permanently withholding the field's rule) plus a `plan`/`apply` loop that never converged. Three fixes were on the table: emit every attribute unconditionally (nulls for absent ones), add a dedicated clearing op, or accept the limitation. The first two change the wire vocabulary — spec text, JSON schema, `field-rules.json`, `state.SchemaRules`, and fixtures move together either way — to buy a capability against an installed base of zero (AGENTS.md: nothing has shipped, so there is nothing to be compatible with and no reason to widen the vocabulary pre-emptively); a clearing op additionally needs a concurrency story (a clear racing a redeclaration is a conflict shape the vocabulary doesn't otherwise have). The decision is to **accept it**: narrowing is not a new problem, it's the same "nothing is ever removed" constraint already governing a `strategy` change above, so it takes the same recipe — declare the field again under a new `op_version`, with a distinct `target`. `writ schema plan`/`apply` (WRIT-191) refuse a narrowing edit outright rather than silently drop it, naming the field and the attribute, and pointing at this recipe. Normative detail and the fold-level fixture pinning the underlying mechanic: `spec/schema-ops.md` §8.1.

The schema DSL declares **types, value types, merge strategies, and relations. Nothing else.** A relation is an `object-ref` value type (WRIT-185) that points at another object's id — not a separate grammar bolted onto the DSL; parsing it is WRIT-187's job, the same as any other field. Writ has no join engine and no reference resolution: what an `object-ref` points at is the consumer's problem to resolve, exactly as a qualified `<repo-id>#<object-id>` reference is today (§Object homing, WRIT-180). No computed fields, no hooks or triggers, no expressions, no permissions, no logic in defaults, no imports or inheritance. AGENTS.md calls framework-building a bug; this fence is written down now, while the DSL is still small, rather than after something has grown past it.

There is no `.writ/` config directory. `writ.schema` is a working-tree source form, Prisma-style — a convenience for authoring and reading a schema — but the log stays the source of truth: a schema is folded from `schema` ops like everything else, and `writ.schema` is a view onto that state, not a second store of it. This is what preserves WRIT-110's settings-as-ops design: settings are still data in the log, now typed by a schema instead of hard-coded.

`writ.schema`'s surface syntax is informative and lives in `engine/schemasrc` (WRIT-187), a public package: `spec/schema-ops.md`'s op vocabulary is the normative artefact, and an independent implementation has to agree on the ops that package compiles to, not on the grammar it parses. The round-trip promise is semantic, not textual: the op vocabulary has no slot for a comment or for source order (`define-field`'s body carries no `description`, and the fold sorts canonically), so comments and layout are lost through the log rather than round-tripped. Comments are not data — `description "..."` is — and a separate comment-preserving `Format` rewrites a source file in place for the case that actually needs the comments back: the file itself, not the log.

None of this touches the substrate. `spec/op-envelope.md` already declares `object_type` an open string, not a closed enum, and producer validation rule 3 already reads "the payload satisfies the vocabulary schema for its `object_type`, for every object type the producer itself emits" — already schema-shaped prose. `spec/ref-layout.md` already permits extension chains (`refs/writ/<writer-id>/custom-type`). And `engine/internal/fold` is already a generic driver — `Fold(ops []Op, rules []Rule) → ObjectState{state map[string]any}`, rules keyed by `(op_type, op_version, field)` against a closed strategy catalogue. What is hard-coded today is everything layered above that driver: the vocabulary spec prose, most of the JSON schemas and `field-rules.json` tables, `engine/state`, the per-type engine facades and projection tables, and the per-type CLI. That is what the follow-up tickets delete.

### Object homing (decided, WRIT-180; supersedes WRIT-113)

Every collaborative object has exactly one **home repo** — the repo whose `refs/writ/*` carry its ops. This is structural, not aesthetic: per-writer refs make pushes conflict-free only because everyone collaborating on an object pushes to and fetches from the same remote; an object whose ops could accumulate "additively" across several repos would need something to replicate them between remotes, and there deliberately is no such thing (no server, no P2P). That argument survives WRIT-113 intact, and it is why decision 1 below is not a loss of anything: an object always needed exactly one home, and the only thing that changes here is who chooses it.

Four decisions, reversing WRIT-113's designated-home model:

- **One repo, one home, no routing.** The home is the repo writ was opened on. An issue created while standing in repo A lives in repo A. There is no configured elsewhere, no transparent second store, no routing branch inside writ. Writ never writes to a repository other than the one you are standing in; git's version of naming another repository is `remote` — a name, a URL, and the user saying which. A team that wants all its issues in one repo achieves that by running writ against that repo — an operational choice made above writ, not a role writ assigns.
- **Qualified references stay; resolution goes.** `<repo-id>#<object-id>` remains in the format, along with repo-ids and the reference parser (`spec/identifiers.md`). What goes is the machinery that turns a repo-id into a slug, a remote URL, or a local path. The precedent is git itself: git records a SHA for an object it does not have, and does not ship a service to go find it. A qualified reference is an opaque pointer meaning "this object is not here" — nothing more. Resolution is the higher layer's problem.
- **Higher layers extend the format without changing the spec.** A repo registry, or any other cross-repo grouping a consumer wants, is exactly the shape the forward-compatibility rules already cover: unknown op types and fields are preserved and ignored (`FC-5`, `FC-12`, `FC-15`; `spec/forward-compatibility.md`), generically, with no writ-specific carve-out. A consumer defines its own object type for its repo registry and writ carries those ops unread. This needs no new spec rule — it is what the forward-compatibility rules already say and are already tested for, generically. It is also what keeps VISION.md's open-core line where it is: a repo registry needs no infrastructure to run, so keeping it out of writ's spec but inside the format (via an unknown, unread object type) avoids nudging a no-infrastructure feature behind a hosted service. With the schema layer (§Schema layer above), a higher layer extending the format this way is the ordinary case, not a demonstration: schema objects are what extension is for.
- **Multi-repo read aggregation is not writ's.** WRIT-113 decision 4 said a client folds `refs/writ/*` from every registered repo into one projection. It was never implemented, and it is now explicitly not writ's job. The projection stays one repo's refs; a client that wants a cross-repo view builds it above writ.

One piece of WRIT-113 is not reversed, only reworded: **one team, repo-global configuration.** A repo's workflow states, labels, and settings are repo-global — no `team` object type, no team scope on anything, in v1. Multi-team arrives later, additively, if demand proves out: a `team` object plus an optional `team` field on scoped objects; old clients ignore the unknown field (degraded to showing everything, per forward-compatibility), and pre-existing scopeless objects fold as belonging to a default team.

### Public issue intake: a bridge, not a format feature (decided, WRIT-111)

Writ structurally does not solve anonymous public issue intake at the format level. Writing any writ op requires push access to `refs/writ/<writer-id>/*`. A stranger filing a bug on an open-source project does not have push access, and Writ provides no authorization model or anonymous write path at the spec layer.

The settled answer is an **intake bot or bridge**: a designated writer with push access that accepts reports from a public webhook, form, email, or forge issues, and writes them into the repository as ordinary signed ops. To attribute external reporters truthfully without synthesizing fake email addresses, bots use the `user:` person identifier scheme (such as `user:github-octocat` or `user:<service>-<id>`) defined in `spec/identifiers.md` (WRIT-102). Alternatively, open-source projects may keep GitHub Issues as their public front door and bridge accepted issues into Writ.

### Anchoring (the hard problem)

Line comments anchor to **content** (blob hash + hunk context), not line numbers, so they survive force-pushes and rebases as well as possible; when re-anchoring fails, comments degrade to "orphaned but preserved," never silently lost. The format (`spec/anchors.md`, WRIT-13) is dual-sided, following Radicle's `CodeLocation`: an anchor carries an `old` and/or `new` side — each a (commit, path, blob, line-range, captured-context) tuple — because deleted-line comments and GitHub's cross-side ranges are not representable as a single blob position. This gets its own spec section and its own fixture family; expect iteration.

Anchor _resolution_ is deliberately not part of the fold. Resolving an anchor requires blob access, and its result legitimately changes when a code branch moves even though no ops changed — so it lives in its own machine (#4 below): a pure function `resolve(anchor, target tree) → position | orphaned`, per the spec's re-anchoring rules (`spec/resolution.md`, WRIT-14), invoked by the projection at materialization time. The fold carries anchors verbatim as data, which keeps `fold(ops) → state` branch-independent and keeps fold goldens stable; the orphaned-anchors fixture family binds to the resolver's output.

## The six machines (engine internals)

1. **Op codec** — domain action ⇄ canonical signed payload in a commit. Owns canonicalization and signature verification.
2. **DAG store** — append op with parents; enumerate an object's ops across all writer refs; topological ordering. Pure object-database work.
3. **Fold** — the heart. One generic reducer, not one per type: ops plus declarative rules in, materialized state out (`Fold(ops, rules) → ObjectState{state map[string]any}`), concurrent-edit resolution exactly as fixtures dictate (last-writer-wins with op-id tiebreak as the default rule, where "last" is position in the causality-monotone total order; per-field rules where the schema says so, drawn from the closed strategy catalogue in `spec/fold.md`). Pure functions, no I/O, thoroughly testable. Must be boring and correct. Rules can now be drawn from a folded `schema` object (WRIT-186): `engine/schema.go`'s `Store.Schema` folds every `schema` object in the log with the engine's built-in bootstrap rules (`state.SchemaRules`, the one table that never comes from the log), and its pure resolver `RulesFromSchemas` turns the result into the same `[]Rule` shape the driver already consumed generically — validating each rule through `spec.ValidateFieldRule` before it can reach `NewAccumulator`, and withholding rules for an `object_type` two schema objects both bind. The shipped SDLC vocabulary still drives its fold from the hard-coded `field-rules.json` tables (`spec/fieldrules.go`) until WRIT-194 deletes them; the driver itself never changed — it always took `rules []Rule` from wherever the caller supplied them.
4. **Anchor resolver** — pure re-anchoring: `resolve(anchor, target tree/blob contents) → resolved position | orphaned`, per the spec's re-anchoring rules (`spec/resolution.md`, WRIT-14). No I/O — trees and blobs are inputs. Invoked by the projection, never by the fold (see §Anchoring for why); orphan results retain the original anchor for possible re-attachment.
5. **Projection** — SQLite cache keyed by ref tips: on read, diff tips vs. last fold, fold only deltas, serve queries from indexed tables; still tip-keyed, still droppable/rebuildable, never a source of truth. Schema-generated tables are WRIT-189's target, not what ships today: `engine/projection/schema.go` still hand-writes 30 `CREATE TABLE` statements — 24 hand-written per-type vocabulary tables (`review` alone owns seven: `reviews`, `review_revisions`, `review_assignees`, `review_labels`, `review_links`, `approvals`, `ci_statuses`) plus six substrate tables (`meta`, `chain_tips`, `code_tips`, `ops`, `objects`, `unknown_ops`) that are type-agnostic and must survive WRIT-189 unchanged. Also home of local-only state: drafts (kept out of shared refs, following Gerrit's draft-handling precedent — publish on intent, never per-keystroke commits), read/unread, sync cursors. An optional DuckDB/Parquet exporter hangs off the same op-log for analytics — derived only, never committed.
6. **Sync plane** — manages refspecs, invokes **system git** for fetch/push, reports per-remote status ("n ops unsynced"). Thin by design; per-writer refs made conflicts structurally impossible.

Machines #1 (op codec), #2 (DAG store), #4 (anchor resolver), and #6 (sync plane) are untouched by the schema layer: they operate below or beside the type system, not on it.

Alongside the machines sits one small shared component: **writer identity** — derivation of the current writer-id (user, device) per the ref-layout spec and signing-key lookup from existing git config. Every append and every sign consults it; `writ init` writes the config, the engine reads it, and nothing identity-shaped is implemented above the engine (the CLI and downstream clients/bridges all consume this component).

## Public API shape

Schema-shaped, never git-shaped — callers see no SHAs or refspecs unless they ask. `Store.Objects`, `Store.Query.Objects`, and `Store.Types` (WRIT-192) are that surface. The typed per-type services (`Store.Reviews`, `.Issues`, `.Comments`, `.Documents`, `.Labels`, `.WorkflowStates`, `.Settings`, plus their typed `Query` readers) are gone (WRIT-195); the generic surface below is the only one that remains:

```go
store, err := writ.Open(path, opts...)         // any git dir: clone, bare, worktree; fully offline
store.Objects.Create(ctx, objectType, op)      // append the creating op for a schema-declared type
store.Objects.Apply(ctx, objectID, op)         // append a further op against an existing object
store.Objects.Get(ctx, objectID)               // folded state as an Object, per the schema
store.Query.Objects(filter)                    // query across types, served from the projection
store.Schema(ctx)                              // the `schema` objects present in the log
store.Types(ctx)                               // the vocabulary actually in effect: built-ins overlaid by the log
store.Drafts.Save(ctx, draft)              // .Get / .List / .Discard / .Publish (local-only state)
store.ReadState.Mark(ctx, objectID)        // .Clear / .Unread (local-only state)
store.Ref(objectID)                        // returns <repo-id>#<object-id> when repo-id is configured
store.Sync(ctx, remote)                    // ensures refspecs, fetches, pushes, refreshes
store.SyncStatus(ctx, remote)              // per-remote unsynced op count
store.Refresh(ctx)                         // explicit projection refresh
store.Rebuild(ctx)                         // explicit drop-and-rebuild of folded projection cache
store.Watch(ctx)                           // <-chan Event (reactive event stream on writes/refolds)
```

`op` is a `writ.NewOp{Type, Version, Fields}`: `Fields` is keyed by declared field name on the way in, but `Objects.Get`'s `Object.Fields` reads back keyed by TARGET key — a rule's declared `target` when it has one, otherwise its field name — which differs whenever a rule declares one (WRIT-198's `assign.add`/`assign.remove` both write field `add`/`remove` and both read back under `assignees`). `Object` deliberately omits the op-level `TotalOrder`/commit ids `ObjectState` carries: a caller who supplied only an object id never asked for a SHA. `writ.Fold(ops, rules)` still returns the full `ObjectState`, since a caller passing ops in has asked for op-shaped output.

The shapes callers see come from the schema folded out of the log, not from Go structs writ ships: `engine/state`'s per-type structs (`Review`, `Comment`, `Issue`, …) are gone (WRIT-195) — `Anchor` stays, since it is content-based data every fold carries verbatim, not a per-type shape — and `store.Objects.Get` returns the generic folded map the schema describes for any declared type, including one no Go type is named after. Typed codegen — generating a consumer's own Go structs from its schema — is deliberately deferred until a real consumer needs it; the DX regression in the meantime (map access instead of typed fields) is a known, accepted cost, not an oversight.

`store.Query.Objects` serves filtered, cross-type queries directly from the SQLite projection cache, whose tables are themselves generated from the schema (WRIT-189) rather than one hand-written table per type. All operations automatically refresh the projection unless disabled via `writ.WithoutAutoRefresh()`.

`store.Watch(ctx)` returns a receive-only channel of schema-shaped change events (`<-chan Event`) emitted on local writes and post-fetch refolds. Events are published only after projection transactions commit, ensuring state is queryable immediately upon receipt. Each subscriber receives events over an independent 128-element buffer under a non-blocking drop policy: if a consumer falls behind, intermediate events are dropped and a single `reset` event is delivered once capacity is available, prompting the consumer to re-query the full state.

Everything above the engine — CLI, downstream TUI or web viewers, GitHub bridges, any hosted service — is a consumer of this one interface, distinguished only by rendering surface. Nothing gets private powers.

## Language: Go (decided; rationale preserved)

We seriously considered Rust; its three advantages dissolved under this project's constraints. (1) Bindings: the CLI in `--json` plumbing mode is the universal API — every language and agent can shell out to one static binary, which Go distributes exceptionally well; cgo-exported shared libraries remain a later option if in-process bindings are ever needed. (2) Git libraries: we take a **hybrid approach** — go-git (mature, pure Go) for local object I/O; **system git for all transport**, which is also git-appraise's approach and is quietly the right engineering call, because SSH agents, credential helpers, gitconfig, proxies, and enterprise auth setups all work for free. (3) Type-safety-for-correctness: the conformance fixture suite is the correctness story, not the compiler. Meanwhile the costs of splitting languages were real: a Rust core underneath means a cgo seam, two toolchains, and a higher barrier for the contributor who starts in a Go client/tool and ends up fixing an engine bug. One language, one `go build ./...`. A Rust (or Python, or TypeScript) implementation of the _spec_ by others would be genuinely welcome — an independent second implementation is the best proof a convention stands on its own.

## SQLite driver: pure Go (decided; rationale preserved)

The projection (six machines, #5) uses `modernc.org/sqlite`, not
`mattn/go-sqlite3`. `mattn` is faster on bulk insert — a spike benchmark on
a projection-shaped workload (5k reviews, 100k comments, one bulk-insert
transaction plus indexed reads by review) measured cgo at roughly 1.4–2.2x
there, with indexed reads too close to call (overlapping ranges across 10
runs) — but both are comfortably fast in absolute terms (sub-second full
refold, sub-tenth-millisecond point lookups), and cgo would compromise the
reason Go was chosen over Rust in the first place:
"the CLI in `--json` plumbing mode is the universal API ... which Go
distributes exceptionally well" (see Language section above). A cgo
dependency in the projection means the release matrix (WRIT-58: linux/macos/
windows × amd64/arm64) needs either a from-scratch build host per target or
a C cross-compiler toolchain wired into CI and kept working, plus musl for
genuinely static Linux binaries since static-linking cgo against glibc
breaks NSS lookups at runtime. `CGO_ENABLED=0` cross-compilation has none of
that: it's `GOOS`/`GOARCH` and nothing else, from any host. Full benchmark,
method, and reproduction steps: `docs/spikes/writ-60-sqlite-driver/`.

## Document concurrency model: sections, multi-value registers, conflicts as data (decided; rationale preserved)

`multi-value` is a catalogue merge strategy (§Schema layer above), and `document` is a schema-declared type like any other — this section is no longer ahead of specifying one. Settled design for how Writ handles concurrent edits to long-form text. Recording the reasoning because three plausible alternatives were considered and rejected, and each will look attractive again to whoever picks this up.

### The decision

**Documents are split into sections, and each section's body is a multi-value register.** On concurrent edits, every version is preserved. A later edit that causally observes them collapses back to one. The fold never merges text and never picks a winner.

```
settled:     body = "..."
conflicted:  body = ["...", "..."]     both preserved, neither invented
```

**Clients render the conflict.** Presentation may show git-style `<<<<<<<` markers, a side-by-side view, or an interactive version picker — presentation is strictly a client choice. Resolution is an ordinary edit op that causally follows both versions.

This is the same line already drawn twice: merge queues and branch protection are coordination services; collaborative editing is a client capability. Sophisticated machinery lives above the format; the format stays boring and deterministic.

### Why not a sequence CRDT (Yjs, Automerge, Loro)

They are excellent and they are the wrong dependency here.

- **Their updates are opaque binary.** Op payloads would stop being canonical JSON — which is what signatures and content-addressing are computed over.
- **VISION says no.** "No binary storage formats for the canonical data — binary means no diff, no merge, no delta compression."
- **It would make Yjs the spec.** Conformance fixtures containing binary CRDT updates mean an independent implementation must reimplement that library byte-for-byte to conform. For a project selling a neutral, independently-implementable convention, that is not a dependency but a surrender.
- **CGO.** Every mature option reaches Go through a Rust core, which undoes WRIT-60's pure-Go decision and the six-target static release matrix that followed from it.

A sequence CRDT is still the right tool for **live co-editing inside a client session** — ephemeral, in-memory, never durable. That option stays open and costs the format nothing.

### Why not three-way merge in the fold

Tempting — it is what git does, the audience resolves conflicts routinely, and conflict markers are more honest than a CRDT silently interleaving two people's paragraphs into a sentence neither wrote.

The problem is conformance surface. "Three-way merge" in a spec means an independent implementation must produce **byte-identical** output, which requires pinning: the diff algorithm (Myers, histogram and patience produce different hunks), the merge variant (`ort` vs `recursive`, `diff3` vs `zdiff3` markers), exact marker syntax, and a reduction for **N** concurrent edits — three-way merge takes exactly two sides and one base, and the op DAG can produce five. That is plausibly a larger spec than everything else combined, for a feature that is not the core product.

Multi-value register gets the same user-visible outcome — *two humans disagreed, you decide* — with nothing to specify beyond "keep them all."

### Why sections rather than whole documents

A conflict should be scoped to the paragraph two people both touched, not the whole document because someone fixed a typo in the intro. Same reason git conflicts are per-hunk.

Sections also give comments somewhere to anchor, the same way comments anchor to code, which makes document review work like code review rather than being a second mechanism.

### One property better than git

In git, whoever merges resolves. Here there is no merge event — the conflict appears in everyone's fold simultaneously, so **anyone can resolve it** with a normal edit. That falls out for free and is worth stating in the spec, because it is genuinely nicer and readers will expect git's rule.

### Open questions for the implementing ticket

- Are sections their own collaborative objects, or an ordered structure inside one document object? Sections-as-objects makes conflicts and anchoring natural; it also multiplies object count.
- Section ordering uses fractional indexing, same mechanism as workflow-state positions.
- Does multi-value register become a general per-field strategy available elsewhere, alongside `lww` and `set-observed-remove`? Probably yes — any field where silent loss is unacceptable wants it — but it is overkill for a title.

## Repo strategy: one monorepo

Everything open lives in a single Apache-2.0 monorepo because the spec, engine, and CLI are one contract with several expressions: a fold-rule change should be one atomic PR touching spec text, fixtures, engine, and CLI together. Split repos invite version skew between fixtures and implementation — the exact failure that erodes trust in a convention. Layout:

```
/spec          — convention doc, JSON schemas, conformance fixtures (the real standard)
/engine        — codec, dag, fold, resolve, projection, sync (public Go API at the root package)
/cmd/writ      — CLI: porcelain for humans, --json plumbing for scripts/agents
/docs
```

Downstream clients (TUIs, web viewers, GitHub bridges, hosted services) live in separate downstream repositories consuming the engine's public Go API (`github.com/writtendev/writ/engine`) or the `--json` CLI plumbing. Keeping the public API strong enough that downstream tools never need private hooks is a deliberate design constraint: it keeps the convention honest. `spec/` can graduate to a neutral home once independent implementations exist and governance is worth formalizing.

That "ordinary pinned Go module" is one monorepo-wide `go.mod` at the repo root, module path `github.com/writtendev/writ`, covering `/engine` and `/cmd/writ` — not a separate module for the engine. A consumer that imports `github.com/writtendev/writ/engine` gets only the engine and its direct dependencies; `engine/internal/` enforces the API boundary whether `engine` sits in its own `go.mod` or as a subtree of one. A second module and a `go.work` file are deferred until a real external consumer needs independent engine versioning (decision and full rationale: `docs/module-boundary-decision.md`, WRIT-61).

## Spec = fixtures

The standard is not the markdown; it's the conformance corpus: fixture repos exercising every tricky configuration (concurrent edits on every type, multi-device writers, orphaned anchors, unknown op types, future versions, malformed signatures) plus golden folded outputs any implementation must reproduce. Fixtures are the ground truth that lets an independent implementation claim compatibility. Fold determinism is fixture-enforced byte-for-byte.

Schema-parametric fixtures are WRIT-190's target, not what ships today: the corpus still assumes one hard-coded vocabulary. WRIT-190 will make a fixture carry its own schema alongside its ops, asserting "given this schema and these ops, the fold produces X" instead. Be honest about the trade — this will be a *wider* conformance surface, not a narrower one, since it will span the cross-product of value types and merge strategies rather than one fixed set of typed fields.

## Known-hard list (tracked, not feared)

Anchoring across force-pushes; canonical encoding; compaction/GC for unbounded op history; repo permission semantics; identity mapping (signing key → directory identity), which is deliberately out of spec scope; host ref-namespace compatibility (the foundational spike — some namespaces like `refs/pull/*` are read-only on some hosts; verify `refs/writ/*` across GitHub, GitLab, Bitbucket, Gitea/Forgejo, Codeberg, and bare-SSH before anything else, with a branch-namespace-encoding fallback sketched in case any major host is restrictive).
