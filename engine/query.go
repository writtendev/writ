package writ

import (
	"context"
	"fmt"

	"github.com/writtendev/writ/engine/projection"
)

// Re-exported filter, ordering, and grouping types.
type (
	// ReviewFilter specifies filter criteria when querying reviews.
	ReviewFilter = projection.ReviewFilter

	// IssueFilter specifies filter criteria when querying issues.
	IssueFilter = projection.IssueFilter

	// CommentFilter specifies filter criteria when querying comments.
	CommentFilter = projection.CommentFilter

	// ObjectFilter specifies filter criteria when querying collaborative objects cross-type.
	ObjectFilter = projection.ObjectFilter

	// OrderBy specifies the sort order for query results.
	OrderBy = projection.OrderBy

	// ReviewResult represents a code review object along with its authorship and timestamps.
	ReviewResult = projection.ReviewResult

	// IssueResult represents an issue object along with its authorship and timestamps.
	IssueResult = projection.IssueResult

	// WorkflowStateFilter specifies filter criteria when querying workflow states.
	WorkflowStateFilter = projection.WorkflowStateFilter

	// WorkflowStateResult represents a workflow state object along with its authorship and timestamps.
	WorkflowStateResult = projection.WorkflowStateResult

	// LabelFilter specifies filter criteria when querying labels.
	LabelFilter = projection.LabelFilter

	// LabelResult represents a label object along with its authorship and timestamps.
	LabelResult = projection.LabelResult

	// DocumentFilter specifies filter criteria when querying documents.
	DocumentFilter = projection.DocumentFilter

	// DocumentResult represents a document object along with its authorship, timestamps, and ordered sections.
	DocumentResult = projection.DocumentResult

	// SectionResult represents a document section object along with its authorship and timestamps.
	SectionResult = projection.SectionResult

	// SettingsResult represents the repository settings along with its object ID and timestamps.
	SettingsResult = projection.SettingsResult

	// CommentResult represents a comment object along with its authorship, timestamps, and anchor resolutions.
	CommentResult = projection.CommentResult

	// ObjectResult represents summary metadata for any collaborative object cross-type.
	ObjectResult = projection.ObjectResult

	// ResolvedPosition describes the resolved anchor position for a comment side.
	ResolvedPosition = projection.ResolvedPosition

	// Author holds the author display name and email address derived from an object's operations.
	Author = projection.Author

	// RefreshStats reports the work performed during a Refresh pass.
	RefreshStats = projection.Stats

	// ObjectChange describes the modifications made to a collaborative object in an incremental refresh batch.
	ObjectChange = projection.ObjectChange
)

const (
	// OrderByCreatedAtAsc sorts results by created_at ascending.
	OrderByCreatedAtAsc = projection.OrderByCreatedAtAsc

	// OrderByCreatedAtDesc sorts results by created_at descending.
	OrderByCreatedAtDesc = projection.OrderByCreatedAtDesc

	// OrderByUpdatedAtAsc sorts results by updated_at ascending.
	OrderByUpdatedAtAsc = projection.OrderByUpdatedAtAsc

	// OrderByUpdatedAtDesc sorts results by updated_at descending.
	OrderByUpdatedAtDesc = projection.OrderByUpdatedAtDesc

	// OrderByTitleAsc sorts results by title ascending.
	OrderByTitleAsc = projection.OrderByTitleAsc

	// OrderByTitleDesc sorts results by title descending.
	OrderByTitleDesc = projection.OrderByTitleDesc

	// OrderByPriorityAsc sorts results by priority ascending (none < low < medium < high < urgent).
	OrderByPriorityAsc = projection.OrderByPriorityAsc

	// OrderByPriorityDesc sorts results by priority descending (urgent > high > medium > low > none).
	OrderByPriorityDesc = projection.OrderByPriorityDesc

	// OrderByPositionAsc sorts results by position ascending.
	OrderByPositionAsc = projection.OrderByPositionAsc

	// OrderByPositionDesc sorts results by position descending.
	OrderByPositionDesc = projection.OrderByPositionDesc

	// OrderByEstimateAsc sorts results by estimate ascending.
	OrderByEstimateAsc = projection.OrderByEstimateAsc

	// OrderByEstimateDesc sorts results by estimate descending.
	OrderByEstimateDesc = projection.OrderByEstimateDesc
)

