# Value types

A merge strategy says how concurrent writes to a field reconcile
(`spec/fold.md` §5); it says nothing about what the field holds. `title: lww`
does not say `title` is a string. Value types are the second, orthogonal axis
a schema needs to be complete: what a field's register or element holds, so a
producer can reject a malformed write before it ever enters the log. Writ
never rewrites history, which is what makes write-time validation mandatory
rather than a convenience — there is no later pass that fixes a bad value.

## The closed catalogue

Like the strategy catalogue, value types are a **closed catalogue** of 12 value types.
No parameterisation beyond what this table lists: no regex
constraints, no cross-field validation, no custom types (WRIT-184 decision 6).

| Type | Encoding | Notes |
| --- | --- | --- |
| `string` | JSON string | optional `max_length`, counted in Unicode code points (see §Length units below) |
| `text` | JSON string | long-form; the type that pairs with the `multi-value` merge strategy; also takes an optional `max_length` |
| `int` | JSON integer | bounded to ±2⁵³−1 per `spec/canonicalization.md` |
| `number` | JSON number | bounded to ±2⁵³−1 per `spec/canonicalization.md`; unlike `int`, a fractional value is legal |
| `bool` | JSON boolean | |
| `timestamp` | RFC 3339 date-time string | exactly the shipped `$defs/timestamp` pattern (`cycle-ops.schema.json`, `review-ops.schema.json`) |
| `enum` | JSON string | parameterised by a declared `enum` value list; replaces the per-vocabulary schema-enum scraping `spec/vocabulary.go` used to do |
| `person-ref` | person identifier | per `spec/identifiers.md`; normalization is intrinsic to the type (see §Normalization below), not a separate rule attribute |
| `object-ref` | `<object-id>` or `<repo-id>#<object-id>` | per `spec/identifiers.md`; an opaque pointer, no resolution |
| `git-oid` | hex object id (40 or 64 characters) | git-shaped, so it stays in writ (`ARCHITECTURE.md` §Schema layer) |
| `position` | base-62 fractional index | per `spec/ordering.md`; validation is the existing canonical-form check |
| `anchor` | anchor object | per `spec/anchors.md`; git-shaped, so it stays in writ |

`anchor` and `git-oid` are the boundary test for the schema-layer
restructuring (WRIT-184): they are not merge strategies, but they are
git-shaped, and writ is signed mergeable state in git. Keeping them as
ordinary catalogue value types — with ordinary validators, no special-casing —
is what makes code review expressible as a schema rather than something writ
hard-codes.

## `spec/schemas/value-types.schema.json`

The normative schema, one `$defs` entry per catalogue type. Where a type's
encoding is already specified elsewhere, the entry `$ref`s that schema rather
than restating it: `person-ref` and `object-ref` `$ref` `identifiers.schema.json`
(`$defs/person-id` and `$defs/reference`), `position` `$ref`s
`ordering.schema.json`, and `anchor` `$ref`s `anchor.schema.json` in full.
`git-oid`'s pattern, previously duplicated as `review-ops.schema.json`'s local
`$defs/oid`, now lives here; `review-ops.schema.json` `$ref`s it back.

`enum`'s schema entry constrains only the JSON type (a string): which strings
are legal is declared per rule by the rule table's own `enum` property, not
restated in the shared schema.

## The rule shape

A field-rules.json entry (`schemas/field-rules.schema.json`) may declare, in
addition to its merge strategy:

| Field | Meaning |
| --- | --- |
| `value_type` (string, optional) | The catalogue entry for what the strategy's register or element holds. For `set-union`/`set-observed-remove` this types the **elements**; for `keyed-lww` it types the register value; for scalar strategies, the scalar. |
| `enum` (array of strings) | Required iff `value_type == "enum"`; forbidden otherwise. The value list. |
| `max_length` (integer, code points) | Optional; only on `value_type` `string` or `text`. |
| `key_types` (object: key column → value type) | Only on `keyed-lww`; must cover exactly the columns `key` declares. |

### `value_type` is optional

