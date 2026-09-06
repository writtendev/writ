# Issue Operations — issue, state, assign, label, link (v1)

Status: **normative**. Schema: [`schemas/issue-ops.schema.json`](schemas/issue-ops.schema.json).
Vectors: [`testdata/issue-ops/`](testdata/issue-ops/).
Field rules: [`testdata/issue-ops/field-rules.json`](testdata/issue-ops/field-rules.json).
Fixtures: [`spec/fixtures/testdata/descriptions/issue-*.yaml`](fixtures/testdata/descriptions/) and [`spec/fixtures/testdata/golden/issue/`](fixtures/testdata/golden/issue/).

This document defines the operation vocabulary, payload schemas, and fold
semantics for issues in Writ (`object_type: "issue"`). It covers issue
creation, metadata updates, state transitions, assignments, labels, and
cross-references (ARCHITECTURE.md §Object types).

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

## Scope & Object Model

An issue is represented in Writ as a collaborative object with
`object_type: "issue"`. Issues live in the repository the client is operating on
(ARCHITECTURE.md §Object homing) and can reference or be referenced by reviews
and other collaborative objects in other repositories via qualified
references (`spec/identifiers.md`).

### Decisions behind the vocabulary

- **`update` covers both retitle and description edits:** Rather than separate
  ops for editing title vs. editing description, a single `update` op with two
  optional fields (where an empty body `{}` is rejected by schema) handles all
  metadata edits. This mirrors the `review` family's `update` op and avoids
  multiplying op types for identical LWW merge semantics.
- **`create` carries only title and description:** Initial issue creation
  captures the core problem statement. State, assignees, labels, and links
  arrive as their own operations, ensuring that "opened and immediately
  triaged" and "triaged later" share one uniform append pipeline and fold path.
  Absent any `set-state` op, the folded issue state defaults to the empty
  string; clients render this as "no state". Fold is pure and per-object and
  cannot know a workspace's default unstarted state — a conforming client
  seeding the default starter workflow states at repository initialization
  (see [`workflow-state-ops.md`](workflow-state-ops.md) §7) and picking one
  of them on `issue create` are what give a real repo's issues a real state
  from birth.
- **`set-state` is `lww`, not `lattice`:** Issues reopen; state transitions
  (unstarted $\leftrightarrow$ completed workflow states) are not monotone, so
  a join-semilattice would be incorrect. Last-writer-wins in the fold's total order resolves
  concurrent state transitions deterministically. The optional `reason` field
  is a free string carrying external reasons (such as GitHub's `state_reason`:
  `"completed"`, `"not_planned"`) or human-provided explanations without
  Writ minting an un-enforceable closed enum.
- **`assign` and `label` are add-wins OR-sets (`set-observed-remove`):**
  Concurrent assignment or labeling on one device and removal on another is
  reconciled via `set-observed-remove` (WRIT-12, `spec/fold.md`). Additions
  win over concurrent removals. Assignee values are scheme-prefixed person
  identifiers ([`spec/identifiers.md`](identifiers.md) §Person identifiers),
  normalized (scheme lowercased; value trimmed and case-folded) prior to set
  evaluation; schemes never unify, so `user:alice` and `email:alice@example.com`
  are two members. Labels are opaque non-empty strings.
- **`link` mirrors `approval`:** A link records an association between the
  issue and another collaborative object (such as a code review or companion
  issue). It uses `keyed-lww` keyed by `target`. Emitting a `link` op with
  `relation: "none"` retracts the link for that target, following the same
  retraction idiom used by approval votes (`verdict: "none"`). The `target`
  field is a reference string per `spec/identifiers.md` (bare `<object-id>`
  within the same repo, or `<repo-id>#<object-id>` cross-repo).
- **No tombstone in v1:** Issues close; they are not deleted. There are no
  tombstones defined for `issue` objects in v1.

### Scope boundaries

- **Comments on issues:** Discussion threads attached to issues are separate
  collaborative objects (`object_type: "comment"`) whose `subject.object_type`
  is `"issue"` (WRIT-9, `spec/comments.md`).
