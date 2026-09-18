package projection

import (
	"fmt"
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

// makeTombstoneFieldRules builds n distinct tombstone-strategy bool
// targets on objectType, zero-padded ("t0000", "t0001", ...) the same way
// makeLWWFieldRules (ddl_internal_test.go) builds lww ones.
func makeTombstoneFieldRules(objectType string, n int) []state.Rule {
	rules := make([]state.Rule, n)
	for i := range rules {
		field := fmt.Sprintf("t%04d", i)
		rules[i] = state.Rule{OpType: "archive", Field: field, Strategy: "tombstone", ValueType: "bool", ObjectType: objectType}
	}
	return rules
}

// TestObjectsTextClauseOneParamPerType pins WRIT-285's binding change:
// objectsTextClause now cross-joins a one-row derived table into each
// EXISTS and binds the LIKE pattern once per contributing type, not once
// per string/text column, so params must equal the number of types that
// contribute a clause, never the number of columns summed across them. A
// regression back to "params += len(columns)" needs a type with more than
// one string column to be caught — "widget" declares 50 here so it would
// be.
func TestObjectsTextClauseOneParamPerType(t *testing.T) {
	rules := map[string][]state.Rule{
		"widget": makeLWWFieldRules("widget", 50),
		"gadget": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "gadget"},
		},
	}
	desc, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}

	clause, params := objectsTextClause(desc, nil)
	if clause == "" {
		t.Fatalf("expected a non-empty text clause")
	}
	if params != 2 {
		t.Fatalf("params = %d, want 2 (one per contributing type: widget, gadget), not one per column", params)
	}
	if got := strings.Count(clause, "(SELECT ? AS p) writ_text"); got != 2 {
		t.Fatalf("expected exactly 2 derived-table cross joins (one per contributing type), got %d in clause: %s", got, clause)
	}
}

// maxParenDepth returns the deepest parenthesis nesting in s.
func maxParenDepth(s string) int {
	depth, max := 0, 0
	for _, r := range s {
		switch r {
		case '(':
			depth++
			if depth > max {
				max = depth
			}
		case ')':
			depth--
		}
	}
	return max
}

// TestBalancedClausesStayShallowForWideSchema pins WRIT-285's fix against
// SQLite's SQLITE_MAX_EXPR_DEPTH directly: a flat left-deep chain's
// parenthesis nesting grows linearly with its term count, so a regression
// back to strings.Join would push this well past any bound that also holds
// for 1998 terms. 64 is comfortably above joinBalanced's O(log n) height
// for 1998 terms (~11) and comfortably below anything a flat chain of 1998
// terms could produce.
func TestBalancedClausesStayShallowForWideSchema(t *testing.T) {
	const n = 1998

	textDesc, err := buildDescriptor(map[string][]state.Rule{"widget": makeLWWFieldRules("widget", n)})
	if err != nil {
		t.Fatalf("buildDescriptor (lww): %v", err)
	}
	textClause, _ := objectsTextClause(textDesc, nil)
	if depth := maxParenDepth(textClause); depth >= 64 {
		t.Fatalf("objectsTextClause nesting depth = %d for %d columns, want < 64", depth, n)
	}

	notDeletedDesc, err := buildDescriptor(map[string][]state.Rule{"widget": makeTombstoneFieldRules("widget", n)})
	if err != nil {
		t.Fatalf("buildDescriptor (tombstone): %v", err)
	}
	notDeletedClause, _ := objectsNotDeletedClause(notDeletedDesc, nil)
	if depth := maxParenDepth(notDeletedClause); depth >= 64 {
		t.Fatalf("objectsNotDeletedClause nesting depth = %d for %d columns, want < 64", depth, n)
	}
}

// TestObjectsTextSearchManyWideTypesDoesNotExceedBindLimit is WRIT-285's
// second reproduction: objectsTextClause used to bind the LIKE pattern once
// per string/text column, so summed across enough wide types that
// approaches SQLite's SQLITE_MAX_VARIABLE_NUMBER (32766) independently of
// the expression-depth limit above. 17 types of 1998 string columns each is
// 33,966 placeholders under the old one-bind-per-column scheme — just over
// the ceiling, and the minimum type count that reproduces it (WRIT-256
// caps a single type at 1998 such columns). Before WRIT-285's one-bind-
// per-type derived table this failed with "SQL logic error: too many SQL
// variables".
//
// This deliberately does not go through db.ApplySchema, unlike the wide
// cases in query_test.go: ApplySchema also emits one CREATE INDEX per
// scalar column (ddlTable.indexStatements, unconditional for any typed
// target), and 17 x 1998 of those took over 40s to execute even without
// the race detector — table/index creation this test has no interest in,
// since indexing has nothing to do with either the depth or the bind-count
// limit. This white-box test instead builds the same descriptor
// buildDescriptor would, creates each table with a bare CREATE TABLE
// (ddlTable.createStatement, no indexStatements), and installs the
// descriptor directly (desc is this package's own field, guarded by
// descMu) so Objects and objectsTextClause run unmodified against it —
// exercising the exact code path this ticket fixes, at a small fraction of
// ApplySchema's cost for the same schema shape.
func TestObjectsTextSearchManyWideTypesDoesNotExceedBindLimit(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	const (
		numTypes = 17
		numCols  = 1998
	)
	rules := make(map[string][]state.Rule, numTypes)
	typeNames := make([]string, numTypes)
	for i := 0; i < numTypes; i++ {
		name := fmt.Sprintf("widetype%02d", i)
		typeNames[i] = name
		rules[name] = makeLWWFieldRules(name, numCols)
	}

	desc, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}
	for _, tbl := range desc.allTables() {
		if _, err := db.db.Exec(tbl.createStatement()); err != nil {
			t.Fatalf("create table %s: %v", tbl.Name, err)
		}
	}
	db.descMu.Lock()
	db.desc = desc
	db.descMu.Unlock()

	target := typeNames[numTypes/2]
	lastCol := fmt.Sprintf("f_f%04d", numCols-1)
	if _, err := db.db.Exec(
		"INSERT INTO objects (object_id, object_type, op_count, author_name, author_email, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		"match-1", target, 1, "Alice Smith", "alice@example.com", int64(1000), int64(1000),
	); err != nil {
		t.Fatalf("insert objects row: %v", err)
	}
	if _, err := db.db.Exec(
		"INSERT INTO "+quoteIdent("o_"+target)+" (object_id, "+lastCol+") VALUES (?, ?)",
		"match-1", "a needle among many wide types",
	); err != nil {
		t.Fatalf("insert into o_%s: %v", target, err)
	}

	results, err := db.Objects(ObjectFilter{Text: "needle"})
	if err != nil {
		t.Fatalf("Objects(Text=needle) across %d %d-column types: %v", numTypes, numCols, err)
	}
	if len(results) != 1 || results[0].ObjectID != "match-1" {
		t.Fatalf("expected only match-1, got %+v", results)
	}
}
