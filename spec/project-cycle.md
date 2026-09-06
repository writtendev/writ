# Project and Cycle Operations — project, cycle (v1)

Status: **normative**. Schema: [`schemas/project-ops.schema.json`](schemas/project-ops.schema.json), [`schemas/cycle-ops.schema.json`](schemas/cycle-ops.schema.json).
Vectors: [`testdata/project/`](testdata/project/), [`testdata/cycle/`](testdata/cycle/).
Field rules: [`testdata/project/field-rules.json`](testdata/project/field-rules.json), [`testdata/cycle/field-rules.json`](testdata/cycle/field-rules.json).
Fixtures: [`spec/fixtures/testdata/descriptions/project-*.yaml`](fixtures/testdata/descriptions/), [`spec/fixtures/testdata/descriptions/cycle-*.yaml`](fixtures/testdata/descriptions/).

This document defines the operation vocabularies, payload schemas, and fold
semantics for repo-scoped grouping objects in Writ: `project` (`object_type: "project"`)
and `cycle` (`object_type: "cycle"`). It covers project creation, metadata updates,
status transitions, cycle creation, cycle date ranges, and issue membership
(ARCHITECTURE.md §Object types).

These objects are specified in v1 so the storage schemas evolve without flag
days, even though user-facing clients for projects and cycles will arrive in a
later development phase.

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

## Scope & Object Model

Projects and cycles are collaborative objects that group issues. Like every
collaborative object, they reside in the repository the client is operating
on (ARCHITECTURE.md §Object homing).

- A `project` represents an initiative or goal with an explicit lifecycle status
  (`planned`, `active`, `paused`, `completed`, `canceled`) and a set of member issues.
- A `cycle` represents a time-bounded iteration or cadence defining a half-open
  time window `[starts_at, ends_at)` and a set of member issues.

### Decisions & Rationale

#### 1. Membership Direction
Fold operates per `object_id` (`spec/fold.md` §1). Placing membership operations
(`add-issue`, `remove-issue`) on the grouping object (`project` or `cycle`)
allows "what issues are in this project/cycle" to be answered by folding a
single object.

The converse query ("what projects or cycles is this issue in?") requires folding
grouping objects. As established in `spec/identifiers.md` §Out of scope, reverse
lookups and backlinks are maintained by the SQLite projection layer as a derived
cache. Placing membership on the grouping objects allows an issue to belong to
multiple projects or cycles across independent writers without write coordination
or commit conflicts.

#### 2. Add-Wins Concurrency for Membership
Issue membership in projects and cycles is folded using the `set-observed-remove`
merge strategy (`spec/fold.md` §5.4). When concurrent `add-issue` and
`remove-issue` operations target the same issue reference ($a \parallel r$),
the addition wins and the issue remains present in the folded member set.
The normative vectors `spec/testdata/fold/merge/set-observed-remove-causal.json`
and `spec/testdata/fold/merge/set-observed-remove-concurrent.json` pin this
behavior.

#### 3. Reference-Form Aliasing (Producer Rule)
Fold performs no reference resolution or string normalization; set elements
compare as exact strings (`spec/fold.md` §6, `spec/identifiers.md` §Separation
from fold). Therefore, a bare reference `0123456789abcdef0123456789abcdef` and a
qualified reference `repo-id#0123456789abcdef0123456789abcdef` to the same issue
would be treated as two distinct elements by fold.

To prevent aliasing:
- Producers MUST emit references in canonical lowercase form (`spec/identifiers.md` §Rule 1).
- Producers SHOULD emit bare `<object-id>` references for same-repository links and
  `<repo-id>#<object-id>` for cross-repository links (`spec/identifiers.md` §Rule 3).

#### 4. Cycle Dates are UTC Instants Written Together
`starts_at` and `ends_at` are RFC 3339 UTC timestamps that define the half-open
instant interval `[starts_at, ends_at)`. Day-granular date strings (e.g. `2026-09-01`)
are omitted to avoid timezone and boundary ambiguities.

Both `starts_at` and `ends_at` MUST be provided together in `create` and
`set-dates` operations. Because fold applies `lww` per field, requiring both fields
in a single operation ensures concurrent date updates never interleave into an
inverted or crossed time window. An invariant rule requires `ends_at` to be
strictly after `starts_at` ($ends\_at > starts\_at$).

#### 5. No Cycle Status Operation
A cycle does not define a `set-status` operation. A cycle's lifecycle state
(`upcoming`, `active`, `completed`) is a pure function of current wall-clock time
relative to `[starts_at, ends_at)` evaluated by clients or projections. Storing a
mutable status field would introduce dual sources of truth and unnecessary
transition lattices.

#### 6. No Tombstones in v1
Mirroring code reviews (`spec/review-ops.md`), projects and cycles are not
destroyed in v1. A project may transition to `canceled`, which is an explicit
lifecycle status rather than a tombstone. Objects remain permanently in the git
DAG.