- **Project and cycle membership:** Grouping issues into projects and cycles
  belongs to the project and cycle op vocabulary (WRIT-11).
- **Cross-repo references:** Identifiers, repo designators, and reference
  resolution algorithms are defined in `spec/identifiers.md` (WRIT-16).
- **Fold driver and total ordering:** The deterministic total order $L$ and
  concurrency resolution are specified in `spec/fold.md` (WRIT-12).

### Public Issue Intake and Bot Attribution

Writ does not define an anonymous or in-band public issue submission protocol in
the format. Every Writ operation is a git commit requiring push access to
`refs/writ/<writer-id>/*`. External contributors and anonymous bug reporters
without repository push access cannot write operations directly.

Public issue intake is externalized to downstream intake bridges and bots:

- **Intake bots author operations:** An intake bot runs as a designated git
  writer with its own `writer-id` and push credentials. It accepts bug reports
  from external interfaces (webhooks, public web forms, email, or forge issues
  such as GitHub Issues) and commits standard `create` and `comment` operations
  into the repository the client is operating on.
- **Truthful attribution via `user:` person identifiers:** When attributing
  external reporters (for example in description headers, comments, or assignees),
  intake bots MUST use scheme-prefixed person identifiers per
  [`spec/identifiers.md`](identifiers.md) §Person identifiers. External accounts
  without verified email addresses are recorded using the `user:` scheme
  (e.g. `user:github-<handle>` or `user:<service>-<id>`), preserving honest origin
  identity without synthesizing fictional email addresses.
- **Forge issues as a front door:** Projects taking public reports may continue
  to use forge issues (such as GitHub Issues) as their public front door, using
  bidirectional bridges to mirror accepted issues into Writ.

## Envelope Binding

Every issue operation is carried in a git commit whose `op.json` payload
conforms to `spec/schemas/op-envelope.schema.json` and
`spec/schemas/issue-ops.schema.json`:

- `object_type` MUST be `"issue"`.
- `op_version` MUST be an integer ≥ 1. This document specifies version `1`.
- `object_id` MUST be a non-empty string identifier (1–256 characters,
  printable non-space ASCII `^[\x21-\x7e]+$`). Canonical Writ-minted object IDs
  are 32 lowercase hexadecimal characters (`^[0-9a-f]{32}$`).
- `op_type` MUST be one of the operation types defined below, or an unknown
  string tolerated under forward-compatibility rules.
- `body` MUST be a JSON object conforming to the schema for the declared
  `op_type` and `op_version`.

Commit author, timestamp, and signature are carried exclusively by the op
commit per `spec/op-envelope.md`. Producers MUST NOT mirror commit-carried
fields into the payload.

## Operation Vocabulary

The issue family defines six operation types for `op_version: 1`:

| `op_type` | Body Schema | Description |
| --- | --- | --- |
| `create` | `{"title": string, "description"?: string, "priority"?: integer, "estimate"?: number, "position"?: string}` | Issue creation and initial description. |
| `update` | `{"title"?: string, "description"?: string, "priority"?: integer, "estimate"?: number, "position"?: string}` | Metadata edits (title, description, priority, estimate, position). |
| `set-state` | `{"state": reference, "reason"?: string, "position"?: string}` | State transitions, optional reason, and optional destination position. |
| `assign` | `{"add"?: [person-id], "remove"?: [person-id]}` | Add or remove assignees. |
| `label` | `{"add"?: [reference], "remove"?: [string]}` | Add or remove labels (referencing label object IDs, FC-16). |
| `link` | `{"target": reference, "target_type"?: string, "relation": "fixes"\|"relates"\|"none"}` | Associate or retract cross-references. |

### 1. `create`

