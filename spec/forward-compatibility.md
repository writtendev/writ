# Forward compatibility & unknown-op rules

Status: **normative**. The key words MUST, MUST NOT, SHOULD, and MAY are
to be interpreted as described in RFC 2119.

Writ operations are append-only and distributed across independent writers.
Because writers update at different cadences, older and newer clients routinely
collaborate on the same objects. This document defines the forward-compatibility
model: how implementations handle unknown op types, unrecognized fields, and
future schema versions, ensuring that schema evolution never requires flag days
or lockstep upgrades (ARCHITECTURE.md §The op envelope).

## The core principle: preserve and ignore

Implementations MUST **preserve and ignore** op types and fields they do not
understand — never drop. Old clients must not destroy new clients' data. An
older client encountering operations or fields emitted by a newer client MUST
retain them in the DAG, replicate them during sync, and maintain them through
all storage and codec operations unmodified.

## The three-way disposition split

When a conforming reader encounters an op in an object's history, it classifies
the op into one of three mutually exclusive categories:

1. **Malformed (Rejected):** The op fails reader validation as defined in
   `spec/op-envelope.md` §Reader validation:
   - The commit tree does not contain exactly one entry (`op.json`, mode `100644`).
   - The payload bytes fail the byte-equality rule (invalid canonical JSON,
     duplicate keys, unescaped control characters, lone surrogates, or non-canonical form).
   - The payload fails schema validation against `spec/schemas/op-envelope.schema.json`
     (missing required envelope fields, invalid field types, or malformed identifiers).
   - The committer identity and timestamp are not byte-identical to the author's.

   Malformed ops are invalid data. A conforming reader MUST reject them at parse
   time with an explicit error. Malformed ops MUST NOT enter the DAG and MUST NOT
   be folded into domain state.

   **This class is envelope-level only.** Every test above is applied to the
   commit and to `op.json` as a whole, before any op type's body is read. An op
   whose envelope satisfies all of them is not in this class no matter what its
   `body` turns out to contain — a body a reader cannot consume is category 3
   below, and category 3 ops MUST enter the DAG. The word "malformed" is used
   in `spec/fold.md` §7.1 of such a body; it does not put the op here.

2. **Interpretable:** The op is a valid envelope whose `(object_type, op_type, op_version)`
   triple is implemented by the reader, **and** whose `body` the reader's merge
   strategies can consume. The reader decodes the op's `body` and folds its
   effects into domain state according to the op type's normative specification.
   An op whose triple is implemented but whose body fails the test in category 3
   is not in this class and MUST NOT be folded.

3. **Valid-but-uninterpretable (Opaque):** The op is a valid envelope that the
   reader cannot interpret, for either of two reasons:

   - **The type is unknown.** Its `object_type` is unknown to the reader, its
     `op_type` is unknown under a known `object_type`, or its `op_version` is
     greater than the highest version the reader implements.
   - **The body cannot be consumed.** Its `(object_type, op_type, op_version)`
     triple *is* implemented, but a field of its `body` carrying a declared merge
     rule holds a JSON value that field's strategy cannot consume — a `set-union`
     field holding a number, a `keyed-lww` key component holding an object, any
     such field holding `null`. `spec/fold.md` §7.1 defines this test, states it
     once per strategy in §5, and is normative for it. The disposition is the one
     stated here: the op is uninterpretable, not malformed.

   The two reasons are one category because they have one disposition. A reader
   that treats the second as category 1 destroys data it was required to keep;
   a reader that treats it as category 2 folds values whose rendering no other
   implementation reproduces, which is the defect §7.1 exists to close.

   **Unknown is not invalid.** An uninterpretable op is a valid, well-formed
   operation. It MUST remain in the DAG, stay reachable, be synchronized to peers,
   and be preserved byte-for-byte. A reader MUST NOT reject or discard an op simply
   because its type, version, or body is uninterpretable.

   Rejection is at op granularity in both cases: an uninterpretable op contributes
   no field writes at all, not even from the fields a reader could have read.
   `spec/fold.md` §7.1 records why.

## Unknown fields (per-field tolerance)

Forward compatibility applies to individual fields as well as whole operations:

- **Envelope-level fields:** The op envelope schema explicitly allows additional
  properties (`additionalProperties: true`). Any unrecognized top-level property
  in `op.json` MUST be preserved and ignored.
- **Body-level fields:** An op whose type and version are known MAY contain
  unrecognized fields inside `body` (added by subsequent minor revisions or client
  extensions). Readers MUST preserve and ignore unrecognized fields within `body`.
- **Nested structures:** Unrecognized fields in nested objects and arrays within
  `body` MUST likewise be preserved and ignored.

