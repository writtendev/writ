package projection_test

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/writtendev/writ/engine/projection"
)

func setupSeededDB(t *testing.T) *projection.DB {
	t.Helper()
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}

	rawDB := db.DB()

	// Insert objects
	insertObject(t, rawDB, "rev-1", "review", 2, "op-rev-1b", "Alice Smith", "alice@example.com", 1000, 1100)
	insertObject(t, rawDB, "rev-2", "review", 1, "op-rev-2", "Bob Jones", "bob@example.com", 1200, 1200)
	insertObject(t, rawDB, "rev-3", "review", 1, "op-rev-3", "Charlie Brown", "charlie@example.com", 1300, 1300)

	insertObject(t, rawDB, "iss-1", "issue", 3, "op-iss-1c", "Alice Smith", "alice@example.com", 2000, 2200)
	insertObject(t, rawDB, "iss-2", "issue", 1, "op-iss-2", "Bob Jones", "bob@example.com", 2100, 2100)
	insertObject(t, rawDB, "iss-3", "issue", 2, "op-iss-3b", "Charlie Brown", "charlie@example.com", 2300, 2400)
	insertObject(t, rawDB, "iss-4", "issue", 1, "op-iss-4", "Dave Wilson", "dave@example.com", 2500, 2500)

	insertObject(t, rawDB, "comm-1", "comment", 1, "op-comm-1", "Alice Smith", "alice@example.com", 3000, 3000)
	insertObject(t, rawDB, "comm-2", "comment", 1, "op-comm-2", "Bob Jones", "bob@example.com", 3100, 3100)
	insertObject(t, rawDB, "comm-3", "comment", 1, "op-comm-3", "Charlie Brown", "charlie@example.com", 3200, 3200)
	insertObject(t, rawDB, "comm-4", "comment", 1, "op-comm-4", "Dave Wilson", "dave@example.com", 3300, 3300)

	// Insert reviews
	execSQL(t, rawDB, "INSERT INTO reviews (object_id, title, description, status, merge_commit, reason) VALUES (?, ?, ?, ?, ?, ?)",
		"rev-1", "Fix 100% CPU in loop_worker", "Resolves high CPU usage during batching", "open", "", "")
	execSQL(t, rawDB, "INSERT INTO review_revisions (review_object_id, revision_index, base, head) VALUES (?, ?, ?, ?)",
		"rev-1", 0, "base-1", "head-1")
	execSQL(t, rawDB, "INSERT INTO review_assignees (review_object_id, assignee) VALUES (?, ?)", "rev-1", "user:alice")
	execSQL(t, rawDB, "INSERT INTO review_assignees (review_object_id, assignee) VALUES (?, ?)", "rev-1", "user:bob")
	execSQL(t, rawDB, "INSERT INTO review_labels (review_object_id, label) VALUES (?, ?)", "rev-1", "area/engine")
	execSQL(t, rawDB, "INSERT INTO review_labels (review_object_id, label) VALUES (?, ?)", "rev-1", "needs-docs")
	execSQL(t, rawDB, "INSERT INTO review_links (review_object_id, target, target_type, relation) VALUES (?, ?, ?, ?)",
		"rev-1", "iss-1", "issue", "fixes")
	execSQL(t, rawDB, "INSERT INTO approvals (review_object_id, subject, revision, verdict, message) VALUES (?, ?, ?, ?, ?)",
		"rev-1", "bob@example.com", "head-1", "approved", "LGTM")

	execSQL(t, rawDB, "INSERT INTO reviews (object_id, title, description, status, merge_commit, reason) VALUES (?, ?, ?, ?, ?, ?)",
		"rev-2", "Add feature foo_bar", "Implements foo_bar integration", "merged", "merge-sha-2", "")
	execSQL(t, rawDB, "INSERT INTO review_assignees (review_object_id, assignee) VALUES (?, ?)", "rev-2", "user:bob")
	execSQL(t, rawDB, "INSERT INTO review_labels (review_object_id, label) VALUES (?, ?)", "rev-2", "area/engine")
	execSQL(t, rawDB, "INSERT INTO reviews (object_id, title, description, status, merge_commit, reason) VALUES (?, ?, ?, ?, ?, ?)",
		"rev-3", "Refactor storage layer", "Clean up legacy drivers", "closed", "", "abandoned")

	// Insert issues
	execSQL(t, rawDB, "INSERT INTO issues (object_id, title, description, state, reason) VALUES (?, ?, ?, ?, ?)",
		"iss-1", "Memory leak in 100% workload", "Under 100% load, buffer_pool grows unbounded", "open", "")
	execSQL(t, rawDB, "INSERT INTO issue_assignees (issue_object_id, assignee) VALUES (?, ?)", "iss-1", "user:alice")
	execSQL(t, rawDB, "INSERT INTO issue_assignees (issue_object_id, assignee) VALUES (?, ?)", "iss-1", "user:bob")
	execSQL(t, rawDB, "INSERT INTO issue_labels (issue_object_id, label) VALUES (?, ?)", "iss-1", "bug")
	execSQL(t, rawDB, "INSERT INTO issue_labels (issue_object_id, label) VALUES (?, ?)", "iss-1", "perf")
	execSQL(t, rawDB, "INSERT INTO issue_links (issue_object_id, target, target_type, relation) VALUES (?, ?, ?, ?)",
		"iss-1", "writ#2", "issue", "blocks")

	execSQL(t, rawDB, "INSERT INTO issues (object_id, title, description, state, reason) VALUES (?, ?, ?, ?, ?)",
		"iss-2", "Support foo_bar in CLI", "CLI flag --foo_bar should be supported", "in_progress", "")
	execSQL(t, rawDB, "INSERT INTO issue_assignees (issue_object_id, assignee) VALUES (?, ?)", "iss-2", "user:bob")
	execSQL(t, rawDB, "INSERT INTO issue_labels (issue_object_id, label) VALUES (?, ?)", "iss-2", "feature")

	execSQL(t, rawDB, "INSERT INTO issues (object_id, title, description, state, reason) VALUES (?, ?, ?, ?, ?)",
		"iss-3", "Documentation typo in docs", "Fix typo in readme", "closed", "completed")
	execSQL(t, rawDB, "INSERT INTO issue_labels (issue_object_id, label) VALUES (?, ?)", "iss-3", "docs")

	execSQL(t, rawDB, "INSERT INTO issues (object_id, title, description, state, reason) VALUES (?, ?, ?, ?, ?)",
		"iss-4", "Unassigned issue for triage", "Needs investigation", "open", "")

	// Insert comments
	execSQL(t, rawDB, "INSERT INTO comments (object_id, subject_type, subject_id, text, in_reply_to, anchor, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"comm-1", "review", "rev-1", "Initial thought on loop_worker: 100% is high", "", "", 0)
	execSQL(t, rawDB, "INSERT INTO comments (object_id, subject_type, subject_id, text, in_reply_to, anchor, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"comm-2", "review", "rev-1", "Reply to initial thought", "comm-1", "", 0)
	execSQL(t, rawDB, "INSERT INTO comments (object_id, subject_type, subject_id, text, in_reply_to, anchor, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"comm-3", "review", "rev-1", "Nested reply under comm-2", "comm-2", "", 0)
	execSQL(t, rawDB, "INSERT INTO comments (object_id, subject_type, subject_id, text, in_reply_to, anchor, deleted) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"comm-4", "review", "rev-1", "Deleted comment in rev-1", "", "", 1)

	// Insert code_tips and anchor_resolutions
	execSQL(t, rawDB, "INSERT INTO code_tips (ref_name, tip) VALUES (?, ?)", "refs/heads/main", "tip-commit-1")
	execSQL(t, rawDB, "INSERT INTO anchor_resolutions (comment_object_id, target_commit, side, outcome, match, path, start_line, end_line, reason) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		"comm-1", "tip-commit-1", "new", "resolved", "exact", "pkg/loop.go", 10, 15, "found match")

	return db
}

func insertObject(t *testing.T, db *sql.DB, objectID, objectType string, opCount int, lastOpID, authorName, authorEmail string, createdAt, updatedAt int64) {
	t.Helper()
	execSQL(t, db, "INSERT INTO objects (object_id, object_type, op_count, last_op_id, author_name, author_email, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		objectID, objectType, opCount, lastOpID, authorName, authorEmail, createdAt, updatedAt)
}

func execSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("execSQL %q: %v", query, err)
	}
}

func TestReviewsFilter(t *testing.T) {
	db := setupSeededDB(t)
	defer db.Close()

	tests := []struct {
		name    string
		filter  projection.ReviewFilter
		wantIDs []string
	}{
		{
			name:    "empty filter returns all reviews by created_at asc",
			filter:  projection.ReviewFilter{},
			wantIDs: []string{"rev-1", "rev-2", "rev-3"},
		},
		{
			name:    "filter by single status",
			filter:  projection.ReviewFilter{Status: []string{"open"}},
			wantIDs: []string{"rev-1"},
		},
		{
			name:    "filter by multiple statuses",
			filter:  projection.ReviewFilter{Status: []string{"merged", "closed"}},
			wantIDs: []string{"rev-2", "rev-3"},
		},
		{
			name:    "filter by author email",
			filter:  projection.ReviewFilter{Author: []string{"alice@example.com"}},
			wantIDs: []string{"rev-1"},
		},
		{
			name:    "filter by author name",
			filter:  projection.ReviewFilter{Author: []string{"Bob Jones"}},
			wantIDs: []string{"rev-2"},
		},
		{
			name:    "filter by substring text in title",
			filter:  projection.ReviewFilter{Text: "storage"},
			wantIDs: []string{"rev-3"},
		},
		{
			name:    "filter by substring text in description case-insensitive",
			filter:  projection.ReviewFilter{Text: "bAtChInG"},
			wantIDs: []string{"rev-1"},
		},
		{
			name:    "filter text with literal percent wildcard escaping",
			filter:  projection.ReviewFilter{Text: "100%"},
			wantIDs: []string{"rev-1"},
		},
		{
			name:    "filter text with literal underscore wildcard escaping",
			filter:  projection.ReviewFilter{Text: "foo_bar"},
			wantIDs: []string{"rev-2"},
		},
		{
			name:    "filter by single assignee",
			filter:  projection.ReviewFilter{Assignee: []string{"user:alice"}},
			wantIDs: []string{"rev-1"},
		},
		{
			name:    "filter by assignee matching multiple reviews",
			filter:  projection.ReviewFilter{Assignee: []string{"user:bob"}},
			wantIDs: []string{"rev-1", "rev-2"},
		},
		{
			name:    "filter by single label",
			filter:  projection.ReviewFilter{Label: []string{"needs-docs"}},
			wantIDs: []string{"rev-1"},
		},
		{
			name:    "filter by label matching multiple reviews",
			filter:  projection.ReviewFilter{Label: []string{"area/engine"}},
			wantIDs: []string{"rev-1", "rev-2"},
		},
		{
			name:    "filter text no match",
			filter:  projection.ReviewFilter{Text: "nonexistent"},
			wantIDs: []string{},
		},
		{
			name:    "order by created_at desc",
			filter:  projection.ReviewFilter{OrderBy: projection.OrderByCreatedAtDesc},
			wantIDs: []string{"rev-3", "rev-2", "rev-1"},
		},
		{
			name:    "pagination limit and offset",
			filter:  projection.ReviewFilter{Limit: 1, Offset: 1},
			wantIDs: []string{"rev-2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := db.Reviews(tt.filter)
			if err != nil {
				t.Fatalf("Reviews failed: %v", err)
			}
			var gotIDs []string
			for _, r := range res {
				gotIDs = append(gotIDs, r.ObjectID)
			}
			if len(gotIDs) == 0 && len(tt.wantIDs) == 0 {
				return
			}
			if !reflect.DeepEqual(gotIDs, tt.wantIDs) {
				t.Errorf("got IDs %v, want %v", gotIDs, tt.wantIDs)
			}
		})
	}
}

func TestIssuesFilter(t *testing.T) {
	db := setupSeededDB(t)
	defer db.Close()

	tests := []struct {
		name    string
		filter  projection.IssueFilter
		wantIDs []string
	}{
		{
			name:    "empty filter returns all issues by created_at asc",
			filter:  projection.IssueFilter{},
			wantIDs: []string{"iss-1", "iss-2", "iss-3", "iss-4"},
		},
		{
			name:    "filter by state",
			filter:  projection.IssueFilter{State: []string{"open"}},
			wantIDs: []string{"iss-1", "iss-4"},
		},
		{
			name:    "filter by single assignee",
			filter:  projection.IssueFilter{Assignee: []string{"user:alice"}},
			wantIDs: []string{"iss-1"},
		},
		{
			name:    "filter by assignee matching multiple issues",
			filter:  projection.IssueFilter{Assignee: []string{"user:bob"}},
			wantIDs: []string{"iss-1", "iss-2"},
		},
		{
			name:    "filter by label",
			filter:  projection.IssueFilter{Label: []string{"bug"}},
			wantIDs: []string{"iss-1"},
		},
		{
			name:    "filter by text in title with literal percent",
			filter:  projection.IssueFilter{Text: "100%"},
			wantIDs: []string{"iss-1"},
		},
		{
			name:    "filter by text in description with literal underscore",
			filter:  projection.IssueFilter{Text: "foo_bar"},
			wantIDs: []string{"iss-2"},
		},
		{
			name: "combined filter: state + label + assignee",
			filter: projection.IssueFilter{
				State:    []string{"open"},
				Assignee: []string{"user:alice"},
				Label:    []string{"bug"},
			},
			wantIDs: []string{"iss-1"},
		},
		{
			name:    "order by title asc",
			filter:  projection.IssueFilter{OrderBy: projection.OrderByTitleAsc},
			wantIDs: []string{"iss-3", "iss-1", "iss-2", "iss-4"},
		},
		{
			name:    "pagination limit 2 offset 1",
			filter:  projection.IssueFilter{Limit: 2, Offset: 1},
			wantIDs: []string{"iss-2", "iss-3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := db.Issues(tt.filter)
			if err != nil {
				t.Fatalf("Issues failed: %v", err)
			}
			var gotIDs []string
			for _, r := range res {
				gotIDs = append(gotIDs, r.ObjectID)
			}
			if len(gotIDs) == 0 && len(tt.wantIDs) == 0 {
				return
			}
			if !reflect.DeepEqual(gotIDs, tt.wantIDs) {
				t.Errorf("got IDs %v, want %v", gotIDs, tt.wantIDs)
			}
		})
	}
}

func TestCommentsFilterAndResolutions(t *testing.T) {
	db := setupSeededDB(t)
	defer db.Close()

	// Default IncludeDeleted: false should exclude comm-4
	res, err := db.Comments(projection.CommentFilter{SubjectID: "rev-1"})
	if err != nil {
		t.Fatalf("Comments: %v", err)
	}
	if len(res) != 3 {
		t.Fatalf("expected 3 non-deleted comments, got %d", len(res))
	}

	// First comment should have resolved position from code_tips default
	if res[0].ObjectID == "comm-1" {
		if len(res[0].Resolved) != 1 {
			t.Fatalf("expected 1 resolution for comm-1, got %d", len(res[0].Resolved))
		}
		if res[0].Resolved[0].Path != "pkg/loop.go" || res[0].Resolved[0].StartLine != 10 {
			t.Errorf("unexpected resolution: %+v", res[0].Resolved[0])
		}
	}

	// IncludeDeleted: true should include comm-4
	allRes, err := db.Comments(projection.CommentFilter{SubjectID: "rev-1", IncludeDeleted: true})
	if err != nil {
		t.Fatalf("Comments with IncludeDeleted: %v", err)
	}
	if len(allRes) != 4 {
		t.Fatalf("expected 4 comments including deleted, got %d", len(allRes))
	}

	// Substring search with wildcards
	txtRes, err := db.Comments(projection.CommentFilter{Text: "100%"})
	if err != nil {
		t.Fatalf("Comments by Text: %v", err)
	}
	if len(txtRes) != 1 || txtRes[0].ObjectID != "comm-1" {
		t.Errorf("expected only comm-1, got %v", txtRes)
	}
}

func TestObjectsCrossTypeFilter(t *testing.T) {
	db := setupSeededDB(t)
	defer db.Close()

	// Filter by type
	revObjs, err := db.Objects(projection.ObjectFilter{Type: []string{"review"}})
	if err != nil {
		t.Fatalf("Objects(review): %v", err)
	}
	if len(revObjs) != 3 {
		t.Fatalf("expected 3 review objects, got %d", len(revObjs))
	}

	issObjs, err := db.Objects(projection.ObjectFilter{Type: []string{"issue"}})
	if err != nil {
		t.Fatalf("Objects(issue): %v", err)
	}
	if len(issObjs) != 4 {
		t.Fatalf("expected 4 issue objects, got %d", len(issObjs))
	}

	// Cross-type text search
	crossObjs, err := db.Objects(projection.ObjectFilter{Text: "100%"})
	if err != nil {
		t.Fatalf("Objects(100%%): %v", err)
	}
	// rev-1 (title), iss-1 (title/desc), comm-1 (text)
	if len(crossObjs) != 3 {
		t.Fatalf("expected 3 objects matching '100%%', got %d (%+v)", len(crossObjs), crossObjs)
	}
}

func TestThreadsAssembly(t *testing.T) {
	db := setupSeededDB(t)
	defer db.Close()

	threads, err := db.Threads("review", "rev-1")
	if err != nil {
		t.Fatalf("Threads: %v", err)
	}

	// We expect 2 root threads:
	// Root 1: comm-1 -> Reply: comm-2 -> Reply: comm-3
	// Root 2: comm-4 (deleted)
	if len(threads) != 2 {
		t.Fatalf("expected 2 root threads, got %d", len(threads))
	}

	root1 := threads[0]
	if root1.ObjectID != "comm-1" {
		t.Errorf("expected first root to be comm-1, got %s", root1.ObjectID)
	}
	if len(root1.Replies) != 1 {
		t.Fatalf("expected comm-1 to have 1 reply, got %d", len(root1.Replies))
	}

	reply1 := root1.Replies[0]
	if reply1.ObjectID != "comm-2" {
		t.Errorf("expected reply to be comm-2, got %s", reply1.ObjectID)
	}
	if len(reply1.Replies) != 1 {
		t.Fatalf("expected comm-2 to have 1 reply, got %d", len(reply1.Replies))
	}

	nestedReply := reply1.Replies[0]
	if nestedReply.ObjectID != "comm-3" {
		t.Errorf("expected nested reply to be comm-3, got %s", nestedReply.ObjectID)
	}
	if len(nestedReply.Replies) != 0 {
		t.Errorf("expected comm-3 to have 0 replies, got %d", len(nestedReply.Replies))
	}

	root2 := threads[1]
	if root2.ObjectID != "comm-4" {
		t.Errorf("expected second root to be comm-4, got %s", root2.ObjectID)
	}
	if !root2.Comment.Deleted {
		t.Errorf("expected comm-4 to have Deleted = true")
	}
}

func TestReviewLabelsAndLinksDetails(t *testing.T) {
	db := setupSeededDB(t)
	defer db.Close()

	res, err := db.Review("rev-1")
	if err != nil {
		t.Fatalf("Review(rev-1) failed: %v", err)
	}

	wantLabels := []string{"area/engine", "needs-docs"}
	if !reflect.DeepEqual(res.Review.Labels, wantLabels) {
		t.Errorf("labels mismatch: got %v, want %v", res.Review.Labels, wantLabels)
	}

	if len(res.Review.Links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(res.Review.Links))
	}
	link := res.Review.Links[0]
	if link.Target != "iss-1" || link.TargetType != "issue" || link.Relation != "fixes" {
		t.Errorf("link mismatch: got %+v", link)
	}
}

func TestLabelsQuery(t *testing.T) {
	db := setupSeededDB(t)
	defer db.Close()

	rawDB := db.DB()
	execSQL(t, rawDB, "INSERT INTO labels (object_id, name, color, description, author_name, author_email, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		"lbl-1", "bug", "#d73a4a", "Bug report", "Alice", "alice@example.com", 1000, 1000)
	execSQL(t, rawDB, "INSERT INTO labels (object_id, name, color, description, author_name, author_email, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		"lbl-2", "feature", "#a2eeef", "New feature", "Bob", "bob@example.com", 1100, 1100)

	labels, err := db.Labels(projection.LabelFilter{})
	if err != nil {
		t.Fatalf("Labels() failed: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(labels))
	}
	if labels[0].Label.Name != "bug" || labels[1].Label.Name != "feature" {
		t.Errorf("unexpected labels order: %+v", labels)
	}

	l, err := db.Label("lbl-1")
	if err != nil {
		t.Fatalf("Label(lbl-1) failed: %v", err)
	}
	if l.Label.Name != "bug" || l.Label.Color != "#d73a4a" {
		t.Errorf("unexpected label: %+v", l)
	}

	_, err = db.Label("nonexistent")
	if err == nil {
		t.Errorf("expected error for nonexistent label, got nil")
	}
}

func TestLabelMatchingByNameAndID(t *testing.T) {
	db := setupSeededDB(t)
	defer db.Close()

	rawDB := db.DB()
	labelID := "0123456789abcdef0123456789abcdef"
	execSQL(t, rawDB, "INSERT INTO labels (object_id, name, color, description, author_name, author_email, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		labelID, "bug", "#d73a4a", "Bug report", "Alice", "alice@example.com", 1000, 1000)

	// iss-1 in setupSeededDB has bare string label "bug"
	// 1. Filter by canonical label ID matches iss-1 because "bug" resolves to labelID
	res, err := db.Issues(projection.IssueFilter{Label: []string{labelID}})
	if err != nil {
		t.Fatalf("Issues(Label ID) failed: %v", err)
	}
	found := false
	for _, issue := range res {
		if issue.ObjectID == "iss-1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected iss-1 to match when filtering by label ID %s", labelID)
	}

	// 2. Add iss-5 with label = labelID
	insertObject(t, rawDB, "iss-5", "issue", 1, "op-iss-5", "Alice", "alice@example.com", 2600, 2600)
	execSQL(t, rawDB, "INSERT INTO issues (object_id, title, description, state, reason) VALUES (?, ?, ?, ?, ?)",
		"iss-5", "Issue with label ID", "Test", "open", "")
	execSQL(t, rawDB, "INSERT INTO issue_labels (issue_object_id, label) VALUES (?, ?)", "iss-5", labelID)

	// 3. Filter by label name "bug" matches both iss-1 (bare "bug") and iss-5 (label ID)
	resByName, err := db.Issues(projection.IssueFilter{Label: []string{"bug"}})
	if err != nil {
		t.Fatalf("Issues(Label name) failed: %v", err)
	}
	hasIss1 := false
	hasIss5 := false
	for _, issue := range resByName {
		if issue.ObjectID == "iss-1" {
			hasIss1 = true
		}
		if issue.ObjectID == "iss-5" {
			hasIss5 = true
		}
	}
	if !hasIss1 || !hasIss5 {
		t.Errorf("expected both iss-1 and iss-5 to match label 'bug', got %d results", len(resByName))
	}
}

func TestIssuesPriorityEstimatePosition(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	rawDB := db.DB()
	// iss-u: priority 1 (urgent), estimate 2.5, position bV (op-u)
	insertObject(t, rawDB, "iss-u", "issue", 1, "op-u", "A", "a@example.com", 1000, 1000)
	execSQL(t, rawDB, "INSERT INTO issues (object_id, title, description, state, reason, priority, estimate, position, position_op_id) VALUES (?, ?, ?, ?, '', ?, ?, ?, ?)",
		"iss-u", "Urgent", "", "open", 1, 2.5, "bV", "op-u")

	// iss-h1: priority 2 (high), estimate 1.0, position aV (op-h1)
	insertObject(t, rawDB, "iss-h1", "issue", 1, "op-h1", "A", "a@example.com", 1010, 1010)
	execSQL(t, rawDB, "INSERT INTO issues (object_id, title, description, state, reason, priority, estimate, position, position_op_id) VALUES (?, ?, ?, ?, '', ?, ?, ?, ?)",
		"iss-h1", "High 1", "", "open", 2, 1.0, "aV", "op-h1")

	// iss-h2: priority 2 (high), estimate 3.0, position aV (op-h2) — shares identical position with iss-h1
	insertObject(t, rawDB, "iss-h2", "issue", 1, "op-h2", "A", "a@example.com", 1015, 1015)
	execSQL(t, rawDB, "INSERT INTO issues (object_id, title, description, state, reason, priority, estimate, position, position_op_id) VALUES (?, ?, ?, ?, '', ?, ?, ?, ?)",
		"iss-h2", "High 2", "", "open", 2, 3.0, "aV", "op-h2")

	// iss-n: priority 0 (none), estimate NULL, position cV (op-n)
	insertObject(t, rawDB, "iss-n", "issue", 1, "op-n", "A", "a@example.com", 1020, 1020)
	execSQL(t, rawDB, "INSERT INTO issues (object_id, title, description, state, reason, priority, estimate, position, position_op_id) VALUES (?, ?, ?, ?, '', ?, NULL, ?, ?)",
		"iss-n", "None", "", "open", 0, "cV", "op-n")

	// 1. Single lookup Issue(objectID)
	issU, err := db.Issue("iss-u")
	if err != nil {
		t.Fatalf("Issue(iss-u): %v", err)
	}
	if issU.Issue.Priority != 1 {
		t.Errorf("iss-u priority = %d, want 1", issU.Issue.Priority)
	}
	if issU.Issue.Estimate == nil || *issU.Issue.Estimate != 2.5 {
		t.Errorf("iss-u estimate = %v, want 2.5", issU.Issue.Estimate)
	}
	if issU.Issue.Position != "bV" {
		t.Errorf("iss-u position = %q, want 'bV'", issU.Issue.Position)
	}

	issN, err := db.Issue("iss-n")
	if err != nil {
		t.Fatalf("Issue(iss-n): %v", err)
	}
	if issN.Issue.Priority != 0 {
		t.Errorf("iss-n priority = %d, want 0", issN.Issue.Priority)
	}
	if issN.Issue.Estimate != nil {
		t.Errorf("iss-n estimate = %v, want nil", issN.Issue.Estimate)
	}

	// 2. Filter by priority
	filtered, err := db.Issues(projection.IssueFilter{Priority: []int{1, 2}})
	if err != nil {
		t.Fatalf("Issues(Priority: [1,2]): %v", err)
	}
	if len(filtered) != 3 {
		t.Fatalf("expected 3 issues with priority 1 or 2, got %d", len(filtered))
	}

	// 3. OrderByPriorityDesc: urgent (1) > high (2) > none (0)
	pDesc, err := db.Issues(projection.IssueFilter{OrderBy: projection.OrderByPriorityDesc})
	if err != nil {
		t.Fatalf("Issues(OrderByPriorityDesc): %v", err)
	}
	if len(pDesc) != 4 || pDesc[0].ObjectID != "iss-u" || pDesc[1].ObjectID != "iss-h1" || pDesc[2].ObjectID != "iss-h2" || pDesc[3].ObjectID != "iss-n" {
		t.Errorf("OrderByPriorityDesc unexpected order: %v, %v, %v, %v", pDesc[0].ObjectID, pDesc[1].ObjectID, pDesc[2].ObjectID, pDesc[3].ObjectID)
	}

	// 4. OrderByPriorityAsc: none (0) < high (2) < urgent (1)
	pAsc, err := db.Issues(projection.IssueFilter{OrderBy: projection.OrderByPriorityAsc})
	if err != nil {
		t.Fatalf("Issues(OrderByPriorityAsc): %v", err)
	}
	if len(pAsc) != 4 || pAsc[0].ObjectID != "iss-n" || pAsc[1].ObjectID != "iss-h1" || pAsc[2].ObjectID != "iss-h2" || pAsc[3].ObjectID != "iss-u" {
		t.Errorf("OrderByPriorityAsc unexpected order: %v, %v, %v, %v", pAsc[0].ObjectID, pAsc[1].ObjectID, pAsc[2].ObjectID, pAsc[3].ObjectID)
	}

	// 5. OrderByEstimateAsc: 1.0 (iss-h1) < 2.5 (iss-u) < 3.0 (iss-h2), then NULL (iss-n)
	eAsc, err := db.Issues(projection.IssueFilter{OrderBy: projection.OrderByEstimateAsc})
	if err != nil {
		t.Fatalf("Issues(OrderByEstimateAsc): %v", err)
	}
	if len(eAsc) != 4 || eAsc[0].ObjectID != "iss-h1" || eAsc[1].ObjectID != "iss-u" || eAsc[2].ObjectID != "iss-h2" || eAsc[3].ObjectID != "iss-n" {
		t.Errorf("OrderByEstimateAsc unexpected order: %v, %v, %v, %v", eAsc[0].ObjectID, eAsc[1].ObjectID, eAsc[2].ObjectID, eAsc[3].ObjectID)
	}

	// 6. OrderByEstimateDesc: 3.0 (iss-h2) > 2.5 (iss-u) > 1.0 (iss-h1), then NULL (iss-n)
	eDesc, err := db.Issues(projection.IssueFilter{OrderBy: projection.OrderByEstimateDesc})
	if err != nil {
		t.Fatalf("Issues(OrderByEstimateDesc): %v", err)
	}
	if len(eDesc) != 4 || eDesc[0].ObjectID != "iss-h2" || eDesc[1].ObjectID != "iss-u" || eDesc[2].ObjectID != "iss-h1" || eDesc[3].ObjectID != "iss-n" {
		t.Errorf("OrderByEstimateDesc unexpected order: %v, %v, %v, %v", eDesc[0].ObjectID, eDesc[1].ObjectID, eDesc[2].ObjectID, eDesc[3].ObjectID)
	}

	// 7. OrderByPositionAsc: aV (iss-h1, op-h1) < aV (iss-h2, op-h2) < bV (iss-u) < cV (iss-n)
	// Demonstrates position_op_id ASC tiebreaking between issues sharing identical position "aV"
	posAsc, err := db.Issues(projection.IssueFilter{OrderBy: projection.OrderByPositionAsc})
	if err != nil {
		t.Fatalf("Issues(OrderByPositionAsc): %v", err)
	}
	if len(posAsc) != 4 || posAsc[0].ObjectID != "iss-h1" || posAsc[1].ObjectID != "iss-h2" || posAsc[2].ObjectID != "iss-u" || posAsc[3].ObjectID != "iss-n" {
		t.Errorf("OrderByPositionAsc unexpected order: %v, %v, %v, %v", posAsc[0].ObjectID, posAsc[1].ObjectID, posAsc[2].ObjectID, posAsc[3].ObjectID)
	}

	// 8. OrderByPositionDesc: cV (iss-n) > bV (iss-u) > aV (iss-h2, op-h2) > aV (iss-h1, op-h1)
	// Demonstrates position_op_id DESC tiebreaking between issues sharing identical position "aV"
	posDesc, err := db.Issues(projection.IssueFilter{OrderBy: projection.OrderByPositionDesc})
	if err != nil {
		t.Fatalf("Issues(OrderByPositionDesc): %v", err)
	}
	if len(posDesc) != 4 || posDesc[0].ObjectID != "iss-n" || posDesc[1].ObjectID != "iss-u" || posDesc[2].ObjectID != "iss-h2" || posDesc[3].ObjectID != "iss-h1" {
		t.Errorf("OrderByPositionDesc unexpected order: %v, %v, %v, %v", posDesc[0].ObjectID, posDesc[1].ObjectID, posDesc[2].ObjectID, posDesc[3].ObjectID)
	}
}


