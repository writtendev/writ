# 0001: Canonical JSON encoding approach

Status: decided (spike); promoted to normative by WRIT-6. The spec text
is `spec/canonicalization.md`, the vectors live at
`spec/testdata/canonicalization/vectors.json`, and the three questions
this document deferred to WRIT-6 are resolved — see the annotations in
"What this does and doesn't guarantee" below.

## Problem

Op signing and content-addressing need byte-stable encoding: two producers
building the logically-same op payload must produce identical bytes, or
signatures won't verify and content hashes won't match. JSON has no
canonical form on its own — key order, number formatting, and string
escaping are all underspecified, so `json.Marshal` twice on equivalent data
isn't guaranteed to agree even within one language, let alone across
implementations.

## Candidates surveyed

**RFC 8785 (JSON Canonicalization Scheme / JCS).** The existing standard for
this problem: sorts object members by UTF-16 code unit order, formats
numbers via the ECMAScript `Number::toString` algorithm, and defines minimal
string escaping. Go implementations exist —
[`gowebpki/jcs`](https://github.com/gowebpki/jcs) and
[`gibson042/canonicaljson-go`](https://github.com/gibson042/canonicaljson-go)
are the two with any real adoption; `cyberphone/json-canonicalization`
ships a Go port alongside the reference implementations in several other
languages. All three implement essentially the same ~150-300 line
algorithm; none carries transitive dependencies of its own, so pulling one
in would cost little in dependency-surface terms. Maintenance signal is
thin either way — these are small, stable-by-nature libraries (the RFC
doesn't change), not actively-developed frameworks, so "last commit date"
isn't a meaningful health signal the way it would be for a larger project.

**Bespoke encoder, following RFC 8785's algorithm shape.** Same output
contract (UTF-16 key order, ES-style number formatting, minimal escaping),
implemented directly against `encoding/json`, `unicode/utf16`, and
`strconv` — no third-party code.

## Decision

Bespoke, implemented in `internal/codec/canonicaljson`. Reasoning:

- **House rule is stdlib-first, and this algorithm doesn't strain the
  standard library.** `encoding/json` with `UseNumber()` gives a
  duplicate-key-collapsing decode; `unicode/utf16.Encode` gives exact
  UTF-16 code-unit ordering; `strconv.AppendFloat(f, 'e', -1, 64)` gives
  the shortest round-trip digit string, which the ES `Number::toString`
  reformatting (decimal vs. exponential, threshold at 1e21 / 1e-6) is a
  straightforward transform of. None of this needed a dependency to get
  right — see the "supplementary-plane vs BMP" test vector, which is the
  one genuinely easy-to-get-wrong case (naive Go string comparison sorts
  by UTF-8 byte order, which disagrees with UTF-16 code-unit order exactly
  where it matters) and which the stdlib-only implementation passes.
- **We don't need JS interop.** RFC 8785's number-formatting fidelity to
  `Number::toString` exists so a canonicalizer can agree with a JavaScript
  engine byte-for-byte. Writ has no JS runtime canonicalizing payloads
  anywhere in its architecture — every producer and verifier is this same
  Go package. Matching the RFC's algorithm is still useful (it's a
  well-specified, already-debugged design to copy), but strict RFC 8785
  compliance isn't load-bearing, so a dependency bought us conformance to a
  constraint we don't actually have.
- **Small enough to own.** ~200 lines, and it's exactly the kind of code
  the house rules call out as needing to stay "boring and correct" — a
  vendored dependency doesn't reduce the review burden much below owning it
  directly, since the failure mode (a subtly wrong float or key-order edge
  case) is caught the same way either path: fixtures.

## What this does and doesn't guarantee

- Numbers are IEEE-754 doubles, per JSON's (and RFC 8785's) numeric model.
  Integers beyond 2^53 silently lose precision — see the `9007199254740993`
  test vector. **Resolved (WRIT-6):** the spec adopts the recommendation —
  any op envelope field needing an exact large integer is typed as a JSON
  string, not a JSON number (`spec/canonicalization.md` "Consequences to
  design around"; `op_version` stays a small JSON integer).
- Duplicate object keys used to collapse to the last value during decode
  (Go's `encoding/json` behavior), before canonicalization ever saw them.
  **Resolved (WRIT-6): duplicate keys are rejected outright**, at any
  nesting depth — the canonical bytes must attest to one unambiguous
  value. `Marshal` now decodes token-by-token so duplicates are caught
  before the map collapse can hide them.
- Non-finite numbers (`NaN`, `Infinity`) are rejected — they have no JSON
  or RFC 8785 representation. WRIT-6 additionally rules that lone UTF-16
  surrogates and invalid UTF-8 are rejected rather than U+FFFD-substituted,
  for the same one-unambiguous-value reason.
- **Resolved (WRIT-6):** where canonicalization sits relative to signing is
  defined by `spec/op-envelope.md` — the `op.json` payload blob must be
  byte-identical to the canonical encoding of its own content, and the
  signature covers the commit (which names that blob through its tree),
  so canonicalization happens before the commit is built and signed. This
  package's contract is still just
  `Marshal(json []byte) (canonical []byte, error)`.

## Prototype and test vectors

`internal/codec/canonicaljson/canonicaljson.go`, with vectors covering key ordering
(ASCII, nested, duplicate keys, prefix-key tie-breaks, the UTF-16-vs-UTF-8
surrogate-pair case), string escaping (all five shorthand escapes, other
control characters, unescaped forward slash, raw non-ASCII passthrough),
and numbers (negative zero, plain negatives, the 1e-6/1e21 notation
thresholds exactly at the boundary, scientific-notation input
normalization, precision loss beyond 2^53). `canonicaljson_test.go` checks
each vector round-trips to its expected canonical form and that
canonicalization is idempotent (canonical bytes are a fixed point), which
is what signing depends on.

These vectors seeded WRIT-6's fixture corpus as intended: they now live
at `spec/testdata/canonicalization/vectors.json` (extended with rejection
cases in a `{name, input, error}` variant of the same shape), embedded by
package `spec`, and the canonicaljson tests read that one committed copy.