#### 7. Deliberate Omissions (No PM-Tool Parity)
Per VISION.md §Non-goals, Writ is an open SDLC foundation rather than an all-in-one
project management platform. The following features are intentionally omitted
from the v1 project and cycle schemas:
- Leads, owners, and individual assignees on grouping objects.
- Priority, health assessments, and status update posts.
- Hierarchical or nested projects/sub-projects.
- Milestones and epics.
- Capacity, velocity, and story-point estimates.
- Manual issue ordering, priority ranking, or swimlane configurations.
- GitHub Projects v2 / GitHub Milestones import mappings (the GitHub bridge is
  strictly PR/comment ⇄ ops sync; see Appendix A).

## Envelope Binding

Every project and cycle operation is carried in a git commit whose `op.json`
payload conforms to `spec/schemas/op-envelope.schema.json` and the corresponding
vocabulary schema (`spec/schemas/project-ops.schema.json` or
`spec/schemas/cycle-ops.schema.json`):

- `object_type` MUST be `"project"` or `"cycle"`.
- `op_version` MUST be an integer ≥ 1. This document specifies version `1`.
- `object_id` MUST be a non-empty string identifier (1–256 characters,
  printable non-space ASCII `^[\x21-\x7e]+$`). Conforming producers mint
  32-character lowercase hex IDs (`spec/identifiers.md`).
- `op_type` MUST be one of the defined operation types, or an unknown string
  tolerated under forward-compatibility rules.
- `body` MUST be a JSON object conforming to the schema for the declared `op_type`
  and `op_version`.

## Project Operation Vocabulary (`op_version: 1`)

The `project` family defines five operation types:

| `op_type` | Body Schema | Description |
| --- | --- | --- |
| `create` | `{"title": string, "description"?: string}` | Project creation and metadata. |
| `update` | `{"title"?: string, "description"?: string}` | Metadata edits (at least one of `title` or `description`). |
| `set-status` | `{"status": enum, "reason"?: string}` | Lifecycle transition (`planned`, `active`, `paused`, `completed`, `canceled`). |
| `add-issue` | `{"issue": reference}` | Add an issue to `project.issues`. |
| `remove-issue` | `{"issue": reference}` | Remove an issue from `project.issues`. |

### 1. `create`

Initializes a project object.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "project",
  "op_type": "create",
  "op_version": 1,
  "body": {
    "title": "Authentication Redesign",
    "description": "Redesign auth flow to support SAML and OIDC"
  }
}
```

- `title` (required): Non-empty string (`minLength: 1`).
- `description` (optional): String markdown description.

### 2. `update`

Updates project metadata. At least one of `title` or `description` MUST be present.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "project",
  "op_type": "update",
  "op_version": 1,
  "body": {
    "title": "Authentication & SSO Redesign"
  }
}
```

### 3. `set-status`

Updates the project lifecycle state.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "project",
  "op_type": "set-status",
  "op_version": 1,
  "body": {
    "status": "paused",
    "reason": "Waiting on upstream API release"
  }
}
```

- `status` (required): One of `"planned"`, `"active"`, `"paused"`, `"completed"`, `"canceled"`.
- `reason` (optional): String explanation for the transition.

### 4. `add-issue`

Adds an issue to the project's member set.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "project",
  "op_type": "add-issue",
  "op_version": 1,
  "body": {
    "issue": "abcdef0123456789abcdef0123456789"
  }
}
```

- `issue` (required): Reference string matching the reference grammar in
  `spec/schemas/identifiers.schema.json#/$defs/reference`.

### 5. `remove-issue`

Removes an issue from the project's member set.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "project",
  "op_type": "remove-issue",
  "op_version": 1,
  "body": {
    "issue": "abcdef0123456789abcdef0123456789"
  }
}
```

- `issue` (required): Reference string matching the reference grammar.

---

## Cycle Operation Vocabulary (`op_version: 1`)

The `cycle` family defines five operation types:

| `op_type` | Body Schema | Description |
| --- | --- | --- |
| `create` | `{"title": string, "starts_at": timestamp, "ends_at": timestamp, "description"?: string}` | Cycle creation with title and date interval. |
| `update` | `{"title"?: string, "description"?: string}` | Metadata edits (at least one of `title` or `description`). |
| `set-dates` | `{"starts_at": timestamp, "ends_at": timestamp}` | Update cycle date interval (both required). |
| `add-issue` | `{"issue": reference}` | Add an issue to `cycle.issues`. |
| `remove-issue` | `{"issue": reference}` | Remove an issue from `cycle.issues`. |

### 1. `create`

Initializes a cycle object.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "cycle",
  "op_type": "create",
  "op_version": 1,
  "body": {
    "title": "Cycle 42",
    "starts_at": "2026-09-01T00:00:00Z",
    "ends_at": "2026-09-15T00:00:00Z",
    "description": "Two-week development cycle for core storage"
  }
}
```

