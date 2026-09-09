package writ

import (
	"context"
	"fmt"
	"time"

	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/engine/resolve"
)

// Draft represents an unpublished local comment draft.
type Draft struct {
	ID          string          `json:"id"`
	SubjectType string          `json:"subject_type"`
	SubjectID   string          `json:"subject_id"`
	InReplyTo   string          `json:"in_reply_to,omitempty"`
	Anchor      *resolve.Anchor `json:"anchor,omitempty"`
	Text        string          `json:"text"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// DraftFilter specifies filtering criteria when querying drafts.
type DraftFilter struct {
	SubjectID   string `json:"subject_id,omitempty"`
	SubjectType string `json:"subject_type,omitempty"`
}

// Drafts provides operations on local-only comment drafts.
type Drafts struct {
	store *Store
}

// Save creates or updates a comment draft. If draft.ID is empty, a new draft ID is minted.
func (d *Drafts) Save(ctx context.Context, draft Draft) (string, error) {
	if d == nil || d.store == nil {
		return "", fmt.Errorf("writ: store is nil")
	}
	if draft.Text == "" {
		return "", fmt.Errorf("writ: draft text cannot be empty")
	}
	if draft.SubjectID == "" {
		return "", fmt.Errorf("writ: draft subject_id cannot be empty")
	}

	pDraft := projection.Draft{
		DraftID:     draft.ID,
		SubjectType: draft.SubjectType,
		SubjectID:   draft.SubjectID,
		InReplyTo:   draft.InReplyTo,
		Anchor:      draft.Anchor,
		Text:        draft.Text,
		CreatedAt:   draft.CreatedAt,
		UpdatedAt:   draft.UpdatedAt,
	}

	id, err := d.store.projection.SaveDraft(pDraft)
	if err != nil {
		return "", fmt.Errorf("writ: save draft: %w", err)
	}
	return id, nil
}

// Get retrieves a comment draft by its draft ID.
func (d *Drafts) Get(ctx context.Context, id string) (Draft, error) {
	if d == nil || d.store == nil {
		return Draft{}, fmt.Errorf("writ: store is nil")
	}
	if id == "" {
		return Draft{}, fmt.Errorf("writ: draft id cannot be empty")
	}

	pd, err := d.store.projection.Draft(id)
	if err != nil {
		return Draft{}, err
	}

	return Draft{
		ID:          pd.DraftID,
		SubjectType: pd.SubjectType,
		SubjectID:   pd.SubjectID,
		InReplyTo:   pd.InReplyTo,
		Anchor:      pd.Anchor,
		Text:        pd.Text,
		CreatedAt:   pd.CreatedAt,
		UpdatedAt:   pd.UpdatedAt,
	}, nil
}

// List returns all comment drafts matching the specified filter.
func (d *Drafts) List(ctx context.Context, filter DraftFilter) ([]Draft, error) {
	if d == nil || d.store == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}

	pList, err := d.store.projection.ListDrafts(projection.DraftFilter{
		SubjectID:   filter.SubjectID,
		SubjectType: filter.SubjectType,
	})
	if err != nil {
		return nil, fmt.Errorf("writ: list drafts: %w", err)
	}

	drafts := make([]Draft, len(pList))
	for i, pd := range pList {
		drafts[i] = Draft{
			ID:          pd.DraftID,
			SubjectType: pd.SubjectType,
			SubjectID:   pd.SubjectID,
			InReplyTo:   pd.InReplyTo,
			Anchor:      pd.Anchor,
			Text:        pd.Text,
			CreatedAt:   pd.CreatedAt,
			UpdatedAt:   pd.UpdatedAt,
		}
	}
	return drafts, nil
}

// Discard deletes a comment draft by its draft ID.
func (d *Drafts) Discard(ctx context.Context, id string) error {
	if d == nil || d.store == nil {
		return fmt.Errorf("writ: store is nil")
	}
	if id == "" {
		return fmt.Errorf("writ: draft id cannot be empty")
	}

	if err := d.store.projection.DeleteDraft(id); err != nil {
		return err
	}
	return nil
}

// Publish converts a local draft into a committed comment object referencing
// its subject by a soft "subject" field (object_type, object_id) — the same
// generic construction Objects.Create uses for every schema-declared type,
// "comment" included — and deletes the draft upon success.
//
// The subject's (and, when set, the reply's) existence is checked through
// the generic Query.Object lookup rather than a per-type projection reader:
// it returns ErrNotFound for a subject or reply that isn't there, and the
// real ObjectType for one that is, so Publish never has to guess or
// hardcode which schema-declared types a draft may target.
//
// Unlike the type-specific comment writers this replaced, Publish no longer
// threads the subject's (or reply's) frontier in as the new comment's
// causal DAG parents: nothing folds, queries, or threads (all of which
// group by the "subject"/"in_reply_to" fields, not DAG ancestry) depended
// on that link, so fold determinism is genuinely unaffected (t* and the
// total order are computed over the per-object_id restricted DAG,
// spec/fold.md §1-§4). What is affected is reachability: ARCHITECTURE.md
// §Ref layout names that edge as the reason an op someone built on stays
// reachable from the referencing writer's ref even if its origin ref rolls
// back, and spec/ref-layout.md §Producer requirements is the normative
// half requiring observed cross-object causal dependencies to follow at
// parents[1:]. Cross-writer causal edges within one object are unaffected —
// Objects.Apply still passes projection.Frontier(objectID) and ApplySchema
// still passes schemaFrontier(...) as parents[1:] (see ApplySchema) — so
// this loss is narrower than "no parents[1:] edge anywhere": what's gone is
// specifically the cross-object edge from a comment to its subject. An
// object commented on by another writer is no longer kept reachable by
// that comment once its own ref rolls back or is only partially fetched.
// Objects.Create has no parameter for observed causal parents, so
// restoring this is out of scope here — recorded as a deliberate,
// disclosed loss, not fixed (see CHANGELOG.md's "Known consequence, not
// fixed here" note under ### Removed).
func (d *Drafts) Publish(ctx context.Context, id string) (string, error) {
	if d == nil || d.store == nil {
		return "", fmt.Errorf("writ: store is nil")
	}

	draft, err := d.Get(ctx, id)
	if err != nil {
		return "", err
	}

	subject, err := d.store.Query.Object(draft.SubjectID)
	if err != nil {
		return "", err
	}

	fields := map[string]any{
		"subject": map[string]string{
			"object_type": subject.ObjectType,
			"object_id":   draft.SubjectID,
		},
		"text": draft.Text,
	}
	if draft.InReplyTo != "" {
		if _, err := d.store.Query.Object(draft.InReplyTo); err != nil {
			return "", err
		}
		fields["in_reply_to"] = draft.InReplyTo
	}
	if draft.Anchor != nil {
		fields["anchor"] = draft.Anchor
	}

	commentID, err := d.store.Objects.Create(ctx, "comment", NewOp{Type: "create", Fields: fields})
	if err != nil {
		return "", err
	}

	// Delete draft after successful comment operation
	_ = d.Discard(ctx, id)

	return commentID, nil
}
