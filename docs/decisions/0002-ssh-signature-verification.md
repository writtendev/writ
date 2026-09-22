# 0002: SSH signature verification approach

Status: decided (WRIT-22). The spec text is `spec/signing.md`, and the
implementation lives in `internal/codec/sshsig` and `internal/codec/verify.go`.

## Problem

Writ stores every operation inside a signed git commit under `refs/writ/*`.
Signature verification must evaluate op authorship and integrity against an
explicit trust store (`allowed_signers`).

The WRIT-3 spike established that verification cannot live inside fold (fold is
pure and deterministic: ops in, state out, no I/O) and must run once at the
ingest boundary. The spike identified two potential approaches for signature
verification: shelling out to `ssh-keygen -Y verify`, or implementing pure-Go
parsing and verification of `PROTOCOL.sshsig` against `golang.org/x/crypto/ssh`.

## Candidates surveyed

**1. Shelling to `ssh-keygen -Y verify`.**
Execute system `ssh-keygen -Y verify -f <allowed_signers> -I <principal> -n git -s <sigfile>`
via subprocess for each commit.
- *Pros:* Offloads wire-format parsing to OpenSSH; proven in the WRIT-3 spike.
- *Cons:* Requires `ssh-keygen` (OpenSSH 8.2+) on `PATH` on the read/verification
  path. Spawns one subprocess per op commit being ingested or verified.
  Most critically, distinguishing between verification failure outcomes
  (`wrong-key` vs. `payload-mutated` vs. `corrupted-signature`) requires scraping
  and matching localized stderr text from `ssh-keygen`, making normative spec
  conformance brittle across platforms, OpenSSH versions, and locales.

**2. Pure-Go verification (`internal/codec/sshsig`).**
Implement wire-format parsing for the OpenSSH armored signature blob
(`PROTOCOL.sshsig`) and OpenSSH `allowed_signers` trust store format in pure Go,
verifying cryptographic signatures via `ssh.PublicKey.Verify` and the standard
library `crypto/sha256` and `crypto/sha512`.
- *Pros:* Fully structural outcome classification (no stderr parsing); no
  subprocess or `PATH` dependency on read path; pure and deterministic;
  fast bulk verification over large op DAG histories.
- *Cons:* Monorepo owns ~200 lines of wire-format parsing and trust-store matching code.

## Decision

Pure-Go verification implemented in `internal/codec/sshsig` and exposed via
`internal/codec/verify.go`.

Reasoning:
- **Structural classification over string scraping.** The fixture corpus and
  conformance goldens explicitly pin a closed outcome vocabulary (`valid`,
  `unsigned`, `wrong-key`, `payload-mutated`, `corrupted-signature`).
  Pure-Go verification classifies outcomes structurally (unparseable blob ->
  `corrupted-signature`; cryptographic check failure -> `payload-mutated`;
  valid crypto but unauthorized key/principal -> `wrong-key`).
- **Read path performance & portability.** Ingesting a multi-writer DAG
  containing hundreds of ops requires no process spawning and operates entirely
  in-memory without depending on `ssh-keygen` existing on `PATH` at runtime.
- **Signing remains subprocess-backed.** While verification is pure Go, signing
  (`internal/codec/sign.go`) intentionally continues to shell out to
  `ssh-keygen -Y sign -n git`. This seamlessly supports agent-held, forwarded,
  and hardware-backed (e.g. YubiKey, Secure Enclave) keys via `SSH_AUTH_SOCK`
  without requiring writ to implement the SSH agent client protocol or private key
  management.
- **Dependency footprint.** `golang.org/x/crypto/ssh` was already a transitive
  dependency via go-git; promoting it to a direct dependency introduces no new
  third-party modules.
