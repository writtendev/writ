package writ_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	writ "github.com/writtendev/writ/engine"
)

// BenchmarkObjectGetLiveVerification measures Objects.Get on a repo with
// real ed25519-signed ops spread across many objects (WRIT-251 round 2
// perf finding). The round-1 code's Objects.Get called dag.Store.Enumerate
// with no way to skip verification, which verified every op in the repo —
// not just the requested object's own ops — against a real trust store.
// So this benchmark's per-call cost grew with the whole log's op count
// instead of staying flat as numObjects grows: on the round-1 code, Get on
// a repo of numObjects*opsPerObject signed ops costs roughly
// numObjects*opsPerObject * (one ed25519 verify), regardless of which
// object is asked for. After the fix, Get verifies only the requested
// object's own ops (dag.SkipVerification during the walk, then
// codec.Op.Verify on just the filtered ops), so the cost is flat in
// numObjects and scales only with opsPerObject.
//
// Uses real signatures, not projection/query_bench_test.go's
// dummySigner() fixtures (dummy-signature fails sshsig.ParseSignature
// before any crypto runs, so that benchmark measures nothing about
// verification cost — the mistake behind the PR's original, incorrect
// "no regression" claim). Run with an explicit iteration count
// (-bench=. -benchtime=20x), since the per-call cost this is measuring
// is large enough that the default time-based -bench would settle on very
// few iterations and be noisy.
func BenchmarkObjectGetLiveVerification(b *testing.B) {
	allowedPath := filepath.Join(b.TempDir(), "allowed_signers")
	dir, signer, _, pubLine := setupSignedRepo(b, allowedPath)
	writeAllowedSigners(b, allowedPath, "alice@example.com", pubLine)

	ctx := context.Background()
	store, err := writ.Open(dir, writ.WithSigner(signer))
	if err != nil {
		b.Fatalf("writ.Open: %v", err)
	}
	defer store.Close()
	applyCoreSchema(b, ctx, store)

	// A few thousand real-signed ops spread across many objects, per the
	// round 1 review finding's ask.
	const numObjects = 50
	const opsPerObject = 60 // 50 * 60 = 3000 ops total
	var firstID string
	for i := 0; i < numObjects; i++ {
		id, err := store.Objects.Create(ctx, "acme.widget", writ.NewOp{
			Type:   "create",
			Fields: map[string]any{"title": fmt.Sprintf("widget %d", i)},
		})
		if err != nil {
			b.Fatalf("Objects.Create %d: %v", i, err)
		}
		if i == 0 {
			firstID = id
		}
		for j := 1; j < opsPerObject; j++ {
			if err := store.Objects.Apply(ctx, id, writ.NewOp{
				Type:   "update",
				Fields: map[string]any{"description": fmt.Sprintf("edit %d", j)},
			}); err != nil {
				b.Fatalf("Objects.Apply %d/%d: %v", i, j, err)
			}
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.Objects.Get(ctx, firstID); err != nil {
			b.Fatalf("Objects.Get: %v", err)
		}
	}
}
