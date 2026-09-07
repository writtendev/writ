package projection

import (
	"strings"
	"testing"
)

// TestObjectsTextClauseRestrictTypes pins round 1's restrictTypes addition
// to objectsTextClause: restricting to a single declared type must drop
// every other type's EXISTS clause and every one of its LIKE placeholders,
// not just leave them semantically dead weight. Round 2's MEDIUM-2 found
// this unpinned — the four query_test.go cases that exercise Objects'
// f.Type filter all restrict to types whose EXISTS clause could never match
// a differently-typed row anyway, so none of them could tell a restricted
// clause from an unrestricted one. This calls objectsTextClause directly
// and inspects the generated SQL fragment and param count, the only way to
// observe the restriction itself rather than its (already-redundant)
// runtime effect.
func TestObjectsTextClauseRestrictTypes(t *testing.T) {
	rules := loadTestRules(t)
	desc, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}

	allClause, allParams := objectsTextClause(desc, nil)
	if !strings.Contains(allClause, "o_review") {
		t.Fatalf("unrestricted text clause does not mention o_review: %s", allClause)
	}
	if !strings.Contains(allClause, "o_comment") {
		t.Fatalf("unrestricted text clause does not mention o_comment: %s", allClause)
	}

	reviewOnlyClause, reviewOnlyParams := objectsTextClause(desc, []string{"review"})
	if !strings.Contains(reviewOnlyClause, "o_review") {
		t.Fatalf("restricted (review) text clause does not mention o_review: %s", reviewOnlyClause)
	}
	if strings.Contains(reviewOnlyClause, "o_comment") {
		t.Fatalf("restricted (review) text clause still mentions o_comment, restrictTypes had no effect: %s", reviewOnlyClause)
	}
	if reviewOnlyParams >= allParams {
		t.Fatalf("restricted (review) text clause params = %d, want fewer than the unrestricted count %d", reviewOnlyParams, allParams)
	}
}

// TestObjectsNotDeletedClauseRestrictTypes is
// TestObjectsTextClauseRestrictTypes's counterpart for
// objectsNotDeletedClause, over the two built-in tombstone-strategy types
// (comment, section): restricting to one must drop the other's EXISTS
// clause entirely, not just make it a harmless no-op at runtime.
func TestObjectsNotDeletedClauseRestrictTypes(t *testing.T) {
	rules := loadTestRules(t)
	desc, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}

	allClause := objectsNotDeletedClause(desc, nil)
	if !strings.Contains(allClause, "o_comment") {
		t.Fatalf("unrestricted not-deleted clause does not mention o_comment: %s", allClause)
	}
	if !strings.Contains(allClause, "o_section") {
		t.Fatalf("unrestricted not-deleted clause does not mention o_section: %s", allClause)
	}

	commentOnlyClause := objectsNotDeletedClause(desc, []string{"comment"})
	if !strings.Contains(commentOnlyClause, "o_comment") {
		t.Fatalf("restricted (comment) not-deleted clause does not mention o_comment: %s", commentOnlyClause)
	}
	if strings.Contains(commentOnlyClause, "o_section") {
		t.Fatalf("restricted (comment) not-deleted clause still mentions o_section, restrictTypes had no effect: %s", commentOnlyClause)
	}
}
