# `writ.schema` — the schema source file format (v1)

Status: **informative**. The normative artefact is
[`schema-ops.md`](schema-ops.md)'s operation vocabulary: a conforming
implementation of writ must agree on the ops that vocabulary defines, and
need not parse `writ.schema` at all. This document, and the Go package
that implements it (`engine/schemasrc`), exist for one reason: a schema
declared as an op sequence is unpleasant to author or review by hand, and
`writ.schema` is a human-editable working-tree source form for it,
Prisma-style — the log stays the source of truth, and this file is a view
onto it, not a second store of it (ARCHITECTURE.md §Schema layer).

A surface syntax carries no conformance obligation of its own. Two
independent implementations of writ that disagree on this grammar can
still interoperate perfectly, as long as both agree on the `schema` op
sequence — exactly as two implementations can disagree on their CLI's flag
names as long as both write the same ops. That is why no
`spec/testdata/` corpus backs this document: a corpus under
`spec/testdata/` is how this specification marks something as part of the
conformance surface, and an informative surface syntax has no conformance
surface to pin — one here would read as normative when it is not. The
grammar's own test corpus lives with its implementation, under
`engine/schemasrc/testdata/`, and is asserted against that package's
behavior, not against other implementations.

Writ's own repository has no `writ.schema` at its root. Writ declares no
types of its own; checking one in here would be naming a downstream
product, which nothing in this repository does (AGENTS.md).

## 1. Shape

Declarative, one type per block, fields carrying a value type and a merge
strategy:

```
namespace acme
description "Acme's vocabulary"

type standup {
  description "A daily standup update"

  op create 1, update 1 {
    title      string(200)       lww
    body       text              multi-value
    author     person-ref        create-once
    attendees  [person-ref]      set-observed-remove
    position   position          lww
    state      enum(open, done)  lattice(open, done)
  }

  op approval 1 {
    description "Approve or block a standup"
    verdict  enum(approve, block)  keyed-lww  key(subject person-ref)
    message  string                keyed-lww  key(subject person-ref)
  }

  op update 2 {
    title  string(200)  lww  target(title_v2)
  }
}
```

A field line is `<field> <value-type-expr> <strategy> <modifier>*`: both a
value type and a merge strategy are required on every field, because a
silent default is exactly the implicit behavior `spec/fold.md` §5 already
forbids. There is no `create`/`update` shorthand for an unnamed op: op
names are never invented by this grammar (§3) — they always come from an
explicit `op` block header, exactly as `define-op`'s `op_type` always
comes from the payload a producer wrote (`schema-ops.md` §4.4).

## 1.1. Encoding

A `writ.schema` source file is UTF-8. A byte, or byte sequence, that is
not valid UTF-8 is a lexical error — a file, line, and column, exactly
like any other rejection here (§9) — wherever it appears, including
inside a comment body or a string literal. `Format` writes a source
file back in place, so silently repairing an invalid byte (to U+FFFD, or
anything else) is not an option: the byte the author actually typed
would be gone with no error to say so, and no way to recover it from the
rewritten file. This applies uniformly; there is no position in the
grammar where an invalid byte is tolerated.

A leading UTF-8 byte-order mark (`EF BB BF`, U+FEFF) is stripped before
lexing begins, so a file saved with one by an editor that adds it
automatically lexes exactly as the same file without it. A BOM anywhere
other than the very first byte of the file is just the character
U+FEFF, which has no token production (§2) and is rejected the same way
any other unexpected character is.

## 2. The fence

This grammar declares **types, value types, merge strategies, and
relations. Nothing else** (ARCHITECTURE.md §Schema layer, WRIT-184
decision 6), and the fence is structural, not a matter of prose
discipline: there is no expression, call, conditional, import, extends,
or annotation production anywhere in the parser, so a computed field or a
hook has no syntax to be written in — not a rejected one, an absent one.
`engine/schemasrc`'s `invalid/` corpus pins a computed field, a hook, an
`if`, an `import`, an `extends`, a `default now()`, and a permission
clause each as a syntax error, so the fence lives in the fixtures and not
only here.

The keyword set below is closed and enumerated; `TestKeywordsAreClosed`
fails by name the moment the lexer's keyword table grows, so widening the
language is a deliberate, reviewed act:

```
namespace  description  type  op  deprecated  untyped  key  target
```

Reservation is **positional**, not global: which of these eight a given
slot refuses depends on where that slot sits, not on the word alone.

* **Namespace, a type name, an op type name.** All eight remain reserved
  outright. A file's `namespace` line, a `type`'s own name, and an
  `op`'s own type name can never be spelled `description`, `type`, `op`,
  and so on — pinned by `reserved-type-name.schema` in the invalid
  corpus (`type type { ... }` is still rejected).
