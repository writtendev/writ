package writ_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
)

// WRIT-251: signature verification never ran on any read path, so a
// forged or unsigned op folded as if it were as trustworthy as a signed
// one from the object's real owner. These tests pin the fix across the
// full stack Open wires it through: dag.Store.EnumerateSince verifying
// every decoded commit, the outcome riding on codec.Op and never gating
// fold, the projection caching it, and Objects.Get/Query.Object surfacing
// the worst-of summary.

// malloryWriterID is a writer id distinct from any test's own author, used
// throughout as the forger's chain namespace.
const malloryWriterID = identity.WriterID("fedcba9876543210")

// genSSHKey generates a fresh ed25519 keypair in dir and returns the
// private key path and the public key's allowed_signers-ready line.
func genSSHKey(t testing.TB, dir, name string) (privPath, pubLine string) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not found on PATH")
	}
	privPath = filepath.Join(dir, name)
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", privPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen unavailable: %v\n%s", err, out)
	}
	pubBytes, err := os.ReadFile(privPath + ".pub")
	if err != nil {
		t.Fatalf("read generated public key: %v", err)
	}
	return privPath, strings.TrimSpace(string(pubBytes))
}

// writeAllowedSigners writes an OpenSSH allowed_signers file authorizing
// principal for pubLine (or nothing, when pubLine is "") and returns its
// path.
func writeAllowedSigners(t testing.TB, path, principal, pubLine string) string {
	t.Helper()
	content := ""
	if pubLine != "" {
		content = principal + " " + pubLine + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write allowed_signers: %v", err)
	}
	return path
}

// forgeOp writes a raw op commit directly to repo's storer under
// forgerID's own writer chain, unsigned unless sig is set, entirely
// bypassing dag.Store.Append and codec.BuildCommit's producer validation
// and signing path -- exactly the way a real forger with push access but
// no writ client at all would produce one. It is the vulnerability
// WRIT-251 closes: nothing about this path is special-cased by the fix,
// which is the point.
func forgeOp(t *testing.T, repo *git.Repository, forgerID identity.WriterID, objectID, objectType, opType string, opVersion int64, fields map[string]any, sig string, causalParents ...string) plumbing.Hash {
	t.Helper()
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal forged body: %v", err)
	}
	env := codec.Envelope{ObjectID: objectID, ObjectType: objectType, OpType: opType, OpVersion: opVersion, Body: body}
	raw, err := codec.EncodePayload(env)
	if err != nil {
		t.Fatalf("encode forged payload: %v", err)
	}
	// A real forger's commit follows the object's current frontier just
	// like a legitimate append would: that is what makes the forgery
	// dangerous (an object mid-history, not a brand new one), and it is
	// also what gives the forged op a later causal position than what it
	// overwrites, so the fold's lww strategy actually picks it -- pinning
	// that the fold folds it in, not merely that it is present.
	when := time.Now().UTC().Add(time.Second)
	commit := &codec.Commit{
		Parents:   causalParents,
		Author:    codec.Identity{Name: "Mallory", Email: "mallory@evil.example", When: when},
		Committer: codec.Identity{Name: "Mallory", Email: "mallory@evil.example", When: when},
		Message:   codec.Message(env),
		Signature: sig,
		Tree:      []codec.TreeEntry{{Name: "op.json", Mode: "100644", Data: raw}},
	}
	hash, err := codec.WriteCommit(context.Background(), repo.Storer, commit, nil)
	if err != nil {
		t.Fatalf("write forged commit: %v", err)
	}
	ref := plumbing.NewHashReference(dag.LocalRefName(forgerID, objectType), hash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("set forged ref: %v", err)
	}
	return hash
}

