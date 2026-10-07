package writ

import (
	"context"
	"fmt"
	"time"

	"github.com/writtendev/writ/internal/projection"
)

// ObjectFilter specifies filter criteria when querying collaborative objects cross-type.
type ObjectFilter struct {
	Type           []string
	Author         []string
	Text           string
	IncludeDeleted bool
	OrderBy        OrderBy
	Limit          int
	Offset         int
}

// OrderBy specifies the sort order for query results.
type OrderBy string

const (
	// OrderByCreatedAtAsc sorts results by created_at ascending.
	OrderByCreatedAtAsc OrderBy = "created_at_asc"

	// OrderByCreatedAtDesc sorts results by created_at descending.
	OrderByCreatedAtDesc OrderBy = "created_at_desc"

	// OrderByUpdatedAtAsc sorts results by updated_at ascending.
	OrderByUpdatedAtAsc OrderBy = "updated_at_asc"

	// OrderByUpdatedAtDesc sorts results by updated_at descending.
	OrderByUpdatedAtDesc OrderBy = "updated_at_desc"
)

// ObjectResult represents summary metadata for any collaborative object cross-type.
type ObjectResult struct {
	ObjectID     string    `json:"object_id"`
	ObjectType   string    `json:"object_type"`
	Author       Author    `json:"author"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	OpCount      int       `json:"op_count"`
	Verification string    `json:"verification"`
}

// Author holds the author display name and email address derived from an object's operations.
type Author struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// RefreshStats reports the work performed during a Refresh pass.
type RefreshStats struct {
	OpsDecoded      int            `json:"ops_decoded"`
	ObjectsTouched  int            `json:"objects_touched"`
	AnchorsResolved int            `json:"anchors_resolved"`
	Rebuilt         bool           `json:"rebuilt"`
	Changed         []ObjectChange `json:"changed,omitempty"`
	Rejections      []Rejection    `json:"rejections,omitempty"`
}

// ObjectChange describes the modifications made to a collaborative object in an incremental refresh batch.
type ObjectChange struct {
	ObjectID   string   `json:"object_id"`
	ObjectType string   `json:"object_type"`
	OpTypes    []string `json:"op_types"`
	Created    bool     `json:"created"`
}

// Rejection records an op commit that Refresh or Rebuild could not
// accept, as reported by RefreshStats.Rejections — either because it
// failed reader validation, or because an object it references was
// not present in this clone; see RejectObjectUnavailable for the two
// shapes that takes. Reason distinguishes which. It carries no commit SHA:
// git plumbing does not reach callers.
type Rejection struct {
	Reason RejectReason `json:"reason"`
	Err    string       `json:"error,omitempty"`
}

// RejectReason is the machine-readable reason a Rejection carries.
type RejectReason string

const (
	// RejectTreeShape reports an op commit whose root tree is malformed.
	RejectTreeShape RejectReason = "tree-shape"

	// RejectPayloadTooLarge reports an op.json blob exceeding MaxPayloadBytes.
	RejectPayloadTooLarge RejectReason = "payload-too-large"

	// RejectNonCanonicalPayload reports an op.json blob that is not byte-for-byte canonical JSON.
	RejectNonCanonicalPayload RejectReason = "non-canonical-payload"

	// RejectSchemaViolation reports an op body that violates schema rules.
	RejectSchemaViolation RejectReason = "schema-violation"

	// RejectCommitterMismatch reports a commit whose committer does not match its author.
	RejectCommitterMismatch RejectReason = "committer-mismatch"

	// RejectCommitTooLarge reports an op commit exceeding MaxCommitBytes.
	RejectCommitTooLarge RejectReason = "commit-too-large"

	// RejectTreeTooLarge reports an op root tree exceeding MaxTreeBytes.
	RejectTreeTooLarge RejectReason = "tree-too-large"

	// RejectObjectUnavailable reports that an op-commit chain references
	// an object absent from this clone: a tree or op.json blob missing
	// from this clone's object store — most commonly one withheld by a
	// partial clone's fetch filter — or a commit missing from this
	// clone's object store (no filter produces that — a filter withholds
	// blobs and trees, not commits). It is engine-local, not part of
	// spec/op-envelope.md's closed reader-validation rejection set.
	RejectObjectUnavailable RejectReason = "object-unavailable"
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
	if err := q.store.checkClosed(); err != nil {
		return nil, err
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return nil, err
	}
	res, err := q.store.projection.Objects(toProjectionFilter(f))
	if err != nil {
		return nil, err
	}
	return fromProjectionResults(res), nil
}

// Object fetches summary metadata for a single collaborative object by its ID, returning ErrNotFound if not found.
// Verification is reported on the result, not enforced; see ObjectResult.Verification.
func (q *Query) Object(id string) (ObjectResult, error) {
	if q == nil || q.store == nil {
		return ObjectResult{}, fmt.Errorf("writ: store is nil")
	}
	if err := q.store.checkClosed(); err != nil {
		return ObjectResult{}, err
	}
	if err := q.store.maybeAutoRefresh(context.Background()); err != nil {
		return ObjectResult{}, err
	}
	r, err := q.store.projection.Object(id)
	if err != nil {
		return ObjectResult{}, err
	}
	return fromProjectionResult(r), nil
}

func toProjectionFilter(f ObjectFilter) projection.ObjectFilter {
	return projection.ObjectFilter{
		Type:           f.Type,
		Author:         f.Author,
		Text:           f.Text,
		IncludeDeleted: f.IncludeDeleted,
		OrderBy:        projection.OrderBy(f.OrderBy),
		Limit:          f.Limit,
		Offset:         f.Offset,
	}
}

func fromProjectionResult(r projection.ObjectResult) ObjectResult {
	return ObjectResult{
		ObjectID:   r.ObjectID,
		ObjectType: r.ObjectType,
		Author: Author{
			Name:  r.Author.Name,
			Email: r.Author.Email,
		},
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
		OpCount:      r.OpCount,
		Verification: r.Verification,
	}
}

func fromProjectionResults(res []projection.ObjectResult) []ObjectResult {
	if res == nil {
		return nil
	}
	out := make([]ObjectResult, len(res))
	for i, r := range res {
		out[i] = fromProjectionResult(r)
	}
	return out
}