* **A field name, and the argument of `target(...)`.** Only `deprecated`
  remains reserved; the other seven are *contextual* keywords here, each
  told apart from a field name by one token of lookahead, total rather
  than heuristic (§3 marks exactly where):
  * Inside an op block, `description` heads a description line iff the
    very next token is a string literal — no `value-type-expr` can begin
    with one, so a field literally named `description` (followed by its
    value type) is never mistaken for one.
  * In a field's modifier list, `key` and `target` are modifiers iff
    immediately followed by `(` — a field line's own name is never
    followed by `(`, so a following field literally named `key` or
    `target` is never swallowed as a bogus modifier of the field before
    it.
  * `namespace`, `op`, `type`, and `untyped` begin no production at all
    inside an op block's field list, so nothing needs to disambiguate
    them there.

  `deprecated` alone survives as reserved in this slot: its modifier is
  bare (no parenthesized argument to look ahead for), so a field
  followed by a field named `deprecated` puts one bare `deprecated`
  token where it could be either the first field's modifier or the
  second field's name, and one token of lookahead does not settle it —
  pinned by `deprecated-field-name.schema` (the word as the first field
  in its op block) and `deprecated-field-name-after-field.schema` (the
  realistic position, after a real field) in the invalid corpus.
  `contextual-keywords.schema` in the valid corpus is the positive
  fixture: one file with fields literally named `description`, `target`,
  `type`, `namespace`, `op`, `key`, and `untyped`, alongside real
  `description "..."` lines, a `target(...)` field immediately followed
  by a field named `target`, and a `key(...)` field immediately followed
  by a field named `key`.

A relation is an `object-ref` value type (`value-types.md`), not a
separate grammar bolted onto this one: writ has no join engine and no
reference resolution, so `object-ref` parses exactly like any other value
type name (§4) and resolving what it points at is the consumer's problem
(`ARCHITECTURE.md` §Object homing).

## 3. Grammar

EBNF, informal (`?` optional, `*` zero-or-more, `+` one-or-more, `|`
alternation):

```
file        = "namespace" ident [ description ] type* ;
description = "description" string ;
type        = "type" ident [ "deprecated" ] "{" [ description ] op-block* "}" ;
op-block    = "op" op-ref ("," op-ref)* "{" [ op-description ] field* "}" ;
op-ref      = ident number ;

(* op-description is description read with one token of lookahead: it
   applies only when "description" is immediately followed by a string
   literal, so a field literally named "description" (followed by its
   value-type-expr, never a string literal) falls to field below
   instead — see §2. file's and type's description have no field
   production to disambiguate from and need no lookahead. *)
op-description
            = "description" string ;

field       = ident value-type-expr strategy-expr modifier* ;

value-type-expr
            = "untyped"
            | "enum" "(" ident ("," ident)* ")"
            | "[" ident "]"
            | ident [ "(" number ")" ] ;

strategy-expr
            = "lattice" "(" ident ("," ident)* ")"
            | ident ;

(* key and target are modifiers read with one token of lookahead: each
   applies only when immediately followed by "(" — a field's own name is
   never followed by "(" — so a following field literally named "key" or
   "target" is never swallowed as a bogus modifier of the field before
   it (§2). *)
modifier    = "key" "(" ident ident ("," ident ident)* ")"
            | "target" "(" ident ")"
            | "deprecated" ;
```

`ident` is `[A-Za-z_][A-Za-z0-9_-]*`; a name bound onto the wire
(namespace, a type name, an op type name, a field name) is further
constrained to that field's own grammar (`schema-ops.md` §4), which the
parser checks and reports with a line and column, same as any other
rejection here. `number` is a decimal integer with no leading zero
required in the source — `op create 01` and `op create 1` name the same
op version — because canonicalization is a wire concern this package
handles once, at emission (§5), independent of how a human happened to
write the digits. `string` is a double-quoted string literal with `\"`,
`\\`, `\n`, and `\t` escapes; `#` runs to end of line as a comment.
Comments are never data — `description "..."` is (§6).

### 3.1. Value-type-expr, one production per shape

* `untyped` — no value type declared (§6; correction 3 of the ticket this
  document implements).
* `enum(a, b, ...)` — iff the field's value type is `enum`; the
  parenthesized list is the enum's value list.
* `[name]` — the element type of a `set-union`/`set-observed-remove`
  field. Required with those two strategies and forbidden with every
  other one: a scalar value type never takes brackets, and a collection
  strategy never omits them (except when the field is `untyped`, which
  has no name to bracket either way).
