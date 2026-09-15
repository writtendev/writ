package sqlitedriver

import (
	"database/sql"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	_ "modernc.org/sqlite"
)

// openDB opens a fresh on-disk database (projection is file-backed in
// practice, not :memory:) under driverName ("sqlite3" for mattn,
// "sqlite" for modernc), with the schema applied.
func openDB(b *testing.B, driverName string) *sql.DB {
	b.Helper()
	path := filepath.Join(b.TempDir(), "projection.db")
	db, err := sql.Open(driverName, path)
	if err != nil {
		b.Fatalf("open %s: %v", driverName, err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		b.Fatalf("%s: set WAL: %v", driverName, err)
	}
	if _, err := db.Exec(schema); err != nil {
		b.Fatalf("%s: apply schema: %v", driverName, err)
	}
	return db
}

// bulkInsert loads numItems items and numEntries entries in one
// transaction per table, the shape a from-scratch refold takes: everything
// derived from the op log, committed once, not row by row.
func bulkInsert(b *testing.B, db *sql.DB, driverName string) {
	b.Helper()

	tx, err := db.Begin()
	if err != nil {
		b.Fatalf("%s: begin: %v", driverName, err)
	}
	itemStmt, err := tx.Prepare("INSERT INTO items (id, status, base, head) VALUES (?, ?, ?, ?)")
	if err != nil {
		b.Fatalf("%s: prepare items: %v", driverName, err)
	}
	for i := 0; i < numItems; i++ {
		id := fmt.Sprintf("item-%d", i)
		if _, err := itemStmt.Exec(id, "open", "deadbeef", "cafebabe"); err != nil {
			b.Fatalf("%s: insert item: %v", driverName, err)
		}
	}
	itemStmt.Close()

	entryStmt, err := tx.Prepare("INSERT INTO entries (id, item_id, author, body, blob_hash, created_at, resolved) VALUES (?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		b.Fatalf("%s: prepare entries: %v", driverName, err)
	}
	for i := 0; i < numEntries; i++ {
		itemID := fmt.Sprintf("item-%d", i%numItems)
		id := fmt.Sprintf("entry-%d", i)
		if _, err := entryStmt.Exec(id, itemID, "alice", "looks good to me, one nit below", "abc123", int64(i), 0); err != nil {
			b.Fatalf("%s: insert entry: %v", driverName, err)
		}
	}
	entryStmt.Close()

	if err := tx.Commit(); err != nil {
		b.Fatalf("%s: commit: %v", driverName, err)
	}
}

func BenchmarkBulkInsert_Mattn(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		db := openDB(b, "sqlite3")
		b.StartTimer()

		bulkInsert(b, db, "sqlite3")

		b.StopTimer()
		db.Close()
		b.StartTimer()
	}
}

func BenchmarkBulkInsert_Modernc(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		db := openDB(b, "sqlite")
		b.StartTimer()

		bulkInsert(b, db, "sqlite")

		b.StopTimer()
		db.Close()
		b.StartTimer()
	}
}

// indexedRead exercises the query shape an item view uses: every entry
// on one item, via the item_id index.
func indexedRead(b *testing.B, db *sql.DB, driverName string, rng *rand.Rand) {
	b.Helper()
	itemID := fmt.Sprintf("item-%d", rng.Intn(numItems))
	rows, err := db.Query("SELECT id, author, body FROM entries WHERE item_id = ?", itemID)
	if err != nil {
		b.Fatalf("%s: query: %v", driverName, err)
	}
	count := 0
	for rows.Next() {
		var id, author, body string
		if err := rows.Scan(&id, &author, &body); err != nil {
			b.Fatalf("%s: scan: %v", driverName, err)
		}
		count++
	}
	rows.Close()
	if count != entriesPerItem {
		b.Fatalf("%s: expected %d entries, got %d", driverName, entriesPerItem, count)
	}
}

func BenchmarkIndexedRead_Mattn(b *testing.B) {
	db := openDB(b, "sqlite3")
	defer db.Close()
	bulkInsert(b, db, "sqlite3")
	rng := rand.New(rand.NewSource(1))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		indexedRead(b, db, "sqlite3", rng)
	}
}

func BenchmarkIndexedRead_Modernc(b *testing.B) {
	db := openDB(b, "sqlite")
	defer db.Close()
	bulkInsert(b, db, "sqlite")
	rng := rand.New(rand.NewSource(1))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		indexedRead(b, db, "sqlite", rng)
	}
}
