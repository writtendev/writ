# Resolution — re-anchoring & orphan degradation (v1)

Status: normative. Schema: [`schemas/resolution.schema.json`](schemas/resolution.schema.json).
Vectors: [`testdata/resolution/`](testdata/resolution/).

An anchor records where in code an object was originally placed ([`spec/anchors.md`](anchors.md)).
As code evolves across commits, rebases, and force-pushes, the anchor must be
resolved against a target tree to find its current location. When re-anchoring
succeeds, the object carrying the anchor is displayed at its new position; when
re-anchoring fails, it degrades to "orphaned but preserved" — never silently
lost (ARCHITECTURE.md §Anchoring).

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

## Scope & Core Principles

This section defines the **resolution algorithm** and **orphan semantics**:
how a content-based anchor is resolved against a target tree to produce a
deterministic resolution outcome.

1. **Standalone pure function.** Resolution is specified as a standalone
   deterministic function:
   $$\text{resolve}(\text{anchor}, \text{target tree}) \to \text{outcome}$$
   The function depends solely on the anchor object and the file paths and blob
   contents of the target tree. It requires no git commit graph, no commit-walk
   history, no network access, and no disk I/O.
2. **Separation from the fold.** Resolution is deliberately **not** part of
   the fold reducers (`fold(ops) \to state`). The fold carries anchors verbatim
   as data, ensuring fold reducers remain branch-independent and fold goldens
   remain stable. Anchor resolution is invoked at projection or presentation
   time against a specific target tree. Conformance fixture suites for
   orphaned anchors bind directly to the resolver's output, not folded state.
3. **Orphaning is not a state transition.** Orphaning is a pure evaluation of
   an anchor against a particular target tree. The same anchor MAY orphan
   against one branch (e.g. where the file was deleted) and resolve against
   another (e.g. where the file is present). Orphaning does not write to the
   operation log or mutate historical operations.
4. **Re-attachment model.** Re-attaching an orphaned object to a new code
   position happens exclusively by creating a *new* operation that
   references the original object and carries a new anchor. Historical
   operations and their anchors are immutable.

## Target Tree & Content Preparation

A **target tree** is presented to the resolver as a set of file entries mapping
repo-relative slash-separated paths to blob contents.

For each file in the target tree:

1. **Path format.** Paths follow standard git tree path conventions (UTF-8,
   non-empty, `/` separator, no leading/trailing `/`, no `.` or `..` segments).
2. **Blob OID derivation.** The blob object ID (OID) for a file is computed
   using standard git object hashing: `sha1("blob <size>\0<bytes>")` (for SHA-1
   repositories) or `sha256("blob <size>\0<bytes>")` (for SHA-256 repositories).
3. **Line splitting and normalization.** The target blob content is decoded and
   split into lines using the identical rules defined in `spec/anchors.md` §Lines
   and encoding:
   - **Splitting:** Split at LF (`0x0A`). Trailing LF does not produce an empty
     final line. CRLF preserves `\r`.
   - **Decoding:** Decoded as UTF-8, replacing invalid byte sequences with `U+FFFD`.
   - **Truncation:** Stored/compared lines longer than 1000 Unicode code points
     are truncated to their first 1000 code points.

## Resolution Outcome

The resolution function produces a resolution outcome object conforming to
[`schemas/resolution.schema.json`](schemas/resolution.schema.json):

```jsonc
{
  "anchor": { /* verbatim input anchor */ },
  "old": { /* side result */ },
  "new": { /* side result */ }
}
```

| Field    | Type   | Required | Meaning |
| -------- | ------ | -------- | ------- |
| `anchor` | object | yes      | The input anchor object verbatim, preserving unknown fields. |
| `old`    | object | see below| Resolution result for the `old` side. Present if and only if `anchor.old` is present. |
| `new`    | object | see below| Resolution result for the `new` side. Present if and only if `anchor.new` is present. |

At least one of `old` or `new` MUST be present in the outcome, exactly matching
the sides present in `anchor`.

### Overall Anchor Resolution Status