Initializes an issue object.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "issue",
  "op_type": "create",
  "op_version": 1,
  "body": {
    "title": "Fix parser crash on empty input",
    "description": "Parser panics with index out of range when given empty input.",
    "priority": 1,
    "estimate": 3,
    "position": "V"
  }
}
```

- `title` (string, required): Summary title of the issue, minLength 1.
- `description` (string, optional): Full Markdown body describing the issue.
- `priority` (integer, optional): Closed enum integer priority from `0` to `4` (0 = None, 1 = Urgent, 2 = High, 3 = Medium, 4 = Low).
- `estimate` (number, optional): Unconstrained non-negative numeric estimate (`minimum: 0`).
- `position` (string, optional): Fractional index position string per [`spec/ordering.md`](ordering.md).

Absent any subsequent `set-state` op, the folded state of an issue is the empty string, which clients render as "no state". Absent an explicit priority, the folded priority is `0` ("none").

### 2. `update`

Modifies issue metadata (title, description, priority, estimate, position).

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "issue",
  "op_type": "update",
  "op_version": 1,
  "body": {
    "title": "Fix parser crash on empty and nil input",
    "priority": 2,
    "estimate": 5,
    "position": "aV"
  }
}
```

- `title` (string, optional): Updated issue title, minLength 1.
- `description` (string, optional): Updated issue description.
- `priority` (integer, optional): Updated priority (`0`–`4`).
- `estimate` (number, optional): Updated non-negative estimate (`minimum: 0`).
- `position` (string, optional): Updated fractional index position string per [`spec/ordering.md`](ordering.md).

At least one of `title`, `description`, `priority`, `estimate`, or `position` MUST be present in an `update` body.
An empty `{}` update body is invalid.

### 3. `set-state`

Transitions the issue state to reference a `workflow-state` collaborative object, optionally updating manual rank position in the destination column.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "issue",
  "op_type": "set-state",
  "op_version": 1,
  "body": {
    "state": "0123456789abcdef0123456789abcdef",
    "reason": "completed",
    "position": "V"
  }
}
```

- `state` (reference string, required): Reference string targeting a `workflow-state`
  collaborative object ID (per [`spec/identifiers.md`](identifiers.md)).
- `reason` (string, optional): Explanation for the state change (e.g. `"completed"`,
  `"not_planned"`, or a free-form human string).
- `position` (string, optional): Fractional index position string in the destination column per [`spec/ordering.md`](ordering.md).

#### Unknown-State Reference Semantics

In an eventually-consistent log, an issue referencing an unknown, missing, or unfetched
state ID MUST fold cleanly without rejection or error. Referential integrity is not
enforceable across independent append-only writer logs. Clients project and render such
issues into an "Unknown" board column until the defining `workflow-state` operation arrives,
at which point the reference heals automatically.

### 4. `assign`

Adds or removes assignees for the issue.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "issue",
  "op_type": "assign",
  "op_version": 1,
  "body": {
    "add": ["email:alice@example.com", "user:bob"],
    "remove": ["email:charlie@example.com"]
  }
}
```

- `add` (array of person identifiers per [`spec/identifiers.md`](identifiers.md), optional): Person identifiers (assignees) to add.
- `remove` (array of person identifiers per [`spec/identifiers.md`](identifiers.md), optional): Person identifiers (assignees) to remove.

At least one of `add` or `remove` MUST be present and contain at least one item.
An empty `{}` body or empty arrays (`"add": []`) are invalid.

Assignees are normalized (leading/trailing whitespace trimmed, lowercase) per
[`spec/identifiers.md`](identifiers.md) before set membership and deduplication are
evaluated. Byte-exact equality after normalisation determines element identity.

### 5. `label`

