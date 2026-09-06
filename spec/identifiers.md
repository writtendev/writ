# Identifiers and cross-repo references

Status: normative. Schema: [`schemas/identifiers.schema.json`](schemas/identifiers.schema.json).
Vectors: [`testdata/references/`](testdata/references/).

Writ connects code reviews, issues, projects, and cycles across multiple
repositories into a unified software development lifecycle graph. Cross-repo
linking — such as an issue in repository A resolved by a review in repository B —
requires that object identities and cross-references be globally unique from
day one (VISION.md §Scope architecture; ARCHITECTURE.md §Object types).

The key words MUST, MUST NOT, SHOULD, and MAY are to be interpreted as
described in RFC 2119.

## Scope

This section defines:

- **Object identifiers** — the canonical form minted by Writ producers.
- **Person identifiers** — format, normalization, and byte-comparison rules for
  collaborative actor identities (assignees, approval subjects).
- **Repository designators** — the immutable identity of a git repository.
- **Reference grammar** — the syntax for bare local and fully-qualified
  cross-repository references.

This section deliberately does not define:

- **The op envelope's `object_id` validation** — op envelopes accept any
  printable non-space ASCII string (1–256 characters) for forward
  compatibility ([`spec/op-envelope.md`](op-envelope.md)). This section
  constrains what conforming Writ producers mint.
