package writ_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/internal/dag"
)

// TestVerification_ReplayedOpIsOneOp is WRIT-312's reproduction at the
// writ.Open level: Alice signs a revision op whose base and head are
// append fields; Mallory, with push access, copies that commit with its
// gpgsig armor re-wrapped to another column width -- the same signature
// bytes, so it still verifies as Alice's, under a new commit SHA -- and
// points a ref in a writer namespace of her own at it. A fresh reader must
// see Alice's one revision, not two: an op's identity is what was signed,
// not the exact bytes it is wrapped in.
func TestVerification_ReplayedOpIsOneOp(t *testing.T) {
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
	const (
		base = "0123456789abcdef0123456789abcdef01234567"
		head = "89abcdef0123456789abcdef0123456789abcdef"
	)
	if err := storeA.Objects.Apply(ctx, id, writ.NewOp{
		Type:   "revision",
		Fields: map[string]any{"base": base, "head": head},
	}); err != nil {
		t.Fatalf("Objects.Apply revision: %v", err)
	}
	if err := storeA.Close(); err != nil {
		t.Fatalf("Close storeA: %v", err)
	}

	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("git.PlainOpen: %v", err)
	}
	tipRef, err := repo.Reference(dag.LocalRefName("0123456789abcdef", "acme.widget"), true)
	if err != nil {
		t.Fatalf("read Alice's chain tip: %v", err)
	}
	orig, err := repo.CommitObject(tipRef.Hash())
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}
	if !strings.Contains(orig.Message, "revision") {
		t.Fatalf("tip %s is not the revision op: %q", orig.Hash, orig.Message)
	}

	var b64 strings.Builder
	for _, line := range strings.Split(orig.PGPSignature, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "-----") {
			b64.WriteString(line)
		}
	}
	rewrapped := "-----BEGIN SSH SIGNATURE-----\n"
	for s := b64.String(); s != ""; {
		n := min(40, len(s))
		rewrapped += s[:n] + "\n"
		s = s[n:]
	}
	rewrapped += "-----END SSH SIGNATURE-----"

	clone := *orig
	clone.PGPSignature = rewrapped
	obj := repo.Storer.NewEncodedObject()
	if err := clone.Encode(obj); err != nil {
		t.Fatalf("Encode replay: %v", err)
	}
	replay, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatalf("store replay: %v", err)
	}
	if replay == orig.Hash {
		t.Fatal("re-wrapping the armor did not change the commit")
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(dag.LocalRefName(malloryWriterID, "acme.widget"), replay)); err != nil {
		t.Fatalf("set replay ref: %v", err)
	}

	storeB, err := writ.Open(dir)
	if err != nil {
		t.Fatalf("writ.Open (fresh): %v", err)
	}
	defer storeB.Close()

	check := func(t *testing.T, label string) {
		t.Helper()
		obj, err := storeB.Objects.Get(ctx, id)
		if err != nil {
			t.Fatalf("%s: Objects.Get: %v", label, err)
		}
		if got, _ := obj.Fields["base"].([]any); len(got) != 1 || got[0] != base {
			t.Errorf("%s: Fields[base] = %v, want exactly [%s] — the replay double-applied an append field", label, obj.Fields["base"], base)
		}
		if got, _ := obj.Fields["head"].([]any); len(got) != 1 || got[0] != head {
			t.Errorf("%s: Fields[head] = %v, want exactly [%s]", label, obj.Fields["head"], head)
		}
		if obj.Verification != "valid" {
			t.Errorf("%s: Object.Verification = %q, want valid", label, obj.Verification)
		}
		res, err := storeB.Query.Object(id)
		if err != nil {
			t.Fatalf("%s: Query.Object: %v", label, err)
		}
		if res.Verification != "valid" {
			t.Errorf("%s: ObjectResult.Verification = %q, want valid", label, res.Verification)
		}
	}
	check(t, "after Open")

	if _, err := storeB.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	check(t, "after Refresh")
	if _, err := storeB.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	check(t, "after Rebuild")
}
