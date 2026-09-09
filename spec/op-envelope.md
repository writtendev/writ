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
- `object_type` (string, required) — the object's type. `schema` is the
  one type this specification defines
  ([`spec/schema-ops.md`](schema-ops.md)); every other value — `acme.widget`,
  `bigco.gadget`, whatever a consumer's schema declares — is data a
  `schema` object writes into the log, not a name this document knows.
  Namespace-qualified as `<namespace>.<type>` (`spec/schema-ops.md` §2):
  `^[a-z][a-z0-9-]{0,63}(\.[a-z][a-z0-9-]{0,63})?$`, at most 129
  characters (64 per segment, plus the separating dot) — the optional
  second segment is what lets two independently authored schemas each
  declare a type of the same bare name without contending for one wire
  `object_type`. `schema` itself is exempt: it has no namespace of its
  own, so it is the one value this grammar admits bare. This layer only
  admits the grammar and enforces nothing about which shape a given
  value must take; whether a non-`schema` `object_type` is required to
  be qualified is a schema-layer rule (`spec/schema-ops.md` §2) this
  envelope layer deliberately does not know or enforce. Deliberately not
  a closed enum — see forward compatibility below. A trailing segment of
  `lock` is grammar-legal here but unwritable as a chain — git rejects
  any ref path component ending in `.lock`; see
  [`spec/ref-layout.md`](ref-layout.md) §4.
- `op_type` (string, required) — the operation's type within its object
  type's vocabulary (e.g. `create`). Lowercase `^[a-z][a-z0-9-]*$`, at
  most 64 characters — unlike `object_type`, `op_type` is never
  namespace-qualified: op types live inside a declaring type's own
  vocabulary and were never globally contended, so qualifying them would
  be scope growth with no collision to close. Which op types an
  `object_type` has is declared by the `schema` object governing it
  ([`spec/schema-ops.md`](schema-ops.md) §4.2), except for `schema`
  itself, whose op vocabulary that document fixes.
- `op_version` (integer, required) — schema version of this op type's
  body, starting at 1. A small JSON integer; it MUST be ≥ 1 and ≤ 2⁵³−1
  so it is always exactly representable as a double. Any field that
  could genuinely grow past 2⁵³ must be a string instead (see
  `spec/canonicalization.md`); versions are small by construction, so
  the integer form is safe and convenient.
- `body` (object, required) — the type-specific content. An open slot at
  this layer: the fields legal for each (`object_type`, `op_type`,
  `op_version`) triple are the ones the `schema` object governing that
  `object_type` declares ([`spec/schema-ops.md`](schema-ops.md)).
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
3. The payload satisfies the declared fields for its `(object_type,
   op_type, op_version)`: every key present in `body` is **declared** —
   either a rule the schema object governing `object_type` names by
   `field`, or a member of `key` on some rule of that same
   `(op_type, op_version)` whose `strategy` is `keyed-lww` — and every
   field whose rule declares a `value_type`
   ([`spec/value-types.md`](value-types.md)) holds a value conforming to
   it. A `keyed-lww` key column need not also be declared as a field: it
   travels in `body` to address the write's register, not to carry a
   value of its own, and writ's own `schema` vocabulary is the worked
   example — `deprecate-type`'s `type` key column is never itself a
   `define-field`. Where a key names both a field and a key column of
   another rule, the field rule governs the value's declared *type* — but
   the JSON-string floor below still applies on top, because fold checks
   every key column present in the body whether or not the same name also
   carries a field rule.
4. The `op_type` and `op_version` are ones the schema object governing
   `object_type` declares. A producer never legitimately authors an op
   type or an op version it cannot interpret; where it appears to, the
   cause is a typo, and the op it would write is one no reader will ever
   interpret either.

