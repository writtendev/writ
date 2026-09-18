package writ_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	writ "github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/internal/codec"
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
// object's own ops (dag.VerifyOnly, matching only that object's ops,
// during the same walk), so the cost is flat in numObjects and scales
// only with opsPerObject.
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

// BenchmarkObjectsGet is WRIT-249's own measurement: Objects.Get's cost as
// the repository grows to thousands of objects, with WRIT-251's
// VerifyOnly scoping and WRIT-255's op.json size cap / packidx cache
// already in place (both merged to main before this ticket started).
//
// VerifyOnly already scopes signature verification (codec.Verify) to the
// requested object. It does not skip the decode step: EnumerateSince
// still runs codec.FromGitCommit + codec.DecodeCommit — the op.json size
// cap, canonical-payload check and envelope-schema validation — against
// every op of every OTHER object in the repository on every single Get.
//
// This benchmark is what WRIT-249 used to decide whether a second,
// cheaper pre-decode filter (peek each commit's op.json for object_id,
// skip the ones that plainly don't match, before running the expensive
// decode) is worth adding as a new enumerate path. It measured a ~20-30%
// reduction at 100-1,000 objects that shrank to a noisy 10-20% at 5,000 —
// below the plan's ~30%-at-1,000+-objects bar — because at that scale the
// ancestry walk's own git object-store I/O, not the JSON validation a
// peek would skip, dominates. See Objects.Get's doc comment for the full
// account and this ticket's PR body for the numbers. This benchmark stays
// as the permanent regression net for whoever revisits this with a real
// index instead of a peek.
//
// Uses a real ed25519 signer (setupSignedRepo), not dummySigner: signing
// happens once per seeded op at setup time, before b.ResetTimer, so it
// never pollutes the timed Get loop, and it lets the one object each
// iteration reads exercise the real codec.Verify(...) path VerifyOnly's
// match triggers for it — the same real-crypto shape
// BenchmarkObjectGetLiveVerification already uses, just at larger scale
// and with signing cost kept out of the timed portion.
//
// Seeds through dag.Store.Append directly (as BenchmarkVocabulariesCache
// does), not Objects.Create/Apply: the schema/rules resolution
// Objects.Create performs on every call is not what this benchmark is
// measuring, and skipping it keeps setup time down at 5,000 objects.
func BenchmarkObjectsGet(b *testing.B) {
	for _, numObjects := range []int{100, 1000, 5000} {
		b.Run(fmt.Sprintf("objects=%d", numObjects), func(b *testing.B) {
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

			dagStore := writ.StoreDAGStore(store)

			// A few ops per object, spread across numObjects objects. The
			// benchmark reads the mid-log object — neither the very first
			// nor the very last chain tip — so it isn't an artificially
			// cheap or expensive position in the walk.
			const opsPerObject = 3
			midID := fmt.Sprintf("w-%d", numObjects/2)
			for i := 0; i < numObjects; i++ {
				id := fmt.Sprintf("w-%d", i)
				for j := 0; j < opsPerObject; j++ {
					opType := "create"
					if j > 0 {
						opType = "update"
					}
					env := codec.Envelope{
						ObjectID:   id,
						ObjectType: "acme.widget",
						OpType:     opType,
						OpVersion:  1,
						Body:       json.RawMessage(fmt.Sprintf(`{"title":"widget %d rev %d"}`, i, j)),
					}
					if _, err := dagStore.Append(ctx, env, nil); err != nil {
						b.Fatalf("seed append object %d op %d: %v", i, j, err)
					}
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := store.Objects.Get(ctx, midID); err != nil {
					b.Fatalf("Objects.Get: %v", err)
				}
			}
		})
	}
}
