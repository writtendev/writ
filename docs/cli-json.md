# CLI JSON Plumbing Interface (`--json`)

The `--json` flag turns `writ` into a machine-readable plumbing tool for scripts, automation, and AI agents. Every read verb supports `--json` and emits a versioned, schema-stable JSON envelope on standard output.

---

## 1. The Common Envelope

All plumbing commands emit a single top-level JSON document on `stdout` adhering to the standard envelope:

```json
{
  "schema_version": 1,
  "kind": "<verb.action>",
  "data": ...
}
```

### Top-Level Fields

| Field | Type | Description |
|---|---|---|
| `schema_version` | integer | Envelope schema version (currently `1`). Bumps only on breaking changes. |
| `kind` | string | Discriminator for the payload schema (e.g. `review.list`, `review.status`, `issue.list`, `issue.status`, `issue.label`, `label.list`, `sync.status`, `sync.result`, `comment.edit`, `comment.delete`, `schema.plan`, `schema.apply`). |
| `data` | object or array | Verb-specific payload structure. |

---

## 2. Stability & Versioning Guarantees

1. **Additive-Only Evolution:** Within `schema_version: 1`, fields may be added to payloads, but existing fields will never be removed, renamed, or retyped.
2. **Forward Compatibility:** Consumers must ignore unknown fields without failing.
3. **Decoupled Wire Model:** The CLI JSON wire schema is maintained in `cmd/writ/internal/wire` and is decoupled from internal engine and spec-fixture structures. Engine tag modifications do not alter plumbing outputs.
4. **Clean Channel Separation:** Valid JSON is emitted strictly on `stdout`. Diagnostic messages, warnings, and error explanations are printed to `stderr` as plain text.
5. **Machine-Readable Exit Codes:** Classification uses process exit codes:
   - `0`: Success.
   - `1`: Unclassified runtime failure or transport error.
   - `2`: Usage error (invalid flag, missing required argument).
   - `3`: Unknown or unconfigured git remote.
   - `4`: Rejected non-fast-forward update.
   - `5`: Not a git repository or store cannot be opened.
6. **No Null Collections:** Empty collections serialize as `[]`, never `null`.
7. **Deterministic Formatting & Ordering:** Timestamps are formatted as ISO 8601 / RFC 3339 UTC with a trailing `Z` (e.g. `2026-01-01T00:00:00Z`). All list responses have a deterministic total order, using object ID ascending as a tiebreaker.

---

## 3. Supported Verbs & Schema Reference

### `writ review list --json`

Lists code reviews matching optional filters.

- **Envelope `kind`**: `"review.list"`
- **`data` Type**: Array of `ReviewSummary` objects (`[]ReviewSummary`)

