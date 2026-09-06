# The op envelope

Status: **normative**. The key words MUST, MUST NOT, SHOULD, and MAY are
to be interpreted as described in RFC 2119.

Every Writ operation carries the same envelope: op id, parent op ids
(the DAG edges), object id, object type, op type and version, author,
timestamp, signature, and a type-specific body. This document defines
where each of those fields lives, what it means, and what a conforming
reader accepts and rejects.

## One record, two carriers

An op **is a git commit**. The envelope is one logical record split
across two carriers, and each field has exactly one home:

| Logical field | Carrier |
| --- | --- |
| op id | the op commit's id (SHA) |
| parent op ids | the commit's parent ids |
| author | the commit author identity (`Name <email>`) |
| timestamp | the commit author time |
| signature | the commit signature header (`gpgsig`) |
| object id, object type, op type, op version, body | a canonical JSON blob at a fixed path in the commit's tree |

No commit-carried field is mirrored into the payload. (The one restatement
that runs the other way — the commit message, derived from payload fields
purely so `git log` reads — is covered under the commit carrier below.)
The alternative — repeating
parents, author, and timestamp inside the JSON payload — was considered
and rejected: it creates two sources of truth that can disagree (payload
parents vs. commit parents), and it is incoherent at the edges, since a
payload cannot contain its own content-derived op id, nor a signature
that covers the payload the signature lives inside. Producers MUST NOT
mirror commit-carried fields into the payload; a reader that encounters
payload fields shadowing commit data (a `parents`, an `author`) treats
them as ordinary unknown fields — preserved and ignored — and the commit
remains the sole source of truth for what they appear to name.

The cost of the split is stated outright: **the payload alone is not
self-describing**. A conforming reader always needs the commit; handing
someone an `op.json` blob out of context loses the op's identity,
ancestry, authorship, and signature.

## The commit carrier

Because the op id *is* the commit id, every byte the commit id derives
from is part of this spec. A producer MUST construct op commits exactly
as follows; two conforming producers given the same logical op and the
same signing key then mint the same op id.

- **Object format.** Commits are standard git commit objects in the
  repository's object format. The op id is the commit id under that
  format — SHA-1 in today's repositories, SHA-256 in SHA-256
  repositories. Op ids are therefore repository-scoped, like every other
  git object id.
- **Tree.** The commit tree MUST contain exactly one entry: a blob named
  `op.json` with file mode `100644` at the root of the tree, holding the
  payload described in the next section. No subdirectories, no other
  files.
- **Parents.** Every commit parent is a happens-before edge. For a
  non-empty chain, `parents[0]` MUST be the writer's previous op commit
  on that chain (the chain predecessor). Additional parents
  (`parents[1:]`) are causal references to other ops that this op
  observed or depended on. When a chain is empty (the writer's first op
  on that chain), causal references start at `parents[0]`; if there are
  no causal dependencies, the op has zero parents. Parent ops MUST NOT
  point at non-op commits. (See [`spec/ref-layout.md`](ref-layout.md) for
  ref layout and edge rules; an object's op-DAG is the
  ancestry-restricted subgraph over its `object_id`.)
- **Author and committer.** The committer identity and timestamp MUST be
  byte-identical to the author identity and timestamp. There is no
  separate "committer" concept in the op model.
- **Timestamp.** Git timestamps carry a UTC offset. Producers MUST write
  the offset `+0000` (i.e. record the instant in UTC). Readers MUST
  interpret any offset as the UTC instant it denotes rather than
  rejecting non-zero offsets — the instant, not the spelling, is the
  datum.
- **Message.** Producers MUST write exactly one line,
  `writ: <op_type> <object_type>/<object_id>`, followed by a single
  newline. The message is derived entirely from payload fields and
  exists only so `git log` over a writ ref is legible. It is a
  producer-side rule, pinned because the message bytes feed the op id:
  readers MUST ignore the message entirely — never parse it as data, and
  never validate it against the payload, which would make the message a
  second source of truth for fields the payload owns. The message is
  therefore the one place the payload's own fields are restated, and it
  is deliberately unverifiable: two commits with identical payloads and
  different messages are both valid ops, with different op ids. Op-id
  reproducibility across producers rests on producers following this
  rule, not on readers enforcing it — which is the trade the no-mirroring
  principle accepts to keep `git log` legible.