Adds or removes labels on the issue. In v1, label operations reference collaborative `label` object identifiers (`spec/identifiers.md#reference`). Unknown or unfetched label references fold cleanly without rejection per `spec/forward-compatibility.md` rule `FC-16`.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "issue",
  "op_type": "label",
  "op_version": 1,
  "body": {
    "add": ["0123456789abcdef0123456789abcdef"],
    "remove": ["fedcba9876543210fedcba9876543210"]
  }
}
```

- `add` (array of label references, optional): Label object identifiers or references to add.
- `remove` (array of non-empty strings, optional): Label object identifiers or references to remove, matching the stored value byte-exactly.

At least one of `add` or `remove` MUST be present and contain at least one item.
An empty `{}` body or empty arrays (`"remove": []`) are invalid.

### 6. `link`

Creates, updates, or retracts a cross-reference link to another collaborative
object (such as a code review or another issue).

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "issue",
  "op_type": "link",
  "op_version": 1,
  "body": {
    "target": "11112222333344445555666677778888#fedcba9876543210fedcba9876543210",
    "target_type": "review",
    "relation": "fixes"
  }
}
```

- `target` (reference string, required): Reference to the target object per
  `spec/identifiers.md`. Either a bare `<object-id>` (for objects in the same
  repository) or a fully-qualified `<repo-id>#<object-id>` (for cross-repo
  references, such as an issue in one repository linking to a review in
  another).
- `target_type` (string, optional): Type of the target collaborative object
  (e.g. `"review"`, `"issue"`), minLength 1.
- `relation` (string, required): One of:
  - `"fixes"`: The target fixes or resolves this issue.
  - `"relates"`: The target is related to this issue.
  - `"none"`: Retracts any existing link to `target`.

## Fold Implications & Merge Strategies

Every body property defined in this vocabulary is mapped to one merge strategy
from `spec/fold.md`'s closed catalogue. A machine-readable copy of these rules
is published in `spec/testdata/issue-ops/field-rules.json`.

Folded issue state is `Issue{title, description, state, reason, priority, estimate, position, assignees, labels, links}`.

| `op_type` | Field | Merge Strategy | Key / Details |
| --- | --- | --- | --- |
| `create` | `title` | `lww` | Last writer wins in total order $L$ |
| `create` | `description` | `lww` | Last writer wins |
| `create` | `priority` | `lww` | Last writer wins; closed vocabulary 0–4; 0 is None |
| `create` | `estimate` | `lww` | Last writer wins; non-negative number |
| `create` | `position` | `lww` | Last writer wins; base-62 fractional index string |
| `update` | `title` | `lww` | Last writer wins |
| `update` | `description` | `lww` | Last writer wins |
| `update` | `priority` | `lww` | Last writer wins |
| `update` | `estimate` | `lww` | Last writer wins |
| `update` | `position` | `lww` | Last writer wins |
| `set-state` | `state` | `lww` | Last writer wins |
| `set-state` | `reason` | `lww` | Last writer wins |
| `set-state` | `position` | `lww` | Last writer wins |
| `assign` | `add` | `set-observed-remove` | Add-wins OR-set over normalized person identifiers (`spec/identifiers.md`) |
| `assign` | `remove` | `set-observed-remove` | Add-wins OR-set over normalized person identifiers (`spec/identifiers.md`) |
| `label` | `add` | `set-observed-remove` | Add-wins OR-set over label strings |
| `label` | `remove` | `set-observed-remove` | Add-wins OR-set over label strings |
| `link` | `target` | `keyed-lww` | Scoped by key `["target"]` |
| `link` | `target_type` | `keyed-lww` | Scoped by key `["target"]` |
| `link` | `relation` | `keyed-lww` | Scoped by key `["target"]`; `"none"` retracts |

### Concurrency and Retraction Semantics

- **Total order and LWW:** Concurrent edits to `title`, `description`, `state`,
  `reason`, `priority`, `estimate`, or `position` resolve via last-writer-wins in the deterministic total order $L$
  (defined in `spec/fold.md` §Causality-monotone total order $L$).
