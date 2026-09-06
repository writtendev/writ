# Document and Section Operations — long-form markdown documents, fractional section ordering, and multi-value registers (v1)

Status: **normative**. Schema: [`schemas/document-ops.schema.json`](schemas/document-ops.schema.json).
Field rules: [`testdata/document/field-rules.json`](testdata/document/field-rules.json), [`testdata/section/field-rules.json`](testdata/section/field-rules.json).
Ordering: [`spec/ordering.md`](ordering.md).
Fold: [`spec/fold.md`](fold.md).

This document defines the operation vocabulary, payload schemas, and fold semantics
for long-form markdown documents (`object_type: "document"`) and document sections
(`object_type: "section"`) in Writ.

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

---

## 1. Scope & Object Model

Writ represents long-form collaborative documentation — implementation plans,
design docs, RFCs, retrospectives, and postmortems — directly in the git repository.

To cleanly isolate concurrent edits and comment anchoring, documents are structured
into two collaborative object types:

1. **Document (`object_type: "document"`)**: The container object holding document
   metadata:
   - `title`: User-defined display title (Last-Writer-Wins, `lww`).
   - `links`: Cross-references to issues, reviews, or other collaborative objects
     (Keyed Last-Writer-Wins, `keyed-lww` on `target`).
   - `labels`: Categorical labels (Add-Wins Observed-Remove Set, `set-observed-remove`).
2. **Section (`object_type: "section"`)**: First-class collaborative object representing
   a discrete section of a document:
   - `document_id`: Immutable identifier of the parent document (`create-once`).
   - `position`: Fractional index position key in base-62 ([`spec/ordering.md`](ordering.md), `lww`).
   - `title`: Optional section heading (`lww`).
   - `body`: Markdown body content folded as a **multi-value register** (`multi-value`).
   - `deleted`: Soft deletion status (tombstone, `tombstone`).

Documents and sections are collaborative objects homed in the repository the
client is operating on (ARCHITECTURE.md §Object homing).

### 1.1. Why Sections Are First-Class Collaborative Objects

Embedding an ordered array of sections inside a single document object would force
any edit or section reordering into a sequence operation on the document root, causing
false conflicts when two users edit different sections concurrently.

By reifying sections as their own collaborative objects:
- **Localized conflicts:** Concurrent edits to Section A and Section B never conflict.
- **Section-level anchoring:** Comments attach directly to `section` objects
  (`subject: {object_type: "section", object_id: "<id>"}`) using the existing
  subject-agnostic comment machinery (`spec/comments.md`).
- **Independent reordering:** Moving a section updates only that section's `position`
  scalar field via fractional indexing without generating operations on neighboring
  sections or the document root.
- **Constant ref count:** Append chains maintain one ref per `(writer, object_type)`:
  `refs/writ/<writer-id>/document` and `refs/writ/<writer-id>/section`. O(sections) creates
  zero additional git refs.

### 1.2. Section Ordering via Fractional Indexing

Sections within a document are presented ordered by:
```
position ASC, op_id ASC
```
per [`spec/ordering.md`](ordering.md) §9.

The `position` field uses base-62 fractional indexing (`engine/order`). Inserting
a section between two existing sections computes a key between their positions without
renumbering any siblings. When two concurrent insertions produce identical positions,
the position-establishing operation ID (`op_id`) breaks ties deterministically.

### 1.3. Document Kinds via Link Relations & Labels

Document kinds MUST NOT be encoded as a closed enum.

Semantic roles are expressed by the **relation** on cross-reference links (`spec/identifiers.md`),
answering *what this document is to that target object*:
- `relation: "implementation-plan"`: Document is an implementation plan for an issue.
- `relation: "design-doc"`: Document is a design specification.
- `relation: "retrospective"`: Document is a project or cycle retrospective.
- `relation: "postmortem"`: Document is an incident postmortem.
- `relation: "relates"`: General association.

This allows a single document to serve as an implementation plan for one issue and
background research for another without requiring spec modifications for new document kinds.
General categorization is supported via `labels`.

### 1.4. At-Mentions and Comments

At-mentions in document and section markdown bodies use the scheme-prefixed person
identifier syntax (`@user:...`, `@email:...`) specified in [`spec/identifiers.md`](identifiers.md)
(WRIT-102). The fold remains pure and does not parse markdown; clients extract and render mentions.

Comments attach to documents or sections using the subject-agnostic comment format
(`spec/comments.md`):
- Top-level document discussion: `subject: {"object_type": "document", "object_id": "<doc-id>"}`
- Section-level inline discussion: `subject: {"object_type": "section", "object_id": "<sec-id>"}`

---

## 2. Envelope Binding

Document and section operations are stored on append chains under:
```
refs/writ/<writer-id>/document
refs/writ/<writer-id>/section
```

Payloads conform to `spec/schemas/op-envelope.schema.json` and
`spec/schemas/document-ops.schema.json`:
- `object_type` MUST be `"document"` or `"section"`.
- `op_version` MUST be `1`.
- `object_id` MUST be a valid object identifier.

