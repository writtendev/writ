# Review Operations — review, revision, assign, approval, ci-status, label, link (v1)

Status: **normative**. Schema: [`schemas/review-ops.schema.json`](schemas/review-ops.schema.json).
Vectors: [`testdata/review-ops/`](testdata/review-ops/).
Field rules: [`testdata/review-ops/field-rules.json`](testdata/review-ops/field-rules.json).

This document defines the operation vocabulary, payload schemas, and fold
semantics for code reviews in Writ (`object_type: "review"`). It covers
review creation, revision pushes, status transitions, assignments (requested
reviewers), approvals and review votes, CI status attachments, labels,
and cross-reference links (ARCHITECTURE.md §Object types).

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

## Scope & Object Model

A code review is represented in Writ as a single collaborative object with
`object_type: "review"`. Approvals, change requests, assignments, CI status
reports, labels, and cross-reference links are operations that fold directly
into fields of the `review` object rather than standalone collaborative objects.

### Decisions behind the vocabulary

- **Decision: what is an object, and what is an op:**
  ARCHITECTURE.md §Object types originally sketched `approval` and `ci-status`
  alongside `review` and `comment` as repo-scoped object types. Under the
  per-writer chain-ref layout (ARCHITECTURE.md §Ref layout, WRIT-7), object
  membership is determined by the payload's `object_id`. If approvals and CI
  statuses were standalone objects, each approval and status would require its
  own object ID and a back-reference to the review, forcing readers to walk
  every writer's approval and status chains and build secondary join indices
  just to materialize a single review.
  Comments justify separate object status (WRIT-9) because they carry rich
  text bodies, multi-turn threading, position edits, and are the primary volume
  driver in code reviews. A vote, assignment, CI check status, label set mutation,
  or cross-reference link does not: an approval is a small vote record scoped to
  `(subject, revision)`, an assignment is an add-wins set mutation, a CI status
  is a status record scoped to `(revision, name)`, labels are an add-wins OR-set,
  and a link is a keyed last-writer-wins relation scoped to `target`.
  Therefore, `review` is the single collaborative object type defined in this
  specification. Assignments, approvals, CI statuses, labels, and links are
  operations on the review object that fold into the materialized `Review` state
  (`Review{title, description, status, merge_commit, reason, assignees, labels,
  links, revisions, approvals, ci_statuses}`), matching the reducer signature
  established in ARCHITECTURE.md §The six machines.
- **Requested reviewer vs. Assignee:**
  GitHub maintains separate lists for `assignees` (who owns the PR) and
  `requested_reviewers` (who was asked to review); Gerrit has `reviewers` and
  `CC`. In Writ, reviews define a single unified `assignees` list via the
  `assign` op. One unified list is simpler, eliminates ambiguity about who is
  expected to act, mirrors the `issue` object model symmetrically (`object_type:
  "issue"`), and allows clients, bridges, and UI components to share routing
  and assignment logic across object types.
- **`assign` and `label` are add-wins OR-sets (`set-observed-remove`):**
  Concurrent assignment or labelling on one device and removal on another is
  reconciled via `set-observed-remove` (WRIT-12, `spec/fold.md`). Additions win
  over concurrent removals. Assignee values are scheme-prefixed person
  identifiers ([`spec/identifiers.md`](identifiers.md) §Person identifiers),
  normalized (scheme lowercased; value trimmed and case-folded) prior to set
  evaluation; schemes never unify, so `user:alice` and `email:alice@example.com`
  are two members. Labels are references to `label` objects.
- **Link directionality: single-sided with derived inverse:**
  Links are declared single-sided on the object being authored (e.g. a review
  links to an issue with `relation: "fixes"`). This design avoids multi-repo
  atomic writes and eliminates inconsistencies where two objects could disagree.
  The projection cache builds indices (such as `review_links` and `issue_links`)
  to answer reverse queries (e.g. finding which reviews close an issue, or which
  issues a review fixes) without requiring writers to write to both refs.
  A review can express "closes &lt;issue&gt;" cross-repo using workspace-global
  references (`<repo-id>#<issue-id>`). If both objects declare links to each other,
  each side folds independently via `keyed-lww` on its own object without conflict.
- **Interaction with `approval`:**
  Approving a review does **not** automatically clear the assignment or request.
  The assignment is a historical fact about who was asked to review, while an
  approval is a fact about what evaluation occurred. Collapsing or automatically
  clearing assignments upon approval destroys history in an append-only log
  designed to preserve historical fidelity. Explicit removals (`"remove": [...]`)
  are required to unassign a reviewer.

