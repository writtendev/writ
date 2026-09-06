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

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT r.object_id, r.title, r.description, r.status, r.merge_commit, r.reason, ")
	sb.WriteString("o.author_name, o.author_email, o.created_at, o.updated_at ")
	sb.WriteString("FROM reviews r JOIN objects o ON o.object_id = r.object_id WHERE 1=1")

	if len(f.Status) > 0 {
		sb.WriteString(" AND r.status IN (" + placeholders(len(f.Status)) + ")")
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
		sb.WriteString(" AND EXISTS (SELECT 1 FROM review_assignees ra WHERE ra.review_object_id = r.object_id AND ra.assignee IN (" + placeholders(len(f.Assignee)) + "))")
		for _, a := range f.Assignee {
			args = append(args, state.NormalizePerson(a))
		}
	}

	if len(f.Label) > 0 {
		appendLabelFilter(&sb, &args, "review_labels", "rl", "review_object_id", "r.object_id", f.Label)
	}

	if f.Text != "" {
		sb.WriteString(" AND (r.title LIKE ? ESCAPE '\\' OR r.description LIKE ? ESCAPE '\\')")
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
		sb.WriteString(" ORDER BY r.title ASC, r.object_id ASC")
	case OrderByTitleDesc:
		sb.WriteString(" ORDER BY r.title DESC, r.object_id DESC")
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

	// Batch load revisions
	revisionsMap := make(map[string][]state.Revision)
	revRows, err := d.queryIn("SELECT review_object_id, base, head FROM review_revisions WHERE review_object_id IN (?) ORDER BY review_object_id ASC, revision_index ASC", objectIDs)
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
	asRows, err := d.queryIn("SELECT review_object_id, assignee FROM review_assignees WHERE review_object_id IN (?) ORDER BY review_object_id ASC, assignee ASC", objectIDs)
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
	lblRows, err := d.queryIn("SELECT review_object_id, label FROM review_labels WHERE review_object_id IN (?) ORDER BY review_object_id ASC, label ASC", objectIDs)
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
	lnkRows, err := d.queryIn("SELECT review_object_id, target, target_type, relation FROM review_links WHERE review_object_id IN (?) ORDER BY review_object_id ASC, target ASC", objectIDs)
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
	appRows, err := d.queryIn("SELECT review_object_id, subject, revision, verdict, message FROM approvals WHERE review_object_id IN (?) ORDER BY review_object_id ASC, subject ASC, revision ASC", objectIDs)
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
	ciRows, err := d.queryIn("SELECT review_object_id, revision, name, state, url, description, started_at, completed_at, external_id FROM ci_statuses WHERE review_object_id IN (?) ORDER BY review_object_id ASC, revision ASC, name ASC", objectIDs)
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
	unknownMap := make(map[string][]state.UnknownOp)
	uRows, err := d.queryIn("SELECT object_id, op_id, object_type, op_type, op_version FROM unknown_ops WHERE object_id IN (?) ORDER BY object_id ASC, op_index ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query unknown_ops: %w", err)
	}
	for uRows.Next() {
		var objID, opID, objType, opType string
		var opVersion int64
		if err := uRows.Scan(&objID, &opID, &objType, &opType, &opVersion); err != nil {
			uRows.Close()
			return nil, fmt.Errorf("projection: scan unknown op: %w", err)
		}
		unknownMap[objID] = append(unknownMap[objID], state.UnknownOp{
			Commit:     opID,
			ObjectType: objType,
			OpType:     opType,
			OpVersion:  opVersion,
		})
	}
	uRows.Close()

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