- `title` (required): Non-empty string (`minLength: 1`).
- `starts_at` (required): RFC 3339 UTC timestamp.
- `ends_at` (required): RFC 3339 UTC timestamp strictly greater than `starts_at`.
- `description` (optional): String markdown description.

### 2. `update`

Updates cycle title or description. At least one field MUST be present.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "cycle",
  "op_type": "update",
  "op_version": 1,
  "body": {
    "title": "Cycle 42 — Extended"
  }
}
```

### 3. `set-dates`

Updates the cycle time window. Both fields MUST be specified together.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "cycle",
  "op_type": "set-dates",
  "op_version": 1,
  "body": {
    "starts_at": "2026-09-01T00:00:00Z",
    "ends_at": "2026-09-20T00:00:00Z"
  }
}
```

- `starts_at` (required): RFC 3339 UTC timestamp.
- `ends_at` (required): RFC 3339 UTC timestamp strictly greater than `starts_at`.

### 4. `add-issue`

Adds an issue to the cycle's member set.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "cycle",
  "op_type": "add-issue",
  "op_version": 1,
  "body": {
    "issue": "abcdef0123456789abcdef0123456789"
  }
}
```

### 5. `remove-issue`

Removes an issue from the cycle's member set.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "cycle",
  "op_type": "remove-issue",
  "op_version": 1,
  "body": {
    "issue": "abcdef0123456789abcdef0123456789"
  }
}
```

---

## Field Rules & Merge Strategies

Every field in the `project` and `cycle` op vocabularies maps to a merge strategy
from `spec/fold.md`'s closed catalogue. Machine-readable field rules are published in
`spec/testdata/project/field-rules.json` and `spec/testdata/cycle/field-rules.json`.

Per `spec/fold.md`, **any field without a declared strategy is not merged**; it is
treated as unknown data, preserved in the DAG, and ignored during fold.

### Project Field Rules

| `op_type` | Field | Merge Strategy | Details |
| --- | --- | --- | --- |
| `create` | `title` | `lww` | Last writer wins in total order |
| `create` | `description` | `lww` | Last writer wins |
| `update` | `title` | `lww` | Last writer wins |
| `update` | `description` | `lww` | Last writer wins |
| `set-status` | `status` | `lww` | Last writer wins |
| `set-status` | `reason` | `lww` | Last writer wins |
| `add-issue` | `issue` | `set-observed-remove` | Add-wins set on `project.issues` |
| `remove-issue` | `issue` | `set-observed-remove` | Add-wins set on `project.issues` |

### Cycle Field Rules

| `op_type` | Field | Merge Strategy | Details |
| --- | --- | --- | --- |
| `create` | `title` | `lww` | Last writer wins in total order |
| `create` | `starts_at` | `lww` | Last writer wins |
| `create` | `ends_at` | `lww` | Last writer wins |
| `create` | `description` | `lww` | Last writer wins |
| `update` | `title` | `lww` | Last writer wins |
| `update` | `description` | `lww` | Last writer wins |
| `set-dates` | `starts_at` | `lww` | Last writer wins |
| `set-dates` | `ends_at` | `lww` | Last writer wins |
| `add-issue` | `issue` | `set-observed-remove` | Add-wins set on `cycle.issues` |
| `remove-issue` | `issue` | `set-observed-remove` | Add-wins set on `cycle.issues` |

---

## Deletion and Retraction Semantics

- **Project lifecycle:** A project is never deleted. Statuses such as `"completed"`
  or `"canceled"` are lifecycle states, not tombstones. There are no tombstones
  defined for `project` or `cycle` in v1.
- **Membership removal:** An issue is removed from a project or cycle by emitting
  `remove-issue`. When folded via `set-observed-remove`, the issue is excluded
  from the materialized `issues` array unless a concurrent `add-issue` exists.

## Forward Compatibility & Unknown Fields

- **Unknown body fields:** Conforming implementations MUST preserve and ignore
  any unknown fields in op bodies.
- **Unknown `op_type` or future `op_version`:** Ops with unknown op types or
  unsupported versions for `object_type: "project"` or `object_type: "cycle"`
  MUST remain in the DAG and contribute to total ordering (`t*`) and ancestry,
  but contribute no field mutations to known state (WRIT-15).

---

## Appendix A — External Integrations & Bridge Scope (Informative)

The Writ-GitHub bridge synchronizes pull requests, code reviews,
and inline comment threads bidirectionally with Writ operations.

GitHub Projects (v2) and GitHub Milestones are not synchronized by the bridge
and have no operational mapping in `spec/testdata/`. Grouping and cycle entities
in Writ are self-contained, repo-scoped primitives.