An unknown field MUST NOT cause schema rejection, MUST NOT prevent an otherwise
interpretable op from folding, and MUST NOT alter the folded values of known fields.

## Fold behavior and materialized state

Fold is a pure, deterministic function (`ops in, state out`) that computes
materialized state from an object's op DAG. The presence of uninterpretable ops
governs fold as follows:

### Field isolation

An uninterpretable op **MUST NOT change or perturb the folded value of any field
a reader understands**.

Two readers with different capability levels evaluating the same DAG MUST arrive
at identical values for all mutually understood fields. Ordering rules, sequence
numbers, tiebreak counters, and state transitions MUST NOT be affected by the
interleaving of uninterpretable ops.

### Opaque record visibility

Folded state MUST surface uninterpretable ops as an explicit, opaque list rather
than skipping them silently. For each uninterpretable op encountered in an object's
DAG, the folded object MUST expose a record containing:

- `op_id`: the commit SHA of the op (carried as `commit` in Go structs and JSON serialization, matching `OpRef.commit`, because in Writ's commit-backed storage an operation's unique identifier is its git commit SHA)
- `object_type`: the object type string from the payload
- `op_type`: the op type string from the payload
- `op_version`: the integer version from the payload

No fields from `body` are exposed in the opaque record. Surfacing opaque ops
makes the presence of newer-client modifications observable in user interfaces
(e.g., displaying "3 operations from a newer version of Writ") and verifiable in
conformance goldens, without guessing at payload semantics.

Both populations of category 3 are surfaced through this one channel: an op with
an unknown type and an op whose body a strategy cannot consume are reported the
same way, because a reader's user can act on either only by looking at the raw
op. Widening the category widens what flows through the record; it does not
change the record.

`FC-5` below is the single definition of what that record carries. No other
section of this document and no section of `spec/fold.md` enumerates it: a
record specified in two places is a record that will be specified two ways.

### Stale vs. corrupt state

Ignoring an uninterpretable op produces a **stale view, never a corrupt one**.
For example, if a newer client closes a `widget` using a new op type that an
older client does not recognize, the older client continues to display the
`widget` as open (accompanied by the opaque op record). This trade-off is
deliberate: stale views are safe and honest, whereas guessing semantics causes
data corruption.

### Fault isolation

An uninterpretable op MUST NOT invalidate other ops in the DAG or cause the
containing object to fail. Fold MUST process all interpretable ops in the DAG
normally, regardless of how many uninterpretable ops are present.

## Version-bump semantics

Every op payload specifies an integer `op_version` (≥ 1). The evolution of op
schemas follows these rules:

1. **Additive changes do not bump version:** Adding new optional fields to an
   existing op type MUST NOT bump `op_version`. Unknown-field tolerance carries
   additive extensions transparently.
2. **Breaking changes bump version:** Any change that alters the meaning of an
   existing field, adds a required field, changes a default value, or modifies
   fold semantics MUST bump `op_version` to the next integer.
3. **Monotonic, per-type version space:** `op_version` is scoped to the
   `(object_type, op_type)` pair. Version numbers are strictly monotonic positive
   integers and MUST NEVER be reused. An `op_type` name MUST NEVER be repurposed
   for different semantics.
4. **Producer constraint:** A producer MUST NOT emit an `op_version` whose
   semantics it does not implement.
5. **No best-effort downgrade:** A reader that implements up to version *N−1*
   encountering a version *N* op MUST treat the op as uninterpretable (opaque).
   Readers MUST NOT attempt best-effort downgrade or interpret version *N* under
   version *N−1* rules. Best-effort downgrading is prohibited because it allows
   older clients to write or derive states that newer clients never authored.

## Round-trip preservation per leg

Preservation requires specific guarantees across all four engine subsystems:

- **Codec leg:** The unit of preservation is the raw payload blob bytes (`op.json`).
  Any decoding operation that will be followed by a re-emit (such as rebasing or
  re-signing commits) MUST retain the original payload bytes. Implementations
  MUST NOT re-serialize payloads from decoded structs with unknown fields dropped,
  as lossy re-serialization changes canonical bytes and invalidates commit signatures.
- **Fold leg:** Fold never writes to disk. Fold preservation requires that the
  reducer function neither errors nor terminates when encountering uninterpretable
  ops, and includes their opaque descriptors in the folded output.
- **Projection leg:** The SQLite projection is a droppable, rebuildable cache,
  never an authoritative store. Cache invalidation and rebuilds MUST reproduce
  the exact same folded state. Implementations MUST NOT prune uninterpretable ops
  from the projection and treat the pruned state as authoritative.
- **Sync leg:** Sync operates at the git transport layer over commit chains and
  refs. Sync transport MUST be type-blind: implementations MUST NOT filter,
  exclude, or rewrite ops during fetch or push based on op type, version, or
  interpretability.