### Scope boundaries

- **Comments and inline threading:** Review comments and discussion threads
  are defined in WRIT-9. Comments reference a review object by its `object_id`
  and anchor to content via the dual-sided anchor format (WRIT-13).
- **Cross-repo references:** Review objects are repo-scoped. Linking a review
  to an issue or project across repositories uses workspace-global IDs
  (WRIT-16).
- **Fold driver and total ordering:** The deterministic linear extension
  `t*(op)` and DAG ordering rules are specified in WRIT-12.

## Envelope Binding

Every review operation is carried in a git commit whose `op.json` payload
conforms to `spec/schemas/op-envelope.schema.json` and
`spec/schemas/review-ops.schema.json`:

- `object_type` MUST be `"review"`.
- `op_version` MUST be an integer ≥ 1. This document specifies version `1`.
- `object_id` MUST be a non-empty string identifier (1–256 characters,
  printable non-space ASCII `^[\x21-\x7e]+$`).
- `op_type` MUST be one of the operation types defined below, or an unknown
  string tolerated under forward-compatibility rules.
- `body` MUST be a JSON object conforming to the schema for the declared
  `op_type` and `op_version`.

Commit OIDs within op bodies (e.g. `base`, `head`, `revision`,
`merge_commit`) MUST be lowercase hexadecimal strings of either 40 characters
(SHA-1) or 64 characters (SHA-256). All OIDs within an op payload MUST have
uniform length, matching the repository's single object format.

## Operation Vocabulary

The review family defines nine operation types for `op_version: 1`:

| `op_type` | Body Schema | Description |
| --- | --- | --- |
| `create` | `{"title": string, "description"?: string}` | Review creation and metadata. |
| `revision` | `{"base": oid, "head": oid}` | Code revision push (base and head commits). |
| `update` | `{"title"?: string, "description"?: string}` | Metadata edits (title, description). |
| `set-status` | `{"status": enum, "merge_commit"?: oid, "reason"?: string}` | Status transitions (`draft`, `open`, `closed`, `merged`). |
| `assign` | `{"add"?: [person-id], "remove"?: [person-id]}` | Add or remove review assignees (requested reviewers). |
| `approval` | `{"revision": oid, "verdict": enum, "subject"?: person-id, "message"?: string}` | Review vote (`approve`, `request-changes`, `none`). |
| `ci-status` | `{"revision": oid, "name": string, "state": enum, "url"?: string, "description"?: string, "started_at"?: timestamp, "completed_at"?: timestamp, "external_id"?: string}` | CI check result on a revision head. |
| `label` | `{"add"?: [reference], "remove"?: [reference]}` | Add or remove review labels (referencing label object IDs, FC-16). |
| `link` | `{"target": reference, "target_type"?: string, "relation": "fixes"\|"relates"\|"none"}` | Associate or retract cross-references (e.g. closes issue). |

### 1. `create`

Initializes a review object.

```jsonc
{
  "object_id": "r-7f3a",
  "object_type": "review",
  "op_type": "create",
  "op_version": 1,
  "body": {
    "title": "Add OAuth2 authentication provider",
    "description": "Implements GitHub and Google OAuth2 login flows."
  }
}
```

- `title` (string, required): Summary title of the review, minLength 1.
- `description` (string, optional): Full Markdown description / pull request body.

**Load-bearing design: `create` carries no `base` or `head`.**
Every revision — including the very first revision — is recorded as a
`revision` op. Placing `base`/`head` in `revision` ensures that initial review
creation, branch updates, and force-pushes share the exact same append
pipeline and data structure. If `create` held base/head, revision 1 would have
two competing representations and fold implementations would require special
casing.

### 2. `revision`

Appends a code revision to the review.

```jsonc
{
  "object_id": "r-7f3a",
  "object_type": "review",
  "op_type": "revision",
  "op_version": 1,
  "body": {
    "base": "0123456789abcdef0123456789abcdef01234567",
    "head": "89abcdef0123456789abcdef0123456789abcdef"
  }
}
```

- `base` (OID string, required): Base commit OID against which the revision
  is compared.
- `head` (OID string, required): Head commit OID of the revision.

#### Revision model and force-pushes

1. **Derived revision numbers:** Revision numbers are derived at fold time.
   Revisions are numbered $1 \dots n$ according to their appearance in the
   fold's deterministic total order (WRIT-12). Producers NEVER write revision
   numbers. Two concurrent writers pushing revisions cannot conflict or both
   claim "revision 2".
