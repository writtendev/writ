package projection_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/internal/projection"
)

func TestProjectionAndLocalTablesDisjoint(t *testing.T) {
	projDB, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open projection failed: %v", err)
	}
	defer projDB.Close()

	// Projection tables and local tables must be disjoint except for meta
	projDump, err := projDB.DumpTables()
	if err != nil {
		t.Fatalf("DumpTables failed: %v", err)
	}
	localDump, err := projDB.DumpLocalTables()
	if err != nil {
		t.Fatalf("DumpLocalTables failed: %v", err)
	}

	for tableName := range localDump {
		if tableName == "meta" {
			continue
		}
		if _, exists := projDump[tableName]; exists {
			t.Errorf("table %q appears in both projectionTables and localTables", tableName)
		}
	}
}

func TestLocalStoreCRUD(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	// Sync cursor CRUD
	syncTime := time.Now().UTC().Truncate(time.Second)
	if err := db.SetSyncCursor("origin", "refs/writ/writer1/widget", "0123456789abcdef", syncTime); err != nil {
		t.Fatalf("SetSyncCursor failed: %v", err)
	}
	if err := db.SetSyncCursor("origin", "refs/writ/writer1/note", "fedcba9876543210", syncTime); err != nil {
		t.Fatalf("SetSyncCursor 2 failed: %v", err)
	}
	if err := db.SetSyncCursor("upstream", "refs/writ/writer1/widget", "1111222233334444", syncTime); err != nil {
		t.Fatalf("SetSyncCursor 3 failed: %v", err)
	}

	originCursors, err := db.SyncCursors("origin")
	if err != nil {
		t.Fatalf("SyncCursors origin failed: %v", err)
	}
	if len(originCursors) != 2 {
		t.Fatalf("expected 2 cursors for origin, got %d", len(originCursors))
	}

	allCursors, err := db.SyncCursors("")
	if err != nil {
		t.Fatalf("SyncCursors all failed: %v", err)
	}
	if len(allCursors) != 3 {
		t.Fatalf("expected 3 total cursors, got %d", len(allCursors))
	}
}

func TestLocalStateSurvivesRebuild(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	tempDir := t.TempDir()
	projPath := filepath.Join(tempDir, "projection.db")
	localPath := filepath.Join(tempDir, "local.db")

	db, err := projection.Open(projPath, projection.WithLocalPath(localPath))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Append op to DAG and refresh projection
	env := makeWidgetEnv("w-survive", "create", map[string]any{"title": "Widget 1"})
	if _, err := store.Append(ctx, env, nil); err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	if _, err := db.Refresh(store, projection.WithSchema(testRules())); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	// Write local state
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.SetSyncCursor("origin", "refs/writ/0123456789abcdef/widget", "tip-sha", now); err != nil {
		t.Fatalf("SetSyncCursor failed: %v", err)
	}

	localDumpBefore, err := db.DumpLocalTables()
	if err != nil {
		t.Fatalf("DumpLocalTables before rebuild failed: %v", err)
	}

	// 1. Run Rebuild on existing handle
	stats, err := db.Rebuild(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Rebuild failed: %v", err)
	}
	if !stats.Rebuilt {
		t.Fatalf("expected Rebuilt=true")
	}

	localDumpAfterRebuild, err := db.DumpLocalTables()
	if err != nil {
		t.Fatalf("DumpLocalTables after rebuild failed: %v", err)
	}
	if !reflect.DeepEqual(localDumpBefore, localDumpAfterRebuild) {
		t.Fatalf("local state changed after Rebuild:\nbefore: %+v\nafter: %+v", localDumpBefore, localDumpAfterRebuild)
	}

	// 2. Delete projection.db, reopen, Rebuild -> local rows still there
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if err := os.Remove(projPath); err != nil {
		t.Fatalf("Remove projection.db failed: %v", err)
	}

	db2, err := projection.Open(projPath, projection.WithLocalPath(localPath))
	if err != nil {
		t.Fatalf("Open reopened failed: %v", err)
	}
	defer db2.Close()

	if _, err := db2.Rebuild(store, projection.WithSchema(testRules())); err != nil {
		t.Fatalf("Rebuild on fresh projection file failed: %v", err)
	}

	localDumpAfterReopen, err := db2.DumpLocalTables()
	if err != nil {
		t.Fatalf("DumpLocalTables after reopen failed: %v", err)
	}
	if !reflect.DeepEqual(localDumpBefore, localDumpAfterReopen) {
		t.Fatalf("local state changed after projection file deletion and reopen:\nbefore: %+v\nafter: %+v", localDumpBefore, localDumpAfterReopen)
	}

	// 3. Force folded-schema reset by writing stale schema_version in meta and reopening
	if _, err := db2.DB().Exec("UPDATE meta SET value = '9999' WHERE key = 'schema_version'"); err != nil {
		t.Fatalf("force stale schema_version failed: %v", err)
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("Close db2: %v", err)
	}

	db3, err := projection.Open(projPath, projection.WithLocalPath(localPath))
	if err != nil {
		t.Fatalf("Open db3 failed: %v", err)
	}
	defer db3.Close()

	localDumpAfterSchemaReset, err := db3.DumpLocalTables()
	if err != nil {
		t.Fatalf("DumpLocalTables after schema reset: %v", err)
	}
	if !reflect.DeepEqual(localDumpBefore, localDumpAfterSchemaReset) {
		t.Fatalf("local state changed after folded schema reset:\nbefore: %+v\nafter: %+v", localDumpBefore, localDumpAfterSchemaReset)
	}
}

