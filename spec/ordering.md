# Fractional Indexing & Shared Ordering Primitive (v1)

Status: **normative**. Schema: [`schemas/ordering.schema.json`](schemas/ordering.schema.json).
Vectors: [`testdata/ordering/vectors.json`](testdata/ordering/vectors.json).
Reference engine implementation: `engine/order`.

This specification defines the shared ordering primitive for user-controlled
sequences that survive concurrent edits in Writ. It establishes a single,
shared fractional indexing mechanism, referenced by every schema-declared
field whose value type is `position` ([`spec/value-types.md`](value-types.md)),
ensuring compatible keys and preventing drift across consuming schemas.

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

---

## 1. Scope & Purpose

In collaborative environments, users frequently define and modify the
relative order of entities — the `widget`s a `gadget` holds, the manual rank
of one item among its siblings, any sequence a person arranges by hand rather
than by a computed key.

In an append-only, distributed event log, integer positions (`0, 1, 2, ...`)
are unsuitable for user-controlled ordering:
1. **Collision on concurrent insert:** Two writers inserting an item between
   positions 1 and 2 both compute the same position or must renumber all
   subsequent items.
2. **Cascading renumbering:** In an append-only log, renumbering $N$ siblings
   generates $N$ distinct operations touching objects the writer has no intent
   to modify, inflating the op history and racing with concurrent edits from other
   writers.

Fractional indexing resolves this by representing positions as subdividable keys:
inserting between two neighbours always produces a new intermediate key without
modifying any existing sibling.

---

## 2. The Decision That Makes This Cheap in Writ

Most fractional indexing systems (such as those used in centralized web applications
or sequence CRDTs without total orders) require position keys to be **globally
unique**. They achieve this by appending per-writer jitter, client UUIDs, or random
suffixes to every generated key.

**Writ already has a causality-monotone total order with an op-id tiebreak**
([`spec/fold.md`](fold.md) §4). That total order provides a deterministic, global
ordering across all operations.

Consequently:
1. **Duplicate position keys are explicitly permitted.** Position keys do NOT
   require per-writer jitter or client UUID suffixes.
2. When two writers concurrently insert an item into the exact same slot using
   the same position key, both operations succeed and both items retain that
   position key.
3. Every implementation resolves ties identically by comparing the operation's
   unique identifier (`op_id`, the git commit SHA) in ASCII byte order.

Producers MUST NOT append random jitter or UUID suffixes to fractional index keys;
Writ's existing tiebreak machinery handles collisions cleanly and deterministically.

---

## 3. Representation

Position keys are **lexicographic ASCII strings**, not floating-point numbers or
rational fractions:
- **No precision floor:** Floating-point numbers lose precision after repeated
  subdivisions (typically ~53 bits of mantissa in IEEE 754 float64). Arbitrary-precision
  rationals require big-integer arithmetic and introduce canonicalization questions
  around coprime reduction.
- **Native storage and query support:** Byte-comparable ASCII strings sort
  natively in SQLite (`ORDER BY position ASC`), require no custom collation
  extensions, serialize cleanly in canonical JSON, and have no precision floor.
- **Byte comparison equals logical order:** Two keys $A$ and $B$ satisfy
  $A < B$ in logical ordering if and only if $A < B$ under standard unsigned
  byte comparison (`memcmp`).

---

## 4. Alphabet

Position keys use a fixed **base-62 alphabet** consisting of 62 ASCII characters:

```
0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz
```

In character code ranges:
- Digits `'0'` through `'9'` (ASCII bytes `0x30` through `0x39`, values 0–9)
- Uppercase letters `'A'` through `'Z'` (ASCII bytes `0x41` through `0x5A`, values 10–35)
- Lowercase letters `'a'` through `'z'` (ASCII bytes `0x61` through `0x7A`, values 36–61)

### Alphabet Invariants

1. **ASCII Collation Order:** In the ASCII standard, digits precede uppercase
   letters, which precede lowercase letters:
   $$\text{'0'} < \text{'9'} < \text{'A'} < \text{'Z'} < \text{'a'} < \text{'z'}$$
   Because the character values 0–61 map monotonically to increasing ASCII byte
   values, standard string byte comparison (`memcmp`) strictly equals logical
   base-62 numerical order.