A `keyed-lww` key column's value MUST be a JSON string regardless of what
its `key_types` entry says — JSON `null` included, which is not tolerated
here the way it is for an ordinary field's absent-shaped "no write" —
because fold's `keyed-lww` strategy treats a non-string key component,
`null` included, as uninterpretable ([`spec/fold.md`](fold.md) §5's "Key
components are strings", enforced via §7.1). This floor binds a key column
unconditionally, including one that is also a declared field: the field
rule governs the value's type (rule 3 above), but does not exempt the same
value from also being a JSON string when fold would key on it. A value
satisfying an `int` or `bool` field's own `value_type` — a JSON number or
boolean — is not a JSON string, so a name playing both roles can only be
written when the field's own encoding is already string-shaped, or when its
value is encoded as the string form the paragraph below describes — and
that string form is what the field's own `value_type` is checked against
too, not the raw string: a producer decodes the content before typechecking
on both sides of the union, the field-rule side exactly as the key-column
side, or an `int`/`number`/`bool`/`anchor` field that doubles as a key
column would satisfy no encoding at all and be permanently unwritable.

That resolution assumes the field's own merge strategy does not itself
inspect the value's raw JSON shape beyond "is this a JSON string" — true of
every strategy but one. `tombstone`'s reducer requires the *raw* body value
to already be a JSON boolean, never a string carrying `"true"`/`"false"` as
encoded content the way the paragraph above lets `int`, `number`, `bool`,
and `anchor` field values do ([`spec/fold.md`](fold.md) §7.1, enforced via
`fold.Uninterpretable`/`fold.ruleAccepts`). A `tombstone` field that also
names a keyed-lww key column of another rule is therefore refused for
*every* value, not merely encoded differently: the key-column floor's
JSON-string requirement and `tombstone`'s own JSON-boolean requirement can
never both hold for the same body value, whatever the field's own
`value_type` says (`tombstone`'s is unset or `bool`, never one of the
decodable members above).

The value's *content* MUST additionally conform to the governing entry —
the field's own `value_type` when the name is also a declared field
(the paragraph above), the key column's `key_types` entry otherwise —
checked the same way a field's value is checked against its `value_type`.
For a catalogue member whose ordinary encoding is already a JSON string
*and* whose own validation already admits exactly one spelling per value —
`string`, `text`, `person-ref`, `object-ref`, `git-oid`, `position` — the
key column's value *is* that content, unchanged: the JSON-string floor
above and the content check are the same string. For `int`, `number`,
`bool`, and `anchor`, whose ordinary encoding is a JSON integer, number,
boolean, or object respectively, the key column's string content is
instead read as that encoding, in text: `"7"` decodes to the JSON integer
`7`, `"true"` to the JSON boolean `true`, and a compact JSON object's text
to the `anchor` value itself. This is not a new constraint —
[`spec/schema-ops.md`](schema-ops.md) §3.1 already lives with it for
`op_version`, which travels as the decimal string `"1"` rather than the
JSON integer `1` wherever it is a key component — only its extension from
`int` alone to every non-string-shaped catalogue member.

The content MUST be that value's canonical JSON encoding, not merely text
that happens to parse to the right shape: the decimal string with no
leading zero, leading `+`, trailing fractional zero, exponent, or
surrounding whitespace; the literal `true`/`false`; or the object's
*compact* encoding, with no insignificant whitespace and members in the
sorted order [`spec/canonicalization.md`](canonicalization.md) already
defines for canonical JSON generally. `"7"`, `"7.0"`, `" 7"`, `"1e3"`, and
`"-0"` all decode to the same JSON number, but fold's `keyed-lww`
strategy ([`spec/fold.md`](fold.md) §5) keys a register on the key
column's raw string, not on the number it denotes — admitting every
spelling that merely parses would let two producers who mean the same key
address two registers that never converge, the opposite of what a
`keyed-lww` column exists for. A key column whose content parses but is
not that canonical spelling is a producer rejection, the same as content
that does not parse at all.

`timestamp` is the one catalogue member whose ordinary encoding is already
a JSON string but whose own grammar ([`spec/value-types.md`](value-types.md))
admits more than one spelling of the same instant, so it does not belong
in the "unchanged" bucket above despite needing no decode step: an RFC 3339
timestamp carries a UTC offset or an explicit numeric one, and optional
fractional seconds of any precision, so `"2024-01-01T00:00:00Z"`,
`"2024-01-01T01:00:00+01:00"`, and `"2024-01-01T00:00:00.000Z"` all name the
same instant with three different byte strings. Fold keys a `keyed-lww`
register on the byte string, not the instant it denotes (`value.Normalize`
is the identity for `timestamp` — normalization is intrinsic only to
`person-ref`, per its catalogue entry above), so a `timestamp` key column's
value MUST already be in that instant's one canonical spelling: UTC (a `Z`
offset, never a numeric one) with fractional seconds present only when
nonzero and written with no trailing zero digits — the same
one-spelling-per-value discipline the paragraph above holds `int` and
`number`'s decimal text to, applied here to a value that is already a JSON
string. A key column value that parses as a conforming `timestamp` but is
not that canonical spelling is a producer rejection, the same as one whose
content does not parse at all.