* `name`, optionally followed by `(n)` — a bare catalogue value type name
  (`value-types.md`); `(n)` is `max_length` and is only legal when `name`
  is `string` or `text`.

### 3.2. Strategy-expr

* `lattice(a, b, ...)` is one production, not a bare strategy word
  followed by a separate modifier: writing `lattice` also states its
  semilattice elements, because the elements are §7's rule requires
  wherever the strategy is `lattice` — there is no way to write the
  strategy without them.
* Every other strategy (`lww`, `create-once`, `set-union`,
  `set-observed-remove`, `append`, `tombstone`, `keyed-lww`,
  `multi-value`) is a bare catalogue word (`fold.md` §5).

### 3.3. Modifiers

* `key(col type, ...)` — required iff the strategy is `keyed-lww`,
  forbidden otherwise. Each pair is one key column and its declared value
  type; the production emits `key` and `key_types` together (§5), which
  is what makes `ValidateFieldRule`'s "`key_types` must cover exactly
  `key`" rule impossible to violate through this grammar.
* `target(name)` — the state key this field's register lands under
  (`fold.md` §5). Optional on every field; required in practice exactly
  when a version bump changes strategy while a prior version already
  claimed the default target (§7). `name` follows the field-name
  reservation rule (§2), not the closed one: only `deprecated` is off
  limits, so `target(description)` and `target(type)` are both legal.
* `deprecated` — marks the field discouraged for new writes without
  removing it (`schema-ops.md` §4, tombstone-style). A type takes the
  same trailing keyword: `type old-thing deprecated { ... }`.

Exactly one spelling exists per rule, which is what makes parsing and
rendering inverse operations rather than a choice made independently on
each side: `[name]` iff a collection strategy, `enum(...)` iff
`value_type == "enum"`, `(n)` only on `string`/`text`, `key(...)` iff
`keyed-lww`, `lattice(...)` iff `strategy == "lattice"`, `untyped` iff no
value type declared.

## 4. `op create 1, update 1 { ... }` — the multi-op block header

A header may name several ops sharing one field list: `op create 1,
update 1 { title string lww }` emits two `define-op`s (`create`/1,
`update`/1) and, for every field in the block, one `define-field` per
named op — two, for `title`. The op names always come from the file and
the versions are always explicit; this grammar never invents an op name
(§2), which is what an unnamed-op shorthand would have needed to do.

## 5. Compiling to the op vocabulary

`engine/schemasrc.Compile(f *File, objectID string) ([]codec.Envelope,
error)` emits, in a fixed, canonically sorted order — independent of how
the source file itself arranges its types, op blocks, or fields — the
`create`/`define-type`/`deprecate-type`/`define-op`/`define-field`/
`deprecate-field` sequence `schema-ops.md` §4 defines: `create`; then, per
type in name order, `define-type`, `deprecate-type` if the type is marked
deprecated, every `define-op` in `(op_type, op_version)` order, every
`define-field` in `(op_type, op_version, field)` order, and finally a
`deprecate-field` for every field marked deprecated, in the same order.

`op_version` is written through one helper, in this package, that always
produces the canonical decimal string `schema-ops.md` §3.1 requires
(`^[1-9][0-9]*$`) — the single place in the package that formats it, so a
non-canonical encoding (`"01"`) has no path to the wire regardless of how
a human wrote the version number in source. Every emitted `define-field`
is additionally validated through `spec.ValidateFieldRule` — the same
function every vocabulary's `field-rules.json` is validated through —
before `Compile` returns, so a file that would produce a rule
`RulesFromSchemas` later drops (a lattice element outside its enum, a
`key_types` mismatch, and so on) is rejected at compile time, with a line
and column, instead of silently vanishing at resolve time. One rule
`ValidateFieldRule` cannot see on its own, because it validates a single
rule at a time, is checked alongside it across the whole type: two
define-fields whose `TargetKey()` collides while their strategies differ
(§7) are rejected the same way, at the same time, rather than only at
resolve time.

`objectID` is an explicit, required parameter with no default and nothing
derived from `namespace`. Reusing it across successive applies to the
same schema object is load-bearing: applying a source file under a fresh
id would create a *second* `schema` object binding the same
`object_type`s, which `RulesFromSchemas` treats as a collision (§6 of
`schema-ops.md`) and responds to by withholding **all** rules for those
types — not just the newly applied ones. Whoever calls `Compile` supplies
the id; this package invents nothing.

## 6. What round-trips, and what does not