2. **MinChar:** The minimum character of the alphabet is `'0'` (value 0).
3. **MaxChar:** The maximum character of the alphabet is `'z'` (value 61).
4. **MidChar:** The midpoint character of the alphabet is `'V'` (value 31,
   $\lfloor 62 / 2 \rfloor$).

---

## 5. Canonical Form

To guarantee a 1-to-1 bijection between position values and their string
representations, position keys MUST adhere to strict canonical formatting.

A string is in **canonical form** if and only if:
1. It is non-empty (`length >= 1`).
2. Every byte is an ASCII character within the base-62 `Alphabet` (`[0-9A-Za-z]`).
3. It does **NOT end with `'0'`** (the minimum character `MinChar`).

### Regular Expression

The canonical form is formally defined by the regular expression:

```
^[0-9A-Za-z]*[1-9A-Za-z]$
```

### Rationale for Forbidding Trailing `'0'`

In base-62 fractional notation, trailing zeros represent equivalent fractions
($0.5 = 0.50$), but in lexicographic string comparison, `"V0"` is strictly
greater than `"V"`. Forbidding trailing `'0'` eliminates redundant aliases,
guarantees canonical uniqueness, and ensures that string comparison strictly
matches fractional magnitude.

Producers MUST NOT emit keys ending with `'0'`. Readers and validators MUST
reject non-canonical keys as invalid input.

---

## 6. Key Generation: `Between(before, after)`

When a client creates an item or moves an existing item, it generates an
intermediate key strictly between two neighbouring keys `before` and `after`.

Both `before` and `after` are either empty strings (`""`) or valid canonical keys.
If both are non-empty, `before < after` in byte order MUST hold.

The generation algorithm `Between(before, after)` operates deterministically as
follows:

### 6.1. Empty Collection (`before == ""` and `after == ""`)

When inserting into an empty collection, the generated key is the alphabet midpoint:

$$\text{Between}("", "") = \text{"V"}$$

### 6.2. Insert Before First (`before == ""` and `after != ""`)

We require a canonical key $K$ such that $"" < K < \text{after}$:
1. Let $k \ge 0$ be the count of leading `'0'` characters in `after`.
2. Let $d = \text{after}[k]$ be the first non-zero character in `after` (which
   always exists because canonical keys cannot be all zeros).
3. Let $\text{idx} = \text{charIndex}(d)$:
   - If $\text{idx} > 1$: let $\text{mid} = \lfloor \text{idx} / 2 \rfloor$. The
     result is $k$ zeros followed by $\text{Alphabet}[\text{mid}]$.
   - If $\text{idx} == 1$ (character `'1'`): the result is $k+1$ zeros followed by
     $\text{MidChar}$ (`'V'`).

Examples:
- $\text{Between}("", \text{"V"}) = \text{"F"}$
- $\text{Between}("", \text{"F"}) = \text{"7"}$
- $\text{Between}("", \text{"1"}) = \text{"0V"}$
- $\text{Between}("", \text{"0V"}) = \text{"0F"}$
- $\text{Between}("", \text{"01"}) = \text{"00V"}$

### 6.3. Insert After Last (`before != ""` and `after == ""`)

We require a canonical key $K$ such that $\text{before} < K$:
1. Let $m \ge 0$ be the count of leading `'z'` characters in `before`.
2. If $m == \text{len}(\text{before})$ (i.e. `before` consists entirely of `'z'`s,
   such as `"z"` or `"zz"`):
   The result is $\text{before} + \text{"V"}$.
3. Otherwise, $\text{before}[m] < \text{'z'}$. Let $\text{idx} = \text{charIndex}(\text{before}[m])$:
   Let $\text{mid} = \lfloor (\text{idx} + 62) / 2 \rfloor$. The result is
   $\text{before}[:m] + \text{Alphabet}[\text{mid}]$.

Examples:
- $\text{Between}(\text{"V"}, "") = \text{"k"}$
- $\text{Between}(\text{"k"}, "") = \text{"s"}$
- $\text{Between}(\text{"z"}, "") = \text{"zV"}$
- $\text{Between}(\text{"zV"}, "") = \text{"zk"}$
- $\text{Between}(\text{"zz"}, "") = \text{"zzV"}$