An anchor's overall resolution status is derived from its side results:

- **Resolved:** At least one present side has `outcome: "resolved"`.
- **Orphaned:** Every present side has `outcome: "orphaned"`.
- **Partially Resolved:** One side has `outcome: "resolved"` and another has
  `outcome: "orphaned"` (for example, a cross-side anchor where the base side's
  deleted line is absent in the target tree, but the head side's added line
  resolves).

### Side Result: Resolved

```jsonc
{
  "outcome": "resolved",
  "match": "exact-path-blob",
  "path": "engine/fold/fold.go",
  "range": { "start": 41, "end": 43 }
}
```

| Field     | Type    | Required | Meaning |
| --------- | ------- | -------- | ------- |
| `outcome` | string  | yes      | Const `"resolved"`. |
| `match`   | string  | yes      | The matching ladder rung that produced the resolution: `"exact-path-blob"`, `"exact-blob-moved"`, `"context-exact"`, or `"context-fuzzy"`. |
| `path`    | string  | yes      | The repo-relative path where the anchor resolved in the target tree. |
| `range`   | object  | see below| The resolved 1-based line range `{ "start": int, "end": int }`. Present for ranged anchors; absent for whole-file anchors. |

### Side Result: Orphaned

```jsonc
{
  "outcome": "orphaned",
  "reason": "no-candidate"
}
```

| Field     | Type   | Required | Meaning |
| --------- | ------ | -------- | ------- |
| `outcome` | string | yes      | Const `"orphaned"`. |
| `reason`  | string | yes      | The reason for orphaning from the v1 enumeration: `"path-absent"`, `"no-candidate"`, `"below-threshold"`, `"ambiguous"`, or `"unsupported-version"`. |

## The Matching Ladder

Each side (`old` and `new`) present in the anchor is resolved independently.
Resolution proceeds down a first-match-wins ladder of 5 rungs. The first rung
whose criteria are satisfied produces the side result; subsequent rungs are not
evaluated.

### Version Pre-Check

If `anchor.version` is not `1` (or not supported by the implementation), the
side immediately degrades to:
$$\{ \text{"outcome"}: \text{"orphaned"}, \text{"reason"}: \text{"unsupported-version"} \}$$

### Whole-File Anchors

When a side has no `range` (and no `context`), it represents a whole-file anchor.
Whole-file anchors evaluate as follows:

1. **`exact-path-blob`:** If `anchor.path` exists in the target tree and its
   blob OID equals `anchor.blob`, it resolves to:
   $$\{ \text{"outcome"}: \text{"resolved"}, \text{"match"}: \text{"exact-path-blob"}, \text{"path"}: \text{anchor.path} \}$$
2. **`exact-blob-moved`:** If `anchor.path` is absent or holds a different blob,
   but `anchor.blob` is present elsewhere in the target tree, choose the
   lexicographically smallest path $p$ holding that blob OID and resolve to:
   $$\{ \text{"outcome"}: \text{"resolved"}, \text{"match"}: \text{"exact-blob-moved"}, \text{"path"}: p \}$$
3. **Orphan:** Whole-file anchors carry no range or context lines and cannot
   perform content matching. If the recorded path exists in the target tree,
   it orphans with reason `"no-candidate"`; otherwise with `"path-absent"`.

---

### Ranged Anchors

For ranged anchors (where `range` and `context` are present), the 5-rung ladder
operates as follows:

#### Rung 1: `exact-path-blob`

- **Condition:** The file at `anchor.path` exists in the target tree, and its
  computed blob OID equals `anchor.blob`.
- **Outcome:** The file is completely untouched. The anchored range carries over
  verbatim:
  ```jsonc
  {
    "outcome": "resolved",
    "match": "exact-path-blob",
    "path": "<anchor.path>",
    "range": { "start": anchor.range.start, "end": anchor.range.end }
  }
  ```

#### Rung 2: `exact-blob-moved`

- **Condition:** The blob at `anchor.path` in the target tree does not match
  `anchor.blob` (or `anchor.path` is absent from the target tree), BUT one or
  more other paths in the target tree have a blob OID matching `anchor.blob`.