#### `ReviewSummary` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | 32-character lowercase hex identifier for the review. |
| `title` | string | Title of the code review. |
| `status` | string | Lifecycle state (`draft`, `open`, `closed`, `merged`). |
| `author` | object | Creator identity: `{ "name": string, "email": string }`. |
| `created_at` | string | Creation timestamp in RFC 3339 UTC (`...Z`). |
| `updated_at` | string | Last modification timestamp in RFC 3339 UTC (`...Z`). |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "review.list",
  "data": [
    {
      "object_id": "0123456789abcdef0123456789abcdef",
      "title": "Add OAuth2 authentication provider",
      "status": "open",
      "author": {
        "name": "Alice",
        "email": "alice@example.com"
      },
      "created_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:05:00Z"
    }
  ]
}
```

---

### `writ review status <id> --json`

Fetches detailed status and folded state for a single code review.

- **Envelope `kind`**: `"review.status"`
- **`data` Type**: `Review` object

#### `Review` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | 32-character lowercase hex identifier for the review. |
| `title` | string | Title of the code review. |
| `description` | string (optional) | Extended review description or rationale. |
| `status` | string | Lifecycle state (`draft`, `open`, `closed`, `merged`). |
| `merge_commit` | string (optional) | Merge commit SHA if the review is merged. |
| `reason` | string (optional) | Reason text supplied with status change. |
| `author` | object | Creator identity: `{ "name": string, "email": string }`. |
| `created_at` | string | Creation timestamp in RFC 3339 UTC. |
| `updated_at` | string | Last modification timestamp in RFC 3339 UTC. |
| `assignees` | array of strings | Assigned reviewers as scheme-prefixed person identifiers (`email:alice@example.com`, `user:alice`): `[ string ]`. |
| `labels` | array of strings | Labels attached to the review, converged from concurrent add/remove operations. |
| `links` | array of objects | Cross-reference links: `[ { "target": string, "target_type": string, "relation": string } ]`. `target` is either a bare object ID (same repo) or `<repo-id>#<object-id>` (cross-repo). |
| `revisions` | array of objects | Pushed revisions: `[ { "base": string, "head": string } ]`. |
| `approvals` | array of objects | Recorded review verdicts: `[ { "subject": string, "revision": string, "verdict": string, "message": string } ]`. |
| `ci_statuses` | array of objects | Automated CI checks: `[ { "revision": string, "name": string, "state": string, "url": string, "description": string, "started_at": string, "completed_at": string, "external_id": string } ]`. |
| `unknown_ops` | array of objects | Preserved forward-compatibility operations: `[ { "commit": string, "object_type": string, "op_type": string, "op_version": integer } ]`. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "review.status",
  "data": {
    "object_id": "0123456789abcdef0123456789abcdef",
    "title": "Add OAuth2 authentication provider",
    "description": "Implements OAuth2 login flows",
    "status": "open",
    "author": {
      "name": "Alice",
      "email": "alice@example.com"
    },
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:05:00Z",
    "assignees": [
      "user:bob"
    ],
    "labels": [
      "area/engine"
    ],
    "links": [
      {
        "target": "fedcba9876543210fedcba9876543210",
        "target_type": "issue",
        "relation": "fixes"
      }
    ],
    "revisions": [
      {
        "base": "0123456789abcdef0123456789abcdef01234567",
        "head": "1111111111111111111111111111111111111111"
      }
    ],
    "approvals": [
      {
        "subject": "user:bob",
        "revision": "1111111111111111111111111111111111111111",
        "verdict": "approve",
        "message": "Looks great!"
      }
    ],
    "ci_statuses": [
      {
        "revision": "1111111111111111111111111111111111111111",
        "name": "ci/test",
        "state": "success",
        "url": "https://ci.example.com/build/123",
        "description": "All unit tests passed"
      }
    ],
    "unknown_ops": []
  }
}
```

---

### `writ issue list --json`

Lists issues matching optional filters.

- **Envelope `kind`**: `"issue.list"`
- **`data` Type**: Array of `IssueSummary` objects (`[]IssueSummary`)

#### `IssueSummary` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | 32-character lowercase hex identifier for the issue. |
| `title` | string | Title of the issue. |
| `state` | string | Workflow-state object ID, or `""` if the issue has no `set-state` op yet. |
| `author` | object | Creator identity: `{ "name": string, "email": string }`. |
| `created_at` | string | Creation timestamp in RFC 3339 UTC (`...Z`). |
| `updated_at` | string | Last modification timestamp in RFC 3339 UTC (`...Z`). |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "issue.list",
  "data": [
    {
      "object_id": "0123456789abcdef0123456789abcdef",
      "title": "Login form rejects valid emails",
      "state": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "author": {
        "name": "Alice",
        "email": "alice@example.com"
      },
      "created_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:05:00Z"
    }
  ]
}
```

---

### `writ issue status <id> --json`

Fetches detailed status and folded state for a single issue.

- **Envelope `kind`**: `"issue.status"`
- **`data` Type**: `Issue` object

