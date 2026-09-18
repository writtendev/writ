package projection_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/writtendev/writ/internal/projection"
	"github.com/writtendev/writ/internal/state"
)

func insertObject(t *testing.T, db *sql.DB, objectID, objectType string, opCount int, authorName, authorEmail string, createdAt, updatedAt int64) {
	t.Helper()
	execSQL(t, db, "INSERT INTO objects (object_id, object_type, op_count, author_name, author_email, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		objectID, objectType, opCount, authorName, authorEmail, createdAt, updatedAt)
}

func execSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("execSQL %q: %v", query, err)
	}
}

// TestObjectsCrossTypeFilter exercises Objects' type filter and cross-type
// text search over two schema-declared types, "ticket" and "standup" — the
// neutral example types spec/schema-source.md uses, not a builtin type
// (AGENTS.md: writ names no downstream product).
func TestObjectsCrossTypeFilter(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	rules := map[string][]state.Rule{
		"ticket": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		},
		"standup": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
		},
	}
	if err := db.ApplySchema(rules); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	rawDB := db.DB()
	insertObject(t, rawDB, "tkt-1", "ticket", 1, "Alice Smith", "alice@example.com", 1000, 1100)
	execSQL(t, rawDB, "INSERT INTO o_ticket (object_id, f_title) VALUES (?, ?)", "tkt-1", "Fix 100% CPU in loop_worker")
	insertObject(t, rawDB, "tkt-2", "ticket", 1, "Bob Jones", "bob@example.com", 1200, 1200)
	execSQL(t, rawDB, "INSERT INTO o_ticket (object_id, f_title) VALUES (?, ?)", "tkt-2", "Add feature foo_bar")
	insertObject(t, rawDB, "tkt-3", "ticket", 1, "Charlie Brown", "charlie@example.com", 1300, 1300)
	execSQL(t, rawDB, "INSERT INTO o_ticket (object_id, f_title) VALUES (?, ?)", "tkt-3", "Refactor storage layer")

	insertObject(t, rawDB, "su-1", "standup", 1, "Alice Smith", "alice@example.com", 2000, 2000)
	execSQL(t, rawDB, "INSERT INTO o_standup (object_id, f_title) VALUES (?, ?)", "su-1", "Memory leak under 100% workload")

	// Filter by type
	tktObjs, err := db.Objects(projection.ObjectFilter{Type: []string{"ticket"}})
	if err != nil {
		t.Fatalf("Objects(ticket): %v", err)
	}
	if len(tktObjs) != 3 {
		t.Fatalf("expected 3 ticket objects, got %d", len(tktObjs))
	}

	suObjs, err := db.Objects(projection.ObjectFilter{Type: []string{"standup"}})
	if err != nil {
		t.Fatalf("Objects(standup): %v", err)
	}
	if len(suObjs) != 1 {
		t.Fatalf("expected 1 standup object, got %d", len(suObjs))
	}

	// Cross-type text search, with literal '%' wildcard escaping.
	crossObjs, err := db.Objects(projection.ObjectFilter{Text: "100%"})
	if err != nil {
		t.Fatalf("Objects(100%%): %v", err)
	}
	// tkt-1 (title) and su-1 (title)
	if len(crossObjs) != 2 {
		t.Fatalf("expected 2 objects matching '100%%', got %d (%+v)", len(crossObjs), crossObjs)
	}

	// Author filter.
	byAuthor, err := db.Objects(projection.ObjectFilter{Author: []string{"alice@example.com"}})
	if err != nil {
		t.Fatalf("Objects(author): %v", err)
	}
	if len(byAuthor) != 2 {
		t.Fatalf("expected 2 objects by alice@example.com, got %d", len(byAuthor))
	}

	// Ordering and pagination.
	desc, err := db.Objects(projection.ObjectFilter{Type: []string{"ticket"}, OrderBy: projection.OrderByCreatedAtDesc})
	if err != nil {
		t.Fatalf("Objects(OrderByCreatedAtDesc): %v", err)
	}
	if len(desc) != 3 || desc[0].ObjectID != "tkt-3" || desc[2].ObjectID != "tkt-1" {
		t.Fatalf("unexpected order: %+v", desc)
	}

	paged, err := db.Objects(projection.ObjectFilter{Type: []string{"ticket"}, Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("Objects(Limit=1, Offset=1): %v", err)
	}
	if len(paged) != 1 || paged[0].ObjectID != "tkt-2" {
		t.Fatalf("unexpected page: %+v", paged)
	}
}

