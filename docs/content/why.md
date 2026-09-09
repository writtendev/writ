---
title: "Why Writ"
slug: "why"
description: "Half of every pull request lives in a platform database. Writ is a documented convention for putting it back in the repository, as signed append-only operations."
---

# Why Writ

## Half of every pull request is missing from your clone

You can clone the code half of every review your team has ever done. Commits,
branches, diffs — content-addressed, signed if you signed them, yours forever,
identical on every machine that fetched them.

You cannot clone the other half. The review threads, the approvals, the
line comments, the "let's do this differently and here's why" that explains
the shape the code ended up in — that lives in a platform database. You reach
it through an API, with a token, under a rate limit, and it does not come with
you when you move hosts.

This asymmetry is not a law of nature. The pull request is a platform
construct, not a git concept. Forges keep the code half of every PR in refs
because git gave them somewhere to put it, and keep the conversational half in
Postgres because nobody ever specified where else it should go.

Writ specifies where else it should go.

## What it actually is

Writ declares no types of its own. Every type your team works in — a ticket, a
review, a comment — is declared as data, in a `writ.schema` file you own, and
each object of one is a **collaborative object**: a DAG of small, signed,
immutable **operations**, each stored as an ordinary git commit under
`refs/writ/*`. Current state is never stored authoritatively. It is derived, by
deterministically folding an object's operations in causal order.

```console
$ writ init                        # fetch refspecs, plus a starter writ.schema
$ cat > writ.schema <<'EOF'         # replace the starter: your vocabulary, as data
namespace acme
type ticket {
  op create 1   { title string(200) lww }
  op revision 1 { base git-oid lww  head git-oid lww }
}
type gadget {
  op create 1 { subject untyped lww  text text multi-value }
}
EOF
$ writ schema apply                # signs and appends the ops declaring them
$ writ object create ticket create -field title="Add rate limiting"
$ writ object apply <id> revision -field base=<base-sha> -field head=<head-sha>
$ writ object create gadget create -field-json subject='{"object_type":"ticket","object_id":"<id>"}' -field text="this allocates in the hot path"
$ writ sync                        # git push, to your own ref namespace
```

That is the whole deployment story. No server, no database, no webhook, no
account. `writ init` adds one line to `.git/config`
(`remote.origin.fetch = refs/writ/*:refs/remotes/origin/writ/*`) and from then
on an ordinary `git fetch` carries review history along with the code.

Four design choices carry most of the weight:

**Per-writer namespaces.** You only ever push to `refs/writ/<your-writer-id>/*`.
Nobody else writes there, so your push can never non-fast-forward against a
colleague's. The entire class of push conflicts disappears — which is what
lets the sync layer stay thin enough to be boring.

**Fold, not state.** Concurrent edits don't conflict; they coexist as sibling
operations and reconcile at read time under rules the spec defines
(last-writer-wins with an op-id tiebreak by default, per-field rules where it
matters). Fold is a pure function — operations in, state out, no I/O — which
is why it can be tested byte-for-byte against fixtures.

**SQLite as a cache, never as truth.** Reads are served from a projection
keyed by ref tips: fold only the delta since last time, serve queries from
indexed tables. Delete it and nothing is lost; it rebuilds from the log.

**Unknown operations are preserved, never dropped.** An implementation that
encounters an operation type it doesn't understand must carry it forward
untouched and surface it in materialized state. Old clients must not destroy
new clients' data. This is the property that lets the format evolve without a
flag day, and it is fixture-enforced.

Because every operation is a signed git commit, every review action is
attributable and tamper-evident — using the SSH key you already sign commits
with, through git's existing signature machinery.

## We are not the first people to think of this

This idea has a lineage, and being precise about it matters more than sounding
original.