- **Priority closed vocabulary & semantic sorting:** Priority is a closed integer enum `0`–`4`: `0` = None, `1` = Urgent, `2` = High, `3` = Medium, `4` = Low. Queries sort semantically: Urgent (1) > High (2) > Medium (3) > Low (4) > None (0).
- **Estimate scaling:** Stored as an unconstrained non-negative number (`minimum: 0`). Interpretation of the estimate scale (Fibonacci points, linear hours, T-shirt sizes) is left to workspace settings and client-side presentation.
- **Manual rank & fractional indexing:** Global position is stored as a base-62 fractional index string per [`spec/ordering.md`](ordering.md). Concurrent inserts at the same position resolve deterministically via op-id tiebreak.
- **Add-Wins OR-Sets:** `assign` and `label` use `set-observed-remove`
  (`spec/fold.md` §5). If an addition of an assignee or label is concurrent
  with a removal ($a \parallel r$), the addition wins and the element remains
  present in the folded set.
- **Keyed link map and retraction:** Links are stored as a map from `target`
  reference string to link attributes. Concurrent links to different targets do
  not conflict. A link is retracted by emitting `relation: "none"`, causing the
  link for that `target` to present as absent in materialized state while the
  operation remains preserved in the DAG.
- **Deletion:** Issues are never deleted in v1. Status transitions into a
  `completed`- or `canceled`-type workflow state represent lifecycle state,
  not object deletion.

## Forward Compatibility & Unknown Fields

- **Unknown body and envelope fields:** Conforming implementations MUST
  preserve and ignore any unknown fields in op payloads (`spec/forward-compatibility.md`).
- **Unknown `op_type` or future `op_version`:** Ops with unknown op types or
  unsupported versions for `object_type: "issue"` MUST remain in the DAG and
  contribute to total ordering ($t^*$) and ancestry, but contribute no field
  mutations to known issue state (WRIT-15).

---

## Appendix A — GitHub Issue Representability (Informative)

To ensure fidelity on the GitHub bridge read path, every
standard GitHub issue mutation maps to the Writ issue op vocabulary.

The conversion vectors under [`testdata/issue-ops/github/`](testdata/issue-ops/github/)
illustrate these mappings.

| GitHub Issue Field / Event | Disposition in Writ |
| --- | --- |
| `title` | `create.title` / `update.title` (`lww`) |
| `body` | `create.description` / `update.description` (`lww`) |
| `state` (`"open"`, `"closed"`) | `set-state.state` referencing a `workflow-state` object: `open` maps to a state of type `unstarted`, `closed` maps to a state of type `completed` (`state_reason: "not_planned"` steers to a `canceled`-type state where the workspace has one). The bridge resolves against the workspace's workflow states; a conforming client seeds the default starter set at repository initialization (see [`workflow-state-ops.md`](workflow-state-ops.md) §7), guaranteeing they exist. (`lww`) |
| `state_reason` (`"completed"`, `"not_planned"`) | `set-state.reason` (`lww`) |
| `assignees` (added / removed) | `assign.add` / `assign.remove` (`set-observed-remove`) |
| `labels` (added / removed) | `label.add` / `label.remove` (`set-observed-remove`) |
| Closing PR / Linked PR | `link` op (`target: <repo-id>#<review-id>`, `relation: "fixes"`, `target_type: "review"`) |
| `user` | Op commit author |
| `created_at`, `updated_at`, `closed_at` | Op commit timestamps on respective ops |
| `comments` | Separate `comment` objects (WRIT-9) referencing `object_id` |
| `milestone` | Workspace metadata / cycle or project link (WRIT-11) |

---

## Appendix B — Linear Schema Mapping (Normative)

This appendix defines the normative mapping rules for translating Linear
entities, issue properties, and discussion records into Writ collaborative
objects and operations (`object_type: "issue"`, `object_type: "workflow-state"`,
`object_type: "comment"`, `object_type: "document"`, and `object_type: "project"`).

### Scope & Importer Contract

Per ARCHITECTURE.md §Repo strategy and VISION.md §The open core and the hosted
layer, bridges and importers live in downstream repositories consuming Writ's
public Go engine API or `--json` CLI plumbing. What belongs in Writ core is the
normative schema mapping: establishing translation rules so that implementing an
importer is a mechanical exercise rather than a series of discretionary judgment
calls.

Conforming importers MUST translate Linear entities and fields according to the
rules in this appendix.

### Entity & Field Mapping