#### `Issue` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | 32-character lowercase hex identifier for the issue. |
| `title` | string | Title of the issue. |
| `description` | string (optional) | Extended issue description. |
| `state` | string | Workflow-state object ID, or `""` if the issue has no `set-state` op yet. |
| `reason` | string (optional) | Reason text supplied with the last state change. |
| `author` | object | Creator identity: `{ "name": string, "email": string }`. |
| `created_at` | string | Creation timestamp in RFC 3339 UTC. |
| `updated_at` | string | Last modification timestamp in RFC 3339 UTC. |
| `assignees` | array of strings | Assignees as scheme-prefixed person identifiers (`email:alice@example.com`, `user:alice`), converged from concurrent add/remove operations. |
| `labels` | array of strings | Labels attached to the issue, converged from concurrent add/remove operations. |
| `links` | array of objects | Cross-reference links: `[ { "target": string, "target_type": string, "relation": string } ]`. `target` is either a bare object ID (same repo) or `<repo-id>#<object-id>` (cross-repo). |
| `comments` | array of objects | Threaded comments attached to the issue: `[ CommentThread ]`. |
| `unknown_ops` | array of objects | Preserved forward-compatibility operations: `[ { "commit": string, "object_type": string, "op_type": string, "op_version": integer } ]`. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "issue.status",
  "data": {
    "object_id": "0123456789abcdef0123456789abcdef",
    "title": "Login form rejects valid emails",
    "description": "RFC 5321 plus-addressing is rejected by the client-side regex",
    "state": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "author": {
      "name": "Alice",
      "email": "alice@example.com"
    },
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:05:00Z",
    "assignees": ["user:bob"],
    "labels": ["bug"],
    "links": [
      {
        "target": "1111111111111111111111111111111111111111",
        "target_type": "review",
        "relation": "fixes"
      }
    ],
    "comments": [],
    "unknown_ops": []
  }
}
```

---

### `writ issue label <id> [--json]`

Views or updates labels on a single issue.

- **Envelope `kind`**: `"issue.label"`
- **`data` Type**: `IssueLabels` object

#### `IssueLabels` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | 32-character lowercase hex identifier for the issue. |
| `labels` | array of strings | Labels attached to the issue, converged from concurrent add/remove operations. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "issue.label",
  "data": {
    "object_id": "0123456789abcdef0123456789abcdef",
    "labels": ["bug", "documentation"]
  }
}
```

---

### `writ sync --status --json [remote...]`

Reports the count of unpushed local operations without performing network transport.

- **Envelope `kind`**: `"sync.status"`
- **`data` Type**: Array of `SyncStatus` objects (`[]SyncStatus`)

#### `SyncStatus` Fields

| Field | Type | Description |
|---|---|---|
| `remote` | string | Name of the git remote (e.g. `origin`). |
| `unsynced` | integer | Number of local operations not yet pushed to the remote. |
| `failure` | object (optional) | Structured failure object (`kind`, `message`, `advice`, `retryable`) when sync status query failed. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "sync.status",
  "data": [
    {
      "remote": "origin",
      "unsynced": 2
    }
  ]
}
```

---

### `writ sync --json [remote...]`

Synchronizes operations with remote git repositories (fetches, pushes, and refreshes the projection cache).

- **Envelope `kind`**: `"sync.result"`
- **`data` Type**: Array of `SyncResult` objects (`[]SyncResult`)

#### `SyncResult` Fields

| Field | Type | Description |
|---|---|---|
| `remote` | string | Name of the git remote. |
| `ops_fetched` | integer | Number of new operations fetched from the remote. |
| `ops_pushed` | integer | Number of local operations pushed to the remote. |
| `objects_touched` | integer | Number of collaborative objects updated in the projection cache. |
| `unsynced` | integer | Remaining unsynced operations count for the remote. |
| `failure` | object (optional) | Structured failure object (`kind`, `message`, `advice`, `retryable`) when transport failed. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "sync.result",
  "data": [
    {
      "remote": "origin",
      "ops_fetched": 1,
      "ops_pushed": 2,
      "objects_touched": 1,
      "unsynced": 0
    }
  ]
}
```

---

### `writ comment edit <comment-id> -m <new-text> --json`

Updates the text content of an existing comment and returns the refreshed comment state.

- **Envelope `kind`**: `"comment.edit"`
- **`data` Type**: `Comment` object

