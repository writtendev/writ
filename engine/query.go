package writ

import (
	"context"
	"fmt"

	"github.com/writtendev/writ/engine/projection"
)

// Re-exported filter, ordering, and grouping types.
type (
	// ObjectFilter specifies filter criteria when querying collaborative objects cross-type.
	ObjectFilter = projection.ObjectFilter

	// OrderBy specifies the sort order for query results.
	OrderBy = projection.OrderBy

	// ObjectResult represents summary metadata for any collaborative object cross-type.
	ObjectResult = projection.ObjectResult

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
)

// Query provides read queries over collaborative objects, served from the projection SQLite cache.
type Query struct {
	store *Store
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
