package projection

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/state"
)

// appendRow is one row of an append target's row-per-entry table, in the
// order (op_seq, entry_idx) the table's primary key already sorts it by.
type appendRow struct {
	OpSeq    int
	EntryIdx int
	Value    any
}

// readAppendRows reads every row of an append table for one object.
func readAppendRows(t *testing.T, db *sql.DB, table, objectID string) []appendRow {
	t.Helper()
	rows, err := db.Query("SELECT op_seq, entry_idx, value FROM "+quoteIdent(table)+" WHERE object_id = ? ORDER BY op_seq ASC, entry_idx ASC", objectID)
	if err != nil {
		t.Fatalf("query %s: %v", table, err)
	}
	defer rows.Close()
	var out []appendRow
	for rows.Next() {
		var r appendRow
		var v any
		if err := rows.Scan(&r.OpSeq, &r.EntryIdx, &v); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		r.Value = v
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", table, err)
	}
	return out
}

// TestWriteAppendRowsSkipsExplicitNullAtTheWriter pins the writer's own
// null check — the `raw == nil` half of writeAppendRows' `if !ok || raw ==
// nil` — directly, with no fold and no skip set in the way.
//
// The end-to-end test for the same shape (append_entries_test.go's
// TestAppendExplicitNullContributesNoRow) cannot reach this branch:
// engine/internal/fold's Uninterpretable quarantines the whole op for a
// null on a matched append field long before the writer sees it, and
// codec's producer validation (op-envelope §Producer validation) refuses to
// write a JSON null body value in the first place — so the branch is
// unreachable through Refresh today, and only a direct call reddens when it
// is removed. It is pinned anyway because it is what keeps the writer's
// behaviour identical to appendAccumulator.Apply's if that whole-op
// quarantine is ever narrowed to per-field: an explicit null must
// contribute no row, exactly like an absent field, never the NULL cell the
// old shared append table's presence-only gate used to write (WRIT-212's
// second folded-in latent defect).
func TestWriteAppendRowsSkipsExplicitNullAtTheWriter(t *testing.T) {
	db, err := sql.Open("sqlite", formatDSN(":memory:"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	const table = "o_widget__note"
	if _, err := db.Exec("CREATE TABLE " + quoteIdent(table) + " (object_id TEXT NOT NULL, op_seq INTEGER NOT NULL, entry_idx INTEGER NOT NULL, value TEXT, PRIMARY KEY (object_id, op_seq, entry_idx))"); err != nil {
		t.Fatalf("create %s: %v", table, err)
	}

	plan := &targetPlan{
		Strategy:    "append",
		ValueType:   "string",
		AppendTable: table,
		AppendRules: []state.Rule{
			{OpType: "note", OpVersion: 1, Field: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"},
		},
	}

	mkOp := func(id, body string) codec.Op {
		return codec.Op{
			ID: id,
			Envelope: codec.Envelope{
				ObjectID:   "w-1",
				ObjectType: "widget",
				OpType:     "note",
				OpVersion:  1,
				Body:       json.RawMessage(body),
			},
		}
	}
	// op-2 carries an explicit JSON null at the matched field; op-3 omits
	// the field entirely. Both must contribute nothing, and op-4 must land
	// at its own index in the slice regardless.
	orderedOps := []codec.Op{
		mkOp("op-1", `{"note":"x"}`),
		mkOp("op-2", `{"note":null}`),
		mkOp("op-3", `{"other":"y"}`),
		mkOp("op-4", `{"note":"z"}`),
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// An empty skip set: nothing is quarantined, so every op above reaches
	// the writer. This is the whole point of the direct call.
	if err := writeAppendRows(tx, plan, "w-1", orderedOps, map[string]bool{}); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			t.Errorf("Rollback: %v", rbErr)
		}
		t.Fatalf("writeAppendRows: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	got := readAppendRows(t, db, table, "w-1")
	want := []appendRow{
		{OpSeq: 0, EntryIdx: 0, Value: "x"},
		{OpSeq: 3, EntryIdx: 0, Value: "z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s rows = %+v, want %+v — an explicit JSON null must contribute no row at all, not a NULL-valued one", table, got, want)
	}
}