- **Signature.** Ops are signed with git's commit-signature machinery:
  the signature rides the `gpgsig` header, and SSH signatures use the
  namespace `git` — the same bytes `git commit -S` produces with
  `gpg.format=ssh`, so existing git tooling verifies ops unmodified.
  Verification mechanics, trust store format, and the verification
  outcome vocabulary are specified in [`spec/signing.md`](signing.md);
  this document only fixes where the signature lives.

## The payload carrier

`op.json` is a JSON object holding the fields that describe *what the
operation is*, encoded canonically per `spec/canonicalization.md` and
constrained by `spec/schemas/op-envelope.schema.json` (JSON Schema,
draft 2020-12).

### Byte-equality rule

The blob's bytes MUST be byte-identical to the canonical encoding of its
own content. A reader MUST re-canonicalize the blob and byte-compare,
rejecting the op on any mismatch — including inputs canonicalization
itself rejects (duplicate keys, lone surrogates, non-canonical
whitespace or number spellings, trailing newline). This one rule is what
makes payload bytes reproducible from payload content, so signatures and
content addressing never depend on encoder quirks.

### Fields

- `object_id` (string, required) — identifier of the collaborative
  object this op belongs to. Opaque at this layer: printable non-space
  ASCII (`^[\x21-\x7e]+$`), 1–256 characters. The globally unique id
  format and cross-repo references are specified in
  [`spec/identifiers.md`](identifiers.md) (WRIT-16); nothing in this envelope
  depends on the id's internal structure, and the envelope schema is
  deliberately unchanged so readers continue accepting any envelope-legal
  opaque id.
- `object_type` (string, required) — the object's type: `review`,
  `comment`, `approval`, `ci-status`, `issue`, `project`, `cycle`, and
  whatever later specs add. Lowercase `^[a-z][a-z0-9-]*$`, at most 64
  characters. Deliberately not a closed enum — see forward
  compatibility below.
- `op_type` (string, required) — the operation's type within its object
  type's vocabulary (e.g. `create`). Same lexical form as `object_type`.
  The per-object-type vocabularies are specified in `spec/review-ops.md`
  (reviews), `spec/comments.md` (comments), `spec/issue-ops.md` (issues),
  and `spec/project-cycle.md` (projects, cycles).
- `op_version` (integer, required) — schema version of this op type's
  body, starting at 1. A small JSON integer; it MUST be ≥ 1 and ≤ 2⁵³−1
  so it is always exactly representable as a double. Any field that
  could genuinely grow past 2⁵³ must be a string instead (see
  `spec/canonicalization.md`); versions are small by construction, so
  the integer form is safe and convenient.
- `body` (object, required) — the type-specific content. An open slot at
  this layer: each (`object_type`, `op_type`, `op_version`) triple
  defines its body schema in the op vocabulary specs (`spec/review-ops.md`
  for reviews, `spec/comments.md` for comments, `spec/issue-ops.md` for issues,
  and `spec/project-cycle.md` for projects and cycles).
  An op with no content still carries `"body":{}`.

### Forward compatibility

Unknown fields — at the top level and inside `body` — MUST be preserved
and ignored, never dropped: the schema deliberately allows additional
properties, and `object_type`/`op_type` are open string forms rather
than enums. An implementation that folds an object containing op types
or fields it does not understand MUST keep those ops intact in the DAG
so newer clients still see them. The full unknown-op and
forward-compatibility rules (how fold treats an op it cannot interpret)
are specified in `spec/forward-compatibility.md`; this document fixes only the
envelope-level constraint.

## Producer validation

A conforming producer MUST NOT sign an op it could have known was
invalid. Before the op commit is built, the producer MUST verify that:

1. The payload satisfies this document's envelope schema
   (`spec/schemas/op-envelope.schema.json`).
2. The payload is byte-canonical per the byte-equality rule above.
3. The payload satisfies the vocabulary schema for its `object_type`,
   for **every object type the producer itself emits**. A producer is
   not required to hold a schema for an object type it never writes; it
   is required to hold one for every type it does.
4. The `op_type` and `op_version` are ones the producer itself defines
   for that `object_type`. A producer never legitimately authors an op
   type or an op version it cannot interpret; where it appears to, the
   cause is a typo, and the op it would write is one no reader will ever
   interpret either.

Rule 3 is the one this document previously left unstated, and the gap is
not academic: the reader rules below constrain what an implementation
accepts, so an implementation that only implemented those could — and
one did — write ops that its own reader would reject.