Comments and source layout have no representation anywhere in the op
vocabulary: `schema-ops.md`'s `define_field_body` has no `description`
property (only `create`, `define-type`, and `define-op` bodies carry one),
and `state.FoldSchema` sorts types, fields, and ops canonically, discarding
whatever order they were written in. Comments and layout are therefore
**lost through the log**, and this package promises exactly two things
instead of one imprecise one:

1. A **semantic** round-trip through the op vocabulary:
   `Compile(Parse(Render(FoldSchema(Compile(Parse(src))))))` reproduces
   `Compile(Parse(src))` byte for byte. Two additional idempotence
   properties hold on their own terms: `Render` applied to already-folded
   state is idempotent (rendering, re-parsing, re-compiling, and
   re-folding reproduces the same rendering), and `Format` is idempotent
   (`Format(Format(src)) == Format(src)`).
2. A comment-preserving **`Format`** (`engine/schemasrc.Format`) that
   reprints a source file canonically in place: every comment survives,
   and everything else — spacing, quoting, and modifier order within a
   line — is normalized. `Format` walks the parsed source file directly,
   not folded state, so it preserves the file's own type, op-block, and
   field arrangement; it does not recompute `FoldSchema`'s canonical
   grouping.

Prose that must persist through the log is written as an explicit
`description "..."`, which is data. A comment is never data, and nothing
here tries to make it one.

One further non-identity is bounded and worth stating plainly: where the
log carries a `define-field` with no matching `define-op` (permitted —
`schema-ops.md` §4.2 does not require one to precede the other), `Render`
emits the op block anyway (grouped, described, and ordered exactly as any
other), and recompiling the rendering adds the missing `define-op`. This
is additive, not lossy — `schema-ops.md` §4.2 already forbids dropping a
field for lacking a `define-op` — and it is the one case where
`Compile(Parse(Render(...)))` for a *foreign* log's schema state can carry
one more op than the original.

## 7. Evolution: a version bump

A field's merge strategy can change from one op version to the next
without a migration, because field rules are already keyed by
`(op_type, op_version, field)` (`schema-ops.md` §8): ops written under the
old version keep folding under the old rules, and ops written under the
new version fold under the new ones. A version bump changing `strategy`
while reusing the same `target` as a prior version is order-dependent —
`fold.md` §5's generic fold groups matched rules by target key alone and
instantiates one accumulator from whichever rule a caller's slice lists
first — and `engine/schemasrc.Compile` rejects it at compile time, with a
line and column, rather than deferring to the resolver: `compileType`
already holds the whole type when a field is compiled, so nothing about
this check needs to wait until the type's rules are assembled elsewhere.
Left unchecked here, the same collision is still caught later —
`RulesFromSchemas` drops the colliding rule and records a
`SchemaConflict` — but only after the ops are signed into the log and
unremovable, which is what makes catching it at `Compile` time the one
that matters:

```
type ticket {
  op create 1 {
    priority  string  lww
  }

  op create 2 {
    priority  enum(low, medium, high)  lattice(low, medium, high)  target(priority_v2)
  }
}
```

Version 2 gives `priority` a distinct `target` because it changes
`strategy` from `lww` to `lattice`; version 1's `priority` keeps the
default target (`priority`, the field name). Reusing one target across a
strategy change is what `fold.md` §5 calls order-dependent — the generic
fold groups matched rules by target key alone and instantiates one
accumulator from whichever rule a caller's slice lists first — and is
exactly what `target(...)` exists to avoid.

## 8. Grammar reference: value types and merge strategies

Both catalogues are closed and defined elsewhere; this grammar spells
them, it does not redefine them.

* Value types (`value-types.md`): `string`, `text`, `int`, `number`,
  `bool`, `timestamp`, `enum`, `person-ref`, `object-ref`, `git-oid`,
  `position`, `anchor` — plus the `untyped` keyword (§3.1), which is not a
  catalogue member but a declaration that none was chosen.
* Merge strategies (`fold.md` §5): `lww`, `create-once`, `set-union`,
  `set-observed-remove`, `append`, `tombstone`, `lattice`, `keyed-lww`,
  `multi-value`.

## 9. Errors

Every rejection carries a file name, a 1-based line, and a 1-based
column, counted in Unicode code points (the unit `value-types.md`
§Length units already uses for every other bound in writ, so a column
here means the same thing every other bound does):

```
writ.schema:12:14: unknown merge strategy "lastwriterwins"; expected one of append, create-once, keyed-lww, lattice, lww, multi-value, set-observed-remove, set-union, tombstone
```

`Parse` collects every error it finds into one list rather than stopping
at the first, so a hand-edited file with several mistakes reports more
than one of them per run.