#### `Comment` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | 32-character lowercase hex identifier for the comment. |
| `subject` | object | Target subject: `{ "object_type": string, "object_id": string }`. |
| `author` | object | Author identity: `{ "name": string, "email": string }`. |
| `created_at` | string | Creation timestamp in RFC 3339 UTC (`...Z`). |
| `updated_at` | string | Last modification timestamp in RFC 3339 UTC (`...Z`). |
| `text` | string | Text content of the comment. |
| `in_reply_to` | string (optional) | Object ID of the parent comment if this is a threaded reply. |
| `anchor` | object (optional) | Anchor location describing where the comment is positioned. |
| `deleted` | boolean | `true` if the comment has been tombstoned/deleted; `false` otherwise. |
| `resolved` | boolean | `true` if the thread is marked as resolved; `false` otherwise. |
| `resolved_by` | string (optional) | Person identifier who resolved the thread. |
| `positions` | array (optional) | Resolved source positions. |
| `unknown_ops` | array | Preserved unrecognized operations on this comment. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "comment.edit",
  "data": {
    "object_id": "c-root",
    "subject": {
      "object_type": "review",
      "object_id": "r-7f3a"
    },
    "author": {
      "name": "Alice Example",
      "email": "alice@example.test"
    },
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:05:00Z",
    "text": "Updated comment text",
    "deleted": false,
    "resolved": false,
    "unknown_ops": []
  }
}
```

---

### `writ comment delete <comment-id> --json`

Marks an existing comment as deleted (tombstone) and returns the refreshed comment state with `deleted: true`.

- **Envelope `kind`**: `"comment.delete"`
- **`data` Type**: `Comment` object

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "comment.delete",
  "data": {
    "object_id": "c-root",
    "subject": {
      "object_type": "review",
      "object_id": "r-7f3a"
    },
    "author": {
      "name": "Alice Example",
      "email": "alice@example.test"
    },
    "created_at": "2026-01-01T00:00:00Z",
    "updated_at": "2026-01-01T00:05:00Z",
    "text": "Original comment text",
    "deleted": true,
    "resolved": false,
    "unknown_ops": []
  }
}
```

---

### `writ label list --json`

Lists labels across the workspace.

- **Envelope `kind`**: `"label.list"`
- **`data` Type**: Array of `LabelSummary` objects (`[]LabelSummary`)

#### `LabelSummary` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | 32-character lowercase hex identifier for the label. |
| `name` | string | Display name of the label. |
| `color` | string | Hex color client hint (e.g. `"#d73a4a"`). |
| `description` | string | Optional description of the label. |
| `created_at` | string | Creation timestamp in RFC 3339 UTC (`...Z`). |
| `updated_at` | string | Last modification timestamp in RFC 3339 UTC (`...Z`). |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "label.list",
  "data": [
    {
      "object_id": "0123456789abcdef0123456789abcdef",
      "name": "bug",
      "color": "#d73a4a",
      "description": "Something isn't working",
      "created_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:00:00Z"
    }
  ]
}
```

---

### `writ schema plan --json`

Parses `writ.schema`, folds the schema objects already in the repository, and reports the ops applying the file would append. Appends no ops.

- **Envelope `kind`**: `"schema.plan"`
- **`data` Type**: `SchemaPlan` object

#### `SchemaPlan` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | 32-character lowercase hex identifier for the target schema object, present only when `created` is `false`. A creation plan mints no id of its own — `apply` resolves its own target independently and mints its own id — so there is no id to report yet; the field is omitted rather than naming one `apply` may never actually create. |
| `namespace` | string | The file's `namespace` declaration. |
| `created` | boolean | `true` iff the repository has no schema object with this namespace yet, so applying would mint a fresh object id. |
| `up_to_date` | boolean | `true` iff `ops` is empty: the file already matches the folded log state. |
| `ops` | array | The ops `apply` would append, in the order it would append them. Empty array (`[]`), never `null`, when `up_to_date`. |
| `ops[].op_type` | string | One of `create`, `define-type`, `define-op`, `define-field`, `deprecate-type`, `deprecate-field` (`spec/schema-ops.md` §4). |
| `ops[].body` | object | The op's normative wire body, verbatim — the same shape a conforming implementation of `spec/schema-ops.md` would sign and append. |
| `current_source` | string | `writ.schema` source text rendered from the log's own folded state for the target object. Empty string when `created` is `true` — there is nothing in the log yet to render. |
| `planned_source` | string | `writ.schema` source text rendered from the state applying the file would produce. |
| `conflicts` | array | Repository-wide `SchemaConflict` entries `RulesFromSchemas` already finds, whether or not this file caused them — this is the only surface that shows them. Empty array (`[]`) when there are none. |
| `conflicts[].object_type` | string | Set for an `object_type` collision between two schema objects; omitted for a namespace-only collision. |
| `conflicts[].namespace` | string | Set for a namespace collision, and echoed on an `object_type` collision when known. |
| `conflicts[].object_ids` | array | The schema object ids involved. |
| `conflicts[].reason` | string | Human-readable explanation. |

A refused plan (an invalid file, or an edit that would remove a declaration) exits `1` and writes plain-text diagnostics to `stderr`; no `SchemaPlan` JSON is emitted.

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "schema.plan",
  "data": {
    "namespace": "acme",
    "created": true,
    "up_to_date": false,
    "ops": [
      {
        "op_type": "create",
        "body": { "namespace": "acme", "description": "Acme's vocabulary" }
      },
      {
        "op_type": "define-type",
        "body": { "type": "standup", "description": "A daily standup update" }
      }
    ],
    "current_source": "",
    "planned_source": "namespace acme\ndescription \"Acme's vocabulary\"\n\ntype standup {\n  description \"A daily standup update\"\n}\n",
    "conflicts": []
  }
}
```

