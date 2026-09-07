package projection_test

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/engine/state"
)

// This file restores, generically, the corpus-wide "Projection == Fold"
// agreement check that fixtures_test.go's
// TestFixturesIncrementalVsColdAndFoldAgreement used to run against the
// (now-deleted) typed Fold*/projection.Review-style readers (round 1 MAJOR
// finding). The property under test was never typed: it is writeTypeRow's
// inversion of state.Fold's output into generated scalar columns, keyed-lww
// groups, append groups, and unknown_ops — entirely generic machinery — so
// this cross-checks it directly against state.Fold over a schema-declared
// type the engine has never heard of ("ticket"), built from two independent
// writer identities' concurrent and causally-ordered ops, exactly as
// makeReviewOp/makeWidgetOp build ops elsewhere in this package: hand-built
// codec.Op values with an explicit parent DAG, reaching Refresh through
// WithEnumOverrideForTest with no producer-validation gate to route around
// (op-envelope's producer validation lives in dag.Store.Append/codec.BuildCommit,
// neither of which this touches).

func makeTicketEnv(objID, opType string, body map[string]any) codec.Envelope {
	bodyRaw, _ := json.Marshal(body)
	env := codec.Envelope{
		ObjectID:   objID,
		ObjectType: "ticket",
		OpType:     opType,
		OpVersion:  1,
		Body:       bodyRaw,
	}
	raw, _ := codec.EncodePayload(env)
	env.Raw = raw
	return env
}