## Targets a projection declines

A projection is a query surface, not a second source of truth, and its row
shape is narrower than the fold's. A conforming implementation MAY decline to
give one target key a queryable representation when its own storage cannot
express what the fold blesses — for instance a tabular projection pairing one
row per operation with one column per target, faced with an operation that
writes two body fields sharing one `append` target and so contributes two
entries to that target at a single position of $L$ (`spec/fold.md` §5's
canonical rule order).

These bounds are a refinement of `FC-13`, not free-standing rules, and carry
no `FC-n` of their own. `FC-13` requires a cache to be rebuildable from the
raw DAG without losing or mutating anything; what follows says what that
means when the fold is interpretable but the row shape is not wide enough to
hold it. They are deliberately not new numbered rules: every `FC-n` is cited
by an instance of the `forward-compat` corpus, and that corpus classifies one
operation at a time against the reader profile, where a declined target
changes no operation's disposition. `FC-13`'s row in §Normative rules summary
points here, so an implementer conforming against that table reaches these
bounds rather than finding the over-broad withhold permitted by silence.
`engine/projection`'s own tests are what pin them executably.

Declining is bounded:

- It MUST NOT change what the fold computes. Folded state read from
  the log is unaffected by what any cache can hold, and a reader going through
  the fold still sees every entry.
- It MUST NOT silently discard the declined target's written values.
  A body field bound to a declined target MUST be routed to the
  same "preserve and ignore" channel an unrecognized body field takes
  (§Unknown fields) rather than dropped on the floor. What that channel can
  hold is the channel's own limit, and is not a further licence to drop:
  where it is a per-object register keyed by body field — as the SQLite
  projection's `unknown_fields` column is — a declined target written by
  several operations retains the latest write per body field, not every
  write. The accumulated value the fold computes (every entry, ordered by $L$
  and, within one operation, by canonical rule order) is then reachable only
  through the fold. A consumer that needs every entry of a declined target
  MUST read it through the fold; for that target the projection is not a
  substitute, which is what "a query surface, not a second source of truth"
  amounts to here. An implementation MAY give a declined target a channel
  that keeps every entry, and none is required to.
- It MUST decline no more than the unrepresentable target itself.
  Withholding a whole object type — and with it every unrelated operation,
  target and row of that type — because one target is unrepresentable is not
  a decline but data loss on the query surface, and is prohibited. So is
  withholding a representable target because it shares a table, an envelope
  or any other implementation grouping with an unrepresentable one: a target
  the projection's own shape can express MUST materialize, and an operation
  that never writes the declined target MUST materialize normally.
- The decision MUST be a function of the schema alone, so that
  dropping and rebuilding the cache reproduces it exactly (`FC-13`).

A projection MAY still withhold a whole object type for a reason that is
genuinely about the type — a generated table or column name colliding with
one another type already owns, or a target key that is not a legal
identifier in the storage engine — since there is then no narrower unit to
withhold. That case remains covered by `FC-1`: the type's operations are
retained verbatim as uninterpretable rather than discarded.

## Explicit prohibitions ("Never drop")

To prevent data destruction across client generations, conforming implementations
MUST NOT:

1. Compact, garbage-collect, or prune ops from the DAG that they cannot interpret.
2. Strip or discard unknown fields when decoding and re-emitting operations.
3. Filter out uninterpretable ops or unknown object types during sync (push/fetch).
4. Abort fold or mark an entire collaborative object invalid because one or more
   ops are uninterpretable.
5. Guess semantics or apply fallback interpretation to unknown op types or future
   versions.

## Unknown object references (referential tolerance)

In an eventually-consistent, append-only distributed event log, operations
frequently reference collaborative objects that may not have arrived in the
local replica yet (for example, a `widget` referencing a newly created
`gadget` authored on an unmerged concurrent branch).

An op referencing an object ID not present in the local store MUST fold normally
without error or rejection (`FC-16`); readers MUST NOT drop or reject operations
due to unresolvable object references. The referencing state carries the
unresolved ID and automatically heals when the target object's operations
arrive. Rejection on unknown references is prohibited because attempting to
enforce cross-object referential integrity across independent append-only writer
logs would cause replicas with different fetch histories to compute diverging
state from the same operations, violating convergence.

## Normative rules summary