A `key_types` entry of `enum` is the one catalogue member this second
check cannot fully apply, decoding or not: `key_types` names a column's
type only, with no slot for the member list an `enum` field's own `enum`
attribute would supply, so an enum-typed key column is held to the
JSON-string floor above and checked no further. Two `keyed-lww` rules
within the same `(op_type,
op_version)` that share a key column name MUST agree on that column's
`key_types` entry, even when their `key` tuples otherwise differ: a
producer resolves a key column's declared type by column name alone, not
by which rule's `key` it belongs to, so a schema whose rules disagree has
every rule participating in that column withheld, rather than letting one
rule's entry silently govern the other's column
(`spec.CheckKeyColumnAgreement`, the same set-level, "no winner is ever
picked" standard [`spec/schema-ops.md`](schema-ops.md) §8 holds a shared
`target` to, applied to a shared key column instead).

The same resolver check refuses the one dual-role combination the
JSON-string floor makes unsatisfiable: a field rule whose `field` is also
some rule's key column for the same `(op_type, op_version)` MUST NOT use
strategy `tombstone`, because `tombstone`'s reducer requires the raw body
value to already be a JSON boolean and a JSON value is never both a
boolean and a string. Such a schema does not resolve — both rules are
withheld — so a producer never sees the combination from a resolved
schema; a `Vocabularies` assembled by hand rather than by a resolver is
refused the same values at write time, since the producer must never
accept what every reader quarantines.

"The schema object governing `object_type`" resolves through a fixed,
exclusive precedence — exactly one tier ever applies to a given op, so no
two sources of truth ever run on the same op and neither can disagree with
the other silently ([`spec/schema-ops.md`](schema-ops.md) is the normative
description; this is the producer-side consequence of it):

1. `object_type == "schema"` resolves against the engine's built-in
   bootstrap table, always, never the log
   ([`spec/schema-ops.md`](schema-ops.md) §7). This is the one permitted
   exception.
2. Otherwise, a `schema` object present in the repository's log declares
   `object_type` and it is not contested (see tier 3) — the log-sourced
   declaration, and only it.
