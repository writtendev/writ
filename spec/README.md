# Spec & Conformance Fixtures

Per ARCHITECTURE.md ("Spec = fixtures"), the conformance fixtures are the
ground truth for the Writ specification. Prose describes intent, but
the conformance corpus defines observable correctness byte-for-byte.

## Spec Files

What lives here, and with what force. **Normative** files define
conformance: an implementation that disagrees with them is wrong.
**Informative** files explain, index, or generate; disagreeing with them
is a documentation bug, not a conformance failure. Machine-readable
files are the stronger half of the spec — when prose and fixtures
disagree, the fixtures win and the prose gets fixed.

**Pins** (on `testdata/` rows only) states which half of the producer/reader
split (§What each corpus pins, below) the row belongs to: **producer**
pins what a producer MUST NOT emit (payload instances checked against JSON
Schema, no fold runs); **reader** pins what a reader MUST do when folding
or resolving an already-written log, including tolerating input a
producer would have refused. A blank cell means the split does not apply
(prose, schema files, informative bridge vectors, and Go tooling).

| File | Force | Pins | Contents |
| --- | --- | --- | --- |
| `op-envelope.md` | Normative | | The op envelope: which fields live on the op commit vs. in the `op.json` payload, op-id derivation, reader validation rules |
| `signing.md` | Normative | | Op signature scheme, signing payload, verification mechanics, allowed_signers trust store, and outcome vocabulary |
| `ref-layout.md` | Normative | | Chain ref layout, writer-id convention, edge rules, and init refspecs |
| `canonicalization.md` | Normative | | Byte-stable canonical JSON encoding: ordering, escaping, number formatting, rejection rules |
| `forward-compatibility.md` | Normative | | Unknown-op handling, forward compatibility, version-bump semantics, and round-trip preservation rules |
| `anchors.md` | Normative | | Content-based comment anchors (v1): the dual-sided anchor object, context capture, re-anchoring and orphaning |
| `fold.md` | Normative | | Fold semantics: the input model, causality-monotone effective time `t*`, the deterministic total order, concurrency rules, the closed per-field merge strategy catalogue, tombstones, and state serialization |
| `identifiers.md` | Normative | | Globally unique object IDs, repo designators, cross-repo references, and person identifiers |
| `ordering.md` | Normative | | Fractional indexing & shared ordering primitive: byte-comparable base-62 strings, canonical form, boundary generation, and op-id tiebreak |
| `value-types.md` | Normative | | The closed value-type catalogue (v1): a schema's second, orthogonal axis alongside `fold.md`'s merge strategies — encoding, validation, and normalization per type |
| `review-ops.md` | Normative | | The review family operation vocabulary (v1): review creation, revisions, status transitions, assignments, approvals, and CI statuses |
| `comments.md` | Normative | | Comment op vocabulary (v1): object model, create/edit/delete ops, threading, anchor reference, GitHub shapes |
| `issue-ops.md` | Normative | | The issue family operation vocabulary (v1): issue creation, metadata updates, state transitions, assignments, labels, and cross-references (Appendix B: Linear schema mapping) |
| `project-cycle.md` | Normative | | Project and cycle operation vocabularies (v1): repo-scoped grouping types, creation, status transitions, cycle dates, and issue membership |
| `resolution.md` | Normative | | Re-anchoring & orphan degradation (v1): the `resolve(anchor, tree)` ladder, tiebreaks, thresholds, and orphan semantics |
| `schema-ops.md` | Normative | | The `schema` object type (v1): writ's one hard-coded vocabulary — namespace/`object_type` binding, envelope binding, `create`/`define-type`/`define-field`/`define-op`/`deprecate-type`/`deprecate-field`, bootstrap order, conflicts, evolution, and rule validation |
| `schemas/op-envelope.schema.json` | Normative | | JSON Schema (draft 2020-12) for the payload half of the envelope |
| `schemas/anchor.schema.json` | Normative | | JSON Schema (draft 2020-12) for the anchor object |
| `schemas/identifiers.schema.json` | Normative | | JSON Schema (draft 2020-12) for identifiers and references |
| `schemas/ordering.schema.json` | Normative | | JSON Schema (draft 2020-12) for the fractional index position key |
| `schemas/value-types.schema.json` | Normative | | JSON Schema (draft 2020-12) for the closed value-type catalogue, one `$defs` entry per type |
| `schemas/review-ops.schema.json` | Normative | | JSON Schema (draft 2020-12) for the review operations family |
| `schemas/comment.schema.json` | Normative | | JSON Schema (draft 2020-12) for comment op payloads |
| `schemas/issue-ops.schema.json` | Normative | | JSON Schema (draft 2020-12) for the issue operations family |
| `schemas/project-ops.schema.json` | Normative | | JSON Schema (draft 2020-12) for project operation payloads |
| `schemas/cycle-ops.schema.json` | Normative | | JSON Schema (draft 2020-12) for cycle operation payloads |
| `schemas/resolution.schema.json` | Normative | | JSON Schema (draft 2020-12) for the resolution outcome object |
| `schemas/field-rules.schema.json` | Normative | | JSON Schema (draft 2020-12) for field merge rule declarations (`field-rules.json`) |
| `schemas/schema-ops.schema.json` | Normative | | JSON Schema (draft 2020-12) for the schema operations family |
| `testdata/canonicalization/vectors.json` | Normative | producer | Canonicalization test vectors: input → exact canonical bytes, or input → rejection |
| `testdata/ordering/vectors.json` | Normative | producer | Fractional indexing test vectors: generation across boundaries, canonical validation, and comparison |
| `testdata/ref-names/vectors.json` | Normative | producer | Ref-naming test vectors (valid/invalid) and pinned refspecs |
| `testdata/envelopes/` | Normative | producer | Envelope payload instances, valid and invalid; `invalid/index.json` records each expected rejection |
| `testdata/forward-compat/` | Normative | reader | Forward-compatibility instances (unknown types, future versions, unknown fields) and synthetic reader profile (+ index.json) |
| `testdata/anchors/valid/`, `testdata/anchors/invalid/` | Normative | producer | Anchor instances; `invalid/index.json` records each expected rejection and whether the schema or an invariant catches it |
| `testdata/anchors/github/` | Informative | | GitHub-position conversion vectors, illustrating a mapping whose enforcement lives in the bridge rather than here |
| `testdata/fold/order/`, `testdata/fold/merge/` | Normative | reader | Fold test vectors: deterministic total order test vectors and merge strategy vectors |
| `testdata/value-types/valid/`, `testdata/value-types/invalid/` | Normative | producer | A valid and invalid instance per catalogue value type; `invalid/index.json` records each expected rejection |
| `testdata/references/valid/`, `testdata/references/invalid/` | Normative | producer | Reference instances; `invalid/index.json` records each expected rejection |
| `testdata/review-ops/valid/`, `testdata/review-ops/invalid/` | Normative | producer | Review operation payload instances; `invalid/index.json` records each expected rejection |
| `testdata/review-ops/field-rules.json` | Normative | reader | Field-by-field fold merge strategy declarations for the review op vocabulary |
| `testdata/review-ops/github/` | Informative | | GitHub PR, review, status, and check-run conversion vectors |
| `testdata/comments/valid/`, `testdata/comments/invalid/` | Normative | producer | Comment op payload instances; `invalid/index.json` records each expected rejection |
| `testdata/comments/github/` | Informative | | GitHub comment conversion vectors (top-level, inline, reply) |
| `testdata/issue-ops/valid/`, `testdata/issue-ops/invalid/` | Normative | producer | Issue operation payload instances; `invalid/index.json` records each expected rejection |
| `testdata/issue-ops/field-rules.json` | Normative | reader | Field-by-field fold merge strategy declarations for the issue op vocabulary |
| `testdata/issue-ops/github/` | Informative | | GitHub issue conversion vectors (opened, labeled/assigned, closed-as-not-planned) |
| `testdata/project/valid/`, `testdata/project/invalid/` | Normative | producer | Project operation payload instances; `invalid/index.json` records each expected rejection |
| `testdata/project/field-rules.json` | Normative | reader | Field-by-field fold merge strategy declarations for the project op vocabulary |
| `testdata/cycle/valid/`, `testdata/cycle/invalid/` | Normative | producer | Cycle operation payload instances; `invalid/index.json` records each expected rejection |
| `testdata/cycle/field-rules.json` | Normative | reader | Field-by-field fold merge strategy declarations for the cycle op vocabulary |
| `testdata/schema-ops/valid/`, `testdata/schema-ops/invalid/` | Normative | producer | Schema operation payload instances; `invalid/index.json` records each expected rejection (schema, invariant, or canonicalization) |
| `testdata/schema-ops/field-rules.json` | Normative | reader | The bootstrap field merge rules for the schema vocabulary itself — the one rule table that never comes from the log |
| `testdata/schema-rules/matrix.json` | Normative | reader | The rule-validation matrix (WRIT-190, axis D): every (strategy, value type) cell, including untyped, asserted against `spec.ValidateFieldRule` — the gate `engine/schema.go`'s `RulesFromSchemas` applies to every rule sourced from the log, since a reader has no producer step to lean on |
| `testdata/resolution/` | Normative | reader | Resolution test vectors (`cases/*.json`) and outcome index (`index.json`) |
| `fixtures/testdata/golden/schema/` | Normative | reader | Signed-fixture golden family driving the typed `writ.FoldSchema` reducer directly (`spec/fixtures/schema_test.go`): bootstrap, the §3.1 non-canonical `op_version` quarantine, multi-writer concurrent declarations, deprecate/redeclare, and schema-level forward compatibility |
| `fixtures/testdata/golden/schema-driven/` | Normative | reader | Signed-fixture golden family (`spec/fixtures/schemadriven_test.go`) folding ordinary objects through rules resolved from the log itself (`writ.FoldSchema` → `writ.RulesFromSchemas` → `writ.Fold`): absent/uninterpretable, object_type/namespace/redefine-schema conflicts, version-bump target remedy (both halves), person-ref normalization across every strategy, invalid-rule dropping, and body-vocabulary tolerance |
| `spec.go` | Informative | | Go embedding of `schemas/` and `testdata/` so every consumer reads the one committed copy |
| `foldvectors.go` | Informative | | Go loader and structural validation for fold ordering and merge test vectors |
| `resolutionvectors.go` | Informative | | Go loader and structural validation for resolution test cases |
| `fixtures/` | Mixed | | The fixture-repo generator and golden harness (informative tooling) producing the golden corpus under `fixtures/testdata/` (normative) |
| `README.md` | Informative | This document: index, conformance model, independent-implementation guide |