#### 1. Issue Entity Mapping

A Linear `Issue` maps to an `issue` collaborative object (`object_type: "issue"`).

| Linear Field | Disposition in Writ | Merge Strategy | Details |
| --- | --- | --- | --- |
| `title` | `create.title` / `update.title` | `lww` | Issue title. |
| `description` | `create.description` / `update.description` | `lww` | Markdown body, with Linear-specific XML tags rewritten per §Markdown Dialect Rewriting. |
| `state` | `set-state.state` | `lww` | Target `workflow-state` object ID (WRIT-104). |
| `priority` | `priority` | `lww` | Numeric priority (WRIT-106): 0 = None, 1 = Urgent, 2 = High, 3 = Medium, 4 = Low. |
| `estimate` | `estimate` | `lww` | Numeric estimate value (WRIT-106). Scale interpretation (Fibonacci, exponential, linear, T-shirt) is left to workspace settings and client display (WRIT-110). |
| `sortOrder` | position string | `lww` | Fractional indexing position string per WRIT-106 and WRIT-108. |
| `assignee` / `assignees` | `assign.add` / `assign.remove` | `set-observed-remove` | Scheme-prefixed person identifiers per §Identity Mapping. |
| `labels` | `label.add` / `label.remove` | `set-observed-remove` | `label` object IDs the importer creates or resolves by name. |
| `relations` (`blocks`, `relatedTo`, `duplicateOf`) | `link` op | `keyed-lww` | Target `link` relation: `"blocks"`, `"relates"`, `"duplicate"`. For basic v1 schema compatibility (where `relation` is constrained to `"fixes"`, `"relates"`, `"none"`), relations other than `"fixes"` MUST degrade to `"relates"`. |
| `parent` / `sub-issues` | `link` op | `keyed-lww` | Target `link` relation: `"parent"` on child issue targeting parent issue ID. For basic v1 schema compatibility, `relation` MUST degrade to `"relates"`. |
| `identifier` (e.g. `WRIT-93`) | `linear:<identifier>` label & description header | `set-observed-remove` / `lww` | Preserved via `linear:<identifier>` label and description provenance header per §Identifier Preservation. |
| `createdAt`, `updatedAt` | Op commit author timestamps | — | Recorded on op commits. |
| `creator` | Op commit author | — | Recorded on initial create op commit (or bridge committer with provenance). |

#### 2. Workflow States

A Linear `WorkflowState` maps to a `workflow-state` collaborative object
(WRIT-104) in the repository the client is operating on:

- `name` (string, `lww`): State display name (e.g. `"Backlog"`, `"In Progress"`, `"Done"`).
- `type` (string, `lww`): One of the five canonical state types (`backlog`,
  `unstarted`, `started`, `completed`, `canceled`), matching Linear's state type vocabulary.
- `position` (string, `lww`): Fractional indexing position string per WRIT-108.
- `color` (string, `lww`): Hex color code string (e.g. `"#f2c94c"`).

#### 3. Comments

A Linear `Comment` maps to a `comment` collaborative object (`object_type: "comment"`,
`spec/comments.md`):

- `subject`: `{"object_type": "issue", "object_id": <issue-id>}`.
- `text`: Comment Markdown text with Linear XML tags rewritten per §Markdown Dialect Rewriting.
- Threading: Replies map to `comment` objects with `in_reply_to` pointing to the
  parent comment object ID.
- `createdAt`, `updatedAt`: Op commit author timestamps.

#### 4. Documents

A Linear `Document` associated with an issue maps to a `document` collaborative
object (WRIT-105):

- Attached to the issue via a `link` op targeting the document's object ID with
  `relation: "implementation-plan"` or `relation: "relates"`. For basic v1 schema
  compatibility, `relation` MUST degrade to `"relates"`.

#### 5. Projects

A Linear `Project` maps to a `project` collaborative object (`object_type: "project"`,
`spec/project-cycle.md`):

- Issue membership in a project is recorded via project membership operations.

### Identity Mapping

