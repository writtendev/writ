# Schema Operations — writ's one hard-coded object type (v1)

Status: **normative**. Schema: [`schemas/schema-ops.schema.json`](schemas/schema-ops.schema.json).
Field rules: [`testdata/schema-ops/field-rules.json`](testdata/schema-ops/field-rules.json).

This document defines the operation vocabulary, payload schemas, fold
semantics, and bootstrap order for schema objects in Writ
(`object_type: "schema"`). `schema` is the one object type Writ hard-codes
(ARCHITECTURE.md §Object types, §Schema layer, WRIT-184 decision 4): a
schema has to exist before anything else can be typed, so it is the single
permitted exception to "every collaborative object type is declared by a
schema." Every other type — `review`, `comment`, `issue`, and the rest of
the shipped SDLC vocabulary today, and any consumer-declared type tomorrow
— is data a `schema` object writes into the log.

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

---

## 1. Scope & Object Model

A `schema` object declares one namespace and, within it, the object types a
repository uses: their fields, the merge strategy and value type each field
folds through, and the op types that carry them. Schemas are collaborative
objects like everything else — signed, append-only, folded from ops on a
per-writer chain (`refs/writ/<writer-id>/schema`) — so authoring one needs
no privileged access beyond an ordinary push, and evolving one needs no
migration tooling beyond appending more ops.

The schema DSL declares **types, value types, merge strategies, and
relations. Nothing else** (ARCHITECTURE.md §Schema layer). This document
specifies the op vocabulary that carries those declarations; it defines no
computed fields, hooks, expressions, permissions, or logic in defaults —
that fence is a house rule, not a limitation of this version, and framework-
building is a bug even one field at a time.

### 1.1. Namespacing

A schema's namespace disambiguates independent authors: two authors can
each publish a schema without coordinating names, because each schema
carries its own namespace. Namespace is a property of the schema object
(set by `create`, §3.1) and never reaches the wire — see §2 for what does.

### 1.2. Scope boundaries

- **No DSL, no plan/apply, no CLI.** This document specifies the op
  vocabulary and its fold; `writ.schema` (the working-tree source form) and
  its parser, `writ schema plan`/`writ schema apply`, and any CLI surface
  are separate work. Tests and fixtures construct `codec.Op` values
  directly, exactly as `engine/state`'s own tests do for every other
  vocabulary.
- **No projection tables.** Schema ops land in the projection's existing
  `unknown_ops` bucket, exactly like any object type the resolved rule
  index does not declare a table for. `Store.Schema` (below) folds from
  the DAG, not the projection, so nothing depends on projection support
  existing.

---

## 2. Namespace and `object_type` Binding

`object_type` on the wire stays the existing bare lowercase form
(`spec/op-envelope.md`): `^[a-z][a-z0-9-]*$`, at most 64 characters, no
slash and no namespace prefix. A schema's namespace is part of the schema
object itself (§1.1) and is never concatenated onto `object_type`; ref
layout and chain naming are unaffected by any of this.

**The load-bearing invariant is `object_type` uniqueness across every
schema object in a repository, not namespace uniqueness.** Because
namespace never reaches the wire, two independent authors can each declare
a type named `standup` under different namespaces, and both would bind the
bare wire `object_type: "standup"` — that is the collision that produces
two candidate rule sets for one type and has to be defined. Namespace
uniqueness is a second, weaker check, reported alongside the first but
never withholding rules on its own (§6).

On an `object_type` collision, **no winner is picked**: neither schema
object's rules are installed for the contested type. Its ops fall through
the absent-schema path to `UnknownOp`, reusing the existing forward-
compatibility mechanism (§5) rather than a new one.

---

## 3. Envelope Binding

Every schema operation is carried in a git commit whose `op.json` payload
conforms to `spec/schemas/op-envelope.schema.json` and
`spec/schemas/schema-ops.schema.json`:

- `object_type` MUST be `"schema"`.
- `op_version` MUST be an integer ≥ 1. This document specifies version `1`.
- `object_id` MUST be a non-empty printable-ASCII string identifier
  (1–256 characters, `^[\x21-\x7e]+$`), per `spec/op-envelope.md`. Unlike
  `settings` (`spec/settings-ops.md` §1.2), there is no well-known
  canonical object ID: a repository MAY have any number of schema objects,
  each authored independently, so any envelope-legal `object_id` is
  conforming.
- `op_type` MUST be one of the operation types defined below (§4), or an
  unknown string tolerated under forward-compatibility rules.
