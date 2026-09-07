package projection

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/writtendev/writ/engine/resolve"
	"github.com/writtendev/writ/engine/state"
)

// ErrNotFound is returned when an object is not found in the projection.
var ErrNotFound = errors.New("writ: object not found")

// Author holds the author display name and email address derived from an object's operations.
type Author struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// ReviewResult represents a code review object along with its authorship and timestamps.
type ReviewResult struct {
	ObjectID  string       `json:"object_id"`
	Author    Author       `json:"author"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	Review    state.Review `json:"review"`
}

// IssueResult represents an issue object along with its authorship and timestamps.
type IssueResult struct {
	ObjectID  string      `json:"object_id"`
	Author    Author      `json:"author"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	Issue     state.Issue `json:"issue"`
}

// WorkflowStateResult represents a workflow state object along with its authorship and timestamps.
type WorkflowStateResult struct {
	ObjectID      string              `json:"object_id"`
	Author        Author              `json:"author"`
	CreatedAt     time.Time           `json:"created_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
	WorkflowState state.WorkflowState `json:"workflow_state"`
}

// LabelResult represents a label object along with its authorship and timestamps.
type LabelResult struct {
	ObjectID  string      `json:"object_id"`
	Author    Author      `json:"author"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
	Label     state.Label `json:"label"`
}

// SectionResult represents a document section object along with its authorship and timestamps.
type SectionResult struct {
	ObjectID  string        `json:"object_id"`
	Author    Author        `json:"author"`
	CreatedAt time.Time     `json:"created_at"`
	UpdatedAt time.Time     `json:"updated_at"`
	Section   state.Section `json:"section"`
}

