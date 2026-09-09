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
| `kind` | string | Discriminator for the payload schema (`sync.status`, `sync.result`, `schema.plan`, `schema.apply`, `schema.show`, `object.create`, `object.apply`, `object.show`, `object.list`). |
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
8. **Pre-v0.1.0 exception to rule 1:** WRIT-195 removed the `review.*`, `issue.*`, `comment.*`, `label.*`, `state.*`, `settings`, and `doc.*` kinds (and their payload shapes) from this same `schema_version: 1` envelope, replacing them with the generic `object.*`/`schema.*` verbs below. Nothing has shipped yet (`AGENTS.md`: "Nothing has shipped: no tags, no users, no external implementations"), so this is a deliberate pre-v0.1.0 break, not a violation of rule 1 going forward — additive-only evolution is the promise from here on, not a retroactive one.

---

## 3. Supported Verbs & Schema Reference

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

### `writ schema plan --json`

Parses `writ.schema`, folds the schema objects already in the repository, and reports the ops applying the file would append. Appends no ops.

- **Envelope `kind`**: `"schema.plan"`
- **`data` Type**: `SchemaPlan` object

#### `SchemaPlan` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | The target schema object's id. On a creation plan (`created: true`), this is `schema:<namespace>`, derived from the file's `namespace` declaration (`spec/identifiers.md`'s schema carve-out) — the exact id `apply` would write to, not a preview. On a reuse plan (`created: false`), this is whatever id the existing schema object already holds, derived or not. Always present. |
| `namespace` | string | The file's `namespace` declaration. |
| `created` | boolean | `true` iff the repository has no schema object with this namespace yet, so applying would create a fresh object at `object_id`. |
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
    "object_id": "schema:acme",
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

A reuse plan (`created: false`) reports the same, real, already-folded `object_id`:

```json
{
  "schema_version": 1,
  "kind": "schema.plan",
  "data": {
    "object_id": "schema:acme",
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
| `object_id` | string | The schema object written to. On creation (`created: true`), `schema:<namespace>`. On reuse (`created: false`), whatever id the existing schema object already held. |
| `namespace` | string | The file's `namespace` declaration. |
| `created` | boolean | `true` iff this apply created a fresh schema object at `object_id`. |
| `ops_appended` | integer | Count of ops actually appended. `0` when the file already matched the log. |
| `ops` | array | The ops appended, same shape as `SchemaPlan.ops`. Empty array (`[]`) when nothing was appended. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "schema.apply",
  "data": {
    "object_id": "schema:acme",
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

### `writ schema show [<type>] --json`

Reports the vocabulary `Store.Types` resolves right now — built-in types overlaid by whatever the log declares. This is a different question from `writ schema plan`/`apply` (`Store.Schema`, the working-tree `writ.schema` file's own view): `schema show` answers "what is installed and folding today," not "what would this file change."

- **Envelope `kind`**: `"schema.show"`
- **`data` Type**: with `<type>`, one `SchemaType` object; with no `<type>`, an array of `SchemaType` objects (`[]SchemaType`) — every installed type, sorted by name.

`<type>` also accepts `schema` itself, even though it never appears in the no-argument array and would not be "declared by the installed vocabulary" in the sense every other name here is: `schema` is writ's one hard-coded object type (`spec/schema-ops.md`), not resolved by `Store.Types`. `schema show schema --json` reports `{"type": "schema"}` with no `fields`/`ops` keys, since neither is resolved for it by this API.

#### `SchemaType` Fields

| Field | Type | Description |
|---|---|---|
| `type` | string | The bare wire `object_type`. |
| `description` | string | Optional type description. Omitted when empty. |
| `deprecated` | boolean | `true` if the type is deprecated. Omitted when `false`. |
| `fields` | array | `SchemaField` entries this type declares. Omitted when empty. |
| `ops` | array | `SchemaOp` entries this type declares. Omitted when empty. |
| `fields[].field` | string | Declared field name (the key an op body carries on the wire — not always the key `object show`'s `fields` reports it back under; see `object.show` below). |
| `fields[].op_type` | string | The op type that writes this field. |
| `fields[].op_version` | integer | The op version that writes this field. |
| `fields[].value_type` | string | Declared value type (`string`, `int`, `number`, `bool`, `anchor`, ...). Omitted when the field declares none. |
| `fields[].enum` | array | Declared enum member strings, when `value_type` is `enum`. Omitted otherwise. `writ object create`/`apply` do not check enum membership at the CLI; this is how a client pre-validates it. |
| `fields[].max_length` | integer | Declared maximum length for a `string`/`text` field. Omitted when unset. `writ object create`/`apply` do not check this at the CLI either — same reason as `enum`. |
| `fields[].strategy` | string | Merge strategy (`lww`, `set-union`, ...). Omitted when unset. |
| `fields[].key` | array | Declared key column name(s), when `strategy` is `keyed-lww`. Omitted otherwise. |
| `fields[].key_types` | object | Map of key column name to its value type, when `strategy` is `keyed-lww`. Omitted otherwise. |
| `fields[].lattice` | array | Declared semilattice element strings, when `strategy` is `lattice`. Omitted otherwise. |
| `fields[].target` | string | Target key this field folds into, when it differs from `field`. Omitted otherwise. |
| `fields[].deprecated` | boolean | `true` if this field declaration is deprecated. Omitted when `false`. |
| `ops[].op_type` | string | Op type name. |
| `ops[].op_version` | integer | Op version. |
| `ops[].description` | string | Optional op description. Omitted when empty. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "schema.show",
  "data": {
    "type": "ticket",
    "fields": [
      { "field": "title", "op_type": "create", "op_version": 1, "value_type": "string", "strategy": "lww" }
    ],
    "ops": [
      { "op_type": "create", "op_version": 1 }
    ]
  }
}
```