// TestObjectsTextClauseSkipsTypeWithNoTextTarget is WRIT-192 round 1
// MEDIUM-3 coverage: objectsTextClause (engine/projection/query.go) builds
// one EXISTS clause per declared type that has a string/text scalar
// column, and simply contributes nothing for a type that has none — a case
// query_test.go had no direct coverage of before this. "widget" declares
// only a bool field, so it must never appear in a Text-filtered result no
// matter what f.Text is, while "gadget" (a string field) is searched
// normally alongside it.
func TestObjectsTextClauseSkipsTypeWithNoTextTarget(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	rules := map[string][]state.Rule{
		"widget": {
			{OpType: "create", OpVersion: 1, Field: "active", Strategy: "lww", ValueType: "bool", ObjectType: "widget"},
		},
		"gadget": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "gadget"},
		},
	}
	if err := db.ApplySchema(rules); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	rawDB := db.DB()
	insertObject(t, rawDB, "widget-1", "widget", 1, "Alice Smith", "alice@example.com", 1000, 1000)
	execSQL(t, rawDB, "INSERT INTO o_widget (object_id, f_active) VALUES (?, ?)", "widget-1", 1)

	insertObject(t, rawDB, "gadget-1", "gadget", 1, "Bob Jones", "bob@example.com", 2000, 2000)
	execSQL(t, rawDB, "INSERT INTO o_gadget (object_id, f_title) VALUES (?, ?)", "gadget-1", "a shiny gadget")

	results, err := db.Objects(projection.ObjectFilter{Text: "shiny"})
	if err != nil {
		t.Fatalf("Objects(Text=shiny): %v", err)
	}
	if len(results) != 1 || results[0].ObjectID != "gadget-1" {
		t.Fatalf("expected only gadget-1 to match, got %+v", results)
	}

	// Restricted to the text-less type alone, objectsTextClause has no
	// column to build a clause from at all: the query must fall to "found
	// nothing" (query.go's "AND 0" branch) rather than error or match
	// everything.
	none, err := db.Objects(projection.ObjectFilter{Type: []string{"widget"}, Text: "shiny"})
	if err != nil {
		t.Fatalf("Objects(Type=widget, Text=shiny): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no matches restricted to widget, got %+v", none)
	}
}

// TestObjectsNotDeletedTwoTombstoneTargets is WRIT-192 round 1 MEDIUM-3
// coverage, and also pins MEDIUM-2's fix: objectsNotDeletedClause's doc
// comment used to claim a multi-tombstone type is excluded only when
// *every* one of its tombstone targets folded true, but the generated
// SQL ANDs "not deleted" per column, so a *single* true column excludes
// the object. "ticket" declares two independent tombstone targets to
// exercise exactly that: only the row where both are false survives the
// default !IncludeDeleted filter.
func TestObjectsNotDeletedTwoTombstoneTargets(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	rules := map[string][]state.Rule{
		"ticket": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "ticket"},
			{OpType: "archive", OpVersion: 1, Field: "archived", Strategy: "tombstone", ValueType: "bool", ObjectType: "ticket"},
			{OpType: "purge", OpVersion: 1, Field: "purged", Strategy: "tombstone", ValueType: "bool", ObjectType: "ticket"},
		},
	}
	if err := db.ApplySchema(rules); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	rawDB := db.DB()
	seed := []struct {
		id               string
		title            string
		archived, purged int
	}{
		{"ticket-live", "still open", 0, 0},
		{"ticket-archived", "archived only", 1, 0},
		{"ticket-purged", "purged only", 0, 1},
		{"ticket-both", "archived and purged", 1, 1},
	}
	for i, s := range seed {
		insertObject(t, rawDB, s.id, "ticket", 1, "Alice Smith", "alice@example.com", int64(1000+i), int64(1000+i))
		execSQL(t, rawDB, "INSERT INTO o_ticket (object_id, f_title, f_archived, f_purged) VALUES (?, ?, ?, ?)",
			s.id, s.title, s.archived, s.purged)
	}

	live, err := db.Objects(projection.ObjectFilter{Type: []string{"ticket"}})
	if err != nil {
		t.Fatalf("Objects(ticket, IncludeDeleted=false): %v", err)
	}
	if len(live) != 1 || live[0].ObjectID != "ticket-live" {
		t.Fatalf("expected only ticket-live to survive the default filter, got %+v", live)
	}

	all, err := db.Objects(projection.ObjectFilter{Type: []string{"ticket"}, IncludeDeleted: true})
	if err != nil {
		t.Fatalf("Objects(ticket, IncludeDeleted=true): %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected all 4 tickets with IncludeDeleted=true, got %d (%+v)", len(all), all)
	}
}

