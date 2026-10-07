package dag_test

import (
	"path/filepath"
	"testing"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/spec/fixtures"
)

func TestEnumerate_MultiWriterChainsFixture(t *testing.T) {
	descs, err := fixtures.LoadCorpus()
	if err != nil {
		t.Fatalf("LoadCorpus failed: %v", err)
	}

	var targetDesc *fixtures.Description
	for _, d := range descs {
		if d.Name == "multi-writer-chains" {
			targetDesc = d
			break
		}
	}
	if targetDesc == nil {
		t.Fatal("fixture multi-writer-chains not found in corpus")
	}

	repoDir := filepath.Join(t.TempDir(), "repo")
	manifest, err := fixtures.Generate(targetDesc, repoDir)
	if err != nil {
		t.Fatalf("fixtures.Generate failed: %v", err)
	}

	ident := testIdentity("0123456789abcdef", "Alice", "alice@example.test")
	store, err := dag.Open(repoDir, ident)
	if err != nil {
		t.Fatalf("dag.Open failed: %v", err)
	}

	res, err := store.Enumerate()
	if err != nil {
		t.Fatalf("store.Enumerate failed: %v", err)
	}

	// 1. Check cursors match manifest refs
	if len(res.Cursors) != len(manifest.Refs) {
		t.Fatalf("res.Cursors len = %d, want %d", len(res.Cursors), len(manifest.Refs))
	}
	for _, r := range manifest.Refs {
		if res.Cursors[r.Name] != r.Commit {
			t.Errorf("cursor for %s = %s, want %s", r.Name, res.Cursors[r.Name], r.Commit)
		}
	}

	// 2. Check Rejections: alice-malformed (missing op.json) and
	// remote-malformed (non-canonical payload) — WRIT-289: each break ends
	// its own chain's walk, so alice-2 (behind alice-malformed, with no
	// other path reaching it) and remote-0 (behind remote-malformed) are
	// both rejections' worth of "the walk stopped here", not silently
	// dropped ops.
	if len(res.Rejections) != 2 {
		t.Fatalf("rejections len = %d, want 2: %v", len(res.Rejections), res.Rejections)
	}
	gotReasons := map[codec.RejectReason]bool{}
	for _, rej := range res.Rejections {
		gotReasons[rej.Reason] = true
	}
	if !gotReasons[codec.RejectTreeShape] {
		t.Errorf("rejections = %v, want one with reason %q", res.Rejections, codec.RejectTreeShape)
	}
	if !gotReasons[codec.RejectNonCanonicalPayload] {
		t.Errorf("rejections = %v, want one with reason %q", res.Rejections, codec.RejectNonCanonicalPayload)
	}

	// 3. Check widget-01 ops (4 ops: alice-1, alice-3, bob-2, remote-1).
	// alice-1 is held only because bob-2's causal parent reaches it
	// directly, bypassing alice-malformed on alice's own chain.
	r1Ops, ok := res.Ops["widget-01"]
	if !ok {
		t.Fatalf("missing widget-01 in Ops: %v", res.Ops)
	}
	if len(r1Ops) != 4 {
		t.Fatalf("widget-01 ops count = %d, want 4", len(r1Ops))
	}
	// Verify sorted by Op ID
	for i := 1; i < len(r1Ops); i++ {
		if r1Ops[i-1].ID >= r1Ops[i].ID {
			t.Errorf("ops not sorted: %s >= %s", r1Ops[i-1].ID, r1Ops[i].ID)
		}
	}

	// 4. Check widget-02 ops (1 op: bob-1). alice-2 sits behind
	// alice-malformed on alice's own chain with no other path reaching
	// it, so WRIT-289's stopping rule drops it.
	r2Ops, ok := res.Ops["widget-02"]
	if !ok {
		t.Fatalf("missing widget-02 in Ops: %v", res.Ops)
	}
	if len(r2Ops) != 1 {
		t.Fatalf("widget-02 ops count = %d, want 1", len(r2Ops))
	}
	for i := 1; i < len(r2Ops); i++ {
		if r2Ops[i-1].ID >= r2Ops[i].ID {
			t.Errorf("ops not sorted: %s >= %s", r2Ops[i-1].ID, r2Ops[i].ID)
		}
	}
}
