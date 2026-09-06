// Package wire defines the CLI-owned JSON wire format for writ plumbing mode (--json).
//
// These wire types are deliberately decoupled from domain serialization and engine state tags
// so that internal engine changes cannot silently break scripted consumers.
package wire

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/resolve"
	"github.com/writtendev/writ/engine/state"
)

// CurrentSchemaVersion is the version of the JSON plumbing envelope schema.
const CurrentSchemaVersion = 1

// Envelope kinds for plumbing commands.
const (
	KindReviewList    = "review.list"
	KindReviewStatus  = "review.status"
	KindIssueList     = "issue.list"
	KindIssueStatus   = "issue.status"
	KindIssueLabel    = "issue.label"
	KindIssueLabels   = "issue.label"
	KindSyncStatus    = "sync.status"
	KindSyncResult    = "sync.result"
	KindCommentEdit   = "comment.edit"
	KindCommentDelete = "comment.delete"
	KindStateList     = "state.list"
	KindLabelList     = "label.list"
	KindDocList       = "doc.list"
	KindDocShow       = "doc.show"
	KindDocCreate     = "doc.create"
	KindDocEdit       = "doc.edit"
	KindDocLink       = "doc.link"
	KindDocSection    = "doc.section"
	KindSettings      = "settings"
	KindSchemaPlan    = "schema.plan"
	KindSchemaApply   = "schema.apply"
)

// Envelope wraps all machine-readable output in a single versioned container.
type Envelope struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Data          any    `json:"data"`
}

// Author represents the display name and email address of an author.
type Author struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Revision represents a code revision push (base and head commit SHAs).
type Revision struct {
	Base string `json:"base"`
	Head string `json:"head"`
}

// Approval represents an approval or review verdict.
type Approval struct {
	Subject  string `json:"subject"`
	Revision string `json:"revision"`
	Verdict  string `json:"verdict"`
	Message  string `json:"message,omitempty"`
}

