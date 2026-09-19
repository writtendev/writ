package projection_test

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/identity"
	"github.com/writtendev/writ/internal/projection"
	"github.com/writtendev/writ/internal/state"
)

// This file restores, generically, the corpus-wide "Projection == Fold"
// agreement check that fixtures_test.go's
// TestFixturesIncrementalVsColdAndFoldAgreement used to run against the
// (now-deleted) typed fold/projection readers (round 1 MAJOR
// finding). The property under test was never typed: it is writeTypeRow's
// inversion of state.Fold's output into generated scalar columns, keyed-lww
// groups, per-target append tables, and unknown_ops — entirely generic
// machinery — so this cross-checks it directly against state.Fold over a
// schema-declared type the engine has never heard of ("record"), built from
// two independent writer identities' concurrent and causally-ordered ops,
// exactly as makeWidgetOp builds ops elsewhere in this package: hand-built
// codec.Op values with an explicit parent DAG, reaching Refresh through
// WithEnumOverrideForTest with no producer-validation gate to route around
// (op-envelope's producer validation lives in dag.Store.Append/codec.BuildCommit,
// neither of which this touches).

func makeRecordEnv(objID, opType string, body map[string]any) codec.Envelope {
	bodyRaw, _ := json.Marshal(body)
	env := codec.Envelope{
		ObjectID:   objID,
		ObjectType: "record",
		OpType:     opType,
		OpVersion:  1,
		Body:       bodyRaw,
	}
	raw, _ := codec.EncodePayload(env)
	env.Raw = raw
	return env
}

func makeRecordOp(objID, id string, parents []string, opType string, body map[string]any, authorName, authorEmail string, when time.Time) codec.Op {
	env := makeRecordEnv(objID, opType, body)
	author := codec.Identity{Name: authorName, Email: authorEmail, When: when}
	return codec.Op{
		Envelope:  env,
		ID:        id,
		Parents:   parents,
		Author:    author,
		Committer: author,
		Message:   "writ: " + opType + " record/" + objID + "\n",
	}
}

func queryStrings(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan %q: %v", query, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %q: %v", query, err)
	}
	return out
}