// TestObjectsSoftDeleteWideningOverSharedRules pins WRIT-189 round 3
// MAJOR-2's generalization of the !IncludeDeleted filter from a single
// hard-coded table/column literal to every declared type with a
// tombstone-strategy target — round 1 found the widening correct but
// unpinned by any test, so this is that test. It runs over testRules()'
// "widget", the one type in this package's shared rule index whose declared
// vocabulary carries a tombstone target (`archived`) alongside ordinary
// scalar fields, so the clause has to be derived from the descriptor rather
// than from the filter's own type list.
func TestObjectsSoftDeleteWideningOverSharedRules(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()
	if err := db.ApplySchema(testRules()); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	rawDB := db.DB()
	insertObject(t, rawDB, "w-live", "widget", 1, "Alice Smith", "alice@example.com", 4000, 4000)
	execSQL(t, rawDB, "INSERT INTO o_widget (object_id, f_title, f_description, f_archived) VALUES (?, ?, ?, ?)",
		"w-live", "Introduction", "still here", 0)
	insertObject(t, rawDB, "w-deleted", "widget", 1, "Bob Jones", "bob@example.com", 4100, 4100)
	execSQL(t, rawDB, "INSERT INTO o_widget (object_id, f_title, f_description, f_archived) VALUES (?, ?, ?, ?)",
		"w-deleted", "Withdrawn", "archived away", 1)

	live, err := db.Objects(projection.ObjectFilter{Type: []string{"widget"}})
	if err != nil {
		t.Fatalf("Objects(widget, IncludeDeleted=false): %v", err)
	}
	if len(live) != 1 || live[0].ObjectID != "w-live" {
		t.Fatalf("expected only w-live to survive the default filter, got %+v", live)
	}

	all, err := db.Objects(projection.ObjectFilter{Type: []string{"widget"}, IncludeDeleted: true})
	if err != nil {
		t.Fatalf("Objects(widget, IncludeDeleted=true): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected both widgets with IncludeDeleted=true, got %d (%+v)", len(all), all)
	}
}

// TestObjectsWithheldTypeCoexistsWithDeclaredTypes is WRIT-192 round 1
// MEDIUM-3 coverage for the third named case: a withheld type (WRIT-189's
// buildTypeDescriptor withholds a whole type's tables rather than guess, on
// a colliding identifier or an ambiguous append shape) alongside an
// ordinary declared type. "widget" here is withheld by the same ambiguous-
// append-field shape TestAmbiguousAppendFieldWithholdsTables uses: two
// append rules binding target "note" to two different Fields under one
// (op_type, op_version). objectsTextClause and objectsNotDeletedClause both
// walk desc.order, which never lists a withheld type, so neither
// contributes a clause for "widget" at all — it must never be excluded by
// either filter (there is no table for either clause to reference), and a
// plain, unfiltered Objects() call must still surface it from the base
// objects row materializeObject always writes, regardless of the missing
// per-type descriptor.
func TestObjectsWithheldTypeCoexistsWithDeclaredTypes(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	rules := map[string][]state.Rule{
		"widget": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"},
			{OpType: "note", OpVersion: 1, Field: "a", Target: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"},
			{OpType: "note", OpVersion: 1, Field: "b", Target: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"},
		},
		"gadget": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "gadget"},
		},
	}
	if err := db.ApplySchema(rules); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	rawDB := db.DB()
	// widget is withheld: no o_widget table exists to insert into, only the
	// base cross-type index row materializeObject always writes.
	insertObject(t, rawDB, "widget-1", "widget", 1, "Alice Smith", "alice@example.com", 1000, 1000)
	insertObject(t, rawDB, "gadget-1", "gadget", 1, "Bob Jones", "bob@example.com", 2000, 2000)
	execSQL(t, rawDB, "INSERT INTO o_gadget (object_id, f_title) VALUES (?, ?)", "gadget-1", "a shiny gadget")

	all, err := db.Objects(projection.ObjectFilter{})
	if err != nil {
		t.Fatalf("Objects(): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected both the withheld-type and declared-type objects, got %d (%+v)", len(all), all)
	}

	// The default !IncludeDeleted filter must not exclude widget-1: no
	// tombstone clause exists for a withheld type, so it is never a
	// candidate for exclusion.
	notDeleted, err := db.Objects(projection.ObjectFilter{IncludeDeleted: false})
	if err != nil {
		t.Fatalf("Objects(IncludeDeleted=false): %v", err)
	}
	if len(notDeleted) != 2 {
		t.Fatalf("expected the withheld-type object to survive the default filter untouched, got %d (%+v)", len(notDeleted), notDeleted)
	}

	// A withheld type contributes no text clause, so it can never match a
	// text search; the declared type alongside it still can.
	textResults, err := db.Objects(projection.ObjectFilter{Text: "shiny"})
	if err != nil {
		t.Fatalf("Objects(Text=shiny): %v", err)
	}
	if len(textResults) != 1 || textResults[0].ObjectID != "gadget-1" {
		t.Fatalf("expected only gadget-1 to match a text search, got %+v", textResults)
	}
}

