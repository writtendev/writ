package projection_test

import (
	"path/filepath"
	"testing"

	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/engine/state"
)

// TestApplySchemaWritesMetaOnStrategyChangeWithSameDigest is WRIT-192 round
// 3's MAJOR finding, reproduced exactly as it was proven against the
// pre-fix code: buildSnapshot (the digest ApplySchema's old equal-digest
// early return keyed off) covers only column name/SQL type/indexed/PK.
// Strategy has zero effect on the generated column — lww, create-once,
// lattice and tombstone all emit the same ddlColumn (ddl.go's
// buildTypeDescriptor, case "lww", "create-once", "lattice", "tombstone") —
// so switching a target's strategy from lww to tombstone, valueType held
// fixed, leaves the digest unchanged. Under the old code that early return
// skipped the meta write entirely, so schema_query_shapes (which carries
// exactly ValueType and Strategy per target) went stale: the in-process
// descriptor was correct (ApplySchema always assigned d.desc = newDesc),
// but the persisted copy a warm reopen rehydrates from
// (descriptorFromPersisted) kept answering with the old (lww) strategy
// forever — a tombstoned object that should be excluded from a default
// (!IncludeDeleted) listing stayed in it. That is round 2's MAJOR-1 symptom,
// reintroduced on a new trigger. The fix writes the meta keys
// unconditionally, every ApplySchema call, regardless of whether the digest
// changed.
func TestApplySchemaWritesMetaOnStrategyChangeWithSameDigest(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "projection.db")

	db, err := projection.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	lwwRules := map[string][]state.Rule{
		"widget": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"},
			{OpType: "retire", OpVersion: 1, Field: "gone", Strategy: "lww", ValueType: "bool", ObjectType: "widget"},
		},
	}
	if err := db.ApplySchema(lwwRules); err != nil {
		t.Fatalf("ApplySchema (lww): %v", err)
	}

	rawDB := db.DB()
	insertObject(t, rawDB, "widget-1", "widget", 1, "op-widget-1", "Alice Smith", "alice@example.com", 1000, 1000)
	execSQL(t, rawDB, "INSERT INTO o_widget (object_id, f_title, f_gone) VALUES (?, ?, ?)", "widget-1", "a widget", 1)

	var digestBefore string
	if err := rawDB.QueryRow("SELECT value FROM meta WHERE key = 'schema_digest'").Scan(&digestBefore); err != nil {
		t.Fatalf("query schema_digest before strategy change: %v", err)
	}

	tombstoneRules := map[string][]state.Rule{
		"widget": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"},
			{OpType: "retire", OpVersion: 1, Field: "gone", Strategy: "tombstone", ValueType: "bool", ObjectType: "widget"},
		},
	}
	if err := db.ApplySchema(tombstoneRules); err != nil {
		t.Fatalf("ApplySchema (tombstone): %v", err)
	}

	var digestAfter string
	if err := rawDB.QueryRow("SELECT value FROM meta WHERE key = 'schema_digest'").Scan(&digestAfter); err != nil {
		t.Fatalf("query schema_digest after strategy change: %v", err)
	}
	if digestBefore != digestAfter {
		t.Fatalf("schema_digest changed on a strategy-only change (%q -> %q): this test no longer reproduces round 3's MAJOR premise", digestBefore, digestAfter)
	}

	// In-process: the descriptor just built is correct, so the tombstoned
	// widget is excluded from a default listing immediately.
	results, err := db.Objects(projection.ObjectFilter{})
	if err != nil {
		t.Fatalf("Objects (in-process, after strategy change): %v", err)
	}
	for _, o := range results {
		if o.ObjectID == "widget-1" {
			t.Fatalf("in-process Objects() included the tombstoned widget-1 right after ApplySchema: %+v", results)
		}
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Warm reopen: a fresh *DB over the same file, with no ApplySchema call
	// in this process — exactly loadPersistedTables' rehydrate-from-meta
	// path. If the strategy-only change above did not update
	// schema_query_shapes, this reopen still sees the old lww shape and
	// wrongly includes widget-1 in the default listing.
	db2, err := projection.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer db2.Close()

	results2, err := db2.Objects(projection.ObjectFilter{})
	if err != nil {
		t.Fatalf("Objects (warm reopen): %v", err)
	}
	for _, o := range results2 {
		if o.ObjectID == "widget-1" {
			t.Fatalf("warm reopen Objects() included the tombstoned widget-1: schema_query_shapes went stale on a digest-preserving strategy change: %+v", results2)
		}
	}
}
