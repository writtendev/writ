# Ref layout and writer-id convention

Status: **normative**. The key words MUST, MUST NOT, SHOULD, and MAY are
to be interpreted as described in RFC 2119.

Writ stores operations in the git repository itself as signed, append-only
commits under a dedicated ref namespace. This document defines the ref
naming grammar, the chain append and edge rules, the writer-id derivation
and sourcing precedence, the exact refspecs written by `writ init`, and host
compatibility guarantees.

## Per-writer append chains

Every writer pushes only to their own namespace, so no other writer's push
ever races yours there: the ordinary class of push conflicts — one writer's
push landing non-fast-forward against another's — is structurally
eliminated between honest, cooperating writers. A writer's own push can
still be rejected non-fast-forward against their own prior push — a backup
restore or a rebase of unpushed-but-shared history rewrites that writer's
own local history, and the next plain push is rejected until they force it
(see §Fetch refspec) — but that rejection is never caused by a second
writer. This is not an access-control guarantee: no git host authenticates
per-ref ownership under `refs/writ/*`, so any principal with push access to
the repository can write (or force-push) into another writer's namespace.
Everyone with push access is trusted not to; a forged or overwritten op
remains detectable after the fact through its signature (`spec/signing.md`),
even though the ref that carried it can be overwritten. Host-side
enforcement of per-ref ownership is future work, not a property this spec
claims today.

Within a writer's namespace, operations are stored as **append chains**, one
chain per writer per object type:

```
refs/writ/<writer-id>/<object-type>
```

Which collaborative object an op belongs to lives exclusively in the op
payload (`object_id` in `op.json`, per `spec/op-envelope.md`), never in the
ref name.

### Ref naming grammar

A conforming Writ ref MUST match the following structure. A reader
recognizes a chain by the ref's name alone: rules 1 through 3 and rule 4's
`<object-type>` syntax (its pattern, its length, and the `.lock`
exclusion) are what it checks. Rule 4's byte-identity with the ops'
`object_type` is the one rule it does not check — a producer obligation
only, with the reader's treatment of a mismatch stated in rule 4. What a
reader does with a ref under `refs/writ/` that fails recognition is
§Reader enumeration's rule. The rules below state the local form. A remote-tracking chain
(`refs/remotes/<remote>/writ/<writer-id>/<object-type>`) conforms when the
part after `refs/remotes/<remote>/` matches them with `refs/` read as
`refs/remotes/<remote>/`; the three-segment rule then counts the segments
after that prefix. `<remote>` is the one or more path components between
`refs/remotes/` and the first component equal to `writ` that follows at least
one component; equivalently, split the part after `refs/remotes/` at its
first `/writ/`. A remote name may itself contain a `writ` component (a remote
named `writ`, `writ/fork` or `team/writ` is accepted), and the split reads it
by position. A leading `writ` belongs to `<remote>`:
`refs/remotes/writ/writ/<writer-id>/<object-type>` is a chain of remote
`writ`, and `refs/remotes/writ/fork/writ/<writer-id>/<object-type>` is a chain
of remote `writ/fork`. A later one does not: a remote named `team/writ` has no
conforming remote-tracking chains, because
`refs/remotes/team/writ/writ/<writer-id>/<object-type>` is read as remote
`team`, whose second segment `writ` is reserved and ignored under §Reader
enumeration, never as a chain of remote `team/writ`. The `remote_valid` and
`remote_invalid` vectors in `spec/testdata/ref-names/vectors.json` pin this
split. It is a known limitation, not a design: WRIT-321 moves the tracking
destination out of `refs/remotes/`, which retires this clause.

1. **Prefix:** The ref name MUST start with `refs/writ/`.
2. **Path segments:** Exactly three path segments MUST follow `refs/`:
   `writ`, `<writer-id>`, and `<object-type>`. There MUST NOT be any
   additional path segments or trailing slashes.
3. **Writer ID:** `<writer-id>` MUST be a lowercase hexadecimal string of
   exactly 16 characters (`^[0-9a-f]{16}$`), representing 64 bits of
   cryptographic randomness. It MUST NOT contain uppercase characters,
   hyphens, slashes, or other non-hexadecimal characters.
