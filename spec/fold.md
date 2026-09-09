# Fold semantics & concurrency rules

Status: **normative**. The key words MUST, MUST NOT, SHOULD, and MAY are to be
interpreted as described in RFC 2119.

Writ is an event-sourcing engine: current state is never stored
authoritatively; it is derived by deterministically **folding** an object's
operations. Concurrent writes do not conflict at push time — they coexist as
sibling operations in the git commit DAG and are reconciled at fold time by
spec-defined rules (ARCHITECTURE.md §Core model).

This document defines fold semantics completely: the input model, the
causality-monotone effective timestamp $t^*$, the deterministic total order,
the definition of concurrency, the generic tombstone and deletion/edit
interleaving model, and the **closed catalogue of per-field merge strategies**.
A conforming implementation written from this specification alone MUST produce
the exact same total order and folded state byte-for-byte on all inputs. The
normative test vectors in `spec/testdata/fold/` enforce this requirement.

## 1. Input model

Fold is a pure, deterministic function: `fold(ops) → state`. It performs no
I/O and accesses no git storage directly.

### The input set

The input to fold is a finite set of operations $S$ sharing a single
`object_id`. How operations are discovered and enumerated across writer chain
refs (`refs/writ/<writer-id>/<type>`) is specified in WRIT-7. Fold receives the
set of operations that are present in the local repository and belong to the
target object.

Each operation $u \in S$ carries:
- `id`: the git commit SHA (lowercase hex string).
- `parents`: the list of parent commit SHAs from the git commit carrier.
- `time`: the commit author timestamp as an integer (seconds since Unix epoch UTC, `1970-01-01T00:00:00Z`).
- `object_id`: the target object identifier from the payload carrier.
- `op_type`: the operation type string from the payload carrier.
- `body`: the JSON object containing type-specific fields.

### Ancestry restriction

Because a writer's append chain stores ops for multiple objects sequentially
(ARCHITECTURE.md §Ref layout), an op's git commit parents often include
commits belonging to other objects or non-op commits.

Fold operates strictly on the **ancestry-restricted DAG**:
For an op $u \in S$, its parents in the restricted DAG, denoted
$\text{Parents}_S(u)$, are the subset of its commit parents that belong to the
same input set $S$:

$$\text{Parents}_S(u) = [\, p \in u.\text{parents} \mid p \in S \,]$$

Edges pointing to commits outside $S$ are excluded from $\text{Parents}_S(u)$.
The order of parents in $\text{Parents}_S(u)$ preserves their relative order in
$u.\text{parents}$.

### Partial ancestry and truncated graphs

In distributed git workflows, a repository may contain a shallow clone, a
partial fetch, or an op referencing a parent commit that has not yet been
fetched.

A missing parent commit **truncates** ancestry: if a parent commit SHA is not
present in $S$, that edge is omitted from $\text{Parents}_S(u)$. Fold MUST NOT
fail, error, or reject an input set due to missing parent commits. Fold is a
pure function of the operations present at the time of evaluation; new state
becoming available when additional operations arrive is ordinary fetch
progression, not non-determinism.

Malformed input that contains a directed cycle within $S$ is invalid; a
conforming reader MUST reject an input set containing cycles.

## 2. Causality and concurrency

### Causality (happens-before)

Within the restricted DAG of $S$:
- An op $u \in S$ is an **immediate parent** of $v \in S$ if $u \in \text{Parents}_S(v)$.
- An op $u \in S$ **causally precedes** $v \in S$ (denoted $u \prec v$, or "$u$ happens-before $v$") if there is a non-empty directed path of parent-to-child edges from $u$ to $v$ in the restricted DAG.

### Concurrency

Two distinct operations $u, v \in S$ ($u \ne v$) are **concurrent** (denoted
$u \parallel v$) if and only if neither causally precedes the other:

$$u \parallel v \iff (u \not\prec v \land v \not\prec u)$$

Concurrency is a structural property of the restricted DAG. Implementations
MUST NOT approximate concurrency using timestamps.

## 3. Effective time $t^*$

Raw commit author timestamps cannot be used directly to order operations: clock
skew or a misconfigured clock could assign a child op an author timestamp
earlier than its parent ($u \prec v$ but $v.\text{time} < u.\text{time}$), which
would contradict causality.

Fold derives a **causality-monotone effective timestamp** $t^*(u)$ for every
op $u \in S$:

$$t^*(u) = \max\left(u.\text{time}, \max_{p \in \text{Parents}_S(u)} t^*(p)\right)$$

If $\text{Parents}_S(u)$ is empty (a root op in the restricted DAG),
$t^*(u) = u.\text{time}$.

### Properties of $t^*$

1. **Causal monotonicity:** For all $u, v \in S$, if $u \prec v$, then $t^*(u) \le t^*(v)$. A descendant can never have an effective timestamp earlier than its ancestor.
2. **Derived at fold time:** $t^*$ is computed entirely from data already carried in the op envelope (commit author time and commit parents). It requires no new envelope fields.
3. **Intuitive wall-clock ordering:** For concurrent operations ($u \parallel v$), $t^*$ reflects the author timestamp, ensuring concurrent edits order naturally by real-world time under normal clock conditions.

