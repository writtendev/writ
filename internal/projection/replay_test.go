package projection_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/identity"
	"github.com/writtendev/writ/internal/projection"
)

// The tests in this file pin WRIT-312's projection half: two commits whose
// signed payloads are byte-identical are one op, and an incremental Refresh
// that meets the second one after the first is already recorded must land on
// exactly the state a fresh Rebuild does.

// garbageCarrier stores a copy of commit id whose gpgsig header is a
// well-formed armor around garbage bytes, ground until the new commit's SHA
// sorts below (below=true) or above id. The signed payload — everything but
// the gpgsig header — is unchanged, so the copy is a second carrier of the
// same op, differing only in bytes the signature never covered.
func garbageCarrier(t *testing.T, repo *git.Repository, id string, below bool) plumbing.Hash {
	t.Helper()
	c, err := repo.CommitObject(plumbing.NewHash(id))
	if err != nil {
		t.Fatalf("CommitObject %s: %v", id, err)
	}
	for i := 0; i < 128; i++ {
		clone := *c
		clone.PGPSignature = fmt.Sprintf("-----BEGIN SSH SIGNATURE-----\n%s\n-----END SSH SIGNATURE-----",
			base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("garbage-%d", i))))
		obj := repo.Storer.NewEncodedObject()
		if err := clone.Encode(obj); err != nil {
			t.Fatalf("Encode carrier: %v", err)
		}
		h, err := repo.Storer.SetEncodedObject(obj)
		if err != nil {
			t.Fatalf("store carrier: %v", err)
		}
		if (h.String() < id) == below {
			return h
		}
	}
	t.Fatalf("could not grind a carrier of %s sorting below=%v", id, below)
	return plumbing.ZeroHash
}

