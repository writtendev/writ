package projection_test

// NOTE: This benchmark currently uses a synthetic corpus at the scale measured in the
// WRIT-60 spike (5k reviews × 20 comments = 100k comments, 5k issues).
// Once downstream bridge/import tooling exists, re-point this benchmark at
// a real imported repository dataset (see WRIT-31 Plan).

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/projection"
)

const (
	benchNumReviews = 5000
	benchNumComments = 100000
	benchNumIssues  = 5000
)

func openBenchDB(tb testing.TB) *projection.DB {
	tb.Helper()
	tempDir := tb.TempDir()
	dbPath := filepath.Join(tempDir, "bench_projection.db")
	db, err := projection.Open(dbPath)
	if err != nil {
		tb.Fatalf("projection.Open: %v", err)
	}
	if err := db.ApplySchema(testRules()); err != nil {
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

	// 1. Bulk insert reviews and their object rows
	objStmt, err := tx.Prepare("INSERT INTO objects (object_id, object_type, op_count, last_op_id, author_name, author_email, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		tb.Fatalf("prepare objects: %v", err)
	}
	defer objStmt.Close()

	revStmt, err := tx.Prepare("INSERT INTO o_review (object_id, f_title, f_description, f_status, f_merge_commit, f_reason) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		tb.Fatalf("prepare reviews: %v", err)
	}
	defer revStmt.Close()

	statuses := []string{"open", "merged", "closed"}
	authors := []struct{ name, email string }{
		{"Alice", "alice@example.com"},
		{"Bob", "bob@example.com"},
		{"Charlie", "charlie@example.com"},
	}

	for i := 0; i < benchNumReviews; i++ {
		revID := fmt.Sprintf("rev-%d", i)
		author := authors[i%len(authors)]
		status := statuses[i%len(statuses)]
		createdAt := int64(1700000000 + i*10)

		if _, err := objStmt.Exec(revID, "review", 1, "op-"+revID, author.name, author.email, createdAt, createdAt); err != nil {
			tb.Fatalf("insert rev object: %v", err)
		}
		if _, err := revStmt.Exec(revID, fmt.Sprintf("Review %d for feature", i), fmt.Sprintf("Description of review %d", i), status, "", ""); err != nil {
			tb.Fatalf("insert review: %v", err)
		}
	}

	// 2. Bulk insert comments. f_subject holds the raw JSON bytes create-once
	// preserves verbatim for the untyped `subject` target — the same shape
	// materializeObject writes, and the shape CommentFilter's subject_type /
	// subject_id filters read back via json_extract.
	commStmt, err := tx.Prepare("INSERT INTO o_comment (object_id, f_subject, f_text, f_in_reply_to, f_anchor, f_deleted) VALUES (?, ?, ?, ?, ?, ?)")
	if err != nil {
		tb.Fatalf("prepare comments: %v", err)
	}
	defer commStmt.Close()

	memberStmt, err := tx.Prepare("INSERT INTO o_comment__subject__members (object_id, member, value) VALUES (?, ?, ?)")
	if err != nil {
		tb.Fatalf("prepare subject members: %v", err)
	}
	defer memberStmt.Close()

	for i := 0; i < benchNumComments; i++ {
		commID := fmt.Sprintf("comm-%d", i)
		revID := fmt.Sprintf("rev-%d", i%benchNumReviews)
		author := authors[i%len(authors)]
		createdAt := int64(1700000000 + i)

		var inReplyTo string
		if i%20 > 0 && i > 0 {
			// Reply to parent comment in same review
			parentIndex := i - (i % 20)
			inReplyTo = fmt.Sprintf("comm-%d", parentIndex)
		}

		subject := fmt.Sprintf(`{"object_type":"review","object_id":%q}`, revID)

		if _, err := objStmt.Exec(commID, "comment", 1, "op-"+commID, author.name, author.email, createdAt, createdAt); err != nil {
			tb.Fatalf("insert comm object: %v", err)
		}
		if _, err := commStmt.Exec(commID, subject, fmt.Sprintf("Comment %d content", i), inReplyTo, "", 0); err != nil {
			tb.Fatalf("insert comment: %v", err)
		}
		if _, err := memberStmt.Exec(commID, "object_type", "review"); err != nil {
			tb.Fatalf("insert subject member object_type: %v", err)
		}
		if _, err := memberStmt.Exec(commID, "object_id", revID); err != nil {
			tb.Fatalf("insert subject member object_id: %v", err)
		}
	}

	// 3. Bulk insert issues
	issStmt, err := tx.Prepare("INSERT INTO o_issue (object_id, f_title, f_description, f_state, f_reason) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		tb.Fatalf("prepare issues: %v", err)
	}
	defer issStmt.Close()

	asStmt, err := tx.Prepare("INSERT INTO o_issue__assignees (object_id, item) VALUES (?, ?)")
	if err != nil {
		tb.Fatalf("prepare assignees: %v", err)
	}
	defer asStmt.Close()

	lblStmt, err := tx.Prepare("INSERT INTO o_issue__labels (object_id, item) VALUES (?, ?)")
	if err != nil {
		tb.Fatalf("prepare labels: %v", err)
	}
	defer lblStmt.Close()

	issueStates := []string{"open", "in_progress", "closed"}
	assignees := []string{"user:alice", "user:bob", "user:charlie"}
	labels := []string{"bug", "feature", "docs"}

	for i := 0; i < benchNumIssues; i++ {
		issID := fmt.Sprintf("iss-%d", i)
		author := authors[i%len(authors)]
		state := issueStates[i%len(issueStates)]
		createdAt := int64(1700000000 + i*5)

		if _, err := objStmt.Exec(issID, "issue", 1, "op-"+issID, author.name, author.email, createdAt, createdAt); err != nil {
			tb.Fatalf("insert iss object: %v", err)
		}
		if _, err := issStmt.Exec(issID, fmt.Sprintf("Issue %d title", i), fmt.Sprintf("Issue %d description", i), state, ""); err != nil {
			tb.Fatalf("insert issue: %v", err)
		}

		if i%4 != 0 {
			// Assigned
			assignee := assignees[i%len(assignees)]
			if _, err := asStmt.Exec(issID, assignee); err != nil {
				tb.Fatalf("insert assignee: %v", err)
			}
		}
		label := labels[i%len(labels)]
		if _, err := lblStmt.Exec(issID, label); err != nil {
			tb.Fatalf("insert label: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		tb.Fatalf("commit seed tx: %v", err)
	}
}

func BenchmarkReviewsListWithFilter(b *testing.B) {
	db := openBenchDB(b)
	defer db.Close()

	filter := projection.ReviewFilter{
		Status: []string{"open"},
		Limit:  50,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := db.Reviews(filter)
		if err != nil {
			b.Fatalf("Reviews: %v", err)
		}
		if len(res) != 50 {
			b.Fatalf("expected 50 reviews, got %d", len(res))
		}
	}
}

func BenchmarkIssuesListWithFilter(b *testing.B) {
	db := openBenchDB(b)
	defer db.Close()

	filter := projection.IssueFilter{
		State:    []string{"open"},
		Assignee: []string{"user:alice"},
		Limit:    50,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := db.Issues(filter)
		if err != nil {
			b.Fatalf("Issues: %v", err)
		}
		if len(res) == 0 {
			b.Fatalf("expected > 0 issues")
		}
	}
}


func BenchmarkThreadsAssembly(b *testing.B) {
	db := openBenchDB(b)
	defer db.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		threads, err := db.Threads("review", "rev-42")
		if err != nil {
			b.Fatalf("Threads: %v", err)
		}
		if len(threads) == 0 {
			b.Fatalf("expected > 0 threads for rev-42")
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

	// 1. Budget for Reviews list with filter: < budget
	t.Run("ReviewsListFilterBudget", func(t *testing.T) {
		filter := projection.ReviewFilter{
			Status: []string{"open"},
			Limit:  50,
		}
		// Warm up query
		_, _ = db.Reviews(filter)

		start := time.Now()
		const iterations = 10
		for i := 0; i < iterations; i++ {
			res, err := db.Reviews(filter)
			if err != nil {
				t.Fatalf("Reviews: %v", err)
			}
			if len(res) != 50 {
				t.Fatalf("expected 50 reviews, got %d", len(res))
			}
		}
		avg := time.Since(start) / iterations
		if avg > budget {
			t.Errorf("Reviews list filter query exceeded budget: avg %v (budget %v)", avg, budget)
		}
	})

	// 2. Budget for Issues list with filter: < budget
	t.Run("IssuesListFilterBudget", func(t *testing.T) {
		filter := projection.IssueFilter{
			State:    []string{"open"},
			Assignee: []string{"user:alice"},
			Limit:    50,
		}
		_, _ = db.Issues(filter)

		start := time.Now()
		const iterations = 10
		for i := 0; i < iterations; i++ {
			res, err := db.Issues(filter)
			if err != nil {
				t.Fatalf("Issues: %v", err)
			}
			if len(res) == 0 {
				t.Fatalf("expected > 0 issues")
			}
		}
		avg := time.Since(start) / iterations
		if avg > budget {
			t.Errorf("Issues list filter query exceeded budget: avg %v (budget %v)", avg, budget)
		}
	})

	// 3. Budget for Threads assembly: < budget
	t.Run("ThreadsAssemblyBudget", func(t *testing.T) {
		_, _ = db.Threads("review", "rev-42")

		start := time.Now()
		const iterations = 10
		for i := 0; i < iterations; i++ {
			threads, err := db.Threads("review", "rev-42")
			if err != nil {
				t.Fatalf("Threads: %v", err)
			}
			if len(threads) == 0 {
				t.Fatalf("expected > 0 threads")
			}
		}
		avg := time.Since(start) / iterations
		if avg > budget {
			t.Errorf("Threads assembly query exceeded budget: avg %v (budget %v)", avg, budget)
		}
	})
}