### Adversarial and clock-skew exposure

Because fold is pure and sees only signed operations, it cannot detect whether
a fast clock was accidental skew or intentional grinding. A writer with a clock
set far in the future will win last-writer-wins races against contemporary
writers until other writers advance past that time. Similarly, git commit SHAs
can be ground to produce a lexicographically smaller op ID to win tiebreaks.
This is an accepted trade-off of pure, offline, decentralized CRDT/event-fold
systems; trust and key authorization are handled at the signing and
verification layers (WRIT-22), not inside fold.

## 4. The deterministic total order

Fold consumes operations in a single, deterministic **total order** $L$, which
is a sequence containing every operation in $S$ exactly once.

The total order is constructed using a deterministic topological sort
(Kahn's algorithm with a priority queue) ordered by $(t^*, \text{id})$:

### Total order algorithm

1. Let $E = \emptyset$ be the set of already emitted operations.
2. Let $R \subseteq S$ be the set of **ready** operations whose restricted parents have all been emitted:
   $$R = \{\, u \in S \setminus E \mid \text{Parents}_S(u) \subseteq E \,\}$$
3. Let $L = [\,]$ be the ordered list of emitted operations.
4. While $R$ is not empty:
   a. Select the candidate $u \in R$ that minimizes the tuple $(t^*(u), u.\text{id})$:
      - Compare $t^*(u)$ and $t^*(v)$ as signed 64-bit integers (smaller timestamp comes first).
      - If $t^*(u) == t^*(v)$, compare $u.\text{id}$ and $v.\text{id}$ lexicographically by byte values as lowercase ASCII hexadecimal strings (smaller byte value comes first).
   b. Remove $u$ from $R$.
   c. Append $u$ to $L$.
   d. Add $u$ to $E$.
   e. For every child $v \in S \setminus E$ where $u \in \text{Parents}_S(v)$:
      if $\text{Parents}_S(v) \subseteq E$, add $v$ to $R$.
5. If $|L| \ne |S|$ upon termination, the graph contains a cycle and the input MUST be rejected. Otherwise, $L$ is the deterministic total order.

### Total order as the single primitive

The total order $L = [o_1, o_2, \dots, o_n]$ is the single primitive that
drives all fold execution. Reducers process operations strictly in the sequence
given by $L$.

**The ancestor/descendant equal-$t^*$ rule:**
If an ancestor $A$ and a descendant $D$ ($A \prec D$) have the same effective
timestamp $t^*(A) = t^*(D)$, but $A.\text{id} > D.\text{id}$ in byte order, a
naive key comparison would rank $D$ before $A$. However, because $D$ cannot
enter the ready set $R$ until $A \in E$, topological validity is strictly
preserved: $A$ is emitted before $D$ in $L$. Last-writer-wins evaluates
operations in $L$ order, so $D$ (the descendant) is processed after $A$ and
overwrites $A$.

## 5. Per-field merge strategies catalogue

To prevent implicit or undefined merge behavior, Writ establishes a
**closed catalogue** of 9 per-field merge strategies.

### The "no implicit behavior" requirement

The schema governing an object type MUST declare exactly one catalogue
strategy for every field of every body it defines
([`spec/schema-ops.md`](schema-ops.md) §4.3).

A field present in an operation payload that has no declared strategy in the
governing schema is **not merged**: it is treated as unknown data,
preserved and ignored per the forward-compatibility rule. Implementations MUST
NOT invent default merge behaviors for undeclared fields.

### Catalogue strategies

| Strategy | Target Data Type | Description |
| --- | --- | --- |
| `lww` | Scalar register | Last-Writer-Wins: the last operation in the total order $L$ that writes the field sets its final value. |
| `create-once` | Immutable register | The first operation in the total order $L$ that writes a non-null value sets the field; all subsequent writes to the field are ignored. |
| `set-union` | Grow-only set | Set of distinct elements; the folded state is the union of all elements added by any operation in $S$. |
| `set-observed-remove` | Add-Wins set | Set supporting addition and removal; additions win over concurrent removals. |
| `append` | Ordered list | Sequence of items ordered strictly by the total order $L$ of the operations that created them. |
| `tombstone` | Deletable entity | Entity deletion state; deletion wins over concurrent edits while edits still fold into state. |
| `lattice` | Join-semilattice | Monotone status transitions governed by a declared join operator ($\sqcup$). |
| `keyed-lww` | Keyed register map | A map from a declared composite key to a register; `lww` applied independently within each key. |
| `multi-value` | Multi-value register | Multi-Value Register (MVR): preserves concurrent values as a deterministic set in total order $L$, collapsing to a single value when an edit causally succeeds prior writes. |

Counters are deliberately omitted from this catalogue; adding a counter
strategy requires a spec amendment.

### Declarative rule tables and value types

Field merge rules are declared in machine-readable tables (`field-rules.json`, conforming to `schemas/field-rules.schema.json`). Each entry defines the merge behavior — and, orthogonally, the value type (`spec/value-types.md`) — for an `(op_type, op_version, field)` tuple:
- `op_type` (string): The operation type.
- `op_version` (integer): The operation schema version.
- `field` (string): The target field in the operation body.
- `target` (optional string): The state key in the generic fold map (`ObjectState.State`). Defaults to `field` if omitted. Rules may declare a `target` state key to avoid strategy collisions when multiple op types define identical body field names with differing merge strategies. The same remedy applies across versions of one op type, not only across op types: `Fold` groups matched rules by target key alone (not by `op_version`) and instantiates one accumulator per target from the first of its matching rules in canonical rule order (§Canonical rule order below), which for a version bump is the lowest `op_version`, so a `define-field` version bump (`spec/schema-ops.md`) that changes `strategy` while reusing a `target` already bound to a different strategy would silently run the older version's strategy over the newer version's writes rather than either rule's declared behavior. A version bump MAY freely change `value_type`, `enum`, `max_length`, `key`, or `key_types` under the same target, because those never change which accumulator factory runs; `lattice` is not on that list, because the `lattice` accumulator reads it at fold time to order its semilattice, so reusing a target across a `lattice` change would likewise order the newer version's writes by the older version's semilattice, and a version bump reusing a target MUST still agree on it. A version bump that changes `strategy` MUST declare a distinct `target`, and a rule table that reuses a target across a strategy change is non-conforming.
- `strategy` (string): Exactly one strategy from the closed catalogue.
- `key` (array of strings, required for `keyed-lww`): The ordered list of body fields forming the composite key.
- `lattice` (array of strings, required for `lattice`): The ordered elements of the semilattice.
- `value_type` (optional string): The closed catalogue entry (`spec/value-types.md`) for what the strategy's register or element holds. Optional: a rule declaring none is untyped, the same no-implicit-behavior idiom this section already states for an undeclared strategy.
- `enum` (array of strings): Required iff `value_type == "enum"`; the value list.
- `max_length` (integer, code points): Optional; only on `value_type` `string` or `text`.
- `key_types` (object: key column → value type): Only on `keyed-lww`; must cover exactly the columns `key` declares.

**Rule matching is scoped by object type.** A rule matches an operation only
when their `op_type` and `op_version` agree (an empty `op_type` on the rule,
or a zero `op_version` on either side, matches anything, as already
described above) and, in addition, only when their object types agree. An
empty object type on either side matches anything, the same convention
`op_version` already uses, so this changes nothing for a rule table folded
against a single known object type. A rule's object type is not a new
`field-rules.json` attribute: for a rule resolved from the log it is
`spec/schema-ops.md`'s `define-field` body's own `type`, and for the
bootstrap table it is the `schema` object type that table declares, so the
wire shape of a `field-rules.json` entry and of a `define-field` op body are
both unchanged by this. Scoping exists so that a body field with a common
name — an OR-set's `add`, say — declared for one object type never matches
an operation of another that happens to define an identically named field
with a different merge strategy, without every schema having to declare a
distinct `target` defensively against every other schema that exists.

**Rules sharing a `target` within one object type MUST agree on every
merge attribute.** Two rules whose `TargetKey()` (`target`, or `field` if
undeclared) is equal MUST agree on `strategy`, `value_type`, `key`,
`key_types`, `enum`, `max_length` and `lattice` — with one exception: two
rules that share both `op_type` and `field`, differing only by `op_version`
(a version bump), MAY freely change `value_type`, `enum`, `max_length`,
`key`, or `key_types` under the same target, as already stated above, and
MUST declare a distinct `target` if the bump also changes `strategy`.
`lattice` is not on that freely-changeable list: unlike those five, it is
consulted by the strategy at fold time — the `lattice` accumulator reads it
to order its semilattice — so two rules that share a target MUST agree on
it even when they are a version bump of one another, exactly as they must
on `value_type`, `key`, `key_types`, `enum` and `max_length` outside a
version bump, and exactly as they must on `strategy` itself. This is what
stops two body fields that merely happen to share a name — such as
one OR-set's `add` side and an unrelated OR-set's `add` side, declared
under different `op_type`s with no `target` of their own — from silently
sharing one accumulator: a rule table with such a case MUST target each
OR-set explicitly (a type whose `assign` op and `tag` op both carry
`add`/`remove` is the worked example: `assign`'s pair declares
`target: "assignees"` and `tag`'s declares `target: "tags"`, so the two
OR-sets land in separate state keys instead of one shared `add`/`remove`
pair). A rule table that violates this agreement rule is non-conforming,
exactly as one that reuses a target across a `strategy` change already was.

**Every rule that matches an operation applies.** An operation is not
limited to the first declared rule that matches it, by declaration order or
by any other ordering: `Fold` applies the write of **every** rule whose
`op_type`, `op_version` and object type match the operation (as defined
above), not only the first one found. Nothing in this specification limits
a target to being reached by one field: two different body fields MAY
legally share one target under one exact `(op_type, op_version)` envelope,
the same way a version bump or a second op_type may (both already
described above), provided the sharing rules agree on every merge attribute
the paragraph above requires. When an operation writes more than one such
field, every matching rule's write lands — an `append` target gains an
entry for each field the operation writes, a `set-union` or
`set-observed-remove` target gains each field's elements, and so on for
every strategy in the catalogue. An implementation that stops after the
first matching rule (**first-match**) is non-conforming: first-match has no
normative tie-break to appeal to (nothing in this document ranks one
matching rule over another), so two conforming first-match implementations
that discover rules in a different order would disagree on which field's
write survives, on the same input, deterministically for each but not
between them — the WRIT-186 defect class of behavior that is a function of
iteration order rather than of the signed log. Applying every match has no
such tie-break to get wrong: it has none to make.
`spec/testdata/fold/merge/append-two-fields-shared-target.json` pins this
for the reachable case — two body fields sharing one `append` target within
one envelope, both written by one operation — and both the reference fold
(`spec/reffold.go`) and the engine reducer (`engine/internal/fold`) apply
every match identically (WRIT-201).

**Canonical rule order.** Where more than one rule bound to one target
matches a single operation, those rules contribute their writes in
ascending `(op_type, op_version, field)`: `op_type` compared in canonical
code unit order, then `op_version` numerically, then `field` in canonical
code unit order. This order is a property of `Fold` itself, not of the
sequence a caller hands its rules in — an implementation MUST NOT let the
order a rule table happens to list, load or enumerate its rules in reach
folded state. Every component of the key is rule content, and a rule table
defines at most one entry per `(op_type, op_version, field)` tuple (§Declarative
rule tables and value types above), so the order is total and every
implementation derives the same one from the schema alone.

The order is observable only for the strategies where the relative order
writes contribute in is itself part of the result — `append` (list
position), and, for two writes within the very same operation only, `lww`,
`create-once` and `keyed-lww` (which of two simultaneous writes is treated
as later). `set-union`, `set-observed-remove`, `tombstone`, `lattice` and
`multi-value` reduce commutatively over which matching rule contributes
first, so the order changes nothing for them; it is stated once for every
strategy rather than per strategy, because an unobservable rule is still a
rule no implementation may deviate from. Across operations the total order
$L$ (§4) governs as it always has: canonical rule order resolves only what
$L$ cannot, two writes originating in one operation and so sharing one
position in $L$. The order the operation's own body happens to list those
fields in is not consulted — canonical JSON object member order is a
property of the encoding, not of the schema.

**Normalization is intrinsic to `person-ref`.** `spec/value-types.md` §Normalization defines the rule: where a rule's `value_type` (or, for a key component, `key_types` entry) is `person-ref`, the field normalizes per `spec/identifiers.md` automatically. It is not a separate declarative attribute a rule table author repeats field by field, and it is not dispatched by inspecting operation types or field names — accumulators remain schema-blind, driven exclusively by the rule's `value_type`/`key_types`.

### Unified empty-value contract

The generic fold map (`map[string]any`) is the single normative representation of folded state across implementations and preserves written values deterministically:
- In scalar registers (`lww`, §5.1), an operation writing an empty scalar (such as `text: ""` or a person identifier that normalizes to `""`) sets the register to `""` in the generic fold map.
- In collection strategies (`set-union` §5.3, `set-observed-remove` §5.4), elements that are empty (or normalize to empty) are dropped from the set. An operation whose elements are all dropped still counts as a write of the field, recording an empty collection in the generic fold map.
- In list strategies (`append` §5.5), an operation writing an empty list records `[]` in the generic fold map.

Typed domain serializations (such as language-specific state structs) MAY omit empty scalar values (`""` / zero values) and empty collections (`[]`) via serialization tags (e.g. `omitempty` or `omitzero`) rather than emitting them. Such omissions are a typed view and wire-serialization convenience; the underlying generic fold state remains normative and retains the empty values.

---

### Detailed strategy specifications

#### 1. `lww` (Last-Writer-Wins)
- **Initial state:** `null` / unset (or schema default).
- **Reduction:** As operations are consumed in total order $L$, if an operation's `body` specifies a value for the field, that value replaces the current state.
- **Empty scalar writes:** An operation writing an empty scalar value — such as `text: ""` or a person identifier that normalizes to `""` (`spec/identifiers.md` §Person identifiers) — sets the register to `""` in the generic fold map. Unlike set strategies (§5.3, §5.4) where empty elements are dropped from collections, scalar registers retain empty strings in normative generic folded state: setting a scalar to empty is a deliberate edit, distinct from the field never having been written. Typed domain serializations MAY omit empty scalar values (e.g. via `omitempty`) rather than emitting them.
- **Result:** The value written by the latest operation in $L$ that specified the field.
- **The value is stored verbatim,** so any JSON type reproduces byte-for-byte through canonical encoding and this strategy imposes no type constraint of its own. A field whose value is `null` makes the whole operation uninterpretable per §7.1: `null` is not a value written, it is a write claimed with no value in it.

#### 2. `create-once`
- **Initial state:** `null` / unset.
- **Reduction:** As operations are consumed in total order $L$, if the field is currently unset and an operation's `body` specifies a value, the field is set to that value. Subsequent operations that specify a value for this field have no effect.
- **Result:** The value set by the earliest operation in $L$ that specified the field.
- **The value is stored verbatim,** as in `lww` (§5.1), and a field whose value is `null` makes the whole operation uninterpretable per §7.1. An earlier revision of this section instead had a `null` write pass over the field silently; the field-level skip it described is what §7.1 replaces.

#### 3. `set-union`
- **Initial state:** Empty set $\emptyset$.
- **Reduction:** Any operation specifying one or more elements adds them to the set.
- **Empty elements are dropped:** An element whose value, after any normalization the field declares (`spec/identifiers.md` §Person identifiers), is the empty string MUST NOT enter the set. This rule applies to **every** item-valued field regardless of its op type, not only to person-valued fields: an empty tag, an empty remote URL, and an empty assignee are equally meaningless, and reducers MUST agree on discarding them. Producers are already forbidden from emitting such elements by the governing schema; the rule exists so that a non-conforming or future writer emitting an empty-string element cannot make two conforming readers disagree. Dropping governs materialized state only and does not weaken the preserve-and-ignore rule (`spec/forward-compatibility.md`): the operation carrying the element remains in the DAG, reachable, replicated, and byte-for-byte intact.
- **Elements are strings.** An element whose JSON value is not a string, or a field whose value is neither a string nor an array of strings, makes the whole operation uninterpretable per §7.1. `null` is such a value, at the field or as an element.
- **Result:** The mathematical set union of all added elements. In serialized state, elements are emitted in canonical sorted order (UTF-16 code unit order for strings, ascending numerical order for numbers). An operation whose elements are all dropped still counts as a write of the field: the field is present in the generic folded state map with the empty set as its value. Typed domain serializations MAY omit an empty collection rather than emitting it.

#### 4. `set-observed-remove` (Add-Wins OR-Set)
- **Initial state:** Empty set $\emptyset$.
- **Body shapes.** A field declaring this strategy has two sides, an add side and a remove side, and a body carries them in one of three shapes. A conforming reader MUST accept all three, because §7.1 is not computable from a strategy whose body shapes are not stated:
  - **Nested** — the declared field holds an object whose `add` and `remove` members are the two sides. Either member MAY be absent.
  - **Flat** — `add` and `remove` are themselves declared fields of the op, each carrying its own side. Either field MAY be absent. The two are one operation on one set, so both sides are read together and both are subject to §7.1: an operation whose `remove` side is malformed is uninterpretable even where a reader reaches it by way of the `add` field. A reader MUST apply removals to additions carried by other operations even when the removal op carries no `add` field, and MUST accept the nested shape present at a flat-declared field.
  - **Scalar** — the declared field carries one side's items directly and the operation's `op_type` says which side (an `add-*` or `add` op type maps to the add side, and a `remove-*` or `remove` op type maps to the remove side).
- **A side holds a string or an array of strings,** exactly as a `set-union` field does (§5.3): a single item needs no array around it. A side the body does not carry is not a write of that side and has no effect.
- **Mechanism:**
  - An add operation $a \in S$ adds an element $x$.
  - A remove operation $r \in S$ removes an element $x$ and targets all add operations of $x$ in its causal past ($a \prec r$).
  - An element $x$ is present in the folded set if and only if there exists at least one add operation $a \in S$ for $x$ such that no remove operation $r \in S$ for $x$ causally follows $a$:
    $$\text{present}(x) \iff \exists a \in S \text{ s.t. } \text{adds}(a, x) \land (\forall r \in S \text{ s.t. } \text{removes}(r, x), a \not\prec r)$$
- **Concurrency behavior:** If an addition $a$ and removal $r$ of the same element $x$ are concurrent ($a \parallel r$), the addition wins and $x$ is present in the folded set.
- **Empty elements are dropped:** As in `set-union` (§5.3), an element whose value — normalized first, where the field's value type is `person-ref` — is the empty string MUST NOT enter either the add side or the remove side of the OR-set. This rule applies to **every** item-valued field regardless of its op type — a `string`-typed OR-set's items are dropped on the same terms as a `person-ref`-typed one's, even though only the latter are normalized before the test.
- **Elements are strings.** As in `set-union` (§5.3), an element that is not a string — `null` included — makes the whole operation uninterpretable per §7.1, on the add side and the remove side alike, in all three body shapes. A side that is present and is neither a string nor an array of strings does so too, and a side whose value is `null` is such a side: an explicitly written side holding no value is a write claimed with no value in it, which is what §7.1 says `null` is. Reading it as an absent side instead would make `{"add": null}` and `{}` fold identically, which is exactly the objection §7.1 raises against skipping. A rejected remove removes nothing: an element it named stays present unless some other operation removes it.
- **Result:** Present elements emitted in canonical sorted order. An operation whose elements are all dropped still counts as a write of the field: the field is present in the generic folded state map with the empty set as its value. Typed domain serializations MAY omit an empty collection rather than emitting it.

#### 5. `append`
- **Initial state:** Empty list `[]`.
- **Reduction:** When an operation in total order $L$ appends an entry (or entries), the entry is added to the tail of the list.
- **Result:** Entries ordered strictly by the position in $L$ of their producing operations. If a single operation produces multiple entries, their relative order within that operation is preserved: entries an operation contributes through two different rules bound to this target are ordered by §5's canonical rule order (ascending `(op_type, op_version, field)`), and entries a single rule contributes from one array-valued field keep that array's own order. An operation that writes the field with an empty array appends nothing, but it is still a write: the field is present in the generic folded state map with the empty list `[]` as its value, which is the strategy's initial state. It MUST NOT fold to `null`.
- **Entries are values.** An entry is stored verbatim, so an entry of any JSON type reproduces byte-for-byte through canonical encoding and no type constraint applies. `null` is the exception, because it is not a value but the absence of one: a field whose value is `null`, or an array holding a `null` entry, makes the whole operation uninterpretable per §7.1.

#### 6. `tombstone` (Deletion and edit interleavings)
- **Initial state:** `deleted = false`.
- **The flag is a boolean.** Where an operation carries the declared tombstone field, its value MUST be `true` or `false`. Any other JSON type — `null` included — makes the whole operation uninterpretable per §7.1. An operation that carries no such field is unaffected: whether it deletes is then read from its op type, as below.
- **Semantics:**
  - A delete operation marks the entity as `deleted = true`.
  - An undelete operation marks the entity as `deleted = false`.
  - **Causal undelete requirement:** An undelete operation $u$ only clears deletions in its causal past:
    $$\text{deleted} \iff \exists d \in S \text{ s.t. } \text{is\_delete}(d) \land (\forall u \in S \text{ s.t. } \text{is\_undelete}(u), d \not\prec u)$$
  - **Concurrent delete and undelete:** If a delete $d$ and undelete $u$ are concurrent ($d \parallel u$), deletion wins (`deleted = true`).
  - **Concurrent delete and edit:** If a delete $d$ and edit $e$ are concurrent ($d \parallel e$), deletion wins (`deleted = true`). However, the field edits in $e$ MUST still be applied to the entity's underlying folded state according to their respective field strategies. Content remains completely reconstructible and is never discarded.
  - **Causal edit after delete:** An edit $e$ that causally succeeds a delete ($d \prec e$) without an intervening undelete applies its field writes to state, but the entity remains `deleted = true` unless an explicit undelete operation is present.

#### 7. `lattice` (Monotone status transitions)
- **Initial state:** The bottom element $\bot$ of the declared semilattice.
- **Semantics:** The field's allowed values form a bounded join-semilattice $(V, \sqcup, \le)$ with partial order $\le$ and join operation $\sqcup$.
- **Reduction:** When an operation writes value $v \in V$, the new state is $\text{state} \sqcup v$.
- **The value is a string.** A value of any other JSON type — `null` included — makes the whole operation uninterpretable per §7.1. A value that *is* a string but is not a declared element of $V$ is a different case and MUST NOT be rejected: it is a status from a later version of the governing schema, which the forward-compatibility preserve-and-ignore rule (`spec/forward-compatibility.md` FC-1) covers. It leaves the lattice state unchanged without quarantining the operation, so that sibling field updates carried in the same operation materialize normally.
- **Result:** Because $\sqcup$ is associative, commutative, and idempotent, concurrent transitions $u \parallel v$ reconcile deterministically to $v_u \sqcup v_v$ regardless of arrival or topological order.

#### 8. `keyed-lww` (Keyed Last-Writer-Wins registers)
- **Initial state:** Empty map `{}`.
- **Key:** A field declaring this strategy MUST also declare a non-empty, ordered list of body fields forming its key $k$. Two operations address the same register if and only if their key tuples are equal, compared component-wise over canonical values.
- **Reduction:** As operations are consumed in total order $L$, an operation writing the field replaces the value stored at its own key $k$ and leaves every other key untouched — that is, `lww` applied independently within each key.
- **Result:** For each key, the value written by the latest operation in $L$ bearing that key. Entries are serialized as a list of `{key, value}` records ordered by their key tuples, compared component-wise.
- **Key components are strings.** Every declared key component an operation carries MUST be a string. One that is a number, a boolean, an object, an array or `null` makes the whole operation uninterpretable per §7.1. A key component the body omits entirely is a different case and is not rejected: it contributes the empty component. A value the strategy stores is under the register rule below, not this one.
- **Normalization:** Where the rule's `value_type` is `person-ref`, or a `key_types` entry for a key component is `person-ref`, the normalization of `spec/identifiers.md` applies to that structural position. A reducer MUST NOT normalize a person identifier for keying and then store it verbatim: where a rule's value and a key component are both `person-ref`, the folded entry's key component and its value are the same normalized string. A non-string key component is schema-invalid and, being a key component, makes its operation uninterpretable per §7.1.
- **Registers hold values.** A value stored at a key is stored verbatim, so any JSON type reproduces byte-for-byte; `null` does not, and makes the operation uninterpretable per §7.1.
- **Why this is not `lww` or a set:** Registers scoped to a key — one vote per (voter, revision), one status per (revision, check name) — need a later write under one key to leave the others alone. Plain `lww` would collapse them to a single register; a set has no notion of a value being replaced.

#### 9. `multi-value` (Multi-value register)
 
For long-form text fields — the fields a schema declares with the `text` value
type (`spec/value-types.md`) — Writ defines the `multi-value` register
strategy (ARCHITECTURE.md §Document concurrency model).

##### Semantics and reduction rules

- **Target data type:** Long-form text field.
- **Initial state:** Unset / empty string `""`.
- **Reduction:**
  - An operation writing the field asserts a version of the text.
  - **Concurrent edits:** When two or more operations writing the field are
    concurrent ($u \parallel v$ in the restricted DAG), all concurrent versions
    are preserved in the folded state. The fold never merges text, never picks a
    winner, and never emits conflict markers.
  - **Causal collapse:** An operation $w$ that causally succeeds concurrent
    operations ($u \prec w$ and $v \prec w$) supersedes them. Only causally
    maximal writes — writes not succeeded by any other write in the object's
    causal DAG — are retained in the register. When a subsequent write causally
    observes all conflicting versions, the register collapses back to a single
    settled value.
- **Result and output shape:**
  - **Settled (single maximal write):** Serialized as a single string:
    `body = "..."`.
  - **Conflicted (multiple concurrent maximal writes):** Serialized as a JSON
    array of strings sorted in canonical code unit order:
    `body = ["...", "..."]`. Both versions are preserved as data; neither is
    invented.
- **Input validity (§7.1):** The value is a string. A value of any other JSON
  type — `null` included — makes the whole operation uninterpretable per §7.1.

#### The client-capability line

Fold is strictly deterministic and pure: it preserves conflicting versions as
data and performs no presentation or synthesis. Presentation of conflicts is
strictly a **client capability**:
- A client MAY render conflicts using git-style `<<<<<<<` markers, a
  side-by-side split view, or an interactive version picker.
- Presentation is entirely a client choice; the storage format and fold engine
  remain boring, deterministic, and pure.
- Live collaborative editing (such as ephemeral in-memory CRDTs) lives inside
  the client session and does not touch canonical durable storage.

#### The anyone-can-resolve property

In git, conflict resolution is tied to a merge event and can only be performed
by whoever merges. Under Writ's concurrency model:
- There is no designated merge event and no privileged merger.
- A conflict appears in everyone's folded state simultaneously as soon as
  concurrent operations are fetched.
- **Anyone can resolve the conflict:** Any collaborator can append an ordinary
  edit operation that causally references all conflicting operations, providing
  the resolved text and collapsing the register back to a single settled string.

## 6. Anchors and external references

Anchors (`spec/anchors.md`) and external references are **opaque to fold**:
- Fold treats anchor objects as raw data values and preserves them verbatim.
- Fold MUST NOT perform anchor resolution or attempt to open git trees/blobs. Anchor resolution is the responsibility of the Anchor Resolver (ARCHITECTURE.md §The six machines #4) and runs during projection materialization.

## 7. Unknown operations and forward compatibility

In accordance with the op envelope specification (`spec/op-envelope.md`) and
forward compatibility rules (WRIT-15):
- An operation carrying an unknown `op_type` or unrecognized fields MUST NOT cause fold to error or abort.
- Unknown operations remain full members of the restricted DAG: they participate in $t^*$ calculation and the total order $L$, maintaining causal relationships for any descendant operations.
- An unknown operation contributes no field writes to recognized fields.

### 7.1 Uninterpretable operations

A field with a declared merge rule can arrive carrying a JSON value its
strategy cannot consume: a `set-union` field holding a number, a `keyed-lww`
key component holding an object, an `append` field holding `null`. Bodies are
not validated on the fold path — a conforming producer validates before
signing (`spec/op-envelope.md` §Producer validation), but fold reads whatever
is in the log, including operations written by a foreign or buggy client, and
what is in the log is permanent.

**The rule.** An operation is **uninterpretable** if any field of its body that
carries a declared merge rule holds a value the field's strategy cannot
consume, as each strategy states in §5. A conforming reader MUST:

1. **Reject the whole operation, not the offending field.** An uninterpretable
   operation contributes no field writes at all — not to the malformed field
   and not to the well-formed fields it also carries.
2. **Quarantine it, not fail.** It is reported through the same channel as an
   operation with an unknown `op_type` (§7), as the opaque record
   `spec/forward-compatibility.md` `FC-5` specifies. That rule is the single
   definition of what the record carries; this section widens the population
   that flows through the channel and does not restate its contents. Rejection
   MUST NOT be an error return from fold: every other operation materializes
   normally. One bad operation costs that operation, never the object.
3. **Preserve it.** As in §7, an uninterpretable operation remains a full
   member of the restricted DAG — it participates in $t^*$, in the total order
   $L$ and in every ancestry test — and it remains in the DAG byte-for-byte
   intact.

**`null` is named as its own case** and is treated identically to a value of
the wrong type, wherever a strategy consumes a value: at the field, as an
element of a collection the strategy consumes, and at a named side of a
collection that has sides — an OR-set `add` or `remove` that is present and
holds `null` (§5.4). It is not a value; it is a write claimed with no value in
it. A side the body does not carry at all is a different case and is not a
write: absent is absent, and `{"add": null}` is not the same claim as `{}`.

**Scope.** This rule reaches only fields that have a declared merge rule. It
does **not** touch forward compatibility:

- Unknown `op_type` values and unknown `op_version` values keep §7 unchanged.
- Unrecognized **fields** keep preserve-and-ignore unchanged. A body carrying a
  field this op version does not define is not uninterpretable; the
  field is ignored and the operation folds.
- It does not recurse. The check reads the value at the declared field and,
  where the strategy consumes a collection, that collection's immediate
  elements. Structured payloads a strategy stores verbatim — an anchor,
  for instance — are opaque to fold (§6), so an anchor whose captured context
  is `null` inside is well formed.
- It does not enforce declared value types. Where a strategy stores a value
  verbatim, any JSON type but `null` is accepted, because a verbatim value
  reproduces byte-for-byte through canonical encoding (§8) in any
  implementation. A `title` carrying a number is schema-invalid and a producer
  MUST NOT write one, but a reader folds it to that number rather than
  rejecting it: the fold catalogue declares strategies, not types, and the
  governing schema is the one place types are declared.

#### Why rejection, and not coercion or skipping

Recorded because the alternatives are the obvious ones and both are worse
(WRIT-124, WRIT-126).

**Not coercion.** Coercing a malformed value to a string launders malformed
data into data that looks legitimate. Once `{"email":"carol@example.com"}`
becomes a person identifier by stringification, that string *is* a person: it
enters set-valued fields, keyed registers key on it, later operations
reference it, and nothing downstream can tell it was ever malformed. For a
format whose product is attribution and tamper-evidence, manufacturing a
person who never signed anything is the worst failure available — and, unlike rejection, it is
irreversible, because the fabricated value is entangled with real references by
the time anyone notices. Coercion is also where implementations diverge in
practice: a reducer that reached for its language's generic value-to-string
conversion emitted `map[email:carol@example.com]` and `<nil>` into folded
state, which no implementation in another language reproduces.

**Not skipping.** Skipping is a quieter coercion: coercion invents a value,
skipping invents an absence. "Approve on behalf of X" silently becomes "approve
anonymously" — a different claim, attributed to someone who asserted the first
one. And because `keyed-lww` substitutes the empty component for an absent key
component, a skipped subject would key the operation on the anonymous register,
where it can overwrite a verdict it was never addressed to. Skipping also makes
*malformed* and *absent* byte-identical in folded state, which is unfalsifiable
in a signed log.

**Why the operation and not the field.** An operation is the unit of signature
and of intent; half-applying one asserts something nobody signed. Writ's
operations are small and single-purpose, so little good data rides along with
the bad. And operation-level is one rule, implementable identically in every
language, where a field-level fallback needs specifying once per field per
strategy — which is the surface that produced these divergences in the first
place.

**Cost.** A reader loses the operation, and gains a quarantine record naming
the commit — which leads to the raw operation and to its signer. With a
conforming producer this only ever discards operations from foreign or buggy
clients.

## 8. State serialization order

To guarantee that folded state is byte-identical across independent
implementations:
- JSON object fields are serialized canonically per `spec/canonicalization.md`.
- Collections derived from `append` strategies are ordered by the total order $L$, and within one operation — which occupies a single position in $L$ — by §5's canonical rule order, ascending `(op_type, op_version, field)` over the rules that operation matched, then by the order any one rule's own array-valued field lists its entries in.
- Collections derived from `set-union` and `set-observed-remove` are serialized as JSON arrays sorted in canonical code unit order.
- Conflicted multi-value registers (`multi-value`) are serialized as JSON arrays of strings sorted in canonical code unit order.

## 9. Conformance data

The normative test vectors and fixture repositories verify compliance:
- `spec/testdata/fold/order/`: Abstract op graphs testing total order derivation across linear chains, multi-writer forks, equal-$t^*$ ties, skewed clocks, ancestry truncation, and multi-object interleaving.
- `spec/testdata/fold/merge/`: Op graphs testing each catalogue strategy, including delete/edit interleavings and concurrent mutations. `schema-*.json` cover the `schema` vocabulary specifically (`spec/schema-ops.md`): a bootstrap fold of a whole schema object, a `deprecate-field` write interleaved with a redeclaring `define-field`, concurrent `define-field` ops on one keyed-lww key, the two `target`-remedy vectors this section's version-bump rule states above (`schema-version-bump-same-target.json`, `schema-version-bump-new-target.json`), and `spec/schema-ops.md` §8.1's narrowing vector (`schema-narrow-field-attribute-not-cleared.json`). `append-two-fields-shared-target.json` and `lww-two-fields-shared-target.json` pin the "every rule that matches an operation applies" requirement and the canonical rule order above (WRIT-201): two body fields sharing one target within one `(op_type, op_version)` envelope, both written by one operation, fold to a two-item list (and to the `(op_type, op_version, field)`-latest write, respectively) under every conforming implementation. Both label their rules so that sorting the labels gives the reverse of canonical rule order, so an implementation taking its order from its own rule slice fails them.
- `spec/fixtures/testdata/descriptions/fold-*.yaml` and `spec/fixtures/testdata/golden/fold/`: Signed fixture repositories exercising concurrent field edits, multi-device writer races, LWW and tiebreaks, per-field merge strategies, delete/undelete/edit interleavings under `tombstone` (`fold-tombstone-threads.yaml`), and ancestry truncation.
- `spec/fixtures/testdata/descriptions/schema-driven-*.yaml` and `spec/fixtures/testdata/golden/schema-driven/`: Signed fixture repositories folding ordinary objects under rules resolved from a `schema` object in the log (`spec/schema-ops.md` §7), covering person normalization at every strategy position, version bumps, and rule-resolution conflicts.