// forgeSignedSchemaOp writes a real, correctly-signed op commit of an
// op_type this build does not define for "schema" against objectID,
// under forgerID's own writer chain -- the forward-compatibility case
// (spec/schema-ops.md) of a client that knows a schema op type this one
// doesn't, written directly to bypass ApplySchema's own producer
// validation (WRIT-251 round 2 finding: Store.Schema must scope
// verification to ops like this one -- ObjectType "schema" -- without
// knowing objectID in advance, and must verify against a live trust
// store, not the one dagStore.Open froze).
func forgeSignedSchemaOp(t *testing.T, ctx context.Context, repo *git.Repository, signer writ.Signer, forgerID identity.WriterID, objectID string, causalParents ...string) plumbing.Hash {
	t.Helper()
	env := codec.Envelope{ObjectID: objectID, ObjectType: "schema", OpType: "future-op", OpVersion: 1, Body: json.RawMessage(`{"whatever":true}`)}
	raw, err := codec.EncodePayload(env)
	if err != nil {
		t.Fatalf("encode forged schema op payload: %v", err)
	}
	when := time.Now().UTC().Add(2 * time.Second)
	commit := &codec.Commit{
		Parents:   causalParents,
		Author:    codec.Identity{Name: "Alice", Email: "alice@example.com", When: when},
		Committer: codec.Identity{Name: "Alice", Email: "alice@example.com", When: when},
		Message:   codec.Message(env),
		Tree:      []codec.TreeEntry{{Name: "op.json", Mode: "100644", Data: raw}},
	}
	if err := codec.SignCommit(ctx, signer, commit); err != nil {
		t.Fatalf("sign forged schema op: %v", err)
	}
	hash, err := codec.WriteCommit(ctx, repo.Storer, commit, nil)
	if err != nil {
		t.Fatalf("write forged schema op commit: %v", err)
	}
	ref := plumbing.NewHashReference(dag.LocalRefName(forgerID, "schema"), hash)
	if err := repo.Storer.SetReference(ref); err != nil {
		t.Fatalf("set forged schema op ref: %v", err)
	}
	return hash
}

// schemaFrontierForTest returns the op commits within store's log for
// coreSchemaObjectID that no other op in that same set names as a
// parent -- the same "no child dependencies within this object"
// definition writ's own unexported schemaFrontier uses, recomputed here
// because this external test package cannot call it directly.
func schemaFrontierForTest(t *testing.T, store *writ.Store) []string {
	t.Helper()
	enumRes, err := writ.StoreDAGStore(store).Enumerate()
	if err != nil {
		t.Fatalf("Enumerate: %v", err)
	}
	ops := enumRes.Ops[coreSchemaObjectID]
	hasChild := make(map[string]bool, len(ops))
	for _, op := range ops {
		for _, p := range op.Parents {
			hasChild[p] = true
		}
	}
	var frontier []string
	for _, op := range ops {
		if !hasChild[op.ID] {
			frontier = append(frontier, op.ID)
		}
	}
	return frontier
}

// setupSignedRepo configures a fresh repo for writer "Alice" with a real
// ed25519 signer and, if allowedSignersPath is non-empty, points
// gpg.ssh.allowedSignersFile at it (the file need not exist yet). It
// returns the repo dir and a Signer wired to the generated key.
func setupSignedRepo(t testing.TB, allowedSignersPath string) (dir string, signer writ.Signer, privPath, pubLine string) {
	t.Helper()
	dir = t.TempDir()
	runGitCmdTB(t, dir, "init")
	runGitCmdTB(t, dir, "config", "user.name", "Alice")
	runGitCmdTB(t, dir, "config", "user.email", "alice@example.com")
	runGitCmdTB(t, dir, "config", "writ.writerId", "0123456789abcdef")
	if allowedSignersPath != "" {
		runGitCmdTB(t, dir, "config", "gpg.ssh.allowedSignersFile", allowedSignersPath)
	}

	privPath, pubLine = genSSHKey(t, dir, "id_alice")
	s, err := codec.NewSigner(identity.SigningKey{Format: "ssh", Value: privPath})
	if err != nil {
		t.Fatalf("codec.NewSigner: %v", err)
	}
	return dir, s, privPath, pubLine
}