### Schema Identity ($id)

Every schema under `schemas/` must declare a `$id` URI of the form `https://writ.dev/spec/<filename>` matching its file name (for example, `https://writ.dev/spec/op-envelope.schema.json`), because spec identity is independent of repository hosting. Absolute `$ref` references between schemas cite these `$id` URIs directly.

## Repository Layout

```
spec/
├── README.md               — this document: conformance model & independent implementation guide
├── op-envelope.md          — normative: the op envelope, field by field
├── signing.md              — normative: op signature scheme, verification mechanics, trust store, and outcome vocabulary
├── ref-layout.md           — normative: chain ref layout, writer-id convention, and refspecs
├── canonicalization.md     — normative: canonical JSON encoding rules
├── forward-compatibility.md — normative: unknown-op and forward-compatibility rules
├── anchors.md              — normative: content-based comment anchors (v1)
├── fold.md                 — normative: fold semantics, total order, and merge strategy catalogue
├── identifiers.md          — normative: globally unique object IDs, references & person identifiers
├── ordering.md             — normative: fractional indexing & shared ordering primitive
├── value-types.md          — normative: the closed value-type catalogue (v1)
├── review-ops.md           — normative: review family operation vocabulary (v1)
├── comments.md             — normative: comment op vocabulary (v1)
├── issue-ops.md            — normative: issue family operation vocabulary (v1), Linear mapping (Appendix B)
├── project-cycle.md        — normative: project & cycle grouping op vocabularies (v1)
├── resolution.md           — normative: re-anchoring & orphan degradation (v1)
├── schema-ops.md           — normative: the `schema` object type (v1), writ's one hard-coded vocabulary
├── spec.go                 — go:embed of schemas/ and testdata/ (package spec)
├── foldvectors.go          — loader and structural validation for fold test vectors
├── resolutionvectors.go    — loader and structural validation for resolution test cases
├── schemas/
│   ├── op-envelope.schema.json — draft 2020-12 schema for the op payload
│   ├── anchor.schema.json  — draft 2020-12 schema for the anchor object
│   ├── identifiers.schema.json — draft 2020-12 schema for identifiers & references
│   ├── ordering.schema.json — draft 2020-12 schema for the fractional index position key
│   ├── value-types.schema.json — draft 2020-12 schema for the closed value-type catalogue
│   ├── review-ops.schema.json  — draft 2020-12 schema for review operations
│   ├── comment.schema.json — draft 2020-12 schema for comment op payloads
│   ├── issue-ops.schema.json — draft 2020-12 schema for issue operations
│   ├── project-ops.schema.json — draft 2020-12 schema for project operation payloads
│   ├── cycle-ops.schema.json — draft 2020-12 schema for cycle operation payloads
│   ├── resolution.schema.json — draft 2020-12 schema for the resolution outcome object
│   ├── field-rules.schema.json — draft 2020-12 schema for field merge rule declarations
│   └── schema-ops.schema.json — draft 2020-12 schema for schema operations
├── testdata/
│   ├── canonicalization/   — encoding vectors (valid and rejected inputs)
│   ├── ordering/           — fractional indexing test vectors (validation, generation, comparison)
│   ├── ref-names/          — ref-naming vectors (valid/invalid) and pinned refspecs
│   ├── envelopes/          — payload instances: valid/ and invalid/ with index.json
│   ├── forward-compat/     — forward-compatibility test corpus: ops/, reader-profile.json, index.json
│   ├── anchors/
│   │   ├── valid/          — anchors that must validate
│   │   ├── invalid/        — anchors that must be rejected (+ index.json of reasons)
│   │   └── github/         — informative GitHub-position conversion vectors
│   ├── fold/
│   │   ├── order/          — deterministic total order vectors
│   │   └── merge/          — per-field merge strategy and interleaving vectors
│   ├── value-types/
│   │   ├── valid/          — a valid instance per catalogue value type
│   │   └── invalid/        — a rejected instance per catalogue value type (+ index.json)
│   ├── references/
│   │   ├── valid/          — reference vectors that must validate
│   │   └── invalid/        — invalid references & index.json of expected rejections
│   ├── persons/
│   │   ├── valid/          — person identifiers with their split, normalization and equality
│   │   └── invalid/        — person identifiers the grammar or bounds reject (+ index.json)
│   ├── review-ops/
│   │   ├── valid/          — review op instances that must validate
│   │   ├── invalid/        — review op instances that must be rejected (+ index.json)
│   │   ├── field-rules.json — fold merge strategies per field
│   │   └── github/         — informative GitHub PR/review/status conversion vectors
│   ├── comments/
│   │   ├── valid/          — comment payloads that must validate
│   │   ├── invalid/        — comment payloads that must be rejected (+ index.json of reasons)
│   │   └── github/         — informative GitHub comment conversion vectors
│   ├── issue-ops/
│   │   ├── valid/          — issue op instances that must validate
│   │   ├── invalid/        — issue op instances that must be rejected (+ index.json)
│   │   ├── field-rules.json — fold merge strategies per field
│   │   └── github/         — informative GitHub issue conversion vectors
│   ├── project/
│   │   ├── valid/          — project op instances that must validate
│   │   ├── invalid/        — project op instances that must be rejected (+ index.json)
│   │   └── field-rules.json — fold merge strategies per field
│   ├── cycle/
│   │   ├── valid/          — cycle op instances that must validate
│   │   ├── invalid/        — cycle op instances that must be rejected (+ index.json)
│   │   └── field-rules.json — fold merge strategies per field
│   ├── schema-ops/
│   │   ├── valid/          — schema op instances that must validate
│   │   ├── invalid/        — schema op instances that must be rejected (+ index.json)
│   │   └── field-rules.json — the bootstrap fold merge strategies for the schema vocabulary itself
│   ├── resolution/
│   │   ├── cases/          — resolution test cases (anchor + target tree -> outcome)
│   │   └── index.json      — case -> outcome/rung mapping
│   └── schema-rules/
│       └── matrix.json     — rule-validation matrix: every (strategy, value_type) cell vs. spec.ValidateFieldRule
└── fixtures/
    ├── README.md           — fixture storage, repo generation, and test harness details
    ├── corpus.go           — loads descriptions from testdata/descriptions/
    ├── description.go      — YAML description parser and schema
    ├── diff.go             — standard unified diff engine for readable failure/update diffs
    ├── envelope_test.go    — envelope fixture family registration & reader validation test
    ├── fold_test.go        — fold fixture family registration & state reduction test
    ├── forwardcompat_test.go — forward-compatibility fixture family registration & preservation test
    ├── generate.go         — deterministic bare git repository builder
    ├── harness.go          — golden-file test harness and fixture family runner
    ├── identity.go         — fixture identities (alice, bob) and signing config
    ├── manifest.go         — manifest data model (SHAs, trees, parents, refs)
    ├── op.go               — OpDesc to canonical payload & commit message derivation
    ├── orphananchors_test.go — orphan-anchors fixture family registration & resolution test
    ├── schema_test.go      — schema fixture family registration & typed FoldSchema golden test
    ├── schemadriven_test.go — schema-driven fixture family: FoldSchema -> RulesFromSchemas -> Fold, golden test
    ├── sign.go             — ed25519 SSH commit signing
    ├── tamper.go           — post-signing commit tampering engine
    ├── tree.go             — git tree object generation with file modes
    ├── verify.go           — SSH allowed-signers trust store and signature verification oracle
    ├── gen/                — CLI command to regenerate fixture repos and check/update goldens
    ├── keys/               — throwaway ed25519 keypairs for deterministic signatures
    └── testdata/
        ├── descriptions/   — declarative YAML descriptions of fixture git histories
        └── golden/         — golden JSON outputs pinned byte-for-byte (manifest/, envelope/, fold/, forward-compat/, and orphan-anchors/)
```