3. Otherwise, `object_type` is **contested** — two or more `schema`
   objects in the log bind the same (namespace-qualified) `object_type`
   ([`spec/schema-ops.md`](schema-ops.md) §6) — the write is **permitted,
   unvalidated**. Nothing is ever removed from the log, so a contested
   `object_type` is contested *forever*: there is no step that resolves
   it. Refusing to write would therefore be a *permanent* write outage,
   not a transient availability dip, and the realistic cause is not
   malice — writ has no anonymous write path, so a peer able to push a
   colliding `schema` object is already a collaborator with push access —
   but two `schema` objects that bind the same bare `object_type` without
   sharing a namespace: different namespaces naming overlapping
   vocabulary, or a hand-crafted `object_id` deliberately colliding with
   one. Two writers bootstrapping the *same* namespace offline no longer
   reach this tier — a schema object's id is derived from its namespace
   rather than minted, so both converge on one object instead of
   contesting each other's `object_type`
   ([`spec/identifiers.md`](identifiers.md) §The `schema` object type: a
   derived exception). This tier's cost, stated plainly rather than left to be
   discovered: a producer may write ops here that **no conforming reader
   will interpret** — a reader also withholds interpretation from a
   contested `object_type` ([`spec/schema-ops.md`](schema-ops.md) §6) —
   and those ops are just as permanent as any other. That is accepted
   because it is recoverable in principle if the contest itself ever
   becomes recoverable, where a write refusal is a hard stop today with no
   such path. This tier is interim: it exists only because the contest has
   no resolution step yet, and disappears the day one is defined.
4. Otherwise — no schema in the log declares `object_type` — the producer
   MUST refuse, naming `object_type` and stating that nothing declares it.

Rule 3 deliberately checks less than a hand-written body schema would:
**required fields are not checked.** The schema DSL a `schema` object's
`define-field`/`define-op` vocabulary draws from declares types, value
types, merge strategies, and relations, and
nothing else — no requiredness, no computed fields, no hooks, no
permissions. A body missing a field its schema would otherwise want is not
corruption; it is an absent register at fold time, exactly like any other
field a producer chooses not to write. Widening rule 3 to check
requiredness would mean adding it to the schema DSL first, which is
framework-building and out of bounds.

Rule 3 is the one this document previously left unstated, and the gap is
not academic: the reader rules below
constrain what an implementation accepts, so an implementation that only
implemented those could — and one did — write ops that its own reader
would reject.

Rules 3 and 4 are distinct, and the difference is why rule 4 is written
out rather than folded into rule 3. At tier 1, the **vocabulary schema in
`spec/schemas/` is reader-safe by construction**: it gates its body rules
on the `op_version` it specifies, so an op carrying an unknown `op_type`
or a future `op_version` is a *valid instance* of it. That is deliberate
— a reader must tolerate both
([`spec/forward-compatibility.md`](forward-compatibility.md)), and any
implementation that validates incoming ops against a published
vocabulary schema must not thereby break forward compatibility. Rule 4
is therefore a producer obligation that schema does not and should not
express; a producer satisfies it from its own vocabulary table, not by
schema validation alone. `spec/testdata/schema-ops/valid/unknown-op-type.json`
is the instance pinning it: an op the vocabulary schema still accepts
despite the unknown `op_type`. At tier 2, the same distinction holds for
a different reason: a `define-op` list — or a `define-field` list, which is equally a
declaration of which op types exist (`spec/schema-ops.md` §4.2) — is
exactly the kind of thing a *reader* must not enforce (unknown `op_type`
and future `op_version` stay tolerated on the read path regardless of
what any schema declares); it binds the producer only.

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
- How an `object_type` declares its op types, fields, and merge rules: [`spec/schema-ops.md`](schema-ops.md).
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
  signatures, malformed payloads, and malformed trees.
- `spec/testdata/producer/` (WRIT-188) — tiers 2, 3, and 4 of the four-tier
  producer precedence above, exercised as paired verdicts: `index.json`
  names, per case under `cases/`, the producer's verdict (with a reason
  code where rule 3 or the envelope schema is the cause) and the
  disposition a reader gives the identical op if it reached the log by
  another path. Every rejected case is accepted by a reader — the corpus's
  whole point is that producer validation tightens the write path only,
  never the read path
  ([`spec/forward-compatibility.md`](forward-compatibility.md) is
  unchanged by this tightening, and its own corpus is the regression net
  for that). Tier 1 is covered in Go instead, by
  `TestSchemaObjectAlwaysValidatesAgainstBootstrapTable`.