| Rule ID | Statement | Subsystem |
| --- | --- | --- |
| `FC-1` | An op envelope satisfying reader validation MUST be treated as valid-but-uninterpretable and retained in the DAG when the reader cannot interpret it — because its `object_type` is unknown, its `op_type` is unknown, its `op_version` is unimplemented, **or** its `(object_type, op_type, op_version)` triple is implemented but a field of its `body` carrying a declared merge rule holds a value that field's strategy cannot consume (`spec/fold.md` §7.1). In neither case may it be treated as malformed, and in neither case may it be folded field-by-field. | Reader / DAG |
| `FC-2` | Unknown top-level fields in `op.json` MUST be preserved byte-for-byte and ignored by readers. | Codec / Envelope |
| `FC-3` | Unknown fields inside `body` (including arbitrarily nested objects and arrays) MUST be preserved byte-for-byte and ignored by readers. | Codec / Payload |
| `FC-4` | Uninterpretable ops MUST NOT alter or perturb the folded value of any field understood by the reader. | Fold |
| `FC-5` | Folded state MUST surface each uninterpretable op as an opaque record containing `op_id` (carried as `commit` in Go structs and JSON serialization, matching `OpRef.commit`, because in Writ's commit-backed storage an op ID is its commit SHA), `object_type`, `op_type`, and `op_version`. | Fold / Projection |
| `FC-6` | Additive optional fields MUST NOT bump `op_version`. | Schema / Evolution |
| `FC-7` | Breaking schema changes, altered field semantics, changed defaults, or modified fold behavior MUST bump `op_version` to the next monotonic integer. | Schema / Evolution |
| `FC-8` | `op_version` numbers MUST be strictly monotonic per `(object_type, op_type)` and never reused; op type names MUST NOT be repurposed for different semantics. | Schema / Evolution |
| `FC-9` | Producers MUST NOT emit an `op_version` whose semantics they do not implement. | Producer |
| `FC-10` | Readers MUST NOT downgrade or guess semantics for an `op_version` higher than they implement; higher versions MUST be treated as uninterpretable. | Reader / Fold |
| `FC-11` | Decoders that re-emit ops MUST retain and write back the exact original payload bytes rather than re-serializing lossy parsed structs. | Codec |
| `FC-12` | Fold MUST NOT error, crash, or abort when encountering uninterpretable ops. | Fold |
| `FC-13` | Projection caches MUST be fully rebuildable from the raw DAG without losing or mutating uninterpretable ops. Where a projection's row shape cannot express a target the fold computes, §Targets a projection declines refines this rule and bounds the decline: it MUST NOT change what the fold computes, MUST route the declined target's body fields to the unknown-field channel rather than discard them, MUST decline no more than the unrepresentable target itself — never a whole object type, and never a representable target that merely shares a table, envelope or other implementation grouping with it — and MUST decide it from the schema alone so a rebuild reproduces it. | Projection |
| `FC-14` | Sync transport MUST NOT filter, prune, or inspect ops based on op type, version, or interpretability. | Sync |
| `FC-15` | An uninterpretable op MUST NOT invalidate or fail the containing collaborative object or other valid ops in the DAG. | Engine / DAG |
| `FC-16` | An op referencing an object ID not present in the local store MUST fold normally without error; readers MUST NOT drop or reject operations due to unresolvable object references. The referencing state carries the unresolved ID and heals when the target object's operations arrive. | Fold / Projection |

## Conformance fixtures

The `forward-compat` fixture family in `spec/fixtures` provides the executable,
repository-level conformance test suite for rules `FC-1` through `FC-5` and `FC-11`
through `FC-15`. Multi-writer git DAGs containing unknown op types, future op versions,
and unrecognized fields are executed against the reader capability profile and
verified for byte-for-byte preservation and field isolation.

That family covers the unknown-type half of `FC-1`. The other half — a known op
whose body a strategy cannot consume — is covered in the two other places
conformance data lives:

- `spec/testdata/forward-compat/ops/uninterpretable-body.json` carries the
  disposition itself: a `create` v1 op, in the reader profile and beyond
  reproach at the envelope, whose `title` holds `null`. Its declared disposition
  is `opaque`, and the harness derives that by folding the body and reading the
  quarantine channel rather than by re-reading the envelope. These instances are
  hand-authored, which is what lets them state a body no conforming producer
  writes — as `unknown-object-type.json` states an `object_type` none emits.

  Its index entry also carries `envelope_disposition`, which the other entries
  do not. An envelope classifier decides only `FC-1`'s type leg; deciding the
  body leg needs the governing schema's merge rules and a fold. Where the two
  legs disagree the index states both, so a reader that classifies envelopes
  and a reader that folds can each be measured against the half they answer,
  and neither is scored on the other's.
- The `uninterpretable-*` merge vectors under `spec/testdata/fold/merge/` carry
  the effect: each asserts both the folded state and the quarantine list,
  against the reference fold and the engine alike.

The repository-level family carries neither, because its fixtures are built by
Writ's own producer, which validates bodies before signing
(`spec/op-envelope.md` §Producer validation) and so cannot emit such an op.

