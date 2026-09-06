package writ

import (
	"fmt"
	"unicode/utf8"

	"github.com/writtendev/writ/engine/internal/person"
	"github.com/writtendev/writ/engine/resolve"
	"github.com/writtendev/writ/engine/state"
)

// Anchor is a content-based comment position object (v1).
// Fold carries anchors verbatim as data per spec/fold.md §6.
type Anchor = resolve.Anchor

// OpRef identifies an operation in an object's total order sequence L
// along with its causality-monotone effective timestamp t*.
type OpRef = state.OpRef

// UnknownOp records an operation that was preserved in the DAG and participated
// in ordering and ancestry, but whose (op_type, op_version) had no declared rules
// per spec/fold.md §7.
type UnknownOp = state.UnknownOp

// ObjectState is the folded state produced by the fold driver for a collaborative object.
type ObjectState = state.ObjectState

// Review represents the materialized state of a code review collaborative object (v1),
// produced by FoldReview.
type Review = state.Review

// Revision represents a code revision push (base and head commits) on a review.
type Revision = state.Revision

// Approval represents a review verdict vote for a specific revision head.
type Approval = state.Approval

// CIStatus represents an automated check result attached to a revision head.
type CIStatus = state.CIStatus

// Comment represents the materialized state of a comment collaborative object (v1).
type Comment = state.Comment

// CommentSubject identifies the collaborative object a comment is attached to.
type CommentSubject = state.CommentSubject

// ParseCommentSubject parses raw JSON bytes into a CommentSubject, retaining the original bytes
// in Raw and preserving unknown fields.
func ParseCommentSubject(raw []byte) (CommentSubject, error) {
	return state.ParseCommentSubject(raw)
}

// CommentThread represents a node in the comment reply forest.
type CommentThread = state.CommentThread

// Issue represents the materialized state of an issue collaborative object (v1),
// produced by FoldIssue.
type Issue = state.Issue

// Link represents a cross-reference link attached to an issue.
type Link = state.Link

// Document represents the materialized state of a document collaborative object (v1),
// produced by FoldDocument.
type Document = state.Document

// Section represents the materialized state of a document section collaborative object (v1),
// produced by FoldSection.
type Section = state.Section

// Project represents the materialized state of a project collaborative object (v1),
// produced by FoldProject.
type Project = state.Project

// Cycle represents the materialized state of a cycle collaborative object (v1),
// produced by FoldCycle.
type Cycle = state.Cycle

// WorkflowState represents the materialized state of a workflow-state collaborative object (v1),
// produced by FoldWorkflowState.
type WorkflowState = state.WorkflowState

// Label represents the materialized state of a label collaborative object (v1),
// produced by FoldLabel.
type Label = state.Label

// Settings represents the materialized state of workspace-level settings (v1),
// produced by FoldSettings.
type Settings = state.Settings

// DefaultSettingsObjectID is the well-known canonical object ID for workspace settings.
const DefaultSettingsObjectID = state.DefaultSettingsObjectID

// DefaultSettings returns the default configuration for a workspace that has
// never written a settings operation.
func DefaultSettings() Settings {
	return state.DefaultSettings()
}

// ParseReference parses a reference string into its repository designator and target object ID.
func ParseReference(ref string) (string, string, error) {
	return state.ParseReference(ref)
}

// NormalizePerson normalizes a person identifier string per spec/identifiers.md
// (scheme lowercased; value trimmed and case-folded).
func NormalizePerson(s string) string {
	return state.NormalizePerson(s)
}

// normalizePersonBounded normalizes a person identifier and enforces the
// grammar and the bounds the person-id schema declares, so that a writer
// cannot append an op the schema would reject. Ops are signed, immutable and
// append-only, so a malformed or unbounded identifier is permanent
// unreclaimable weight; the only place to stop it is before the write.
//
// The value bound counts code points, matching JSON Schema maxLength, and is
// applied to the normalized value, so the engine accepts exactly what
// spec/schemas/identifiers.schema.json accepts.
//
// A violation is an error, never a truncation and never a repair: two distinct
// identifiers truncated to the same string would collapse into one person for
// assignment, approval keying and set membership, and a bare identifier
// silently given a scheme would guess at which person is meant.
//
// An identifier that normalizes to the empty string is returned as such, not
// rejected — callers omit the key, which is the schema's minLength guard.
func normalizePersonBounded(what, s string) (string, error) {
	norm := state.NormalizePerson(s)
	if norm == "" {
		return "", nil
	}
	scheme, value, _ := person.Split(norm)
	switch p := person.Check(norm); p {
	case person.Valid:
		return norm, nil
	case person.MissingScheme:
		return "", fmt.Errorf("writ: %s %s carries no scheme: a person identifier is scheme:value, for example %q or %q",
			what, quoteBounded(norm), "email:alice@example.com", "user:alice")
	case person.SchemeCharset:
		return "", fmt.Errorf("writ: %s has scheme %s, which must match [a-z][a-z0-9+.-]*", what, quoteBounded(scheme))
	case person.SchemeTooLong:
		return "", fmt.Errorf("writ: %s has a %d-character scheme, over the %d-character person identifier scheme limit",
			what, utf8.RuneCountInString(scheme), person.MaxSchemeLen)
	case person.EmptyValue:
		return "", fmt.Errorf("writ: %s has scheme %q and an empty value", what, scheme)
	case person.ValueTooLong:
		return "", fmt.Errorf("writ: %s value is %d characters, over the %d-character person identifier limit",
			what, utf8.RuneCountInString(value), person.MaxValueLen)
	default:
		return "", fmt.Errorf("writ: %s is not a conforming person identifier: %s", what, p)
	}
}

// quoteBounded quotes s for an error message without reproducing an identifier
// that may be up to 353 characters long. It is display trimming and nothing
// else: no caller ever sees the shortened string as a value.
func quoteBounded(s string) string {
	const max = 64
	if utf8.RuneCountInString(s) <= max {
		return fmt.Sprintf("%q", s)
	}
	r := []rune(s)
	return fmt.Sprintf("%q...", string(r[:max]))
}
