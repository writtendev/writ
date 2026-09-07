package projection_test

// NOTE: This benchmark currently uses a synthetic corpus at the scale measured in the
// WRIT-60 spike (5k tickets x one assignee each). Once downstream bridge/import tooling
// exists, re-point this benchmark at a real imported repository dataset (see WRIT-31 Plan).

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/projection"
)

const (
	benchNumTickets = 5000
)

func openBenchDB(tb testing.TB) *projection.DB {
	tb.Helper()
	tempDir := tb.TempDir()
	dbPath := filepath.Join(tempDir, "bench_projection.db")
	db, err := projection.Open(dbPath)
	if err != nil {
		tb.Fatalf("projection.Open: %v", err)
	}
	if err := db.ApplySchema(neutralTestRules()); err != nil {
		tb.Fatalf("ApplySchema: %v", err)
	}

	seedBenchDB(tb, db)
	return db
}

func seedBenchDB(tb testing.TB, db *projection.DB) {
	tb.Helper()
	rawDB := db.DB()

	tx, err := rawDB.Begin()
	if err != nil {
		tb.Fatalf("begin seed tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Bulk insert tickets and their object rows
	objStmt, err := tx.Prepare("INSERT INTO objects (object_id, object_type, op_count, last_op_id, author_name, author_email, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		tb.Fatalf("prepare objects: %v", err)
	}
	defer objStmt.Close()

	tktStmt, err := tx.Prepare("INSERT INTO o_ticket (object_id, f_title, f_status) VALUES (?, ?, ?)")
	if err != nil {
		tb.Fatalf("prepare tickets: %v", err)
	}
	defer tktStmt.Close()

	asStmt, err := tx.Prepare("INSERT INTO o_ticket__assignees (object_id, item) VALUES (?, ?)")
	if err != nil {
		tb.Fatalf("prepare assignees: %v", err)
	}
	defer asStmt.Close()

	statuses := []string{"open", "in_progress", "closed"}
	authors := []struct{ name, email string }{
		{"Alice", "alice@example.com"},
		{"Bob", "bob@example.com"},
		{"Charlie", "charlie@example.com"},
	}
	assignees := []string{"user:alice", "user:bob", "user:charlie"}

	for i := 0; i < benchNumTickets; i++ {
		tktID := fmt.Sprintf("tkt-%d", i)
		author := authors[i%len(authors)]
		status := statuses[i%len(statuses)]
		createdAt := int64(1700000000 + i*10)

		if _, err := objStmt.Exec(tktID, "ticket", 1, "op-"+tktID, author.name, author.email, createdAt, createdAt); err != nil {
			tb.Fatalf("insert ticket object: %v", err)
		}
		if _, err := tktStmt.Exec(tktID, fmt.Sprintf("Ticket %d for feature", i), status); err != nil {
			tb.Fatalf("insert ticket: %v", err)
		}

		if i%4 != 0 {
			// Assigned
			assignee := assignees[i%len(assignees)]
			if _, err := asStmt.Exec(tktID, assignee); err != nil {
				tb.Fatalf("insert assignee: %v", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit seed tx: %v", err)
	}
}

func BenchmarkObjectsListWithFilter(b *testing.B) {
	db := openBenchDB(b)
	defer db.Close()

	filter := projection.ObjectFilter{
		Type:  []string{"ticket"},
		Text:  "feature",
		Limit: 50,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := db.Objects(filter)
		if err != nil {
			b.Fatalf("Objects: %v", err)
		}
		if len(res) != 50 {
			b.Fatalf("expected 50 objects, got %d", len(res))
		}
	}
}

func BenchmarkObjectGet(b *testing.B) {
	db := openBenchDB(b)
	defer db.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := db.Object("tkt-42")
		if err != nil {
			b.Fatalf("Object: %v", err)
		}
		if res.ObjectID != "tkt-42" {
			b.Fatalf("expected tkt-42, got %s", res.ObjectID)
		}
	}
}

// TestQueryPerformanceBudget asserts wall-clock query budgets over synthetic projection data.
//
// Wall-clock timing assertions in the standard unit suite are prone to flaking on shared
// or loaded CI runners, cold caches, or slower hardware. By default, this test is skipped
// to keep unit tests fast and deterministic (see WRIT-146).
//
// To run the performance budget assertion explicitly:
//
//	WRIT_PERF_BUDGET=1 go test -v ./engine/projection -run TestQueryPerformanceBudget
//	WRIT_PERF_BUDGET=1 go test -race -v ./engine/projection -run TestQueryPerformanceBudget
//
// For standard profiling and regression measurement, run the benchmarks directly:
//
//	go test -bench=. ./engine/projection
func TestQueryPerformanceBudget(t *testing.T) {
	if os.Getenv("WRIT_PERF_BUDGET") == "" || testing.Short() {
		t.Skip("skipping query performance budget test; set WRIT_PERF_BUDGET=1 to run or use go test -bench=.")
	}

	budget := 20 * time.Millisecond
	if isRaceDetector {
		// modernc pure-Go SQLite interpreter runs ~10x slower under the Go race detector
		budget = 250 * time.Millisecond
	}

	db := openBenchDB(t)
	defer db.Close()

	// 1. Budget for Objects list with filter: < budget
	t.Run("ObjectsListFilterBudget", func(t *testing.T) {
		filter := projection.ObjectFilter{
			Type:  []string{"ticket"},
			Text:  "feature",
			Limit: 50,
		}
		// Warm up query
		_, _ = db.Objects(filter)

		start := time.Now()
		const iterations = 10
		for i := 0; i < iterations; i++ {
			res, err := db.Objects(filter)
			if err != nil {
				t.Fatalf("Objects: %v", err)
			}
			if len(res) != 50 {
				t.Fatalf("expected 50 objects, got %d", len(res))
			}
		}
		avg := time.Since(start) / iterations
		if avg > budget {
			t.Errorf("Objects list filter query exceeded budget: avg %v (budget %v)", avg, budget)
		}
	})

	// 2. Budget for a single Object lookup: < budget
	t.Run("ObjectGetBudget", func(t *testing.T) {
		_, _ = db.Object("tkt-42")

		start := time.Now()
		const iterations = 10
		for i := 0; i < iterations; i++ {
			res, err := db.Object("tkt-42")
			if err != nil {
				t.Fatalf("Object: %v", err)
			}
			if res.ObjectID != "tkt-42" {
				t.Fatalf("expected tkt-42, got %s", res.ObjectID)
			}
		}
		avg := time.Since(start) / iterations
		if avg > budget {
			t.Errorf("Object get query exceeded budget: avg %v (budget %v)", avg, budget)
		}
	})
}
