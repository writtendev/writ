package projection_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/engine/state"
)

// makeReviewOp builds a codec.Op directly rather than through store.Append:
// op-envelope's review-ops.schema.json requires both "base" and "head" on a
// "revision" op, so store.Append's producer validation refuses the very
// body shape this test needs (a push writing only one of the two fields) —
// but that shape is exactly what a materializer has to survive, whether it
// reached the log from an older schema version, a different validator, or
// any other producer than this repo's own. codec.EncodePayload performs no
// schema validation of its own (only dag.Store.Append does), so building
// Raw through it and the rest of the op by hand, exactly as
// TestCollidingLogDeclaredTypeStaysOpenable does, reaches Refresh with no
// producer-validation gate to route around.
func makeReviewOp(id string, parents []string, opType string, body map[string]any, when time.Time) codec.Op {
	env := makeReviewEnv("rev-1", opType, 1, body)
	author := codec.Identity{Name: "Test Writer", Email: "writer@example.com", When: when}
	return codec.Op{
		Envelope:  env,
		ID:        id,
		Parents:   parents,
		Author:    author,
		Committer: author,
		Message:   "writ: " + opType + " review/rev-1\n",
	}
}

// TestReviewRevisionPairingWithOmittedField is WRIT-189 round 2's MAJOR-1
// finding: Reviews and Review used to zip two independent append child
// tables (o_review__base and o_review__head) back into Revision pairs by
// local list position — but state.FoldReview pairs them per "revision" op,
// appending one Revision entry per matching op regardless of which of
// "base" and "head" that particular op actually wrote. A push that writes
// only one field shifts every later per-field position out of step with
// its sibling field: the first push here writes only "base" (no "head" at
// all), so the old per-field zip paired this push's base with the SECOND
// push's head, and the second push's base with an empty head — mispairing
// both revisions while still reporting a plausible-looking count of 2, not
// an out-of-range crash, which is exactly the class of bug that would
// hide in an equality check that only counts rows.
//
// ddl.go's appendGroupPlan fixes this generically, by materializing every
// append-strategy target sharing a rule's (op_type, op_version) envelope
// through one shared child table (o_review__base_head) — one row per
// contributing op, written by writeAppendGroupRows in materialize.go — so
// the pairing is fixed at write time and a reader just reads rows in order.
// This test pins that against the one typed reducer, state.FoldReview,
// whose per-op semantics ddl.go's grouping has to reproduce: two pushes,
// the first omitting "head" entirely (not writing it as an empty string),
// must come back from both Review and Reviews exactly as FoldReview pairs
// them.
func TestReviewRevisionPairingWithOmittedField(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeReviewOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	// Omits "head" entirely: an absent field, not an empty-string write.
	opRev1 := makeReviewOp("op-revision-1", []string{"op-create-1"}, "revision", map[string]any{
		"base": "0000000000000000000000000000000000000001",
	}, base.Add(1*time.Second))
	opRev2 := makeReviewOp("op-revision-2", []string{"op-revision-1"}, "revision", map[string]any{
		"base": "0000000000000000000000000000000000000002",
		"head": "0000000000000000000000000000000000000003",
	}, base.Add(2*time.Second))

	ops := []codec.Op{opCreate, opRev1, opRev2}

	want, err := state.FoldReview(ops)
	if err != nil {
		t.Fatalf("state.FoldReview failed: %v", err)
	}
	wantRevisions := []state.Revision{
		{Base: "0000000000000000000000000000000000000001", Head: ""},
		{Base: "0000000000000000000000000000000000000002", Head: "0000000000000000000000000000000000000003"},
	}
	if !reflect.DeepEqual(want.Revisions, wantRevisions) {
		t.Fatalf("test setup: FoldReview's revisions = %+v, want %+v", want.Revisions, wantRevisions)
	}

	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	enumRes := &dag.EnumerateResult{
		Ops: map[string][]codec.Op{"rev-1": ops},
		Cursors: dag.CursorSet{
			"refs/writ/0123456789abcdef/review": "op-revision-2",
		},
		DecodedCommits: len(ops),
	}

	if _, err := db.Refresh(store, projection.WithSchema(testRules()), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	gotSingle, err := db.Review("rev-1")
	if err != nil {
		t.Fatalf("db.Review failed: %v", err)
	}
	if !reflect.DeepEqual(gotSingle.Review.Revisions, want.Revisions) {
		t.Fatalf("db.Review revisions = %+v, want (FoldReview) %+v", gotSingle.Review.Revisions, want.Revisions)
	}

	list, err := db.Reviews(projection.ReviewFilter{})
	if err != nil {
		t.Fatalf("db.Reviews failed: %v", err)
	}
	var found *projection.ReviewResult
	for i := range list {
		if list[i].ObjectID == "rev-1" {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("rev-1 not found in db.Reviews")
	}
	if !reflect.DeepEqual(found.Review.Revisions, want.Revisions) {
		t.Fatalf("db.Reviews revisions = %+v, want (FoldReview) %+v", found.Review.Revisions, want.Revisions)
	}
}
