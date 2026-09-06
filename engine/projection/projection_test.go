package projection_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/writtendev/writ/engine/projection"
)

func TestOpenCloseMemory(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	// The literal is deliberate: bumping the projection schema has to be a
	// conscious edit, because it is what makes an existing checkout rebuild
	// its cache. WRIT-104 took it to 9 for workflow_states table. WRIT-109
	// took it to 10 for labels table. WRIT-105 took it to 11 for document
	// and section tables. WRIT-106 took it to 12 for issue priority,
	// estimate, and position columns. WRIT-110 took it to 13 for settings
	// table. WRIT-182 took it to 14, dropping the repos/repo_remotes tables.
	if v := projection.SchemaVersion(); v != 14 {
		t.Fatalf("expected schema version 14, got %d", v)
	}

	var version string
	err = db.DB().QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&version)
	if err != nil {
		t.Fatalf("query schema_version failed: %v", err)
	}
	if want := strconv.Itoa(projection.SchemaVersion()); version != want {
		t.Fatalf("stored schema_version %q, want %q", version, want)
	}
}

func TestSchemaVersionMismatchRecreates(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "projection.db")

	// 1. Initial open
	db, err := projection.Open(dbPath)
	if err != nil {
		t.Fatalf("initial Open failed: %v", err)
	}

	// Insert dummy object row
	_, err = db.DB().Exec("INSERT INTO objects (object_id, object_type, op_count, last_op_id, author_name, author_email, created_at, updated_at) VALUES ('obj-1', 'review', 1, 'sha-1', 'alice', 'alice@example.com', 100, 100)")
	if err != nil {
		t.Fatalf("insert dummy object failed: %v", err)
	}

	// Deliberately corrupt schema_version
	_, err = db.DB().Exec("UPDATE meta SET value = '99' WHERE key = 'schema_version'")
	if err != nil {
		t.Fatalf("corrupt schema_version failed: %v", err)
	}
	_ = db.Close()

	// 2. Re-open: should detect mismatch, drop tables, and recreate at the current version
	db2, err := projection.Open(dbPath)
	if err != nil {
		t.Fatalf("re-open after mismatch failed: %v", err)
	}
	defer db2.Close()

	var version string
	err = db2.DB().QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&version)
	if err != nil {
		t.Fatalf("query schema_version failed: %v", err)
	}
	if want := strconv.Itoa(projection.SchemaVersion()); version != want {
		t.Fatalf("recreated schema_version %q, want %q", version, want)
	}

	// The dummy object must no longer exist (tables recreated)
	var count int
	err = db2.DB().QueryRow("SELECT COUNT(*) FROM objects WHERE object_id = 'obj-1'").Scan(&count)
	if err != nil {
		t.Fatalf("query objects failed: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected objects to be wiped on schema mismatch, found %d rows", count)
	}
}

func TestMemoryConnectionPooling(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	// Insert an initial row into objects to ensure data is shared across all concurrent workers.
	_, err = db.DB().Exec("INSERT INTO objects (object_id, object_type, op_count, last_op_id, author_name, author_email, created_at, updated_at) VALUES ('obj-pool-1', 'review', 1, 'sha-1', 'alice', 'alice@example.com', 100, 100)")
	if err != nil {
		t.Fatalf("insert object failed: %v", err)
	}

	const numWorkers = 16
	const queriesPerWorker = 25

	var wg sync.WaitGroup
	errCh := make(chan error, numWorkers*queriesPerWorker)

	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < queriesPerWorker; j++ {
				var count int
				if err := db.DB().QueryRow("SELECT COUNT(*) FROM objects").Scan(&count); err != nil {
					errCh <- fmt.Errorf("worker %d objects query %d: %w", workerID, j, err)
					return
				}
				if count != 1 {
					errCh <- fmt.Errorf("worker %d objects query %d: expected 1 row, got %d", workerID, j, count)
					return
				}

				var version string
				if err := db.DB().QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&version); err != nil {
					errCh <- fmt.Errorf("worker %d meta query %d: %w", workerID, j, err)
					return
				}
				if want := strconv.Itoa(projection.SchemaVersion()); version != want {
					errCh <- fmt.Errorf("worker %d meta query %d: expected version %s, got %s", workerID, j, want, version)
					return
				}

				var localVersion string
				if err := db.LocalDB().QueryRow("SELECT value FROM meta WHERE key = 'local_schema_version'").Scan(&localVersion); err != nil {
					errCh <- fmt.Errorf("worker %d local meta query %d: %w", workerID, j, err)
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("connection pooling error: %v", err)
	}
}