// TestVerification_ForgedOpFoldsButFlagged is the WRIT-251 reproduction:
// writer A signs a real op with a trusted key; a forger with no signer at
// all appends a further op against A's object under their own writer
// chain. The forged op still folds -- ruling 1, fold ignores verification
// -- but the object's Verification summary flags it, and a fresh
// writ.Open sees the same thing A's own store would refresh into.
func TestVerification_ForgedOpFoldsButFlagged(t *testing.T) {
	allowedPath := filepath.Join(t.TempDir(), "allowed_signers")
	dir, signer, _, pubLine := setupSignedRepo(t, allowedPath)
	writeAllowedSigners(t, allowedPath, "alice@example.com", pubLine)

	ctx := context.Background()
	storeA, err := writ.Open(dir, writ.WithSigner(signer))
	if err != nil {
		t.Fatalf("writ.Open: %v", err)
	}
	applyCoreSchema(t, ctx, storeA)

	id, err := storeA.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Basecamp"},
	})
	if err != nil {
		t.Fatalf("Objects.Create: %v", err)
	}
	if _, err := storeA.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// Before the forgery: A's own signed, trusted op reports valid, both
	// live (Objects.Get) and cached (Query.Object).
	preObj, err := storeA.Objects.Get(ctx, id)
	if err != nil {
		t.Fatalf("Objects.Get (pre-forgery): %v", err)
	}
	if preObj.Verification != "valid" {
		t.Errorf("pre-forgery Object.Verification = %q, want %q", preObj.Verification, "valid")
	}
	preRes, err := storeA.Query.Object(id)
	if err != nil {
		t.Fatalf("Query.Object (pre-forgery): %v", err)
	}
	if preRes.Verification != "valid" {
		t.Errorf("pre-forgery ObjectResult.Verification = %q, want %q", preRes.Verification, "valid")
	}
	if err := storeA.Close(); err != nil {
		t.Fatalf("Close storeA: %v", err)
	}

	// The forgery: no signer, a writer id nobody has ever seen, straight
	// at the object A created -- causally following A's own create op, the
	// way a real edit would.
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("git.PlainOpen: %v", err)
	}
	forgeOp(t, repo, malloryWriterID, id, "acme.widget", "update", 1, map[string]any{"title": "FORGED"}, "", preRes.LastOpID)

	// A fresh writ.Open, exactly as a second reader would see it.
	storeB, err := writ.Open(dir)
	if err != nil {
		t.Fatalf("writ.Open (fresh): %v", err)
	}
	defer storeB.Close()

	obj, err := storeB.Objects.Get(ctx, id)
	if err != nil {
		t.Fatalf("Objects.Get: %v", err)
	}
	if title, _ := obj.Fields["title"].(string); title != "FORGED" {
		t.Errorf("Fields[title] = %q, want %q: the forged op must still fold (WRIT-251 ruling 1)", title, "FORGED")
	}
	if obj.Verification != "unsigned" {
		t.Errorf("Object.Verification = %q, want %q", obj.Verification, "unsigned")
	}

	res, err := storeB.Query.Object(id)
	if err != nil {
		t.Fatalf("Query.Object: %v", err)
	}
	if res.Verification != "unsigned" {
		t.Errorf("ObjectResult.Verification = %q, want %q", res.Verification, "unsigned")
	}
}