- `body` MUST be a JSON object conforming to the schema for the declared
  `op_type` and `op_version: 1`.

### 3.1. Key components are strings on the fold path

Several body fields below are **key components** of a `keyed-lww` register:
`type`, `op_type`, `op_version`, and `field`. Writing `op_version` as a
JSON integer works nowhere it is a key component, because
`ruleAccepts` (`engine/internal/fold/reject.go`) makes an operation
uninterpretable if any declared `keyed-lww` key column holds a non-string
value. This is a deliberate encoding, not an oversight:
`define-field` and `define-op` bodies carry `op_version` — the version of
the *declared* `op_type`'s body, i.e. the vocabulary being described, not
the version of this `define-field`/`define-op` op itself — as a **decimal
string** (`"op_version": "1"`), matched by a `key_types` entry of `string`
wherever it appears in a key.

**This canonical form is checked at fold time, not only by the payload
schema.** `spec/schemas/schema-ops.schema.json`'s `op_version_string`
pattern (`^[1-9][0-9]*$`) rejects a non-canonical `op_version` — a leading
zero, a non-digit character, or an empty string — before a conforming
producer ever writes one (`spec/testdata/schema-ops/invalid/define-field-op-version-leading-zero.json`
pins the rejection). That check is producer-side, and a non-conforming
peer's ref can still carry `"op_version": "01"` regardless of it. **A
`define-op`, `define-field`, or `deprecate-field` op whose `op_version`
body field is not this canonical decimal encoding is uninterpretable, and a
conforming fold MUST report it through `UnknownOps` rather than fold it
in.** Without this check, two declarations differing only in a
non-canonical `op_version` encoding — `"1"` and `"01"`, both denoting the
same integer — would resolve to the same declaration key once compared
numerically and collapse onto one, and which of the two survived would
depend on input order rather than on the ops themselves: the fold is
required to be pure and deterministic (`spec/fold.md`), and this
particular nondeterminism is hostile-writer-controllable, since any peer
can push the colliding ref. The check runs independently of, and in
addition to, the payload schema — a reader has no producer step to lean on
and cannot assume the log it reads was ever validated.