## Conformance Model

Any Writ implementation (the reference Go engine or an independent
implementation in Rust, Python, TypeScript, etc.) is verified by running
the conformance fixture corpus through its pipeline and comparing the output
byte-for-byte against the golden files in `spec/fixtures/testdata/golden/`.

### The Fixture Lifecycle

1. **Declarative Description:** Checked in as human-readable YAML under
   `spec/fixtures/testdata/descriptions/`. Specifies refs, commit chains,
   authors, timestamps, and file contents.
2. **Deterministic Repository Generation:** Built into a bare git repository
   with deterministic ed25519 SSH commit signatures.
3. **Execution Under Test:** The implementation under test reads the
   repository's `refs/writ/*` namespaces, enumerates operations, and folds
   them into materialized state.
4. **Golden Comparison:** The output is compared byte-for-byte against
   `testdata/golden/<name>.json`.

## Reusing the Corpus in an Independent Implementation

An independent implementation can reuse the conformance corpus in one of
two ways. Under types-as-data (ARCHITECTURE.md §Schema layer, WRIT-184),
neither option can assume the implementation holds a vocabulary: `schema`
is the one object type Writ hard-codes, and everything else — including
every field this corpus exercises on an ordinary object — is data a
`schema` object writes into the log. So "execute your fold reducer against
a repo" is not the whole pipeline; the schema-driven pipeline below is:

1. Read `refs/writ/*` and enumerate operations, grouped by `object_id`
   (`spec/ref-layout.md`) — no schema is needed to find schema objects,
   because `object_type` rides the payload.
2. Fold every object whose `object_type == "schema"` with your **built-in
   bootstrap table** — the one rule table your implementation must hard-code,
   because `schema` has to exist before anything else can be typed
   (`spec/schema-ops.md` §Bootstrap). Its published, normative form is
   `testdata/schema-ops/field-rules.json`, byte-for-byte what
   `state.SchemaRules()` returns in the reference engine.
3. Resolve per-`object_type` rules from the folded schema objects: validate
   every candidate field rule (the same checks `spec.ValidateFieldRule`
   encodes — closed strategy catalogue, `keyed-lww`'s key/`key_types`
   requirements, `lattice`'s elements, `tombstone`'s bool-only, `enum`'s
   required values), drop and report the ones that fail rather than
   installing them, and detect the two schema-vs-schema conflicts (an
   `object_type` two schema objects both bind, and a version bump that
   reuses a `target` across a strategy change) per `spec/schema-ops.md` §6
   and §8. Neither conflict kind picks a winner.
4. Fold every other object with the rules resolved for its own
   `object_type`. An `object_type` no schema declares (yet) is not a fold
   error: every op on it reports through `UnknownOps`, exactly as any other
   unknown-op case does (`spec/forward-compatibility.md` FC-1, FC-12).
