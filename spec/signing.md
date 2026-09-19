# Op Signature and Verification

Status: **normative**. The key words MUST, MUST NOT, SHOULD, and MAY are
to be interpreted as described in RFC 2119.

Every Writ operation is stored as a standard signed git commit under
`refs/writ/*`. Signing uses git's commit-signature machinery (`gpgsig`
header), SSH format (`gpg.format=ssh`). This document defines the wire
format of op signatures, the signed payload, the trust store format, and
the normative verification algorithm and outcome vocabulary.

## Signature Scheme and Namespace

- **Protocol:** Ops are signed using the OpenSSH SSHSIG protocol (`PROTOCOL.sshsig`).
- **Armoring:** Signatures ride the git commit's `gpgsig` header as an armored
  SSHSIG block delimited by `-----BEGIN SSH SIGNATURE-----` and
  `-----END SSH SIGNATURE-----`.
- **Namespace:** Op signatures MUST use the namespace `"git"`. Using the
  standard `"git"` namespace ensures that op commits remain verifiable with
  system `git verify-commit` and standard git hosting tools without modification.
- **Hash Algorithms:** Conforming verifiers MUST support both `"sha512"` and
  `"sha256"` hash algorithms.

## Signed Payload and Op Identity

- **Signed Bytes:** The bytes covered by the signature are the exact commit
  object bytes excluding the `gpgsig` signature header (the byte sequence
  produced by git's `EncodeWithoutSignature`).
- **Op Identity:** The op id is the git commit object identifier (SHA) of
  the **signed** commit object. Two conforming producers given the same logical
  op and signing key generate the same op id.

## Verification Model

### Separation from Fold and Envelope Validation

In accordance with AGENTS.md ("Fold is pure and deterministic: ops in,
state out, no I/O") and WRIT-3:

1. **Fold does not verify signatures:** Signature verification is not part
   of fold and MUST NOT run during fold.
2. **Reader validation is independent:** Envelope reader validation
   ([`spec/op-envelope.md`](op-envelope.md) rules 1–4) validates tree structure,
   payload canonicalization, envelope schema, and author/committer match
   independently of signature state.
3. **Ingest-time verification:** Signature verification is performed at the
   ingest boundary (when an op is read from a ref into the local DAG store).
   Its result travels with the op as data. It never gates whether the op
   folds or is included in the projection: an op folds regardless of its
   verification outcome (AGENTS.md "Fold is pure and deterministic"), the
   same way `git` verifies a commit's signature and reports its status
   without ever refusing to store or show the commit.

### Trust Store and Principal Validation

- **Explicit Input:** Verification is a pure function that accepts an op commit
  and an explicit trust store (`allowed_signers` rules). Verification MUST NOT
  read repository git config or invoke external subprocesses.
- **Principal:** The principal verified against the trust store MUST be the
  commit author's email address (`author.email`).
- **Pattern Matching:** Principal and `namespaces=` matching MUST follow the
  glob-and-negation rule below, with case folding disabled for both lists
  (this is what `sshsig.c`'s `check_allowed_keys_line` gets from OpenSSH's
  `match_pattern_list`/`match_pattern` in `match.c`, stated here on its own
  terms rather than by reference to a particular upstream revision): each
  list is a comma-separated sequence of subpatterns, compared
  case-sensitively over bytes (not runes, so a multi-byte character is
  several match units, not one), where `?` matches exactly one byte and
  every byte that is not `*` or `?` -- `[`, `]`, and `\` included --
  compares literally; there is no character class and no escape. `*`
  matches any run of bytes, including none, and a run of two or more
  consecutive `*` is equivalent to one: in particular, a `*` (or a run of
  `*`) at the end of a subpattern matches even after the rest of the
  subpattern has already consumed the entire value, so `alice@example.test`
  matches both `alice@example.test*` and `alice@example.test**`. A leading
  `!` negates a subpattern, and a negated match rejects the rule outright
  regardless of any other subpattern's outcome. An empty subpattern (as
  from a doubled comma) matches only the empty string. A list matches only
  if at least one **non-negated** subpattern in it matches the value; a
  negated subpattern that matches rejects the list outright as above, but a
  negated subpattern that does *not* match never by itself authorizes
  anything -- it only declines to reject. Consequently a list with no
  non-negated subpattern -- including one made up entirely of negated
  subpatterns that all fail to match -- never matches: `namespaces="!ssh"`
  does not authorize the `"git"` namespace (or any other), because it
  contains no non-negated subpattern for `"git"` to match against.

  OpenSSH's `match_pattern_list` copies each subpattern into a fixed
  1024-byte buffer and, once a subpattern reaches 1023 bytes, aborts the
  entire list -- discarding even an already-recorded match from an earlier
  subpattern in the same list. That is a `match.c` buffer-size artifact,
  not part of the matching semantics, and it is the one place this spec
  deliberately makes writ *more* permissive than real OpenSSH: conforming
  verifiers MUST NOT impose any subpattern length limit, and MUST NOT let
  one subpattern's length affect whether any other subpattern in the same
  list matches.
- **Author Timestamp:** When an `allowed_signers` rule specifies validity
  windows (`valid-after` and `valid-before`), the timestamp checked against
  the window MUST be the commit author's timestamp (`author.when`).
- **Unconfigured Trust Store:** When no trust store is configured or provided,
  a cryptographically valid signature produces the outcome `wrong-key` (reporting
  the key fingerprint), ensuring unconfigured trust is never treated as verified.

## Verification Outcomes

Verifiers MUST report one of the following closed set of outcomes:

| Outcome | Meaning | Valid |
| --- | --- | --- |
| `valid` | Signature cryptographically verifies over the payload, and the signing key is authorized for the author principal in the trust store at the author timestamp. | `true` |
| `unsigned` | The commit does not contain a signature header. | `false` |
| `wrong-key` | Signature cryptographically verifies over the payload, but the signing key is not authorized for the author principal in the trust store (or no trust store is configured). | `false` |
| `payload-mutated` | Cryptographic signature verification failed (the commit payload was modified after signing, or the signature does not match the public key). | `false` |
| `corrupted-signature` | The signature header or binary SSHSIG payload is malformed, truncated, or unparseable. | `false` |

## Format Limitations (v1)

1. **`cert-authority` unsupported:** OpenSSH `allowed_signers` lines specifying
   the `cert-authority` option are not supported in v1 and MUST be skipped
   as untrusted rather than honoured.
2. **SSH only:** PGP signatures are unsupported (`gpg.format=ssh` only).
3. **Key distribution:** Public key distribution and directory-identity mapping
   are out of scope for the op spec (per `ARCHITECTURE.md`).
