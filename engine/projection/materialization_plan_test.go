package projection_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/projection"
)

// TestRefreshWithoutSchemaOnReopenedCacheErrors is WRIT-189 round 2's
// MINOR-6 finding: a reopened cache's descriptor starts out name-only
// (descriptorFromPersisted — desc.types is nil, only desc.tables, the
// DumpTables/Rebuild-truncate view, is populated from meta) until this
// process's own first ApplySchema call. Round 1's lazy-Open fix
// (engine/open.go skipping the eager ApplySchema when
// HasGeneratedTables() is already true) widened the window where a
// Refresh/Rebuild call could run against that name-only state: without
// WithSchema, materializeObject's `td == nil` branch quietly routed every
// object to unknown_ops while deleteObjectState found no known table for
// the object's prior type and left its previously generated rows in
// place — a generated table and unknown_ops silently disagreeing about the
// same object, not an error.
//
// No shipped writ path hits this (engine.Store.Refresh/Rebuild always pass
// WithSchema), but engine/projection is a public package a caller can drive
// directly. This reproduces the exact shape: apply a schema, materialize an
// object, close, reopen the same file (a fresh *DB whose descriptor is
// name-only again), and call Refresh with no schema — which must now fail
// clearly rather than silently strand the object.
func TestRefreshWithoutSchemaOnReopenedCacheErrors(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "projection.db")
	_, store := createTestStore(t, "0123456789abcdef")

	env := makeWidgetEnv("w-1", "create", map[string]any{"title": "T"})
	if _, err := store.Append(ctx, env, nil); err != nil {
		t.Fatalf("store.Append failed: %v", err)
	}

	db1, err := projection.Open(dbPath)
	if err != nil {
		t.Fatalf("initial Open failed: %v", err)
	}
	if _, err := db1.Refresh(store, projection.WithSchema(testRules())); err != nil {
		t.Fatalf("initial Refresh (with schema) failed: %v", err)
	}
	var title string
	if err := db1.DB().QueryRow("SELECT f_title FROM o_widget WHERE object_id = 'w-1'").Scan(&title); err != nil {
		t.Fatalf("query o_widget before reopen failed: %v", err)
	}
	if title != "T" {
		t.Fatalf("o_widget.f_title before reopen = %q, want %q", title, "T")
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Reopen: a fresh *DB over the same file, generated tables already on
	// disk (and so recorded in meta's schema_tables), but this process has
	// not called ApplySchema on it yet — exactly the name-only-descriptor
	// state the finding describes.
	db2, err := projection.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	defer db2.Close()

	if _, err := db2.Refresh(store); err == nil {
		t.Fatalf("Refresh without WithSchema on a reopened cache: expected an error, got none")
	} else if !strings.Contains(err.Error(), "materialization plan") {
		t.Fatalf("Refresh without WithSchema on a reopened cache: error %q does not name the missing materialization plan", err.Error())
	}

	// The prior materialization must be untouched by the refused call — no
	// stale-vs-unknown_ops divergence, because nothing was written.
	if err := db2.DB().QueryRow("SELECT f_title FROM o_widget WHERE object_id = 'w-1'").Scan(&title); err != nil {
		t.Fatalf("query o_widget after refused Refresh failed: %v", err)
	}
	if title != "T" {
		t.Fatalf("o_widget.f_title after refused Refresh = %q, want %q (unchanged)", title, "T")
	}
	var unknownCount int
	if err := db2.DB().QueryRow("SELECT COUNT(*) FROM unknown_ops WHERE object_id = 'w-1'").Scan(&unknownCount); err != nil {
		t.Fatalf("query unknown_ops after refused Refresh failed: %v", err)
	}
	if unknownCount != 0 {
		t.Fatalf("unknown_ops count after refused Refresh = %d, want 0 (nothing should have been folded)", unknownCount)
	}

	// Rebuild without WithSchema must refuse identically.
	if _, err := db2.Rebuild(store); err == nil {
		t.Fatalf("Rebuild without WithSchema on a reopened cache: expected an error, got none")
	} else if !strings.Contains(err.Error(), "materialization plan") {
		t.Fatalf("Rebuild without WithSchema on a reopened cache: error %q does not name the missing materialization plan", err.Error())
	}

	// Supplying WithSchema on the reopened handle works exactly as before.
	if _, err := db2.Refresh(store, projection.WithSchema(testRules())); err != nil {
		t.Fatalf("Refresh with schema on the reopened handle failed: %v", err)
	}
}