func makeTicketOp(objID, id string, parents []string, opType string, body map[string]any, authorName, authorEmail string, when time.Time) codec.Op {
	env := makeTicketEnv(objID, opType, body)
	author := codec.Identity{Name: authorName, Email: authorEmail, When: when}
	return codec.Op{
		Envelope:  env,
		ID:        id,
		Parents:   parents,
		Author:    author,
		Committer: author,
		Message:   "writ: " + opType + " ticket/" + objID + "\n",
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

// TestProjectionMatchesFoldAcrossStrategies builds one "ticket" object from
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
	const objID = "ticket-1"
	base := time.Unix(1700000000, 0).UTC()
	alice := func(id string, parents []string, opType string, body map[string]any, offset int) codec.Op {
		return makeTicketOp(objID, id, parents, opType, body, "Alice", "alice@example.com", base.Add(time.Duration(offset)*time.Second))
	}
	bob := func(id string, parents []string, opType string, body map[string]any, offset int) codec.Op {
		return makeTicketOp(objID, id, parents, opType, body, "Bob", "bob@example.com", base.Add(time.Duration(offset)*time.Second))
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

	// Unknown op type: no rule below names "escalate" for "ticket" —
	// forward-compat preservation, not an error.
	opUnknown := alice("op-unknown", []string{opTriageBackend.ID}, "escalate", map[string]any{"reason": "urgent"}, 7)

	ops := []codec.Op{
		opCreate, opStatusAlice, opStatusBob, opAssignAlice, opAssignBob,
		opNoteAlice, opNoteBob, opArchive, opTriageFrontend1, opTriageFrontend2,
		opTriageBackend, opUnknown,
	}

	ticketRules := []state.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "ticket"},
		{OpType: "set-status", OpVersion: 1, Field: "status", Strategy: "lww", ValueType: "string", ObjectType: "ticket"},
		{OpType: "assign", OpVersion: 1, Field: "add", Target: "assignees", Strategy: "set-observed-remove", ValueType: "string", ObjectType: "ticket"},
		{OpType: "assign", OpVersion: 1, Field: "remove", Target: "assignees", Strategy: "set-observed-remove", ValueType: "string", ObjectType: "ticket"},
		{OpType: "archive", OpVersion: 1, Field: "archived", Strategy: "tombstone", ValueType: "bool", ObjectType: "ticket"},
		{OpType: "note", OpVersion: 1, Field: "note", Strategy: "append", ValueType: "string", ObjectType: "ticket"},
		{OpType: "triage", OpVersion: 1, Field: "level", Strategy: "keyed-lww", Key: []string{"component"}, KeyTypes: map[string]string{"component": "string"}, ValueType: "string", ObjectType: "ticket"},
	}
	rulesByType := map[string][]state.Rule{"ticket": ticketRules}

	// The reference this generic materialization has to match.
	want, err := state.Fold(ops, ticketRules)
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
			"refs/writ/0123456789abcdef/ticket": "op-unknown",
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
	if err := rawDB.QueryRow("SELECT f_title, f_status, f_archived FROM o_ticket WHERE object_id = ?", objID).
		Scan(&gotTitle, &gotStatus, &gotArchived); err != nil {
		t.Fatalf("query o_ticket: %v", err)
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
	gotAssignees := queryStrings(t, rawDB, "SELECT item FROM o_ticket__assignees WHERE object_id = ? ORDER BY item ASC", objID)
	if !reflect.DeepEqual(gotAssignees, wantAssignees) {
		t.Fatalf("o_ticket__assignees = %v, want (state.Fold) %v", gotAssignees, wantAssignees)
	}
	if !reflect.DeepEqual(wantAssignees, []string{"alice", "bob"}) {
		t.Fatalf("test setup: state.Fold's assignees = %v, want [alice bob] — both concurrent adds must survive", wantAssignees)
	}

	// append: note, from two concurrent writers — order matters here, so
	// this is compared positionally, not as a set.
	wantNotes, _ := want.State["note"].([]any)
	gotNotes := queryStrings(t, rawDB, "SELECT f_note FROM o_ticket__note WHERE object_id = ? ORDER BY idx ASC", objID)
	if len(gotNotes) != len(wantNotes) {
		t.Fatalf("o_ticket__note has %d rows, state.Fold's note has %d entries: got %v, want %v", len(gotNotes), len(wantNotes), gotNotes, wantNotes)
	}
	for i := range wantNotes {
		if gotNotes[i] != wantNotes[i] {
			t.Fatalf("o_ticket__note[%d] = %q, want (state.Fold) %q — append ordering disagrees with the pure fold", i, gotNotes[i], wantNotes[i])
		}
	}

	// keyed-lww: triage level per component, one key causally overridden.
	wantLevels, ok := want.State["level"].([]any)
	if !ok || len(wantLevels) != 2 {
		t.Fatalf("test setup: state.Fold's level = %#v, want 2 keyed entries", want.State["level"])
	}
	rows, err := rawDB.Query("SELECT k_component, f_level FROM o_ticket__k_component WHERE object_id = ? ORDER BY k_component ASC", objID)
	if err != nil {
		t.Fatalf("query o_ticket__k_component: %v", err)
	}
	defer rows.Close()
	var gotLevels []struct{ component, level string }
	for rows.Next() {
		var c, l string
		if err := rows.Scan(&c, &l); err != nil {
			t.Fatalf("scan o_ticket__k_component: %v", err)
		}
		gotLevels = append(gotLevels, struct{ component, level string }{c, l})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate o_ticket__k_component: %v", err)
	}
	if len(gotLevels) != 2 {
		t.Fatalf("o_ticket__k_component has %d rows, want 2: %+v", len(gotLevels), gotLevels)
	}
	for i, entry := range wantLevels {
		m, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("state.Fold's level[%d] = %#v, want a keyed-lww entry map", i, entry)
		}
		key, _ := m["key"].([]string)
		val, _ := m["value"].(string)
		if len(key) != 1 || key[0] != gotLevels[i].component || val != gotLevels[i].level {
			t.Fatalf("o_ticket__k_component row %d = %+v, want key %v value %q (state.Fold)", i, gotLevels[i], key, val)
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
	const objID = "ticket-2"
	base := time.Unix(1700000000, 0).UTC()

	opCreate := makeTicketOp(objID, "op-create", nil, "create", map[string]any{"title": "Truncated"}, "Alice", "alice@example.com", base)
	opNote1 := makeTicketOp(objID, "op-note-1", []string{opCreate.ID}, "note", map[string]any{"note": "first"}, "Alice", "alice@example.com", base.Add(1*time.Second))
	// opNote2 exists in the log but is never fetched by this projection
	// build below — the "truncated" half of the ancestry.
	_ = makeTicketOp(objID, "op-note-2", []string{opNote1.ID}, "note", map[string]any{"note": "second"}, "Alice", "alice@example.com", base.Add(2*time.Second))

	ticketRules := []state.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "ticket"},
		{OpType: "note", OpVersion: 1, Field: "note", Strategy: "append", ValueType: "string", ObjectType: "ticket"},
	}
	rulesByType := map[string][]state.Rule{"ticket": ticketRules}

	truncatedOps := []codec.Op{opCreate, opNote1}

	want, err := state.Fold(truncatedOps, ticketRules)
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
			string(dag.LocalRefName(identity.WriterID("0123456789abcdef"), "ticket")): "op-note-1",
		},
		DecodedCommits: len(truncatedOps),
	}
	if _, err := db.Refresh(store, projection.WithSchema(rulesByType), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	rawDB := db.DB()
	var gotTitle string
	if err := rawDB.QueryRow("SELECT f_title FROM o_ticket WHERE object_id = ?", objID).Scan(&gotTitle); err != nil {
		t.Fatalf("query o_ticket: %v", err)
	}
	if gotTitle != "Truncated" {
		t.Fatalf("f_title = %q, want %q", gotTitle, "Truncated")
	}

	gotNotes := queryStrings(t, rawDB, "SELECT f_note FROM o_ticket__note WHERE object_id = ? ORDER BY idx ASC", objID)
	if len(gotNotes) != 1 || gotNotes[0] != "first" {
		t.Fatalf("o_ticket__note over the truncated prefix = %v, want [first] (state.Fold agrees, and must not see the un-fetched \"second\" op)", gotNotes)
	}
}