func countRows(t *testing.T, db *projection.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.DB().QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func revisionEnv() codec.Envelope {
	return makeWidgetEnv("w-1", "revision", map[string]any{
		"base": "0123456789abcdef0123456789abcdef01234567",
		"head": "89abcdef0123456789abcdef0123456789abcdef",
	})
}

func TestRefresh_ReplayedOpMatchesRebuild(t *testing.T) {
	for _, below := range []bool{true, false} {
		name := "replay sorts above the original"
		if below {
			name = "replay sorts below the original"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			repo, store := createTestStore(t, "0123456789abcdef")
			rules := testRules()

			if _, err := store.Append(ctx, makeWidgetEnv("w-1", "create", map[string]any{"title": "T"}), nil); err != nil {
				t.Fatalf("Append create: %v", err)
			}
			revision, err := store.Append(ctx, revisionEnv(), nil)
			if err != nil {
				t.Fatalf("Append revision: %v", err)
			}

			db, err := projection.Open(":memory:")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer db.Close()
			if _, err := db.Refresh(store, projection.WithSchema(rules)); err != nil {
				t.Fatalf("Refresh initial: %v", err)
			}

			// Mallory, with push access, writes a second commit carrying
			// Alice's revision payload under a namespace of her own.
			replay := garbageCarrier(t, repo, revision.ID, below)
			survivor, dropped := revision.ID, replay.String()
			if below {
				survivor, dropped = dropped, survivor
			}
			if err := repo.Storer.SetReference(plumbing.NewHashReference(
				plumbing.ReferenceName("refs/writ/fedcba9876543210/widget"), replay)); err != nil {
				t.Fatalf("set replay ref: %v", err)
			}

			stats, err := db.Refresh(store, projection.WithSchema(rules))
			if err != nil {
				t.Fatalf("Refresh after replay: %v", err)
			}
			if !stats.Rebuilt {
				t.Fatalf("Refresh after replay did not rebuild: %+v — a new carrier can move an op's id, which an incremental delta cannot express", stats)
			}

			assertOneRevision := func(t *testing.T, db *projection.DB) {
				t.Helper()
				if n := countRows(t, db, "SELECT COUNT(*) FROM ops WHERE object_id = 'w-1'"); n != 2 {
					t.Fatalf("ops rows for w-1 = %d, want 2 (create, and the replayed revision once)", n)
				}
				if n := countRows(t, db, "SELECT COUNT(*) FROM ops WHERE op_id = ?", survivor); n != 1 {
					t.Fatalf("no op row under the lowest-sorting carrier %s", survivor)
				}
				if n := countRows(t, db, "SELECT COUNT(*) FROM ops WHERE op_id = ?", dropped); n != 0 {
					t.Fatalf("the dropped carrier %s is recorded as an op", dropped)
				}
				for _, c := range []string{survivor, dropped} {
					if n := countRows(t, db, "SELECT COUNT(*) FROM op_carriers WHERE commit_id = ? AND op_id = ?", c, survivor); n != 1 {
						t.Fatalf("op_carriers does not map %s to %s", c, survivor)
					}
				}
				if got := queryAppendValues(t, db.DB(), "o_widget__base"); len(got) != 1 {
					t.Fatalf("o_widget__base = %v, want exactly one entry — the replay double-applied an append field", got)
				}
			}
			assertOneRevision(t, db)

			// A quiet Refresh after the rebuild must not rebuild again: the
			// replay is now a recorded carrier, not a new commit.
			quiet, err := db.Refresh(store, projection.WithSchema(rules))
			if err != nil {
				t.Fatalf("quiet Refresh: %v", err)
			}
			if quiet.Rebuilt || quiet.OpsDecoded != 0 {
				t.Fatalf("quiet Refresh = %+v, want no rebuild and no decodes (rebuild loop)", quiet)
			}

			// Bob's op names the carrier that did not become the op id as its
			// causal parent. The replay chain's tip has not moved, so this
			// arrives through an incremental pass whose walk stops at the
			// recorded carrier; the parent must still read as the op id.
			storeBob, err := dag.OpenRepo(repo, identity.Identity{
				WriterID: identity.WriterID("1111111111111111"),
				Author:   identity.Author{Name: "Bob", Email: "bob@example.test"},
			}, withVocabularies(appendRules()))
			if err != nil {
				t.Fatalf("OpenRepo bob: %v", err)
			}
			child, err := storeBob.Append(ctx, makeWidgetEnv("w-1", "update", map[string]any{"title": "Bob"}), []string{dropped})
			if err != nil {
				t.Fatalf("Append bob: %v", err)
			}
			stats, err = db.Refresh(store, projection.WithSchema(rules))
			if err != nil {
				t.Fatalf("Refresh after bob: %v", err)
			}
			if stats.Rebuilt || stats.OpsDecoded != 1 {
				t.Fatalf("Refresh after bob = %+v, want one incremental decode", stats)
			}
			var parentsJSON string
			if err := db.DB().QueryRow("SELECT parents FROM ops WHERE op_id = ?", child.ID).Scan(&parentsJSON); err != nil {
				t.Fatalf("read bob's op: %v", err)
			}
			var parents []string
			if err := json.Unmarshal([]byte(parentsJSON), &parents); err != nil {
				t.Fatalf("unmarshal parents: %v", err)
			}
			if !reflect.DeepEqual(parents, []string{survivor}) {
				t.Fatalf("bob's parents = %v, want [%s] (the op id, not the dropped carrier %s)", parents, survivor, dropped)
			}

			// Whatever the incremental path did, a fresh projection of the
			// same repository holds exactly the same rows.
			incDump, err := db.DumpTables()
			if err != nil {
				t.Fatalf("DumpTables: %v", err)
			}
			cold, err := projection.Open(":memory:")
			if err != nil {
				t.Fatalf("Open cold: %v", err)
			}
			defer cold.Close()
			if _, err := cold.Refresh(store, projection.WithSchema(rules)); err != nil {
				t.Fatalf("cold Refresh: %v", err)
			}
			coldDump, err := cold.DumpTables()
			if err != nil {
				t.Fatalf("cold DumpTables: %v", err)
			}
			if !reflect.DeepEqual(incDump, coldDump) {
				t.Fatalf("incremental dump != cold dump:\nincremental: %+v\ncold: %+v", incDump, coldDump)
			}
		})
	}
}