A rule that declares no `value_type` is **untyped**: it typechecks nothing,
the same no-implicit-behavior idiom `spec/fold.md` §5 already states for a
field with no declared merge strategy ("implementations MUST NOT invent
default merge behaviors for undeclared fields"). This is not a compatibility
shim tolerating an old form — every rule table here is new — it is the
existing idiom applied to the second axis.

Exactly five rules across the eleven shipped `field-rules.json` tables are
untyped, and each is named so the exception cannot quietly spread:

- `comments`' `create.subject`, a two-field record (`{object_type,
  object_id}`, `schemas/comment.schema.json` `$defs/subject`) folded whole
  under `create-once`.
- `schema-ops`' `define-field.enum`, `.key`, `.key_types`, and `.lattice`
  (`spec/schema-ops.md`): each holds an array or an object — the shape of a
  *rule table's own* `enum`, `key`, `key_types`, or `lattice` attribute
  (`spec/fold.md` §5) — not a scalar or a register the closed catalogue was
  built to type. `define-field.max_length` is not among them: it is a
  plain integer and types as `int` like any other.

No catalogue entry expresses a record, an array, or an object, and adding
one would be the scope growth WRIT-184 decision 6 forbids.
`TestUntypedRulesAreNamed` binds this: adding an untyped rule anywhere not
in this named set fails it by name.

## Orthogonality

Value types and merge strategies are **orthogonal**: any (value type,
strategy) pair that typechecks is legal, whether or not the shipped corpus
happens to use it. `anchor` under `set-union`, `position` under `keyed-lww`,
`git-oid` under `append` — all typecheck. `git-oid` is already used under
`append` (`review.revision.base`/`head`), `lww` (`review.set-status.merge_commit`)
and `keyed-lww` (`review.approval.revision`, `review.ci-status.revision`),
which demonstrates the orthogonality claim from the shipped corpus rather than
merely asserting it.

Producer validation (`spec.ValidateFieldRule`) rejects five rule shapes as
never able to typecheck, because the strategy's accumulator constrains the
value:

1. `tombstone` with a `value_type` other than `bool`. The tombstone
   accumulator (`engine/internal/fold/strategy.go`) tests `val == true` /
   `val == false` and nothing else, so any other declared type is a rule that
   can never fire.
2. `lattice` with a `value_type` other than `enum`, or `lattice` elements not
   a subset of the rule's `enum`. The semilattice's elements and the field's
   legal values are the same list.
3. `keyed-lww` whose `key_types` does not cover exactly `key` — and, across
   every rule sharing a key tuple within one `field-rules.json`, disagreeing
   `key_types` for the same columns.
4. `enum` declared with no `enum` values, or `enum` values declared on a
   non-`enum` `value_type`.
5. `max_length` declared on a `value_type` other than `string`/`text`.

The same field name is not bound to the same `value_type` across vocabularies.
`link.relation` is `enum` (`[fixes, relates, none]`) in `review` and `issue`,
but `string` — unconstrained — in `document`: `document`'s underlying schema
field carries no `enum` constraint, and typing it `enum` here would be a
producer-behaviour change this ticket must not make. This is a known
inconsistency, not an oversight; `WRIT-194` resolves it when the SDLC
vocabulary is deleted from the spec.

## Normalization is intrinsic to `person-ref`

`normalize` is not a rule attribute. Where a rule's `value_type` is
`person-ref`, the field normalizes per `spec/identifiers.md`'s person
identifier normalization rule — automatically, because normalization is a
property of the type, not a declaration a rule table author repeats field by
field. The three structural positions a rule can normalize map onto the
value-type shape directly:

- `NormalizesValue()` ≡ scalar or keyed-register position, `value_type ==
  "person-ref"`.
- `NormalizesItems()` ≡ collection strategy (`set-union`,
  `set-observed-remove`), `value_type == "person-ref"`.
- `NormalizesKey(k)` ≡ `strategy == "keyed-lww"`, `key_types[k] ==
  "person-ref"`.

A reducer MUST NOT normalize a person identifier for keying and then store it
verbatim: where a rule's `value_type` is `person-ref` and one of its
`key_types` entries is also `person-ref` for the same conceptual value (such
as `review.approval.subject`), the folded entry's key component and its value
are the same normalized string. This is unchanged from `spec/fold.md` §5's
prior "declarative normalization attributes" text; only the source of the
declaration moved.

## Producer-side and reader-tolerant

Matching `spec/op-envelope.md`'s existing split: a producer MUST reject a
value that fails its declared `value_type` (producer validation rule 3, now
driven off `value_type` as well as the per-vocabulary JSON schema). A reader
MUST tolerate a value it cannot interpret, surfacing it through the existing
`UnknownOp` channel (`spec/forward-compatibility.md`) rather than dropping the
operation carrying it. Nothing on the read path calls the value-type
validator: `engine/internal/value` is a producer-side guard, exactly as
`engine/internal/person.Check` already is.

## Length units

Every length bound in writ — `person-id`'s, `reference`'s, an anchor line's,
and now a value-type rule's `max_length` — counts Unicode **code points**, not
bytes and not UTF-16 code units. Every bound in the shipped schemas is a JSON
Schema `maxLength` keyword, which counts code points; a value-type rule's
`max_length` matches that unit exactly, so a validator written against one is
correct against the other. Graphemes are not expressible in JSON Schema and
would need a Unicode segmentation table — a new dependency this repository has
no other use for. `TestValueTypeMaxLengthUnitIsCodePoints`
(`spec/value_types_test.go`) is the executable form of this claim, in the
shape of `TestPersonIDLengthUnitIsCodePoints` and its two siblings
(`spec/length_units_test.go`).

## Fixtures

`spec/testdata/value-types/valid/` and `invalid/` hold a valid and invalid
instance per catalogue type, `{"value_type": ..., "value": ..., "params":
{...}}`, plus `invalid/index.json` recording the reason for each rejection in
the style `testdata/persons/invalid/index.json` established.