5. Serialize the folded state (and, for schema objects themselves, the
   folded `Schema` structure and any resolver conflicts) to canonical JSON.
6. Assert the output matches the appropriate `testdata/golden/<name>.json`
   byte-for-byte.

### Option A: Pre-generate Repositories via the Tooling

Generate real git repositories on disk using the reference generator:

```bash
go run ./spec/fixtures/gen -out /tmp/writ-fixtures
```

This creates standard bare git repositories in `/tmp/writ-fixtures/<name>`
that any git library (e.g. `git2-rs`, `pygit2`, `nodegit`, or system git)
can inspect, open, and read.

Your test suite then, per fixture repository:
1. Iterates over each repository in `/tmp/writ-fixtures/`.
2. Reads the refs under `refs/writ/` (or fixture refs).
3. Runs the schema-driven pipeline above: fold the schema objects present
   with your built-in bootstrap table, resolve per-`object_type` rules
   (dropping and reporting invalid ones and conflicts), then fold every
   other object with its type's resolved rules — or, for a fixture with no
   schema object at all, fold every object with no rules and confirm every
   op reports as unknown.
4. Serializes the folded state (and, where the fixture carries one, the
   folded schema state and conflicts) to canonical JSON.
5. Asserts that the output matches `spec/fixtures/testdata/golden/<name>.json`
   byte-for-byte — `testdata/golden/schema/` for the schema family and
   `testdata/golden/schema-driven/` for the schema-driven family.