// DocumentResult represents a document object along with its authorship, timestamps, and ordered sections.
type DocumentResult struct {
	ObjectID  string          `json:"object_id"`
	Author    Author          `json:"author"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	Document  state.Document  `json:"document"`
	Sections  []SectionResult `json:"sections,omitempty"`
}

// SettingsResult represents the workspace settings along with its object ID and timestamps.
type SettingsResult struct {
	ObjectID  string         `json:"object_id"`
	Settings  state.Settings `json:"settings"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// DocumentFilter specifies filter criteria when querying documents.
type DocumentFilter struct {
	Labels []string
}

// ResolvedPosition describes the resolved anchor position for a comment side.
type ResolvedPosition struct {
	Side      string `json:"side"`
	Outcome   string `json:"outcome"`
	Match     string `json:"match,omitempty"`
	Path      string `json:"path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// CommentResult represents a comment object along with its authorship, timestamps,
// and anchor position resolutions.
//
// Note: The Resolved field represents anchor position resolution (where an anchor
// lands in a git tree), NOT comment thread resolution. Thread resolution is recorded
// on the folded Comment state (Comment.Resolved, Comment.ResolvedBy).
type CommentResult struct {
	ObjectID  string             `json:"object_id"`
	Author    Author             `json:"author"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
	Comment   state.Comment      `json:"comment"`
	Resolved  []ResolvedPosition `json:"resolved,omitempty"`
}

// ObjectResult represents summary metadata for any collaborative object cross-type.
type ObjectResult struct {
	ObjectID   string    `json:"object_id"`
	ObjectType string    `json:"object_type"`
	Author     Author    `json:"author"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	OpCount    int       `json:"op_count"`
	LastOpID   string    `json:"last_op_id"`
}

// Reviews executes a list and filter query over code reviews.
func (d *DB) Reviews(f ReviewFilter) ([]ReviewResult, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}
	if err := d.requireBuiltinShape("review"); err != nil {
		return nil, err
	}

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT r.object_id, COALESCE(r.f_title, ''), COALESCE(r.f_description, ''), COALESCE(r.f_status, ''), COALESCE(r.f_merge_commit, ''), COALESCE(r.f_reason, ''), ")
	sb.WriteString("o.author_name, o.author_email, o.created_at, o.updated_at ")
	sb.WriteString("FROM o_review r JOIN objects o ON o.object_id = r.object_id WHERE 1=1")

	if len(f.Status) > 0 {
		sb.WriteString(" AND r.f_status IN (" + placeholders(len(f.Status)) + ")")
		for _, s := range f.Status {
			args = append(args, s)
		}
	}

	if len(f.Author) > 0 {
		sb.WriteString(" AND (o.author_email IN (" + placeholders(len(f.Author)) + ") OR o.author_name IN (" + placeholders(len(f.Author)) + "))")
		for _, a := range f.Author {
			args = append(args, a)
		}
		for _, a := range f.Author {
			args = append(args, a)
		}
	}

	if len(f.Assignee) > 0 {
		sb.WriteString(" AND EXISTS (SELECT 1 FROM o_review__assignees ra WHERE ra.object_id = r.object_id AND ra.item IN (" + placeholders(len(f.Assignee)) + "))")
		for _, a := range f.Assignee {
			args = append(args, state.NormalizePerson(a))
		}
	}

	if len(f.Label) > 0 {
		// appendLabelFilter's generated EXISTS clause resolves a label
		// filter value against o_label.f_name directly, a second type's
		// column this reader has no other guard for — requireBuiltinShape
		// above only covers "review" (WRIT-189 round 5 MAJOR-2, the same
		// unguarded-cross-type-read class round 3 MAJOR-2 closed for
		// Objects and round 5 closes for Issues' f.State branch).
		if err := d.requireBuiltinShape("label"); err != nil {
			return nil, err
		}
		appendLabelFilter(&sb, &args, "o_review__labels", "rl", "object_id", "r.object_id", f.Label)
	}

	if f.Text != "" {
		sb.WriteString(" AND (r.f_title LIKE ? ESCAPE '\\' OR r.f_description LIKE ? ESCAPE '\\')")
		escaped := "%" + escapeLike(f.Text) + "%"
		args = append(args, escaped, escaped)
	}

	switch f.OrderBy {
	case OrderByCreatedAtAsc:
		sb.WriteString(" ORDER BY o.created_at ASC, r.object_id ASC")
	case OrderByCreatedAtDesc:
		sb.WriteString(" ORDER BY o.created_at DESC, r.object_id DESC")
	case OrderByUpdatedAtAsc:
		sb.WriteString(" ORDER BY o.updated_at ASC, r.object_id ASC")
	case OrderByUpdatedAtDesc:
		sb.WriteString(" ORDER BY o.updated_at DESC, r.object_id DESC")
	case OrderByTitleAsc:
		sb.WriteString(" ORDER BY r.f_title ASC, r.object_id ASC")
	case OrderByTitleDesc:
		sb.WriteString(" ORDER BY r.f_title DESC, r.object_id DESC")
	default:
		sb.WriteString(" ORDER BY o.created_at ASC, r.object_id ASC")
	}

	appendLimitOffset(&sb, &args, f.Limit, f.Offset)

	rows, err := d.db.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query reviews: %w", err)
	}
	defer rows.Close()

	type rawReview struct {
		objectID    string
		title       string
		description string
		status      string
		mergeCommit string
		reason      string
		authorName  string
		authorEmail string
		createdAt   int64
		updatedAt   int64
	}

	var rawReviews []rawReview
	var objectIDs []string

	for rows.Next() {
		var rr rawReview
		if err := rows.Scan(
			&rr.objectID, &rr.title, &rr.description, &rr.status, &rr.mergeCommit, &rr.reason,
			&rr.authorName, &rr.authorEmail, &rr.createdAt, &rr.updatedAt,
		); err != nil {
			return nil, fmt.Errorf("projection: scan review: %w", err)
		}
		rawReviews = append(rawReviews, rr)
		objectIDs = append(objectIDs, rr.objectID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: iterate reviews: %w", err)
	}

	if len(rawReviews) == 0 {
		return []ReviewResult{}, nil
	}

	// Batch load revisions: base and head are two members of one append
	// group (WRIT-189's generic append-envelope shape — ddl.go's
	// appendGroupPlan), sharing a single child table keyed by (object_id,
	// idx) where idx already reflects the originating op's position, one
	// row per revision push. That is the pairing between a push's base and
	// head fixed at write time (writeAppendGroupRows), so a reader just
	// reads rows in idx order — no zipping two independently-ordered
	// per-field lists back together by position, which is what silently
	// mispaired or dropped a revision whenever one push wrote only one of
	// the two fields (round 2 MAJOR-1).
	revisionsMap := make(map[string][]state.Revision)
	revRows, err := d.queryIn("SELECT object_id, COALESCE(f_base, ''), COALESCE(f_head, '') FROM o_review__base_head WHERE object_id IN (?) ORDER BY object_id ASC, idx ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query review revisions: %w", err)
	}
	for revRows.Next() {
		var objID, base, head string
		if err := revRows.Scan(&objID, &base, &head); err != nil {
			revRows.Close()
			return nil, fmt.Errorf("projection: scan review revision: %w", err)
		}
		revisionsMap[objID] = append(revisionsMap[objID], state.Revision{Base: base, Head: head})
	}
	revRows.Close()

	// Batch load assignees
	assigneesMap := make(map[string][]string)
	asRows, err := d.queryIn("SELECT object_id, item FROM o_review__assignees WHERE object_id IN (?) ORDER BY object_id ASC, item ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query review assignees: %w", err)
	}
	for asRows.Next() {
		var objID, assignee string
		if err := asRows.Scan(&objID, &assignee); err != nil {
			asRows.Close()
			return nil, fmt.Errorf("projection: scan review assignee: %w", err)
		}
		assigneesMap[objID] = append(assigneesMap[objID], assignee)
	}
	asRows.Close()

	// Batch load labels
	labelsMap := make(map[string][]string)
	lblRows, err := d.queryIn("SELECT object_id, item FROM o_review__labels WHERE object_id IN (?) ORDER BY object_id ASC, item ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query review labels: %w", err)
	}
	for lblRows.Next() {
		var objID, label string
		if err := lblRows.Scan(&objID, &label); err != nil {
			lblRows.Close()
			return nil, fmt.Errorf("projection: scan review label: %w", err)
		}
		labelsMap[objID] = append(labelsMap[objID], label)
	}
	lblRows.Close()

	// Batch load links
	linksMap := make(map[string][]state.Link)
	lnkRows, err := d.queryIn("SELECT object_id, k_target, COALESCE(f_target_type, ''), COALESCE(f_relation, '') FROM o_review__k_target WHERE object_id IN (?) AND COALESCE(f_relation, '') NOT IN ('', 'none') ORDER BY object_id ASC, k_target ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query review links: %w", err)
	}
	for lnkRows.Next() {
		var objID, target, targetType, relation string
		if err := lnkRows.Scan(&objID, &target, &targetType, &relation); err != nil {
			lnkRows.Close()
			return nil, fmt.Errorf("projection: scan review link: %w", err)
		}
		linksMap[objID] = append(linksMap[objID], state.Link{
			Target:     target,
			TargetType: targetType,
			Relation:   relation,
		})
	}
	lnkRows.Close()

	// Batch load approvals
	approvalsMap := make(map[string][]state.Approval)
	appRows, err := d.queryIn("SELECT object_id, k_subject, k_revision, COALESCE(f_verdict, ''), COALESCE(f_message, '') FROM o_review__k_subject_revision WHERE object_id IN (?) AND COALESCE(f_verdict, '') NOT IN ('', 'none') ORDER BY object_id ASC, k_subject ASC, k_revision ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query approvals: %w", err)
	}
	for appRows.Next() {
		var objID, subject, revision, verdict, message string
		if err := appRows.Scan(&objID, &subject, &revision, &verdict, &message); err != nil {
			appRows.Close()
			return nil, fmt.Errorf("projection: scan approval: %w", err)
		}
		approvalsMap[objID] = append(approvalsMap[objID], state.Approval{
			Subject:  subject,
			Revision: revision,
			Verdict:  verdict,
			Message:  message,
		})
	}
	appRows.Close()

	// Batch load ci_statuses
	ciMap := make(map[string][]state.CIStatus)
	ciRows, err := d.queryIn("SELECT object_id, k_revision, k_name, COALESCE(f_state, ''), COALESCE(f_url, ''), COALESCE(f_ci_description, ''), COALESCE(f_started_at, ''), COALESCE(f_completed_at, ''), COALESCE(f_external_id, '') FROM o_review__k_revision_name WHERE object_id IN (?) ORDER BY object_id ASC, k_revision ASC, k_name ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query ci_statuses: %w", err)
	}
	for ciRows.Next() {
		var objID, revision, name, stateVal, url, description, startedAt, completedAt, externalID string
		if err := ciRows.Scan(&objID, &revision, &name, &stateVal, &url, &description, &startedAt, &completedAt, &externalID); err != nil {
			ciRows.Close()
			return nil, fmt.Errorf("projection: scan ci_status: %w", err)
		}
		ciMap[objID] = append(ciMap[objID], state.CIStatus{
			Revision:    revision,
			Name:        name,
			State:       stateVal,
			URL:         url,
			Description: description,
			StartedAt:   startedAt,
			CompletedAt: completedAt,
			ExternalID:  externalID,
		})
	}
	ciRows.Close()

	// Batch load unknown_ops
	unknownMap, err := d.loadUnknownOps(objectIDs)
	if err != nil {
		return nil, err
	}

	results := make([]ReviewResult, 0, len(rawReviews))
	for _, rr := range rawReviews {
		review := state.Review{
			Title:       rr.title,
			Description: rr.description,
			Status:      rr.status,
			MergeCommit: rr.mergeCommit,
			Reason:      rr.reason,
			Assignees:   assigneesMap[rr.objectID],
			Labels:      labelsMap[rr.objectID],
			Links:       linksMap[rr.objectID],
			Revisions:   revisionsMap[rr.objectID],
			Approvals:   approvalsMap[rr.objectID],
			CIStatuses:  ciMap[rr.objectID],
			UnknownOps:  unknownMap[rr.objectID],
		}
		results = append(results, ReviewResult{
			ObjectID:  rr.objectID,
			Author:    Author{Name: rr.authorName, Email: rr.authorEmail},
			CreatedAt: time.Unix(rr.createdAt, 0).UTC(),
			UpdatedAt: time.Unix(rr.updatedAt, 0).UTC(),
			Review:    review,
		})
	}

	return results, nil
}

// loadUnknownOps batch-loads unknown_ops rows for a set of object IDs — the
// one substrate table that never changes shape under this ticket.
func (d *DB) loadUnknownOps(objectIDs []string) (map[string][]state.UnknownOp, error) {
	unknownMap := make(map[string][]state.UnknownOp)
	if len(objectIDs) == 0 {
		return unknownMap, nil
	}
	uRows, err := d.queryIn("SELECT object_id, op_id, object_type, op_type, op_version FROM unknown_ops WHERE object_id IN (?) ORDER BY object_id ASC, op_index ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query unknown_ops: %w", err)
	}
	defer uRows.Close()
	for uRows.Next() {
		var objID, opID, objType, opType string
		var opVersion int64
		if err := uRows.Scan(&objID, &opID, &objType, &opType, &opVersion); err != nil {
			return nil, fmt.Errorf("projection: scan unknown op: %w", err)
		}
		unknownMap[objID] = append(unknownMap[objID], state.UnknownOp{
			Commit:     opID,
			ObjectType: objType,
			OpType:     opType,
			OpVersion:  opVersion,
		})
	}
	return unknownMap, uRows.Err()
}

func (d *DB) unknownOpsFor(objectID string) ([]state.UnknownOp, error) {
	rows, err := d.db.Query("SELECT op_id, object_type, op_type, op_version FROM unknown_ops WHERE object_id = ? ORDER BY op_index ASC", objectID)
	if err != nil {
		return nil, fmt.Errorf("projection: query unknown_ops: %w", err)
	}
	defer rows.Close()
	var out []state.UnknownOp
	for rows.Next() {
		var u state.UnknownOp
		if err := rows.Scan(&u.Commit, &u.ObjectType, &u.OpType, &u.OpVersion); err != nil {
			return nil, fmt.Errorf("projection: scan unknown op: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Issues executes a list and filter query over issues.
func (d *DB) Issues(f IssueFilter) ([]IssueResult, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}
	if err := d.requireBuiltinShape("issue"); err != nil {
		return nil, err
	}

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT i.object_id, COALESCE(i.f_title, ''), COALESCE(i.f_description, ''), COALESCE(i.f_state, ''), COALESCE(i.f_reason, ''), COALESCE(i.f_priority, 0), i.f_estimate, COALESCE(i.f_position, ''), ")
	sb.WriteString("o.author_name, o.author_email, o.created_at, o.updated_at ")
	sb.WriteString("FROM o_issue i JOIN objects o ON o.object_id = i.object_id WHERE 1=1")

	if len(f.State) > 0 {
		// This branch's EXISTS clause below reads o_workflow_state.f_name
		// and .f_type directly, not through generated SQL derived from
		// "issue"'s own shape — requireBuiltinShape("issue") above says
		// nothing about whether "workflow-state" still has its built-in
		// columns. A log schema redeclaring workflow-state alone (issue
		// left untouched) reshapes o_workflow_state out from under this
		// literal and turns a state-filtered Issues call into a raw SQLite
		// "no such column: ws.f_name" instead of the named error this guard
		// exists to produce (WRIT-189 round 5 MAJOR-2, the same class round
		// 3 MAJOR-2 closed for Objects, since made descriptor-driven
		// instead of guarded — WRIT-192).
		if err := d.requireBuiltinShape("workflow-state"); err != nil {
			return nil, err
		}
		sb.WriteString(" AND (")
		for idx, s := range f.State {
			if idx > 0 {
				sb.WriteString(" OR ")
			}
			sb.WriteString("(COALESCE(i.f_state, '') = ? OR EXISTS (SELECT 1 FROM o_workflow_state ws WHERE ws.object_id = i.f_state AND (LOWER(ws.f_name) = LOWER(?) OR LOWER(ws.f_type) = LOWER(?) OR (LOWER(?) = 'closed' AND ws.f_type = 'completed') OR (LOWER(?) = 'open' AND ws.f_type IN ('unstarted', 'backlog')))))")
			args = append(args, s, s, s, s, s)
		}
		sb.WriteString(")")
	}

	if len(f.Priority) > 0 {
		sb.WriteString(" AND COALESCE(i.f_priority, 0) IN (" + placeholders(len(f.Priority)) + ")")
		for _, p := range f.Priority {
			args = append(args, p)
		}
	}

	if len(f.Author) > 0 {
		sb.WriteString(" AND (o.author_email IN (" + placeholders(len(f.Author)) + ") OR o.author_name IN (" + placeholders(len(f.Author)) + "))")
		for _, a := range f.Author {
			args = append(args, a)
		}
		for _, a := range f.Author {
			args = append(args, a)
		}
	}

	if len(f.Assignee) > 0 {
		sb.WriteString(" AND EXISTS (SELECT 1 FROM o_issue__assignees ia WHERE ia.object_id = i.object_id AND ia.item IN (" + placeholders(len(f.Assignee)) + "))")
		for _, a := range f.Assignee {
			args = append(args, state.NormalizePerson(a))
		}
	}

	if len(f.Label) > 0 {
		// Same cross-type read as Reviews' identical guard above: this
		// filter's EXISTS clause resolves against o_label.f_name, which
		// requireBuiltinShape("issue") above says nothing about (WRIT-189
		// round 5 MAJOR-2).
		if err := d.requireBuiltinShape("label"); err != nil {
			return nil, err
		}
		appendLabelFilter(&sb, &args, "o_issue__labels", "il", "object_id", "i.object_id", f.Label)
	}

	if f.Text != "" {
		sb.WriteString(" AND (i.f_title LIKE ? ESCAPE '\\' OR i.f_description LIKE ? ESCAPE '\\')")
		escaped := "%" + escapeLike(f.Text) + "%"
		args = append(args, escaped, escaped)
	}

	switch f.OrderBy {
	case OrderByCreatedAtAsc:
		sb.WriteString(" ORDER BY o.created_at ASC, i.object_id ASC")
	case OrderByCreatedAtDesc:
		sb.WriteString(" ORDER BY o.created_at DESC, i.object_id DESC")
	case OrderByUpdatedAtAsc:
		sb.WriteString(" ORDER BY o.updated_at ASC, i.object_id ASC")
	case OrderByUpdatedAtDesc:
		sb.WriteString(" ORDER BY o.updated_at DESC, i.object_id DESC")
	case OrderByTitleAsc:
		sb.WriteString(" ORDER BY i.f_title ASC, i.object_id ASC")
	case OrderByTitleDesc:
		sb.WriteString(" ORDER BY i.f_title DESC, i.object_id DESC")
	case OrderByPriorityAsc:
		sb.WriteString(" ORDER BY CASE WHEN COALESCE(i.f_priority, 0) = 0 THEN 0 ELSE 5 - COALESCE(i.f_priority, 0) END ASC, COALESCE(i.f_position, '') ASC, o.created_at ASC, i.object_id ASC")
	case OrderByPriorityDesc:
		sb.WriteString(" ORDER BY CASE WHEN COALESCE(i.f_priority, 0) = 0 THEN 5 ELSE COALESCE(i.f_priority, 0) END ASC, COALESCE(i.f_position, '') ASC, o.created_at ASC, i.object_id ASC")
	case OrderByPositionAsc:
		sb.WriteString(" ORDER BY COALESCE(i.f_position, '') ASC, COALESCE(i.f_position__op_id, '') ASC, o.created_at ASC, i.object_id ASC")
	case OrderByPositionDesc:
		sb.WriteString(" ORDER BY COALESCE(i.f_position, '') DESC, COALESCE(i.f_position__op_id, '') DESC, o.created_at DESC, i.object_id DESC")
	case OrderByEstimateAsc:
		sb.WriteString(" ORDER BY CASE WHEN i.f_estimate IS NULL THEN 1 ELSE 0 END, i.f_estimate ASC, COALESCE(i.f_position, '') ASC, o.created_at ASC, i.object_id ASC")
	case OrderByEstimateDesc:
		sb.WriteString(" ORDER BY CASE WHEN i.f_estimate IS NULL THEN 1 ELSE 0 END, i.f_estimate DESC, COALESCE(i.f_position, '') ASC, o.created_at ASC, i.object_id ASC")
	default:
		sb.WriteString(" ORDER BY o.created_at ASC, i.object_id ASC")
	}

	appendLimitOffset(&sb, &args, f.Limit, f.Offset)

	rows, err := d.db.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query issues: %w", err)
	}
	defer rows.Close()

	type rawIssue struct {
		objectID    string
		title       string
		description string
		state       string
		reason      string
		priority    int
		estimate    sql.NullFloat64
		position    string
		authorName  string
		authorEmail string
		createdAt   int64
		updatedAt   int64
	}

	var rawIssues []rawIssue
	var objectIDs []string

	for rows.Next() {
		var ri rawIssue
		if err := rows.Scan(
			&ri.objectID, &ri.title, &ri.description, &ri.state, &ri.reason,
			&ri.priority, &ri.estimate, &ri.position,
			&ri.authorName, &ri.authorEmail, &ri.createdAt, &ri.updatedAt,
		); err != nil {
			return nil, fmt.Errorf("projection: scan issue: %w", err)
		}
		rawIssues = append(rawIssues, ri)
		objectIDs = append(objectIDs, ri.objectID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: iterate issues: %w", err)
	}

	if len(rawIssues) == 0 {
		return []IssueResult{}, nil
	}

	// Batch load assignees
	assigneesMap := make(map[string][]string)
	asRows, err := d.queryIn("SELECT object_id, item FROM o_issue__assignees WHERE object_id IN (?) ORDER BY object_id ASC, item ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query issue assignees: %w", err)
	}
	for asRows.Next() {
		var objID, assignee string
		if err := asRows.Scan(&objID, &assignee); err != nil {
			asRows.Close()
			return nil, fmt.Errorf("projection: scan issue assignee: %w", err)
		}
		assigneesMap[objID] = append(assigneesMap[objID], assignee)
	}
	asRows.Close()

	// Batch load labels
	labelsMap := make(map[string][]string)
	lblRows, err := d.queryIn("SELECT object_id, item FROM o_issue__labels WHERE object_id IN (?) ORDER BY object_id ASC, item ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query issue labels: %w", err)
	}
	for lblRows.Next() {
		var objID, label string
		if err := lblRows.Scan(&objID, &label); err != nil {
			lblRows.Close()
			return nil, fmt.Errorf("projection: scan issue label: %w", err)
		}
		labelsMap[objID] = append(labelsMap[objID], label)
	}
	lblRows.Close()

	// Batch load links
	linksMap := make(map[string][]state.Link)
	lnkRows, err := d.queryIn("SELECT object_id, k_target, COALESCE(f_target_type, ''), COALESCE(f_relation, '') FROM o_issue__k_target WHERE object_id IN (?) AND COALESCE(f_relation, '') NOT IN ('', 'none') ORDER BY object_id ASC, k_target ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query issue links: %w", err)
	}
	for lnkRows.Next() {
		var objID, target, targetType, relation string
		if err := lnkRows.Scan(&objID, &target, &targetType, &relation); err != nil {
			lnkRows.Close()
			return nil, fmt.Errorf("projection: scan issue link: %w", err)
		}
		linksMap[objID] = append(linksMap[objID], state.Link{
			Target:     target,
			TargetType: targetType,
			Relation:   relation,
		})
	}
	lnkRows.Close()

	unknownMap, err := d.loadUnknownOps(objectIDs)
	if err != nil {
		return nil, err
	}

	results := make([]IssueResult, 0, len(rawIssues))
	for _, ri := range rawIssues {
		var est *float64
		if ri.estimate.Valid {
			est = &ri.estimate.Float64
		}
		issue := state.Issue{
			Title:       ri.title,
			Description: ri.description,
			State:       ri.state,
			Reason:      ri.reason,
			Priority:    ri.priority,
			Estimate:    est,
			Position:    ri.position,
			Assignees:   assigneesMap[ri.objectID],
			Labels:      labelsMap[ri.objectID],
			Links:       linksMap[ri.objectID],
			UnknownOps:  unknownMap[ri.objectID],
		}
		results = append(results, IssueResult{
			ObjectID:  ri.objectID,
			Author:    Author{Name: ri.authorName, Email: ri.authorEmail},
			CreatedAt: time.Unix(ri.createdAt, 0).UTC(),
			UpdatedAt: time.Unix(ri.updatedAt, 0).UTC(),
			Issue:     issue,
		})
	}

	return results, nil
}

// commentSubjectFilterSQL builds the SQL fragment matching a comment's
// json_extract'd subject: f_subject holds create-once's raw, verbatim JSON
// bytes for the untyped `subject` target, so a subject filter must go
// through json_extract, never whole-blob equality — create-once preserves
// unknown members and key order, and CommentFilter filters subject_type and
// subject_id independently.
func commentSubjectFilterSQL(alias string) (subjectType, subjectID string) {
	return "json_extract(" + alias + ".f_subject, '$.object_type')", "json_extract(" + alias + ".f_subject, '$.object_id')"
}

// Comments executes a list and filter query over comments.
func (d *DB) Comments(f CommentFilter) ([]CommentResult, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}
	if err := d.requireBuiltinShape("comment"); err != nil {
		return nil, err
	}

	subjectTypeExpr, subjectIDExpr := commentSubjectFilterSQL("c")

	var args []any

	// Filtering goes through the generic members table (indexed on member,
	// value), joined in rather than checked via a correlated EXISTS: an
	// EXISTS subquery lets the planner pick o_comment as the driving table
	// (a full 100k-row scan probing the index once per row), where a JOIN
	// lets it drive from whichever member row the (member, value) index
	// narrows down first and reach o_comment through its primary key — at
	// 100k-comment scale that is the difference this ticket's
	// BenchmarkThreadsAssembly gate exists to catch. The SELECT list still
	// reads f_subject directly via json_extract; that cost is paid only for
	// the rows already narrowed down by the joins below.
	// SubjectID is joined first when both are present: in practice it is the
	// selective half (one object's comments among the whole table), while
	// SubjectType (e.g. every comment on any review) routinely is not.
	// SQLite has no statistics on this generic, schema-agnostic table (no
	// ANALYZE run over it) to infer that itself, so join order is this
	// query's only lever — and it is exactly the lever
	// BenchmarkThreadsAssembly exists to hold accountable.
	var joins []string
	if f.SubjectID != "" {
		joins = append(joins, "JOIN o_comment__subject__members si ON si.object_id = c.object_id AND si.member = 'object_id' AND si.value = ?")
		args = append(args, f.SubjectID)
	}
	if f.SubjectType != "" {
		joins = append(joins, "JOIN o_comment__subject__members st ON st.object_id = c.object_id AND st.member = 'object_type' AND st.value = ?")
		args = append(args, f.SubjectType)
	}

	var sb strings.Builder
	sb.WriteString("SELECT c.object_id, COALESCE(" + subjectTypeExpr + ", ''), COALESCE(" + subjectIDExpr + ", ''), COALESCE(c.f_text, ''), COALESCE(c.f_in_reply_to, ''), COALESCE(c.f_anchor, ''), COALESCE(c.f_deleted, 0), c.f_resolved, COALESCE(c.f_resolved_by, ''), ")
	sb.WriteString("o.author_name, o.author_email, o.created_at, o.updated_at ")
	sb.WriteString("FROM o_comment c ")
	for _, j := range joins {
		sb.WriteString(j)
		sb.WriteString(" ")
	}
	sb.WriteString("JOIN objects o ON o.object_id = c.object_id WHERE 1=1")

	if f.Resolved != nil {
		if *f.Resolved {
			sb.WriteString(" AND c.f_resolved = 1")
		} else {
			sb.WriteString(" AND (c.f_resolved = 0 OR c.f_resolved IS NULL)")
		}
	}

	if len(f.Author) > 0 {
		sb.WriteString(" AND (o.author_email IN (" + placeholders(len(f.Author)) + ") OR o.author_name IN (" + placeholders(len(f.Author)) + "))")
		for _, a := range f.Author {
			args = append(args, a)
		}
		for _, a := range f.Author {
			args = append(args, a)
		}
	}

	if f.Text != "" {
		sb.WriteString(" AND c.f_text LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(f.Text)+"%")
	}

	if !f.IncludeDeleted {
		sb.WriteString(" AND (c.f_deleted = 0 OR c.f_deleted IS NULL)")
	}

	switch f.OrderBy {
	case OrderByCreatedAtAsc:
		sb.WriteString(" ORDER BY o.created_at ASC, c.object_id ASC")
	case OrderByCreatedAtDesc:
		sb.WriteString(" ORDER BY o.created_at DESC, c.object_id DESC")
	case OrderByUpdatedAtAsc:
		sb.WriteString(" ORDER BY o.updated_at ASC, c.object_id ASC")
	case OrderByUpdatedAtDesc:
		sb.WriteString(" ORDER BY o.updated_at DESC, c.object_id DESC")
	default:
		sb.WriteString(" ORDER BY o.created_at ASC, c.object_id ASC")
	}

	appendLimitOffset(&sb, &args, f.Limit, f.Offset)

	rows, err := d.db.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query comments: %w", err)
	}
	defer rows.Close()

	type rawComment struct {
		objectID    string
		subjectType string
		subjectID   string
		text        string
		inReplyTo   string
		anchor      string
		deleted     int
		resolved    sql.NullInt64
		resolvedBy  string
		authorName  string
		authorEmail string
		createdAt   int64
		updatedAt   int64
	}

	var rawComments []rawComment
	var objectIDs []string

	for rows.Next() {
		var rc rawComment
		if err := rows.Scan(
			&rc.objectID, &rc.subjectType, &rc.subjectID, &rc.text, &rc.inReplyTo, &rc.anchor, &rc.deleted, &rc.resolved, &rc.resolvedBy,
			&rc.authorName, &rc.authorEmail, &rc.createdAt, &rc.updatedAt,
		); err != nil {
			return nil, fmt.Errorf("projection: scan comment: %w", err)
		}
		rawComments = append(rawComments, rc)
		objectIDs = append(objectIDs, rc.objectID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: iterate comments: %w", err)
	}

	if len(rawComments) == 0 {
		return []CommentResult{}, nil
	}

	unknownMap, err := d.loadUnknownOps(objectIDs)
	if err != nil {
		return nil, err
	}

	// Determine target commit for resolutions:
	targetCommit := f.TargetCommit
	if targetCommit == "" {
		// Default to the sole code_tips entry if exactly one exists
		tipRows, err := d.db.Query("SELECT DISTINCT tip FROM code_tips WHERE tip != ''")
		if err == nil {
			var tips []string
			for tipRows.Next() {
				var tip string
				if err := tipRows.Scan(&tip); err == nil {
					tips = append(tips, tip)
				}
			}
			tipRows.Close()
			if len(tips) == 1 {
				targetCommit = tips[0]
			}
		}
	}

	// Batch load resolutions
	resolutionsMap := make(map[string][]ResolvedPosition)
	var resQuery string
	var extraArgs []any
	if targetCommit != "" {
		resQuery = "SELECT object_id, side, outcome, match, path, start_line, end_line, reason FROM anchor_resolutions WHERE object_id IN (?) AND target_commit = ? ORDER BY object_id ASC, side ASC"
		extraArgs = []any{targetCommit}
	} else {
		resQuery = "SELECT object_id, side, outcome, match, path, start_line, end_line, reason FROM anchor_resolutions WHERE object_id IN (?) ORDER BY object_id ASC, side ASC"
	}

	resRows, err := d.queryIn(resQuery, objectIDs, extraArgs...)
	if err != nil {
		return nil, fmt.Errorf("projection: query anchor resolutions: %w", err)
	}
	for resRows.Next() {
		var objID, side, outcome, match, path, reason string
		var startLine, endLine int
		if err := resRows.Scan(&objID, &side, &outcome, &match, &path, &startLine, &endLine, &reason); err != nil {
			resRows.Close()
			return nil, fmt.Errorf("projection: scan anchor resolution: %w", err)
		}
		resolutionsMap[objID] = append(resolutionsMap[objID], ResolvedPosition{
			Side:      side,
			Outcome:   outcome,
			Match:     match,
			Path:      path,
			StartLine: startLine,
			EndLine:   endLine,
			Reason:    reason,
		})
	}
	resRows.Close()

	results := make([]CommentResult, 0, len(rawComments))
	for _, rc := range rawComments {
		var anchor *resolve.Anchor
		if rc.anchor != "" && rc.anchor != "null" {
			a, err := resolve.ParseAnchor([]byte(rc.anchor))
			if err == nil {
				anchor = &a
			}
		}

		var resolvedPtr *bool
		if rc.resolved.Valid {
			val := rc.resolved.Int64 == 1
			resolvedPtr = &val
		}

		comment := state.Comment{
			Subject: state.CommentSubject{
				ObjectType: rc.subjectType,
				ObjectID:   rc.subjectID,
			},
			Text:       rc.text,
			InReplyTo:  rc.inReplyTo,
			Anchor:     anchor,
			Deleted:    rc.deleted == 1,
			Resolved:   resolvedPtr,
			ResolvedBy: rc.resolvedBy,
			UnknownOps: unknownMap[rc.objectID],
		}

		results = append(results, CommentResult{
			ObjectID:  rc.objectID,
			Author:    Author{Name: rc.authorName, Email: rc.authorEmail},
			CreatedAt: time.Unix(rc.createdAt, 0).UTC(),
			UpdatedAt: time.Unix(rc.updatedAt, 0).UTC(),
			Comment:   comment,
			Resolved:  resolutionsMap[rc.objectID],
		})
	}

	return results, nil
}

// objectTextColumns returns shape's generated table name and the sorted
// "f_"-prefixed columns of its scalar targets whose declared value_type is
// "string" or "text" — every column Objects' full-text search below can
// usefully LIKE against. Only lww/create-once/lattice/tombstone targets
// carry a Column at all (a collection or keyed-lww target materializes into
// a child table instead), so shape.Targets already holds exactly the scalar
// ones (objectQueryShapeFromType, ddl.go).
func objectTextColumns(shape objectQueryShape) (table string, columns []string) {
	for _, target := range shape.Targets {
		if target.ValueType != "string" && target.ValueType != "text" {
			continue
		}
		columns = append(columns, target.Column)
	}
	sort.Strings(columns)
	return shape.Table, columns
}

// objectsTextClause builds Objects' f.Text filter directly from the
// installed schema descriptor: one EXISTS per declared type (restricted to
// restrictTypes when non-empty, the types f.Type itself already narrows the
// query to) over that type's own string/text scalar columns, replacing the
// five built-in table/column literals WRIT-189 round 3 MAJOR-2 hard-coded
// here (WRIT-192). A type with no string/text scalar column at all — every
// declared type, immediately after ApplySchema installs an empty
// descriptor, or a type whose only text fields are collection- or
// keyed-lww-valued — contributes no clause; Objects falls through to
// "found nothing" for f.Text rather than referencing a table it has no
// column to search.
//
// Reads desc.queryShapes/queryOrder, not desc.types/order: those two stay
// nil on a name-only reopen (requireMaterializationPlan's guard depends on
// it — see schemaDescriptor's doc comment in ddl.go), but queryShapes is
// rehydrated from meta in exactly that state (descriptorFromPersisted), so
// this clause is correct whether desc came from a live buildDescriptor call
// or a warm reopen that has not run ApplySchema in this process yet
// (WRIT-192 round 2 MAJOR-1).
func objectsTextClause(desc *schemaDescriptor, restrictTypes []string) (clause string, params int) {
	if desc == nil {
		return "", 0
	}

	var allow map[string]bool
	if len(restrictTypes) > 0 {
		allow = make(map[string]bool, len(restrictTypes))
		for _, t := range restrictTypes {
			allow[t] = true
		}
	}

	var parts []string
	for _, objectType := range desc.queryOrder {
		if allow != nil && !allow[objectType] {
			continue
		}
		table, columns := objectTextColumns(desc.queryShapes[objectType])
		if len(columns) == 0 {
			continue
		}

		var colParts []string
		for _, col := range columns {
			colParts = append(colParts, "x."+col+" LIKE ? ESCAPE '\\'")
		}
		parts = append(parts, "EXISTS (SELECT 1 FROM "+table+" x WHERE x.object_id = o.object_id AND ("+strings.Join(colParts, " OR ")+"))")
		params += len(columns)
	}
	if len(parts) == 0 {
		return "", 0
	}
	return "(" + strings.Join(parts, " OR ") + ")", params
}

// objectsNotDeletedClause builds Objects' default !IncludeDeleted filter
// directly from the installed schema descriptor: "no tombstone-strategy
// target folded true", over every declared type that has one (restricted to
// restrictTypes when non-empty, the same restriction objectsTextClause
// applies — the types f.Type itself already narrows the query to, so a
// clause for any other type would be dead weight), replacing the single
// built-in literal (o_comment.f_deleted) WRIT-189 round 3 MAJOR-2
// hard-coded here (WRIT-192). A type with more than one tombstone-strategy
// target — none exist in the shipped vocabulary, but the schema DSL does
// not forbid it — is excluded when any one of its tombstone targets folded
// true (the generated clause is an AND of "not deleted" per column, so a
// single deleted-true column fails it).
//
// Reads desc.queryShapes/queryOrder for the same reason objectsTextClause
// does — see its doc comment (WRIT-192 round 2 MAJOR-1).
func objectsNotDeletedClause(desc *schemaDescriptor, restrictTypes []string) string {
	if desc == nil {
		return ""
	}

	var allow map[string]bool
	if len(restrictTypes) > 0 {
		allow = make(map[string]bool, len(restrictTypes))
		for _, t := range restrictTypes {
			allow[t] = true
		}
	}

	var parts []string
	for _, objectType := range desc.queryOrder {
		if allow != nil && !allow[objectType] {
			continue
		}
		shape := desc.queryShapes[objectType]
		var cols []string
		for _, target := range shape.Targets {
			if target.Strategy == "tombstone" {
				cols = append(cols, target.Column)
			}
		}
		if len(cols) == 0 {
			continue
		}
		sort.Strings(cols)

		var notDeleted []string
		for _, col := range cols {
			notDeleted = append(notDeleted, "(x."+col+" = 0 OR x."+col+" IS NULL)")
		}
		parts = append(parts, "(o.object_type != '"+objectType+"' OR EXISTS (SELECT 1 FROM "+shape.Table+" x WHERE x.object_id = o.object_id AND "+strings.Join(notDeleted, " AND ")+"))")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " AND ")
}

// Objects executes a cross-type summary query over collaborative objects.
func (d *DB) Objects(f ObjectFilter) ([]ObjectResult, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}

	desc := d.descriptor()

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT o.object_id, o.object_type, o.op_count, o.last_op_id, ")
	sb.WriteString("o.author_name, o.author_email, o.created_at, o.updated_at ")
	sb.WriteString("FROM objects o WHERE 1=1")

	if len(f.Type) > 0 {
		sb.WriteString(" AND o.object_type IN (" + placeholders(len(f.Type)) + ")")
		for _, t := range f.Type {
			args = append(args, t)
		}
	}

	if len(f.Author) > 0 {
		sb.WriteString(" AND (o.author_email IN (" + placeholders(len(f.Author)) + ") OR o.author_name IN (" + placeholders(len(f.Author)) + "))")
		for _, a := range f.Author {
			args = append(args, a)
		}
		for _, a := range f.Author {
			args = append(args, a)
		}
	}

	if f.Text != "" {
		clause, params := objectsTextClause(desc, f.Type)
		if clause != "" {
			escaped := "%" + escapeLike(f.Text) + "%"
			sb.WriteString(" AND " + clause)
			for i := 0; i < params; i++ {
				args = append(args, escaped)
			}
		} else {
			// No declared type has a string/text scalar column to search
			// (restricted to f.Type, when set): nothing can match.
			sb.WriteString(" AND 0")
		}
	}

	if !f.IncludeDeleted {
		if clause := objectsNotDeletedClause(desc, f.Type); clause != "" {
			sb.WriteString(" AND " + clause)
		}
	}

	switch f.OrderBy {
	case OrderByCreatedAtAsc:
		sb.WriteString(" ORDER BY o.created_at ASC, o.object_id ASC")
	case OrderByCreatedAtDesc:
		sb.WriteString(" ORDER BY o.created_at DESC, o.object_id DESC")
	case OrderByUpdatedAtAsc:
		sb.WriteString(" ORDER BY o.updated_at ASC, o.object_id ASC")
	case OrderByUpdatedAtDesc:
		sb.WriteString(" ORDER BY o.updated_at DESC, o.object_id DESC")
	default:
		sb.WriteString(" ORDER BY o.created_at ASC, o.object_id ASC")
	}

	appendLimitOffset(&sb, &args, f.Limit, f.Offset)

	rows, err := d.db.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query objects: %w", err)
	}
	defer rows.Close()

	var results []ObjectResult
	for rows.Next() {
		var or ObjectResult
		var createdAt, updatedAt int64
		if err := rows.Scan(
			&or.ObjectID, &or.ObjectType, &or.OpCount, &or.LastOpID,
			&or.Author.Name, &or.Author.Email, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("projection: scan object: %w", err)
		}
		or.CreatedAt = time.Unix(createdAt, 0).UTC()
		or.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		results = append(results, or)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: iterate objects: %w", err)
	}

	if results == nil {
		return []ObjectResult{}, nil
	}

	return results, nil
}

// Object fetches summary metadata for a single collaborative object by its ID, returning ErrNotFound if not found.
func (d *DB) Object(objectID string) (ObjectResult, error) {
	if d == nil || d.db == nil {
		return ObjectResult{}, fmt.Errorf("projection: database is closed")
	}
	if objectID == "" {
		return ObjectResult{}, ErrNotFound
	}

	var (
		res          ObjectResult
		createdAtSec int64
		updatedAtSec int64
	)

	err := d.db.QueryRow(`
		SELECT object_id, object_type, op_count, last_op_id, author_name, author_email, created_at, updated_at
		FROM objects
		WHERE object_id = ?
	`, objectID).Scan(
		&res.ObjectID,
		&res.ObjectType,
		&res.OpCount,
		&res.LastOpID,
		&res.Author.Name,
		&res.Author.Email,
		&createdAtSec,
		&updatedAtSec,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return ObjectResult{}, ErrNotFound
		}
		return ObjectResult{}, fmt.Errorf("projection: query object %s: %w", objectID, err)
	}

	res.CreatedAt = time.Unix(createdAtSec, 0).UTC()
	res.UpdatedAt = time.Unix(updatedAtSec, 0).UTC()

	return res, nil
}

// Threads retrieves and structures all comments attached to a subject into a comment reply forest.
func (d *DB) Threads(subjectType, subjectID string) ([]state.CommentThread, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}

	comments, err := d.Comments(CommentFilter{
		SubjectType:    subjectType,
		SubjectID:      subjectID,
		IncludeDeleted: true,
		OrderBy:        OrderByCreatedAtAsc,
	})
	if err != nil {
		return nil, fmt.Errorf("projection: threads query comments: %w", err)
	}

	if len(comments) == 0 {
		return nil, nil
	}

	type commentNode struct {
		res       CommentResult
		origIndex int
	}

	nodeMap := make(map[string]commentNode, len(comments))
	for i, c := range comments {
		nodeMap[c.ObjectID] = commentNode{res: c, origIndex: i}
	}

	// Build parent -> children map and identify roots
	parentMap := make(map[string]string, len(comments))
	for _, c := range comments {
		if c.Comment.InReplyTo != "" && c.Comment.InReplyTo != c.ObjectID {
			if _, parentExists := nodeMap[c.Comment.InReplyTo]; parentExists {
				parentMap[c.ObjectID] = c.Comment.InReplyTo
			}
		}
	}

	// Cycle detection
	visitState := make(map[string]int, len(comments))
	inCycle := make(map[string]bool)

	for _, c := range comments {
		objID := c.ObjectID
		if visitState[objID] != 0 {
			continue
		}
		var path []string
		curr := objID
		for curr != "" {
			if visitState[curr] == 1 {
				cycleStart := false
				for _, nodeID := range path {
					if nodeID == curr {
						cycleStart = true
					}
					if cycleStart {
						inCycle[nodeID] = true
					}
				}
				break
			}
			if visitState[curr] == 2 {
				break
			}
			visitState[curr] = 1
			path = append(path, curr)
			curr = parentMap[curr]
		}
		for _, nodeID := range path {
			visitState[nodeID] = 2
		}
	}

	for objID := range inCycle {
		delete(parentMap, objID)
	}

	childrenMap := make(map[string][]string, len(comments))
	var rootIDs []string
	for _, c := range comments {
		parentID, hasParent := parentMap[c.ObjectID]
		if hasParent {
			childrenMap[parentID] = append(childrenMap[parentID], c.ObjectID)
		} else {
			rootIDs = append(rootIDs, c.ObjectID)
		}
	}

	// Sibling sorting helper: created_at ASC, origIndex ASC, object_id ASC
	sortIDs := func(ids []string) {
		sort.Slice(ids, func(i, j int) bool {
			nI := nodeMap[ids[i]]
			nJ := nodeMap[ids[j]]
			if !nI.res.CreatedAt.Equal(nJ.res.CreatedAt) {
				return nI.res.CreatedAt.Before(nJ.res.CreatedAt)
			}
			if nI.origIndex != nJ.origIndex {
				return nI.origIndex < nJ.origIndex
			}
			return ids[i] < ids[j]
		})
	}

	sortIDs(rootIDs)

	var buildTree func(id string) state.CommentThread
	buildTree = func(id string) state.CommentThread {
		chIDs := childrenMap[id]
		sortIDs(chIDs)
		replies := make([]state.CommentThread, 0, len(chIDs))
		for _, chID := range chIDs {
			replies = append(replies, buildTree(chID))
		}
		node := nodeMap[id]
		return state.CommentThread{
			ObjectID:   id,
			Comment:    node.res.Comment,
			Replies:    replies,
			UnknownOps: node.res.Comment.UnknownOps,
		}
	}

	threads := make([]state.CommentThread, 0, len(rootIDs))
	for _, rootID := range rootIDs {
		threads = append(threads, buildTree(rootID))
	}

	return threads, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}

func appendLimitOffset(sb *strings.Builder, args *[]any, limit, offset int) {
	if limit > 0 && offset > 0 {
		sb.WriteString(" LIMIT ? OFFSET ?")
		*args = append(*args, limit, offset)
	} else if limit > 0 {
		sb.WriteString(" LIMIT ?")
		*args = append(*args, limit)
	} else if offset > 0 {
		sb.WriteString(" LIMIT -1 OFFSET ?")
		*args = append(*args, offset)
	}
}

// queryIn executes a query where placeholder `?` in `WHERE ... IN (?)` is expanded
// for the slice of string ids.
func (d *DB) queryIn(queryPattern string, ids []string, extraArgs ...any) (*sql.Rows, error) {
	if len(ids) == 0 {
		// Return an empty result set by querying with a false condition
		emptyQuery := strings.Replace(queryPattern, "(?)", "(NULL)", 1)
		return d.db.Query(emptyQuery, extraArgs...)
	}

	ph := placeholders(len(ids))
	query := strings.Replace(queryPattern, "(?)", "("+ph+")", 1)

	var args []any
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, extraArgs...)

	return d.db.Query(query, args...)
}

// Frontier returns the observed frontier of op commits for the given object ID:
// the op commits with no child dependencies within that object.
func (d *DB) Frontier(objectID string) ([]string, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}
	if objectID == "" {
		return nil, nil
	}

	rows, err := d.db.Query("SELECT op_id, parents FROM ops WHERE object_id = ? ORDER BY op_id ASC", objectID)
	if err != nil {
		return nil, fmt.Errorf("projection: query ops for frontier %s: %w", objectID, err)
	}
	defer rows.Close()

	allOps := make(map[string]bool)
	hasChildren := make(map[string]bool)

	for rows.Next() {
		var opID, parentsJSON string
		if err := rows.Scan(&opID, &parentsJSON); err != nil {
			return nil, fmt.Errorf("projection: scan op for frontier: %w", err)
		}
		allOps[opID] = true
		if len(parentsJSON) > 0 {
			var parents []string
			if err := json.Unmarshal([]byte(parentsJSON), &parents); err == nil {
				for _, p := range parents {
					hasChildren[p] = true
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: iterate ops for frontier: %w", err)
	}

	var frontier []string
	for opID := range allOps {
		if !hasChildren[opID] {
			frontier = append(frontier, opID)
		}
	}
	sort.Strings(frontier)

	return frontier, nil
}

// Review fetches a single review by its object ID, returning ErrNotFound if not found.
func (d *DB) Review(objectID string) (ReviewResult, error) {
	if d == nil || d.db == nil {
		return ReviewResult{}, fmt.Errorf("projection: database is closed")
	}
	if err := d.requireBuiltinShape("review"); err != nil {
		return ReviewResult{}, err
	}
	if objectID == "" {
		return ReviewResult{}, ErrNotFound
	}

	var rr struct {
		objectID    string
		title       string
		description string
		status      string
		mergeCommit string
		reason      string
		authorName  string
		authorEmail string
		createdAt   int64
		updatedAt   int64
	}

	err := d.db.QueryRow(
		"SELECT r.object_id, COALESCE(r.f_title, ''), COALESCE(r.f_description, ''), COALESCE(r.f_status, ''), COALESCE(r.f_merge_commit, ''), COALESCE(r.f_reason, ''), o.author_name, o.author_email, o.created_at, o.updated_at FROM o_review r JOIN objects o ON o.object_id = r.object_id WHERE r.object_id = ?",
		objectID,
	).Scan(
		&rr.objectID, &rr.title, &rr.description, &rr.status, &rr.mergeCommit, &rr.reason,
		&rr.authorName, &rr.authorEmail, &rr.createdAt, &rr.updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ReviewResult{}, ErrNotFound
		}
		return ReviewResult{}, fmt.Errorf("projection: query review %s: %w", objectID, err)
	}

	// See Reviews' batch load above for why one row (o_review__base_head,
	// ddl.go's appendGroupPlan) replaces the old base/head zip-by-position.
	revRows, err := d.db.Query("SELECT COALESCE(f_base, ''), COALESCE(f_head, '') FROM o_review__base_head WHERE object_id = ? ORDER BY idx ASC", objectID)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("projection: query review revisions: %w", err)
	}
	var revisions []state.Revision
	for revRows.Next() {
		var base, head string
		if err := revRows.Scan(&base, &head); err != nil {
			revRows.Close()
			return ReviewResult{}, fmt.Errorf("projection: scan review revision: %w", err)
		}
		revisions = append(revisions, state.Revision{Base: base, Head: head})
	}
	revRows.Close()

	var assignees []string
	asRows, err := d.db.Query("SELECT item FROM o_review__assignees WHERE object_id = ? ORDER BY item ASC", objectID)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("projection: query review assignees: %w", err)
	}
	defer asRows.Close()
	for asRows.Next() {
		var assignee string
		if err := asRows.Scan(&assignee); err != nil {
			return ReviewResult{}, fmt.Errorf("projection: scan review assignee: %w", err)
		}
		assignees = append(assignees, assignee)
	}
	if err := asRows.Err(); err != nil {
		return ReviewResult{}, fmt.Errorf("projection: iterate review assignees: %w", err)
	}

	var labels []string
	lblRows, err := d.db.Query("SELECT item FROM o_review__labels WHERE object_id = ? ORDER BY item ASC", objectID)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("projection: query review labels: %w", err)
	}
	defer lblRows.Close()
	for lblRows.Next() {
		var label string
		if err := lblRows.Scan(&label); err != nil {
			return ReviewResult{}, fmt.Errorf("projection: scan review label: %w", err)
		}
		labels = append(labels, label)
	}
	if err := lblRows.Err(); err != nil {
		return ReviewResult{}, fmt.Errorf("projection: iterate review labels: %w", err)
	}

	var links []state.Link
	lnkRows, err := d.db.Query("SELECT k_target, COALESCE(f_target_type, ''), COALESCE(f_relation, '') FROM o_review__k_target WHERE object_id = ? AND COALESCE(f_relation, '') NOT IN ('', 'none') ORDER BY k_target ASC", objectID)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("projection: query review links: %w", err)
	}
	defer lnkRows.Close()
	for lnkRows.Next() {
		var target, targetType, relation string
		if err := lnkRows.Scan(&target, &targetType, &relation); err != nil {
			return ReviewResult{}, fmt.Errorf("projection: scan review link: %w", err)
		}
		links = append(links, state.Link{
			Target:     target,
			TargetType: targetType,
			Relation:   relation,
		})
	}
	if err := lnkRows.Err(); err != nil {
		return ReviewResult{}, fmt.Errorf("projection: iterate review links: %w", err)
	}

	var approvals []state.Approval
	appRows, err := d.db.Query("SELECT k_subject, k_revision, COALESCE(f_verdict, ''), COALESCE(f_message, '') FROM o_review__k_subject_revision WHERE object_id = ? AND COALESCE(f_verdict, '') NOT IN ('', 'none') ORDER BY k_subject ASC, k_revision ASC", objectID)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("projection: query approvals: %w", err)
	}
	defer appRows.Close()
	for appRows.Next() {
		var subject, revision, verdict, message string
		if err := appRows.Scan(&subject, &revision, &verdict, &message); err != nil {
			return ReviewResult{}, fmt.Errorf("projection: scan approval: %w", err)
		}
		approvals = append(approvals, state.Approval{
			Subject:  subject,
			Revision: revision,
			Verdict:  verdict,
			Message:  message,
		})
	}
	if err := appRows.Err(); err != nil {
		return ReviewResult{}, fmt.Errorf("projection: iterate approvals: %w", err)
	}

	var ciStatuses []state.CIStatus
	ciRows, err := d.db.Query("SELECT k_revision, k_name, COALESCE(f_state, ''), COALESCE(f_url, ''), COALESCE(f_ci_description, ''), COALESCE(f_started_at, ''), COALESCE(f_completed_at, ''), COALESCE(f_external_id, '') FROM o_review__k_revision_name WHERE object_id = ? ORDER BY k_revision ASC, k_name ASC", objectID)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("projection: query ci_statuses: %w", err)
	}
	defer ciRows.Close()
	for ciRows.Next() {
		var revision, name, stateVal, url, description, startedAt, completedAt, externalID string
		if err := ciRows.Scan(&revision, &name, &stateVal, &url, &description, &startedAt, &completedAt, &externalID); err != nil {
			return ReviewResult{}, fmt.Errorf("projection: scan ci_status: %w", err)
		}
		ciStatuses = append(ciStatuses, state.CIStatus{
			Revision:    revision,
			Name:        name,
			State:       stateVal,
			URL:         url,
			Description: description,
			StartedAt:   startedAt,
			CompletedAt: completedAt,
			ExternalID:  externalID,
		})
	}
	if err := ciRows.Err(); err != nil {
		return ReviewResult{}, fmt.Errorf("projection: iterate ci_statuses: %w", err)
	}

	unknownOps, err := d.unknownOpsFor(objectID)
	if err != nil {
		return ReviewResult{}, err
	}

	return ReviewResult{
		ObjectID:  rr.objectID,
		Author:    Author{Name: rr.authorName, Email: rr.authorEmail},
		CreatedAt: time.Unix(rr.createdAt, 0).UTC(),
		UpdatedAt: time.Unix(rr.updatedAt, 0).UTC(),
		Review: state.Review{
			Title:       rr.title,
			Description: rr.description,
			Status:      rr.status,
			MergeCommit: rr.mergeCommit,
			Reason:      rr.reason,
			Assignees:   assignees,
			Labels:      labels,
			Links:       links,
			Revisions:   revisions,
			Approvals:   approvals,
			CIStatuses:  ciStatuses,
			UnknownOps:  unknownOps,
		},
	}, nil
}

// Issue fetches a single issue by its object ID, returning ErrNotFound if not found.
func (d *DB) Issue(objectID string) (IssueResult, error) {
	if d == nil || d.db == nil {
		return IssueResult{}, fmt.Errorf("projection: database is closed")
	}
	if err := d.requireBuiltinShape("issue"); err != nil {
		return IssueResult{}, err
	}
	if objectID == "" {
		return IssueResult{}, ErrNotFound
	}

	var ri struct {
		objectID    string
		title       string
		description string
		state       string
		reason      string
		priority    int
		estimate    sql.NullFloat64
		position    string
		authorName  string
		authorEmail string
		createdAt   int64
		updatedAt   int64
	}

	err := d.db.QueryRow(
		"SELECT i.object_id, COALESCE(i.f_title, ''), COALESCE(i.f_description, ''), COALESCE(i.f_state, ''), COALESCE(i.f_reason, ''), COALESCE(i.f_priority, 0), i.f_estimate, COALESCE(i.f_position, ''), o.author_name, o.author_email, o.created_at, o.updated_at FROM o_issue i JOIN objects o ON o.object_id = i.object_id WHERE i.object_id = ?",
		objectID,
	).Scan(
		&ri.objectID, &ri.title, &ri.description, &ri.state, &ri.reason,
		&ri.priority, &ri.estimate, &ri.position,
		&ri.authorName, &ri.authorEmail, &ri.createdAt, &ri.updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IssueResult{}, ErrNotFound
		}
		return IssueResult{}, fmt.Errorf("projection: query issue %s: %w", objectID, err)
	}

	var assignees []string
	asRows, err := d.db.Query("SELECT item FROM o_issue__assignees WHERE object_id = ? ORDER BY item ASC", objectID)
	if err != nil {
		return IssueResult{}, fmt.Errorf("projection: query issue assignees: %w", err)
	}
	defer asRows.Close()
	for asRows.Next() {
		var assignee string
		if err := asRows.Scan(&assignee); err != nil {
			return IssueResult{}, fmt.Errorf("projection: scan issue assignee: %w", err)
		}
		assignees = append(assignees, assignee)
	}
	if err := asRows.Err(); err != nil {
		return IssueResult{}, fmt.Errorf("projection: iterate issue assignees: %w", err)
	}

	var labels []string
	lblRows, err := d.db.Query("SELECT item FROM o_issue__labels WHERE object_id = ? ORDER BY item ASC", objectID)
	if err != nil {
		return IssueResult{}, fmt.Errorf("projection: query issue labels: %w", err)
	}
	defer lblRows.Close()
	for lblRows.Next() {
		var label string
		if err := lblRows.Scan(&label); err != nil {
			return IssueResult{}, fmt.Errorf("projection: scan issue label: %w", err)
		}
		labels = append(labels, label)
	}
	if err := lblRows.Err(); err != nil {
		return IssueResult{}, fmt.Errorf("projection: iterate issue labels: %w", err)
	}

	var links []state.Link
	lnkRows, err := d.db.Query("SELECT k_target, COALESCE(f_target_type, ''), COALESCE(f_relation, '') FROM o_issue__k_target WHERE object_id = ? AND COALESCE(f_relation, '') NOT IN ('', 'none') ORDER BY k_target ASC", objectID)
	if err != nil {
		return IssueResult{}, fmt.Errorf("projection: query issue links: %w", err)
	}
	defer lnkRows.Close()
	for lnkRows.Next() {
		var target, targetType, relation string
		if err := lnkRows.Scan(&target, &targetType, &relation); err != nil {
			return IssueResult{}, fmt.Errorf("projection: scan issue link: %w", err)
		}
		links = append(links, state.Link{
			Target:     target,
			TargetType: targetType,
			Relation:   relation,
		})
	}
	if err := lnkRows.Err(); err != nil {
		return IssueResult{}, fmt.Errorf("projection: iterate issue links: %w", err)
	}

	unknownOps, err := d.unknownOpsFor(objectID)
	if err != nil {
		return IssueResult{}, err
	}

	var est *float64
	if ri.estimate.Valid {
		est = &ri.estimate.Float64
	}

	return IssueResult{
		ObjectID:  ri.objectID,
		Author:    Author{Name: ri.authorName, Email: ri.authorEmail},
		CreatedAt: time.Unix(ri.createdAt, 0).UTC(),
		UpdatedAt: time.Unix(ri.updatedAt, 0).UTC(),
		Issue: state.Issue{
			Title:       ri.title,
			Description: ri.description,
			State:       ri.state,
			Reason:      ri.reason,
			Priority:    ri.priority,
			Estimate:    est,
			Position:    ri.position,
			Assignees:   assignees,
			Labels:      labels,
			Links:       links,
			UnknownOps:  unknownOps,
		},
	}, nil
}

// WorkflowStates executes a list and filter query over workflow states.
func (d *DB) WorkflowStates(f WorkflowStateFilter) ([]WorkflowStateResult, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}
	if err := d.requireBuiltinShape("workflow-state"); err != nil {
		return nil, err
	}

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT ws.object_id, COALESCE(ws.f_name, ''), COALESCE(ws.f_type, ''), COALESCE(ws.f_position, ''), COALESCE(ws.f_color, ''), COALESCE(ws.f_description, ''), ")
	sb.WriteString("o.author_name, o.author_email, o.created_at, o.updated_at, o.last_op_id ")
	sb.WriteString("FROM o_workflow_state ws JOIN objects o ON o.object_id = ws.object_id WHERE 1=1")

	if len(f.Type) > 0 {
		sb.WriteString(" AND ws.f_type IN (" + placeholders(len(f.Type)) + ")")
		for _, t := range f.Type {
			args = append(args, t)
		}
	}

	switch f.OrderBy {
	case OrderByCreatedAtAsc:
		sb.WriteString(" ORDER BY o.created_at ASC, ws.object_id ASC")
	case OrderByCreatedAtDesc:
		sb.WriteString(" ORDER BY o.created_at DESC, ws.object_id DESC")
	case OrderByUpdatedAtAsc:
		sb.WriteString(" ORDER BY o.updated_at ASC, ws.object_id ASC")
	case OrderByUpdatedAtDesc:
		sb.WriteString(" ORDER BY o.updated_at DESC, ws.object_id DESC")
	case OrderByTitleAsc:
		sb.WriteString(" ORDER BY ws.f_name ASC, ws.object_id ASC")
	case OrderByTitleDesc:
		sb.WriteString(" ORDER BY ws.f_name DESC, ws.object_id DESC")
	default:
		sb.WriteString(" ORDER BY COALESCE(ws.f_position, '') ASC, COALESCE(ws.f_position__op_id, '') ASC")
	}

	appendLimitOffset(&sb, &args, f.Limit, f.Offset)

	rows, err := d.db.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query workflow states: %w", err)
	}
	defer rows.Close()

	type rawState struct {
		objectID    string
		name        string
		stateType   string
		position    string
		color       string
		description string
		authorName  string
		authorEmail string
		createdAt   int64
		updatedAt   int64
		lastOpID    string
	}

	var rawStates []rawState
	var objectIDs []string

	for rows.Next() {
		var rs rawState
		if err := rows.Scan(
			&rs.objectID, &rs.name, &rs.stateType, &rs.position, &rs.color, &rs.description,
			&rs.authorName, &rs.authorEmail, &rs.createdAt, &rs.updatedAt, &rs.lastOpID,
		); err != nil {
			return nil, fmt.Errorf("projection: scan workflow state: %w", err)
		}
		rawStates = append(rawStates, rs)
		objectIDs = append(objectIDs, rs.objectID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: scan workflow states rows: %w", err)
	}

	unknownMap, err := d.loadUnknownOps(objectIDs)
	if err != nil {
		return nil, err
	}

	results := make([]WorkflowStateResult, 0, len(rawStates))
	for _, rs := range rawStates {
		results = append(results, WorkflowStateResult{
			ObjectID: rs.objectID,
			Author: Author{
				Name:  rs.authorName,
				Email: rs.authorEmail,
			},
			CreatedAt: time.Unix(rs.createdAt, 0).UTC(),
			UpdatedAt: time.Unix(rs.updatedAt, 0).UTC(),
			WorkflowState: state.WorkflowState{
				Name:        rs.name,
				Type:        rs.stateType,
				Position:    rs.position,
				Color:       rs.color,
				Description: rs.description,
				UnknownOps:  unknownMap[rs.objectID],
			},
		})
	}
	return results, nil
}

// WorkflowState fetches a single workflow state by its object ID, returning ErrNotFound if not found.
func (d *DB) WorkflowState(id string) (WorkflowStateResult, error) {
	if d == nil || d.db == nil {
		return WorkflowStateResult{}, fmt.Errorf("projection: database is closed")
	}
	if err := d.requireBuiltinShape("workflow-state"); err != nil {
		return WorkflowStateResult{}, err
	}
	if id == "" {
		return WorkflowStateResult{}, fmt.Errorf("projection: workflow state id cannot be empty")
	}

	var rs struct {
		objectID    string
		name        string
		stateType   string
		position    string
		color       string
		description string
		authorName  string
		authorEmail string
		createdAt   int64
		updatedAt   int64
		lastOpID    string
	}

	query := "SELECT ws.object_id, COALESCE(ws.f_name, ''), COALESCE(ws.f_type, ''), COALESCE(ws.f_position, ''), COALESCE(ws.f_color, ''), COALESCE(ws.f_description, ''), " +
		"o.author_name, o.author_email, o.created_at, o.updated_at, o.last_op_id " +
		"FROM o_workflow_state ws JOIN objects o ON o.object_id = ws.object_id WHERE ws.object_id = ?"

	err := d.db.QueryRow(query, id).Scan(
		&rs.objectID, &rs.name, &rs.stateType, &rs.position, &rs.color, &rs.description,
		&rs.authorName, &rs.authorEmail, &rs.createdAt, &rs.updatedAt, &rs.lastOpID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WorkflowStateResult{}, ErrNotFound
		}
		return WorkflowStateResult{}, fmt.Errorf("projection: query workflow state %s: %w", id, err)
	}

	unknownOps, err := d.unknownOpsFor(id)
	if err != nil {
		return WorkflowStateResult{}, err
	}

	return WorkflowStateResult{
		ObjectID: rs.objectID,
		Author: Author{
			Name:  rs.authorName,
			Email: rs.authorEmail,
		},
		CreatedAt: time.Unix(rs.createdAt, 0).UTC(),
		UpdatedAt: time.Unix(rs.updatedAt, 0).UTC(),
		WorkflowState: state.WorkflowState{
			Name:        rs.name,
			Type:        rs.stateType,
			Position:    rs.position,
			Color:       rs.color,
			Description: rs.description,
			UnknownOps:  unknownOps,
		},
	}, nil
}

// Labels executes a list query over labels.
func (d *DB) Labels(f LabelFilter) ([]LabelResult, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}
	if err := d.requireBuiltinShape("label"); err != nil {
		return nil, err
	}

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT l.object_id, COALESCE(l.f_name, ''), COALESCE(l.f_color, ''), COALESCE(l.f_description, ''), ")
	sb.WriteString("o.author_name, o.author_email, o.created_at, o.updated_at ")
	sb.WriteString("FROM o_label l JOIN objects o ON o.object_id = l.object_id WHERE 1=1")

	switch f.OrderBy {
	case OrderByCreatedAtAsc:
		sb.WriteString(" ORDER BY o.created_at ASC, l.object_id ASC")
	case OrderByCreatedAtDesc:
		sb.WriteString(" ORDER BY o.created_at DESC, l.object_id DESC")
	case OrderByUpdatedAtAsc:
		sb.WriteString(" ORDER BY o.updated_at ASC, l.object_id ASC")
	case OrderByUpdatedAtDesc:
		sb.WriteString(" ORDER BY o.updated_at DESC, l.object_id DESC")
	case OrderByTitleAsc:
		sb.WriteString(" ORDER BY l.f_name ASC, l.object_id ASC")
	case OrderByTitleDesc:
		sb.WriteString(" ORDER BY l.f_name DESC, l.object_id DESC")
	default:
		sb.WriteString(" ORDER BY LOWER(l.f_name) ASC, l.object_id ASC")
	}

	appendLimitOffset(&sb, &args, f.Limit, f.Offset)

	rows, err := d.db.Query(sb.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query labels: %w", err)
	}
	defer rows.Close()

	type rawLabel struct {
		objectID    string
		name        string
		color       string
		description string
		authorName  string
		authorEmail string
		createdAt   int64
		updatedAt   int64
	}

	var rawLabels []rawLabel
	var objectIDs []string

	for rows.Next() {
		var rl rawLabel
		if err := rows.Scan(
			&rl.objectID, &rl.name, &rl.color, &rl.description,
			&rl.authorName, &rl.authorEmail, &rl.createdAt, &rl.updatedAt,
		); err != nil {
			return nil, fmt.Errorf("projection: scan label: %w", err)
		}
		rawLabels = append(rawLabels, rl)
		objectIDs = append(objectIDs, rl.objectID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: scan labels rows: %w", err)
	}

	unknownMap, err := d.loadUnknownOps(objectIDs)
	if err != nil {
		return nil, err
	}

	results := make([]LabelResult, 0, len(rawLabels))
	for _, rl := range rawLabels {
		results = append(results, LabelResult{
			ObjectID: rl.objectID,
			Author: Author{
				Name:  rl.authorName,
				Email: rl.authorEmail,
			},
			CreatedAt: time.Unix(rl.createdAt, 0).UTC(),
			UpdatedAt: time.Unix(rl.updatedAt, 0).UTC(),
			Label: state.Label{
				Name:        rl.name,
				Color:       rl.color,
				Description: rl.description,
				UnknownOps:  unknownMap[rl.objectID],
			},
		})
	}

	return results, nil
}

// Label fetches a single label by its object ID, returning ErrNotFound if not found.
func (d *DB) Label(id string) (LabelResult, error) {
	if d == nil || d.db == nil {
		return LabelResult{}, fmt.Errorf("projection: database is closed")
	}
	if err := d.requireBuiltinShape("label"); err != nil {
		return LabelResult{}, err
	}
	if id == "" {
		return LabelResult{}, fmt.Errorf("projection: label id cannot be empty")
	}

	var (
		objectID    string
		name        string
		color       string
		description string
		authorName  string
		authorEmail string
		createdAt   int64
		updatedAt   int64
	)

	err := d.db.QueryRow(
		"SELECT l.object_id, COALESCE(l.f_name, ''), COALESCE(l.f_color, ''), COALESCE(l.f_description, ''), o.author_name, o.author_email, o.created_at, o.updated_at FROM o_label l JOIN objects o ON o.object_id = l.object_id WHERE l.object_id = ?",
		id,
	).Scan(&objectID, &name, &color, &description, &authorName, &authorEmail, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LabelResult{}, ErrNotFound
		}
		return LabelResult{}, fmt.Errorf("projection: query label %s: %w", id, err)
	}

	unknownOps, err := d.unknownOpsFor(id)
	if err != nil {
		return LabelResult{}, err
	}

	return LabelResult{
		ObjectID: objectID,
		Author: Author{
			Name:  authorName,
			Email: authorEmail,
		},
		CreatedAt: time.Unix(createdAt, 0).UTC(),
		UpdatedAt: time.Unix(updatedAt, 0).UTC(),
		Label: state.Label{
			Name:        name,
			Color:       color,
			Description: description,
			UnknownOps:  unknownOps,
		},
	}, nil
}

func appendLabelFilter(sb *strings.Builder, args *[]any, tableName, alias, objIDCol, parentObjIDCol string, labelFilters []string) {
	if len(labelFilters) == 0 {
		return
	}
	var rawValues []string
	var lowerValues []string
	seenRaw := make(map[string]bool)
	seenLower := make(map[string]bool)

	addVal := func(v string) {
		if !seenRaw[v] {
			seenRaw[v] = true
			rawValues = append(rawValues, v)
		}
		low := strings.ToLower(v)
		if !seenLower[low] {
			seenLower[low] = true
			lowerValues = append(lowerValues, low)
		}
	}

	for _, l := range labelFilters {
		addVal(l)
		if idx := strings.Index(l, "#"); idx >= 0 && idx < len(l)-1 {
			addVal(l[idx+1:])
		}
	}

	rawPH := placeholders(len(rawValues))
	lowPH := placeholders(len(lowerValues))

	sb.WriteString(fmt.Sprintf(" AND EXISTS (SELECT 1 FROM %s %s WHERE %s.%s = %s AND (%s.item IN (%s) OR LOWER(%s.item) IN (%s) OR (LENGTH(%s.item) > 32 AND substr(%s.item, -32) IN (%s)) OR %s.item IN (SELECT object_id FROM o_label WHERE LOWER(f_name) IN (%s)) OR (LENGTH(%s.item) > 32 AND substr(%s.item, -32) IN (SELECT object_id FROM o_label WHERE LOWER(f_name) IN (%s))) OR LOWER(%s.item) IN (SELECT LOWER(f_name) FROM o_label WHERE object_id IN (%s))))",
		tableName, alias, alias, objIDCol, parentObjIDCol,
		alias, rawPH,
		alias, lowPH,
		alias, alias, rawPH,
		alias, lowPH,
		alias, alias, lowPH,
		alias, rawPH,
	))

	for _, v := range rawValues {
		*args = append(*args, v)
	}
	for _, v := range lowerValues {
		*args = append(*args, v)
	}
	for _, v := range rawValues {
		*args = append(*args, v)
	}
	for _, v := range lowerValues {
		*args = append(*args, v)
	}
	for _, v := range lowerValues {
		*args = append(*args, v)
	}
	for _, v := range rawValues {
		*args = append(*args, v)
	}
}

// Documents executes a list and filter query over documents, returning documents with their ordered sections.
func (d *DB) Documents(f DocumentFilter) ([]DocumentResult, error) {
	if err := d.requireBuiltinShape("document"); err != nil {
		return nil, err
	}
	if err := d.requireBuiltinShape("section"); err != nil {
		return nil, err
	}

	var conditions []string
	var args []any

	if len(f.Labels) > 0 {
		qmarks := make([]string, len(f.Labels))
		for i, label := range f.Labels {
			qmarks[i] = "?"
			args = append(args, label)
		}
		conditions = append(conditions, fmt.Sprintf(
			"d.object_id IN (SELECT object_id FROM o_document__labels WHERE item IN (%s) GROUP BY object_id HAVING COUNT(DISTINCT item) = %d)",
			strings.Join(qmarks, ", "), len(f.Labels),
		))
	}

	query := "SELECT d.object_id, COALESCE(d.f_title, ''), " +
		"o.author_name, o.author_email, o.created_at, o.updated_at " +
		"FROM o_document d JOIN objects o ON o.object_id = d.object_id"

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY o.created_at ASC, d.object_id ASC"

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query documents: %w", err)
	}
	defer rows.Close()

	type rawDoc struct {
		objectID    string
		title       string
		authorName  string
		authorEmail string
		createdAt   int64
		updatedAt   int64
	}

	var rawDocs []rawDoc
	var docIDs []string
	for rows.Next() {
		var rd rawDoc
		if err := rows.Scan(&rd.objectID, &rd.title, &rd.authorName, &rd.authorEmail, &rd.createdAt, &rd.updatedAt); err != nil {
			return nil, fmt.Errorf("projection: scan document: %w", err)
		}
		rawDocs = append(rawDocs, rd)
		docIDs = append(docIDs, rd.objectID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: iterate documents: %w", err)
	}

	if len(rawDocs) == 0 {
		return []DocumentResult{}, nil
	}

	labelsByDoc, linksByDoc, err := d.loadDocumentDetails(docIDs)
	if err != nil {
		return nil, err
	}
	sectionsByDoc, err := d.loadSectionsForDocuments(docIDs)
	if err != nil {
		return nil, err
	}
	unknownMap, err := d.loadUnknownOps(docIDs)
	if err != nil {
		return nil, err
	}

	results := make([]DocumentResult, len(rawDocs))
	for i, rd := range rawDocs {
		results[i] = DocumentResult{
			ObjectID: rd.objectID,
			Author: Author{
				Name:  rd.authorName,
				Email: rd.authorEmail,
			},
			CreatedAt: time.Unix(rd.createdAt, 0).UTC(),
			UpdatedAt: time.Unix(rd.updatedAt, 0).UTC(),
			Document: state.Document{
				Title:      rd.title,
				Labels:     labelsByDoc[rd.objectID],
				Links:      linksByDoc[rd.objectID],
				UnknownOps: unknownMap[rd.objectID],
			},
			Sections: sectionsByDoc[rd.objectID],
		}
	}
	return results, nil
}

func (d *DB) loadDocumentDetails(docIDs []string) (map[string][]string, map[string][]state.Link, error) {
	labelsByDoc := make(map[string][]string)
	linksByDoc := make(map[string][]state.Link)
	if len(docIDs) == 0 {
		return labelsByDoc, linksByDoc, nil
	}

	lblRows, err := d.queryIn("SELECT object_id, item FROM o_document__labels WHERE object_id IN (?) ORDER BY object_id ASC, item ASC", docIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("projection: query document labels: %w", err)
	}
	for lblRows.Next() {
		var objID, label string
		if err := lblRows.Scan(&objID, &label); err != nil {
			lblRows.Close()
			return nil, nil, fmt.Errorf("projection: scan document label: %w", err)
		}
		labelsByDoc[objID] = append(labelsByDoc[objID], label)
	}
	lblRows.Close()
	if err := lblRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("projection: iterate document labels: %w", err)
	}

	lnkRows, err := d.queryIn("SELECT object_id, k_target, COALESCE(f_target_type, ''), COALESCE(f_relation, '') FROM o_document__k_target WHERE object_id IN (?) AND COALESCE(f_relation, '') NOT IN ('', 'none') ORDER BY object_id ASC, k_target ASC", docIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("projection: query document links: %w", err)
	}
	for lnkRows.Next() {
		var objID, target, targetType, relation string
		if err := lnkRows.Scan(&objID, &target, &targetType, &relation); err != nil {
			lnkRows.Close()
			return nil, nil, fmt.Errorf("projection: scan document link: %w", err)
		}
		linksByDoc[objID] = append(linksByDoc[objID], state.Link{Target: target, TargetType: targetType, Relation: relation})
	}
	lnkRows.Close()
	if err := lnkRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("projection: iterate document links: %w", err)
	}

	return labelsByDoc, linksByDoc, nil
}

func (d *DB) loadSectionsForDocuments(docIDs []string) (map[string][]SectionResult, error) {
	sectionsByDoc := make(map[string][]SectionResult)
	if len(docIDs) == 0 {
		return sectionsByDoc, nil
	}

	qmarks := make([]string, len(docIDs))
	args := make([]any, len(docIDs))
	for i, id := range docIDs {
		qmarks[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		"SELECT s.object_id, COALESCE(s.f_document_id, ''), COALESCE(s.f_position, ''), COALESCE(s.f_position__op_id, ''), COALESCE(s.f_title, ''), "+
			"o.author_name, o.author_email, o.created_at, o.updated_at "+
			"FROM o_section s JOIN objects o ON o.object_id = s.object_id "+
			"WHERE s.f_document_id IN (%s) AND (s.f_deleted = 0 OR s.f_deleted IS NULL) "+
			"ORDER BY COALESCE(s.f_position, '') ASC, COALESCE(s.f_position__op_id, '') ASC",
		strings.Join(qmarks, ", "),
	)

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query sections: %w", err)
	}
	defer rows.Close()

	type rawSection struct {
		objectID    string
		documentID  string
		position    string
		title       string
		authorName  string
		authorEmail string
		createdAt   int64
		updatedAt   int64
	}
	var rawSections []rawSection
	var sectionIDs []string
	for rows.Next() {
		var rs rawSection
		var opID string
		if err := rows.Scan(&rs.objectID, &rs.documentID, &rs.position, &opID, &rs.title, &rs.authorName, &rs.authorEmail, &rs.createdAt, &rs.updatedAt); err != nil {
			return nil, fmt.Errorf("projection: scan section: %w", err)
		}
		rawSections = append(rawSections, rs)
		sectionIDs = append(sectionIDs, rs.objectID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: iterate sections: %w", err)
	}

	bodies, err := d.loadSectionBodies(sectionIDs)
	if err != nil {
		return nil, err
	}

	for _, rs := range rawSections {
		sectionsByDoc[rs.documentID] = append(sectionsByDoc[rs.documentID], SectionResult{
			ObjectID: rs.objectID,
			Author: Author{
				Name:  rs.authorName,
				Email: rs.authorEmail,
			},
			CreatedAt: time.Unix(rs.createdAt, 0).UTC(),
			UpdatedAt: time.Unix(rs.updatedAt, 0).UTC(),
			Section: state.Section{
				DocumentID: rs.documentID,
				Position:   rs.position,
				Title:      rs.title,
				Body:       bodies[rs.objectID],
			},
		})
	}
	return sectionsByDoc, nil
}

// loadSectionBodies reads o_section__body rows for a set of sections and
// derives each section's Body: a bare string when settled (one row), a
// []string when concurrent (more than one row) — sections.conflicted and
// sections.body used to be materialized columns; both are now derived from
// this child table by the reader instead.
func (d *DB) loadSectionBodies(sectionIDs []string) (map[string]any, error) {
	bodies := make(map[string]any)
	if len(sectionIDs) == 0 {
		return bodies, nil
	}
	rows, err := d.queryIn("SELECT object_id, value FROM o_section__body WHERE object_id IN (?) ORDER BY object_id ASC, idx ASC", sectionIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query section bodies: %w", err)
	}
	defer rows.Close()
	byID := make(map[string][]string)
	for rows.Next() {
		var objID, val string
		if err := rows.Scan(&objID, &val); err != nil {
			return nil, fmt.Errorf("projection: scan section body: %w", err)
		}
		byID[objID] = append(byID[objID], val)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: iterate section bodies: %w", err)
	}
	for objID, vals := range byID {
		if len(vals) == 1 {
			bodies[objID] = vals[0]
		} else {
			bodies[objID] = vals
		}
	}
	return bodies, nil
}

// Document fetches a single document by its object ID, returning ErrNotFound if not found.
func (d *DB) Document(id string) (DocumentResult, error) {
	if err := d.requireBuiltinShape("document"); err != nil {
		return DocumentResult{}, err
	}
	if err := d.requireBuiltinShape("section"); err != nil {
		return DocumentResult{}, err
	}

	query := "SELECT d.object_id, COALESCE(d.f_title, ''), " +
		"o.author_name, o.author_email, o.created_at, o.updated_at " +
		"FROM o_document d JOIN objects o ON o.object_id = d.object_id WHERE d.object_id = ?"

	var objID, title, authorName, authorEmail string
	var createdAt, updatedAt int64
	err := d.db.QueryRow(query, id).Scan(
		&objID, &title, &authorName, &authorEmail, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DocumentResult{}, ErrNotFound
		}
		return DocumentResult{}, fmt.Errorf("projection: query document %s: %w", id, err)
	}

	labelsByDoc, linksByDoc, err := d.loadDocumentDetails([]string{id})
	if err != nil {
		return DocumentResult{}, err
	}
	sectionsByDoc, err := d.loadSectionsForDocuments([]string{id})
	if err != nil {
		return DocumentResult{}, err
	}
	unknownOps, err := d.unknownOpsFor(id)
	if err != nil {
		return DocumentResult{}, err
	}

	return DocumentResult{
		ObjectID: objID,
		Author: Author{
			Name:  authorName,
			Email: authorEmail,
		},
		CreatedAt: time.Unix(createdAt, 0).UTC(),
		UpdatedAt: time.Unix(updatedAt, 0).UTC(),
		Document: state.Document{
			Title:      title,
			Labels:     labelsByDoc[id],
			Links:      linksByDoc[id],
			UnknownOps: unknownOps,
		},
		Sections: sectionsByDoc[id],
	}, nil
}

// Section fetches a single document section by its object ID, returning ErrNotFound if not found.
func (d *DB) Section(id string) (SectionResult, error) {
	if err := d.requireBuiltinShape("section"); err != nil {
		return SectionResult{}, err
	}

	query := "SELECT s.object_id, COALESCE(s.f_document_id, ''), COALESCE(s.f_position, ''), COALESCE(s.f_title, ''), COALESCE(s.f_deleted, 0), " +
		"o.author_name, o.author_email, o.created_at, o.updated_at " +
		"FROM o_section s JOIN objects o ON o.object_id = s.object_id WHERE s.object_id = ?"

	var objID, docID, pos, title string
	var deleted int
	var authorName, authorEmail string
	var createdAt, updatedAt int64
	err := d.db.QueryRow(query, id).Scan(
		&objID, &docID, &pos, &title, &deleted, &authorName, &authorEmail, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SectionResult{}, ErrNotFound
		}
		return SectionResult{}, fmt.Errorf("projection: query section %s: %w", id, err)
	}

	bodies, err := d.loadSectionBodies([]string{id})
	if err != nil {
		return SectionResult{}, err
	}
	unknownOps, err := d.unknownOpsFor(id)
	if err != nil {
		return SectionResult{}, err
	}

	return SectionResult{
		ObjectID: objID,
		Author: Author{
			Name:  authorName,
			Email: authorEmail,
		},
		CreatedAt: time.Unix(createdAt, 0).UTC(),
		UpdatedAt: time.Unix(updatedAt, 0).UTC(),
		Section: state.Section{
			DocumentID: docID,
			Position:   pos,
			Title:      title,
			Body:       bodies[id],
			Deleted:    deleted == 1,
			UnknownOps: unknownOps,
		},
	}, nil
}

// Settings queries the current workspace settings from the projection.
// If no settings operations have been written to the projection,
// it returns the default settings.
func (d *DB) Settings() (SettingsResult, error) {
	if d == nil || d.db == nil {
		return SettingsResult{}, fmt.Errorf("projection: database is closed")
	}
	if err := d.requireBuiltinShape("settings"); err != nil {
		return SettingsResult{}, err
	}

	hasTable, err := d.hasTable("o_settings")
	if err != nil {
		return SettingsResult{}, err
	}
	if !hasTable {
		return SettingsResult{
			ObjectID: state.DefaultSettingsObjectID,
			Settings: state.DefaultSettings(),
		}, nil
	}

	var (
		objectID           string
		name               sql.NullString
		identifier         sql.NullString
		timezone           sql.NullString
		estimateScale      sql.NullString
		allowZeroInt       sql.NullInt64
		cyclesEnabledInt   sql.NullInt64
		cycleDurationWeeks sql.NullInt64
		cycleStartDay      sql.NullInt64
		cycleCooldownWeeks sql.NullInt64
		triageEnabledInt   sql.NullInt64
		unkJSON            sql.NullString
		updatedAt          int64
	)

	// No COALESCE here: a NULL column means the register has never been
	// written by any op (state.FoldSettings starts from
	// state.DefaultSettings() and only overwrites a field an op's body
	// actually names), and this reader must reproduce that fold exactly
	// rather than substitute SQL's own zero-value default — UTC/fibonacci/2/1
	// for timezone/estimate_scale/cycle_duration_weeks/cycle_start_day, not
	// ""/""/0/0 (MAJOR-1, WRIT-189 round 1).
	row := d.db.QueryRow(
		"SELECT s.object_id, s.f_name, s.f_identifier, s.f_timezone, s.f_estimate_scale, "+
			"s.f_allow_zero_estimates, s.f_cycles_enabled, s.f_cycle_duration_weeks, s.f_cycle_start_day, s.f_cycle_cooldown_weeks, "+
			"s.f_triage_enabled, s.unknown_fields, o.updated_at "+
			"FROM o_settings s JOIN objects o ON o.object_id = s.object_id ORDER BY CASE WHEN s.object_id = ? THEN 0 ELSE 1 END, s.object_id ASC LIMIT 1",
		state.DefaultSettingsObjectID,
	)
	err = row.Scan(
		&objectID, &name, &identifier, &timezone, &estimateScale,
		&allowZeroInt, &cyclesEnabledInt, &cycleDurationWeeks, &cycleStartDay, &cycleCooldownWeeks,
		&triageEnabledInt, &unkJSON, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SettingsResult{
			ObjectID: state.DefaultSettingsObjectID,
			Settings: state.DefaultSettings(),
		}, nil
	}
	if err != nil {
		return SettingsResult{}, fmt.Errorf("projection: query settings: %w", err)
	}

	unkKeys := make(map[string]any)
	if unkJSON.Valid && unkJSON.String != "" {
		_ = json.Unmarshal([]byte(unkJSON.String), &unkKeys)
	}

	defaults := state.DefaultSettings()
	sett := state.Settings{
		ObjectID:           objectID,
		Name:               nullOr(name, defaults.Name),
		Identifier:         nullOr(identifier, defaults.Identifier),
		Timezone:           nullOr(timezone, defaults.Timezone),
		EstimateScale:      nullOr(estimateScale, defaults.EstimateScale),
		AllowZeroEstimates: nullIntOr(allowZeroInt, boolToInt(defaults.AllowZeroEstimates)) != 0,
		CyclesEnabled:      nullIntOr(cyclesEnabledInt, boolToInt(defaults.CyclesEnabled)) != 0,
		CycleDurationWeeks: int(nullIntOr(cycleDurationWeeks, int64(defaults.CycleDurationWeeks))),
		CycleStartDay:      int(nullIntOr(cycleStartDay, int64(defaults.CycleStartDay))),
		CycleCooldownWeeks: int(nullIntOr(cycleCooldownWeeks, int64(defaults.CycleCooldownWeeks))),
		TriageEnabled:      nullIntOr(triageEnabledInt, boolToInt(defaults.TriageEnabled)) != 0,
		UnknownKeys:        unkKeys,
	}

	unknownOps, err := d.unknownOpsFor(objectID)
	if err != nil {
		return SettingsResult{}, err
	}
	sett.UnknownOps = unknownOps

	return SettingsResult{
		ObjectID:  objectID,
		Settings:  sett,
		UpdatedAt: time.Unix(updatedAt, 0).UTC(),
	}, nil
}

// hasTable reports whether a generated table with the given name currently
// exists — Settings() is called before any op has ever been folded (fresh
// Open, no ApplySchema yet), and must return defaults rather than error on
// a missing table.
func (d *DB) hasTable(name string) (bool, error) {
	var n int
	err := d.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("projection: check table %s: %w", name, err)
	}
	return n > 0, nil
}

// nullOr returns v's string if the register was ever written, or def — the
// typed reader's stand-in for a register that has never been set, distinct
// from one explicitly set to "".
func nullOr(v sql.NullString, def string) string {
	if v.Valid {
		return v.String
	}
	return def
}

// nullIntOr is nullOr's integer counterpart.
func nullIntOr(v sql.NullInt64, def int64) int64 {
	if v.Valid {
		return v.Int64
	}
	return def
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