// CIStatus represents an automated CI check result.
type CIStatus struct {
	Revision    string `json:"revision"`
	Name        string `json:"name"`
	State       string `json:"state"`
	URL         string `json:"url,omitempty"`
	Description string `json:"description,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	ExternalID  string `json:"external_id,omitempty"`
}

// UnknownOp records an unrecognized operation preserved for forward compatibility.
type UnknownOp struct {
	Commit     string `json:"commit"`
	ObjectType string `json:"object_type"`
	OpType     string `json:"op_type"`
	OpVersion  int64  `json:"op_version"`
}

// ReviewSummary is a single row in the review list output.
type ReviewSummary struct {
	ObjectID  string    `json:"object_id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Author    Author    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ReviewLink represents a cross-reference link attached to a review.
type ReviewLink struct {
	Target     string `json:"target"`
	TargetType string `json:"target_type,omitempty"`
	Relation   string `json:"relation"`
}

// Review is the full detail view of a code review collaborative object.
type Review struct {
	ObjectID    string       `json:"object_id"`
	Title       string       `json:"title"`
	Description string       `json:"description,omitempty"`
	Status      string       `json:"status"`
	MergeCommit string       `json:"merge_commit,omitempty"`
	Reason      string       `json:"reason,omitempty"`
	Author      Author       `json:"author"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
	Assignees   []string     `json:"assignees"`
	Labels      []string     `json:"labels"`
	Links       []ReviewLink `json:"links"`
	Revisions   []Revision   `json:"revisions"`
	Approvals   []Approval   `json:"approvals"`
	CIStatuses  []CIStatus   `json:"ci_statuses"`
	UnknownOps  []UnknownOp  `json:"unknown_ops"`
}

// IssueSummary is a single row in the issue list output.
type IssueSummary struct {
	ObjectID  string    `json:"object_id"`
	Title     string    `json:"title"`
	State     string    `json:"state"`
	Priority  int       `json:"priority"`
	Estimate  *float64  `json:"estimate,omitempty"`
	Position  string    `json:"position,omitempty"`
	Author    Author    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IssueLink represents a cross-reference link attached to an issue.
type IssueLink struct {
	Target     string `json:"target"`
	TargetType string `json:"target_type,omitempty"`
	Relation   string `json:"relation"`
}

// Issue is the full detail view of an issue collaborative object.
type Issue struct {
	ObjectID    string          `json:"object_id"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	State       string          `json:"state"`
	Reason      string          `json:"reason,omitempty"`
	Priority    int             `json:"priority"`
	Estimate    *float64        `json:"estimate,omitempty"`
	Position    string          `json:"position,omitempty"`
	Author      Author          `json:"author"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Assignees   []string        `json:"assignees"`
	Labels      []string        `json:"labels"`
	Links       []IssueLink     `json:"links"`
	Comments    []CommentThread `json:"comments"`
	UnknownOps  []UnknownOp     `json:"unknown_ops"`
}

// IssueLabels represents the labels attached to an issue.
type IssueLabels struct {
	ObjectID string   `json:"object_id"`
	Labels   []string `json:"labels"`
}

// Range is a 1-based inclusive line range [Start, End].
type Range struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Context holds captured line content and surrounding context.
type Context struct {
	Before  []string `json:"before,omitempty"`
	Lines   []string `json:"lines,omitempty"`
	Omitted int      `json:"omitted,omitempty"`
	After   []string `json:"after,omitempty"`
}

// SideAnchor describes the position on one side of a diff.
type SideAnchor struct {
	Commit  string   `json:"commit"`
	Path    string   `json:"path"`
	Blob    string   `json:"blob"`
	Range   *Range   `json:"range,omitempty"`
	Context *Context `json:"context,omitempty"`
}

// Anchor is a content-based comment anchor.
type Anchor struct {
	Version int         `json:"version"`
	Old     *SideAnchor `json:"old,omitempty"`
	New     *SideAnchor `json:"new,omitempty"`
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

// CommentSubject identifies the collaborative object a comment is attached to.
type CommentSubject struct {
	ObjectType string `json:"object_type"`
	ObjectID   string `json:"object_id"`
}

// Comment represents a comment on a collaborative object.
type Comment struct {
	ObjectID   string             `json:"object_id"`
	Subject    CommentSubject     `json:"subject"`
	Author     Author             `json:"author"`
	CreatedAt  time.Time          `json:"created_at"`
	UpdatedAt  time.Time          `json:"updated_at"`
	Text       string             `json:"text"`
	InReplyTo  string             `json:"in_reply_to,omitempty"`
	Anchor     *Anchor            `json:"anchor,omitempty"`
	Deleted    bool               `json:"deleted"`
	Resolved   bool               `json:"resolved"`
	ResolvedBy string             `json:"resolved_by,omitempty"`
	Positions  []ResolvedPosition `json:"positions,omitempty"`
	UnknownOps []UnknownOp        `json:"unknown_ops"`
}

// CommentThread represents a root comment and its nested replies.
type CommentThread struct {
	ObjectID   string          `json:"object_id"`
	Comment    Comment         `json:"comment"`
	Replies    []CommentThread `json:"replies"`
	UnknownOps []UnknownOp     `json:"unknown_ops"`
}

// Failure represents structured error reporting for a failed sync operation.
type Failure struct {
	Kind      string `json:"kind"`
	Message   string `json:"message"`
	Advice    string `json:"advice,omitempty"`
	Retryable bool   `json:"retryable"`
}

// SyncStatus reports synchronization status for a single remote.
type SyncStatus struct {
	Remote   string   `json:"remote"`
	Unsynced int      `json:"unsynced"`
	Failure  *Failure `json:"failure,omitempty"`
}

// SyncResult reports aggregate statistics from a sync operation for a single remote.
type SyncResult struct {
	Remote         string   `json:"remote"`
	OpsFetched     int      `json:"ops_fetched"`
	OpsPushed      int      `json:"ops_pushed"`
	ObjectsTouched int      `json:"objects_touched"`
	Unsynced       int      `json:"unsynced"`
	Failure        *Failure `json:"failure,omitempty"`
}

// FromReviewResultSummary converts a writ.ReviewResult into a ReviewSummary wire struct.
func FromReviewResultSummary(r writ.ReviewResult) ReviewSummary {
	return ReviewSummary{
		ObjectID:  r.ObjectID,
		Title:     r.Review.Title,
		Status:    r.Review.Status,
		Author:    Author{Name: r.Author.Name, Email: r.Author.Email},
		CreatedAt: r.CreatedAt.UTC(),
		UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// FromReviewResultSummaries converts a slice of writ.ReviewResults into a slice of ReviewSummaries.
// An empty slice is returned rather than nil to ensure JSON serialization as `[]`.
func FromReviewResultSummaries(reviews []writ.ReviewResult) []ReviewSummary {
	if len(reviews) == 0 {
		return []ReviewSummary{}
	}
	out := make([]ReviewSummary, len(reviews))
	for i, r := range reviews {
		out[i] = FromReviewResultSummary(r)
	}
	return out
}

// FromReviewResult converts a writ.ReviewResult into a full detail Review wire struct.
// Collections are always initialized to empty non-nil slices so they serialize as `[]`.
func FromReviewResult(r writ.ReviewResult) Review {
	revisions := make([]Revision, len(r.Review.Revisions))
	for i, rev := range r.Review.Revisions {
		revisions[i] = Revision{
			Base: rev.Base,
			Head: rev.Head,
		}
	}
	approvals := make([]Approval, len(r.Review.Approvals))
	for i, app := range r.Review.Approvals {
		approvals[i] = Approval{
			Subject:  app.Subject,
			Revision: app.Revision,
			Verdict:  app.Verdict,
			Message:  app.Message,
		}
	}
	ciStatuses := make([]CIStatus, len(r.Review.CIStatuses))
	for i, ci := range r.Review.CIStatuses {
		ciStatuses[i] = CIStatus{
			Revision:    ci.Revision,
			Name:        ci.Name,
			State:       ci.State,
			URL:         ci.URL,
			Description: ci.Description,
			StartedAt:   ci.StartedAt,
			CompletedAt: ci.CompletedAt,
			ExternalID:  ci.ExternalID,
		}
	}
	unknownOps := make([]UnknownOp, len(r.Review.UnknownOps))
	for i, u := range r.Review.UnknownOps {
		unknownOps[i] = UnknownOp{
			Commit:     u.Commit,
			ObjectType: u.ObjectType,
			OpType:     u.OpType,
			OpVersion:  u.OpVersion,
		}
	}
	assignees := r.Review.Assignees
	if assignees == nil {
		assignees = []string{}
	}
	labels := r.Review.Labels
	if labels == nil {
		labels = []string{}
	}
	links := make([]ReviewLink, len(r.Review.Links))
	for i, l := range r.Review.Links {
		links[i] = ReviewLink{
			Target:     l.Target,
			TargetType: l.TargetType,
			Relation:   l.Relation,
		}
	}

	return Review{
		ObjectID:    r.ObjectID,
		Title:       r.Review.Title,
		Description: r.Review.Description,
		Status:      r.Review.Status,
		MergeCommit: r.Review.MergeCommit,
		Reason:      r.Review.Reason,
		Author:      Author{Name: r.Author.Name, Email: r.Author.Email},
		CreatedAt:   r.CreatedAt.UTC(),
		UpdatedAt:   r.UpdatedAt.UTC(),
		Assignees:   assignees,
		Labels:      labels,
		Links:       links,
		Revisions:   revisions,
		Approvals:   approvals,
		CIStatuses:  ciStatuses,
		UnknownOps:  unknownOps,
	}
}

// FromIssueResultSummary converts a writ.IssueResult into an IssueSummary wire struct.
func FromIssueResultSummary(r writ.IssueResult) IssueSummary {
	return IssueSummary{
		ObjectID:  r.ObjectID,
		Title:     r.Issue.Title,
		State:     r.Issue.State,
		Priority:  r.Issue.Priority,
		Estimate:  r.Issue.Estimate,
		Position:  r.Issue.Position,
		Author:    Author{Name: r.Author.Name, Email: r.Author.Email},
		CreatedAt: r.CreatedAt.UTC(),
		UpdatedAt: r.UpdatedAt.UTC(),
	}
}

// FromIssueResultSummaries converts a slice of writ.IssueResults into a slice of IssueSummaries.
// An empty slice is returned rather than nil to ensure JSON serialization as `[]`.
func FromIssueResultSummaries(issues []writ.IssueResult) []IssueSummary {
	if len(issues) == 0 {
		return []IssueSummary{}
	}
	out := make([]IssueSummary, len(issues))
	for i, r := range issues {
		out[i] = FromIssueResultSummary(r)
	}
	return out
}

// FromIssueResult converts a writ.IssueResult and its comment threads into a full detail Issue wire struct.
// Collections are always initialized to empty non-nil slices so they serialize as `[]`.
func FromIssueResult(r writ.IssueResult, threads []state.CommentThread) Issue {
	assignees := r.Issue.Assignees
	if assignees == nil {
		assignees = []string{}
	}
	labels := r.Issue.Labels
	if labels == nil {
		labels = []string{}
	}
	links := make([]IssueLink, len(r.Issue.Links))
	for i, l := range r.Issue.Links {
		links[i] = IssueLink{
			Target:     l.Target,
			TargetType: l.TargetType,
			Relation:   l.Relation,
		}
	}
	comments := make([]CommentThread, len(threads))
	for i, t := range threads {
		comments[i] = FromCommentThread(t)
	}
	unknownOps := make([]UnknownOp, len(r.Issue.UnknownOps))
	for i, u := range r.Issue.UnknownOps {
		unknownOps[i] = UnknownOp{
			Commit:     u.Commit,
			ObjectType: u.ObjectType,
			OpType:     u.OpType,
			OpVersion:  u.OpVersion,
		}
	}

	return Issue{
		ObjectID:    r.ObjectID,
		Title:       r.Issue.Title,
		Description: r.Issue.Description,
		State:       r.Issue.State,
		Reason:      r.Issue.Reason,
		Priority:    r.Issue.Priority,
		Estimate:    r.Issue.Estimate,
		Position:    r.Issue.Position,
		Author:      Author{Name: r.Author.Name, Email: r.Author.Email},
		CreatedAt:   r.CreatedAt.UTC(),
		UpdatedAt:   r.UpdatedAt.UTC(),
		Assignees:   assignees,
		Labels:      labels,
		Links:       links,
		Comments:    comments,
		UnknownOps:  unknownOps,
	}
}

// FromIssueLabels converts an issue ID and label slice into an IssueLabels wire struct.
// Collections are always initialized to empty non-nil slices so they serialize as `[]`.
func FromIssueLabels(objectID string, labels []string) IssueLabels {
	if labels == nil {
		labels = []string{}
	}
	return IssueLabels{
		ObjectID: objectID,
		Labels:   labels,
	}
}

// FromSyncStatus converts a writ.SyncStatus into a SyncStatus wire struct.
func FromSyncStatus(s writ.SyncStatus) SyncStatus {
	return SyncStatus{
		Remote:   s.Remote,
		Unsynced: s.Unsynced,
	}
}

// FromSyncStatusFailure converts a remote name, error, and unsynced count into a SyncStatus wire struct with Failure populated.
func FromSyncStatusFailure(remote string, err error, unsynced int) SyncStatus {
	var failure *Failure
	if err != nil {
		var syncErr *writ.SyncError
		if errors.As(err, &syncErr) {
			failure = &Failure{
				Kind:      syncErr.Kind,
				Message:   syncErr.Message,
				Advice:    syncErr.Advice,
				Retryable: syncErr.Retryable,
			}
			unsynced = syncErr.Unsynced
		} else {
			failure = &Failure{
				Kind:      "unknown",
				Message:   err.Error(),
				Retryable: false,
			}
		}
	}
	return SyncStatus{
		Remote:   remote,
		Unsynced: unsynced,
		Failure:  failure,
	}
}

// FromSyncResult converts a remote name and writ.SyncResult into a SyncResult wire struct.
func FromSyncResult(remote string, res writ.SyncResult) SyncResult {
	return SyncResult{
		Remote:         remote,
		OpsFetched:     res.OpsFetched,
		OpsPushed:      res.OpsPushed,
		ObjectsTouched: res.ObjectsTouched,
		Unsynced:       res.Unsynced,
	}
}

// FromSyncResultFailure converts a remote name, writ.SyncResult, and error into a SyncResult wire struct with Failure populated.
func FromSyncResultFailure(remote string, res writ.SyncResult, err error) SyncResult {
	var failure *Failure
	unsynced := res.Unsynced
	if err != nil {
		var syncErr *writ.SyncError
		if errors.As(err, &syncErr) {
			failure = &Failure{
				Kind:      syncErr.Kind,
				Message:   syncErr.Message,
				Advice:    syncErr.Advice,
				Retryable: syncErr.Retryable,
			}
			unsynced = syncErr.Unsynced
		} else {
			failure = &Failure{
				Kind:      "unknown",
				Message:   err.Error(),
				Retryable: false,
			}
		}
	}
	return SyncResult{
		Remote:         remote,
		OpsFetched:     res.OpsFetched,
		OpsPushed:      res.OpsPushed,
		ObjectsTouched: res.ObjectsTouched,
		Unsynced:       unsynced,
		Failure:        failure,
	}
}

func fromResolveAnchor(a resolve.Anchor) *Anchor {
	var oldSide, newSide *SideAnchor
	if a.Old != nil {
		oldSide = fromResolveSideAnchor(a.Old)
	}
	if a.New != nil {
		newSide = fromResolveSideAnchor(a.New)
	}
	return &Anchor{
		Version: a.Version,
		Old:     oldSide,
		New:     newSide,
	}
}

func fromResolveSideAnchor(s *resolve.SideAnchor) *SideAnchor {
	if s == nil {
		return nil
	}
	var rng *Range
	if s.Range != nil {
		rng = &Range{Start: s.Range.Start, End: s.Range.End}
	}
	var ctx *Context
	if s.Context != nil {
		ctx = &Context{
			Before:  s.Context.Before,
			Lines:   s.Context.Lines,
			Omitted: s.Context.Omitted,
			After:   s.Context.After,
		}
	}
	return &SideAnchor{
		Commit:  s.Commit,
		Path:    s.Path,
		Blob:    s.Blob,
		Range:   rng,
		Context: ctx,
	}
}

// FromCommentResult converts a writ.CommentResult into a Comment wire struct.
func FromCommentResult(c writ.CommentResult) Comment {
	unknownOps := make([]UnknownOp, len(c.Comment.UnknownOps))
	for i, u := range c.Comment.UnknownOps {
		unknownOps[i] = UnknownOp{
			Commit:     u.Commit,
			ObjectType: u.ObjectType,
			OpType:     u.OpType,
			OpVersion:  u.OpVersion,
		}
	}
	var anchor *Anchor
	if c.Comment.Anchor != nil {
		anchor = fromResolveAnchor(*c.Comment.Anchor)
	}
	var positions []ResolvedPosition
	if len(c.Resolved) > 0 {
		positions = make([]ResolvedPosition, len(c.Resolved))
		for i, r := range c.Resolved {
			positions[i] = ResolvedPosition{
				Side:      r.Side,
				Outcome:   r.Outcome,
				Match:     r.Match,
				Path:      r.Path,
				StartLine: r.StartLine,
				EndLine:   r.EndLine,
				Reason:    r.Reason,
			}
		}
	}
	return Comment{
		ObjectID: c.ObjectID,
		Subject: CommentSubject{
			ObjectType: c.Comment.Subject.ObjectType,
			ObjectID:   c.Comment.Subject.ObjectID,
		},
		Author:     Author{Name: c.Author.Name, Email: c.Author.Email},
		CreatedAt:  c.CreatedAt.UTC(),
		UpdatedAt:  c.UpdatedAt.UTC(),
		Text:       c.Comment.Text,
		InReplyTo:  c.Comment.InReplyTo,
		Anchor:     anchor,
		Deleted:    c.Comment.Deleted,
		Resolved:   c.Comment.IsResolved(),
		ResolvedBy: c.Comment.ResolvedBy,
		Positions:  positions,
		UnknownOps: unknownOps,
	}
}

// FromCommentThread converts a state.CommentThread into a CommentThread wire struct.
func FromCommentThread(t state.CommentThread) CommentThread {
	replies := make([]CommentThread, len(t.Replies))
	for i, rep := range t.Replies {
		replies[i] = FromCommentThread(rep)
	}
	unknownOps := make([]UnknownOp, len(t.UnknownOps))
	for i, u := range t.UnknownOps {
		unknownOps[i] = UnknownOp{
			Commit:     u.Commit,
			ObjectType: u.ObjectType,
			OpType:     u.OpType,
			OpVersion:  u.OpVersion,
		}
	}
	var anchor *Anchor
	if t.Comment.Anchor != nil {
		anchor = fromResolveAnchor(*t.Comment.Anchor)
	}
	return CommentThread{
		ObjectID: t.ObjectID,
		Comment: Comment{
			ObjectID: t.ObjectID,
			Subject: CommentSubject{
				ObjectType: t.Comment.Subject.ObjectType,
				ObjectID:   t.Comment.Subject.ObjectID,
			},
			Text:       t.Comment.Text,
			InReplyTo:  t.Comment.InReplyTo,
			Anchor:     anchor,
			Deleted:    t.Comment.Deleted,
			Resolved:   t.Comment.IsResolved(),
			ResolvedBy: t.Comment.ResolvedBy,
			UnknownOps: unknownOps,
		},
		Replies:    replies,
		UnknownOps: unknownOps,
	}
}

// StateSummary represents summary information about a workflow state in wire format.
type StateSummary struct {
	ObjectID    string    `json:"object_id"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	Position    string    `json:"position"`
	Color       string    `json:"color,omitempty"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// FromWorkflowStateResult converts a domain WorkflowStateResult to a wire StateSummary.
func FromWorkflowStateResult(r writ.WorkflowStateResult) StateSummary {
	return StateSummary{
		ObjectID:    r.ObjectID,
		Name:        r.WorkflowState.Name,
		Type:        r.WorkflowState.Type,
		Position:    r.WorkflowState.Position,
		Color:       r.WorkflowState.Color,
		Description: r.WorkflowState.Description,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// FromWorkflowStateResultSummaries converts a slice of domain WorkflowStateResults to wire StateSummaries.
func FromWorkflowStateResultSummaries(results []writ.WorkflowStateResult) []StateSummary {
	summaries := make([]StateSummary, len(results))
	for i, r := range results {
		summaries[i] = FromWorkflowStateResult(r)
	}
	return summaries
}

// LabelSummary represents a label object in wire listings.
type LabelSummary struct {
	ObjectID    string    `json:"object_id"`
	Name        string    `json:"name"`
	Color       string    `json:"color,omitempty"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// FromLabelResult converts a domain LabelResult to a wire LabelSummary.
func FromLabelResult(r writ.LabelResult) LabelSummary {
	return LabelSummary{
		ObjectID:    r.ObjectID,
		Name:        r.Label.Name,
		Color:       r.Label.Color,
		Description: r.Label.Description,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// FromLabelResultSummaries converts a slice of domain LabelResults to wire LabelSummaries.
func FromLabelResultSummaries(results []writ.LabelResult) []LabelSummary {
	summaries := make([]LabelSummary, len(results))
	for i, r := range results {
		summaries[i] = FromLabelResult(r)
	}
	return summaries
}

// DocumentSummary represents a document in a list view.
type DocumentSummary struct {
	ObjectID  string    `json:"object_id"`
	Title     string    `json:"title"`
	Author    Author    `json:"author"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Labels    []string  `json:"labels"`
	Sections  int       `json:"sections"`
}

// Section represents a document section.
type Section struct {
	ObjectID   string    `json:"object_id"`
	DocumentID string    `json:"document_id"`
	Position   string    `json:"position"`
	Title      string    `json:"title,omitempty"`
	Body       any       `json:"body"` // string or []string
	Conflicted bool      `json:"conflicted"`
	Deleted    bool      `json:"deleted"`
	Author     Author    `json:"author"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// DocumentLink represents a cross-reference link attached to a document.
type DocumentLink struct {
	Target     string `json:"target"`
	TargetType string `json:"target_type,omitempty"`
	Relation   string `json:"relation"`
}

// Document is the full detail view of a document collaborative object.
type Document struct {
	ObjectID  string         `json:"object_id"`
	Title     string         `json:"title"`
	Author    Author         `json:"author"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Labels    []string       `json:"labels"`
	Links     []DocumentLink `json:"links"`
	Sections  []Section      `json:"sections"`
}

// FromDocumentResult converts a domain DocumentResult to a wire Document.
func FromDocumentResult(d writ.DocumentResult) Document {
	links := make([]DocumentLink, len(d.Document.Links))
	for i, l := range d.Document.Links {
		links[i] = DocumentLink{
			Target:     l.Target,
			TargetType: l.TargetType,
			Relation:   l.Relation,
		}
	}
	sections := make([]Section, len(d.Sections))
	for i, s := range d.Sections {
		sections[i] = FromSectionResult(s)
	}
	labels := d.Document.Labels
	if labels == nil {
		labels = []string{}
	}
	return Document{
		ObjectID:  d.ObjectID,
		Title:     d.Document.Title,
		Author:    Author{Name: d.Author.Name, Email: d.Author.Email},
		CreatedAt: d.CreatedAt,
		UpdatedAt: d.UpdatedAt,
		Labels:    labels,
		Links:     links,
		Sections:  sections,
	}
}

// FromSectionResult converts a domain SectionResult to a wire Section.
func FromSectionResult(s writ.SectionResult) Section {
	var body any
	if s.Section.IsConflicted() {
		body = s.Section.ConflictBodies()
	} else {
		body = s.Section.SettledBody()
	}
	return Section{
		ObjectID:   s.ObjectID,
		DocumentID: s.Section.DocumentID,
		Position:   s.Section.Position,
		Title:      s.Section.Title,
		Body:       body,
		Conflicted: s.Section.IsConflicted(),
		Deleted:    s.Section.Deleted,
		Author:     Author{Name: s.Author.Name, Email: s.Author.Email},
		CreatedAt:  s.CreatedAt,
		UpdatedAt:  s.UpdatedAt,
	}
}

// FromDocumentResults converts a slice of domain DocumentResults to wire DocumentSummaries.
func FromDocumentResults(docs []writ.DocumentResult) []DocumentSummary {
	res := make([]DocumentSummary, len(docs))
	for i, d := range docs {
		labels := d.Document.Labels
		if labels == nil {
			labels = []string{}
		}
		res[i] = DocumentSummary{
			ObjectID:  d.ObjectID,
			Title:     d.Document.Title,
			Author:    Author{Name: d.Author.Name, Email: d.Author.Email},
			CreatedAt: d.CreatedAt,
			UpdatedAt: d.UpdatedAt,
			Labels:    labels,
			Sections:  len(d.Sections),
		}
	}
	return res
}

// SettingsWire represents workspace settings in JSON plumbing output.
type SettingsWire struct {
	ObjectID           string         `json:"object_id"`
	Name               string         `json:"name"`
	Identifier         string         `json:"identifier"`
	Timezone           string         `json:"timezone"`
	EstimateScale      string         `json:"estimate_scale"`
	AllowZeroEstimates bool           `json:"allow_zero_estimates"`
	CyclesEnabled      bool           `json:"cycles_enabled"`
	CycleDurationWeeks int            `json:"cycle_duration_weeks"`
	CycleStartDay      int            `json:"cycle_start_day"`
	CycleCooldownWeeks int            `json:"cycle_cooldown_weeks"`
	TriageEnabled      bool           `json:"triage_enabled"`
	UnknownKeys        map[string]any `json:"unknown_keys,omitempty"`
}

// FromSettingsResult converts a domain SettingsResult to its wire representation.
func FromSettingsResult(res writ.SettingsResult) SettingsWire {
	return SettingsWire{
		ObjectID:           res.ObjectID,
		Name:               res.Settings.Name,
		Identifier:         res.Settings.Identifier,
		Timezone:           res.Settings.Timezone,
		EstimateScale:      res.Settings.EstimateScale,
		AllowZeroEstimates: res.Settings.AllowZeroEstimates,
		CyclesEnabled:      res.Settings.CyclesEnabled,
		CycleDurationWeeks: res.Settings.CycleDurationWeeks,
		CycleStartDay:      res.Settings.CycleStartDay,
		CycleCooldownWeeks: res.Settings.CycleCooldownWeeks,
		TriageEnabled:      res.Settings.TriageEnabled,
		UnknownKeys:        res.Settings.UnknownKeys,
	}
}

// SchemaOpEntry is one op `writ schema plan`/`apply` would append (or did),
// in the same op vocabulary spec/schema-ops.md defines. Body is the
// envelope body verbatim — the normative wire form is the honest answer to
// "which ops would be appended", stable in a way a re-modelled shape would
// not be.
type SchemaOpEntry struct {
	OpType string          `json:"op_type"`
	Body   json.RawMessage `json:"body"`
}

// FromSchemaEnvelopes converts a compiled op sequence into wire entries,
// preserving order. Collections are always non-nil so they serialize as `[]`.
func FromSchemaEnvelopes(envs []codec.Envelope) []SchemaOpEntry {
	out := make([]SchemaOpEntry, len(envs))
	for i, e := range envs {
		out[i] = SchemaOpEntry{OpType: e.OpType, Body: json.RawMessage(e.Body)}
	}
	return out
}

// SchemaConflict is a load-bearing collision between schema objects
// (spec/schema-ops.md §Conflicts), reported by `plan` whether or not the
// working-tree file caused it — it is the only surface that shows them.
type SchemaConflict struct {
	ObjectType string   `json:"object_type,omitempty"`
	Namespace  string   `json:"namespace,omitempty"`
	ObjectIDs  []string `json:"object_ids"`
	Reason     string   `json:"reason"`
}

// FromSchemaConflicts converts domain SchemaConflicts to wire form.
// Collections are always non-nil so they serialize as `[]`.
func FromSchemaConflicts(conflicts []writ.SchemaConflict) []SchemaConflict {
	out := make([]SchemaConflict, len(conflicts))
	for i, c := range conflicts {
		ids := c.ObjectIDs
		if ids == nil {
			ids = []string{}
		}
		out[i] = SchemaConflict{ObjectType: c.ObjectType, Namespace: c.Namespace, ObjectIDs: ids, Reason: c.Reason}
	}
	return out
}

// SchemaPlan is the `schema.plan` JSON payload: the target schema object
// `writ schema apply` would write to, whether it would be minted fresh, and
// the ops that would be appended to bring it in line with the working-tree
// writ.schema file.
type SchemaPlan struct {
	ObjectID      string           `json:"object_id"`
	Namespace     string           `json:"namespace"`
	Created       bool             `json:"created"`
	UpToDate      bool             `json:"up_to_date"`
	Ops           []SchemaOpEntry  `json:"ops"`
	CurrentSource string           `json:"current_source"`
	PlannedSource string           `json:"planned_source"`
	Conflicts     []SchemaConflict `json:"conflicts"`
}

// SchemaApply is the `schema.apply` JSON payload: the target schema object
// written to, and the ops actually appended.
type SchemaApply struct {
	ObjectID    string          `json:"object_id"`
	Namespace   string          `json:"namespace"`
	Created     bool            `json:"created"`
	OpsAppended int             `json:"ops_appended"`
	Ops         []SchemaOpEntry `json:"ops"`
}