2. **Preserving history across force-pushes:** A force-push (or `synchronize`
   event) is simply a new `revision` op appended to the review's DAG. Previous
   `revision` ops are never overwritten or deleted; historical revisions and
   their associated comments remain preserved in the repository history by
   construction.
3. **Referencing revisions by head OID:** Subsequent ops that reference a
   revision (such as `approval`, `ci-status`, or line comments) reference the
   revision by its **`head` commit OID**, not by an ephemeral integer revision
   number or the `revision` op's commit SHA. The head commit OID represents
   what the reviewer or CI runner actually evaluated, matches external systems
   (such as GitHub's `commit_id`), and allows an author to vote or attach CI
   without having fetched the specific `revision` op commit beforehand.
4. **Shared head tiebreak:** In the rare case where multiple revisions in a
   review share the exact same `head` OID (e.g. a revert and re-push), any
   reference to that `head` OID attaches to the **earliest revision** with that
   head in the fold's total order.

### 3. `update`

Modifies review metadata.

```jsonc
{
  "object_id": "r-7f3a",
  "object_type": "review",
  "op_type": "update",
  "op_version": 1,
  "body": {
    "title": "Add OAuth2 authentication provider (Google & GitHub)"
  }
}
```

- `title` (string, optional): Updated review title, minLength 1.
- `description` (string, optional): Updated review description.

At least one of `title` or `description` MUST be present in an `update` body.
An empty `{}` update body is invalid.

### 4. `set-status`

Transitions the review lifecycle state.

```jsonc
{
  "object_id": "r-7f3a",
  "object_type": "review",
  "op_type": "set-status",
  "op_version": 1,
  "body": {
    "status": "merged",
    "merge_commit": "abcdef0123456789abcdef0123456789abcdef01"
  }
}
```

- `status` (string, required): One of `"draft"`, `"open"`, `"closed"`,
  `"merged"`.
- `merge_commit` (OID string, optional): Commit OID representing the merged
  result in the target branch.
- `reason` (string, optional): Human-readable explanation for the status
  change (e.g. why a review was closed).

**Producer rule on `merged` status:** Producers MUST NOT emit a status
transition out of `"merged"` (e.g. transitioning `merged` $\to$ `open`).
Readers MUST NOT reject such operations if encountered, ensuring fold
determinism and forward tolerance without non-lattice joins.

### 5. `assign`

Adds or removes assignees (requested reviewers) for the review.

```jsonc
{
  "object_id": "r-7f3a",
  "object_type": "review",
  "op_type": "assign",
  "op_version": 1,
  "body": {
    "add": ["email:alice@example.com", "user:bob"],
    "remove": ["email:charlie@example.com"]
  }
}
```

- `add` (array of person identifiers per [`spec/identifiers.md`](identifiers.md), optional): Person identifiers (assignees / requested reviewers) to add.
- `remove` (array of person identifiers per [`spec/identifiers.md`](identifiers.md), optional): Person identifiers (assignees / requested reviewers) to remove.

At least one of `add` or `remove` MUST be present and contain at least one item.
An empty `{}` body or empty arrays (`"add": []`) are invalid.

Assignees are normalized (the identifier trimmed and split at its first colon,
the scheme lowercased, the value trimmed and case-folded) per
[`spec/identifiers.md`](identifiers.md) before set membership and deduplication are
evaluated. Byte-exact equality after normalisation determines element identity,
and the scheme is part of what is compared.

### 6. `approval`

Records a review verdict (approval, change request, or dismissal) for a
specific revision head.

```jsonc
{
  "object_id": "r-7f3a",
  "object_type": "review",
  "op_type": "approval",
  "op_version": 1,
  "body": {
    "revision": "89abcdef0123456789abcdef0123456789abcdef",
    "verdict": "approve",
    "subject": "email:alice@example.com",
    "message": "Looks great, approved!"
  }
}
```

- `revision` (OID string, required): Head commit OID of the revision being
  evaluated.
- `verdict` (string, required): One of:
  - `"approve"`: Approves the revision.
  - `"request-changes"`: Requests changes before merging.
  - `"none"`: Retracts or dismisses any existing verdict for the `(subject, revision)` pair.
- `subject` (person identifier per [`spec/identifiers.md`](identifiers.md), optional):
  Person identifier (writer or user email identity) whose vote is recorded.
  Serves as an explicit "on behalf of" override (e.g., for imported GitHub review
  dismissals or bot submissions). When omitted or empty after normalization, the
  effective subject defaults to `email:` concatenated with the op commit author's
  email (`email:<author.email>`), normalized per [`spec/identifiers.md`](identifiers.md).
  Normalized (trimmed, lowercase) per [`spec/identifiers.md`](identifiers.md)
  prior to key evaluation.
- `message` (string, optional): Text summary or review message explaining the
  verdict.

#### Authorization & Dismissal Model

The `subject` field allows recording an approval or dismissal on behalf of
another writer, which is necessary when importing GitHub review dismissals or
team approvals. When omitted or empty after normalization, the effective subject
defaults to `email:` concatenated with the op commit author's email (`email:<author.email>`),
allowing `subject` to act strictly as an "on behalf of" override and ensuring that
distinct reviewers' subjectless approvals on the same revision do not overwrite each
other under `keyed-lww`.

**Writ has no authorization model at the specification layer.** Anyone with
git push access to their namespace can push an `approval` op. Operations are
cryptographically attributable (via commit signatures) and tamper-evident,
not authoritative. Policy questions — such as which authors have write
permissions, whose approval is required to merge, or whether a dismissal is
authorized — belong to the coordination service or client application
(VISION.md §The open core and the hosted layer). The spec provides faithful
event capture without imposing an enforcement mechanism it cannot verify
offline.

### 7. `ci-status`

Attaches a continuous integration or automated check result to a revision
head.

```jsonc
{
  "object_id": "r-7f3a",
  "object_type": "review",
  "op_type": "ci-status",
  "op_version": 1,
  "body": {
    "revision": "89abcdef0123456789abcdef0123456789abcdef",
    "name": "ci/test",
    "state": "success",
    "url": "https://ci.example.com/builds/9876",
    "description": "All unit tests passed",
    "started_at": "2026-08-30T18:00:00Z",
    "completed_at": "2026-08-30T18:04:30Z",
    "external_id": "9876"
  }
}
```

- `revision` (OID string, required): Head commit OID of the revision.
- `name` (string, required): Unique name or context identifier for the check
  (e.g. `"ci/test"`, `"lint"`), minLength 1.
- `state` (string, required): One of `"pending"`, `"success"`, `"failure"`,
  `"error"`, `"cancelled"`, `"neutral"`, `"skipped"`.
- `url` (string, optional): Web URL linking to full build logs or status
  details.
- `description` (string, optional): Short summary message from CI.
- `started_at` (RFC 3339 timestamp string, optional): When the check began.
- `completed_at` (RFC 3339 timestamp string, optional): When the check completed.
- `external_id` (string, optional): Identifier from the external CI provider
  (e.g. GitHub check run ID).

### 8. `label`

Adds or removes labels on the review. In v1, label operations reference collaborative `label` object identifiers (`spec/identifiers.md#reference`). Unknown or unfetched label references fold cleanly without rejection per `spec/forward-compatibility.md` rule `FC-16`.

```jsonc
{
  "object_id": "r-7f3a",
  "object_type": "review",
  "op_type": "label",
  "op_version": 1,
  "body": {
    "add": ["0123456789abcdef0123456789abcdef"],
    "remove": ["fedcba9876543210fedcba9876543210"]
  }
}
```

- `add` (array of label references, optional): Label object identifiers or references to attach to the review.
- `remove` (array of label references, optional): Label object identifiers or references to remove. `object-ref` is an opaque pointer with no resolution (`spec/value-types.md`), so a `remove` entry matches the stored value byte-exactly.

`add` and `remove` are the two sides of one `set-observed-remove` OR-set, so both carry the same value type (`object-ref`, per `spec/identifiers.md#reference`).

At least one of `add` or `remove` MUST be present and contain at least one item.
An empty `{}` body or empty arrays (`"add": []`) are invalid.

### 9. `link`

Associates or retracts cross-references between the review and other
collaborative objects (e.g. closing an issue, cross-repo references).

```jsonc
{
  "object_id": "r-7f3a",
  "object_type": "review",
  "op_type": "link",
  "op_version": 1,
  "body": {
    "target": "0123456789abcdef0123456789abcdef",
    "target_type": "issue",
    "relation": "fixes"
  }
}
```

- `target` (reference string, required): Target reference identifier. Can be
  a bare local object ID (e.g. `0123456789abcdef0123456789abcdef`) or a
  qualified cross-repository reference (`<repo-id>#<object-id>`), conforming to
  `spec/identifiers.md`.
- `target_type` (string, optional): Target object type (`"issue"`, `"review"`,
  `"project"`, `"cycle"`, etc.).
- `relation` (string, required): One of:
  - `"fixes"`: This review fixes or closes the target issue (the standard "Closes &lt;issue&gt;" reference).
  - `"relates"`: The review is related to the target object.
  - `"none"`: Retracts any existing link to `target`.

## Fold Implications & Merge Strategies

Every body property defined in this vocabulary is mapped to one merge strategy
from WRIT-12's closed catalogue. A machine-readable copy of these rules is
published in `spec/testdata/review-ops/field-rules.json`.

Folded review state is `Review{title, description, status, merge_commit, reason, assignees, labels, links, revisions, approvals, ci_statuses}`.

Per WRIT-12, **any field without a declared strategy is not merged**; it is
treated as unknown data, preserved in the DAG, and ignored during fold.

| `op_type` | Field | Merge Strategy | Key / Details |
| --- | --- | --- | --- |
| `create` | `title` | `lww` | Last writer wins in fold total order |
| `create` | `description` | `lww` | Last writer wins |
| `revision` | `base` | `append` | Appended to `revisions` list in total order |
| `revision` | `head` | `append` | Appended to `revisions` list in total order |
| `update` | `title` | `lww` | Last writer wins |
| `update` | `description` | `lww` | Last writer wins |
| `set-status` | `status` | `lww` | Last writer wins |
| `set-status` | `merge_commit` | `lww` | Last writer wins |
| `set-status` | `reason` | `lww` | Last writer wins |
| `assign` | `add` | `set-observed-remove` | Add-wins OR-set over normalized person identifiers (`spec/identifiers.md`); maps to state key `assignees` |
| `assign` | `remove` | `set-observed-remove` | Add-wins OR-set over normalized person identifiers (`spec/identifiers.md`); maps to state key `assignees` |
| `approval` | `revision` | `keyed-lww` | Scoped by key `[subject, revision]` over effective subject (omitted/empty subject defaults to `email:` + op commit author's email, normalized per `spec/identifiers.md`) |
| `approval` | `verdict` | `keyed-lww` | Scoped by key `[subject, revision]` over effective subject; `"none"` retracts verdict |
| `approval` | `subject` | `keyed-lww` | Scoped by key `[subject, revision]` over effective subject |
| `approval` | `message` | `keyed-lww` | Scoped by key `[subject, revision]` over effective subject |
| `ci-status` | `revision` | `keyed-lww` | Scoped by key `[revision, name]`; maps to state key `ci_revision` (WRIT-198: `approval.revision` and `ci-status.revision` share the body field name `revision` but are keyed on different tuples, so `ci-status.revision` targets its own state key) |
| `ci-status` | `name` | `keyed-lww` | Scoped by key `[revision, name]` |
| `ci-status` | `state` | `keyed-lww` | Scoped by key `[revision, name]` |
| `ci-status` | `url` | `keyed-lww` | Scoped by key `[revision, name]` |
| `ci-status` | `description` | `keyed-lww` | Scoped by key `[revision, name]`; maps to state key `ci_description` |
| `ci-status` | `started_at` | `keyed-lww` | Scoped by key `[revision, name]` |
| `ci-status` | `completed_at` | `keyed-lww` | Scoped by key `[revision, name]` |
| `ci-status` | `external_id` | `keyed-lww` | Scoped by key `[revision, name]` |
| `label` | `add` | `set-observed-remove` | Add-wins OR-set over label references (`object-ref`); maps to state key `labels` |
| `label` | `remove` | `set-observed-remove` | Add-wins OR-set over label references (`object-ref`); maps to state key `labels` |
| `link` | `target` | `keyed-lww` | Scoped by key `[target]` |
| `link` | `target_type` | `keyed-lww` | Scoped by key `[target]` |
| `link` | `relation` | `keyed-lww` | Scoped by key `[target]`; `"none"` retracts link |

### Deletion and Retraction Semantics

- **Review lifecycle:** A review is never deleted. Statuses such as `"closed"`
  are lifecycle states, not tombstones. There are no tombstones defined for
  the `review` object in v1.
- **Approval retraction:** An approval verdict is retracted by emitting an
  `approval` op with `verdict: "none"`. When folded, a verdict of `"none"`
  causes the subject's vote to present as absent in materialized state, while
  the operation itself remains intact in the DAG.
- **CI status updates:** A CI status is superseded by emitting a new
  `ci-status` op with the same `(revision, name)` key.
- **Link retraction:** A cross-reference link is retracted by emitting a `link`
  op with `relation: "none"` for the specified `target`.
- **Label removal:** A label is removed by emitting a `label` op with `"remove": [label]`.

## Forward Compatibility & Unknown Fields

- **Unknown body fields:** Conforming implementations MUST preserve and
  ignore any unknown fields in op bodies.
- **Unknown `op_type` or future `op_version`:** Ops with unknown op types or
  unsupported versions for `object_type: "review"` MUST remain in the DAG
  and contribute to total ordering (`t*`) and ancestry, but contribute no
  field mutations to known review state (WRIT-15).

---

## Appendix A — GitHub PR Representability (Informative)

To ensure full compatibility on the GitHub bridge read path, every
field in GitHub's pull request, review, and status/check-run payloads must be
representable in Writ.

The conversion vectors under [`testdata/review-ops/github/`](testdata/review-ops/github/)
demonstrate this mapping for opened PRs, force-pushes (`synchronize`), reviews,
dismissals, check runs, commit statuses, requested reviewers, labels, linked/closing issues, and merges.

### 1. Pull Request Payload Mapping

| GitHub PR Field | Disposition in Writ |
| --- | --- |
| `title` | `create.title` / `update.title` (`lww`) |
| `body` | `create.description` / `update.description` (`lww`) |
| `base.sha`, `head.sha` | `revision` op (`base`, `head`) (`append`) |
| `state` (`open`, `closed`), `draft` (`true`, `false`) | `set-status.status` (`"draft"`, `"open"`, `"closed"`) |
| `merged` (`true`), `merge_commit_sha` | `set-status.status: "merged"`, `set-status.merge_commit` |
| `user` | Op commit author on `create` / `update` |
| `created_at`, `updated_at`, `closed_at`, `merged_at` | Op commit author timestamps on respective ops |
| `number`, `id`, `node_id` | External metadata / workspace-global ID mapping (WRIT-16) |
| `comments`, `review_comments` | Separate `comment` objects (WRIT-9) referencing `object_id` |
| `assignees`, `requested_reviewers` (added / removed) | `assign.add` / `assign.remove` (`set-observed-remove`) |
| `labels` (added / removed) | `label.add` / `label.remove` (`set-observed-remove`) |
| Closing issue references (e.g. "Closes #123") / Linked issues | `link` op (`target: <repo-id>#<issue-id>`, `relation: "fixes"`, `target_type: "issue"`) |
| `milestone` | Workspace metadata / cycle or project link (WRIT-11) |
| `mergeable`, `rebaseable` | Derived state computed from git trees by client/projection |

### 2. Pull Request Review Payload Mapping

| GitHub Review Field | Disposition in Writ |
| --- | --- |
| `state: "APPROVED"` | `approval` op (`verdict: "approve"`) |
| `state: "CHANGES_REQUESTED"` | `approval` op (`verdict: "request-changes"`) |
| `state: "DISMISSED"` | `approval` op (`verdict: "none"`, `subject: <reviewer>`) |
| `state: "COMMENTED"` | `comment` ops (WRIT-9); message carried as review comment |
| `commit_id` | `approval.revision` (head commit OID) |
| `user.login` | `approval.subject` (and op commit author) |
| `body` | `approval.message` |
| `submitted_at` | Op commit timestamp on `approval` op |
| `id`, `node_id` | External bridge reference |

### 3. Check Run and Commit Status Mapping

| GitHub Status / Check Field | Disposition in Writ |
| --- | --- |
| `name` / `context` | `ci-status.name` |
| `status: "completed"`, `conclusion: "success"` | `ci-status.state: "success"` |
| `status: "completed"`, `conclusion: "failure"` | `ci-status.state: "failure"` |
| `status: "completed"`, `conclusion: "cancelled"` | `ci-status.state: "cancelled"` |
| `status: "completed"`, `conclusion: "neutral"` | `ci-status.state: "neutral"` |
| `status: "completed"`, `conclusion: "skipped"` | `ci-status.state: "skipped"` |
| `status: "in_progress"`, `status: "queued"`, status `pending` | `ci-status.state: "pending"` |
| `target_url` / `html_url` | `ci-status.url` |
| `description` / `output.summary` | `ci-status.description` |
| `started_at` | `ci-status.started_at` |
| `completed_at` | `ci-status.completed_at` |
| `id` / `external_id` | `ci-status.external_id` |
| `head_sha` / `sha` | `ci-status.revision` |