This quarantine belongs to the schema object's own typed materialization,
one layer above the generic `keyed-lww` strategy `spec/fold.md` §7.1
defines: that strategy's own uninterpretability rule ("key components are
strings") is satisfied by any string, canonical or not, so
`spec/testdata/fold/merge/`'s vectors — which drive the generic `Fold(ops,
rules)` over already-resolved `FieldRule`s and compare key components as
opaque strings — cannot exercise a check that depends on interpreting one
key component's *numeric* value, and this rule has no representation in
that corpus. That gap is about this specific corpus, not about fixtures
generally: `spec/fixtures/`'s signed-fixture golden families drive typed
reducers directly — `spec/fixtures/settings_test.go`'s family drives
`writ.FoldSettings` and golden-pins its output byte-for-byte — and the
`schema` family (`spec/fixtures/schema_test.go`) is built the same way and
pins this exactly: `schema-op-version-non-canonical.yaml` carries a
non-canonical `op_version` at all three affected op types, and its golden
(`spec/fixtures/testdata/golden/schema/schema-op-version-non-canonical.json`)
records the surviving canonical declarations alongside every quarantined op
in `unknown_ops`.

### 3.2. Distinct `target` per op type

`define-field` and `define-op` bodies both carry `type`, `op_type`, and
`op_version`; without distinct `target` state keys they would collide on
one fold accumulator with different key arities (`define-field` is keyed
on four components, `define-op` on three). Every `define-field` rule
targets a `field_*` state key; every `define-op` rule targets an `op_*`
state key; every `define-type` rule targets a `type_*` state key. `create`
keeps bare `namespace` / `description` state keys, since a schema object
has exactly one `create`-scoped register set.

---

## 4. Operation Vocabulary (`op_version: 1`)

The `schema` family defines six operation types:

| `op_type` | Body Schema | Description |
| --- | --- | --- |
| `create` | `{"namespace": string, "description"?: string}` | Initializes the schema object and its namespace. |
| `define-type` | `{"type": string, "description"?: string}` | Declares an object type: its bare wire name and description. |
| `define-field` | `{"type": string, "op_type": string, "op_version": string, "field": string, "value_type"?: string, "enum"?: [string], "max_length"?: int, "strategy": string, "key"?: [string], "key_types"?: {string: string}, "lattice"?: [string], "target"?: string}` | Declares a field on a type: the op that writes it, its merge strategy, and (orthogonally) its value type. |
| `define-op` | `{"type": string, "op_type": string, "op_version": string, "description"?: string}` | Declares an op type within a type's vocabulary. |
| `deprecate-type` | `{"type": string, "deprecated": true}` | Tombstone-style: marks a declared type deprecated. |
| `deprecate-field` | `{"type": string, "op_type": string, "op_version": string, "field": string, "deprecated": true}` | Tombstone-style: marks a declared field deprecated. |

Nothing is ever removed from the log: `deprecate-type` and
`deprecate-field` write `deprecated: true` into the *same* keyed-lww
register a `define-type`/`define-field` op is scoped by (§5), which is what
makes them tombstone-style without the per-object `tombstone` strategy —
that strategy answers "is this whole object deleted", a different
question from "is this one declared field or type still current."

### 4.1. `create`

Initializes a schema object and sets its namespace.

```jsonc
{
  "object_id": "sch-acme",
  "object_type": "schema",
  "op_type": "create",
  "op_version": 1,
  "body": {
    "namespace": "acme",
    "description": "Acme's SDLC vocabulary"
  }
}
```

- `namespace` (string, required): `^[a-z][a-z0-9-]*$`, at most 64
  characters. Folds `create-once` (§5): the first namespace any `create`
  op sets is permanent for this schema object.
- `description` (string, optional): Human-readable description. Folds
  `lww`.

### 4.2. `define-type`

Declares an object type.

```jsonc
{
  "object_id": "sch-acme",
  "object_type": "schema",
  "op_type": "define-type",
  "op_version": 1,
  "body": {
    "type": "standup",
    "description": "A daily standup update"
  }
}
```

- `type` (string, required): The bare wire `object_type`
  (`^[a-z][a-z0-9-]*$`, at most 64 characters) — the same grammar
  `spec/op-envelope.md` gives `object_type`, because this value is used
  verbatim as one.
- `description` (string, optional).

A type need not be declared by `define-type` before a `define-field` or
`define-op` names it: nothing requires `define-type` to precede the
others, and a field or op declaration MUST NOT be dropped for lacking one
(§6).

### 4.3. `define-field`

Declares one field on one op's body for one type.

```jsonc
{
  "object_id": "sch-acme",
  "object_type": "schema",
  "op_type": "define-field",
  "op_version": 1,
  "body": {
    "type": "standup",
    "op_type": "approval",
    "op_version": "1",
    "field": "verdict",
    "value_type": "enum",
    "enum": ["approve", "block"],
    "strategy": "keyed-lww",
    "key": ["subject"],
    "key_types": { "subject": "person-ref" }
  }
}
```

- `type` (string, required): The declaring object type's bare wire name.
- `op_type` (string, required): The op type, within `type`'s vocabulary,
  whose body carries this field.
- `op_version` (string, required): The op version, as a decimal string
  (§3.1).
- `field` (string, required): The body field this rule governs,
  `^[a-z][a-z0-9_]*$`, at most 64 characters.
- `value_type` (string, optional): One member of the closed value-type
  catalogue (`spec/value-types.md`). A rule declaring none is untyped, the
  same no-implicit-behavior idiom `spec/fold.md` §5 states for an
  undeclared strategy.
- `enum` (array of strings, optional): Required iff `value_type ==
  "enum"`; forbidden otherwise.
- `max_length` (integer, optional): Only meaningful when `value_type` is
  `string` or `text`.
- `strategy` (string, required): One member of the closed merge-strategy
  catalogue (`spec/fold.md` §5).
- `key` (array of strings, required iff `strategy == "keyed-lww"`): The
  ordered composite key.
- `key_types` (object, required iff `strategy == "keyed-lww"`): Maps each
  `key` column to its value type; MUST cover exactly the columns `key`
  declares.
- `lattice` (array of strings, required iff `strategy == "lattice"`): The
  ordered semilattice elements.
- `target` (string, optional): The state key this field's register lands
  under (`spec/fold.md` §5, §7 below).

These cross-field consistency rules mirror `spec.ValidateFieldRule`
exactly (`spec/fieldrules.go`) — the same function that validates every
other vocabulary's `field-rules.json` — because a `define-field` op *is* a
`FieldRule` in waiting (§7).

### 4.4. `define-op`

Declares an op type within a type's vocabulary.

```jsonc
{
  "object_id": "sch-acme",
  "object_type": "schema",
  "op_type": "define-op",
  "op_version": 1,
  "body": {
    "type": "standup",
    "op_type": "approval",
    "op_version": "1",
    "description": "Approve or block a standup"
  }
}
```

- `type` (string, required).
- `op_type` (string, required): The op type being declared.
- `op_version` (string, required): Decimal string (§3.1).
- `description` (string, optional).

### 4.5. `deprecate-type`

```jsonc
{
  "object_id": "sch-acme",
  "object_type": "schema",
  "op_type": "deprecate-type",
  "op_version": 1,
  "body": { "type": "standup", "deprecated": true }
}
```

- `type` (string, required).
- `deprecated` (boolean, required, `true`): Producers MUST write `true`;
  there is no `deprecate-type`-family op that clears it in this version.

### 4.6. `deprecate-field`

```jsonc
{
  "object_id": "sch-acme",
  "object_type": "schema",
  "op_type": "deprecate-field",
  "op_version": 1,
  "body": {
    "type": "standup",
    "op_type": "create",
    "op_version": "1",
    "field": "summary",
    "deprecated": true
  }
}
```

- `type`, `op_type`, `op_version`, `field` (all required): Identifies the
  declaration being deprecated, matching a `define-field`'s key exactly.
- `deprecated` (boolean, required, `true`).

---

## 5. Fold Semantics

Every declared field folds through the closed strategy catalogue
(`spec/fold.md` §5) exactly as any other collaborative object's fields do
— schema needs no new strategy and no new value type. `define-type`,
`define-field`, and `define-op` are `keyed-lww` registers; `deprecate-type`
and `deprecate-field` write `deprecated: true` into the *same* key,
which is what makes them tombstone-style without needing the `tombstone`
strategy (that strategy is per-object, not per-key). `create` is
`create-once` for `namespace`, `lww` for `description`.

`deprecated: true` marks a declaration as discouraged for *new* writes; it
is not a removal, and it must never stop already-signed ops from folding.
A deprecated field's rule stays installed and active for every object of
the type it governs: `engine/schema.go`'s resolver carries `deprecated`
through onto the resolved rule as data — for a producer or UI to read, or
to warn against writing more of — and never withholds installing the rule
because of it. Nothing about deprecating a field changes what an op
written under it folds to, before or after the deprecation (§7 step 3,
§8). This is the same "nothing is ever removed" guarantee stated for the
op vocabulary above, made explicit at the resolver boundary where it would
otherwise be easy to read `deprecated` as a filter.

| `op_type` | Field | Merge Strategy | Key |
| --- | --- | --- | --- |
| `create` | `namespace` | `create-once` | — |
| `create` | `description` | `lww` | — |
| `define-type` | `type` | `keyed-lww` | `[type]` |
| `define-type` | `description` | `keyed-lww` | `[type]` |
| `deprecate-type` | `deprecated` | `keyed-lww` | `[type]` |
| `define-op` | `op_type` | `keyed-lww` | `[type, op_type, op_version]` |
| `define-op` | `description` | `keyed-lww` | `[type, op_type, op_version]` |
| `define-field` | `field`, `value_type`, `enum`, `max_length`, `strategy`, `key`, `key_types`, `lattice`, `target` | `keyed-lww` | `[type, op_type, op_version, field]` |
| `deprecate-field` | `deprecated` | `keyed-lww` | `[type, op_type, op_version, field]` |

The full, machine-readable table — including each field's `target` state
key and `value_type` — is `testdata/schema-ops/field-rules.json`.

Folded schema state is `Schema{object_id, namespace, description, types[],
unknown_ops}`, where each `SchemaType{type, description, deprecated,
fields[], ops[]}` groups every `SchemaField` and `SchemaOp` declared for
it. A type surfaces once it is named by *any* of `define-type`,
`define-field`, or `define-op` — nothing requires `define-type` to precede
the others.

---

## 6. Conflicts

Reading any object requires first folding the `schema` objects present in
a repository and resolving them into per-`object_type` rule sets. Two
kinds of conflict can arise, and neither is picked a winner:

1. **`object_type` collision** (§2): two schema objects both bind the
   same bare `object_type`. Withholding rules for the contested type is
   the whole remedy — no new fold rule, no new mechanism. The ops of that
   `object_type` fall through the absent-schema path (§7.1) to `UnknownOp`,
   exactly as if no schema had ever declared it. `schema` itself cannot be
   redefined this way: a `define-type` naming `schema` from within the log
   is always a conflict, never installed, because `schema` is the engine's
   one hard-coded exception (§1).
2. **Namespace collision**: two schema objects declare the same namespace.
   A weaker, mostly cosmetic case — reported alongside an `object_type`
   collision when both occur, but on its own it withholds nothing.

A conflict is **resolver output, not fold output**. `Fold(ops, rules) →
ObjectState{ObjectID, ObjectType, TotalOrder, State, UnknownOps}` is
per-object and pure; a collision between two *different* schema objects is
cross-object and has no home in one object's `ObjectState`. It is the
`engine/schema.go` resolver, described in §7, that returns conflicts as
data alongside the rule index it builds.

---

## 7. Bootstrap

Stated once, as the single permitted exception: `schema` is the only
object type whose rules never come from the log.

1. Enumerate ops from `refs/writ/*` and group by `object_id` — existing
   DAG-layer machinery (`spec/ref-layout.md`); `object_type` rides the
   payload, so no schema is needed to find schema objects. There is no
   chicken-and-egg at the DAG layer.
2. Fold every group whose `object_type == "schema"` with the engine's
   built-in table (`state.SchemaRules`) — the only rules that exist for
   `schema`, and the only object type that never consults the log.
3. Materialize `spec.FieldRule`-shaped rules from the folded `field_*`
   registers of every field declaration, deprecated or not: `deprecated`
   is carried onto the resolved rule as data, not a filter. A rule is
   never withheld for being deprecated (§5, §8).
4. Validate each candidate rule through `spec.ValidateFieldRule`. A rule
   that fails is dropped and reported, never handed to the fold driver
   (§9).
5. Index surviving rules by bound `object_type`, visiting schema objects
   in ascending `object_id` order, so two conforming implementations build
   the same index from the same input regardless of enumeration order.
   Detect and withhold collisions (§6).
6. Fold every other object with the rules resolved for its `object_type`.

### 7.1. Absent schema is not a fold error

`Fold(ops, rules)` called with rules that match nothing already marks
every op `known = false`, emits them all as `UnknownOps`, builds no
accumulators, and returns a `nil` error — that is exactly the required
behavior for an `object_type` no schema (yet) defines, and it needs no new
code. This is covered by the existing forward-compatibility rules `FC-1`
("because its `object_type` is unknown") and `FC-12`
(`spec/forward-compatibility.md`); this document does not restate them,
and no edit to `spec/forward-compatibility.md` is expected. The existing
forward-compatibility corpus stays untouched and green — it is the
regression net for this rule.

---

## 8. Evolution

Field rules are already keyed by `(op_type, op_version, field)`, so
changing a field's merge strategy is a **version bump, not an edit**: ops
written under `op_version` 1 keep folding under `op_version` 1's rules,
and ops written under `op_version` 2 fold under `op_version` 2's, whatever
order they arrive in. There is no destructive schema change, no planner
that flags one, and no migration path — the only answer consistent with
the no-rewriting-history rule.

This needs one qualification the generic fold's own mechanics impose
(`spec/fold.md` §5): `Fold` groups matched rules by **target key alone**,
not by `op_version`, and instantiates one accumulator per target from the
first of its matching rules in canonical rule order (ascending `(op_type,
op_version, field)`), which for a version bump of one `(op_type, field)`
is the lowest `op_version`. Two rules that share a target and a strategy
are indistinguishable to the accumulator regardless of which one is
"first" — so a version bump:

- **MAY freely change** `value_type`, `enum`, `max_length`, `key`, or
  `key_types` while keeping the same `target` (or omitting `target`,
  which defaults to `field`): the accumulator factory selected is the
  same either way, because these attributes are not consulted by the
  strategy at fold time (`spec/value-types.md` §Producer-side and
  reader-tolerant: nothing on the read path calls the value-type
  validator). `lattice` is deliberately absent from this list: the
  `lattice` accumulator reads it to order its semilattice, so it *is*
  consulted at fold time. A version bump reusing a `target` therefore
  **MUST still agree on `lattice`**, exactly as the cross-`op_type`/
  cross-`field` case below requires — two same-strategy `lattice` rules
  sharing a target but declaring different orderings are exactly as
  order-dependent as two rules disagreeing on `strategy` itself, version
  bump or not. `spec.CheckTargetCollision` (`spec/fieldrules.go`) enforces
  this inside the carve-out, not only outside it; the resolver-level
  consequence — the second rule dropped as a `SchemaConflict` and an op
  written under it becoming an `UnknownOp` — is pinned by
  `spec/fixtures/testdata/descriptions/schema-driven-version-bump-lattice-collision.yaml`.
- **MUST declare a distinct `target`** when it changes `strategy`: reusing
  a target across a strategy change makes the older version's strategy
  silently run over the newer version's writes, since canonical rule order
  hands the accumulator the lower `op_version`'s rule — neither rule's
  declared behavior. `engine/schema.go`'s resolver enforces this: a version
  bump that changes `strategy` while reusing a `target` already bound to a
  different strategy is rejected — the rule is dropped, not installed, and
  reported (§9) alongside an `object_type` collision.

The old and new strategy consequently land under different state keys.
`testdata/fold/merge/schema-version-bump-same-target.json` and
`schema-version-bump-new-target.json` pin both halves of this rule.

The "MAY freely change" bullet is bounded to exactly this case — the same
`op_type` and `field`, differing only by `op_version` — and no wider. Two
rules that share a `target` (declared, or defaulted from `field`) without
being a version bump of one another — two different `op_type`s, or two
different `field`s, that happen to reuse the same target — MUST agree on
`value_type`, `key`, `key_types`, `enum`, `max_length` and `lattice` too,
not only on `strategy` (`spec/fold.md` §5). Two OR-set halves that merely
happen to share a body field name (`add`, `remove`) but mean different
logical state — one op type's assignee set and an unrelated op type's
label set, say — are exactly the shape this catches: both would agree on
`strategy` (`set-observed-remove`) while disagreeing on `value_type`, so a
table that lets them collide on an undeclared shared target is
non-conforming and MUST target each one explicitly instead
(`spec/review-ops.md`'s `assignees`/`labels` split is the worked example).
The resolver enforces this alongside the strategy-only case above, and so
does `engine/schemasrc`'s compiler for a `writ.schema` source file before it
ever reaches the log.

### 8.1. Clearing a field attribute (decided, WRIT-200)

`define-field`'s body carries `value_type`, `enum`, `max_length`, `key`,
`key_types`, `lattice`, and `target` only when set (§4), and each is its
own independent `keyed-lww` register at `(type, op_type, op_version,
field)` — nine distinct targets, one per attribute
(`testdata/schema-ops/field-rules.json`; §5's table shows one row for
`define-field` as a whole, not one per attribute) — overwritten only when
a later op's body actually carries that key. There is no vocabulary that
clears one: an op whose body omits `enum` does not remove a previously
declared `enum`, it leaves the existing register standing, forever
(`testdata/fold/merge/schema-narrow-field-attribute-not-cleared.json`
pins exactly this — a redeclaration that drops `enum` overwrites
`value_type` but never touches `field_enum`, which keeps the original
value across the whole fixture).

This is asymmetric. **Widening** — declaring an attribute a field didn't
have, `max_length` on a previously-unbounded `string` say — is an
ordinary redeclaration under the same `op_version`: the new op's body
carries the key, so it's an ordinary `keyed-lww` overwrite and applies
cleanly, no version bump needed. **Narrowing** — dropping an attribute a
field already has — has no representation: append-only means there is no
op that clears a register once written, and the vocabulary defines none
for this. It was found as the underlying gap behind WRIT-191's refusal:
`writ schema apply` detects exactly this case (comparing the log's folded
state against the file's full compiled sequence) and refuses to append
rather than silently leave the log holding both the file's new
declaration and the stale one it was meant to replace.

Three ways to close the gap were weighed: emit every attribute
unconditionally (explicit nulls for absent ones), add a dedicated
clearing op, or accept the limitation. The first two change the wire
vocabulary — new shapes for every `define-field` body or new op
vocabulary, either way touching this document, the JSON schema,
`testdata/schema-ops/field-rules.json`, `state.SchemaRules`, and fixtures
together, and a clearing op additionally needs a story for what clearing
means under concurrent writers (a clear racing a redeclaration is a new
conflict shape this document does not otherwise have). That cost would
buy a capability the recipe below already delivers without a wire
change: **the decision is to accept it.** Narrowing an attribute is not a
new problem — it is the same "nothing is ever removed" constraint this
section already states for a `strategy` change (§8 above) — so it takes
the same recipe: declare the field again under a **new `op_version`**.

Whether that new declaration also needs a **distinct `target`** follows
§8's own two bullets, applied to narrowing instead of to an ordinary
redeclaration. `value_type`, `enum`, and `max_length` never make a shared
`target` order-dependent: `enum` and `max_length` are validation-only and
the fold never reads them, and `value_type` is read off the *matched* rule
on every op rather than captured when the target's accumulator is built.
Narrowing one of them is therefore a version bump under the *same*
`target` — `string(200)` narrowed to `string` is `op create 2` declaring
`title` unbounded again, reusing `target: title` (or omitting it, which
defaults to the same place `op_version` 1 uses).

`key` and `key_types` read like they belong in that group — §8's MAY
bullet names both — but narrowing either, in the sense this section
means (a redeclaration whose body stops carrying the attribute at all,
leaving the log holding a register the file no longer describes), is
unreachable without also changing `strategy`: both are required exactly
when `strategy` is `keyed-lww` and forbidden otherwise
(`spec/fieldrules.go`'s `ValidateFieldRule`), so a redeclaration that
drops `key` has, by construction, also stopped declaring `keyed-lww`.
That is the `strategy`-change case §8's second bullet already governs,
not the same-target case its first bullet grants for this section's
narrowing scenario. The entailed `strategy` change is the whole reason,
and nothing about `key`/`key_types` themselves adds to it: exactly like
`value_type`, they are read from the *matched* rule on every `Apply`
(`engine/internal/fold/strategy.go`'s `keyedLWWAccumulator.Apply` builds
the composite key from `rule.Key` and `rule.KeyTypes`) rather than being
captured when the accumulator is instantiated, so two `keyed-lww` rules
sharing a target and differing only in them are not order-dependent.
§8's MAY bullet is correct as stated and stays correct: it covers a
version bump that keeps `key`/`key_types` present and changes their
*value* — narrowing which columns compose the key while staying
`keyed-lww` — which never reaches this section's clearing case, because
the attribute is never absent from the body, only different.

`lattice` is the remaining attribute §8 excludes from its MAY bullet,
and its reason is the other one: `newLatticeAccumulator`
(`engine/internal/fold/strategy.go`) builds its rank map once, when
`Fold` instantiates one accumulator per target from whichever matched
rule the slice lists first, so that rule's ordering governs every op at
the target and two rules sharing it while disagreeing on `lattice` are
exactly as order-dependent as two disagreeing on `strategy` — version
bump or not (WRIT-206; `spec/fieldrules.go`'s `equalMergeAttrs`, which
is why `lattice` alone is never skipped for a version bump). Narrowing
`lattice` therefore needs a distinct target. Narrowing `target` itself
is the trivial case — reverting to the field-name default is already a
target change.

`writ schema apply`'s refusal message follows the same split
(`cmd/writ/schema.go`): it asks for a distinct target only when the
narrowed attribute is `key`, `key_types`, `lattice`, or `target`, and
for a plain `op_version` bump otherwise. The old, wider declaration is
not deleted — nothing is — it stays live for whatever already writes
`op_version` 1, exactly as any other version bump leaves the superseded
version folding on unaffected.

---

## 9. Rule Validation

Rule validation is the security boundary for a rule sourced from the log,
not the fold path. The fold path performs **no** value-type checking
(`spec/value-types.md` §Producer-side and reader-tolerant), and
`ruleAccepts` (`engine/internal/fold/reject.go`) treats an `lww` field as
"any non-null JSON value." So a `define-field` op carrying `strategy: ""`
or `strategy: "bogus"` folds cleanly into schema state — `Uninterpretable`
has no opinion on a string value at a `keyed-lww`-typed field, because
schema's own fold rules type `strategy` as `string`, not as a member of
the strategy catalogue — and handing that value to `Fold` as a rule would
reach `NewAccumulator` (`engine/internal/fold/strategy.go`), which returns
a hard `fold: unknown strategy ""` error. That would violate the
uninterpretable-operation contract (`spec/fold.md` §7.1) for the
*consuming* object, not the schema object itself.

`engine/schema.go`'s resolver closes this: every candidate rule is
validated through `spec.ValidateFieldRule` — the same function every other
vocabulary's `field-rules.json` is validated through on load — before it
can be installed. A rule that fails is dropped and reported as a conflict
(§6) rather than reaching `Fold`. This is why the resolver lives in package
`writ` (`engine/schema.go`, which already imports `spec`) and not in
`engine/internal/fold`: that package's import allowlist does not include
`spec`, and keeping fold pure is a house rule, not a convenience.

---

## 10. Forward Compatibility

- **Unknown body fields:** Conforming implementations MUST preserve and
  ignore any unknown fields in `schema` op bodies.
- **Unknown `op_type` or future `op_version`:** Ops with unknown op types
  or unsupported versions for `object_type: "schema"` MUST remain in the
  DAG and contribute to total ordering (`t*`) and ancestry, but contribute
  no field mutations to known schema state.
- **Absent schema for an encountered `object_type`:** See §7.1.

---

## 11. Producer Validation

`spec/op-envelope.md` §Producer validation's rules 3 and 4 resolve
"the schema object governing `object_type`" through a five-tier
precedence; this section states what that means for the resolver this
document already specifies (§7's bootstrap, §6's collision pass) rather
than restating the precedence itself (WRIT-188).

- **The same resolved rules drive the write path.** §7 already resolves
  every folded `schema` object into a per-`object_type` rule index for
  reading; the producer path (`spec/op-envelope.md` rule 3/4) is driven
  from the same resolution — not a second, independently derived one —
  so the two can never disagree about what a repository's schema
  declares. A repository whose log narrowly declares a type this engine
  also embeds a vocabulary for (`issue`, say) has that narrower
  declaration bind its own producer exclusively (`spec/op-envelope.md`
  tier 2 outranks tier 3): a field the embedded vocabulary would accept
  but the log schema does not declare is refused, loudly, naming the
  schema object responsible. That is intended, not a bug to route around
  — a caller-visible shape sourced from an engine-embedded table rather
  than the schema actually in the log is exactly what this project's own
  house rules call a finding.
- **A contested `object_type` withholds reads but not writes.** §6
  withholds fold rules for a contested type; nothing about that requires
  withholding the write. The producer permits an op of a contested type
  unvalidated (`spec/op-envelope.md`'s tier 4) precisely because refusing
  it would be a *permanent* write outage — a contested `object_type` is
  contested forever, since nothing is ever removed from the log — while a
  reader degrading to `UnknownOp` is not: it is exactly the same
  degradation an object with no schema at all already gets (§7.1), fully
  recoverable the moment the contest itself is. The asymmetry is the
  point, not an oversight: a producer can retract nothing it has already
  signed, so the fence is on the side where a mistake is undoable.
- **Declarations are grammar-gated like field rules.** §9's rule
  validation gate — every candidate rule passed through
  `spec.ValidateFieldRule` before it reaches `Fold` — checks a great deal
  about a `define-field` rule, but not the `op_type` grammar itself
  (`ValidateFieldRule` only checks it is non-empty). A schema-declared
  `op_type` failing `spec/op-envelope.md`'s envelope grammar
  (`^[a-z][a-z0-9-]*$`, at most 64 characters) could never be written
  through the ordinary envelope path regardless, so this is not a new
  security boundary — it buys a clearer rejection and a rule index that
  is never keyed by an unwritable `op_type`, and it applies to a
  `define-op` declaration exactly as it does to `define-field`'s.

---

## Conformance Data

- `spec/schemas/schema-ops.schema.json` — the payload schema.
- `spec/testdata/schema-ops/valid/`, `spec/testdata/schema-ops/invalid/` —
  payload instances; `invalid/index.json` records each expected rejection
  (`schema`, `invariant`, or `canonicalization`).
- `spec/testdata/schema-ops/field-rules.json` — the bootstrap table,
  normative, byte-for-byte the published form of `state.SchemaRules()`.
- `spec/testdata/fold/merge/schema-*.json` — fold vectors: a bootstrap
  fold of a whole schema object, a `deprecate-field`/redeclare
  interleaving, concurrent `define-field` ops on one keyed-lww key, the
  two `target`-remedy vectors from §8, and §8.1's narrowing vector (a
  redeclaration that drops `enum` leaves `field_enum` standing while
  overwriting `field_value_type`). These drive the generic `Fold(ops,
  rules)` over already-resolved `FieldRule`s, so they cannot pin §3.1's
  fold-time `op_version` canonicalization quarantine (that check depends on
  interpreting a key component's numeric value, one layer above what the
  generic `keyed-lww` strategy's own uninterpretability rule requires); see
  §3.1 for why. The signed-fixture golden family under `spec/fixtures/`
  (below) drives the typed `writ.FoldSchema` reducer the way
  `spec/fixtures/settings_test.go` drives `writ.FoldSettings`, and pins it
  exactly.
- `spec/fixtures/testdata/golden/schema/` — the signed-fixture golden
  family (`spec/fixtures/schema_test.go`, `TestSchemaFamily`) driving
  `writ.FoldSchema` directly: a bootstrap of a whole schema object, the
  §3.1 non-canonical `op_version` quarantine across all three affected op
  types, a concurrent multi-writer `define-field` race, a
  `deprecate-field`/redeclare interleaving, and unknown `op_type` /
  future `op_version` on the `schema` object type itself (§10).
- `spec/testdata/producer/` (WRIT-188) — §11's producer/reader paired
  verdicts over ops governed by a schema resolved from the log: see
  `spec/op-envelope.md` §Conformance data for the full description.
