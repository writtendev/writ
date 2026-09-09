package writ_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	writ "github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
)

// runGitCmdTB is runGitCmd's testing.TB counterpart: the existing helper is
// typed to *testing.T, which a *testing.B cannot satisfy, so a benchmark
// that needs the same "git init and configure a writer identity" setup
// package-level tests already use needs its own copy.
func runGitCmdTB(tb testing.TB, dir string, args ...string) {
	tb.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("git %v failed: %v\nOutput: %s", args, err, string(out))
	}
}

func setupConfiguredRepoTB(tb testing.TB) string {
	tb.Helper()
	dir := tb.TempDir()
	runGitCmdTB(tb, dir, "init")
	runGitCmdTB(tb, dir, "config", "user.name", "Bench Bot")
	runGitCmdTB(tb, dir, "config", "user.email", "bench@example.com")
	runGitCmdTB(tb, dir, "config", "writ.writerId", "0123456789abcdef")
	runGitCmdTB(tb, dir, "config", "gpg.format", "ssh")
	runGitCmdTB(tb, dir, "config", "user.signingKey", "key::ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGdummy")
	return dir
}

// BenchmarkVocabulariesCache is the resolver cache hit/miss benchmark the
// WRIT-188 plan's Tests section asked for and round 1 found missing — the
// regression net that would have caught the MAJOR-1 finding: every Append
// used to move its own writer chain's tip, which the vocabularies cache
// fingerprinted itself against, so the very next Append always looked like
// an invalidating change and re-ran a full Schema/Enumerate fold over the
// whole log. Hit should cost roughly one IterReferences pass regardless of
// log size; Miss pays for a full fold and grows with it — that contrast,
// not either number alone, is what a reader should take from this.
func BenchmarkVocabulariesCache(b *testing.B) {
	dir := setupConfiguredRepoTB(b)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		b.Fatalf("writ.Open failed: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	dagStore := writ.StoreDAGStore(store)
	applyCoreSchema(b, ctx, store)

	// A moderately sized log of ordinary, non-"schema" ops: what Miss's
	// full re-fold has to walk, and what Hit must stay flat despite.
	const logSize = 200
	for i := 0; i < logSize; i++ {
		env := codec.Envelope{
			ObjectID:   fmt.Sprintf("w-%d", i),
			ObjectType: "widget",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"T"}`),
		}
		if _, err := dagStore.Append(ctx, env, nil); err != nil {
			b.Fatalf("seed append %d failed: %v", i, err)
		}
	}

	b.Run("Hit", func(b *testing.B) {
		// Warm the cache once; every call after this must hit it.
		if _, err := writ.StoreVocabularies(store, ctx); err != nil {
			b.Fatalf("StoreVocabularies failed: %v", err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := writ.StoreVocabularies(store, ctx); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("Miss", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			// A "schema" append is the one thing that must invalidate the
			// cache (spec/schema-ops.md §7): each iteration forces the
			// next StoreVocabularies call to pay for a full re-resolve
			// over the log seeded above.
			schemaEnv := codec.Envelope{
				ObjectID:   fmt.Sprintf("sch-miss-%d", i),
				ObjectType: "schema",
				OpType:     "create",
				OpVersion:  1,
				Body:       json.RawMessage(`{"namespace":"bench"}`),
			}
			if _, err := dagStore.Append(ctx, schemaEnv, nil); err != nil {
				b.Fatalf("invalidating append %d failed: %v", i, err)
			}
			b.StartTimer()

			if _, err := writ.StoreVocabularies(store, ctx); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkAppendByRefCount is WRIT-188 round 2's major-finding reproduction,
// kept in the tree as a live measurement of a known, tracked regression
// rather than as a claim that it is fixed. dag.WithProducerVocabularies puts
// Store.vocabularies on every Append, and Store.vocabularies calls
// dag.Chains before it can even compare its cache's fingerprint. dag.Chains
// is a single storer.IterReferences pass over every reference in the
// repository (round 2 added, and round 3 reverted, a filesystem-scoped fast
// path: it closed this gap but introduced a repo-bricking failure and a
// silent read-path data-loss case — see the WRIT-188 round 3 review), so
// per-Append cost here is linear in the repository's TOTAL ref count —
// ordinary refs/heads entries, the kind a couple hundred branches (or one
// `git fetch`) leaves behind — not only in its writ chain count. `main`
// never calls dag.Chains on the append path at all, so it has no equivalent
// scaling to compare against here — see BenchmarkVocabulariesCache/Hit for
// the flat, log-size-independent cost this benchmark's own cache still
// preserves. The regression this benchmark reproduces is tracked as its own
// ticket, separate from WRIT-188.
func BenchmarkAppendByRefCount(b *testing.B) {
	for _, refCount := range []int{0, 200, 500, 2000} {
		b.Run(fmt.Sprintf("refs=%d", refCount), func(b *testing.B) {
			dir := setupConfiguredRepoTB(b)
			store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
			if err != nil {
				b.Fatalf("writ.Open failed: %v", err)
			}
			defer store.Close()
			ctx := context.Background()
			dagStore := writ.StoreDAGStore(store)
			applyCoreSchema(b, ctx, store)

			// Ordinary loose refs a real repository accumulates as
			// branches, tags, and remote-tracking refs — none of them
			// writ chains, but dag.Chains' IterReferences pass walks
			// every one of them anyway.
			storer := dagStore.Storer()
			for i := 0; i < refCount; i++ {
				name := plumbing.ReferenceName(fmt.Sprintf("refs/heads/branch-%d", i))
				hash := plumbing.NewHash(fmt.Sprintf("%040x", i+1))
				if err := storer.SetReference(plumbing.NewHashReference(name, hash)); err != nil {
					b.Fatalf("seed ref %d failed: %v", i, err)
				}
			}

			// One warm-up append populates the vocabularies cache before
			// timing begins, so every timed Append is a cache hit — the
			// dimension finding 1 measured.
			warmup := codec.Envelope{
				ObjectID: "warm", ObjectType: "widget", OpType: "create",
				OpVersion: 1, Body: json.RawMessage(`{"title":"T"}`),
			}
			if _, err := dagStore.Append(ctx, warmup, nil); err != nil {
				b.Fatalf("warm-up append failed: %v", err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				env := codec.Envelope{
					ObjectID:   fmt.Sprintf("w-%d", i),
					ObjectType: "widget",
					OpType:     "create",
					OpVersion:  1,
					Body:       json.RawMessage(`{"title":"T"}`),
				}
				if _, err := dagStore.Append(ctx, env, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkOpenByRefCount is WRIT-189 round 1's MAJOR-2 finding's own
// reproduction: writ.Open used to resolve the schema (a cold
// dag.Store.Enumerate, unconditionally) and call projection.DB.ApplySchema
// on every single call, including a reopen of a cache that already has its
// generated tables from a prior process. That made Open itself linear in
// the repository's total ref count — measured at 6.3x slower at 2,000 loose
// refs than at 0 — and paid by every CLI invocation, reads included, not
// only the first one after a schema change.
//
// Each subtest's warm-up Open creates the cache once (so its generated
// tables are persisted to meta) and seeds refCount ordinary loose refs
// exactly as BenchmarkAppendByRefCount does; only the repeated Opens in the
// timed loop are measured, each one a reopen of that same warm, unchanged
// cache — the case that must now stay flat.
func BenchmarkOpenByRefCount(b *testing.B) {
	for _, refCount := range []int{0, 200, 500, 2000} {
		b.Run(fmt.Sprintf("refs=%d", refCount), func(b *testing.B) {
			dir := setupConfiguredRepoTB(b)

			warm, err := writ.Open(dir, writ.WithSigner(dummySigner()))
			if err != nil {
				b.Fatalf("warm-up writ.Open failed: %v", err)
			}
			storer := writ.StoreDAGStore(warm).Storer()
			for i := 0; i < refCount; i++ {
				name := plumbing.ReferenceName(fmt.Sprintf("refs/heads/branch-%d", i))
				hash := plumbing.NewHash(fmt.Sprintf("%040x", i+1))
				if err := storer.SetReference(plumbing.NewHashReference(name, hash)); err != nil {
					b.Fatalf("seed ref %d failed: %v", i, err)
				}
			}
			if err := warm.Close(); err != nil {
				b.Fatalf("warm-up Close failed: %v", err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
				if err != nil {
					b.Fatalf("writ.Open failed: %v", err)
				}
				if err := store.Close(); err != nil {
					b.Fatalf("Close failed: %v", err)
				}
			}
		})
	}
}