// TestProjectionMatchesFoldAcrossStrategies builds one "record" object from
// two writers (Alice, Bob) exercising lww (title, and a genuinely
// concurrent status write to force a real tie-break rather than a
// single-writer sequential one), set-observed-remove (concurrent assignee
// adds from both writers), tombstone (archived), append (concurrent notes
// from both writers), keyed-lww (per-component triage level, one key
// causally overridden by the other writer), and an unknown op type
// ("escalate", which no rule below names) — and cross-checks every one of
// those against the pure state.Fold over the identical op set, reading the
// materialized values back with plain SQL exactly as an external tool
// would, never through a typed reader.
func TestProjectionMatchesFoldAcrossStrategies(t *testing.T) {
	const objID = "record-1"
	base := time.Unix(1700000000, 0).UTC()
	alice := func(id string, parents []string, opType string, body map[string]any, offset int) codec.Op {
		return makeRecordOp(objID, id, parents, opType, body, "Alice", "alice@example.com", base.Add(time.Duration(offset)*time.Second))
	}
	bob := func(id string, parents []string, opType string, body map[string]any, offset int) codec.Op {
		return makeRecordOp(objID, id, parents, opType, body, "Bob", "bob@example.com", base.Add(time.Duration(offset)*time.Second))
	}

	opCreate := alice("op-create", nil, "create", map[string]any{"title": "Q3 rollout"}, 0)

	// Concurrent LWW: both branch from opCreate, neither observes the other.
	opStatusAlice := alice("op-status-alice", []string{opCreate.ID}, "set-status", map[string]any{"status": "in-review"}, 1)
	opStatusBob := bob("op-status-bob", []string{opCreate.ID}, "set-status", map[string]any{"status": "blocked"}, 1)

	// Concurrent OR-set adds.
	opAssignAlice := alice("op-assign-alice", []string{opStatusAlice.ID}, "assign", map[string]any{"add": []any{"alice"}}, 2)
	opAssignBob := bob("op-assign-bob", []string{opStatusBob.ID}, "assign", map[string]any{"add": []any{"bob"}}, 2)

	// Concurrent appends.
	opNoteAlice := alice("op-note-alice", []string{opAssignAlice.ID}, "note", map[string]any{"note": "kickoff done"}, 3)
	opNoteBob := bob("op-note-bob", []string{opAssignBob.ID}, "note", map[string]any{"note": "risk flagged"}, 3)

	// Tombstone, causally merging both branches above.
	opArchive := alice("op-archive", []string{opNoteAlice.ID, opNoteBob.ID}, "archive", map[string]any{"archived": true}, 4)

	// Keyed-lww: two keys, one causally overridden by the other writer.
	opTriageFrontend1 := alice("op-triage-fe1", []string{opArchive.ID}, "triage", map[string]any{"component": "frontend", "level": "high"}, 5)
	opTriageFrontend2 := bob("op-triage-fe2", []string{opTriageFrontend1.ID}, "triage", map[string]any{"component": "frontend", "level": "medium"}, 6)
	opTriageBackend := alice("op-triage-be", []string{opArchive.ID}, "triage", map[string]any{"component": "backend", "level": "low"}, 5)

	// Unknown op type: no rule below names "escalate" for "record" —
	// forward-compat preservation, not an error.
	opUnknown := alice("op-unknown", []string{opTriageBackend.ID}, "escalate", map[string]any{"reason": "urgent"}, 7)

	ops := []codec.Op{
		opCreate, opStatusAlice, opStatusBob, opAssignAlice, opAssignBob,
		opNoteAlice, opNoteBob, opArchive, opTriageFrontend1, opTriageFrontend2,
		opTriageBackend, opUnknown,
	}

	recordRules := []state.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "record"},
		{OpType: "set-status", OpVersion: 1, Field: "status", Strategy: "lww", ValueType: "string", ObjectType: "record"},
		{OpType: "assign", OpVersion: 1, Field: "add", Target: "assignees", Strategy: "set-observed-remove", ValueType: "string", ObjectType: "record"},
		{OpType: "assign", OpVersion: 1, Field: "remove", Target: "assignees", Strategy: "set-observed-remove", ValueType: "string", ObjectType: "record"},
		{OpType: "archive", OpVersion: 1, Field: "archived", Strategy: "tombstone", ValueType: "bool", ObjectType: "record"},
		{OpType: "note", OpVersion: 1, Field: "note", Strategy: "append", ValueType: "string", ObjectType: "record"},
		{OpType: "triage", OpVersion: 1, Field: "level", Strategy: "keyed-lww", Key: []string{"component"}, KeyTypes: map[string]string{"component": "string"}, ValueType: "string", ObjectType: "record"},
	}
	rulesByType := map[string][]state.Rule{"record": recordRules}

	// The reference this generic materialization has to match.
	want, err := state.Fold(ops, recordRules)
	if err != nil {
		t.Fatalf("state.Fold: %v", err)
	}

	_, store := createTestStore(t, "0123456789abcdef")
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("projection.Open: %v", err)
	}
	defer db.Close()

	enumRes := &dag.EnumerateResult{
		Ops: map[string][]codec.Op{objID: ops},
		Cursors: dag.CursorSet{
			"refs/writ/0123456789abcdef/record": "op-unknown",
		},
		DecodedCommits: len(ops),
	}
	if _, err := db.Refresh(store, projection.WithSchema(rulesByType), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	rawDB := db.DB()

	// lww: title, status.
	wantTitle, _ := want.State["title"].(string)
	wantStatus, _ := want.State["status"].(string)
	var gotTitle, gotStatus sql.NullString
	var gotArchived sql.NullInt64
	if err := rawDB.QueryRow("SELECT f_title, f_status, f_archived FROM o_record WHERE object_id = ?", objID).
		Scan(&gotTitle, &gotStatus, &gotArchived); err != nil {
		t.Fatalf("query o_record: %v", err)
	}
	if wantTitle == "" || gotTitle.String != wantTitle {
		t.Fatalf("f_title = %q, want (state.Fold) %q", gotTitle.String, wantTitle)
	}
	if wantStatus == "" || gotStatus.String != wantStatus {
		t.Fatalf("f_status = %q, want (state.Fold) %q — projection and fold disagree on the concurrent LWW tie-break winner", gotStatus.String, wantStatus)
	}

	// tombstone: archived.
	wantArchived, _ := want.State["archived"].(bool)
	if !wantArchived {
		t.Fatalf("test setup: state.Fold's archived = %v, want true", want.State["archived"])
	}
	if gotArchived.Int64 == 0 {
		t.Fatalf("f_archived = %v, want true (state.Fold agrees archived=true)", gotArchived)
	}

	// set-observed-remove: assignees, from two concurrent writers.
	wantAssignees, _ := want.State["assignees"].([]string)
	gotAssignees := queryStrings(t, rawDB, "SELECT item FROM o_record__assignees WHERE object_id = ? ORDER BY item ASC", objID)
	if !reflect.DeepEqual(gotAssignees, wantAssignees) {
		t.Fatalf("o_record__assignees = %v, want (state.Fold) %v", gotAssignees, wantAssignees)
	}
	if !reflect.DeepEqual(wantAssignees, []string{"alice", "bob"}) {
		t.Fatalf("test setup: state.Fold's assignees = %v, want [alice bob] — both concurrent adds must survive", wantAssignees)
	}

	// append: note, from two concurrent writers — order matters here, so
	// this is compared positionally, not as a set.
	wantNotes, _ := want.State["note"].([]any)
	gotNotes := queryStrings(t, rawDB, "SELECT value FROM o_record__note WHERE object_id = ? ORDER BY op_seq ASC, entry_idx ASC", objID)
	if len(gotNotes) != len(wantNotes) {
		t.Fatalf("o_record__note has %d rows, state.Fold's note has %d entries: got %v, want %v", len(gotNotes), len(wantNotes), gotNotes, wantNotes)
	}
	for i := range wantNotes {
		if gotNotes[i] != wantNotes[i] {
			t.Fatalf("o_record__note[%d] = %q, want (state.Fold) %q — append ordering disagrees with the pure fold", i, gotNotes[i], wantNotes[i])
		}
	}

	// keyed-lww: triage level per component, one key causally overridden.
	wantLevels, ok := want.State["level"].([]any)
	if !ok || len(wantLevels) != 2 {
		t.Fatalf("test setup: state.Fold's level = %#v, want 2 keyed entries", want.State["level"])
	}
	rows, err := rawDB.Query("SELECT k_component, f_level FROM o_record__k_component WHERE object_id = ? ORDER BY k_component ASC", objID)
	if err != nil {
		t.Fatalf("query o_record__k_component: %v", err)
	}
	defer rows.Close()
	var gotLevels []struct{ component, level string }
	for rows.Next() {
		var c, l string
		if err := rows.Scan(&c, &l); err != nil {
			t.Fatalf("scan o_record__k_component: %v", err)
		}
		gotLevels = append(gotLevels, struct{ component, level string }{c, l})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate o_record__k_component: %v", err)
	}
	if len(gotLevels) != 2 {
		t.Fatalf("o_record__k_component has %d rows, want 2: %+v", len(gotLevels), gotLevels)
	}
	for i, entry := range wantLevels {
		m, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("state.Fold's level[%d] = %#v, want a keyed-lww entry map", i, entry)
		}
		key, _ := m["key"].([]string)
		val, _ := m["value"].(string)
		if len(key) != 1 || key[0] != gotLevels[i].component || val != gotLevels[i].level {
			t.Fatalf("o_record__k_component row %d = %+v, want key %v value %q (state.Fold)", i, gotLevels[i], key, val)
		}
	}
	if gotLevels[0].component != "backend" || gotLevels[0].level != "low" {
		t.Fatalf("expected backend/low first (key-sorted), got %+v", gotLevels[0])
	}
	if gotLevels[1].component != "frontend" || gotLevels[1].level != "medium" {
		t.Fatalf("expected frontend to have been overridden to medium by the causally later write, got %+v", gotLevels[1])
	}

	// Unknown op type: preserved and ignored, not dropped, and not
	// silently folded into any known column.
	if len(want.UnknownOps) != 1 || want.UnknownOps[0].Commit != opUnknown.ID {
		t.Fatalf("state.Fold's UnknownOps = %+v, want exactly [%s]", want.UnknownOps, opUnknown.ID)
	}
	gotUnknown := queryStrings(t, rawDB, "SELECT op_id FROM unknown_ops WHERE object_id = ? ORDER BY op_index ASC", objID)
	if len(gotUnknown) != 1 || gotUnknown[0] != opUnknown.ID {
		t.Fatalf("unknown_ops op_ids = %v, want [%s] (state.Fold agrees)", gotUnknown, opUnknown.ID)
	}
}

