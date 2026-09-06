# Fixture Storage, Generation & Golden Test Harness (WRIT-56, WRIT-59)

The conformance corpus's fixtures are *git repositories* — multi-writer
DAGs, force-pushed histories, signed commits. A `.git` directory can't be
committed as a normal tree inside this monorepo, so this package provides
both the storage/generation tooling and the **golden-file test harness**
that enforces spec correctness across all fixture families.

## Architecture

1. **Declarative Storage:** Each fixture repo is defined by a human-readable
   YAML description in `testdata/descriptions/`. Everything — refs, commits,
   trees, file contents, signers, force-pushes — is reviewable in a pull
   request diff.
2. **Deterministic Generator:** `Generate` builds a bare git repo into a
   temporary or output directory. Commit timestamps are explicit UTC RFC 3339,
   and commit signatures use deterministic ed25519 keys (RFC 8032) from `keys/`.
3. **Golden Test Harness:** `harness.go` loads descriptions, generates repos
   into `t.TempDir()`, runs them through the target system (codec, fold,
   projection), and validates output byte-for-byte against `testdata/golden/`.

## Declarative Description Knobs

Fixture YAML descriptions under `testdata/descriptions/` support the following commit-level knobs:

- **`id` & `parents`:** Commit labeling and DAG parent resolution. `parents: [labels]` resolves against previous commit IDs across the description. Omitted parents fall back to the implicit linear chain.
- **`op:` block:** Structured operation payload (`object_id`, `object_type`, `op_type`, `op_version`, `body`, plus optional arbitrary extra fields). Automatically canonicalized into `op.json` at file mode `100644` with a derived commit message `writ: <op_type> <object_type>/<object_id>\n`. Mutually exclusive with raw `files:` and `message:`.
- **`sign_as:`** Sign with a named identity's key (e.g. `bob`) other than the author's own key.
- **`tamper:`** Closed enum applied post-signing to simulate malformed/tampered commits while retaining the original signature: `payload-byte`, `message`, `author`, `signature`, `op-json-mode-exec`.
- **`unsigned: true`:** Omit the commit signature header.
- **`committer:`** Override committer identity to test reader rejection of author/committer divergence.
- **`expect:`** Declared machine-readable expectation: `accept` or `{reject: <reason>}` from the closed rejection reason set (`wrong-key`, `payload-mutated`, `corrupted-signature`, `unsigned`, `non-canonical-payload`, `duplicate-key`, `lone-surrogate`, `schema-violation`, `extra-tree-entry`, `op-json-subdirectory`, `missing-op-json`, `invalid-op-json-mode`, `committer-mismatch`).
- **`disposition:`** Declared forward-compatibility classification under the reader capability profile: `interpretable` or `opaque` (from the closed enum), only meaningful alongside an `op:` block.
- **`resolutions:`** Top-level list of resolution cases declaring an anchor's source (`at` commit label, `path`, optional `range`, `side`), `target` commit label, and `expect` outcome (`resolved` with `match` rung or `orphaned` with `reason`, or cross-side/status expectations). Anchors are captured from the generated source tree and resolved against the target tree.

## The Fixture Families

- **`manifest`:** Pinned repository manifest outputs (`testdata/golden/*.json`) covering all generated refs, commits, SHAs, and trees.
- **`envelope`:** Golden envelope outputs (`testdata/golden/envelope/*.json`) verifying byte-for-byte canonicalization, schema conformance, tree structure, pure-Go SSH signature verification (`codec.Verify`), and declared vs observed disposition equality.
- **`forward-compat`:** Golden forward-compatibility outputs (`testdata/golden/forward-compat/*.json`) verifying that unknown op types, future op versions, and unknown fields are preserved byte-for-byte, classified according to the reader profile, and surfaced as opaque records without perturbing known state.
- **`fold`:** Golden folded state outputs (`testdata/golden/fold/*.json`) verifying that concurrent field edits, multi-device writer races, LWW and tiebreak rules, per-field merge strategies, and ancestry truncation reduce deterministically to byte-identical folded states across writers and DAG permutations.
- **`orphan-anchors`:** Golden resolution outputs (`testdata/golden/orphan-anchors/*.json`) verifying pure anchor resolution (`resolve.Resolve`) across real git history rewrites (rebase, rename, file and line deletion, hunk drift, force-push), checking matching ladder rungs, orphan degradation reasons, overall status derivation, schema validity, and byte-identical orphan preservation.