// TestVerification_TrustStoreEditTripsRebuild pins ruling 4: editing
// allowed_signers changes the digest ApplySchema compares, so the next
// Refresh (not just an explicit Rebuild) re-verifies every op against the
// new file -- a fixed trust store repairs cached wrong-key rows on its
// own.
func TestVerification_TrustStoreEditTripsRebuild(t *testing.T) {
	allowedPath := filepath.Join(t.TempDir(), "allowed_signers")
	dir, signer, _, pubLine := setupSignedRepo(t, allowedPath)
	// Start with an allowed_signers file that exists but authorizes nobody.
	writeAllowedSigners(t, allowedPath, "alice@example.com", "")

	ctx := context.Background()
	storeA, err := writ.Open(dir, writ.WithSigner(signer))
	if err != nil {
		t.Fatalf("writ.Open: %v", err)
	}
	applyCoreSchema(t, ctx, storeA)

	id, err := storeA.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Basecamp"},
	})
	if err != nil {
		t.Fatalf("Objects.Create: %v", err)
	}
	if _, err := storeA.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	res, err := storeA.Query.Object(id)
	if err != nil {
		t.Fatalf("Query.Object: %v", err)
	}
	if res.Verification != "wrong-key" {
		t.Fatalf("ObjectResult.Verification = %q, want %q (unauthorized key)", res.Verification, "wrong-key")
	}
	if err := storeA.Close(); err != nil {
		t.Fatalf("Close storeA: %v", err)
	}

	// Fix the trust store, then reopen and Refresh -- no explicit Rebuild.
	writeAllowedSigners(t, allowedPath, "alice@example.com", pubLine)

	storeB, err := writ.Open(dir)
	if err != nil {
		t.Fatalf("writ.Open (fresh): %v", err)
	}
	defer storeB.Close()
	stats, err := storeB.Refresh(ctx)
	if err != nil {
		t.Fatalf("Refresh (fresh): %v", err)
	}
	res2, err := storeB.Query.Object(id)
	if err != nil {
		t.Fatalf("Query.Object (fresh): %v", err)
	}
	if res2.Verification != "valid" {
		t.Errorf("ObjectResult.Verification after fixing trust store = %q, want %q (stats=%+v)", res2.Verification, "valid", stats)
	}

	// Droppable-cache invariant: an explicit Rebuild must not change the
	// answer Refresh already reached on its own.
	if _, err := storeB.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	res3, err := storeB.Query.Object(id)
	if err != nil {
		t.Fatalf("Query.Object (post-rebuild): %v", err)
	}
	if res3.Verification != "valid" {
		t.Errorf("ObjectResult.Verification after Rebuild = %q, want %q", res3.Verification, "valid")
	}
}

// TestVerification_UnconfiguredAndUnreadableTrustStore pins ruling 2 and
// its extension: Open never refuses to open over the trust store, whether
// none is configured or the configured path cannot be read, and both
// treat a validly-signed op the same way -- wrong-key, never valid,
// because there is nothing to authorize it against.
func TestVerification_UnconfiguredAndUnreadableTrustStore(t *testing.T) {
	cases := []struct {
		name       string
		configPath func(t *testing.T) string // returns the gpg.ssh.allowedSignersFile value, or "" for unset
	}{
		{
			name: "unconfigured",
			configPath: func(t *testing.T) string {
				return ""
			},
		},
		{
			name: "path to a missing file",
			configPath: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "does-not-exist")
			},
		},
		{
			name: "malformed file",
			configPath: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "malformed_allowed_signers")
				if err := os.WriteFile(path, []byte("not a valid allowed_signers line \x00\x01"), 0o600); err != nil {
					t.Fatalf("write malformed allowed_signers: %v", err)
				}
				return path
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.configPath(t)
			dir, signer, _, _ := setupSignedRepo(t, path)

			ctx := context.Background()
			store, err := writ.Open(dir, writ.WithSigner(signer))
			if err != nil {
				t.Fatalf("writ.Open must never refuse over the trust store: %v", err)
			}
			defer store.Close()
			applyCoreSchema(t, ctx, store)

			id, err := store.Objects.Create(ctx, "acme.widget", writ.NewOp{
				Type:   "create",
				Fields: map[string]any{"title": "Basecamp"},
			})
			if err != nil {
				t.Fatalf("Objects.Create: %v", err)
			}
			if _, err := store.Refresh(ctx); err != nil {
				t.Fatalf("Refresh: %v", err)
			}

			obj, err := store.Objects.Get(ctx, id)
			if err != nil {
				t.Fatalf("Objects.Get: %v", err)
			}
			if obj.Verification != "wrong-key" {
				t.Errorf("Object.Verification = %q, want %q", obj.Verification, "wrong-key")
			}
			res, err := store.Query.Object(id)
			if err != nil {
				t.Fatalf("Query.Object: %v", err)
			}
			if res.Verification != "wrong-key" {
				t.Errorf("ObjectResult.Verification = %q, want %q", res.Verification, "wrong-key")
			}
		})
	}
}