func TestPragmasConfigured(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "pragmas.db")

	db, err := projection.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	checkPragmas := func(t *testing.T, name string, sqlDB *sql.DB, expectWAL bool) {
		t.Helper()
		var busyTimeout int
		if err := sqlDB.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("%s: query busy_timeout failed: %v", name, err)
		}
		if busyTimeout != 5000 {
			t.Errorf("%s: expected busy_timeout=5000, got %d", name, busyTimeout)
		}

		var foreignKeys int
		if err := sqlDB.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("%s: query foreign_keys failed: %v", name, err)
		}
		if foreignKeys != 1 {
			t.Errorf("%s: expected foreign_keys=1, got %d", name, foreignKeys)
		}

		var synchronous int
		if err := sqlDB.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatalf("%s: query synchronous failed: %v", name, err)
		}
		if synchronous != 1 { // 1 = NORMAL
			t.Errorf("%s: expected synchronous=1 (NORMAL), got %d", name, synchronous)
		}

		if expectWAL {
			var journalMode string
			if err := sqlDB.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
				t.Fatalf("%s: query journal_mode failed: %v", name, err)
			}
			if strings.ToLower(journalMode) != "wal" {
				t.Errorf("%s: expected journal_mode=wal, got %s", name, journalMode)
			}
		}
	}

	t.Run("FileDB_Main", func(t *testing.T) {
		checkPragmas(t, "db", db.DB(), true)
	})
	t.Run("FileDB_Local", func(t *testing.T) {
		checkPragmas(t, "localDB", db.LocalDB(), true)
	})

	// Verify that a freshly checked out connection from the pool inherits these pragmas via DSN.
	t.Run("FileDB_NewConn", func(t *testing.T) {
		ctx := context.Background()
		conn1, err := db.DB().Conn(ctx)
		if err != nil {
			t.Fatalf("get conn1: %v", err)
		}
		defer conn1.Close()

		conn2, err := db.DB().Conn(ctx)
		if err != nil {
			t.Fatalf("get conn2: %v", err)
		}
		defer conn2.Close()

		var busyTimeout, foreignKeys, synchronous int
		if err := conn2.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("conn2: query busy_timeout failed: %v", err)
		}
		if busyTimeout != 5000 {
			t.Errorf("conn2: expected busy_timeout=5000, got %d", busyTimeout)
		}
		if err := conn2.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("conn2: query foreign_keys failed: %v", err)
		}
		if foreignKeys != 1 {
			t.Errorf("conn2: expected foreign_keys=1, got %d", foreignKeys)
		}
		if err := conn2.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatalf("conn2: query synchronous failed: %v", err)
		}
		if synchronous != 1 {
			t.Errorf("conn2: expected synchronous=1, got %d", synchronous)
		}
	})

	memDB, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer memDB.Close()

	t.Run("MemoryDB_Main", func(t *testing.T) {
		checkPragmas(t, "memoryDB", memDB.DB(), false)
	})
	t.Run("MemoryDB_Local", func(t *testing.T) {
		checkPragmas(t, "memoryLocalDB", memDB.LocalDB(), false)
	})
}

func TestFileStoreConcurrency(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "concurrency.db")

	db, err := projection.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	const numWriters = 6
	const writesPerWriter = 20
	const numReaders = 6
	const readsPerReader = 40

	var wg sync.WaitGroup
	errCh := make(chan error, numWriters+numReaders)

	// Launch writers
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < writesPerWriter; j++ {
				tx, err := db.DB().Begin()
				if err != nil {
					errCh <- fmt.Errorf("writer %d begin tx %d: %w", workerID, j, err)
					return
				}
				objID := fmt.Sprintf("obj-w%d-%d", workerID, j)
				_, err = tx.Exec(
					"INSERT INTO objects (object_id, object_type, op_count, last_op_id, author_name, author_email, created_at, updated_at) VALUES (?, 'review', 1, 'sha-1', 'alice', 'alice@example.com', 100, 100)",
					objID,
				)
				if err != nil {
					_ = tx.Rollback()
					errCh <- fmt.Errorf("writer %d insert tx %d: %w", workerID, j, err)
					return
				}
				if err := tx.Commit(); err != nil {
					errCh <- fmt.Errorf("writer %d commit tx %d: %w", workerID, j, err)
					return
				}
			}
		}(i)
	}

	// Launch readers
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < readsPerReader; j++ {
				var count int
				if err := db.DB().QueryRow("SELECT COUNT(*) FROM objects").Scan(&count); err != nil {
					errCh <- fmt.Errorf("reader %d query %d: %w", workerID, j, err)
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrency error: %v", err)
	}

	var totalObjects int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM objects").Scan(&totalObjects); err != nil {
		t.Fatalf("final count query failed: %v", err)
	}
	if want := numWriters * writesPerWriter; totalObjects != want {
		t.Errorf("expected %d total objects, got %d", want, totalObjects)
	}
}