// TestProjectionMatchesFoldOnTruncatedAncestry is the "truncated ancestry"
// leg of the same restored cross-check: a projection built from only a
// prefix of one writer's chain (a partial fetch, or a fetch racing an
// in-flight push) must still materialize a state that agrees with
// state.Fold over that identical prefix — not the full history, which the
// partial projection never saw.
func TestProjectionMatchesFoldOnTruncatedAncestry(t *testing.T) {
	const objID = "record-2"
	base := time.Unix(1700000000, 0).UTC()

	opCreate := makeRecordOp(objID, "op-create", nil, "create", map[string]any{"title": "Truncated"}, "Alice", "alice@example.com", base)
	opNote1 := makeRecordOp(objID, "op-note-1", []string{opCreate.ID}, "note", map[string]any{"note": "first"}, "Alice", "alice@example.com", base.Add(1*time.Second))
	// opNote2 exists in the log but is never fetched by this projection
	// build below — the "truncated" half of the ancestry.
	_ = makeRecordOp(objID, "op-note-2", []string{opNote1.ID}, "note", map[string]any{"note": "second"}, "Alice", "alice@example.com", base.Add(2*time.Second))

	recordRules := []state.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "record"},
		{OpType: "note", OpVersion: 1, Field: "note", Strategy: "append", ValueType: "string", ObjectType: "record"},
	}
	rulesByType := map[string][]state.Rule{"record": recordRules}

	truncatedOps := []codec.Op{opCreate, opNote1}

	want, err := state.Fold(truncatedOps, recordRules)
	if err != nil {
		t.Fatalf("state.Fold: %v", err)
	}
	wantNotes, _ := want.State["note"].([]any)
	if !reflect.DeepEqual(wantNotes, []any{"first"}) {
		t.Fatalf("test setup: state.Fold's note over the truncated prefix = %v, want [first]", wantNotes)
	}

	_, store := createTestStore(t, "0123456789abcdef")
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("projection.Open: %v", err)
	}
	defer db.Close()

	enumRes := &dag.EnumerateResult{
		Ops: map[string][]codec.Op{objID: truncatedOps},
		Cursors: dag.CursorSet{
			string(dag.LocalRefName(identity.WriterID("0123456789abcdef"), "record")): "op-note-1",
		},
		DecodedCommits: len(truncatedOps),
	}
	if _, err := db.Refresh(store, projection.WithSchema(rulesByType), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	rawDB := db.DB()
	var gotTitle string
	if err := rawDB.QueryRow("SELECT f_title FROM o_record WHERE object_id = ?", objID).Scan(&gotTitle); err != nil {
		t.Fatalf("query o_record: %v", err)
	}
	if gotTitle != "Truncated" {
		t.Fatalf("f_title = %q, want %q", gotTitle, "Truncated")
	}

	gotNotes := queryStrings(t, rawDB, "SELECT value FROM o_record__note WHERE object_id = ? ORDER BY op_seq ASC, entry_idx ASC", objID)
	if len(gotNotes) != 1 || gotNotes[0] != "first" {
		t.Fatalf("o_record__note over the truncated prefix = %v, want [first] (state.Fold agrees, and must not see the un-fetched \"second\" op)", gotNotes)
	}
}