---

## 3. Operation Vocabulary

### 3.1. Document Operations (`object_type: "document"`)

| `op_type` | Body Schema | Description |
| --- | --- | --- |
| `create` | `{"title": string}` | Create a document container. |
| `update` | `{"title": string}` | Update document title. |
| `link` | `{"target": reference, "target_type"?: string, "relation": string}` | Add or update a cross-reference link. |
| `label` | `{"add"?: string[], "remove"?: string[]}` | Add or remove document labels. |

#### `create`
Initializes a document object with a required non-empty title:
```json
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "document",
  "op_type": "create",
  "op_version": 1,
  "body": {
    "title": "Architecture RFC: Distributed Search"
  }
}
```

#### `update`
Updates the document title:
```json
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "document",
  "op_type": "update",
  "op_version": 1,
  "body": {
    "title": "Architecture RFC: Distributed Search (Revised)"
  }
}
```

#### `link`
Establishes a cross-reference link keyed by `target`:
```json
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "document",
  "op_type": "link",
  "op_version": 1,
  "body": {
    "target": "fedcba9876543210fedcba9876543210",
    "target_type": "issue",
    "relation": "implementation-plan"
  }
}
```

#### `label`
Adds or removes labels using Add-Wins OR-Set semantics:
```json
{
  "object_id": "0123456789abcdef0123456789abcdef",
  "object_type": "document",
  "op_type": "label",
  "op_version": 1,
  "body": {
    "add": ["rfcs", "storage"],
    "remove": ["draft"]
  }
}
```

---

### 3.2. Section Operations (`object_type: "section"`)

| `op_type` | Body Schema | Description |
| --- | --- | --- |
| `create` | `{"document_id": reference, "position": position, "title"?: string, "body": string}` | Create a document section. |
| `edit` | `{"body": string}` | Edit section body content. |
| `move` | `{"position": position}` | Reorder section to a new fractional position. |
| `update` | `{"title"?: string, "position"?: position}` | Update section title or position. |
| `delete` | `{}` | Tombstone the section. |

#### `create`
Initializes a section object referencing its parent document:
```json
{
  "object_id": "a0b1c2d3e4f50123456789abcdef0123",
  "object_type": "section",
  "op_type": "create",
  "op_version": 1,
  "body": {
    "document_id": "0123456789abcdef0123456789abcdef",
    "position": "V",
    "title": "Motivation",
    "body": "Existing search indexing incurs high latency under load."
  }
}
```

#### `edit`
Asserts a new version of the section body:
```json
{
  "object_id": "a0b1c2d3e4f50123456789abcdef0123",
  "object_type": "section",
  "op_type": "edit",
  "op_version": 1,
  "body": {
    "body": "Existing search indexing incurs p99 latency spikes under multi-writer load."
  }
}
```

#### `move`
Reorders the section by updating its fractional index position:
```json
{
  "object_id": "a0b1c2d3e4f50123456789abcdef0123",
  "object_type": "section",
  "op_type": "move",
  "op_version": 1,
  "body": {
    "position": "k"
  }
}
```

#### `update`
Updates section title or position:
```json
{
  "object_id": "a0b1c2d3e4f50123456789abcdef0123",
  "object_type": "section",
  "op_type": "update",
  "op_version": 1,
  "body": {
    "title": "Background & Motivation"
  }
}
```

#### `delete`
Tombstones the section. The body is empty (`{}`):
```json
{
  "object_id": "a0b1c2d3e4f50123456789abcdef0123",
  "object_type": "section",
  "op_type": "delete",
  "op_version": 1,
  "body": {}
}
```

---

## 4. Multi-Value Register Reduction for Section Bodies

Section bodies fold under the `multi-value` register strategy ([`spec/fold.md`](fold.md) §Document section strategy: `multi-value`):

1. **Assertion:** Every `create` and `edit` operation writing `body` asserts a text version.
2. **Causal Dominance:** A write $w_2$ that causally observes write $w_1$ ($w_1 \prec w_2$ in the section's restricted DAG) supersedes $w_1$.
3. **Maximal Set:** The folded state retains only **causally maximal writes** — writes not succeeded by any other write in the causal DAG.
4. **Settled State:** If there is exactly one causally maximal write (or all maximal writes share identical text), the register is settled:
   ```json
   "body": "Settled markdown text"
   ```
5. **Conflicted State:** If there are multiple concurrent maximal writes with distinct text, all versions are preserved as a JSON array sorted in canonical code unit order:
   ```json
   "body": [
     "Concurrent edit from Alice",
     "Concurrent edit from Bob"
   ]
   ```
6. **Causal Collapse:** A subsequent `edit` operation whose commit causally succeeds all conflicting versions (by referencing their commits as causal parents) collapses the multi-value register back to a single settled string.
7. **Client Presentation:** The fold never merges text and never generates conflict markers. Presenting conflicts (via git conflict markers `<<<<<<<`, side-by-side diffs, or version pickers) is strictly a client capability.