// TestObjectsHostileTypeNameNotDeletedAndLimit is WRIT-253's end-to-end pin
// for the query.go fix, at the layer the resolver gate in engine/schema.go
// never reaches on its own: ApplySchema takes a caller-supplied rules map
// directly, with no grammar check on its keys, so this package's own
// query-building has to be safe regardless of what a declared object type
// looks like. Before the fix, objectsNotDeletedClause spliced the type
// into the SQL text as "o.object_type != '<t>'"; a type carrying its own
// quote and comment syntax, "a') OR 1 --", broke out of that literal,
// disabling the default !IncludeDeleted filter and turning "--" into a SQL
// line comment that silently dropped everything after it, including
// LIMIT — exactly WRIT-253's reproduction.
func TestObjectsHostileTypeNameNotDeletedAndLimit(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	const hostile = `a') OR 1 --`
	rules := map[string][]state.Rule{
		hostile: {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: hostile},
			{OpType: "archive", OpVersion: 1, Field: "archived", Strategy: "tombstone", ValueType: "bool", ObjectType: hostile},
		},
	}
	if err := db.ApplySchema(rules); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}

	// The generated table name follows buildTypeDescriptor's documented
	// "o_" + strings.ReplaceAll(objectType, "-", "_") scheme (ddl.go's
	// quoteIdent doc comment); quoted here in the test exactly as
	// quoteIdent quotes it in production, since a hostile type name is not
	// a valid unquoted SQL identifier.
	rawDB := db.DB()
	tableName := "o_" + strings.ReplaceAll(hostile, "-", "_")
	quotedTable := `"` + strings.ReplaceAll(tableName, `"`, `""`) + `"`

	seed := []struct {
		id       string
		archived int
	}{
		{"obj-live-1", 0},
		{"obj-live-2", 0},
		{"obj-deleted", 1},
	}
	for i, s := range seed {
		insertObject(t, rawDB, s.id, hostile, 1, "Alice Smith", "alice@example.com", int64(1000+i), int64(1000+i))
		execSQL(t, rawDB, "INSERT INTO "+quotedTable+" (object_id, f_title, f_archived) VALUES (?, ?, ?)",
			s.id, "title for "+s.id, s.archived)
	}

	live, err := db.Objects(projection.ObjectFilter{Type: []string{hostile}})
	if err != nil {
		t.Fatalf("Objects(hostile type, IncludeDeleted=false): %v", err)
	}
	if len(live) != 2 {
		t.Fatalf("expected 2 live objects surviving the default !IncludeDeleted filter, got %d (%+v)", len(live), live)
	}

	limited, err := db.Objects(projection.ObjectFilter{Type: []string{hostile}, Limit: 1})
	if err != nil {
		t.Fatalf("Objects(hostile type, Limit=1): %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("expected LIMIT 1 to be honoured, got %d rows (%+v) — WRIT-253's injected object type used to comment out LIMIT entirely", len(limited), limited)
	}

	all, err := db.Objects(projection.ObjectFilter{Type: []string{hostile}, IncludeDeleted: true})
	if err != nil {
		t.Fatalf("Objects(hostile type, IncludeDeleted=true): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected all 3 objects with IncludeDeleted=true, got %d (%+v)", len(all), all)
	}
}
