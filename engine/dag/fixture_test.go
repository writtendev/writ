package dag_test

import (
	"path/filepath"
	"testing"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
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

	// 2. Check Rejections: alice-malformed missing op.json
	if len(res.Rejections) != 1 {
		t.Fatalf("rejections len = %d, want 1", len(res.Rejections))
	}
	rej := res.Rejections[0]
	if rej.Reason != codec.RejectMissingOpJSON {
		t.Errorf("rejection reason = %q, want %q", rej.Reason, codec.RejectMissingOpJSON)
	}

	// 3. Check widget-01 ops (4 ops: alice-1, alice-3, bob-2, remote-1)
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

	// 4. Check widget-02 ops (2 ops: alice-2, bob-1)
	r2Ops, ok := res.Ops["widget-02"]
	if !ok {
		t.Fatalf("missing widget-02 in Ops: %v", res.Ops)
	}
	if len(r2Ops) != 2 {
		t.Fatalf("widget-02 ops count = %d, want 2", len(r2Ops))
	}
	for i := 1; i < len(r2Ops); i++ {
		if r2Ops[i-1].ID >= r2Ops[i].ID {
			t.Errorf("ops not sorted: %s >= %s", r2Ops[i-1].ID, r2Ops[i].ID)
		}
	}
}
