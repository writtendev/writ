package projection

import (
	"strings"
	"testing"

	"github.com/writtendev/writ/internal/state"
)

// tombstoneRules is loadTestRules with one tombstone-strategy target added
// to each of its two declared types. objectsNotDeletedClause builds a clause
// only for a type that has such a target, and the counterpart test below
// needs two of them to observe restrictTypes dropping one; nothing else in
// this file's subject depends on the shape, so this extends loadTestRules
// rather than restating it.
func tombstoneRules(t *testing.T) map[string][]state.Rule {
	t.Helper()
	rules := loadTestRules(t)
	for _, objectType := range []string{"widget", "gadget"} {
		rules[objectType] = append(rules[objectType], state.Rule{
			OpType: "archive", OpVersion: 1, Field: "archived", Strategy: "tombstone", ValueType: "bool", ObjectType: objectType,
		})
	}
	return rules
}

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
// runtime effect. The tables it names are the ones loadTestRules' own
// declared types generate: writ hard-codes no object type but `schema`, so
// there is no table name here that did not come from a declaration.
func TestObjectsTextClauseRestrictTypes(t *testing.T) {
	rules := loadTestRules(t)
	desc, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}

	allClause, allParams := objectsTextClause(desc, nil)
	if !strings.Contains(allClause, "o_widget") {
		t.Fatalf("unrestricted text clause does not mention o_widget: %s", allClause)
	}
	if !strings.Contains(allClause, "o_gadget") {
		t.Fatalf("unrestricted text clause does not mention o_gadget: %s", allClause)
	}

	widgetOnlyClause, widgetOnlyParams := objectsTextClause(desc, []string{"widget"})
	if !strings.Contains(widgetOnlyClause, "o_widget") {
		t.Fatalf("restricted (widget) text clause does not mention o_widget: %s", widgetOnlyClause)
	}
	if strings.Contains(widgetOnlyClause, "o_gadget") {
		t.Fatalf("restricted (widget) text clause still mentions o_gadget, restrictTypes had no effect: %s", widgetOnlyClause)
	}
	if widgetOnlyParams >= allParams {
		t.Fatalf("restricted (widget) text clause params = %d, want fewer than the unrestricted count %d", widgetOnlyParams, allParams)
	}
}

// TestObjectsNotDeletedClauseRestrictTypes is
// TestObjectsTextClauseRestrictTypes's counterpart for
// objectsNotDeletedClause, over tombstoneRules' two tombstone-strategy
// types: restricting to one must drop the other's EXISTS clause entirely,
// not just make it a harmless no-op at runtime.
func TestObjectsNotDeletedClauseRestrictTypes(t *testing.T) {
	rules := tombstoneRules(t)
	desc, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}

	allClause, allParams := objectsNotDeletedClause(desc, nil)
	if !strings.Contains(allClause, "o_widget") {
		t.Fatalf("unrestricted not-deleted clause does not mention o_widget: %s", allClause)
	}
	if !strings.Contains(allClause, "o_gadget") {
		t.Fatalf("unrestricted not-deleted clause does not mention o_gadget: %s", allClause)
	}
	if len(allParams) != 2 {
		t.Fatalf("expected 2 object_type params (widget, gadget), got %+v", allParams)
	}

	widgetOnlyClause, widgetOnlyParams := objectsNotDeletedClause(desc, []string{"widget"})
	if !strings.Contains(widgetOnlyClause, "o_widget") {
		t.Fatalf("restricted (widget) not-deleted clause does not mention o_widget: %s", widgetOnlyClause)
	}
	if strings.Contains(widgetOnlyClause, "o_gadget") {
		t.Fatalf("restricted (widget) not-deleted clause still mentions o_gadget, restrictTypes had no effect: %s", widgetOnlyClause)
	}
	if len(widgetOnlyParams) != 1 || widgetOnlyParams[0] != "widget" {
		t.Fatalf("expected exactly one object_type param (widget), got %+v", widgetOnlyParams)
	}
}

// TestObjectsNotDeletedClauseParameterizesObjectType pins WRIT-253's fix:
// the clause used to splice a declared object type straight into the SQL
// text as a single-quoted literal ("o.object_type != '"+objectType+"'"),
// the one place a schema-derived value reached SQL neither via quoteIdent
// nor as a parameter. Nothing upstream of this package's callers grammar-
// checks a declared type — ApplySchema takes a caller-supplied rules map
// directly, and the resolver gate the rest of WRIT-253 adds lives above
// this package, not in it — so this package's own defense has to hold
// regardless. A type name carrying its own quote and comment syntax, e.g.
// "a') OR 1 --", used to break out of the literal: this asserts the
// generated clause carries no trace of the hostile text and instead binds
// it as a "?" parameter.
func TestObjectsNotDeletedClauseParameterizesObjectType(t *testing.T) {
	const hostile = `a') OR 1 --`
	rules := map[string][]state.Rule{
		hostile: {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: hostile},
			{OpType: "archive", OpVersion: 1, Field: "archived", Strategy: "tombstone", ValueType: "bool", ObjectType: hostile},
		},
	}
	desc, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}

	clause, params := objectsNotDeletedClause(desc, nil)
	if strings.Contains(clause, hostile) {
		t.Fatalf("not-deleted clause embeds the hostile object type as literal text: %s", clause)
	}
	if !strings.Contains(clause, "o.object_type != ?") {
		t.Fatalf("not-deleted clause does not parameterize object_type: %s", clause)
	}
	if len(params) != 1 || params[0] != hostile {
		t.Fatalf("expected one param carrying the hostile object type verbatim, got %+v", params)
	}
}
