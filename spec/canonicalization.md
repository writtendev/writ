# Canonical JSON encoding

Status: **normative**. The key words MUST, MUST NOT, and MAY are to be
interpreted as described in RFC 2119.

Writ signs and content-addresses op payloads, so two independent
implementations given the logically-same value have to produce the same
bytes. This document defines that encoding completely: a second
implementation written from this file alone must agree byte-for-byte
with the reference implementation (`internal/codec/canonicaljson`) on
every input. The machine-checkable form of that claim is the vector file
`spec/testdata/canonicalization/vectors.json`; when prose and vectors
disagree, the vectors win and the prose has a bug to fix.

The algorithm is RFC 8785 (JCS) in shape — UTF-16 key ordering,
ECMAScript number formatting, minimal string escaping — with stricter
input handling: inputs that RFC 8785 pipelines typically normalize
silently (duplicate keys, lone surrogates, malformed UTF-8) are
**rejected** here, because a signature must attest to one unambiguous
value. Background on why the encoding is bespoke rather than a library:
`docs/decisions/0001-canonical-json-encoding.md`.

## Input

The input is a single JSON value as UTF-8 text.

A canonicalizer MUST reject:

- **Input that is not valid UTF-8.** No U+FFFD substitution: a decoder
  that "repairs" malformed bytes and one that doesn't would disagree on
  the output.
- **Objects containing duplicate member keys**, at any nesting depth.
  Keys are compared *after* JSON escape decoding, so `"a"` and `"a"`
  are the same key. Rationale: "last value wins" makes the canonical
  bytes unable to distinguish "no duplicates" from "duplicates silently
  resolved", so a signature could not attest to what the producer meant.
  (Rejection category: `duplicate-key`.)
- **Lone (unpaired) UTF-16 surrogates** in strings or keys: a `\uXXXX`
  escape in U+D800–U+DBFF not immediately followed by an escape in
  U+DC00–U+DFFF, or an escape in U+DC00–U+DFFF not preceded by one in
  U+D800–U+DBFF. These denote no Unicode character; decoders that
  substitute U+FFFD make the result indistinguishable from a legal
  literal U+FFFD. (Rejection category: `lone-surrogate`.)
- **Numbers with no IEEE-754 double-precision representation**: anything
  that overflows to infinity (e.g. `1e400`). `NaN` and `Infinity` have
  no JSON representation to begin with. (Rejection category:
  `non-finite-number`.)
- **Trailing data**: any non-whitespace after the single JSON value —
  including a stray `}` or `]`, which some streaming decoders treat as
  end-of-input rather than as trailing data. (Rejection category:
  `not-one-value`.)
- **Values nested deeper than 10000 levels** of arrays and objects.
  A shared bound is part of the contract: without one, a payload one
  implementation canonicalizes is one another cannot even parse (Go's
  `encoding/json` enforces this same 10000-level ceiling), so validity
  would disagree exactly where signing needs agreement. The 10000th
  nesting level MUST be accepted and the 10001st rejected; only arrays
  and objects nest, so a scalar inside the 10000th bracket is still at
  the 10000th level and MUST be accepted. This rule has no vector
  category — see "Test vectors" below.
- **Input that is not well-formed JSON** (syntax errors). These carry no
  rejection category and are not vectored; they are ordinary parse
  failures.

## Output

The output is the input value re-encoded as UTF-8 with:

- **No insignificant whitespace.** No spaces, tabs, or newlines between
  tokens; no byte order mark; no trailing newline. The output ends at
  the final byte of the value.
- **Object members sorted by their keys in UTF-16 code unit order**,
  comparing the keys' UTF-16 encodings lexicographically, shorter prefix
  first. This is *not* Unicode code point order once keys leave the
  Basic Multilingual Plane: a supplementary-plane character encodes as a
  surrogate pair in U+D800–U+DFFF and therefore sorts *before* BMP
  characters at U+E000 and above. See the "supplementary-plane vs BMP"
  vector. Array element order is preserved as given.
- **Strings minimally escaped.** Escape exactly: `"` as `\"`, `\` as
  `\\`, backspace as `\b`, tab as `\t`, line feed as `\n`, form feed as
  `\f`, carriage return as `\r`, and every other code point below U+0020
  as `\u00xx` with lowercase hex. Every other character — including
  `/`, U+007F, and all non-ASCII — is emitted literally as UTF-8. Escaped
  surrogate pairs in the input are emitted as the literal UTF-8 encoding
  of the character they denote.
- **Numbers formatted by the ECMAScript `Number::toString` algorithm**
  applied to the input literal's nearest IEEE-754 double (ECMA-262
  §6.1.6.1.20, the same choice as RFC 8785 §3.2.2.3): the shortest
  decimal digit string that round-trips to the same double, in plain
  decimal notation for magnitudes in [1e-6, 1e21) and exponential
  notation (`1e+21`, `1e-7`) outside it, with negative zero emitted as
  `0`. There is no integer/float distinction: `5.0` encodes as `5`.

## Consequences to design around

Because numbers are doubles, integers beyond 2⁵³ silently lose
precision (see the `9007199254740993` vector). Any payload field that
needs an exact large integer MUST be typed as a JSON string. Writ's own
schemas follow this rule; `op_version` stays a JSON integer because it
is small by construction.

Canonical output is a **fixed point**: canonicalizing canonical bytes
MUST return them unchanged. Signing and the byte-equality rule in
`spec/op-envelope.md` depend on this; the test suite enforces it for
every vector.

## Test vectors

`spec/testdata/canonicalization/vectors.json` is an array of entries in
one of two forms:

```json
{"name": "...", "input": "<json text>", "canonical": "<exact output>"}
{"name": "...", "input": "<json text>", "error": "<rejection category>"}
```

A conforming canonicalizer MUST produce exactly `canonical` for every
entry of the first form and MUST reject every entry of the second form.
Rejection categories (`duplicate-key`, `lone-surrogate`,
`non-finite-number`, `not-one-value`) classify the reason;
the error surface (message text, error codes) is implementation-defined,
but a test harness SHOULD verify its implementation rejects each entry
for the categorized reason, not merely that it rejects — the reference
implementation's tests do. Plain syntax errors carry no category and are
not vectored, so `not-one-value` means trailing data specifically.

Two rejection rules are not expressible as vectors, and implementations
MUST cover both with their own direct tests, as the reference
implementation does: **invalid UTF-8**, because the vector file's `input`
is itself a JSON string and can only carry valid text; and the
**10000-level depth limit**, whose boundary cases would be tens of
kilobytes of brackets apiece.
