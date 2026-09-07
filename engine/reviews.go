package writ

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec"
)

// Reviews provides operations on code review collaborative objects.
type Reviews struct {
	store *Store
}

// NewReview specifies parameters for creating a code review. Base and Head are
// commit OIDs, not ref names — spec/review-ops.md defines the revision body as
// a pair of OIDs, and the producer refuses to sign anything else. Callers
// holding a ref resolve it first, as the CLI does.
type NewReview struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Base        string `json:"base,omitempty"`
	Head        string `json:"head,omitempty"`
}

// ReviewEdit specifies metadata edits for a code review.
type ReviewEdit struct {
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
}

// ReviewStatus specifies status transitions for a code review.
type ReviewStatus struct {
	Status      string `json:"status"`
	MergeCommit string `json:"merge_commit,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// NewComment specifies parameters for adding a comment to a review or issue.
type NewComment struct {
	Text      string  `json:"text"`
	Anchor    *Anchor `json:"anchor,omitempty"`
	InReplyTo string  `json:"in_reply_to,omitempty"`
}

// Create initializes a new code review collaborative object, minting an object ID.
// If Base and Head are provided, an initial revision operation is automatically appended.
func (r *Reviews) Create(ctx context.Context, n NewReview) (string, error) {
	if r == nil || r.store == nil {
		return "", fmt.Errorf("writ: store is nil")
	}
	if err := r.store.ensureWritable(); err != nil {
		return "", err
	}
	if n.Title == "" {
		return "", fmt.Errorf("writ: review title cannot be empty")
	}
	if (n.Base != "" && n.Head == "") || (n.Base == "" && n.Head != "") {
		return "", fmt.Errorf("writ: both base and head must be specified for revision")
	}
	if n.Base != "" {
		if err := requireCommitOID("base", n.Base); err != nil {
			return "", err
		}
		if err := requireCommitOID("head", n.Head); err != nil {
			return "", err
		}
	}

	id := newObjectID()

	createBody := map[string]any{
		"title": n.Title,
	}
	if n.Description != "" {
		createBody["description"] = n.Description
	}

	createBytes, err := json.Marshal(createBody)
	if err != nil {
		return "", fmt.Errorf("writ: marshal review create body: %w", err)
	}

	createEnv := codec.Envelope{
		ObjectID:   id,
		ObjectType: "review",
		OpType:     "create",
		OpVersion:  1,
		Body:       createBytes,
	}

	var revEnv *codec.Envelope
	if n.Base != "" && n.Head != "" {
		revBytes, err := json.Marshal(map[string]string{
			"base": n.Base,
			"head": n.Head,
		})
		if err != nil {
			return "", fmt.Errorf("writ: marshal revision body: %w", err)
		}

		revEnv = &codec.Envelope{
			ObjectID:   id,
			ObjectType: "review",
			OpType:     "revision",
			OpVersion:  1,
			Body:       revBytes,
		}
	}

	// Both ops are checked before either is written. Create is two appends, and
	// an append is a signed commit in an append-only log: a create that lands
	// followed by a revision the producer refuses would leave a review in the
	// log forever that the caller never got an ID for, cannot address, cannot
	// retry idempotently and cannot withdraw.
	envs := []codec.Envelope{createEnv}
	if revEnv != nil {
		envs = append(envs, *revEnv)
	}
	if err := r.store.checkBeforeAppend(ctx, envs...); err != nil {
		return "", fmt.Errorf("writ: create review: %w", err)
	}

	if _, err := r.store.dagStore.Append(ctx, createEnv, nil); err != nil {
		return "", fmt.Errorf("writ: create review: %w", err)
	}

	if revEnv != nil {
		if _, err := r.store.dagStore.Append(ctx, *revEnv, nil); err != nil {
			return "", fmt.Errorf("writ: append initial revision: %w", err)
		}
	}

	_ = r.store.maybeAutoRefresh(ctx)
	return id, nil
}

// Update modifies title or description metadata for an existing review.
func (r *Reviews) Update(ctx context.Context, id string, edit ReviewEdit) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("writ: store is nil")
	}
	if err := r.store.ensureWritable(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("writ: review id cannot be empty")
	}
	if edit.Title == nil && edit.Description == nil {
		return fmt.Errorf("writ: at least one of title or description must be provided")
	}
	// A review has a title for its whole life: create requires one and the
	// update body's title has minLength 1, so there is no "clear the title"
	// state to reach. Pass nil to leave it alone.
	if edit.Title != nil && *edit.Title == "" {
		return fmt.Errorf("writ: review title cannot be empty: pass a nil title to leave it unchanged")
	}

	if err := r.store.maybeAutoRefresh(ctx); err != nil {
		return fmt.Errorf("writ: auto refresh: %w", err)
	}

	if _, err := r.store.projection.Review(id); err != nil {
		return err
	}

	frontier, err := r.store.projection.Frontier(id)
	if err != nil {
		return fmt.Errorf("writ: get frontier: %w", err)
	}

	body := make(map[string]any)
	if edit.Title != nil {
		body["title"] = *edit.Title
	}
	if edit.Description != nil {
		body["description"] = *edit.Description
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("writ: marshal update body: %w", err)
	}

	env := codec.Envelope{
		ObjectID:   id,
		ObjectType: "review",
		OpType:     "update",
		OpVersion:  1,
		Body:       bodyBytes,
	}

	if _, err := r.store.dagStore.Append(ctx, env, frontier); err != nil {
		return fmt.Errorf("writ: update review: %w", err)
	}

	_ = r.store.maybeAutoRefresh(ctx)
	return nil
}

// PushRevision appends a new code revision (base and head commit hashes) to the review.
func (r *Reviews) PushRevision(ctx context.Context, id, base, head string) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("writ: store is nil")
	}
	if err := r.store.ensureWritable(); err != nil {
		return err
	}
	if id == "" || base == "" || head == "" {
		return fmt.Errorf("writ: id, base, and head must be non-empty")
	}
	if err := requireCommitOID("base", base); err != nil {
		return err
	}
	if err := requireCommitOID("head", head); err != nil {
		return err
	}

	if err := r.store.maybeAutoRefresh(ctx); err != nil {
		return fmt.Errorf("writ: auto refresh: %w", err)
	}

	if _, err := r.store.projection.Review(id); err != nil {
		return err
	}

	frontier, err := r.store.projection.Frontier(id)
	if err != nil {
		return fmt.Errorf("writ: get frontier: %w", err)
	}

	bodyBytes, err := json.Marshal(map[string]string{
		"base": base,
		"head": head,
	})
	if err != nil {
		return fmt.Errorf("writ: marshal revision body: %w", err)
	}

	env := codec.Envelope{
		ObjectID:   id,
		ObjectType: "review",
		OpType:     "revision",
		OpVersion:  1,
		Body:       bodyBytes,
	}

	if _, err := r.store.dagStore.Append(ctx, env, frontier); err != nil {
		return fmt.Errorf("writ: push revision: %w", err)
	}

	_ = r.store.maybeAutoRefresh(ctx)
	return nil
}

// SetStatus transitions the review lifecycle state (e.g. "open", "draft", "closed", "merged").
func (r *Reviews) SetStatus(ctx context.Context, id string, status ReviewStatus) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("writ: store is nil")
	}
	if err := r.store.ensureWritable(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("writ: review id cannot be empty")
	}
	if status.Status == "" {
		return fmt.Errorf("writ: review status cannot be empty")
	}
	if !slices.Contains(spec.ReviewStatuses(), status.Status) {
		return fmt.Errorf("writ: invalid status %q (must be %s)", status.Status, spec.FormatOptions(spec.ReviewStatuses()))
	}
	if status.MergeCommit != "" {
		if err := requireCommitOID("merge commit", status.MergeCommit); err != nil {
			return err
		}
	}

	if err := r.store.maybeAutoRefresh(ctx); err != nil {
		return fmt.Errorf("writ: auto refresh: %w", err)
	}

	res, err := r.store.projection.Review(id)
	if err != nil {
		return err
	}
	if res.Review.Status == "merged" {
		return fmt.Errorf("writ: cannot transition review %s out of \"merged\" status", id)
	}

	frontier, err := r.store.projection.Frontier(id)
	if err != nil {
		return fmt.Errorf("writ: get frontier: %w", err)
	}

	body := map[string]any{
		"status": status.Status,
	}
	if status.MergeCommit != "" {
		body["merge_commit"] = status.MergeCommit
	}
	if status.Reason != "" {
		body["reason"] = status.Reason
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("writ: marshal set-status body: %w", err)
	}

	env := codec.Envelope{
		ObjectID:   id,
		ObjectType: "review",
		OpType:     "set-status",
		OpVersion:  1,
		Body:       bodyBytes,
	}

	if _, err := r.store.dagStore.Append(ctx, env, frontier); err != nil {
		return fmt.Errorf("writ: set review status: %w", err)
	}

	_ = r.store.maybeAutoRefresh(ctx)
	return nil
}

// Assign adds and/or removes assignees (requested reviewers) on a review.
func (r *Reviews) Assign(ctx context.Context, id string, add, remove []string) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("writ: store is nil")
	}
	if err := r.store.ensureWritable(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("writ: review id cannot be empty")
	}
	if len(add) == 0 && len(remove) == 0 {
		return fmt.Errorf("writ: add or remove must be non-empty")
	}

	if err := r.store.maybeAutoRefresh(ctx); err != nil {
		return fmt.Errorf("writ: auto refresh: %w", err)
	}

	if _, err := r.store.projection.Review(id); err != nil {
		return err
	}

	frontier, err := r.store.projection.Frontier(id)
	if err != nil {
		return fmt.Errorf("writ: get frontier: %w", err)
	}

	var normAdd, normRemove []string
	for _, a := range add {
		norm, err := normalizePersonBounded("assignee", a)
		if err != nil {
			return err
		}
		if norm != "" {
			normAdd = append(normAdd, norm)
		}
	}
	for _, rem := range remove {
		norm, err := normalizePersonBounded("assignee", rem)
		if err != nil {
			return err
		}
		if norm != "" {
			normRemove = append(normRemove, norm)
		}
	}
	if len(normAdd) == 0 && len(normRemove) == 0 {
		return fmt.Errorf("writ: add or remove must be non-empty")
	}

	body := make(map[string]any)
	if len(normAdd) > 0 {
		body["add"] = normAdd
	}
	if len(normRemove) > 0 {
		body["remove"] = normRemove
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("writ: marshal assign body: %w", err)
	}

	env := codec.Envelope{
		ObjectID:   id,
		ObjectType: "review",
		OpType:     "assign",
		OpVersion:  1,
		Body:       bodyBytes,
	}

	if _, err := r.store.dagStore.Append(ctx, env, frontier); err != nil {
		return fmt.Errorf("writ: assign review: %w", err)
	}

	_ = r.store.maybeAutoRefresh(ctx)
	return nil
}

// Label adds and/or removes labels on a review.
func (r *Reviews) Label(ctx context.Context, id string, add, remove []string) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("writ: store is nil")
	}
	if err := r.store.ensureWritable(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("writ: review id cannot be empty")
	}
	if err := validateLabels(add, remove); err != nil {
		return err
	}

	if err := r.store.maybeAutoRefresh(ctx); err != nil {
		return fmt.Errorf("writ: auto refresh: %w", err)
	}

	if _, err := r.store.projection.Review(id); err != nil {
		return err
	}

	frontier, err := r.store.projection.Frontier(id)
	if err != nil {
		return fmt.Errorf("writ: get frontier: %w", err)
	}

	body := make(map[string]any)
	if len(add) > 0 {
		body["add"] = add
	}
	if len(remove) > 0 {
		body["remove"] = remove
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("writ: marshal label body: %w", err)
	}

	env := codec.Envelope{
		ObjectID:   id,
		ObjectType: "review",
		OpType:     "label",
		OpVersion:  1,
		Body:       bodyBytes,
	}

	if _, err := r.store.dagStore.Append(ctx, env, frontier); err != nil {
		return fmt.Errorf("writ: label review: %w", err)
	}

	_ = r.store.maybeAutoRefresh(ctx)
	return nil
}

// Link creates or modifies a cross-reference link on a review.
func (r *Reviews) Link(ctx context.Context, id string, l Link) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("writ: store is nil")
	}
	if err := r.store.ensureWritable(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("writ: review id cannot be empty")
	}
	if l.Target == "" || l.Relation == "" {
		return fmt.Errorf("writ: review id, target, and relation must be non-empty")
	}
	if !slices.Contains(spec.LinkRelations(), l.Relation) {
		return fmt.Errorf("writ: invalid relation %q (must be %s)", l.Relation, spec.FormatOptions(spec.LinkRelations()))
	}

	if _, _, err := state.ParseReference(l.Target); err != nil {
		return fmt.Errorf("writ: invalid link target: %w", err)
	}

	if err := r.store.maybeAutoRefresh(ctx); err != nil {
		return fmt.Errorf("writ: auto refresh: %w", err)
	}

	if _, err := r.store.projection.Review(id); err != nil {
		return err
	}

	frontier, err := r.store.projection.Frontier(id)
	if err != nil {
		return fmt.Errorf("writ: get frontier: %w", err)
	}

	body := map[string]any{
		"target":   l.Target,
		"relation": l.Relation,
	}
	if l.TargetType != "" {
		body["target_type"] = l.TargetType
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("writ: marshal link body: %w", err)
	}

	env := codec.Envelope{
		ObjectID:   id,
		ObjectType: "review",
		OpType:     "link",
		OpVersion:  1,
		Body:       bodyBytes,
	}

	if _, err := r.store.dagStore.Append(ctx, env, frontier); err != nil {
		return fmt.Errorf("writ: link review: %w", err)
	}

	_ = r.store.maybeAutoRefresh(ctx)
	return nil
}

// Comment appends a new comment collaborative object attached to the review.
func (r *Reviews) Comment(ctx context.Context, id string, c NewComment) (string, error) {
	if r == nil || r.store == nil {
		return "", fmt.Errorf("writ: store is nil")
	}
	if err := r.store.ensureWritable(); err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("writ: review id cannot be empty")
	}
	if c.Text == "" {
		return "", fmt.Errorf("writ: comment text cannot be empty")
	}
	if c.Anchor != nil {
		if err := validateAnchor(c.Anchor); err != nil {
			return "", err
		}
	}

	if err := r.store.maybeAutoRefresh(ctx); err != nil {
		return "", fmt.Errorf("writ: auto refresh: %w", err)
	}

	if _, err := r.store.projection.Review(id); err != nil {
		return "", err
	}

	reviewFrontier, err := r.store.projection.Frontier(id)
	if err != nil {
		return "", fmt.Errorf("writ: get review frontier: %w", err)
	}

	causalParents := make([]string, len(reviewFrontier))
	copy(causalParents, reviewFrontier)

	if c.InReplyTo != "" {
		replyFrontier, err := r.store.projection.Frontier(c.InReplyTo)
		if err != nil {
			return "", fmt.Errorf("writ: get reply frontier: %w", err)
		}
		if len(replyFrontier) == 0 {
			return "", fmt.Errorf("writ: in_reply_to comment %s not found: %w", c.InReplyTo, ErrNotFound)
		}
		causalParents = append(causalParents, replyFrontier...)
	}
	causalParents = dedupeAndSort(causalParents)

	commentID := newObjectID()

	body := map[string]any{
		"subject": CommentSubject{
			ObjectType: "review",
			ObjectID:   id,
		},
		"text": c.Text,
	}
	if c.InReplyTo != "" {
		body["in_reply_to"] = c.InReplyTo
	}
	if c.Anchor != nil {
		body["anchor"] = c.Anchor
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("writ: marshal comment body: %w", err)
	}

	env := codec.Envelope{
		ObjectID:   commentID,
		ObjectType: "comment",
		OpType:     "create",
		OpVersion:  1,
		Body:       bodyBytes,
	}

	if _, err := r.store.dagStore.Append(ctx, env, causalParents); err != nil {
		return "", fmt.Errorf("writ: comment on review: %w", err)
	}

	_ = r.store.maybeAutoRefresh(ctx)
	return commentID, nil
}

// Approve records a review verdict (approval, change request, or dismissal) for the review.
// If Revision is omitted, it defaults to the latest revision head.
// If Verdict is omitted, it defaults to "approve".
func (r *Reviews) Approve(ctx context.Context, id string, a Approval) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("writ: store is nil")
	}
	if err := r.store.ensureWritable(); err != nil {
		return err
	}
	if id == "" {
		return fmt.Errorf("writ: review id cannot be empty")
	}

	if a.Verdict == "" {
		a.Verdict = "approve"
	}
	if !slices.Contains(spec.ApprovalVerdicts(), a.Verdict) {
		return fmt.Errorf("writ: invalid verdict %q (must be %s)", a.Verdict, spec.FormatOptions(spec.ApprovalVerdicts()))
	}
	if a.Revision != "" {
		if err := requireCommitOID("revision", a.Revision); err != nil {
			return err
		}
	}

	if err := r.store.maybeAutoRefresh(ctx); err != nil {
		return fmt.Errorf("writ: auto refresh: %w", err)
	}

	res, err := r.store.projection.Review(id)
	if err != nil {
		return err
	}

	if a.Revision == "" {
		if len(res.Review.Revisions) == 0 {
			return fmt.Errorf("writ: cannot approve review with no revisions")
		}
		a.Revision = res.Review.Revisions[len(res.Review.Revisions)-1].Head
	}

	frontier, err := r.store.projection.Frontier(id)
	if err != nil {
		return fmt.Errorf("writ: get frontier: %w", err)
	}

	body := map[string]any{
		"revision": a.Revision,
		"verdict":  a.Verdict,
	}
	subject, err := normalizePersonBounded("approval subject", a.Subject)
	if err != nil {
		return err
	}
	if subject != "" {
		body["subject"] = subject
	}
	body["message"] = a.Message

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("writ: marshal approval body: %w", err)
	}

	env := codec.Envelope{
		ObjectID:   id,
		ObjectType: "review",
		OpType:     "approval",
		OpVersion:  1,
		Body:       bodyBytes,
	}

	if _, err := r.store.dagStore.Append(ctx, env, frontier); err != nil {
		return fmt.Errorf("writ: approve review: %w", err)
	}

	_ = r.store.maybeAutoRefresh(ctx)
	return nil
}

func dedupeAndSort(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(items))
	var result []string
	for _, item := range items {
		if item != "" && !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	sort.Strings(result)
	return result
}