// Issues executes a list and filter query over issues.
func (d *DB) Issues(f IssueFilter) ([]IssueResult, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT i.object_id, i.title, i.description, i.state, i.reason, i.priority, i.estimate, i.position, ")
	sb.WriteString("o.author_name, o.author_email, o.created_at, o.updated_at ")
	sb.WriteString("FROM issues i JOIN objects o ON o.object_id = i.object_id WHERE 1=1")

	if len(f.State) > 0 {
		sb.WriteString(" AND (")
		for idx, s := range f.State {
			if idx > 0 {
				sb.WriteString(" OR ")
			}
			sb.WriteString("(i.state = ? OR EXISTS (SELECT 1 FROM workflow_states ws WHERE ws.object_id = i.state AND (LOWER(ws.name) = LOWER(?) OR LOWER(ws.type) = LOWER(?) OR (LOWER(?) = 'closed' AND ws.type = 'completed') OR (LOWER(?) = 'open' AND ws.type IN ('unstarted', 'backlog')))))")
			args = append(args, s, s, s, s, s)
		}
		sb.WriteString(")")
	}

	if len(f.Priority) > 0 {
		sb.WriteString(" AND i.priority IN (" + placeholders(len(f.Priority)) + ")")
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
		sb.WriteString(" AND EXISTS (SELECT 1 FROM issue_assignees ia WHERE ia.issue_object_id = i.object_id AND ia.assignee IN (" + placeholders(len(f.Assignee)) + "))")
		for _, a := range f.Assignee {
			args = append(args, state.NormalizePerson(a))
		}
	}

	if len(f.Label) > 0 {
		appendLabelFilter(&sb, &args, "issue_labels", "il", "issue_object_id", "i.object_id", f.Label)
	}

	if f.Text != "" {
		sb.WriteString(" AND (i.title LIKE ? ESCAPE '\\' OR i.description LIKE ? ESCAPE '\\')")
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
		sb.WriteString(" ORDER BY i.title ASC, i.object_id ASC")
	case OrderByTitleDesc:
		sb.WriteString(" ORDER BY i.title DESC, i.object_id DESC")
	case OrderByPriorityAsc:
		sb.WriteString(" ORDER BY CASE WHEN i.priority = 0 THEN 0 ELSE 5 - i.priority END ASC, i.position ASC, o.created_at ASC, i.object_id ASC")
	case OrderByPriorityDesc:
		sb.WriteString(" ORDER BY CASE WHEN i.priority = 0 THEN 5 ELSE i.priority END ASC, i.position ASC, o.created_at ASC, i.object_id ASC")
	case OrderByPositionAsc:
		sb.WriteString(" ORDER BY i.position ASC, i.position_op_id ASC, o.created_at ASC, i.object_id ASC")
	case OrderByPositionDesc:
		sb.WriteString(" ORDER BY i.position DESC, i.position_op_id DESC, o.created_at DESC, i.object_id DESC")
	case OrderByEstimateAsc:
		sb.WriteString(" ORDER BY CASE WHEN i.estimate IS NULL THEN 1 ELSE 0 END, i.estimate ASC, i.position ASC, o.created_at ASC, i.object_id ASC")
	case OrderByEstimateDesc:
		sb.WriteString(" ORDER BY CASE WHEN i.estimate IS NULL THEN 1 ELSE 0 END, i.estimate DESC, i.position ASC, o.created_at ASC, i.object_id ASC")
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
	asRows, err := d.queryIn("SELECT issue_object_id, assignee FROM issue_assignees WHERE issue_object_id IN (?) ORDER BY issue_object_id ASC, assignee ASC", objectIDs)
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
	lblRows, err := d.queryIn("SELECT issue_object_id, label FROM issue_labels WHERE issue_object_id IN (?) ORDER BY issue_object_id ASC, label ASC", objectIDs)
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
	lnkRows, err := d.queryIn("SELECT issue_object_id, target, target_type, relation FROM issue_links WHERE issue_object_id IN (?) ORDER BY issue_object_id ASC, target ASC", objectIDs)
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

	// Batch load unknown_ops
	unknownMap := make(map[string][]state.UnknownOp)
	uRows, err := d.queryIn("SELECT object_id, op_id, object_type, op_type, op_version FROM unknown_ops WHERE object_id IN (?) ORDER BY object_id ASC, op_index ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query unknown_ops: %w", err)
	}
	for uRows.Next() {
		var objID, opID, objType, opType string
		var opVersion int64
		if err := uRows.Scan(&objID, &opID, &objType, &opType, &opVersion); err != nil {
			uRows.Close()
			return nil, fmt.Errorf("projection: scan unknown op: %w", err)
		}
		unknownMap[objID] = append(unknownMap[objID], state.UnknownOp{
			Commit:     opID,
			ObjectType: objType,
			OpType:     opType,
			OpVersion:  opVersion,
		})
	}
	uRows.Close()

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

// Comments executes a list and filter query over comments.
func (d *DB) Comments(f CommentFilter) ([]CommentResult, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT c.object_id, c.subject_type, c.subject_id, c.text, c.in_reply_to, c.anchor, c.deleted, c.resolved, c.resolved_by, ")
	sb.WriteString("o.author_name, o.author_email, o.created_at, o.updated_at ")
	sb.WriteString("FROM comments c JOIN objects o ON o.object_id = c.object_id WHERE 1=1")

	if f.SubjectType != "" {
		sb.WriteString(" AND c.subject_type = ?")
		args = append(args, f.SubjectType)
	}
	if f.SubjectID != "" {
		sb.WriteString(" AND c.subject_id = ?")
		args = append(args, f.SubjectID)
	}

	if f.Resolved != nil {
		if *f.Resolved {
			sb.WriteString(" AND c.resolved = 1")
		} else {
			sb.WriteString(" AND (c.resolved = 0 OR c.resolved IS NULL)")
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
		sb.WriteString(" AND c.text LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(f.Text)+"%")
	}

	if !f.IncludeDeleted {
		sb.WriteString(" AND c.deleted = 0")
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

	// Batch load unknown_ops
	unknownMap := make(map[string][]state.UnknownOp)
	uRows, err := d.queryIn("SELECT object_id, op_id, object_type, op_type, op_version FROM unknown_ops WHERE object_id IN (?) ORDER BY object_id ASC, op_index ASC", objectIDs)
	if err != nil {
		return nil, fmt.Errorf("projection: query unknown_ops: %w", err)
	}
	for uRows.Next() {
		var objID, opID, objType, opType string
		var opVersion int64
		if err := uRows.Scan(&objID, &opID, &objType, &opType, &opVersion); err != nil {
			uRows.Close()
			return nil, fmt.Errorf("projection: scan unknown op: %w", err)
		}
		unknownMap[objID] = append(unknownMap[objID], state.UnknownOp{
			Commit:     opID,
			ObjectType: objType,
			OpType:     opType,
			OpVersion:  opVersion,
		})
	}
	uRows.Close()

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
		resQuery = "SELECT comment_object_id, side, outcome, match, path, start_line, end_line, reason FROM anchor_resolutions WHERE comment_object_id IN (?) AND target_commit = ? ORDER BY comment_object_id ASC, side ASC"
		extraArgs = []any{targetCommit}
	} else {
		resQuery = "SELECT comment_object_id, side, outcome, match, path, start_line, end_line, reason FROM anchor_resolutions WHERE comment_object_id IN (?) ORDER BY comment_object_id ASC, side ASC"
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

// Objects executes a cross-type summary query over collaborative objects.
func (d *DB) Objects(f ObjectFilter) ([]ObjectResult, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("projection: database is closed")
	}

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
		escaped := "%" + escapeLike(f.Text) + "%"
		sb.WriteString(" AND (")
		sb.WriteString("EXISTS (SELECT 1 FROM reviews r WHERE r.object_id = o.object_id AND (r.title LIKE ? ESCAPE '\\' OR r.description LIKE ? ESCAPE '\\'))")
		sb.WriteString(" OR EXISTS (SELECT 1 FROM issues i WHERE i.object_id = o.object_id AND (i.title LIKE ? ESCAPE '\\' OR i.description LIKE ? ESCAPE '\\'))")
		sb.WriteString(" OR EXISTS (SELECT 1 FROM comments c WHERE c.object_id = o.object_id AND c.text LIKE ? ESCAPE '\\')")
		sb.WriteString(" OR EXISTS (SELECT 1 FROM projects p WHERE p.object_id = o.object_id AND (p.title LIKE ? ESCAPE '\\' OR p.description LIKE ? ESCAPE '\\'))")
		sb.WriteString(" OR EXISTS (SELECT 1 FROM cycles cy WHERE cy.object_id = o.object_id AND (cy.title LIKE ? ESCAPE '\\' OR cy.description LIKE ? ESCAPE '\\'))")
		sb.WriteString(")")
		args = append(args, escaped, escaped, escaped, escaped, escaped, escaped, escaped, escaped, escaped)
	}

	if !f.IncludeDeleted {
		sb.WriteString(" AND (o.object_type != 'comment' OR EXISTS (SELECT 1 FROM comments c WHERE c.object_id = o.object_id AND c.deleted = 0))")
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
		"SELECT r.object_id, r.title, r.description, r.status, r.merge_commit, r.reason, o.author_name, o.author_email, o.created_at, o.updated_at FROM reviews r JOIN objects o ON o.object_id = r.object_id WHERE r.object_id = ?",
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

	var revisions []state.Revision
	revRows, err := d.db.Query("SELECT base, head FROM review_revisions WHERE review_object_id = ? ORDER BY revision_index ASC", objectID)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("projection: query review revisions: %w", err)
	}
	defer revRows.Close()
	for revRows.Next() {
		var base, head string
		if err := revRows.Scan(&base, &head); err != nil {
			return ReviewResult{}, fmt.Errorf("projection: scan review revision: %w", err)
		}
		revisions = append(revisions, state.Revision{Base: base, Head: head})
	}
	if err := revRows.Err(); err != nil {
		return ReviewResult{}, fmt.Errorf("projection: iterate review revisions: %w", err)
	}

	var assignees []string
	asRows, err := d.db.Query("SELECT assignee FROM review_assignees WHERE review_object_id = ? ORDER BY assignee ASC", objectID)
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
	lblRows, err := d.db.Query("SELECT label FROM review_labels WHERE review_object_id = ? ORDER BY label ASC", objectID)
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
	lnkRows, err := d.db.Query("SELECT target, target_type, relation FROM review_links WHERE review_object_id = ? ORDER BY target ASC", objectID)
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
	appRows, err := d.db.Query("SELECT subject, revision, verdict, message FROM approvals WHERE review_object_id = ? ORDER BY subject ASC, revision ASC", objectID)
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
	ciRows, err := d.db.Query("SELECT revision, name, state, url, description, started_at, completed_at, external_id FROM ci_statuses WHERE review_object_id = ? ORDER BY revision ASC, name ASC", objectID)
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

	var unknownOps []state.UnknownOp
	uRows, err := d.db.Query("SELECT op_id, object_type, op_type, op_version FROM unknown_ops WHERE object_id = ? ORDER BY op_index ASC", objectID)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("projection: query unknown_ops: %w", err)
	}
	defer uRows.Close()
	for uRows.Next() {
		var opID, objType, opType string
		var opVersion int64
		if err := uRows.Scan(&opID, &objType, &opType, &opVersion); err != nil {
			return ReviewResult{}, fmt.Errorf("projection: scan unknown op: %w", err)
		}
		unknownOps = append(unknownOps, state.UnknownOp{
			Commit:     opID,
			ObjectType: objType,
			OpType:     opType,
			OpVersion:  opVersion,
		})
	}
	if err := uRows.Err(); err != nil {
		return ReviewResult{}, fmt.Errorf("projection: iterate unknown_ops: %w", err)
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
		"SELECT i.object_id, i.title, i.description, i.state, i.reason, i.priority, i.estimate, i.position, o.author_name, o.author_email, o.created_at, o.updated_at FROM issues i JOIN objects o ON o.object_id = i.object_id WHERE i.object_id = ?",
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
	asRows, err := d.db.Query("SELECT assignee FROM issue_assignees WHERE issue_object_id = ? ORDER BY assignee ASC", objectID)
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
	lblRows, err := d.db.Query("SELECT label FROM issue_labels WHERE issue_object_id = ? ORDER BY label ASC", objectID)
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
	lnkRows, err := d.db.Query("SELECT target, target_type, relation FROM issue_links WHERE issue_object_id = ? ORDER BY target ASC", objectID)
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

	var unknownOps []state.UnknownOp
	uRows, err := d.db.Query("SELECT op_id, object_type, op_type, op_version FROM unknown_ops WHERE object_id = ? ORDER BY op_index ASC", objectID)
	if err != nil {
		return IssueResult{}, fmt.Errorf("projection: query unknown_ops: %w", err)
	}
	defer uRows.Close()
	for uRows.Next() {
		var opID, objType, opType string
		var opVersion int64
		if err := uRows.Scan(&opID, &objType, &opType, &opVersion); err != nil {
			return IssueResult{}, fmt.Errorf("projection: scan unknown op: %w", err)
		}
		unknownOps = append(unknownOps, state.UnknownOp{
			Commit:     opID,
			ObjectType: objType,
			OpType:     opType,
			OpVersion:  opVersion,
		})
	}
	if err := uRows.Err(); err != nil {
		return IssueResult{}, fmt.Errorf("projection: iterate unknown_ops: %w", err)
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

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT ws.object_id, ws.name, ws.type, ws.position, ws.color, ws.description, ")
	sb.WriteString("o.author_name, o.author_email, o.created_at, o.updated_at, o.last_op_id ")
	sb.WriteString("FROM workflow_states ws JOIN objects o ON o.object_id = ws.object_id WHERE 1=1")

	if len(f.Type) > 0 {
		sb.WriteString(" AND ws.type IN (" + placeholders(len(f.Type)) + ")")
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
		sb.WriteString(" ORDER BY ws.name ASC, ws.object_id ASC")
	case OrderByTitleDesc:
		sb.WriteString(" ORDER BY ws.name DESC, ws.object_id DESC")
	default:
		sb.WriteString(" ORDER BY ws.position ASC, ws.op_id ASC")
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

	unknownMap := make(map[string][]state.UnknownOp)
	if len(objectIDs) > 0 {
		uRows, err := d.queryIn("SELECT object_id, op_id, object_type, op_type, op_version FROM unknown_ops WHERE object_id IN (?) ORDER BY object_id ASC, op_index ASC", objectIDs)
		if err != nil {
			return nil, fmt.Errorf("projection: query unknown_ops: %w", err)
		}
		for uRows.Next() {
			var objID, opID, objType, opType string
			var opVersion int64
			if err := uRows.Scan(&objID, &opID, &objType, &opType, &opVersion); err != nil {
				uRows.Close()
				return nil, fmt.Errorf("projection: scan unknown op: %w", err)
			}
			unknownMap[objID] = append(unknownMap[objID], state.UnknownOp{
				Commit:     opID,
				ObjectType: objType,
				OpType:     opType,
				OpVersion:  opVersion,
			})
		}
		uRows.Close()
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

	query := "SELECT ws.object_id, ws.name, ws.type, ws.position, ws.color, ws.description, " +
		"o.author_name, o.author_email, o.created_at, o.updated_at, o.last_op_id " +
		"FROM workflow_states ws JOIN objects o ON o.object_id = ws.object_id WHERE ws.object_id = ?"

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

	var unknownOps []state.UnknownOp
	uRows, err := d.db.Query("SELECT op_id, object_type, op_type, op_version FROM unknown_ops WHERE object_id = ? ORDER BY op_index ASC", id)
	if err != nil {
		return WorkflowStateResult{}, fmt.Errorf("projection: query unknown_ops: %w", err)
	}
	defer uRows.Close()
	for uRows.Next() {
		var opID, objType, opType string
		var opVersion int64
		if err := uRows.Scan(&opID, &objType, &opType, &opVersion); err != nil {
			return WorkflowStateResult{}, fmt.Errorf("projection: scan unknown op: %w", err)
		}
		unknownOps = append(unknownOps, state.UnknownOp{
			Commit:     opID,
			ObjectType: objType,
			OpType:     opType,
			OpVersion:  opVersion,
		})
	}
	if err := uRows.Err(); err != nil {
		return WorkflowStateResult{}, fmt.Errorf("projection: iterate unknown_ops: %w", err)
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

	var sb strings.Builder
	var args []any

	sb.WriteString("SELECT l.object_id, l.name, l.color, l.description, ")
	sb.WriteString("l.author_name, l.author_email, l.created_at, l.updated_at ")
	sb.WriteString("FROM labels l WHERE 1=1")

	switch f.OrderBy {
	case OrderByCreatedAtAsc:
		sb.WriteString(" ORDER BY l.created_at ASC, l.object_id ASC")
	case OrderByCreatedAtDesc:
		sb.WriteString(" ORDER BY l.created_at DESC, l.object_id DESC")
	case OrderByUpdatedAtAsc:
		sb.WriteString(" ORDER BY l.updated_at ASC, l.object_id ASC")
	case OrderByUpdatedAtDesc:
		sb.WriteString(" ORDER BY l.updated_at DESC, l.object_id DESC")
	case OrderByTitleAsc:
		sb.WriteString(" ORDER BY l.name ASC, l.object_id ASC")
	case OrderByTitleDesc:
		sb.WriteString(" ORDER BY l.name DESC, l.object_id DESC")
	default:
		sb.WriteString(" ORDER BY LOWER(l.name) ASC, l.object_id ASC")
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

	unknownMap := make(map[string][]state.UnknownOp)
	if len(objectIDs) > 0 {
		uRows, err := d.queryIn("SELECT object_id, op_id, object_type, op_type, op_version FROM unknown_ops WHERE object_id IN (?) ORDER BY object_id ASC, op_index ASC", objectIDs)
		if err != nil {
			return nil, fmt.Errorf("projection: query unknown_ops: %w", err)
		}
		for uRows.Next() {
			var objID, opID, objType, opType string
			var opVersion int64
			if err := uRows.Scan(&objID, &opID, &objType, &opType, &opVersion); err != nil {
				uRows.Close()
				return nil, fmt.Errorf("projection: scan unknown op: %w", err)
			}
			unknownMap[objID] = append(unknownMap[objID], state.UnknownOp{
				Commit:     opID,
				ObjectType: objType,
				OpType:     opType,
				OpVersion:  opVersion,
			})
		}
		uRows.Close()
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
		"SELECT object_id, name, color, description, author_name, author_email, created_at, updated_at FROM labels WHERE object_id = ?",
		id,
	).Scan(&objectID, &name, &color, &description, &authorName, &authorEmail, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LabelResult{}, ErrNotFound
		}
		return LabelResult{}, fmt.Errorf("projection: query label %s: %w", id, err)
	}

	uRows, err := d.db.Query(
		"SELECT op_id, object_type, op_type, op_version FROM unknown_ops WHERE object_id = ? ORDER BY op_index ASC",
		id,
	)
	if err != nil {
		return LabelResult{}, fmt.Errorf("projection: query unknown_ops: %w", err)
	}
	defer uRows.Close()

	var unknownOps []state.UnknownOp
	for uRows.Next() {
		var u state.UnknownOp
		if err := uRows.Scan(&u.Commit, &u.ObjectType, &u.OpType, &u.OpVersion); err != nil {
			return LabelResult{}, fmt.Errorf("projection: scan unknown op: %w", err)
		}
		unknownOps = append(unknownOps, u)
	}
	if err := uRows.Err(); err != nil {
		return LabelResult{}, fmt.Errorf("projection: iterate unknown_ops: %w", err)
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

	sb.WriteString(fmt.Sprintf(" AND EXISTS (SELECT 1 FROM %s %s WHERE %s.%s = %s AND (%s.label IN (%s) OR LOWER(%s.label) IN (%s) OR (LENGTH(%s.label) > 32 AND substr(%s.label, -32) IN (%s)) OR %s.label IN (SELECT object_id FROM labels WHERE LOWER(name) IN (%s)) OR (LENGTH(%s.label) > 32 AND substr(%s.label, -32) IN (SELECT object_id FROM labels WHERE LOWER(name) IN (%s))) OR LOWER(%s.label) IN (SELECT LOWER(name) FROM labels WHERE object_id IN (%s))))",
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
	var conditions []string
	var args []any

	if len(f.Labels) > 0 {
		placeholders := make([]string, len(f.Labels))
		for i, label := range f.Labels {
			placeholders[i] = "?"
			args = append(args, label)
		}
		conditions = append(conditions, fmt.Sprintf(
			"d.object_id IN (SELECT document_id FROM document_labels WHERE label IN (%s) GROUP BY document_id HAVING COUNT(DISTINCT label) = %d)",
			strings.Join(placeholders, ", "), len(f.Labels),
		))
	}

	query := "SELECT d.object_id, d.title, d.state_json, " +
		"o.author_name, o.author_email, o.created_at, o.updated_at " +
		"FROM documents d JOIN objects o ON o.object_id = d.object_id"

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
		stateJSON   string
		authorName  string
		authorEmail string
		createdAt   int64
		updatedAt   int64
	}

	var rawDocs []rawDoc
	var docIDs []string
	for rows.Next() {
		var rd rawDoc
		if err := rows.Scan(&rd.objectID, &rd.title, &rd.stateJSON, &rd.authorName, &rd.authorEmail, &rd.createdAt, &rd.updatedAt); err != nil {
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

	sectionsByDoc, err := d.loadSectionsForDocuments(docIDs)
	if err != nil {
		return nil, err
	}

	results := make([]DocumentResult, len(rawDocs))
	for i, rd := range rawDocs {
		var docState state.Document
		if err := json.Unmarshal([]byte(rd.stateJSON), &docState); err != nil {
			return nil, fmt.Errorf("projection: unmarshal document state for %s: %w", rd.objectID, err)
		}
		results[i] = DocumentResult{
			ObjectID: rd.objectID,
			Author: Author{
				Name:  rd.authorName,
				Email: rd.authorEmail,
			},
			CreatedAt: time.Unix(rd.createdAt, 0).UTC(),
			UpdatedAt: time.Unix(rd.updatedAt, 0).UTC(),
			Document:  docState,
			Sections:  sectionsByDoc[rd.objectID],
		}
	}
	return results, nil
}

func (d *DB) loadSectionsForDocuments(docIDs []string) (map[string][]SectionResult, error) {
	sectionsByDoc := make(map[string][]SectionResult)
	if len(docIDs) == 0 {
		return sectionsByDoc, nil
	}

	placeholders := make([]string, len(docIDs))
	args := make([]any, len(docIDs))
	for i, id := range docIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(
		"SELECT s.object_id, s.document_id, s.position, s.op_id, s.title, s.state_json, "+
			"o.author_name, o.author_email, o.created_at, o.updated_at "+
			"FROM sections s JOIN objects o ON o.object_id = s.object_id "+
			"WHERE s.document_id IN (%s) AND s.deleted = 0 "+
			"ORDER BY s.position ASC, s.op_id ASC",
		strings.Join(placeholders, ", "),
	)

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("projection: query sections: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var objID, docID, pos, opID, title, stateJSON, authorName, authorEmail string
		var createdAt, updatedAt int64
		if err := rows.Scan(&objID, &docID, &pos, &opID, &title, &stateJSON, &authorName, &authorEmail, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("projection: scan section: %w", err)
		}

		var secState state.Section
		if err := json.Unmarshal([]byte(stateJSON), &secState); err != nil {
			return nil, fmt.Errorf("projection: unmarshal section state for %s: %w", objID, err)
		}

		sectionsByDoc[docID] = append(sectionsByDoc[docID], SectionResult{
			ObjectID: objID,
			Author: Author{
				Name:  authorName,
				Email: authorEmail,
			},
			CreatedAt: time.Unix(createdAt, 0).UTC(),
			UpdatedAt: time.Unix(updatedAt, 0).UTC(),
			Section:   secState,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("projection: iterate sections: %w", err)
	}
	return sectionsByDoc, nil
}

// Document fetches a single document by its object ID, returning ErrNotFound if not found.
func (d *DB) Document(id string) (DocumentResult, error) {
	query := "SELECT d.object_id, d.title, d.state_json, " +
		"o.author_name, o.author_email, o.created_at, o.updated_at " +
		"FROM documents d JOIN objects o ON o.object_id = d.object_id WHERE d.object_id = ?"

	var objID, title, stateJSON, authorName, authorEmail string
	var createdAt, updatedAt int64
	err := d.db.QueryRow(query, id).Scan(
		&objID, &title, &stateJSON, &authorName, &authorEmail, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DocumentResult{}, ErrNotFound
		}
		return DocumentResult{}, fmt.Errorf("projection: query document %s: %w", id, err)
	}

	var docState state.Document
	if err := json.Unmarshal([]byte(stateJSON), &docState); err != nil {
		return DocumentResult{}, fmt.Errorf("projection: unmarshal document state for %s: %w", id, err)
	}

	sectionsByDoc, err := d.loadSectionsForDocuments([]string{id})
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
		Document:  docState,
		Sections:  sectionsByDoc[id],
	}, nil
}

// Section fetches a single document section by its object ID, returning ErrNotFound if not found.
func (d *DB) Section(id string) (SectionResult, error) {
	query := "SELECT s.object_id, s.document_id, s.position, s.op_id, s.title, s.state_json, " +
		"o.author_name, o.author_email, o.created_at, o.updated_at " +
		"FROM sections s JOIN objects o ON o.object_id = s.object_id WHERE s.object_id = ?"

	var objID, docID, pos, opID, title, stateJSON, authorName, authorEmail string
	var createdAt, updatedAt int64
	err := d.db.QueryRow(query, id).Scan(
		&objID, &docID, &pos, &opID, &title, &stateJSON, &authorName, &authorEmail, &createdAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SectionResult{}, ErrNotFound
		}
		return SectionResult{}, fmt.Errorf("projection: query section %s: %w", id, err)
	}

	var secState state.Section
	if err := json.Unmarshal([]byte(stateJSON), &secState); err != nil {
		return SectionResult{}, fmt.Errorf("projection: unmarshal section state for %s: %w", id, err)
	}

	return SectionResult{
		ObjectID: objID,
		Author: Author{
			Name:  authorName,
			Email: authorEmail,
		},
		CreatedAt: time.Unix(createdAt, 0).UTC(),
		UpdatedAt: time.Unix(updatedAt, 0).UTC(),
		Section:   secState,
	}, nil
}

// Settings queries the current workspace settings from the projection.
// If no settings operations have been written to the projection,
// it returns the default settings.
func (d *DB) Settings() (SettingsResult, error) {
	if d == nil || d.db == nil {
		return SettingsResult{}, fmt.Errorf("projection: database is closed")
	}

	var (
		objectID           string
		name               string
		identifier         string
		timezone           string
		estimateScale      string
		allowZeroInt       int
		cyclesEnabledInt   int
		cycleDurationWeeks int
		cycleStartDay      int
		cycleCooldownWeeks int
		triageEnabledInt   int
		unkJSON            string
		updatedAt          int64
	)

	row := d.db.QueryRow(
		"SELECT object_id, name, identifier, timezone, estimate_scale, allow_zero_estimates, cycles_enabled, cycle_duration_weeks, cycle_start_day, cycle_cooldown_weeks, triage_enabled, unknown_keys, updated_at FROM settings ORDER BY CASE WHEN object_id = ? THEN 0 ELSE 1 END, object_id ASC LIMIT 1",
		state.DefaultSettingsObjectID,
	)
	err := row.Scan(
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
	if unkJSON != "" {
		_ = json.Unmarshal([]byte(unkJSON), &unkKeys)
	}

	sett := state.Settings{
		ObjectID:           objectID,
		Name:               name,
		Identifier:         identifier,
		Timezone:           timezone,
		EstimateScale:      estimateScale,
		AllowZeroEstimates: allowZeroInt != 0,
		CyclesEnabled:      cyclesEnabledInt != 0,
		CycleDurationWeeks: cycleDurationWeeks,
		CycleStartDay:      cycleStartDay,
		CycleCooldownWeeks: cycleCooldownWeeks,
		TriageEnabled:      triageEnabledInt != 0,
		UnknownKeys:        unkKeys,
	}

	// Read unknown ops
	rows, err := d.db.Query(
		"SELECT op_id, object_type, op_type, op_version FROM unknown_ops WHERE object_id = ? ORDER BY op_index ASC",
		objectID,
	)
	if err != nil {
		return SettingsResult{}, fmt.Errorf("projection: query unknown_ops: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var u state.UnknownOp
		if err := rows.Scan(&u.Commit, &u.ObjectType, &u.OpType, &u.OpVersion); err != nil {
			return SettingsResult{}, fmt.Errorf("projection: scan unknown op: %w", err)
		}
		sett.UnknownOps = append(sett.UnknownOps, u)
	}
	if err := rows.Err(); err != nil {
		return SettingsResult{}, fmt.Errorf("projection: iterate unknown_ops: %w", err)
	}

	return SettingsResult{
		ObjectID:  objectID,
		Settings:  sett,
		UpdatedAt: time.Unix(updatedAt, 0).UTC(),
	}, nil
}