### 6.4. Insert Between Neighbours (`before != ""` and `after != ""`)

Let $p$ be the length of the longest common prefix of `before` and `after`:

1. **Strict prefix:** If $p == \text{len}(\text{before})$ (`before` is a prefix of `after`):
   $$\text{Between}(\text{before}, \text{after}) = \text{before} + \text{Between}("", \text{after}[p:])$$
2. **Differing character:** If $p < \text{len}(\text{before})$, then
   $\text{before}[p] < \text{after}[p]$. Let $i_1 = \text{charIndex}(\text{before}[p])$
   and $i_2 = \text{charIndex}(\text{after}[p])$:
   - **Gap exists ($i_2 - i_1 > 1$):**
     Let $\text{mid} = i_1 + \lfloor (i_2 - i_1) / 2 \rfloor$.
     The result is $\text{before}[:p] + \text{Alphabet}[\text{mid}]$.
   - **No gap ($i_2 - i_1 == 1$):**
     If $\text{len}(\text{before}) == p+1$:
       The result is $\text{before}[:p+1] + \text{"V"}$.
     Otherwise ($\text{len}(\text{before}) > p+1$):
       The result is $\text{before}[:p+1] + \text{Between}(\text{before}[p+1:], "")$.

Examples:
- $\text{Between}(\text{"a"}, \text{"c"}) = \text{"b"}$
- $\text{Between}(\text{"a"}, \text{"b"}) = \text{"aV"}$
- $\text{Between}(\text{"a"}, \text{"aV"}) = \text{"aF"}$
- $\text{Between}(\text{"aF"}, \text{"aV"}) = \text{"aN"}$
- $\text{Between}(\text{"aV"}, \text{"b"}) = \text{"ak"}$
- $\text{Between}(\text{"a"}, \text{"an"}) = \text{"aO"}$
- $\text{Between}(\text{"an"}, \text{"b"}) = \text{"at"}$
- $\text{Between}(\text{"a"}, \text{"a1"}) = \text{"a0V"}$
- $\text{Between}(\text{"a0V"}, \text{"a1"}) = \text{"a0k"}$
- $\text{Between}(\text{"az"}, \text{"b"}) = \text{"azV"}$

---

## 7. Key Growth & Rebalancing

Repeatedly inserting items between the same two adjacent neighbours incrementally
grows key lengths (`a`, `aV`, `aF`, `a7`, `a3`, `a1`, `a0V`, ...).

### Rebalancing is Explicitly Out of Scope

In a centralized database, long keys are periodically cleaned up by rebalancing
(renumbering all items with evenly-spaced positions).

In Writ's distributed, append-only architecture:
1. Renumbering is a distributed write across multiple objects that races with
   concurrent user edits.
2. In per-writer append chains, rewriting sibling objects forces a writer to
   modify objects owned or touched by other collaborators.
3. In practice, manual human reordering produces modest key growth that remains
   well within acceptable limits (a few dozen bytes even after extreme subdivision).

Therefore, **rebalancing MUST NOT be performed by fold or projection reducers**.
If key compaction is ever required in a repository, it MUST be executed as an
explicit, client-initiated bulk update operation, never an automatic fold behaviour.

---

## 8. Fold Strategy: Ordinary Scalar LWW Register

A fractional position key is stored as an ordinary scalar register governed by
the standard **Last-Writer-Wins (`lww`)** merge strategy ([`spec/fold.md`](fold.md) §5.1).

- Reordering an object is simply an operation updating that object's
  `position` field.
- No sequence CRDTs, fractional-index-specific merge strategies, or custom conflict
  resolution logic are added to the fold engine.
- Concurrently written positions are reconciled by ordinary LWW based on the
  causality-monotone total order $L$.

---

## 9. Deterministic Collection Ordering

When presenting or querying an ordered collection of objects:

1. **Primary Sort Key:** `position` ascending (standard ASCII byte comparison).
2. **Secondary Sort Key (Tiebreak):** `op_id` (git commit SHA of the winning
   operation) ascending (standard ASCII byte comparison).

In SQLite projections, this corresponds directly to:

```sql
ORDER BY position ASC, op_id ASC;
```

This ensures that all conforming clients and engines display identically ordered
lists without ambiguity, even under concurrent insertions at identical position keys.