- **Permission semantics** — see [Out of scope](#out-of-scope).

## Object identifiers

A collaborative object in Writ (a review, comment, issue, project, cycle)
possesses an identifier that is globally unique across all repositories,
writers, and devices.

```jsonc
"0123456789abcdef0123456789abcdef"
```

The canonical format minted by Writ producers is **128 bits of cryptographically
secure pseudorandomness, formatted as 32 lowercase hexadecimal ASCII characters**
(`^[0-9a-f]{32}$`).

Object IDs are minted client-side at object creation with no coordination,
central registry, or network communication.

### Collision safety

With 128 bits of entropy generated from a cryptographically secure random
number generator (CSPRNG), the probability $P$ of an ID collision among $N$
minted objects across all repositories and writers is bounded by the birthday
paradox approximation:

$$P \approx \frac{N^2}{2 \times 2^{128}} = \frac{N^2}{2^{129}}$$

Across one trillion ($10^{12}$) minted objects, the
probability of a collision is less than $1.47 \times 10^{-15}$. This ensures
collision safety across independent writers without requiring centralized
locking.

### Rationale and closed alternatives

Two alternative identification schemes were considered and rejected:

1. **The create op's commit ID (SHA):** In Writ's envelope model, the op
   commit ID is computed over a tree containing `op.json`. The `op.json`
   payload explicitly carries `object_id` (so all ops belonging to an object
   share the same object ID). Using the first op's commit ID as the object ID
   creates an impossible circular dependency: the commit ID cannot be known
   until `op.json` is constructed and hashed, but `op.json` would need to
   contain the commit ID.
2. **Sequential integers (e.g. #1, #2):** In Writ's distributed architecture,
   writers append ops to per-writer append chains (`refs/writ/<writer-id>/<type>`)
   and push to their own namespaces without locking. Sequential numbering
   requires a central coordinator to allocate monotonic sequence numbers,
   which is incompatible with offline creation and conflict-free concurrent
   pushes.

### Producer and reader conformance

- **Producers MUST** mint object IDs using 128 bits of cryptographically
  secure randomness formatted as 32 lowercase hexadecimal characters.
- **Readers MUST NOT** reject object IDs that fail to match the 32-hex
  lowercase format if they satisfy the envelope's printable non-space ASCII
  constraint (`^[\x21-\x7e]+$`, 1–256 characters). Readers MUST treat
  non-conforming or foreign IDs as opaque identifiers. This forward-compatibility
  rule ensures older readers do not discard objects created by newer or
  third-party producers.

## Person identifiers (`person-id`)

A collaborative actor in Writ (a human author, reviewer, assignee, or a
non-human writer such as CI) is identified in operation payloads using a
**person identifier**.

```jsonc
"email:alice@example.com"
"user:alice"
```

Person identifiers appear in op payloads across the SDLC vocabulary:
- **Assignees** on reviews ([`spec/review-ops.md`](review-ops.md) §5 `assign`)
- **Assignees** on issues ([`spec/issue-ops.md`](issue-ops.md) §4 `assign`)
- **Approval and dismissal subjects** on reviews ([`spec/review-ops.md`](review-ops.md) §6 `approval`)
- **Thread resolvers** (`resolved_by`) on comments ([`spec/comments.md`](comments.md) §5 `resolve`)

This section supersedes the earlier bare-email person identifier format
(WRIT-99): a conforming identifier now always carries a scheme.

### A label, not a credential

A person identifier proves nothing and is not meant to. Attribution in Writ is
established cryptographically, by the signature on the op commit
([`spec/op-envelope.md`](op-envelope.md)): who *wrote* an op is answered by the
commit, not by anything inside the payload. A person identifier answers a
different question — who an op *refers to*: who is assigned, whose approval
this is, who resolved this thread. Any writer can name any person; the
signature is what records who made that claim.

That is why the format needs exactly one property, and only that one:

> **Two independent implementations MUST agree on whether two person
> identifiers denote the same person.**

Equality, not identity. Everything below exists to make that agreement
mechanical — a grammar, a split rule, a normalization, and a comparison —
and nothing below attempts to establish who anyone actually is.

### Format

A person identifier is a scheme and a value, separated by a colon:

```text
person-id = scheme ":" value
```

| Field | Grammar | Bound | Meaning |
| --- | --- | --- | --- |
| `scheme` | `[a-z][a-z0-9+.-]*` | at most 32 characters | Names the namespace the value belongs to. |
| `value` | any non-empty string | at most 320 code points | Opaque to Writ within its scheme. |

Two schemes are defined:

| Scheme | Value | Example |
| --- | --- | --- |
| `email` | A single email address. | `email:alice@example.com` |
| `user` | An opaque handle, scoped to the team. | `user:alice` |

Writ does not parse either value. An `email:` value is not validated against
RFC 5321 and a `user:` value has no assigned meaning beyond the team that
mints it; both are compared, never interpreted.

**Non-human writers use the same schemes.** A CI writer is `user:ci`, not a
fictional mailbox such as `bot@ci.writ.dev`. Inventing an address for an actor
that has none produces an identifier that looks routable and is not, and it
puts a domain nobody controls into a permanent record. `user:` exists exactly
so that an actor without an email address does not need one.

**Public intake bots and third-party attribution.** The `user:` scheme also
serves downstream intake bots that bridge public issue reports from external
services (such as GitHub Issues, web forms, or bug bounty platforms) into Writ
operations. An intake bot importing a ticket from an external contributor
records `user:github-octocat` or `user:<service>-<id>`. This states truthfully
what identity is known, avoiding synthesized email addresses (such as
`octocat@users.noreply.github.com` or fictional domains) that look routable and
are not.

### Parsing: split on the first colon

An implementation MUST split a person identifier at its **first** colon: the
scheme is everything before it, the value is everything after it.

The first colon and not "a colon", because a value may legally contain one. An
email address may carry a colon inside a quoted local part, so:

```text
email:"a:b"@example.com   →   scheme "email", value "\"a:b\"@example.com"
```

Splitting on the last colon, or rejecting an identifier with more than one,
gives a different answer for that address — and a visibly wrong one: a
last-colon split reads the scheme as `email:"a`, which is outside the scheme
charset, so a conforming identifier is refused. Both spellings agree on every
ASCII example and disagree only on a rare input, which is the shape of defect
that surfaces years later as an unreproducible report. Fixture:
[`testdata/persons/valid/first-colon-quoted-local-part.json`](testdata/persons/valid/first-colon-quoted-local-part.json).

### Equality is per-scheme, and schemes never unify

Two person identifiers denote the same person if and only if their normalized
forms are byte-equal. Because the scheme is part of the normalized form, this
means:

> `email:alice@example.com` and `user:alice` are **different identifiers,
> permanently**, even where every human involved knows they are the same
> person.

Unifying identities across schemes is a client or registry concern
(§[Identity mapping out of scope](#identity-mapping-out-of-scope)), never the
format's. The moment the format gets clever about aliasing — a rule that
`user:alice` and `email:alice@example.com` are the same when some registry says
so — two conforming implementations begin disagreeing about the one property
the format has to deliver, because they will not have the same registry.

The accepted cost is visible: a person assigned under both schemes appears
twice in an assignee set. That is the honest rendering of what the ops say, and
a client is free to present them as one person. The fold is not.

**Unknown schemes** are preserved and compared byte-wise, exactly like known
ones, and are never merged with a known scheme. This is the same rule as
unknown-op tolerance ([`spec/forward-compatibility.md`](forward-compatibility.md)):
a reader that does not recognize `keybase:` still folds it correctly, because
folding a person identifier never requires understanding it.

### A bare identifier is invalid

A colonless string is **not** a person identifier. There is no bare form, no
legacy alias for the pre-WRIT-102 bare-email format, and no implicit scheme:
a reader MUST NOT read `alice@example.com` as `email:alice@example.com`.

The consequence is stated rather than papered over. Combined with the op-level
reject for malformed bodies (WRIT-124/126), an op already written with a bare
identifier becomes uninterpretable once that lands and is quarantined into
`unknown_ops` ([`spec/fold.md`](fold.md) §7). Nothing is destroyed — the op
stays in the log, signed and readable, and any future rule can pick it back up
— but folded state loses those assignees and approval subjects. That is
accepted: an implicit scheme would mean guessing which person a bare string
meant, and a guess that is wrong is worse than a value that is absent.

### Length bounds

| What | Bound |
| --- | --- |
| `scheme` | `[a-z][a-z0-9+.-]*`, at most **32** characters |
| `value` | at most **320** code points, counted **after normalization** |
| whole `person-id` | derived: **353** (32 + 1 + 320) |

The whole-identifier bound is derived, not independent. It is stated so that a
validator seeing only the flat string — a JSON Schema `maxLength`, a database
column — has a number, and so that nobody has to derive it twice and get it
wrong once.

One value bound across all schemes, deliberately. A per-scheme bound is a
second number for two implementations to disagree about, and the bound's job
does not vary by scheme.

Four rules carry normative weight:

1. **Reject, never truncate — at any layer.** A producer, a validator, a
   projection, and a client all MUST reject an over-long identifier rather
   than shorten it. Truncation collapses two distinct identifiers into one:
   an attacker who registers a long identifier whose truncation equals a
   victim's becomes the same person as the victim for assignment, approval
   keying and set membership. That is attribution confusion with a direct
   attack path, which a length cap is not worth.
2. **The bound applies to the normalized value.** Normalization changes
   length — NFC composition shrinks, NFD expands, full case folding expands
   (`ß` → `ss`). Checking the raw input validates a string that is then
   discarded, and two conforming readers checking at different points would
   disagree about the same identifier. An identifier that crosses the bound
   only *before* normalization is valid.
3. **This is a resource bound, not an RFC 5321 conformance check.** Op bodies
   are written into signed, immutable commits in an append-only log: an
   unbounded string is stored once per op and never reclaimed, so a
   multi-megabyte identifier is permanent repository weight. The bound stops
   that and does nothing else. Writ does not parse email addresses and MUST
   NOT be read as implying that it does. This is what makes code points an
   honest unit: JSON Schema `maxLength` counts code points where RFC 5321's
   320 counts octets, so a 320-code-point CJK value occupies roughly 960
   bytes. Immaterial for a bound whose job is stopping megabytes — and
   preferable to claiming an octet-exact conformance Writ never performs.

4. **The unit is Unicode code points — not octets, and not UTF-16 code
   units.** All three coincide on ASCII, which is why nearly every identifier
   in practice cannot tell them apart, and why an implementation reaching for
   its language's default length is not obviously wrong until someone with a
   non-ASCII identifier arrives. Go's `len`, Rust's `String::len` and Python's
   `len` over `bytes` count octets; JavaScript's `String.prototype.length` and
   Java's `String.length()` count UTF-16 code units. Neither is the unit here.
   A conforming implementation MUST count code points, which is what JSON
   Schema `maxLength` counts.

   This is fixture-enforced rather than left to the prose, because prose
   cannot fail a build. Three at-limit vectors bracket the bound in the three
   units — [`value-at-limit.json`](testdata/persons/valid/value-at-limit.json)
   (ASCII), [`value-at-limit-multibyte.json`](testdata/persons/valid/value-at-limit-multibyte.json)
   (320 code points, 640 octets) and
   [`value-at-limit-supplementary.json`](testdata/persons/valid/value-at-limit-supplementary.json)
   (320 code points, 640 UTF-16 code units) — with an over-limit companion for
   each under [`testdata/persons/invalid/`](testdata/persons/invalid/). An
   implementation counting octets rejects the second; one counting UTF-16 code
   units rejects the third; either way it fails the corpus. `TestPersonIDLengthUnitIsCodePoints`
   computes those witnesses from the corpus rather than naming them, so
   deleting the last vector that separates a unit fails the suite instead of
   quietly reopening the hole.

The number 320 is inherited from the ceiling an RFC 5321 address can reach (a
64-octet local part, `@`, and a 255-octet domain). It is a starting point that
happens to be roomy, not a conformance claim, and it applies to `user:` values
that have nothing to do with email.

### Normalization rules

To guarantee deterministic comparison, portable queries, and interoperability
across independent implementations, person identifiers MUST be normalized.
Normalization is **structural**: it treats the two halves separately.

0. **The identifier as a whole:** leading and trailing whitespace removed,
   *then* split at the first colon. Whitespace around the identifier is
   therefore not part of the scheme.
1. **Scheme:** lowercased. Whitespace inside or after a scheme is not removed,
   because the scheme charset has no whitespace in it: `email :alice@x` has no
   valid scheme and is not a person identifier.
2. **Value:** leading and trailing whitespace removed, then folded by the
   algorithm in §[The value folding algorithm](#the-value-folding-algorithm).
3. **Non-empty:** after normalization, both the scheme and the value MUST
   contain at least one character.

$$\text{norm}(s) = \text{lowercase}(\text{scheme}(s)) \mathbin{\|} \text{":"} \mathbin{\|} \text{fold}(\text{trim\_whitespace}(\text{value}(s)))$$

The bound in §[Length bounds](#length-bounds) applies to the value **after**
this step. The identifier as it appears in the op body is the normalized form,
which producers MUST already have written.

### The value folding algorithm

$\text{fold}$ is three named steps, applied in this order, and it is the same
algorithm for **every scheme**:

1. **Normalize to NFC** — Unicode Normalization Form C
   ([UAX #15](https://www.unicode.org/reports/tr15/)).
2. **Apply Unicode default case folding** — `toCasefold(X)`
   ([UAX #21](https://www.unicode.org/reports/tr21/) §2.3), the **full**
   mappings, being the `C` and `F` entries of `CaseFolding.txt`. The `T`
   entries are **not** applied: folding is locale-independent, and a Turkish
   locale MUST NOT change the answer.
3. **Normalize to NFC again.**

All three steps are evaluated against **Unicode 15.0.0**, which this document
pins. An implementation MUST state the Unicode version it folds against, and a
change of version is a change to this specification.

$$\text{fold}(v) = \text{NFC}(\text{toCasefold}(\text{NFC}(v)))$$

**Why "lowercase" was not enough.** The earlier rule said the value was
lowercased, which is not a specification. Go's `strings.ToLower` applies simple
case mapping and answers `i` for `İ` (U+0130); Rust's `str::to_lowercase`
applies the full `SpecialCasing` mappings and answers `i` followed by U+0307.
Two conforming implementations, different bytes, one person split into two
identities across assignee sets and approval keys. Under the algorithm above
both answer `i` followed by U+0307, because `toCasefold` is one function with
one answer.

**Why NFC, and why twice.** Without a normalization step, `é` written as U+00E9
and `é` written as `e` followed by U+0301 are two different people, which no
user typed and no client can repair. The trailing NFC is not redundant: case
folding does not preserve a normal form, so folding NFC input can leave a
composable sequence behind — U+017F followed by U+0301 folds to `s` followed by
U+0301, which is not NFC. Without the second pass $\text{fold}$ would not be
idempotent, and because normalization is applied by the producer, by the fold
and again by any projection, a rule whose second pass disagreed with its first
would reintroduce exactly the divergence it exists to remove. With it,
$\text{fold}(\text{fold}(v)) = \text{fold}(v)$ for every input.

**Why full folding rather than simple.** Simple case folding (the `C` and `S`
entries) is exposed by the standard library of neither Go, Python nor Rust, so
specifying it would give an implementer nothing to target and every implementer
a table to transcribe.
Default case folding is one call in each: Go `golang.org/x/text/cases.Fold`,
Python `str.casefold()`, ICU `u_strFoldCase` with `U_FOLD_CASE_DEFAULT`. The
cost is that folding can lengthen a value — `ß` folds to `ss` — which
§[Length bounds](#length-bounds) already accounts for by measuring the bound
after normalization.

**One algorithm for every scheme**, deliberately. A per-scheme fold — `email:`
per RFC 5321 and IDNA, `user:` per PRECIS
([RFC 8265](https://www.rfc-editor.org/rfc/rfc8265) `UsernameCaseMapped`) — was
considered and rejected. It is a second and third algorithm for two
implementations to disagree about, it makes the answer to "are these the same
person" depend on parsing a value this format promises never to interpret
(§[Format](#format)), and an unknown scheme would have no rule at all. The
accepted cost is that an `email:` value is not folded the way an SMTP server
would fold it. Writ does not deliver mail; it compares strings.

**Stream-Safe Text is not applied.** $\text{NFC}$ above is Normalization Form C
and nothing else. Implementations MUST NOT insert U+034F COMBINING GRAPHEME
JOINER, and MUST NOT stop composing, because a value carries a long run of
combining marks. This is called out because it is a live interoperability trap
rather than a hypothetical: normalization libraries that implement UAX #15
Stream-Safe Text apply it by default, and a value with more than 30 consecutive
non-starters — comfortably inside the 320-code-point bound — then folds to
different bytes in different implementations, which is the one thing this
format may not do.

This document does not restrict which characters a value may contain. Control
characters, bidirectional overrides and zero-width characters are an open
question, deliberately separate from how a value is folded.

### Comparison and equality

Two person identifiers $A$ and $B$ denote the same person if and only if their
normalized byte representations are equal:

$$\text{equal}(A, B) \iff \text{norm}(A) == \text{norm}(B)$$

For example:
- `"  Email:Alice@Example.COM  "` normalizes to `"email:alice@example.com"`.
- `"email:alice@example.com"` and `"  EMAIL:ALICE@EXAMPLE.COM  "` compare as
  equal.
- `"email:alice@example.com"` and `"user:alice"` do **not** compare as equal.
- `"user:José"` written with U+00E9 and `"user:José"` written with `e` followed
  by U+0301 compare as **equal**: NFC composes both to the same bytes.
- `"user:İ"` (U+0130) normalizes to `user:` followed by `i` and U+0307, on
  every conforming implementation.
- Deduplication, set membership tests in add-wins OR-sets (`set-observed-remove`),
  and keyed LWW lookups (`keyed-lww`) operate on the normalized string.

### Writing a third party's identifier

Assignment writes somebody else's identifier. When Alice assigns Bob, it is
**Bob's** identifier that Alice puts into the op — and that op is a signed
commit in an append-only log that is pushed to a remote and cloned by everyone
with read access. Under the `email:` scheme, that is Bob's email address in an
unretractable public record.

Two properties make this worth stating plainly rather than burying:

- **`delete` is a projection tombstone, not erasure.** Removing an assignee, or
  deleting a comment, folds to state that hides the value; the op that carries
  it stays in git history exactly as written and travels with every clone.
  There is no operation in this format that removes data from the log.
- **The writer is not the subject.** The person whose address is published is
  usually not the person who decided to publish it, and cannot withdraw it.

The mitigation is `user:`. A team that does not want member email
addresses in its published op log uses opaque handles — `user:alice` — and
keeps the handle-to-person mapping wherever it keeps its other member data,
outside the format. Writ takes no position on which scheme a team should
use; it provides one that does not require publishing an address, and names
the consequence of the one that does.

### Producer and reader conformance

- **Producers MUST** emit normalized, scheme-prefixed person identifiers when
  writing operation payloads, and MUST reject — never truncate, never repair —
  an identifier that violates the grammar or the bounds.
- **Producers MUST NOT** write a `writer-id` where a `person-id` is expected,
  nor derive one from the other
  (§[Relationship to `writer-id`](#relationship-to-writer-id)).
- **Readers and Reducers MUST** normalize person identifiers upon reading op
  payloads prior to evaluating set membership, keyed lookups, deduplication,
  or projection indices.
- **Readers MUST NOT** unify identifiers across schemes, and MUST preserve and
  compare byte-wise the identifiers whose schemes they do not recognize.
- **Reducers MUST** carry the normalized form into the OR-set members and
  `keyed-lww` entries they fold, not merely into the comparison that selects
  them. Where a `keyed-lww` key component is derived from a person identifier,
  the value stored under that key reads back normalized as well: normalizing an
  identifier for keying and then storing the payload verbatim is non-conforming.
- **Scalar registers (`lww`) retain normalized strings:** While rule 3 defines
  normalized person identifiers as containing at least one character, and set
  strategies (`spec/fold.md` §5.3, §5.4) drop elements that normalize to empty,
  scalar registers governed by `lww` (`spec/fold.md` §5.1) — such as comment
  `resolved_by` — retain normalized strings (including `""` when non-conforming
  input normalizes to empty) in the normative generic fold map. See `spec/fold.md`
  §5.1 for the scalar register rule and the unified empty-value contract.

**What the schema can and cannot say.** The `person-id` definition in
[`schemas/identifiers.schema.json`](schemas/identifiers.schema.json) enforces
the grammar and the bounds — the scheme's charset and 32-character cap, a
non-empty value, and the derived 353 `maxLength`. It cannot enforce
normalization of the *value*, because the value is opaque within its scheme and
a whitespace-trimming rule is not expressible in a pattern that must also admit
quoted local parts. `"email: alice@example.com"` is therefore a shape the schema
accepts and a conforming producer never writes. Schema validation is a
necessary check, not a sufficient one; §[Normalization rules](#normalization-rules)
is the rest of the obligation.

### Conformance vectors

The normative cases live in [`testdata/persons/`](testdata/persons):
`valid/` vectors carry the identifier, the scheme and value it splits into, its
normalized form, and identifiers it must and must not compare equal to;
`invalid/` vectors carry an identifier the grammar or the bounds reject, with
`invalid/index.json` recording why. Between them they pin first-colon parsing
with a quoted local part, cross-scheme non-equality, case and whitespace
normalization, unknown-scheme preservation, a maximal-length value and one code
point more, a value that crosses the bound only *before* normalization, and an
over-long scheme.

Fold-level behaviour is pinned separately, by
[`fixtures/testdata/descriptions/fold-person-schemes.yaml`](fixtures/testdata/descriptions/fold-person-schemes.yaml)
(schemes never unify; an unknown scheme folds like any other) and
[`fold-person-normalization.yaml`](fixtures/testdata/descriptions/fold-person-normalization.yaml)
(denormalized identifiers fold to one member).

### Relationship to `writer-id`

Writ clearly separates device-scoped physical namespaces from collaborative actor
identities:

| Concept | Format | Scope | Purpose |
| --- | --- | --- | --- |
| **`writer-id`** | 16 lowercase hex characters (`^[0-9a-f]{16}$`) | Device-scoped `(user, device)` | Git ref namespace (`refs/writ/<writer-id>/`) for append-only concurrent writes without locking. |
| **`person-id`** | `scheme ":" value`, normalized | Minted by a team or repository; stable across that actor's devices and repositories | Collaborative actor identity (assignee, reviewer, voter) across multiple devices and repositories. |

A single person (e.g. `email:alice@example.com`) may author ops from multiple
machines and devices, each with its own distinct `writer-id` (e.g. laptop
`4d8a23b35dd50102` and desktop `0123456789abcdef`). The `writer-id` partitions
the git refspace; the `person-id` identifies the collaborative actor. A
`writer-id` is never a person identifier: it has no scheme, and substituting
one would be a bare identifier, which is invalid.

A producer MUST NOT write a `writer-id` where a `person-id` is expected, and
MUST NOT derive one identifier from the other. The format objection above is
not the only one: the two identifiers have different scopes. A `person-id`,
minted by a team or repository, names one collaborative actor across all of
that actor's devices and repositories, while a `writer-id` names
`(user, device)` — so the person above holds two of them. Substituting a
`writer-id` therefore splits one human into two collaborative actors: two
assignees, two voters, and — because
approval fold is scoped by the key `[subject, revision]`
([`spec/review-ops.md`](review-ops.md) §Fold Implications & Merge Strategies) —
two approvers on the same revision, neither of whom is the person who
approved it.

Writ clients derive the local person identifier from git configuration:
`writ.personId` when set, otherwise `email:` followed by the normalized
`user.email`. A client that can derive neither MUST report that rather than
inventing an identifier.

### Identity mapping out of scope

Mapping cryptographic signing keys (SSH/GPG) or person email addresses to
central directory identities (such as LDAP, SSO, or corporate IAM) is
**deliberately out of scope** for the open format and specification
(ARCHITECTURE.md §Known-hard list, "identity mapping"). So is mapping one
scheme's identifier to another's: a registry that knows `user:alice` is
`email:alice@example.com` is a legitimate thing for a client or a coordination
service to hold, and is not part of the format.

Writ's open format records what was declared in op payloads and verifies
tamper-evident commit signatures; organizational authority and identity verification
policies belong to the hosting forge or coordination service.

## Repository designators (`repo-id`)

A repository designator uniquely identifies a git repository.

```jsonc
"a1b2c3d4e5f60718293a4b5c6d7e8f90"
```

A repository designator MUST be **32 lowercase hexadecimal characters**
(`^[0-9a-f]{32}$`), minted once using 128 bits of CSPRNG entropy when
`writ init` initializes the repository.

The repository designator is **immutable**: it remains unchanged when a
repository is renamed, transferred between organizations, cloned, or mirrored
across different remotes.

### Storage and discovery

To allow a local clone to determine its own repository identity:

1. **Ref carrier (`refs/writ/meta/repo-id`):** The repository designator is
   recorded in git under `refs/writ/meta/repo-id` as a lightweight ref (or
   single commit holding the repo ID). Because it lives under `refs/writ/*`,
   it is fetched by standard `git fetch` operations, ensuring a fresh clone
   immediately knows its repository ID.
2. **Config cache (`writ.repo-id`):** `writ init` records the repository
   designator in `.git/config` under the key `writ.repo-id`. When present,
   engines and tools MAY read `writ.repo-id` from local config as a fast cache
   to avoid reading git ref objects on every invocation. If `.git/config` lacks
   the key, the engine reads `refs/writ/meta/repo-id` and populates the config
   cache.

### Object homing

A collaborative object's operations MUST live under the `refs/writ/*` of
exactly one repository — the one the client was operating on when the object
was created. This is structural, not aesthetic: per-writer refs make pushes
conflict-free only because everyone collaborating on an object pushes to and
fetches from the same remote; an object whose ops could accumulate across
several repositories would need something to replicate them between remotes,
and there is deliberately no such thing (ARCHITECTURE.md §Object homing,
WRIT-180; supersedes WRIT-113).

Workflow states ([`spec/workflow-state-ops.md`](workflow-state-ops.md)),
labels ([`spec/label-ops.md`](label-ops.md)), and settings
([`spec/settings-ops.md`](settings-ops.md)) are repo-global: this
specification defines no `team` object type and no team scoping field in v1.
If team scoping is introduced later, it MUST arrive additively: a `team`
object type plus an optional scoping field on affected objects, such that
existing scopeless objects fold as belonging to a default team and older
clients degrade to ignoring the unknown field per
[`spec/forward-compatibility.md`](forward-compatibility.md) — never a
breaking change to existing payloads.

## Reference grammar

A reference points to a collaborative object. References appear in op bodies
(for example, an issue linking to a fixing review, or a comment replying to a
review).

### Syntax

A reference string MUST take one of two forms:

```text
reference = [ repo-id "#" ] object-id
```

| Form | Syntax | Meaning | Example |
| --- | --- | --- | --- |
| **Bare reference** | `<object-id>` | Points to an object in the **same repository** carrying the referencing operation. | `0123456789abcdef0123456789abcdef` |
| **Fully-qualified reference** | `<repo-id>#<object-id>` | Points to an object in the repository identified by `<repo-id>`. | `a1b2c3d4e5f60718293a4b5c6d7e8f90#0123456789abcdef0123456789abcdef` |

### Rules

1. **Same-repo scoping:** When an operation references an object in the same
   repository (for example, a comment or approval referencing a review in the
   same repo), producers SHOULD emit the **bare reference** form (`<object-id>`).
   This keeps local references compact and independent of repository
   designators.
2. **Cross-repo scoping:** When an operation references an object in a different
   repository, producers MUST emit the **fully-qualified reference** form
   (`<repo-id>#<object-id>`).
3. **Normalization:** References MUST be lowercase-normalized. All hexadecimal
   digits in `<repo-id>` and canonical `<object-id>` MUST be lowercase ASCII
   characters (`0-9`, `a-f`). This guarantees that references are
   **byte-comparable**, which is required because deterministic fold reducers
   use string comparison of IDs and references as deterministic tiebreakers.
4. **No interior whitespace:** References MUST NOT contain whitespace, control
   characters, or multiple `#` characters.

### Length bound

A reference string is at most **289 code points**. The bound is a resource
bound on a string stored permanently in an append-only log, exactly as the
person-identifier bound is, and it is likewise enforced **by rejection, never
by truncation** — a truncated reference points at a different object, or at
nothing.

The bound is derived from the reference grammar and envelope constraints:
32 lowercase hexadecimal characters for `repo-id`, 1 delimiter character (`#`),
and at most 256 characters for `object_id` (bounded by
[`schemas/op-envelope.schema.json`](schemas/op-envelope.schema.json)), yielding
32 + 1 + 256 = 289 code points. Stating the derived bound gives validators
operating on flat reference strings an exact ceiling without requiring
multi-field inspection.

The unit is code points, and the distinction is observable here rather than
theoretical: `reference`'s pattern is `^([0-9a-f]{32}#)?[^#\s]+$`, whose
second half admits non-ASCII. Most bounded strings in the schemas are paired
with an ASCII-only pattern, which makes the units indistinguishable and the
question moot; `person-id`, `reference` and the anchor schema's `line` (WRIT-154)
are the three that are not. The bound is bracketed at 288 / 289 / 290 in all
three units under [`testdata/references/`](testdata/references/), and
`TestReferenceLengthUnitIsCodePoints` computes the witness that separates each
unit from the corpus itself.

Producers do not carry a second copy of this number. `codec.BuildCommit`, the
only constructor of an op commit, validates every body against its vocabulary
schema (WRIT-129), and the schemas `$ref` this definition — so a producer that
tried to write an over-long `target` is refused before the commit exists, by
the same 289 the fixtures enforce.

### Short forms and presentation

In human-facing user interfaces (CLI, TUI, web), clients MAY display shortened
prefixes of object IDs (e.g. `a1b2c3d`) or replace `<repo-id>` with the
human-readable repository slug (e.g. `writ#a1b2c3d` or `writtendev/writ#a1b2c3d`).

Short forms are strictly a client display concern. Canonical references stored
in op payloads MUST always use full, unabbreviated identifiers.

## Out of scope

The following concerns are explicitly out of scope for this specification:

- **Permission semantics:** Access control, branch protection, and
  write permissions are governed by the underlying git transport and hosting
  provider (e.g. SSH keys, forge repository permissions). Writ defines no
  custom server-side permission or ACL system. Tracked in ARCHITECTURE.md
  §Known-hard list ("repo permission semantics").
- **Identity mapping:** Mapping cryptographic signing keys (SSH/GPG) to
  directory identities (LDAP, SSO, email) is tracked in ARCHITECTURE.md
  §Known-hard list.
- **Backlink indexing:** References are strictly one-directional in op payloads
  (e.g. issue → review). Bidirectional links ("find all reviews addressing this
  issue") are computed dynamically by the local SQLite projection index, not
  recorded in op graphs.
- **Cross-repo transactional atomicity:** Git operations are per-repository.
  Cross-repository references are eventually consistent across repository syncs;
  no distributed 2-phase commit or transactional locking across distinct git
  repositories is attempted.
