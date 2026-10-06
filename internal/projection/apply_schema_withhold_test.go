package projection_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/projection"
)

// gadgetTagsIndex is the name ddlTable.indexStatements gives the index on
// the item column of testRules()'s gadget tags child table. Creating an
// object of that name on a substrate table first is the one practical way to
// make SQLite refuse a type's generated DDL now that the generated names are
// collision-free by construction (WRIT-322): no legal schema can.
const gadgetTagsIndex = `idx:o_gadget__tags:item`

func metaValue(t *testing.T, db *sql.DB, key string) string {
	t.Helper()
	var v string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = ?", key).Scan(&v); err != nil {
		t.Fatalf("read meta %q: %v", key, err)
	}
	return v
}

func makeGadgetEnv(objID, opType string, body map[string]any) codec.Envelope {
	bodyRaw, _ := json.Marshal(body)
	env := codec.Envelope{ObjectID: objID, ObjectType: "gadget", OpType: opType, OpVersion: 1, Body: bodyRaw}
	raw, _ := codec.EncodePayload(env)
	env.Raw = raw
	return env
}

// assertGadgetWithheld checks every place a withheld type must be absent
// from, and that the rest of the schema is intact.
func assertGadgetWithheld(t *testing.T, db *projection.DB) {
	t.Helper()
	raw := db.DB()
	if got := metaValue(t, raw, "schema_withheld_types"); got != `["gadget"]` {
		t.Fatalf("schema_withheld_types = %s, want [\"gadget\"]", got)
	}
	for _, key := range []string{"schema_tables", "schema_query_shapes"} {
		if v := metaValue(t, raw, key); strings.Contains(v, "gadget") {
			t.Fatalf("%s still names the withheld type: %s", key, v)
		}
		if v := metaValue(t, raw, key); !strings.Contains(v, "widget") {
			t.Fatalf("%s lost the surviving type: %s", key, v)
		}
	}
	if tableExists(t, db, "o_gadget") || tableExists(t, db, "o_gadget__tags") {
		t.Fatal("a withheld type's tables survived its savepoint rollback")
	}
	if !tableExists(t, db, "o_widget") {
		t.Fatal("o_widget missing: one type's refusal took the others down")
	}
}

// TestApplySchemaWithholdsTypeSQLiteRefuses is WRIT-322 (c): a DDL error from
// peer data must never be fatal to the pass. A type whose DDL SQLite refuses
// is withheld — absent from the descriptor and meta, its ops in unknown_ops —
// and every other type materializes. The decision survives the equal-digest
// warm path and a reopen, where no DDL runs.
func TestApplySchemaWithholdsTypeSQLiteRefuses(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	dbPath := filepath.Join(t.TempDir(), "projection.db")
	db, err := projection.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.DB().Exec(`CREATE INDEX "` + gadgetTagsIndex + `" ON meta(key)`); err != nil {
		t.Fatalf("inject conflicting index: %v", err)
	}

	if _, err := store.Append(ctx, makeWidgetEnv("w-1", "create", map[string]any{"title": "T"}), nil); err != nil {
		t.Fatalf("Append widget: %v", err)
	}
	if _, err := store.Append(ctx, makeGadgetEnv("g-1", "create", map[string]any{"title": "G"}), nil); err != nil {
		t.Fatalf("Append gadget: %v", err)
	}

	if err := db.ApplySchema(testRules()); err != nil {
		t.Fatalf("ApplySchema with a refused type must succeed: %v", err)
	}
	assertGadgetWithheld(t, db)

	check := func(label string, db *projection.DB) {
		t.Helper()
		if _, err := db.Refresh(store, projection.WithSchema(testRules())); err != nil {
			t.Fatalf("%s: Refresh: %v", label, err)
		}
		assertGadgetWithheld(t, db)

		var title string
		if err := db.DB().QueryRow("SELECT f_title FROM o_widget WHERE object_id = 'w-1'").Scan(&title); err != nil || title != "T" {
			t.Fatalf("%s: o_widget w-1 title = %q, err %v; the surviving type must materialize", label, title, err)
		}
		var unknown int
		if err := db.DB().QueryRow("SELECT COUNT(*) FROM unknown_ops WHERE object_id = 'g-1' AND object_type = 'gadget'").Scan(&unknown); err != nil || unknown != 1 {
			t.Fatalf("%s: withheld type's op in unknown_ops = %d, err %v; want 1", label, unknown, err)
		}
		if _, err := db.Objects(projection.ObjectFilter{}); err != nil {
			t.Fatalf("%s: Objects: %v", label, err)
		}
	}

	check("first apply", db)

	// Equal digest: no DDL runs, so the withhold must come from meta.
	digest := metaValue(t, db.DB(), "schema_digest")
	if err := db.ApplySchema(testRules()); err != nil {
		t.Fatalf("ApplySchema (equal digest): %v", err)
	}
	if got := metaValue(t, db.DB(), "schema_digest"); got != digest {
		t.Fatalf("schema_digest changed on an equal-digest apply: %s -> %s", digest, got)
	}
	check("equal-digest apply", db)

	// Reopen: a fresh handle over the same file, then the engine's own
	// ApplySchema-then-Refresh.
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	db2, err := projection.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	if _, err := db2.Objects(projection.ObjectFilter{}); err != nil {
		t.Fatalf("Objects on a reopened cache before any ApplySchema: %v", err)
	}
	check("reopen", db2)
}