With no `<type>`, `data` is a bare array of the same shape, one entry per installed type.

---

### `writ object create <type> <op-type> --json`

Appends the op that starts a new object of `<type>`, using `<op-type>`'s field rules from the installed vocabulary to parse each `-field` value.

`-field <k>=<v>` converts its value by the field's declared `value_type` (`writ schema show <type>` reports it). That conversion has one gap: the closed value-type catalogue (`spec/value-types.md`) has no generic "object" entry — `anchor` is the only object-shaped catalogue type — and a field with no declared `value_type` at all (the catalogue's documented `untyped` case — an `{object_type, object_id}` record naming another object, say) has no type to convert by in the first place. `-field-json <k>=<v>` is the escape hatch: it decodes its value as JSON directly, with no type-directed conversion, and works for any field regardless of what `value_type` it declares or whether it declares one. Like `-field`, it does no validation of its own — `enum` membership, `max_length`, and every other constraint are left to the producer validator that already runs inside `Objects.Create`/`Apply`, so a `-field-json` write that fails validation is refused there, exactly like an invalid `-field` write. Repeating `-field-json` for the same key builds a JSON array of the decoded elements, same as repeating `-field`; giving the same key to both `-field` and `-field-json` is refused.

- **Envelope `kind`**: `"object.create"`
- **`data` Type**: `ObjectCreated` object

#### `ObjectCreated` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | 32-character lowercase hex identifier minted for the new object. |
| `object_type` | string | The object type created. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "object.create",
  "data": {
    "object_id": "0123456789abcdef0123456789abcdef",
    "object_type": "ticket"
  }
}
```

---

### `writ object apply <object-id> <op-type> --json`

Appends a further op against an existing object, causally following its current frontier. The object's type comes from the object itself, not from a flag.

- **Envelope `kind`**: `"object.apply"`
- **`data` Type**: `ObjectApplied` object

#### `ObjectApplied` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | The object applied to. |
| `op_type` | string | The op type appended. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "object.apply",
  "data": {
    "object_id": "0123456789abcdef0123456789abcdef",
    "op_type": "update"
  }
}
```

---

### `writ object show <object-id> --json`

Folds an object's state directly from the log — never the projection cache — and reports it.

- **Envelope `kind`**: `"object.show"`
- **`data` Type**: `Object` object

