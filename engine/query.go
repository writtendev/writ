package writ

import (
	"context"
	"fmt"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/projection"
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

	// Rejection records an op commit that Refresh or Rebuild could not
	// accept, as reported by RefreshStats.Rejections — either because it
	// failed reader validation, or because an object it references was
	// not present in this clone; see RejectObjectUnavailable for the two
	// shapes that takes. Reason distinguishes which.
	Rejection = dag.Rejection

	// RejectReason is the machine-readable reason a Rejection carries.
	RejectReason = codec.RejectReason
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

	// RejectObjectUnavailable reports that an op-commit chain references
	// an object absent from this clone: a tree or op.json blob missing
	// from this clone's object store — most commonly one withheld by a
	// partial clone's fetch filter — or a commit missing from this
	// clone's object store (no filter produces that — a filter withholds
	// blobs and trees, not commits). It is engine-local, not part of
	// spec/op-envelope.md's closed reader-validation rejection set.
	RejectObjectUnavailable = dag.RejectObjectUnavailable
)

// Query provides read queries over collaborative objects, served from the projection SQLite cache.
type Query struct {
	store *Store
}

// Objects executes a cross-type summary query over collaborative objects.
// Results include objects whose ops did not verify; ObjectResult.Verification
// reports the outcome, nothing here filters on it — see the engine package
// doc's trust model.
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
// Verification is reported on the result, not enforced; see ObjectResult.Verification.
func (q *Query) Object(id string) (ObjectResult, error) {
	if q == nil || q.store == nil {
		return ObjectResult{}, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return ObjectResult{}, err
	}
	return q.store.projection.Object(id)
}