4. **Object type:** `<object-type>` MUST match
   `^[a-z][a-z0-9-]{0,63}(\.[a-z][a-z0-9-]{0,63})?$` with a length of at
   least 1 and at most 129 characters (64 per segment, plus the
   separating dot for the namespace-qualified form,
   `spec/op-envelope.md`). A producer MUST make it byte-identical to the
   `object_type` field of the ops it stores on that chain. A reader does
   not check this: it recognizes the chain from the name's syntax alone
   and never compares the ref's `<object-type>` to its ops'
   `object_type`. A mismatch is not a failure of ref recognition, is not
   a rejection under [`spec/op-envelope.md`](op-envelope.md) §Reader
   validation, and does not stop the walk: each op is grouped and folded
   by the `object_type` in its own `op.json`, whatever object type the ref
   that carried it names (many corpus fixtures, `multi-writer-chains`
   among them, carry `acme.widget` ops on `.../widget` refs).
   `<object-type>` MUST NOT end
   in `.lock`: git rejects any slash-separated ref path component ending
   in `.lock` outright (verified against real git: `git
   check-ref-format refs/writ/<writer-id>/acme.lock` fails, while
   `git check-ref-format refs/writ/<writer-id>/lock` and
   `.../acme.standup` both succeed), so an object type ending in
   `.lock` — such as a qualified `<namespace>.lock` — can never be
   written to a chain at all. Bare `lock` does not end in `.lock` and is
   a legal, ref-writable object type; so is a namespace of `lock`
   (`lock.thing` is fine). The exclusion is on the trailing `.lock`
   suffix, not on the segment's content alone.

Because the grammar disallows uppercase characters, two chains can never
collide on case-insensitive filesystems (such as macOS and Windows default
configurations) or across git loose and packed ref representations.

### Forward compatibility

The set of object types is open. Unknown object types automatically receive
their own chains (e.g. `refs/writ/<writer-id>/custom-type`) and are
transferred by the wildcard fetch and push refspecs with zero configuration
or schema changes.

## Append rule and graph edges

Every commit parent in Writ is a true happens-before edge.

### Producer requirements

When constructing an op commit, a producer MUST assign commit parents as
follows:

- **Non-empty chain:** `parents[0]` MUST be the commit id of the writer's
  previous op on that chain (the current local chain tip). Any additional
  causal dependencies observed by this op (e.g. ops from other writers or
  other chains) follow at `parents[1:]`.
- **Empty chain:** When creating the first op on a chain, causal
  dependencies start at `parents[0]`. If the first op has no causal
  dependencies, it MUST have zero parents (a root commit).
- **Target validity:** All commit parents MUST point at valid op commits
  and MUST NOT point at arbitrary non-op commits.
- **Distinct parents:** Parent op ids MUST be pairwise distinct; a
  producer MUST NOT emit a commit whose parent list repeats an op id.
  (See [`spec/op-envelope.md`](op-envelope.md) §Parents for the
  reader-side rule: a reader MUST accept a repeat and treat the parent
  list as a set of happens-before edges.)

**Why the chain spine MUST exist:** The requirement that `parents[0]`
points to the writer's previous op on that chain is self-enforcing. It
ensures that all earlier operations created by that writer remain reachable
in git's commit graph from the writer's single ref tip. If a writer failed to
link previous ops, git's reachability and garbage collection (`git gc`)
could prune earlier ops that lack other references.

### Reader enumeration

Readers MUST NOT rely on verifying the chain spine to discover or group
operations. A conforming reader:

1. Enumerates all refs under `refs/writ/*` (local writer) and
   `refs/remotes/*/writ/*` (remote-tracking chains fetched from remotes)
   that a reader recognizes as chains under §Ref naming grammar (the name's
   shape; rule 4's byte-identity with the ops' `object_type` is a producer
   obligation, not a recognition rule). A ref under `refs/writ/` (or
   `refs/remotes/<remote>/writ/`) that fails recognition MUST be
   ignored by a reader: not enumerated, not walked, and not reported as an
   error or a rejection. A second path segment (the one immediately after
   `writ/`) that is not a 16-lowercase-hex `<writer-id>` (for example
   `refs/writ/v2/...`) is reserved for future revisions of this format.
   The `+refs/writ/*` fetch refspec still transfers such refs. Why: a future format change that is not additive can
   then live in its own namespace beside this one, readable by readers that
   know it and invisible to readers that do not, instead of forking every
   repository's writ data.