// TestVerification_TwoHandlesAgreeAfterTrustStoreEdit pins the round 2
// review finding on engine/open.go: freezing the trust-store digest (and
// the parsed trust store itself) at Open let two long-lived handles that
// disagreed about allowed_signers's contents fight over the one shared
// projection cache forever -- every Refresh from either handle forced a
// full rebuild, indefinitely, alternating the cached ObjectResult.Verification
// between "valid" and "wrong-key" depending only on which handle refreshed
// last, and the older handle (A, opened before the file was fixed) never
// picked up the edit at all, no matter how many times it refreshed.
//
// Reading the file fresh at Refresh/Rebuild time (Store.currentTrustStore)
// fixes both: A picks up the fix on its very next Refresh even though it
// was opened before it, and once both handles' digests agree with the
// file's current contents, neither forces another rebuild.
//
// This fails on the round-1 code: A's dag.Store.trustStore and
// projection.DB.trustStoreDigest are fixed at Open (when allowed_signers
// authorized nobody) and never updated, so every A.Refresh keeps writing
// a stale, mismatching digest and re-verifying against the stale store --
// resA.Verification stays "wrong-key" forever, and iterations after the
// first keep reporting Rebuilt on both handles instead of settling.
func TestVerification_TwoHandlesAgreeAfterTrustStoreEdit(t *testing.T) {
	allowedPath := filepath.Join(t.TempDir(), "allowed_signers")
	dir, signer, _, pubLine := setupSignedRepo(t, allowedPath)
	// A opens while allowed_signers exists but authorizes nobody.
	writeAllowedSigners(t, allowedPath, "alice@example.com", "")

	ctx := context.Background()
	storeA, err := writ.Open(dir, writ.WithSigner(signer))
	if err != nil {
		t.Fatalf("writ.Open (A): %v", err)
	}
	defer storeA.Close()
	applyCoreSchema(t, ctx, storeA)

	// An extra, real, correctly-signed op of an op type nothing this build
	// defines for "schema" (spec/schema-ops.md's forward-compatibility
	// case -- a newer client that knows a schema op type this one
	// doesn't; ApplySchema itself refuses to produce this, per
	// checkBeforeAppend's producer validation, so it is forged the same
	// way forgeOp forges an unrecognized-writer commit, just signed for
	// real). FoldSchema quarantines it as an UnknownOp rather than
	// folding it, but its Verification still depends on the trust store
	// exactly like any other op's -- this is what Store.Schema's round 2
	// finding (using dagStore's Open-time trust store instead of a live
	// one) got wrong.
	repoForForge, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("git.PlainOpen: %v", err)
	}
	aliceWriterID := identity.WriterID("0123456789abcdef")
	forgeSignedSchemaOp(t, ctx, repoForForge, signer, aliceWriterID, coreSchemaObjectID, schemaFrontierForTest(t, storeA)...)

	id, err := storeA.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Basecamp"},
	})
	if err != nil {
		t.Fatalf("Objects.Create: %v", err)
	}
	if _, err := storeA.Refresh(ctx); err != nil {
		t.Fatalf("Refresh (A, initial): %v", err)
	}
	if res, err := storeA.Query.Object(id); err != nil {
		t.Fatalf("Query.Object (A, initial): %v", err)
	} else if res.Verification != "wrong-key" {
		t.Fatalf("initial ObjectResult.Verification = %q, want %q", res.Verification, "wrong-key")
	}
	if v, err := futureOpVerification(storeA, ctx); err != nil {
		t.Fatalf("Schema (A, initial): %v", err)
	} else if v != "wrong-key" {
		t.Fatalf("initial Schema().UnknownOps future-op verification = %q, want %q", v, "wrong-key")
	}

	// Fix the file, then open B fresh: B's own Open-time read already
	// sees the fix, but that is not what this test is pinning -- see
	// below.
	writeAllowedSigners(t, allowedPath, "alice@example.com", pubLine)
	storeB, err := writ.Open(dir)
	if err != nil {
		t.Fatalf("writ.Open (B): %v", err)
	}
	defer storeB.Close()

	// Alternate B.Refresh and A.Refresh. Rebuilding once, to reconcile the
	// stale cache with the now-fixed file, is expected and fine; a rebuild
	// on every later iteration, from either handle, is the bug.
	for i := 0; i < 4; i++ {
		statsB, err := storeB.Refresh(ctx)
		if err != nil {
			t.Fatalf("Refresh (B, iter %d): %v", i, err)
		}
		resB, err := storeB.Query.Object(id)
		if err != nil {
			t.Fatalf("Query.Object (B, iter %d): %v", i, err)
		}
		if resB.Verification != "valid" {
			t.Errorf("iter %d: B's ObjectResult.Verification = %q, want %q (stats=%+v)", i, resB.Verification, "valid", statsB)
		}
		if v, err := futureOpVerification(storeB, ctx); err != nil {
			t.Fatalf("Schema (B, iter %d): %v", i, err)
		} else if v != "valid" {
			t.Errorf("iter %d: B's Schema().UnknownOps future-op verification = %q, want %q", i, v, "valid")
		}

		statsA, err := storeA.Refresh(ctx)
		if err != nil {
			t.Fatalf("Refresh (A, iter %d): %v", i, err)
		}
		resA, err := storeA.Query.Object(id)
		if err != nil {
			t.Fatalf("Query.Object (A, iter %d): %v", i, err)
		}
		// The old store must pick up the edit: A was opened while the
		// file authorized nobody, but by now the file is fixed, and A
		// has refreshed since.
		if resA.Verification != "valid" {
			t.Errorf("iter %d: A's ObjectResult.Verification = %q, want %q (stats=%+v) -- the old handle must pick up the trust-store edit", i, resA.Verification, "valid", statsA)
		}
		// Store.Schema reads the DAG directly, not the projection cache
		// Refresh maintains -- but before the round 2 fix it still fell
		// back to dagStore's Open-time trust store, so A (opened while the
		// file authorized nobody) would report "valid" from Query.Object
		// here yet keep reporting "wrong-key" from Schema for the very
		// same underlying trust-store state, forever.
		if v, err := futureOpVerification(storeA, ctx); err != nil {
			t.Fatalf("Schema (A, iter %d): %v", i, err)
		} else if v != "valid" {
			t.Errorf("iter %d: A's Schema().UnknownOps future-op verification = %q, want %q -- Schema must agree with Query.Object", i, v, "valid")
		}

		if i >= 1 && (statsA.Rebuilt || statsB.Rebuilt) {
			t.Errorf("iter %d: unexpected rebuild after reconciliation should have settled (statsA.Rebuilt=%v statsB.Rebuilt=%v)", i, statsA.Rebuilt, statsB.Rebuilt)
		}
	}
}

// futureOpVerification returns the Verification outcome Store.Schema
// reports for TestVerification_TwoHandlesAgreeAfterTrustStoreEdit's
// forged "future-op" UnknownOp on coreSchemaObjectID.
func futureOpVerification(store *writ.Store, ctx context.Context) (string, error) {
	schemas, err := store.Schema(ctx)
	if err != nil {
		return "", err
	}
	for _, sch := range schemas {
		if sch.ObjectID != coreSchemaObjectID {
			continue
		}
		for _, uo := range sch.UnknownOps {
			if uo.OpType == "future-op" {
				return uo.Verification, nil
			}
		}
	}
	return "", fmt.Errorf("future-op UnknownOp not found in Schema() for %s", coreSchemaObjectID)
}