**[git-appraise](https://github.com/google/git-appraise)** (Google, ~2015)
proved the mechanics: reviews as git objects under `refs/notes/devtools/*`,
one JSON object per line so git's `cat_sort_uniq` merge strategy resolves
concurrent writes, zero server-side setup, any host. Adoption stayed niche.
Part of that was timing, and part was git-notes: notes don't fetch by default,
and notes attached to commits are orphaned when rebase rewrites those commits.
**What we took:** the line-oriented mergeability insight, and the conviction
that "any host, no server" is achievable. **What we changed:** plain refs
instead of notes, for exactly the two reasons above.

**[Gerrit NoteDb](https://gerrit-review.googlesource.com/Documentation/note-db.html)**
is the production-scale existence proof. Gerrit migrated off SQL entirely —
status, votes, comments, all of it in git meta refs beside the code — gaining
atomic replication, automatic audit history, and plugin extensibility with no
schema migrations. At very large scale, for years. **What we took:** the
confidence that git-as-review-database is not a toy, and the special-repo
pattern for org-scoped data. **What we changed:** nothing, really. Gerrit
proved the substrate; we're pointing it at repositories Gerrit doesn't host.

**[Radicle](https://radicle.xyz/)** is the most complete modern design in this
space and our primary architectural inspiration. Its collaborative objects are
CRDTs implemented as DAGs of signed operation-commits, with per-writer
namespaces and a SQLite cache materializing current state. **What we took:**
essentially the whole shape — op-log, per-writer refs, signing, fold-to-cache.
**What we changed:** Radicle pairs this data model with a peer-to-peer network,
because its mission is sovereign serverless collaboration. Writ has a narrower
goal — working with the git hosting people already pay for — so we use the
existing remote as the sync point and omit the networking layer entirely.
Different missions, shared foundations, and their design is worth reading on
its own terms.

## What changed since 2015: agents

Earlier attempts had a weak answer to "why move review data?" The honest
version was aesthetic — it *should* live with the code. That is not a reason
most teams will act on.

There is a concrete one now. An agent working in a repository has one clone.
If the SDLC history is in that clone, the agent has the ticket, its
discussion, the review that closed it, and the code — co-located with the
artifact it is editing, available with `git fetch`, with no API token, no
integration, and no rate limit. Review history is among the highest-value
context you can give a coding agent, and today it sits behind exactly the
interfaces agents are worst at using.

The signing story compounds this. As more review actions are taken by
non-humans, "who approved this, provably" stops being a compliance checkbox
and starts being an operational question.

## Showing the work: why chains and not one ref per object

The natural design — Radicle's design — is one ref per collaborative object.
It is clean, it makes `git for-each-ref` a legible index, and it is what we
started with. We ended up somewhere else, and the reasoning is the most
load-bearing decision in the project.

Git re-advertises **every** ref matching your fetch refspec on **every**
fetch. There is no incremental advertisement. So if each comment is an object
with its own ref, a mature repository's quiet background sync pays for the
whole namespace, forever.

We measured it, against real GitHub and a `file://` control
([full method, scripts and data](https://github.com/writtendev/writ/tree/main/docs/spikes/writ-69-ref-scaling)):

| layout | refs | no-op fetch (GitHub) | `file://` control | advertisement |
|---|---|---|---|---|
| per-writer chains | 1,200 | 0.96 s | 0.11 s | 85 KB |
| per-object | 1,000 | 1.00 s | 0.12 s | 116 KB |
| per-object | 10,000 | 1.20 s | 0.44 s | 1.13 MB |
| per-object | 30,000 | 1.57 s | 1.28 s | 3.40 MB |
| per-object | 100,000 | *not reachable* | 13.6 s | 11.3 MB |
| per-object | 500,000 | *not reachable* | 87.5 s | 56.6 MB |

Advertisement cost is exactly linear — 118.75 bytes per per-object ref — so
the 100k and 500k byte figures are arithmetic, and their times are bounded
below by the control column, which contains no network at all. Those two rows
are extrapolated, and labelled as such in the spike.

Three things fall out of this:

**The recurring cost is real and the client is worse than the wire.** The
`file://` control goes 0.44 s → 1.28 s → 13.6 s → 87.5 s across 10k → 30k →
100k → 500k refs. That 30k→100k step is 3.3× the refs for 10.6× the time.
Ref processing is superlinear in this range, so a faster pipe does not save
the layout.

**The host's write path fails before its read path does.** A single push
creating 9,000 refs was refused outright by GitHub — every ref rejected with
`Internal Server Error`. Chunked into 2,000-ref pushes it succeeds, but
per-ref cost climbs with the repository's total ref count: ~24 ms/ref at 1k,
~68 ms/ref at 30k. Pushing one 20,000-ref increment took 22.6 minutes of
continuous pushing. Backfilling a mature repository's history into per-object
refs on a real host is an hours-long, failure-prone operation *by
construction*.

**Onboarding degrades too.** First fetch of the writ refs into a clean clone:
1.7 s at 1k refs, 4.8 s at 10k, 21.3 s at 30k. At 100k that is minutes added
to every new clone and every CI runner that wants review data.

Per-object refs stay comfortable below roughly 10,000 total. Their own
realistic scale — every comment on five years of a busy repository — is
100,000 to 500,000. That is one to two orders of magnitude of headroom in the
wrong direction, and the costs are structural rather than tunable.

So Writ stores operations as **append chains**:
`refs/writ/<writer-id>/<type>` points at that writer's latest operation of
that type, each operation's git parent being their previous one. Which object
an operation belongs to rides in the payload, never in the ref name. Ref count
becomes O(writers × devices × types) — around 1,200 for a 300-person
organization — and, crucially, stays constant as review activity grows, because
appending moves an existing ref instead of creating a new one.

The cost we accepted: reading one object without a projection means walking
every writer's chain and grouping by object id. Per-object legibility moved out
of `git for-each-ref` and into `writ` plumbing. That is a real loss, and it is
the right trade.

The precedents that use per-object refs — Gerrit, Radicle — escape this by
controlling their own servers. Writ never will. That difference in mission is
what produced a different answer from the same starting point.

## Which hosts this actually works on

The host-agnostic claim deserves precision, because we have not verified it
everywhere.

| Host | Status |
|---|---|
| GitHub | **Verified.** Push, `ls-remote`, fetch by refspec, delete. |
| Gitea (self-hosted) | **Verified**, including survival of a forced aggressive GC. |
| Bare git over SSH | **Verified**, including forced GC. |
| Forgejo / Codeberg | **Inferred** from the Gitea result. Codeberg itself was never pushed to. |
| GitLab | **Not verified.** No push was made. Documentation indicates its restrictions are scoped to its own internal namespaces. |
| Bitbucket | **Not verified.** Same caveat. |

One gap is worth naming specifically: on GitHub we confirmed push, fetch and
delete, but **not** survival of server-side GC — that runs on GitHub's
schedule and could not be triggered or observed in a single session. Nothing
in git's model or GitHub's documentation suggests reachable refs in a
third-party namespace are treated specially, but that is inference, not
observation.

As a control, pushing to `refs/pull/999999/*` on GitHub was rejected with
`deny updating a hidden ref` — which confirms the probe would have detected a
`refs/writ/*` restriction if one existed.
[Full results.](https://github.com/writtendev/writ/blob/main/docs/ref-namespace-host-compatibility.md)

## What we are deliberately not building

Not a forge. Not git hosting. Writ does not compete for where your repository
lives; it rides along inside it as one more git client.

No peer-to-peer networking — Radicle is doing that mission properly and we
have nothing to add.

No project-management feature parity, and no project-management vocabulary
either: issues, projects and cycles are types a consumer declares, not types
writ ships. What the spec owes them from day one is the substrate they need —
globally unique object ids, merge strategies, forward compatibility — and
nothing above it.

No binary storage format for canonical data. Binary means no diff, no merge,
no delta compression — and git *is* the append-only log already. Commits are
the entries; refs are the head pointers.

No anonymous intake format or in-git public issue submissions. Writing an
operation requires push access to a `refs/writ/*` namespace, which public bug
reporters do not have. Writ does not build an anonymous write protocol or
authorization layer; public intake belongs in a bridge or intake bot.

## What's hard, and what isn't solved

**Comment anchoring across force-pushes** is the genuinely hard problem in
this space. Writ anchors to content — blob hash plus hunk context, dual-sided
so deleted-line and cross-side comments are representable — never to line
numbers. When re-anchoring fails, a comment degrades to "orphaned but
preserved," never silently lost. Expect iteration here.

**Unbounded history.** Operation logs grow forever by design. Writ needs a
compaction and archival answer eventually. It does not have one yet, and
pretending otherwise would be the wrong kind of confidence.

**Cold start.** Getting review data means having a clone. That is slower than
an API call, and it is a real cost for casual and mobile access, which git is
bad at.

**Multiplayer liveness.** Git has no push notification. Without something
watching remotes, your teammate's comment arrives on your next fetch. Your own
writes are local and instant — single-digit milliseconds, nothing pending,
nothing to roll back — and offline is categorically strong. But seeing other
people quickly is the axis where a server-backed tool wins today.

**Identity mapping** from signing keys to directory identities is deliberately
out of spec scope, and it is exactly what an organization deploying this would
ask about first.

**Public issue intake.** Writing a writ operation requires push access to
`refs/writ/<writer-id>/*`. A stranger filing a bug on an open-source project
does not have it. Writ is built for the work of a team that already has commit
access; it structurally cannot let unauthenticated strangers write ops without
an account. The answer is an intake bot — a designated writer with push
credentials that bridges incoming webhooks, web forms, email, or an external
tracker into writ operations. To attribute external reporters truthfully
without synthesizing fake email addresses, an intake bot uses the `user:`
person identifier scheme (`user:<service>-<id>`, such as
`user:github-octocat`) while the bot itself signs the operation commit. For
many open-source projects, the honest answer is to keep GitHub Issues as the
public front door and use Writ for everything behind it.

## The spec is the fixtures

The standard is not the prose. It is the conformance corpus: fixture
repositories exercising concurrent edits on every type, multi-device writers,
orphaned anchors, unknown operation types, future versions, malformed
signatures — plus golden folded outputs that any implementation must reproduce
byte for byte.

That is what makes "an open convention" mean something. A second independent
implementation, in any language, can demonstrate compatibility rather than
assert it. If that community materializes, `spec/` should graduate to a
neutral home; a convention governed solely by the people who wrote the
reference implementation is a convention in name only.

## Where the money is, said plainly

Writ is Apache-2.0. The patent grant matters for enterprise adoption, and it
is the git-appraise precedent. Deliberately not a source-available license: the
format's credibility as a neutral convention depends on it being unambiguously
free.

The line we hold: **everything that reads or writes the format is open.
Services that require running infrastructure may be offered as hosted
products.** The test — if something being closed would undermine your
confidence that your data is portable and the format is neutral, it must be
open.

Concretely, what could be sold later: an awareness relay that watches remotes
and turns seconds of polling into sub-second liveness, and coordination
services that need a designated actor — merge queues, branch protection,
approval policy, org-wide search. Architecturally every one of those is *just
another git client*: an actor with a signing key that fetches, folds and
pushes operations like anyone else, through the same public API. No privileged
database, no private fork of the format, and a relay is never a source of
truth — if it's down, clients degrade to polling and git remains canonical.

Anyone can build and host equivalents on the same floor. We would rather earn
that business on quality than on lock-in, and the architecture is arranged so
that we have to.

## Where this actually is

Built and working: the spec and its conformance fixtures; the engine — codec,
DAG store, fold, anchor resolver, SQLite projection, sync — behind a
schema-shaped public Go API, never git-shaped: callers see no SHAs or
refspecs unless they ask, and the shapes they see come from the schema in
the log, not from Go structs writ ships; and the `writ` CLI, deliberately
plumbing rather than porcelain, with `--json` on every read verb for scripts
and agents.

Not built: the terminal client, a GitHub bridge, the relay, compaction. The
public API is deliberately strong enough that clients need no private hooks —
that constraint is what keeps the convention honest — and a client built on it
is coming, but it is not here yet and nothing about Writ requires you to wait
for it or use it. Build another one; that would be the best possible outcome.

If you try it, the thing most worth reporting is anything about your host, your
scale, or an anchor that should have survived and didn't.

<p class="cta">
  <a class="btn" href="latest/docs/">Documentation</a>
  <a class="btn btn-outline" href="https://github.com/writtendev/writ">GitHub</a>
</p>
