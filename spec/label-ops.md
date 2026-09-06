# Label Operations — label, metadata, and lifecycle (v1)

Status: **normative**. Schema: [`schemas/label-ops.schema.json`](schemas/label-ops.schema.json).
Field rules: [`testdata/label/field-rules.json`](testdata/label/field-rules.json).
Forward compatibility: [`spec/forward-compatibility.md`](forward-compatibility.md).

This document defines the operation vocabulary, payload schemas, and fold
semantics for labels in Writ (`object_type: "label"`). Labels represent
categorization, tags, and visual indicators attached to issues and reviews in
a repository.

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

---

## 1. Scope & Object Model

In Writ, **each label is its own collaborative object**
(`object_type: "label"`), carrying a user-defined display name (`name`), an
optional client presentation color hint (`color`), and an optional human
description (`description`). Issues and reviews reference labels by object
identifier (`object_id`).

Labels live in the repository the client is operating on and are repo-global
(ARCHITECTURE.md §Object homing): all projects, issues, and reviews in that
repository share the common label definitions.

### 1.1. Why Labels Are Collaborative Objects

When labels are bare strings directly embedded on issues and reviews, renaming
a label requires rewriting the label set on every single object carrying it
($O(N)$ operations across the entire repository). As independent collaborative
objects, a rename or color adjustment is a single operation on the `label`
object itself. Furthermore, object-backed labels prevent accidental duplicate
labels caused by case or formatting discrepancies (`bug` vs `Bug`).

### 1.2. Label Groups

Label groups (parent-child collections enforcing mutual exclusion, such that an
issue can carry at most one member of a group) are **explicitly omitted** from
v1 core.

In an append-only distributed event log where concurrent writers operate
without central locking, mutual exclusion cannot be guaranteed: two writers can
concurrently apply two members of the same group to an issue, and both
operations are mathematically valid. An append-only log cannot discard one
concurrent operation without arbitrary data loss. Introducing group objects into
v1 would introduce schema weight and speculative abstraction for a constraint
that could only ever be enforced client-side. If client-side grouping hints are
desired in a future version, an optional `group` string or `parent_id`
reference may be added additively without breaking changes.

---

## 2. Envelope Binding

Every label operation is carried in a git commit whose `op.json` payload
conforms to `spec/schemas/op-envelope.schema.json` and
`spec/schemas/label-ops.schema.json`:

- `object_type` MUST be `"label"`.
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
refs/writ/<writer-id>/label
```

---

## 3. Operation Vocabulary

The label family defines two operation types for `op_version: 1`:

| `op_type` | Body Schema | Description |
| --- | --- | --- |
| `create` | `{"name": string, "color"?: string, "description"?: string}` | Create a label. |
| `update` | `{"name"?: string, "color"?: string, "description"?: string}` | Update label properties. |

### 3.1. `create`

Initializes a label collaborative object.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "label",
  "op_type": "create",
  "op_version": 1,
  "body": {
    "name": "bug",
    "color": "#d73a4a",
    "description": "Something isn't working"
  }
}
```

- `name` (string, required): User-facing display name, minLength 1.
- `color` (string, optional): Client presentation color hint (e.g. hex color code `"#d73a4a"`).
- `description` (string, optional): Human description of the label's intent.

### 3.2. `update`

Updates one or more properties of an existing label.

```jsonc
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "label",
  "op_type": "update",
  "op_version": 1,
  "body": {
    "name": "defect",
    "color": "#e2b93c"
  }
}
```

At least one property (`name`, `color`, or `description`) MUST be present in an
`update` op body. An empty `{}` body is invalid.

---

## 4. Fold Semantics & Merge Strategies

Every body property defined in this vocabulary is mapped to the standard
`lww` (Last-Writer-Wins) merge strategy in total order $L$ ([`spec/fold.md`](fold.md) §5.1).
A machine-readable copy of these rules is published in
`spec/testdata/label/field-rules.json`.

Folded state is `Label{name, color, description, unknown_ops}`.

| `op_type` | Field | Merge Strategy | Details |
| --- | --- | --- | --- |
| `create` | `name` | `lww` | Last writer wins in total order $L$ |
| `create` | `color` | `lww` | Last writer wins |
| `create` | `description` | `lww` | Last writer wins |
| `update` | `name` | `lww` | Last writer wins |
| `update` | `color` | `lww` | Last writer wins |
| `update` | `description` | `lww` | Last writer wins |

---

## 5. Distributed Referential Integrity: Unknown-Label References

Issues and reviews reference labels by object identifier in their respective
`label` operations (`add` and `remove` arrays).

Per normative rule `FC-16` ([`spec/forward-compatibility.md`](forward-compatibility.md)
§Unknown object references), **an operation referencing an unknown or unfetched
label ID MUST fold normally without error or rejection**.

When an issue or review references a label object that has not yet arrived
locally:
1. The issue/review folds cleanly under its add-wins OR-set, carrying the target
   label reference verbatim.
2. The client projection displays the raw label reference as a fallback string.
3. When the defining `label` object operations arrive in a subsequent fetch,
   the reference resolves and automatically renders the label's defined display
   name and color.