## The Golden-File Test Harness

The test harness (`fixtures.Run` and `fixtures.RunFamily`) is the shared
machinery that every fixture family and fold test runs through.

### One-Line Registration for New Fixture Families

Registering a new fixture family in a Go test requires a single call:

```go
func TestManifestFamily(t *testing.T) {
    fixtures.RunFamily(t, "manifest", func(t *testing.T, fix *fixtures.Fixture) ([]byte, error) {
        return json.MarshalIndent(fix.Manifest, "", "  ")
    })
}
```

For advanced use cases (custom corpus directories, filters, or specific golden
paths), use `fixtures.Run`:

```go
func TestFoldFamily(t *testing.T) {
    fixtures.Run(t, fixtures.Family{
        Name:      "fold",
        GoldenDir: "testdata/golden/fold",
        Filter: func(desc *fixtures.Description) bool {
            return strings.HasPrefix(desc.Name, "fold-")
        },
        Runner: func(t *testing.T, fix *fixtures.Fixture) ([]byte, error) {
            return foldState(fix.Repo)
        },
    })
}
```

Each registered family automatically executes each fixture in its own subtest
(`t.Run(desc.Name)`), generating a clean bare git repo in `t.TempDir()` and
passing a populated `*fixtures.Fixture` (name, description, repo directory,
go-git repo instance, and manifest) to the runner.

### Byte-for-Byte Comparison with Readable Diffs

Comparison against golden files is strictly byte-for-byte (`bytes.Equal`).
On failure, the harness generates a clear unified diff showing the exact line
numbers, context, additions, and deletions:

```
--- testdata/golden/linear-history.json (golden)
+++ got (actual output)
@@ -10,3 +10,3 @@
-      "author": "Alice Example <alice@example.test>",
+      "author": "Bob Example <bob@example.test>",
```

### Deliberate Update-Goldens Flow

Spec changes must be deliberate, never accidental drift. Updating golden files
requires an explicit flag:

```bash
# In Go tests:
go test ./spec/fixtures/... -update-golden

# Or via the generation CLI:
go run ./spec/fixtures/gen -update-golden
```

When updating goldens:
- **New goldens:** created and logged with `[NEW GOLDEN] created <path> (<N> bytes)`.
- **Unchanged goldens:** logged with `[UNCHANGED] <path>`.
- **Modified goldens:** written to disk and **the exact unified diff is printed**
  (`[UPDATED GOLDEN] <path>:\n<diff>`), ensuring that the diff is immediately
  visible in test logs and review output.

## Regenerating the Corpus via CLI

```bash
go run ./spec/fixtures/gen [-out DIR] [-update-golden]
```

- With no flags: builds every description into `spec/fixtures/out/<name>/`
  (gitignored bare repos, inspectable with plain git) and checks each manifest
  against its golden file. A mismatch prints a readable diff and exits non-zero.
- With `-update-golden`: writes/updates golden files and prints the exact diff
  of what changed.

## Independent Implementation Reuse

An independent implementation (e.g. in Rust, Python, TypeScript) can reuse
this corpus to verify compatibility:

1. Run `go run ./spec/fixtures/gen -out /path/to/repos` to build the fixture
   git repositories.
2. For each repository, load its refs under `refs/writ/` and fold all
   operations into materialized state.
3. Serialize the folded state to canonical JSON.
4. Compare byte-for-byte against the golden files in
   `spec/fixtures/testdata/golden/`.