A reuse plan (`created: false`) reports the real, already-folded `object_id`:

```json
{
  "schema_version": 1,
  "kind": "schema.plan",
  "data": {
    "object_id": "0123456789abcdef0123456789abcdef",
    "namespace": "acme",
    "created": false,
    "up_to_date": true,
    "ops": [],
    "current_source": "namespace acme\ndescription \"Acme's vocabulary\"\n\ntype standup {\n  description \"A daily standup update\"\n}\n",
    "planned_source": "namespace acme\ndescription \"Acme's vocabulary\"\n\ntype standup {\n  description \"A daily standup update\"\n}\n",
    "conflicts": []
  }
}
```

---

### `writ schema apply --json`

Runs the same computation as `writ schema plan`, then signs and appends the resulting ops.

- **Envelope `kind`**: `"schema.apply"`
- **`data` Type**: `SchemaApply` object

#### `SchemaApply` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | The schema object written to. |
| `namespace` | string | The file's `namespace` declaration. |
| `created` | boolean | `true` iff this apply minted a fresh object id. |
| `ops_appended` | integer | Count of ops actually appended. `0` when the file already matched the log. |
| `ops` | array | The ops appended, same shape as `SchemaPlan.ops`. Empty array (`[]`) when nothing was appended. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "schema.apply",
  "data": {
    "object_id": "0123456789abcdef0123456789abcdef",
    "namespace": "acme",
    "created": true,
    "ops_appended": 2,
    "ops": [
      {
        "op_type": "create",
        "body": { "namespace": "acme", "description": "Acme's vocabulary" }
      },
      {
        "op_type": "define-type",
        "body": { "type": "standup", "description": "A daily standup update" }
      }
    ]
  }
}
```

---

## 4. Worked `jq` Examples

### List open reviews
```bash
writ review list --json | jq -r '.data[] | select(.status == "open") | "\(.object_id) \(.title)"'
```

### Check if a review has approvals
```bash
writ review status <id> --json | jq -e '.data.approvals[] | select(.verdict == "approve")' > /dev/null
```

### Extract the latest revision head commit
```bash
writ review status <id> --json | jq -r '.data.revisions[-1].head'
```

### List open issues
```bash
writ issue list -state open --json | jq -r '.data[] | "\(.object_id) \(.title)"'
```

### Check if an issue is assigned to a user
```bash
writ issue status <id> --json | jq -e --arg who "user:bob" '.data.assignees | index($who)' > /dev/null
```

### Check total unsynced operations before network sync
```bash
writ sync --status --json | jq '[.data[].unsynced] | add'
```

### Verify zero errors across all CI checks
```bash
writ review status <id> --json | jq 'all(.data.ci_statuses[]; .state == "success")'
```

### List labels on an issue
```bash
writ issue label <id> --json | jq -r '.data.labels[]'
```

### Edit a comment and extract its updated text
```bash
writ comment edit <id> -m "new text" --json | jq -r '.data.text'
```

### Check if a comment is deleted
```bash
writ comment delete <id> --json | jq '.data.deleted'
```
