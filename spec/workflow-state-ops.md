# Workflow State Operations — state, column ordering, and lifecycle (v1)

Status: **normative**. Schema: [`schemas/workflow-state-ops.schema.json`](schemas/workflow-state-ops.schema.json).
Field rules: [`testdata/workflow-state/field-rules.json`](testdata/workflow-state/field-rules.json).
Ordering: [`spec/ordering.md`](ordering.md).

This document defines the operation vocabulary, payload schemas, and fold
semantics for workflow states in Writ (`object_type: "workflow-state"`).
Workflow states represent the board columns and lifecycle stages of issues
in a repository.

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

---

## 1. Scope & Object Model

In Writ, **each workflow state is its own collaborative object**
(`object_type: "workflow-state"`), carrying a user-defined display name,
a semantic type, a fractional index column position, and optional visual hints.
Issues reference workflow states by object identifier (`object_id`).

Workflow states live in the repository the client is operating on and are
repo-global (ARCHITECTURE.md §Object homing): all projects and issues in
that repository share the common workflow state definitions.

### 1.1. Why States Are Collaborative Objects

An ordered array of states within a single workspace configuration object
would make column reordering a sequence operation requiring complex sequence
CRDTs. By giving each workflow state its own collaborative object with an
independent `position` field, column reordering reduces to updating a scalar
field governed by standard Last-Writer-Wins (`lww`) in total order $L$.

Fractional indexing ([`spec/ordering.md`](ordering.md)) allows arbitrary column
insertions between adjacent columns without renumbering siblings.

### 1.2. The Five Semantic Types

`type` is the load-bearing semantic field. It allows clients, automatons, and
agents to understand the meaning of custom columns (such as "In Review", "QA",
or "Shipped") without hard-coding proprietary vocabularies:

| `type` | Description |
| --- | --- |
| `backlog` | Issues gathered for future triage or grooming; not scheduled for immediate work. |
| `unstarted` | Issues prioritized and ready for work (e.g. "Todo", "Next"). |
| `started` | Issues actively in development, review, or QA (e.g. "In Progress", "In Review"). |
| `completed` | Issues successfully finished and closed (e.g. "Done", "Shipped"). |
| `canceled` | Issues abandoned, duplicate, or rejected (e.g. "Canceled", "Won't Fix"). |

Conforming implementations MUST enforce that `type` is one of these five values.

---

## 2. Envelope Binding

Every workflow-state operation is carried in a git commit whose `op.json` payload
conforms to `spec/schemas/op-envelope.schema.json` and
`spec/schemas/workflow-state-ops.schema.json`:

- `object_type` MUST be `"workflow-state"`.
- `op_version` MUST be an integer ≥ 1. This document specifies version `1`.
- `object_id` MUST be a non-empty string identifier (1–256 characters,
  printable non-space ASCII `^[\x21-\x7e]+$`). Canonical Writ-minted object IDs
  are 32 lowercase hexadecimal characters (`^[0-9a-f]{32}$`).
- `op_type` MUST be one of the operation types defined below, or an unknown
  string tolerated under forward-compatibility rules.
- `body` MUST be a JSON object conforming to the schema for the declared
  `op_type` and `op_version`.

Operations are stored on append chains under:
```
refs/writ/<writer-id>/workflow-state
```

---

## 3. Operation Vocabulary

The workflow-state family defines two operation types for `op_version: 1`:

| `op_type` | Body Schema | Description |
| --- | --- | --- |
| `create` | `{"name": string, "type": enum, "position": position, "color"?: string, "description"?: string}` | Create a workflow state. |
| `update` | `{"name"?: string, "type"?: enum, "position"?: position, "color"?: string, "description"?: string}` | Update workflow state properties. |

### 3.1. `create`