- **Tiebreak:** If multiple paths hold identical content with `anchor.blob`,
  select the **lexicographically smallest path** $p$.
- **Outcome:** The file moved without edits. The anchored range carries over
  verbatim at the new path:
  ```jsonc
  {
    "outcome": "resolved",
    "match": "exact-blob-moved",
    "path": "<p>",
    "range": { "start": anchor.range.start, "end": anchor.range.end }
  }
  ```

#### Rung 3: `context-exact`

- **Condition:** The blob OID did not match at Rung 1 or Rung 2 (the file was
  edited or moved and edited), but the anchored lines appear verbatim and in
  order somewhere in the candidate target content.
- **Search Scope:**
  - If `anchor.path` exists in the target tree: search **only** within `anchor.path`.
  - If `anchor.path` is absent from the target tree: search across all paths in
    the target tree, evaluated in lexicographical order.
- **Window Matching:**
  Let $N = \text{anchor.range.end} - \text{anchor.range.start} + 1$ be the
  length of the anchored range.
  - **Non-elided range (`context.omitted` absent):** A candidate window of $N$
    lines starting at 1-based line $s$ ($1 \le s \le \text{total\_lines} - N + 1$)
    is an exact match iff for all $0 \le i < N$:
    $$\text{target\_lines}[s + i - 1] == \text{context.lines}[i]$$
  - **Elided long range (`context.omitted` present):** The range spans $N = 64 + \text{context.omitted}$
    lines. `context.lines` contains 32 head lines and 32 tail lines. A candidate
    window of $N$ lines starting at line $s$ is an exact match iff:
    $$\text{target\_lines}[s + i - 1] == \text{context.lines}[i] \quad \text{for } 0 \le i < 32$$
    $$\text{target\_lines}[s + N - 32 + j - 1] == \text{context.lines}[32 + j] \quad \text{for } 0 \le j < 32$$
- **Candidate Selection & Tiebreaks:**
  If multiple candidate windows match verbatim, select the winning window using
  the following tiebreak rules in fixed order:
  1. **Highest collar score:** Count of matching `before` lines immediately
     preceding line $s$ plus matching `after` lines immediately following line
     $s + N - 1$ (up to $|\text{before}| + |\text{after}|$ points).
  2. **Smallest distance from original position:** $|s - \text{anchor.range.start}|$.
  3. **Earliest position:** Smallest line number $s$.
  4. **Lexicographically earliest path:** Smallest path $p$ (when searching across
     multiple files).
- **Outcome:**
  Let $(p, s, s + N - 1)$ be the uniquely selected window:
  ```jsonc
  {
    "outcome": "resolved",
    "match": "context-exact",
    "path": "<p>",
    "range": { "start": s, "end": s + N - 1 }
  }
  ```

#### Rung 4: `context-fuzzy`

- **Condition:** No candidate window matched verbatim at Rung 3. We score all
  candidate windows of length $N$ across candidate files against the anchored
  lines and surrounding collar.
- **Search Scope:**
  - If `anchor.path` exists in the target tree: search **only** within `anchor.path`.
  - If `anchor.path` is absent from the target tree: search across all paths in
    the target tree, evaluated in lexicographical order.