Linear identifies actors by internal UUIDs, display names, and optional email
addresses. Writ identifies actors in operation payloads using scheme-prefixed
person identifiers ([`spec/identifiers.md`](identifiers.md) §Person identifiers,
WRIT-102, WRIT-119).

Conforming importers MUST translate actor identities according to the following
rules:

1. **User with email:** When an email address is available on the Linear user,
   the importer MUST emit `email:<normalized-email>`, where the address is
   normalized by the value folding algorithm in `spec/identifiers.md` §The value
   folding algorithm (trimmed whitespace, lowercase NFC, Unicode default case
   folding). Example: `email:alice@example.com`.
2. **User without email:** When no email address is available (e.g. users
   authenticated via third-party SAML without email disclosure, or deactivated
   accounts), the importer MUST emit `user:<normalized-handle>` (or the slugified user
   identifier if no handle exists), where the handle is normalized by the value
   folding algorithm in `spec/identifiers.md` §The value folding algorithm
   (trimmed whitespace, lowercase NFC, Unicode default case folding). Example: `user:bob`.
3. **System and bot actors:** Automated Linear integrations and bot users MUST
   map to `user:linear` or `user:<normalized-bot-name>`, where the bot name is
   normalized by the value folding algorithm in `spec/identifiers.md` §The value
   folding algorithm. Example: `user:linear`.
4. **Validation and bounds:**
   - Per `spec/identifiers.md` §The producer normalization rule, importers MUST
     write person identifiers in normalized form; unnormalized identifiers MUST
     NOT be written into op payloads.
   - Importers MUST NOT emit bare (colonless) strings; colonless strings are
     invalid person identifiers and are rejected by Writ schemas.
   - The scheme MUST match `^[a-z][a-z0-9+.-]*$` and be at most 32 characters.
   - The normalized value MUST be non-empty and at most 320 Unicode code points.
   - The total person identifier string MUST NOT exceed the derived bound of
     353 code points (32 + 1 + 320).
   - Over-long identifiers MUST be rejected, never truncated.
   - Schemes never unify: `email:alice@example.com` and `user:alice` are distinct
     identities in Writ and MUST NOT be merged by the importer.

### Team Scoping

In Linear, workflow states, issue keys, and triage queues are scoped to a team,
while an organization workspace often contains multiple teams.

Per ARCHITECTURE.md §Object homing:
- A Writ repository corresponds to a single team: workflow states, labels, and
  settings are repo-global, and there is no `team` collaborative object in
  v1 core.
- Importers MUST target one Writ repository per Linear team.
- Team-level workflow states, labels, and settings map to repo-global objects
  within that team's Writ repository.
- Cross-team relations in Linear (e.g. an issue in Team A blocking an issue in
  Team B) MAY be recorded as `link` ops with fully-qualified
  `<repo-id>#<object-id>` targets pointing into Team B's Writ repository. Writ
  does not resolve the target repository for such a reference, so importers
  SHOULD additionally record a human-readable external link (a Markdown link
  in the issue description or a comment) alongside the qualified `link` op.

### Identifier Preservation

Linear uses human-facing, sequential, team-scoped issue identifiers (such as
`WRIT-93` or `ENG-101`). Writ mints 128-bit random lowercase hexadecimal object
IDs (`^[0-9a-f]{32}$`, `spec/identifiers.md` §Object identifiers).

Losing the original Linear key breaks references across git commit history, pull
requests, external documentation, and human memory. Importers MUST preserve the
original Linear identifier using two complementary mechanisms:

1. **Indexable label:** Add a label formatted as `linear:<identifier>` (e.g.
   `linear:WRIT-93`, preserving the team prefix and issue number in uppercase ASCII
   for the key) via an `issue` `label` op (`add: ["linear:<identifier>"]`). This
   enables fast index queries in the projection (`store.Query.Issues(writ.IssueFilter{Label: []string{"linear:WRIT-93"}})`)
   without modifying the core issue schema.