#### `Object` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | The object's id. |
| `object_type` | string | The object's type, determined from its own ops. |
| `fields` | object | **An open map keyed by TARGET key** — a rule's declared `target` when it has one, otherwise its field name — not a fixed field set. This is the one place this document's §2 "additive-only, never retyped" promise carries a schema-shaped carve-out: the keys present, and the type of each value, are whatever the installed vocabulary for `object_type` declares, which a consumer-declared schema can change without this CLI's own version bumping. Values are the merge-strategy's folded representation: a scalar, a JSON array, or a JSON object. |
| `unknown_ops` | array | Ops this object carries that no installed rule interprets — forward compatibility, not an error. Empty array (`[]`) when there are none. |
| `unknown_ops[].commit` | string | The op commit's SHA — the only way to name an uninterpretable op for a caller who needs to report or investigate it. |
| `unknown_ops[].object_type` | string | The op's own `object_type`. |
| `unknown_ops[].op_type` | string | The op's own `op_type`. |
| `unknown_ops[].op_version` | integer | The op's own `op_version`. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "object.show",
  "data": {
    "object_id": "0123456789abcdef0123456789abcdef",
    "object_type": "ticket",
    "fields": {
      "title": "Fix the thing",
      "tags": ["urgent", "backend"]
    },
    "unknown_ops": []
  }
}
```

---

### `writ object list [<type>] --json`

Lists collaborative objects across every schema-declared type, or within one, from the projection cache.

- **Envelope `kind`**: `"object.list"`
- **`data` Type**: Array of `ObjectSummary` objects (`[]ObjectSummary`)

`<type>` also accepts `schema`, as the one exception to "an undeclared `<type>` is refused by name": `Store.Types` never resolves `schema` (writ's one hard-coded object type, not schema-declared), but `writ schema apply` leaves real `schema` rows in the projection, and this filter finds them rather than refusing the one type that demonstrably has objects.

#### `ObjectSummary` Fields

| Field | Type | Description |
|---|---|---|
| `object_id` | string | 32-character lowercase hex identifier. |
| `object_type` | string | The object's type. |
| `author` | object | `{ "name": string, "email": string }` — the object's creating author. |
| `created_at` | string | Creation timestamp in RFC 3339 UTC (`...Z`). |
| `updated_at` | string | Last modification timestamp in RFC 3339 UTC (`...Z`). |
| `op_count` | integer | Number of ops folded into this object. |

#### Example Output

```json
{
  "schema_version": 1,
  "kind": "object.list",
  "data": [
    {
      "object_id": "0123456789abcdef0123456789abcdef",
      "object_type": "ticket",
      "author": { "name": "Alice", "email": "alice@example.com" },
      "created_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:00:00Z",
      "op_count": 1
    }
  ]
}
```

---

## 4. Worked `jq` Examples

### List every object of one type
```bash
writ object list ticket --json | jq -r '.data[] | "\(.object_id) \(.object_type)"'
```
`object.list` reports only summary columns (id, type, author, timestamps,
op count) — a field like `title` lives on the folded object itself, so
listing titles takes one `object.show` per row:
```bash
writ object list ticket --json \
  | jq -r '.data[].object_id' \
  | while read -r id; do
      writ object show "$id" --json | jq -r '"\(.data.object_id) \(.data.fields.title)"'
    done
```

### Extract one field from an object's folded state
```bash
writ object show <id> --json | jq -r '.data.fields.title'
```

### Check whether an object carries any unrecognized (forward-compatibility) ops
```bash
writ object show <id> --json | jq -e '.data.unknown_ops | length > 0' > /dev/null
```

### Check total unsynced operations before network sync
```bash
writ sync --status --json | jq '[.data[].unsynced] | add'
```

### List the fields and ops a type declares
```bash
writ schema show ticket --json | jq -r '.data.fields[] | "\(.field) (\(.op_type) v\(.op_version))"'
```

### List every installed type name
```bash
writ schema show --json | jq -r '.data[].type'
```

### Check whether `writ.schema` matches what is already in the log
```bash
writ schema plan --json | jq '.data.up_to_date'
```