2. Walks commit ancestry from every enumerated ref tip. A commit that fails
   [`spec/op-envelope.md`](op-envelope.md) §Reader validation is rejected,
   and the walk MUST NOT follow that commit's parents: a chain is a chain,
   and a break in it is the end of it (WRIT-289). An operation behind such a
   break is enumerated only if some other path of valid operations reaches
   it — for example a causal DAG parent edge from a different writer's
   chain. Signature-verification outcomes (`spec/signing.md`) and unknown
   object types, op types, op versions, or fields
   (`spec/forward-compatibility.md`) never stop the walk: both are
   operations, and the stopping rule applies to a commit that fails reader
   validation itself, and to every commit this reader's clone cannot read
   at all — its own commit object, its own root tree, or some other object
   its tree names — with exactly one exception: a commit whose own commit
   object is present, and whose root tree passes every one of §Reader
   validation rule 1's tree-shape checks against what this reader can see
   without the blob's own bytes — the tree has exactly one entry, that
   entry is named `op.json`, and it is a regular-file blob at mode
   `100644` — but is missing only that entry's own blob (what a
   blob-filtered clone's fetch filter leaves behind). That commit is
   still rejected — it carries no operation this reader can decode — but
   its parents, already known from the commit object itself, are still
   followed, because a reader that cannot read a blob it did not fetch
   cannot tell whether that blob would have encoded a valid operation.
   Every other locally-unreadable shape stops the walk exactly like a
   reader-validation failure: an absent root tree (a reader that cannot
   read a tree cannot tell whether it would have named an `op.json` entry
   at all — the shape a tree-filtered clone's ref tip on ordinary code
   history has, at every commit); a root tree present but without a
   top-level `op.json` entry, even when some other object it names is
   also absent (rule 1 already rejects that shape on the entries this
   reader can see — `missing-op-json`, or `op-json-subdirectory` if the
   absent subtree itself turns out to hold an `op.json` a complete reader
   would find, which this reader cannot tell — so either way there is
   nothing left to learn by reading further); and a root tree that
   already fails one of rule 1's other tree-shape checks on what this
   reader can see — an extra entry
   beside `op.json` whatever that entry's own blob holds, `op.json`
   present as a directory instead of a blob, or `op.json` present at some
   mode other than `100644` — which already settles that the commit is
   not an operation, whether or not some other object the tree names is
   also absent. There is no depth or count bound beyond this rule:
   walking past a run of the one narrow exception costs exactly what
   walking an equally long chain of valid operations costs, and any
   writer with push access can already produce a valid-operation chain of
   any length, so the exception adds no bound-stepping leverage beyond
   what the format already permits.
3. Deduplicates visited operations by commit SHA (the op id).
4. Groups operations by the `object_id` found in each op commit's `op.json`
   payload.

Why no bound rather than a fixed depth or count past the last recognized
operation: a fixed bound is only a threshold for an attacker to step over,
and tolerating a bounded run of non-op commits is the same exposure as
tolerating an unbounded one, just with the cost capped instead of removed.
The accepted cost of stopping at the first break instead is that a
repository which relied on a reader walking *through* a malformed or
non-op commit to reach ops beyond it will, under this rule, surface fewer
operations than before — see `spec/fixtures/testdata/descriptions/
multi-writer-chains.yaml`, which pins exactly this tradeoff.