2. **Provenance header:** Prepend a provenance blockquote to the issue description:
   ```markdown
   > Imported from Linear [WRIT-93](https://linear.app/<workspace>/issue/WRIT-93/...)
   ```
   If the external web URL is unavailable, the header MUST be formatted as:
   ```markdown
   > Imported from Linear WRIT-93
   ```

### Markdown Dialect Rewriting

Linear descriptions and comments use CommonMark augmented with custom XML tags
for entity references. Importers MUST rewrite these XML tags into standard
CommonMark / GitHub Flavored Markdown (GFM) before generating `create`, `update`,
or `comment` op bodies:

1. **Issue references:**
   `<issue id="..." href="URL">KEY</issue>` MUST be rewritten to `[KEY](URL)`.
   If `href` is absent, it MUST be rewritten to `KEY`.
2. **User mentions:**
   `<mention id="...">@Name</mention>` MUST be rewritten to `@Name`.
3. **Project references:**
   `<project id="..." href="URL">Name</project>` MUST be rewritten to `[Name](URL)`.
   If `href` is absent, it MUST be rewritten to `Name`.
4. Standard CommonMark and GFM syntax (fenced code blocks, blockquotes, tables,
   task lists, bold/italics) MUST be preserved verbatim.

### Deliberately Unmapped Concepts & Round-Trip Losses

#### Deliberately Unmapped Concepts

The following Linear concepts deliberately do NOT map to Writ v1 primitives:

1. **Cycles:** Linear cycles represent rolling time-boxed sprint automation and
   cadences, not static append-only SDLC artifacts. Sprint management belongs to
   client workflow automation. Importers MAY optionally record cycle membership as
   a label formatted as `cycle:<name>` (e.g. `cycle:Cycle-42`).
2. **Initiatives:** High-level portfolio tracking across projects is omitted from
   Writ v1 core to keep the object graph minimal and execution-focused.
3. **Triage Queue:** Linear's triage queue is an ephemeral review buffer. Issues
   in triage enter the Writ workspace backlog directly (with an optional `triage`
   label or corresponding workflow state).
4. **SLAs:** Service-level agreement notification timers, risk levels, and
   escalation rules are server-side automation features out of scope for a
   local-first, offline-capable git event-sourcing engine.
5. **Roadmaps & Saved Views:** Visual timeline views, filter queries, and personal
   view configurations belong to client user interfaces, not canonical repository data.
6. **Cross-Team Links:** Bounded by the one-repo-one-team model in v1
   (ARCHITECTURE.md §Object homing).
7. **Custom Fields:** Arbitrary organization-defined custom fields not part of
   the standard issue schema have no native typed representation in v1 core.

#### Round-Trip Data Losses

Because Writ prioritizes an open, minimal, and host-agnostic SDLC substrate in git,
importing from Linear and subsequent round-trip synchronization incurs the
following permanent data losses. Importers MUST document these losses and SHOULD
warn users about them:

- **Cycle cadence & history:** Sprint rolling timelines, velocity history, and
  cycle auto-rollover configurations are lost.
- **Initiative hierarchy:** Parent-child associations between projects and
  multi-team initiatives are lost.
- **Triage & SLA timers:** Triage entry timestamps, SLA risk warnings, SLA
  breach timestamps, and response timers are lost.
- **Native cross-team DAG relations:** Cross-team dependency graphs degrade to
  unindexed external Markdown links.
- **Granular link relations:** Under basic v1 schemas where `relation` is
  constrained to `"fixes"`, `"relates"`, and `"none"`, fine-grained relations
  (`"blocks"`, `"duplicate"`, `"parent"`, `"implementation-plan"`) degrade to
  `"relates"`, losing directed dependency semantics.
- **Entity internal UUIDs:** Linear's internal UUIDs on issues, comments, labels,
  and tags are replaced by Writ's 128-bit random IDs (though preserved in
  provenance headers and `linear:<key>` labels).
- **XML tag metadata:** Internal UUID attributes on `<issue>`, `<mention>`, and
  `<project>` tags are lost during CommonMark normalization.