func TestDropAndRebuildReproducesFoldedState(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	tempDir := t.TempDir()
	projPath := filepath.Join(tempDir, "projection.db")
	localPath := filepath.Join(tempDir, "local.db")

	db, err := projection.Open(projPath, projection.WithLocalPath(localPath))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// Build state incrementally
	env1 := makeWidgetEnv("w-drop", "create", map[string]any{
		"title":       "Drop Rebuild Widget",
		"description": "Initial description",
	})
	if _, err := store.Append(ctx, env1, nil); err != nil {
		t.Fatalf("Append 1 failed: %v", err)
	}
	if _, err := db.Refresh(store, projection.WithSchema(testRules())); err != nil {
		t.Fatalf("Refresh 1 failed: %v", err)
	}

	env2 := makeWidgetEnv("w-drop", "update", map[string]any{
		"title": "Updated Drop Rebuild Widget",
	})
	if _, err := store.Append(ctx, env2, nil); err != nil {
		t.Fatalf("Append 2 failed: %v", err)
	}
	if _, err := db.Refresh(store, projection.WithSchema(testRules())); err != nil {
		t.Fatalf("Refresh 2 failed: %v", err)
	}

	incrementalDump, err := db.DumpTables()
	if err != nil {
		t.Fatalf("DumpTables incremental failed: %v", err)
	}

	// Close handle, delete projection file
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if err := os.Remove(projPath); err != nil {
		t.Fatalf("Remove projection.db failed: %v", err)
	}

	// Reopen with fresh schema and Rebuild
	dbFresh, err := projection.Open(projPath, projection.WithLocalPath(localPath))
	if err != nil {
		t.Fatalf("Open dbFresh failed: %v", err)
	}
	defer dbFresh.Close()

	stats, err := dbFresh.Rebuild(store, projection.WithSchema(testRules()))
	if err != nil {
		t.Fatalf("Rebuild fresh failed: %v", err)
	}
	if !stats.Rebuilt || stats.ObjectsTouched != 1 {
		t.Fatalf("unexpected rebuild stats: %+v", stats)
	}

	coldDump, err := dbFresh.DumpTables()
	if err != nil {
		t.Fatalf("DumpTables cold failed: %v", err)
	}

	if !reflect.DeepEqual(incrementalDump, coldDump) {
		t.Fatalf("incremental dump != cold dump after drop and rebuild:\nincremental: %+v\ncold: %+v", incrementalDump, coldDump)
	}
}
