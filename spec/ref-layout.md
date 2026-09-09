# Ref layout and writer-id convention

Status: **normative**. The key words MUST, MUST NOT, SHOULD, and MAY are
to be interpreted as described in RFC 2119.

Writ stores operations in the git repository itself as signed, append-only
commits under a dedicated ref namespace. This document defines the ref
naming grammar, the chain append and edge rules, the writer-id derivation
and sourcing precedence, the exact refspecs written by `writ init`, and host
compatibility guarantees.

## Per-writer append chains

Push conflicts are structurally eliminated by giving every writer their own
namespace: a writer only ever pushes to their own ref, so pushes cannot
non-fast-forward against another writer.

Within a writer's namespace, operations are stored as **append chains**, one
chain per writer per object type:

```
refs/writ/<writer-id>/<object-type>
```

Which collaborative object an op belongs to lives exclusively in the op
payload (`object_id` in `op.json`, per `spec/op-envelope.md`), never in the
ref name.

### Ref naming grammar

A conforming Writ ref MUST match the following structure:

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
   `spec/op-envelope.md`). It MUST be byte-identical to the `object_type`
   field of the ops stored on that chain. The final (or only) segment
   MUST NOT be exactly `lock`: git rejects any slash-separated ref path
   component ending in `.lock` outright, so an object type whose trailing
   segment is `lock` — bare `lock`, or a qualified `<namespace>.lock` —
   can never be written to a chain at all. A namespace of `lock` is
   fine (`lock.thing` is a legal, ref-writable object type); the
   exclusion is on the trailing segment only.

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
   `refs/remotes/*/writ/*` (remote-tracking chains fetched from remotes).
2. Walks full commit ancestry from every enumerated ref tip.
3. Deduplicates visited operations by commit SHA (the op id).
4. Groups operations by the `object_id` found in each op commit's `op.json`
   payload.

An object's op-DAG is the ancestry-restricted subgraph over its `object_id`.
Rollback detection is an ancestry reachability check against the previously
observed ref tip; neither reader enumeration nor rollback detection requires
chain spine inspection or writer attribution.

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
remote.<remote>.fetch = refs/writ/*:refs/remotes/<remote>/writ/*
```

Command executed:
```bash
git config --add remote.<remote>.fetch 'refs/writ/*:refs/remotes/<remote>/writ/*'
```

Key properties:
- **No leading `+` (non-forced):** Deliberately omitted so that remote
  rollbacks or non-fast-forward updates are rejected by git (`[rejected] (non-fast-forward)`)
  rather than silently dropping or rewriting remote history.
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
  name parsing, valid and invalid forms, and pinned refspec strings.
- `spec/ref_layout_test.go` — test suite asserting grammar conformance and
  `git check-ref-format` validation.