// TestMismatchedValueTypeReachesTheColumnAsText is WRIT-279's own executable
// statement for columnValue's three type-mismatch fallthroughs
// (materialize.go: int, number, bool): a folded value that contradicts its
// target's declared value_type now reaches the projection as text rather
// than as SQL NULL.
//
// Non-vacuity: on main at 6555fc5, columnValue("int", "abc") returned nil
// and no other code path could put the value there, so the assertion below
// that a mismatched column is non-NULL and equals the written string is red
// on main and green after this change — reverting only the three
// materialize.go lines this ticket touches turns it red again. No other
// test in this tree exercises that fallthrough, so nothing else moves.
//
// Every case's value is deliberately non-numeric-looking, so SQLite's
// column affinity cannot convert it, except "int-numeric-looking", which is
// deliberately the opposite: spec/fold.md §7.1 keeps state.Fold and the
// projection agreeing the value is present, but SQLite's INTEGER affinity
// still converts "42" into the integer 42 on the way in, so the two
// surfaces disagree on *type* for a value that merely looks numeric. That
// residual is not closed by this fix (see columnValue's doc comment) and
// this case pins it deliberately, so typeof() must never be asserted "text"
// universally.
func TestMismatchedValueTypeReachesTheColumnAsText(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()

	rules := []state.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "record"},
		{OpType: "note", OpVersion: 1, Field: "count", Strategy: "lww", ValueType: "int", ObjectType: "record"},
		{OpType: "note", OpVersion: 1, Field: "amount", Strategy: "lww", ValueType: "number", ObjectType: "record"},
		{OpType: "note", OpVersion: 1, Field: "flag", Strategy: "lww", ValueType: "bool", ObjectType: "record"},
	}
	rulesByType := map[string][]state.Rule{"record": rules}

	cases := []struct {
		name       string
		objID      string
		field      string
		column     string
		value      string
		wantTypeof string
	}{
		{"int", "record-mismatch-int", "count", "f_count", "abc", "text"},
		{"number", "record-mismatch-number", "amount", "f_amount", "not-a-number", "text"},
		{"bool", "record-mismatch-bool", "flag", "f_flag", "yes", "text"},
		{"int-numeric-looking", "record-mismatch-int-numeric", "count", "f_count", "42", "integer"},
	}

	byObj := make(map[string][]codec.Op, len(cases))
	var lastOpID string
	for _, tc := range cases {
		opCreate := makeRecordOp(tc.objID, "op-create-"+tc.name, nil, "create", map[string]any{"title": "T"}, "Alice", "alice@example.com", base)
		opNote := makeRecordOp(tc.objID, "op-note-"+tc.name, []string{opCreate.ID}, "note", map[string]any{tc.field: tc.value}, "Alice", "alice@example.com", base.Add(1*time.Second))
		byObj[tc.objID] = []codec.Op{opCreate, opNote}
		lastOpID = opNote.ID

		want, err := state.Fold(byObj[tc.objID], rules)
		if err != nil {
			t.Fatalf("%s: state.Fold failed: %v", tc.name, err)
		}
		if got, _ := want.State[tc.field].(string); got != tc.value {
			t.Fatalf("%s: test setup: state.Fold's %s = %#v, want %q — fold keeps a value_type mismatch verbatim (spec/fold.md §7.1)", tc.name, tc.field, want.State[tc.field], tc.value)
		}
	}

	build := func(t *testing.T) (*projection.DB, map[string][]map[string]any) {
		t.Helper()
		_, store := createTestStore(t, "0123456789abcdef")
		db, err := projection.Open(":memory:")
		if err != nil {
			t.Fatalf("Open(:memory:) failed: %v", err)
		}
		enumRes := &dag.EnumerateResult{
			Ops:            byObj,
			Cursors:        dag.CursorSet{"refs/writ/0123456789abcdef/record": lastOpID},
			DecodedCommits: 2 * len(cases),
		}
		if _, err := db.Refresh(store, projection.WithSchema(rulesByType), projection.WithEnumOverrideForTest(enumRes)); err != nil {
			t.Fatalf("Refresh failed: %v", err)
		}
		dump, err := db.DumpTables()
		if err != nil {
			t.Fatalf("DumpTables failed: %v", err)
		}
		return db, dump
	}

	db, dump1 := build(t)
	defer db.Close()
	rawDB := db.DB()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var val sql.NullString
			var typ string
			query := "SELECT " + tc.column + ", typeof(" + tc.column + ") FROM o_record WHERE object_id = ?"
			if err := rawDB.QueryRow(query, tc.objID).Scan(&val, &typ); err != nil {
				t.Fatalf("query o_record.%s: %v", tc.column, err)
			}
			if !val.Valid {
				t.Fatalf("%s = NULL, want the mismatched value %q stored, not dropped as SQL NULL", tc.column, tc.value)
			}
			if val.String != tc.value {
				t.Fatalf("%s = %q, want %q", tc.column, val.String, tc.value)
			}
			if typ != tc.wantTypeof {
				t.Fatalf("typeof(%s) = %q, want %q", tc.column, typ, tc.wantTypeof)
			}
		})
	}

	// Drop-and-rebuild: db.Rebuild itself always cold-walks the real store
	// (it ignores WithEnumOverrideForTest, by design — see
	// append_entries_test.go's TestAppendTablesSurviveDropAndRebuild — and
	// writ's own producer refuses these mismatched bodies, so they cannot
	// reach a real store through store.Append). The equivalent check here
	// is the property drop-and-rebuild exists to guarantee: a second,
	// independently-built projection refreshed from the identical DAG
	// reproduces byte-identical rows, so the projection stays a droppable
	// cache (AGENTS.md) even for a value its declared value_type cannot
	// represent.
	db2, dump2 := build(t)
	defer db2.Close()
	if !reflect.DeepEqual(dump1, dump2) {
		t.Fatalf("projection rebuilt from the identical DAG differs from the original build:\nfirst:  %+v\nsecond: %+v", dump1, dump2)
	}
}