- **Scoring Function:**
  For each candidate window of length $N$ starting at line $s$ in candidate file $p$:
  - **Anchored lines score ($2$ points per match):**
    - Non-elided: count of $0 \le i < N$ where $\text{target\_lines}[s + i - 1] == \text{context.lines}[i]$.
      $$\text{anchored\_points} = 2 \times \text{matches}, \quad \text{max\_anchored} = 2N$$
    - Elided: count of matching head-32 lines plus matching tail-32 lines (out of 64).
      $$\text{anchored\_points} = 2 \times \text{matches}, \quad \text{max\_anchored} = 2 \times 64 = 128$$
  - **Collar lines score ($1$ point per match):**
    - $\text{before\_matches}$: count of matching lines in `context.before`
      immediately preceding $s$ ($1 \le k \le |\text{before}|$, $\text{target\_lines}[s - k] == \text{context.before}[|\text{before}| - k]$).
    - $\text{after\_matches}$: count of matching lines in `context.after`
      immediately following $s + N - 1$ ($0 \le k < |\text{after}|$, $\text{target\_lines}[s + N + k] == \text{context.after}[k]$).
    - $\text{collar\_points} = \text{before\_matches} + \text{after\_matches}$, $\text{max\_collar} = |\text{context.before}| + |\text{context.after}|$.
  - **Total score & Maximum score:**
    $$\text{score} = \text{anchored\_points} + \text{collar\_points}$$
    $$\text{max\_score} = \text{max\_anchored} + \text{max\_collar}$$
- **Acceptance Criteria:**
  A candidate window is accepted if and only if:
  1. **Threshold:** It scores at least 60% of the maximum possible score:
     $$\frac{\text{score}}{\text{max\_score}} \ge 0.60 \quad (\text{i.e. } \text{score} \times 100 \ge 60 \times \text{max\_score})$$
  2. **Strict best:** It strictly beats every other candidate window:
     $$\text{best\_score} > \text{second\_best\_score}$$
- **Outcome:**
  If accepted, resolves to:
  ```jsonc
  {
    "outcome": "resolved",
    "match": "context-fuzzy",
    "path": "<p>",
    "range": { "start": s, "end": s + N - 1 }
  }
  ```

#### Rung 5: `orphaned`

If Rungs 1 through 4 do not resolve the anchor, the side degrades to
`"outcome": "orphaned"`. The orphan `reason` is assigned from the following
rules:

| Reason | Condition |
| ------ | --------- |
| `"unsupported-version"` | Anchor version is unsupported or unimplemented. |
| `"path-absent"` | The recorded path is absent from the target tree, and no candidate file yielded any matching lines (max score is 0). |
| `"no-candidate"` | The file at `anchor.path` exists (or candidate files were checked), but has fewer lines than range length $N$, or every candidate window scored 0 points. |
| `"below-threshold"` | At least one candidate window scored $> 0$ points, but the highest score was strictly below the 60% threshold ($\text{best\_score} < 0.60 \times \text{max\_score}$). |
| `"ambiguous"` | The highest score met or exceeded the 60% threshold, but two or more distinct candidate windows tied for the highest score ($\text{best\_score} == \text{second\_best\_score}$). |

Outcome:
```jsonc
{
  "outcome": "orphaned",
  "reason": "<reason>"
}
```

## Orphan Semantics & Client Obligations

The guiding principle of Writ's anchoring model is that **anchored objects are
never silently lost**.

1. **Preservation:** Implementations MUST NOT drop, discard, or hide orphaned
   anchors.
2. **Presentation:** Clients MUST present an orphaned anchor to the user with:
   - Clear visual indication that it is *orphaned* / *unanchored* in
     the current tree.
   - The full folded payload of the object carrying it.
   - The recorded historical location: path, range, and the captured context
     lines (`before`, `lines`, `after`).
3. **Immutability:** Implementations MUST NOT mutate the original anchor or op
   payload when resolution fails.
4. **Re-attachment:** When a user or client re-attaches an orphaned object to
   a new position, it MUST be recorded as a new operation, preserving the full
   historical lineage.

## V1 Scope Boundaries (Non-Normative)

The v1 resolution specification deliberately bounds its algorithmic complexity:

- **No commit-walk rename heuristics:** Resolution does not walk git commit
  history or execute git diff rename heuristics (`-M`). It relies entirely on
  content identity (`blob`) and verbatim context matching across candidate files.
- **No cross-blob splice recovery:** If a previously contiguous range of code is
  split across multiple files or spliced non-contiguously, resolution will orphan
  or match only the single surviving contiguous window.
- **Deterministic pure evaluation:** Because resolution requires only the anchor
  and the target tree, implementations can run resolution in any context
  (projection builders, CLI tools, web frontends) with guaranteed identical
  results.