An object's op-DAG is the ancestry-restricted subgraph over its `object_id`.
Rollback detection is a reachability check against the previously observed
ref tip, using this same reader walk — a commit only reachable by walking
through a commit that fails reader validation does not count as reachable,
so a tip advanced across such a break reports as a rollback (`Rewound`)
exactly as a genuine force-push would, even though the old tip is still, in
the weaker sense of plain git ancestry, an ancestor of the new one. A
commit reachable only through one whose root tree passes every tree-shape
check above and is missing only its own `op.json` blob remains reachable
exactly as before: that break does not stop the walk either way (see the
stopping rule's one exception above), so it does not stop reachability for
rollback detection. Every other locally-unreadable shape stops
reachability the same way it stops the walk. Neither reader enumeration
nor rollback detection requires chain spine inspection or writer
attribution.

## Writer ID convention

A writer-id is an opaque 64-bit identifier (16 lowercase hex characters)
associated with a single writer device.

### Sourcing precedence

When determining the writer-id for local operations, the engine MUST
evaluate configuration in the following order:

1. **Repository configuration:** `writ.writerId` in the local repository
   `.git/config`.
2. **Global configuration:** `writ.writerId` in the user's global
   `~/.gitconfig`.
3. **Automated / CI environments:** Automated agents and CI runners MUST
   provide a stable, pre-configured `writ.writerId` via git configuration
   rather than minting an ephemeral ID per run.
4. **Minting:** If no writer-id is configured, `writ init` MUST generate
   16 lowercase hex characters from a cryptographically secure random number
   generator and write it to `.git/config` under `writ.writerId`.

### Bounding ref growth

Bounding ref count at $\mathcal{O}(\text{writers} \times \text{devices} \times \text{types})$
is a core architectural requirement (ARCHITECTURE.md, WRIT-69). Sourcing from
global or local configuration and using stable bot IDs prevents ref counts
from growing $\mathcal{O}(\text{clone events} \times \text{types})$ over
time.

### Identity and key stability

- **Attribution:** Writer-id is an opaque routing key for ref namespaces,
  never an identity claim. Human attribution and authorization derive
  exclusively from the commit author header and the cryptographic commit
  signature (`gpgsig`, WRIT-22, WRIT-43).
- **Key rotation:** Because the writer-id is random and carries no PII or
  cryptographic key material, rotating a signing key does not change the
  writer-id or fork the writer's append chain.
- **Lost ID:** If a local configuration is lost, minting a new writer-id
  starts a new chain. Earlier ops on the previous chain remain intact on
  the remote and continue to fold in normally by `object_id`.
- **Collision recovery:** With 64 bits of cryptographic randomness,
  accidental collisions are negligible ($p < 10^{-9}$ for millions of
  writers). If a newly minted writer-id matches an existing remote ref
  observed during fetch, the client MUST mint a new writer-id.
- **Multi-device writers:** Multiple clones across different machines or
  devices use distinct writer-ids by design, dissolving concurrent writes
  into sibling DAG branches resolved at fold time.

## Exact refspecs (`writ init`)

The entire deployment story for Writ is the refspec configuration written
by `writ init` into `.git/config`.

### Fetch refspec

For each configured remote `<remote>`, `writ init` appends the following
fetch refspec:

```
remote.<remote>.fetch = +refs/writ/*:refs/remotes/<remote>/writ/*
```

Command executed:
```bash
git config --add remote.<remote>.fetch '+refs/writ/*:refs/remotes/<remote>/writ/*'
```

Key properties:
- **Leading `+` (forced):** A peer force-pushing a rewind of their own chain
  (a backup restore, a rebase of unpushed-but-shared history, or a plain
  `--force`) is a normal, non-hostile event, not an attack to reject. A
  non-forced refspec turns that single event into a team-wide availability
  failure: every other writer's `writ sync` fails non-fast-forward on every
  retry, and the rewinding peer's own unrelated ops can never reach the
  remote either, recoverable only by raw `git fetch`/`update-ref` plumbing.
  The forced refspec instead lets the rewind land: the local remote-tracking
  ref for that peer moves back silently, `dag.EnumerateSince` reports the
  chain as `Rewound`, and `internal/projection/refresh.go` falls through to a
  full projection rebuild — the safety a non-forced refspec would have
  bought is safety the projection layer already provides on every fetch.
  The accepted cost: this clone's local view of the rewound peer can lose
  ops it previously showed, silently, the moment the forced fetch runs. That
  is not data loss — any op another writer causally built on the rewound
  ops stays reachable from *that other writer's own chain*
  (`ARCHITECTURE.md` §Ref layout), so nothing referenced from elsewhere in
  the DAG disappears — but this clone's view of that writer's history can
  move backward without warning.
- **Remote-tracking namespace:** Fetching into `refs/remotes/<remote>/writ/*`
  keeps remote chains isolated from the local writing namespace `refs/writ/*`.
  This prevents plain `git fetch` from failing with non-fast-forward errors
  when the local writer has unpushed operations, and prevents `git fetch --prune`
  from deleting unpushed local chains.
- **Idempotency:** `writ init` MUST check existing `remote.<remote>.fetch`
  entries and avoid adding duplicate lines on repeated invocations.

### Push refspec

Pushing is handled explicitly by Writ's sync plane using the writer's
specific chain refspec:

```
refs/writ/<writer-id>/*:refs/writ/<writer-id>/*
```

`writ init` MUST NOT set `remote.<remote>.push` in `.git/config`. Setting a
push refspec in git config would alter the default behavior of ordinary
`git push` commands run by the user for standard repository branches.

### The `--prune` behavior

When `fetch.prune` is enabled or `git fetch --prune` is run, git prunes
remote-tracking refs under `refs/remotes/<remote>/writ/*` that no longer
exist on the remote. Because local chains live under `refs/writ/*`,
`--prune` never touches unpushed local chains.

## Host compatibility and fallbacks

The canonical namespace for Writ is `refs/writ/*`.

### Empirical verification status

Per WRIT-1 and WRIT-69:
- **Verified:** GitHub, Gitea/Forgejo, and bare SSH accept pushes and
  fetches for arbitrary `refs/*` namespaces with no special server-side
  restrictions.
- **Outstanding (WRIT-64):** GitLab, Bitbucket Cloud, and Codeberg
  remain to be empirically verified.

Chains bound ref counts to small, predictable numbers, avoiding the
$\mathcal{O}(\text{writers} \times \text{objects})$ explosion that causes
git fetch latency degradation (~119 bytes re-advertised per ref on every
fetch) and server-side push failures on major hosts (WRIT-69).

### Fallback escape hatch: branch namespace

If a specific git host restricts pushes outside of `refs/heads/*`, Writ
defines an optional fallback escape hatch using branch-namespace encoding:

```
refs/heads/writ/<writer-id>/<object-type>
```

This fallback:
- Is a host-specific workaround, NOT an alternative format that general
  implementations must support by default.
- Encodes the identical three-part segment hierarchy (`writ`, `<writer-id>`,
  `<object-type>`) under `refs/heads/`.
- Must be used with caution, as `refs/heads/*` is subject to branch-protection
  rules and CI push triggers (`on: push`) on many hosting platforms.

## Conformance data

- `spec/testdata/ref-names/vectors.json` — normative test vectors for ref
  name parsing, valid and invalid forms, and pinned refspec strings. Its
  `remote_valid` and `remote_invalid` lists pin where `<remote>` ends in a
  remote-tracking ref (remotes `origin`, `writ`, `writ/fork` and `team/fork`,
  and the ignored `team/writ` split), each valid entry naming the expected
  `remote`.
- `spec/ref_layout_test.go` — test suite asserting grammar conformance and
  `git check-ref-format` validation.
- `spec/fixtures/testdata/descriptions/multi-writer-chains.yaml` and its
  fold golden (`spec/fixtures/testdata/golden/fold/multi-writer-chains.json`)
  — the §Reader enumeration stopping rule (WRIT-289): two writer chains, a
  remote-tracking chain, a cross-chain causal parent edge, and two breaks
  (a commit missing `op.json` and one whose `op.json` is present but
  non-canonical), pinning exactly which operations a conforming reader
  holds and which it does not.
- `spec/fixtures/testdata/descriptions/reserved-ref-namespaces.yaml` and its
  fold golden (`spec/fixtures/testdata/golden/fold/reserved-ref-namespaces.json`)
  — the §Reader enumeration MUST-ignore rule (WRIT-314): a conforming chain
  beside a `refs/writ/v2/...` ref, a `refs/writ/zz-not-hex/...` ref, and a
  `refs/remotes/origin/writ/v2/...` ref, each carrying something a reader
  would surface if it walked the ref, pinning that only the conforming
  chain's operations are held and the fold golden records no rejections.