### Option B: Self-Contained Repository Generation

If your implementation prefers not to depend on the Go generator, you can
reproduce repository generation directly:
1. Parse the YAML descriptions in `spec/fixtures/testdata/descriptions/`.
2. Read the author private keys from `spec/fixtures/keys/`.
3. Construct standard git commit objects with RFC 3339 UTC timestamps.
4. Sign commits using standard OpenSSH ed25519 signatures (`ssh-keygen -Y sign -n git`).
   Because RFC 8032 ed25519 signatures are purely deterministic, your generated
   commits will have the exact same SHAs as the reference manifests.
5. Run the schema-driven pipeline above against the repository you built,
   the same way Option A's test suite runs it against a generated one.

## What each corpus pins: producer versus reader

Two different questions, and this corpus answers both, deliberately kept
apart (see the **Pins** column in the file table above):

- **Producer-side** (`testdata/*/valid/`, `testdata/*/invalid/` across every
  vocabulary, including `testdata/value-types/` and `testdata/schema-ops/`):
  what a conforming producer MUST NOT emit. These are payload instances
  checked against JSON Schema and the invariant checks `invalid/index.json`
  names; no fold ever runs over them.
- **Reader-side** (`testdata/fold/`, `testdata/schema-rules/matrix.json`,
  and the `fixtures/testdata/golden/schema/` and
  `fixtures/testdata/golden/schema-driven/` families): what a reader MUST
  do when folding or resolving an already-written log — including
  *tolerating* input a producer would have refused, because a reader has no
  producer step to lean on and cannot assume the log it reads was ever
  validated. `spec/schema-ops.md` §3.1's non-canonical `op_version`
  quarantine and §9's rule-validation gate are the sharpest instances of
  this split: each has a producer-side fixture already
  (`testdata/schema-ops/invalid/define-field-op-version-leading-zero.json`
  pins the producer half of §3.1), and this ticket is where the reader half
  of both finally gets one too.

On the read path, `value_type` is consulted for exactly one purpose —
`person-ref` normalization (`spec/value-types.md` §Producer-side and
reader-tolerant) — so most of the 117-cell (strategy × value type,
including untyped) cross-product folds byte-identically to its untyped
twin. That is why the cross-product is covered as a **rule-validation
matrix** (`testdata/schema-rules/matrix.json`, asserted against
`spec.ValidateFieldRule`) rather than as one fold fixture per cell: the
matrix pins the 22 cells that are actually illegal (`tombstone` with a
non-`bool` type, `lattice` with a non-`enum` type) and the 95 that are
legal, while the fold corpus stays behavioural — pinning what a legal
combination folds *to*, not merely that it's legal.