// Query provides read queries over collaborative objects, served from the projection SQLite cache.
type Query struct {
	store *Store
}

// Reviews executes a list and filter query over code reviews.
func (q *Query) Reviews(f ReviewFilter) ([]ReviewResult, error) {
	if q == nil || q.store == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return nil, err
	}
	return q.store.projection.Reviews(f)
}

// Issues executes a list and filter query over issues.
func (q *Query) Issues(f IssueFilter) ([]IssueResult, error) {
	if q == nil || q.store == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return nil, err
	}
	return q.store.projection.Issues(f)
}

// Comments executes a list and filter query over comments.
func (q *Query) Comments(f CommentFilter) ([]CommentResult, error) {
	if q == nil || q.store == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return nil, err
	}
	return q.store.projection.Comments(f)
}

// Objects executes a cross-type summary query over collaborative objects.
func (q *Query) Objects(f ObjectFilter) ([]ObjectResult, error) {
	if q == nil || q.store == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return nil, err
	}
	return q.store.projection.Objects(f)
}

// Threads retrieves and structures all comments attached to a subject into a comment reply forest.
func (q *Query) Threads(subjectType, subjectID string) ([]CommentThread, error) {
	if q == nil || q.store == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return nil, err
	}
	return q.store.projection.Threads(subjectType, subjectID)
}

// Review fetches a single review by its object ID, returning ErrNotFound if not found.
func (q *Query) Review(id string) (ReviewResult, error) {
	if q == nil || q.store == nil {
		return ReviewResult{}, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return ReviewResult{}, err
	}
	return q.store.projection.Review(id)
}

// Issue fetches a single issue by its object ID, returning ErrNotFound if not found.
func (q *Query) Issue(id string) (IssueResult, error) {
	if q == nil || q.store == nil {
		return IssueResult{}, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return IssueResult{}, err
	}
	return q.store.projection.Issue(id)
}

// Object fetches summary metadata for a single collaborative object by its ID, returning ErrNotFound if not found.
func (q *Query) Object(id string) (ObjectResult, error) {
	if q == nil || q.store == nil {
		return ObjectResult{}, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return ObjectResult{}, err
	}
	return q.store.projection.Object(id)
}

// WorkflowStates executes a list and filter query over workflow states.
func (q *Query) WorkflowStates(f WorkflowStateFilter) ([]WorkflowStateResult, error) {
	if q == nil || q.store == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return nil, err
	}
	return q.store.projection.WorkflowStates(f)
}

// WorkflowState fetches a single workflow state by its object ID, returning ErrNotFound if not found.
func (q *Query) WorkflowState(id string) (WorkflowStateResult, error) {
	if q == nil || q.store == nil {
		return WorkflowStateResult{}, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return WorkflowStateResult{}, err
	}
	return q.store.projection.WorkflowState(id)
}

// Labels executes a list and filter query over labels.
func (q *Query) Labels(f LabelFilter) ([]LabelResult, error) {
	if q == nil || q.store == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return nil, err
	}
	return q.store.projection.Labels(f)
}

// Label fetches a single label by its object ID, returning ErrNotFound if not found.
func (q *Query) Label(id string) (LabelResult, error) {
	if q == nil || q.store == nil {
		return LabelResult{}, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return LabelResult{}, err
	}
	return q.store.projection.Label(id)
}

// Documents executes a list and filter query over documents.
func (q *Query) Documents(f DocumentFilter) ([]DocumentResult, error) {
	if q == nil || q.store == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return nil, err
	}
	return q.store.projection.Documents(f)
}

// Document fetches a single document by its object ID, returning ErrNotFound if not found.
func (q *Query) Document(id string) (DocumentResult, error) {
	if q == nil || q.store == nil {
		return DocumentResult{}, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return DocumentResult{}, err
	}
	return q.store.projection.Document(id)
}

// Section fetches a single document section by its object ID, returning ErrNotFound if not found.
func (q *Query) Section(id string) (SectionResult, error) {
	if q == nil || q.store == nil {
		return SectionResult{}, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return SectionResult{}, err
	}
	return q.store.projection.Section(id)
}

// Settings returns the current repository settings.
func (q *Query) Settings() (SettingsResult, error) {
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return SettingsResult{}, err
	}
	return q.store.projection.Settings()
}