Rules 3 and 4 are distinct, and the difference is why rule 4 is written
out rather than folded into rule 3. The **vocabulary schemas in
`spec/schemas/` are reader-safe by construction**: they gate their body
rules on the `op_version` they specify, so an op carrying an unknown
`op_type` or a future `op_version` is a *valid instance* of them. That
is deliberate — a reader must tolerate both
([`spec/forward-compatibility.md`](forward-compatibility.md)), and any
implementation that validates incoming ops against a published
vocabulary schema must not thereby break forward compatibility. Rule 4
is therefore a producer obligation that the schemas do not and should
not express; a producer satisfies it from its own vocabulary table, not
by schema validation alone. All six shipped vocabularies are built this
way, and each of their corpora carries vectors under `valid/` pinning an
unknown `op_type` and a future `op_version` as instances every one of
them accepts.

The asymmetry with reader validation is deliberate. An op is a signed
commit in an append-only log: a producer that writes an invalid op
cannot withdraw it. The op stays in history, is fetched by every clone,
and is rejected by every strict reader forever, and the best any client
can do is tombstone it in a local projection. A producer that refuses to
write one costs its caller an error message. Be maximally strict where
failure is free.

Rules 3 and 4 bind producers only, and "producer" means the act of
authoring a new op. They do not extend to re-encoding an op read from
the log: an implementation that decodes an op it fetched and
re-serializes it — to cache it, relay it, or project it — is acting as a
reader there, and MUST keep the tolerance defined in
[`spec/forward-compatibility.md`](forward-compatibility.md). Unknown
object types, unknown op types, unknown op versions, and unknown fields
pass through untouched on that path.

The line is the signature, not the intent. **Re-signing an op under a
new key is authoring, and is bound by rules 3 and 4** — a bridge that
reads an op from elsewhere and commits it onto its own writer chain has
produced a new op, whatever it calls the activity, and it vouches for
that op with its own identity. The tolerated case above is the one where
the original signed commit is carried through unchanged; a mirror, a
cache and a projection all qualify, and a re-signing bridge does not.
The consequence is intended: an implementation that cannot interpret an
op type cannot put its own name on it.

## Reader validation

A conforming reader, given a commit reached via a writ ref, MUST reject
the op (not repair, not skip silently — the reader's error surface says
why) if any of the following fail:

1. The commit tree does not contain exactly one entry, `op.json`, mode
   `100644`.
2. The payload fails the byte-equality rule above.
3. The payload fails schema validation: a required field is missing or
   a defined field violates its type or form. Unknown *additional*
   fields are not a violation.
4. The committer identity and timestamp are not byte-identical to the
   author's. (The message and timestamp-offset rules above bind
   producers, not readers: readers ignore the message, and interpret any
   offset as its UTC instant.)

Signature verification is a separate concern (see [`spec/signing.md`](signing.md)) — it
cannot live in fold, and this document does not define when it runs.

## Out of scope, with forward references

- Ref layout, writer-id convention, and refspecs: [`spec/ref-layout.md`](ref-layout.md).
- Per-op-type body schemas and vocabularies: review family ([`spec/review-ops.md`](review-ops.md)), comment ([`spec/comments.md`](comments.md)), issue ([`spec/issue-ops.md`](issue-ops.md)), project/cycle ([`spec/project-cycle.md`](project-cycle.md)).
- Fold semantics, ordering, and concurrency tiebreaks: [`spec/fold.md`](fold.md).
- Unknown-op handling and forward-compatibility rules: [`spec/forward-compatibility.md`](forward-compatibility.md).
- Globally unique object-id format and cross-repo references: [`spec/identifiers.md`](identifiers.md) (**WRIT-16**).
- Signature verification mechanics, trust store, and outcomes: [`spec/signing.md`](signing.md).

## Conformance data

- `spec/schemas/op-envelope.schema.json` — the payload schema.
- `spec/testdata/envelopes/valid/` — instances that validate and are
  byte-canonical.
- `spec/testdata/envelopes/invalid/` — instances a reader must reject
  (schema violations including non-object payloads and invalid identifiers,
  plus canonicalization failures including trailing data, duplicate keys,
  and lone surrogates); `index.json` records each file's expected rejection
  (`schema` or `canonicalization`) and the reason.
- Envelope fixtures as *generated git repositories* — full commits with
  signatures, including tampered ones — are the WRIT-17 fixture family
  (`spec/fixtures/testdata/descriptions/envelope-*.yaml` and
  `spec/fixtures/testdata/golden/envelope/`), covering valid envelopes, bad
  signatures, malformed payloads, and malformed trees. Per-op-type body
  schemas and vocabularies defer to WRIT-8–11.