Initializes a workflow-state collaborative object.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "workflow-state",
  "op_type": "create",
  "op_version": 1,
  "body": {
    "name": "In Review",
    "type": "started",
    "position": "l",
    "color": "#f2c94c",
    "description": "Work submitted for peer review"
  }
}
```

- `name` (string, required): User-facing display name, minLength 1.
- `type` (string, required): One of `backlog`, `unstarted`, `started`, `completed`, `canceled`.
- `position` (string, required): Canonical fractional index key conforming to [`spec/ordering.md`](ordering.md).
- `color` (string, optional): Client presentation color hint (e.g. hex color code `"#f2c94c"`).
- `description` (string, optional): Human description of the state's intent.

### 3.2. `update`

Updates one or more properties of an existing workflow state.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "workflow-state",
  "op_type": "update",
  "op_version": 1,
  "body": {
    "name": "Code Review",
    "color": "#e2b93c"
  }
}
```

At least one property (`name`, `type`, `position`, `color`, or `description`)
MUST be present in an `update` op body. An empty `{}` body is invalid.

---

## 4. Fold Semantics & Merge Strategies

Every body property defined in this vocabulary is mapped to the standard
`lww` (Last-Writer-Wins) merge strategy in total order $L$ ([`spec/fold.md`](fold.md) §5.1).
A machine-readable copy of these rules is published in
`spec/testdata/workflow-state/field-rules.json`.

Folded state is `WorkflowState{name, type, position, color, description, unknown_ops}`.

| `op_type` | Field | Merge Strategy | Details |
| --- | --- | --- | --- |
| `create` | `name` | `lww` | Last writer wins in total order $L$ |
| `create` | `type` | `lww` | Last writer wins |
| `create` | `position` | `lww` | Last writer wins |
| `create` | `color` | `lww` | Last writer wins |
| `create` | `description` | `lww` | Last writer wins |
| `update` | `name` | `lww` | Last writer wins |
| `update` | `type` | `lww` | Last writer wins |
| `update` | `position` | `lww` | Last writer wins |
| `update` | `color` | `lww` | Last writer wins |
| `update` | `description` | `lww` | Last writer wins |

---

## 5. Column Ordering & Deterministic Tiebreak

When querying or presenting workflow states as columns on a board:

1. **Primary Sort Key:** `position` ascending (standard ASCII byte comparison).
2. **Secondary Sort Key:** `op_id` (git commit SHA of the winning operation) ascending (standard ASCII byte comparison).

In SQLite projections:
```sql
SELECT * FROM workflow_states ORDER BY position ASC, op_id ASC;
```

Concurrent insertions at identical fractional positions resolve deterministically
via op-id tiebreak without rebalancing or data loss.

---

## 6. Distributed Referential Integrity: Unknown-State References

In an eventually-consistent, append-only distributed event log, writers create
and move issues asynchronously across isolated branches.

**Rule:** Issues referencing an unknown, missing, or unfetched state ID MUST fold
cleanly without error or rejection.
Referential integrity cannot be enforced across independent append-only writer logs.
Attempting to reject an issue operation that references an unfetched state ID would
cause two replicas with different fetch states to compute diverging states from
the same op history, breaking convergence.

When a client or projection encounters an issue referencing an unknown state:
1. The issue folds cleanly, preserving the target state reference verbatim.
2. The client renders the issue in a fallback "Unknown" column (or at the end of the board).
3. When the defining `workflow-state` operation arrives in a subsequent fetch, the
   reference heals automatically and the issue projects into its defined column.

---

## 7. Default Starter States

Upon workspace initialization (`writ init`), Writ seeds five default starter
workflow states in `refs/writ/<writer-id>/workflow-state` if none exist:

1. **Backlog** (`type: "backlog"`, `position: "1"`)
2. **Todo** (`type: "unstarted"`, `position: "V"`)
3. **In Progress** (`type: "started"`, `position: "k"`)
4. **Done** (`type: "completed"`, `position: "s"`)
5. **Canceled** (`type: "canceled"`, `position: "zV"`)

These are ordinary collaborative objects: workspace members can rename, reorder,
recolor, or replace them as needed using standard operations.
