# Anchors — content-based positions in code (v1)

Status: normative. Schema: [`schemas/anchor.schema.json`](schemas/anchor.schema.json).
Vectors: [`testdata/anchors/`](testdata/anchors/).

An **anchor** records *where in the code* an object points — a value type
([`spec/value-types.md`](value-types.md)) any schema-declared object can
carry. Writ anchors to **content** — blob identity plus captured hunk
context — never to bare line numbers, so an anchored position survives
force-pushes, rebases, and renames as well as possible, and degrades to
"orphaned but preserved" when it cannot (ARCHITECTURE.md §Anchoring).

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

## Scope

This section defines the anchor **format**: the value object, its fields,
and the invariants a conforming producer and validator enforce. It
deliberately does not define:

- **Re-anchoring and orphan degradation** — how an anchor is resolved
  against a target tree (`resolve(anchor, tree) → position | orphaned`).
  That is its own spec section ([`resolution.md`](resolution.md)), implemented
  by the engine's anchor resolver (WRIT-66) and pinned by standalone resolution
  vectors and the orphaned-anchors fixture family (WRIT-19). This section only
  guarantees the format *carries enough signal* for resolution — see
  [Resolution affordances](#resolution-affordances-non-normative).
- **The op types that embed an anchor.** Which fields of which object types
  carry one is declared by a schema ([`spec/schema-ops.md`](schema-ops.md)).
  An anchor is a value object inside op bodies, not an op itself.
- **Cross-repo references.** Anchors are repo-local: every OID in an anchor
  refers to an object in the repository whose ref namespace carries the
  containing op. Workspace-global identity is specified in
  [`spec/identifiers.md`](identifiers.md) (WRIT-16).

Because anchors travel inside op payloads, they inherit the op envelope's
rules: canonical JSON encoding (byte-stable, signature-covered) and
unknown-field tolerance — readers MUST preserve and ignore fields they do
not understand, and MUST NOT drop them on rewrite.

## The anchor object

```jsonc
{
  "version": 1,
  "old": { /* side anchor */ },
  "new": { /* side anchor */ }
}
```

| Field     | Type    | Required | Meaning |
| --------- | ------- | -------- | ------- |
| `version` | integer | yes      | Anchor format version. This section defines version `1`. |
| `old`     | object  | see below | Position in the **old** content — the base side of the change the anchor was made against. Anchors on deleted lines live here. |
| `new`     | object  | see below | Position in the **new** content — the side whose then-current form the anchor addresses. |

At least one of `old` and `new` MUST be present. An anchor made outside any
diff view (on a file "at commit X") uses `new` alone. An anchor on content
that the change removes (a deleted line, a deleted file) uses `old` alone.
Both sides are present when the producer can state the position in both the
old and new content — an anchor on an unchanged line of a modified or
renamed file, or a range that spans deleted and added lines
([cross-side ranges](#cross-side-ranges)).

Why two sides rather than a side *marker*: a single (blob, range) cannot
represent a range that starts in deleted content and ends in added content,
which real diff-viewing platforms permit (GitHub `start_side: "LEFT"` +
`side: "RIGHT"`); and losing the old-side position of an anchor on a
deleted line would make base-side rendering unreconstructible. The
dual-sided shape follows Radicle's `CodeLocation` (old/new ranges), a design
this project credits in ARCHITECTURE.md.

### The side anchor

```jsonc
{
  "commit": "2ae787a3e353251a99120a6935bfd6b807e60d5a",
  "path": "engine/fold/fold.go",
  "blob": "b7e23ec29af22b0b4e41da31e868d57226121c84",
  "range": { "start": 41, "end": 43 },
  "context": {
    "before": ["\tif err != nil {", "\t\treturn nil, err", "\t}"],
    "lines": ["\tstate.Status = next", "\tstate.UpdatedAt = op.Time", "\treturn state, nil"],
    "after": ["}", "", "// fold folds one op."]
  }
}
```

| Field     | Type   | Required | Meaning |
| --------- | ------ | -------- | ------- |
| `commit`  | string | yes      | OID of the commit in whose tree this side was observed — for `new`, typically the head of the change being annotated; for `old`, its base. Provenance, and the entry point for diff- and rename-based resolution. |
| `path`    | string | yes      | Repo-relative slash-separated path of the file within `commit`'s tree. |
| `blob`    | string | yes      | OID of the file's blob at `path` in `commit`'s tree — the content identity the anchor is anchored *to*. |
| `range`   | object | no       | The anchored line range within the blob. **Absent means the anchor addresses the file as a whole** ([whole-file anchors](#whole-file-anchors)). |
| `context` | object | with `range` | Captured content of and around the range ([context capture](#context-capture)). Present if and only if `range` is present. |

`range`:

| Field   | Type    | Required | Meaning |
| ------- | ------- | -------- | ------- |
| `start` | integer | yes      | First anchored line, 1-based. MUST be ≥ 1. |
| `end`   | integer | yes      | Last anchored line, inclusive. MUST be ≥ `start`. A single-line anchor has `end` = `start`. |

`start` and `end` MUST NOT exceed the blob's line count (a constraint on
producers, checkable only with the blob in hand; the standalone vectors do
not exercise it, the fixture repos of WRIT-19 do).

### OIDs

`commit` and `blob` are lowercase hexadecimal object IDs: exactly 40
characters (SHA-1 repos) or exactly 64 (SHA-256 repos). The containing
repository's object format governs; all OIDs within one anchor MUST use the
same length. Anchors do not record the hash algorithm — they are repo-local
and the repository already fixes it.

### Paths

A `path` MUST be non-empty, use `/` as its only separator, and contain no
leading or trailing `/`, no empty segment, and no `.` or `..` segment — the
same shape git tree paths take. `old.path` and `new.path` MAY differ; that
is the [rename representation](#renames).

Paths are JSON strings and therefore UTF-8. **v1 limitation, stated
deliberately:** files whose git tree paths are not valid UTF-8 cannot be
anchored in v1. A later version can add an alternate byte-level path
carrier under the unknown-field evolution rules.

## Lines and encoding

Deterministic re-anchoring (WRIT-66: same inputs, same result, always)
requires that two implementations derive the *same lines* from the same
blob. These rules define that derivation exactly:

1. **Splitting.** A blob's content is split into lines at LF (byte `0x0A`),
   at the byte level, before any decoding. The LF terminates a line and is
   not part of it. A final segment not terminated by LF is still a line; an
   empty final segment (content ending in LF) is not. The empty blob has
   zero lines. CR (`0x0D`) receives no special treatment: a CRLF file's
   lines each end with a trailing CR, preserved verbatim.
2. **Decoding.** Each line's bytes are decoded as UTF-8; every byte that is
   not part of a valid UTF-8 encoding is replaced with U+FFFD (one
   replacement character per invalid byte). This transform is deterministic,
   so applying it to both the captured context and a candidate target line
   yields a consistent comparison even for non-UTF-8 content. Binary blobs
   are not excluded — their captured lines are simply low-signal; their
   `blob` OID still anchors exactly.
3. **Truncation.** A decoded line longer than 1000 Unicode code points is
   stored truncated to its first 1000 code points. Consumers comparing
   context against target content MUST apply the same truncation to target
   lines. This bounds op size on pathological content (minified sources)
   without breaking exact-match determinism.

A stored line therefore never contains LF and never exceeds 1000 code
points — the schema enforces both.

Note the enforcement asymmetry: line bounds are enforced by producers through
**truncation** (`decodeAndTruncateLine`), unlike `person-id` and `reference` in
[`identifiers.md`](identifiers.md) which are enforced by **rejection** (a
truncated identifier or reference points to a different entity or nothing). A
stored anchor instance carrying a line exceeding 1000 code points violates the
schema and is rejected by schema validation.

## Context capture

`context` captures the anchored content itself and a small collar around
it, decoded and truncated per the rules above:

| Field     | Type             | Required | Meaning |
| --------- | ---------------- | -------- | ------- |
| `before`  | array of strings | yes      | The lines immediately preceding `range.start`, in file order. A producer that can read the blob MUST capture exactly 3 — fewer only when the file boundary leaves fewer. A producer without access to the surrounding content MAY capture fewer, including none. |
| `lines`   | array of strings | yes      | The anchored lines themselves. Never empty. |
| `omitted` | integer          | no       | Count of elided middle lines, present only on truncated captures of long ranges (below). MUST be ≥ 1 when present. |
| `after`   | array of strings | yes      | The lines immediately following `range.end`, in file order. Same capture rule and allowances as `before`. |

`before` and `after` are always present, as possibly-empty arrays — absent
and empty would be two encodings of one meaning, which canonical encoding
exists to avoid.

**Long ranges.** When the range spans **64 lines or fewer**, `lines` MUST
contain the complete content of the range and `omitted` MUST be absent.
When it spans more, `lines` MUST contain exactly the first 32 and last 32
lines of the range (64 entries) and `omitted` MUST equal
`(end − start + 1) − 64`. The schema pins the 64-entry ceiling on `lines`;
the rest is cross-field arithmetic JSON Schema cannot express — validators
enforce it alongside the schema, and the invalid vectors pin it.

The collar width (3) matches unified-diff default context: enough signal
for fuzzy re-anchoring to distinguish the range from similar code
elsewhere, small enough to keep every op carrying an anchor cheap.

## Case representations

The definitions above compose to cover the cases history rewriting
produces. These shapes are pinned by the valid vectors named below.

### Whole-file anchors

A side with no `range` (and therefore no `context`) addresses the file as
a whole — a file-level annotation, an anchor on a binary or empty
file. Vector: `whole-file-new.json`.

### Deleted lines and deleted files

Content the change removes exists only on the old side, so the anchor
carries `old` alone: the base commit, the path and blob there, and for
line-level anchors the range and context in the *base* blob. An anchor on
a file's deletion is `old` alone with no range. When later history restores
the content, the preserved old-side blob and context are what re-anchoring
works from. Vectors: `deleted-line-old.json`, `whole-file-deletion-old.json`.

### Renames

An anchor never mutates, so it has no "the file moved" state — it records
where the content *was observed*, per side: `old.path` is the path in the
base tree, `new.path` the path in the head tree, and they differ exactly
when the change renames the file. Rewrites that move content after the
anchor was made are the resolver's problem, and the format's contribution
is that `blob` is path-independent: an unchanged file found at a new path
re-anchors exactly by blob identity, with `context` taking over when the
move also edited the file. Vector: `rename-both-sides.json`.

### Cross-side ranges

An anchor can select a contiguous span of *diff rows* — each row a
context line (present in both files), a deletion (old only), or an
addition (new only) — and such a span need not live in one file's
content. The representation is the span's **projection onto each file**:
each side's `range` runs from the first through the last line of that
side's file appearing in the span (context rows count for both sides),
and a side with no lines in the span is absent. The projection is total
and direction-agnostic, so two converters of the same span produce the
same two ranges regardless of how the hunk interleaves its runs or which
side the span starts and ends on (GitHub permits both `start_side: LEFT`
→ `side: RIGHT` and the reverse). The anchor does not record how the
sides interleave in any particular diff rendering — that is derivable
from the two commits, and rendering is a client concern. Vectors:
`cross-side-range.json`, `github/multi-line-cross-side.json`,
`github/reverse-cross-side.json` (a reverse-direction span crossing an
interior context row).

## Versioning and evolution

`version` is the format's evolution seam, and it leads the object (general
versioning and forward-compatibility rules: `spec/forward-compatibility.md`):

- Additive change — new optional fields — does **not** bump `version`.
  Unknown-field tolerance already carries it: old readers preserve and
  ignore what they don't understand.
- Incompatible change — altered meaning of existing fields, new
  requirements — bumps `version` to the next integer and gets its own
  schema.
- A reader encountering a `version` it does not implement MUST treat the
  anchor as **opaque but preserved**: the containing op remains valid, the
  anchor's bytes are retained and re-emitted untouched, and clients present
  the carrying object as position-unresolved. Rejecting the op would let an
  old client destroy a new client's data, which the envelope rules forbid.

The v1 schema validates version-1 anchors exactly (`"version": {"const": 1}`);
it is not the instrument for the opaque-preservation rule, which operates
above per-version validation.

The schema deliberately does not set `additionalProperties: false`
anywhere: a v1 validator must accept anchors written by producers that have
added fields since.

## Resolution affordances (non-normative)

Why these fields are enough to re-anchor across common rewrites — the
algorithm itself, thresholds, and orphan semantics are specified in
[`resolution.md`](resolution.md):

- **`blob`** is the exact-match key. Force-pushes and rebases that do not
  touch the file leave its blob OID intact somewhere in the new tree;
  finding it (at the recorded path first, anywhere second) re-anchors
  exactly, including across renames, with no content comparison at all.
- **`context`** is the fuzzy signal. When the blob is gone — the file was
  edited — the captured before/lines/after collar locates the range in the
  edited content by line matching, tolerating drift the way diff hunks do.
- **`commit`** is the diff entry point. When content matching is ambiguous,
  a resolver holding the repository can walk from the recorded commit to
  the target (rename detection, line mapping through diffs) — the recorded
  provenance makes that computation possible but never required.
- **`path`** is the first place to look and the last thing shown: an
  orphaned anchor still names the file and shows the captured lines, which
  is what "orphaned but preserved" renders as.

## Appendix A — GitHub inline-comment convertibility (informative)

The GitHub bridge read path must import GitHub pull-request
review comments without losing position information. This appendix
demonstrates the mapping; the conversion vectors under
[`testdata/anchors/github/`](testdata/anchors/github/) make it concrete as
`{github, pr, anchor}` triples. The vectors' `anchor` members are validated
against the schema by `spec/anchors_test.go` today; executing the
conversion itself becomes a bridge test when the bridge lands.

A GitHub review comment's position fields, and where each lands:

| GitHub field | Disposition |
| ------------ | ----------- |
| `path` | Names the file on the **head side** of the diff regardless of `side` (the old side only when the file was deleted), so it feeds `new.path` directly; `old.path` comes from the diff's file pairing (rename detection) between the two commits — the same computation that locates the old-side blob. |
| `side`, `start_side` | Encoded structurally: `RIGHT` positions produce `new`, `LEFT` positions produce `old`; a `start_side` ≠ `side` range produces both ([cross-side ranges](#cross-side-ranges)). |
| `line`, `start_line` | `range.end` and `range.start` on the corresponding side (`start_line` absent means a single-line comment: `start` = `end` = `line`). |
| `original_commit_id`, `original_line`, `original_start_line` | The anchor is captured at the comment's *original* position: for `RIGHT`, `commit` is `original_commit_id`; for `LEFT` it is the diff's base — the merge-base of the PR's base branch and `original_commit_id`, which the bridge computes from the repository (GitHub's API reports only the *current* `base_sha`, not the base at comment time; the merge-base against the original head recovers it unless the base branch itself was rewritten, in which case the current-position fallback below applies). The original line fields feed `range`. GitHub's *current* `commit_id`/`line` are that platform's own re-anchoring output — derived state Writ's resolver recomputes rather than imports. |
| `diff_hunk` | Informative excerpt, redundant for capture: the bridge holds the repository, so `context` is captured from the blob at the recorded commit per the [capture rules](#context-capture), never parsed out of the hunk. |
| `position`, `original_position` | Legacy hunk offsets, derivable from `diff_hunk` + line numbers; carried by nothing, reconstructible by the bridge on the write path from the diff itself. |
| `subject_type` | `"line"` produces ranged sides; `"file"` produces a [whole-file anchor](#whole-file-anchors). |
| `commit_id`, `in_reply_to_id`, `body`, reactions, author, timestamps | Not position data — they map to the body and envelope of the op carrying the anchor, not to the anchor. |

Every position field is thus either carried structurally, carried as
content, or derived state recomputable from what is carried — which is the
lossless-convertibility claim of this ticket's definition of done.

One honest boundary: capture needs the blobs, so it needs the recorded
commits to be fetchable. When they are not — the original head was
force-pushed away and the platform garbage-collected it — a v1 anchor for
the *original* position cannot be captured. The bridge then anchors at the
comment's **current** position instead (`commit_id`, `line`, `side` —
GitHub's own re-anchoring output, whose commit is the live head and always
fetchable): a faithful anchor for where the platform itself says the
comment now points, rather than a fabricated one for a position whose
content is gone.

Worked examples, as vectors:

- `github/single-line-right.json` — ordinary new-side comment.
- `github/deleted-line-left.json` — `side: "LEFT"`, old-side anchor.
- `github/multi-line-cross-side.json` — `start_side: "LEFT"`,
  `side: "RIGHT"`, dual-sided anchor.
- `github/reverse-cross-side.json` — `start_side: "RIGHT"`,
  `side: "LEFT"`, spanning an interior context row: both projections
  include the unchanged line.
- `github/file-level.json` — `subject_type: "file"`, whole-file anchor.