// TestApplySchemaWithholdIsDecidedByTheSchemaAlone: dropping the cache and
// rebuilding from the same schema reproduces the same withhold, so the
// withheld set is a function of the schema, never of leftover cache state.
func TestApplySchemaWithholdIsDecidedByTheSchemaAlone(t *testing.T) {
	for i := 0; i < 2; i++ {
		db, err := projection.Open(":memory:")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, err := db.DB().Exec(`CREATE INDEX "` + gadgetTagsIndex + `" ON meta(key)`); err != nil {
			t.Fatalf("inject conflicting index: %v", err)
		}
		if err := db.ApplySchema(testRules()); err != nil {
			t.Fatalf("ApplySchema: %v", err)
		}
		assertGadgetWithheld(t, db)
		db.Close()
	}
}

// TestApplySchemaPropagatesNonLogicErrors: only SQLite's plain SQLITE_ERROR
// class withholds. A read-only database refuses the first CREATE TABLE with
// SQLITE_READONLY, which must still fail the pass: a resource or I/O failure
// is not a statement about the schema.
func TestApplySchemaPropagatesNonLogicErrors(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// One connection, so the pragma applies to the connection ApplySchema uses.
	db.DB().SetMaxOpenConns(1)
	if _, err := db.DB().Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatalf("PRAGMA query_only: %v", err)
	}
	// The error must come from the type DDL itself, not from a later
	// statement after a swallowed withhold: widening the withhold class to
	// every sqlite error would push the failure to "truncate anchor_resolutions".
	err = db.ApplySchema(testRules())
	if err == nil {
		t.Fatal("ApplySchema on a read-only database returned nil; a non-SQLITE_ERROR failure must propagate")
	}
	if !strings.Contains(err.Error(), "exec generated schema") {
		t.Fatalf("ApplySchema error = %v; want the READONLY failure of the type DDL, not a later statement", err)
	}
	if _, err := db.DB().Exec("PRAGMA query_only = OFF"); err != nil {
		t.Fatalf("PRAGMA query_only off: %v", err)
	}
	var n int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM meta WHERE key = 'schema_withheld_types'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("a failed apply recorded schema_withheld_types (n=%d, err=%v)", n, err)
	}
}

// TestApplySchemaDropsStaleGeneratedTable: an o_ table on disk that no
// recorded descriptor names but the new descriptor does (a torn write, a
// stale cache) used to survive the stray sweep and make its own CREATE TABLE
// fail with "already exists" — a refusal decided by leftover cache state, not
// by the schema. Every generated table is dropped and recreated instead.
func TestApplySchemaDropsStaleGeneratedTable(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.DB().Exec("CREATE TABLE o_gadget (stale TEXT)"); err != nil {
		t.Fatalf("create stale table: %v", err)
	}
	if err := db.ApplySchema(testRules()); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	if got := metaValue(t, db.DB(), "schema_withheld_types"); got != "[]" {
		t.Fatalf("schema_withheld_types = %s, want []: a stale table must not withhold a type", got)
	}
	rows, err := db.DB().Query("SELECT name FROM pragma_table_info('o_gadget')")
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	if !reflect.DeepEqual(cols[:1], []string{"object_id"}) {
		t.Fatalf("o_gadget columns = %v, want the regenerated table", cols)
	}
}
