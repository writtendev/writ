package writ

import (
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/state"
)

// Rule specifies the merge strategy and value type for an (op_type, op_version, field) tuple.
type Rule = state.Rule

// Sentinels re-exported from internal/fold.
var (
	// ErrCycle is returned when the operation graph contains a directed cycle.
	ErrCycle = state.ErrCycle

	// ErrDuplicateOpID is returned when the input set contains duplicate operation IDs.
	ErrDuplicateOpID = state.ErrDuplicateOpID

	// ErrMixedObjects is returned when the input set spans multiple object IDs.
	ErrMixedObjects = state.ErrMixedObjects
)

// Fold executes deterministic fold reduction on an input set of operations
// against declared field merge rules, returning the resulting ObjectState.
func Fold(ops []codec.Op, rules []Rule) (ObjectState, error) {
	return state.Fold(ops, rules)
}

// FoldReview executes deterministic fold reduction on an input set of operations
// for a code review collaborative object, returning the materialized Review state.
func FoldReview(ops []codec.Op) (Review, error) {
	return state.FoldReview(ops)
}

// FoldComment reduces operations for a single comment object into a typed Comment.
// Subject and anchor payloads are preserved byte-identically from the winning create op.
func FoldComment(ops []codec.Op) (Comment, error) {
	return state.FoldComment(ops)
}

// FoldComments groups operations across multiple comment objects, folds each comment,
// and constructs the reply forest as a slice of CommentThread roots.
func FoldComments(ops []codec.Op) ([]CommentThread, error) {
	return state.FoldComments(ops)
}

// FoldIssue executes deterministic fold reduction on an input set of operations
// for an issue collaborative object, returning the materialized Issue state.
func FoldIssue(ops []codec.Op) (Issue, error) {
	return state.FoldIssue(ops)
}

// FoldProject executes deterministic fold reduction on an input set of operations
// for a project collaborative object, returning the materialized Project state.
func FoldProject(ops []codec.Op) (Project, error) {
	return state.FoldProject(ops)
}

// FoldCycle executes deterministic fold reduction on an input set of operations
// for a cycle collaborative object, returning the materialized Cycle state.
func FoldCycle(ops []codec.Op) (Cycle, error) {
	return state.FoldCycle(ops)
}

// FoldWorkflowState executes deterministic fold reduction on an input set of operations
// for a workflow-state collaborative object, returning the materialized WorkflowState state.
func FoldWorkflowState(ops []codec.Op) (WorkflowState, error) {
	return state.FoldWorkflowState(ops)
}

// FoldLabel executes deterministic fold reduction on an input set of operations
// for a label collaborative object, returning the materialized Label state.
func FoldLabel(ops []codec.Op) (Label, error) {
	return state.FoldLabel(ops)
}

// FoldSettings executes deterministic fold reduction on an input set of operations
// for a settings collaborative object, returning the materialized Settings state.
func FoldSettings(ops []codec.Op) (Settings, error) {
	return state.FoldSettings(ops)
}

// FoldSchema executes deterministic fold reduction on an input set of operations
// for a schema collaborative object, returning the materialized Schema state.
func FoldSchema(ops []codec.Op) (Schema, error) {
	return state.FoldSchema(ops)
}

// ReviewRules returns the built-in field merge rules for the review-ops vocabulary (v1).
func ReviewRules() []Rule {
	return state.ReviewRules()
}

// IssueRules returns the built-in field merge rules for the issue-ops vocabulary (v1).
func IssueRules() []Rule {
	return state.IssueRules()
}

// ProjectRules returns the built-in field merge rules for the project vocabulary (v1).
func ProjectRules() []Rule {
	return state.ProjectRules()
}

// CycleRules returns the built-in field merge rules for the cycle vocabulary (v1).
func CycleRules() []Rule {
	return state.CycleRules()
}

// WorkflowStateRules returns the built-in field merge rules for the workflow-state vocabulary (v1).
func WorkflowStateRules() []Rule {
	return state.WorkflowStateRules()
}

// LabelRules returns the built-in field merge rules for the label vocabulary (v1).
func LabelRules() []Rule {
	return state.LabelRules()
}

// SettingsRules returns the built-in field merge rules for the settings vocabulary (v1).
func SettingsRules() []Rule {
	return state.SettingsRules()
}

// SchemaRules returns the built-in field merge rules for the schema vocabulary
// (v1) — the one rule table that never comes from the log
// (spec/schema-ops.md §Bootstrap).
func SchemaRules() []Rule {
	return state.SchemaRules()
}

// FoldDocument executes deterministic fold reduction on an input set of operations
// for a document collaborative object, returning the materialized Document state.
func FoldDocument(ops []codec.Op) (Document, error) {
	return state.FoldDocument(ops)
}

// FoldSection executes deterministic fold reduction on an input set of operations
// for a section collaborative object, returning the materialized Section state.
func FoldSection(ops []codec.Op) (Section, error) {
	return state.FoldSection(ops)
}

// DocumentRules returns the built-in field merge rules for the document vocabulary (v1).
func DocumentRules() []Rule {
	return state.DocumentRules()
}

// SectionRules returns the built-in field merge rules for the section vocabulary (v1).
func SectionRules() []Rule {
	return state.SectionRules()
}
