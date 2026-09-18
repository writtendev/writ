package projection_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/projection"
	"github.com/writtendev/writ/internal/state"
)

// makeWidgetEnv builds one op envelope for "widget", the object type
// testRules() declares: it is what the tests that write through dag.Store
// hand to store.Append (refresh_test.go and its callers), and what
// makeWidgetOp below wraps into a codec.Op fed straight to Refresh via
// WithEnumOverrideForTest. That second path never reaches producer
// validation, so the op types this file builds ("note-v1", "push") need not
// be ones any declared vocabulary would accept.
func makeWidgetEnv(objID, opType string, body map[string]any) codec.Envelope {
	bodyRaw, _ := json.Marshal(body)
	env := codec.Envelope{
		ObjectID:   objID,
		ObjectType: "widget",
		OpType:     opType,
		OpVersion:  1,
		Body:       bodyRaw,
	}
	raw, _ := codec.EncodePayload(env)
	env.Raw = raw
	return env
}

func makeWidgetOp(id string, parents []string, opType string, body map[string]any, when time.Time) codec.Op {
	env := makeWidgetEnv("w-1", opType, body)
	author := codec.Identity{Name: "Test Writer", Email: "writer@example.com", When: when}
	return codec.Op{
		Envelope:  env,
		ID:        id,
		Parents:   parents,
		Author:    author,
		Committer: author,
		Message:   "writ: " + opType + " widget/w-1\n",
	}
}

// makeVersionedWidgetOp is makeWidgetOp generalized over op_version, for the
// wildcard-op_version shape below: makeWidgetEnv hardcodes OpVersion 1.
func makeVersionedWidgetOp(id string, parents []string, opType string, opVersion int64, body map[string]any, when time.Time) codec.Op {
	env := makeWidgetEnv("w-1", opType, body)
	env.OpVersion = opVersion
	raw, _ := codec.EncodePayload(env)
	env.Raw = raw
	author := codec.Identity{Name: "Test Writer", Email: "writer@example.com", When: when}
	return codec.Op{
		Envelope:  env,
		ID:        id,
		Parents:   parents,
		Author:    author,
		Committer: author,
		Message:   "writ: " + opType + " widget/w-1\n",
	}
}

func queryAppendValues(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query("SELECT value FROM "+table+" WHERE object_id = ? ORDER BY op_seq ASC, entry_idx ASC", "w-1")
	if err != nil {
		t.Fatalf("query %s: %v", table, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		if !v.Valid {
			t.Fatalf("%s: unexpected NULL value — a row is only ever written for an actual entry (WRIT-212)", table)
		}
		out = append(out, v.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", table, err)
	}
	return out
}

// appendEntry is one row of an append target's row-per-entry table:
// (op_seq, entry_idx, value), the three columns WRIT-212 gives a contract.
// queryAppendValues above reads only the values, which is all fold parity
// needs; pinning op_seq's own contract — that it is the op's index in the
// object's total order L, gaps at quarantined ops included, and not a
// counter compacting around them — needs all three.
type appendEntry struct {
	OpSeq    int
	EntryIdx int
	Value    string
}

func queryAppendEntries(t *testing.T, db *sql.DB, table, objectID string) []appendEntry {
	t.Helper()
	rows, err := db.Query("SELECT op_seq, entry_idx, value FROM "+table+" WHERE object_id = ? ORDER BY op_seq ASC, entry_idx ASC", objectID)
	if err != nil {
		t.Fatalf("query %s: %v", table, err)
	}
	defer rows.Close()
	var out []appendEntry
	for rows.Next() {
		var e appendEntry
		var v sql.NullString
		if err := rows.Scan(&e.OpSeq, &e.EntryIdx, &v); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		if !v.Valid {
			t.Fatalf("%s: unexpected NULL value — a row is only ever written for an actual entry (WRIT-212)", table)
		}
		e.Value = v.String
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", table, err)
	}
	return out
}

// TestAppendEntriesContentIsDeterministic is WRIT-189 round 3 MAJOR-1's own
// executable statement, carried forward under WRIT-212's row-per-entry
// table: an append target legally declared under two envelopes — here two
// different op_types, "note-v1" and "note-v2", both agreeing on strategy and
// value_type and both targeting the same field "note" (spec/schema-ops.md
// §8's "two different op_types ... that happen to reuse the same target"
// carve-out, which requires exactly this agreement) — must have every
// envelope's ops materialized, in canonical rule order, and the result must
// not depend on which order buildTypeDescriptor happens to see the two
// rules in.
func TestAppendEntriesContentIsDeterministic(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeWidgetOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	opNoteV1 := makeWidgetOp("op-note-v1-1", []string{"op-create-1"}, "note-v1", map[string]any{"note": "a"}, base.Add(1*time.Second))
	opNoteV2 := makeWidgetOp("op-note-v2-1", []string{"op-note-v1-1"}, "note-v2", map[string]any{"note": "b"}, base.Add(2*time.Second))
	ops := []codec.Op{opCreate, opNoteV1, opNoteV2}

	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	noteV1Rule := state.Rule{OpType: "note-v1", Field: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	noteV2Rule := state.Rule{OpType: "note-v2", Field: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}

	rulesOriginal := map[string][]state.Rule{"widget": {titleRule, noteV1Rule, noteV2Rule}}
	rulesReordered := map[string][]state.Rule{"widget": {noteV2Rule, titleRule, noteV1Rule}}

	want, err := state.Fold(ops, []state.Rule{titleRule, noteV1Rule, noteV2Rule})
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	wantNotes, ok := want.State["note"].([]any)
	if !ok || !reflect.DeepEqual(wantNotes, []any{"a", "b"}) {
		t.Fatalf("test setup: state.Fold's note = %#v, want [\"a\" \"b\"]", want.State["note"])
	}

	readNotes := func(t *testing.T, rules map[string][]state.Rule) []string {
		t.Helper()
		_, store := createTestStore(t, "0123456789abcdef")

		db, err := projection.Open(":memory:")
		if err != nil {
			t.Fatalf("Open(:memory:) failed: %v", err)
		}
		defer db.Close()

		enumRes := &dag.EnumerateResult{
			Ops: map[string][]codec.Op{"w-1": ops},
			Cursors: dag.CursorSet{
				"refs/writ/0123456789abcdef/widget": "op-note-v2-1",
			},
			DecodedCommits: len(ops),
		}

		if _, err := db.Refresh(store, projection.WithSchema(rules), projection.WithEnumOverrideForTest(enumRes)); err != nil {
			t.Fatalf("Refresh failed: %v", err)
		}

		return queryAppendValues(t, db.DB(), "o_widget__note")
	}

	gotOriginal := readNotes(t, rulesOriginal)
	gotReordered := readNotes(t, rulesReordered)

	wantStrs := []string{"a", "b"}
	if !reflect.DeepEqual(gotOriginal, wantStrs) {
		t.Fatalf("o_widget__note rows (original rule order) = %#v, want %#v — an envelope's ops were dropped", gotOriginal, wantStrs)
	}
	if !reflect.DeepEqual(gotReordered, wantStrs) {
		t.Fatalf("o_widget__note rows (reordered rules) = %#v, want %#v — an envelope's ops were dropped", gotReordered, wantStrs)
	}
	if !reflect.DeepEqual(gotOriginal, gotReordered) {
		t.Fatalf("o_widget__note rows differ between rule orderings: original %#v, reordered %#v — materialized content is order-dependent", gotOriginal, gotReordered)
	}
}

// TestAppendTargetDiffersFromField is WRIT-189 round 4 MAJOR-1's own
// executable statement, carried forward under WRIT-212's row-per-entry
// tables: an append rule whose declared target(...) differs from its field
// must still materialize the value the op body actually carries under that
// field name, not under the target key. "base" and "head" are two distinct
// append targets, each with its own table now (WRIT-212 — before this, two
// targets sharing an envelope shared one table, o_widget__base_head); this
// pins that each target's own writer still resolves the right field.
func TestAppendTargetDiffersFromField(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeWidgetOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	opPush := makeWidgetOp("op-push-1", []string{"op-create-1"}, "push", map[string]any{"base_sha": "aaa", "head_sha": "bbb"}, base.Add(1*time.Second))
	ops := []codec.Op{opCreate, opPush}

	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	baseRule := state.Rule{OpType: "push", OpVersion: 1, Field: "base_sha", Target: "base", Strategy: "append", ValueType: "git-oid", ObjectType: "widget"}
	headRule := state.Rule{OpType: "push", OpVersion: 1, Field: "head_sha", Target: "head", Strategy: "append", ValueType: "git-oid", ObjectType: "widget"}
	rules := []state.Rule{titleRule, baseRule, headRule}

	// The probe: state.Fold is what the materialized tables have to agree
	// with. base_sha/head_sha resolve through target(...) to "base"/"head"
	// in the folded state map.
	want, err := state.Fold(ops, rules)
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	wantBase, _ := want.State["base"].([]any)
	wantHead, _ := want.State["head"].([]any)
	if !reflect.DeepEqual(wantBase, []any{"aaa"}) || !reflect.DeepEqual(wantHead, []any{"bbb"}) {
		t.Fatalf("test setup: state.Fold's base/head = %#v / %#v, want [\"aaa\"] / [\"bbb\"]", want.State["base"], want.State["head"])
	}

	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	enumRes := &dag.EnumerateResult{
		Ops: map[string][]codec.Op{"w-1": ops},
		Cursors: dag.CursorSet{
			"refs/writ/0123456789abcdef/widget": "op-push-1",
		},
		DecodedCommits: len(ops),
	}

	rulesByType := map[string][]state.Rule{"widget": rules}
	if _, err := db.Refresh(store, projection.WithSchema(rulesByType), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	gotBase := queryAppendValues(t, db.DB(), "o_widget__base")
	gotHead := queryAppendValues(t, db.DB(), "o_widget__head")
	if !reflect.DeepEqual(gotBase, []string{"aaa"}) {
		t.Fatalf("o_widget__base = %v, want [\"aaa\"] — target(...) differing from field was read by target key instead of field name", gotBase)
	}
	if !reflect.DeepEqual(gotHead, []string{"bbb"}) {
		t.Fatalf("o_widget__head = %v, want [\"bbb\"] — target(...) differing from field was read by target key instead of field name", gotHead)
	}
}

// TestAppendCrossTargetPairingSharesOpSeq restores, generically, the
// coverage WRIT-189 round 2's MAJOR-1 finding pinned in a since-deleted
// per-type test: a shape where one op writes only one of two co-located
// append targets, the other field entirely absent rather than written
// empty.
//
// Before WRIT-189 round 2, two independent per-target child tables zipped
// back into pairs by local list position mispaired the moment an op wrote
// only one of the two fields. Round 2 fixed it with one shared row per op;
// WRIT-212 replaces that shared row with an explicit, joinable op_seq
// column on each target's own table instead — this test pins that the new
// mechanism gives the same answer: "base" gets a row for every push op,
// "head" gets a row only for the op that wrote it, and that row's op_seq
// equals the corresponding "base" row's op_seq (the pairing, now carried as
// data instead of implied by table co-location).
func TestAppendCrossTargetPairingSharesOpSeq(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeWidgetOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	// Omits "head_sha" entirely: an absent field, not an empty-string write.
	opPush1 := makeWidgetOp("op-push-1", []string{"op-create-1"}, "push", map[string]any{"base_sha": "aaa"}, base.Add(1*time.Second))
	opPush2 := makeWidgetOp("op-push-2", []string{"op-push-1"}, "push", map[string]any{"base_sha": "bbb", "head_sha": "ccc"}, base.Add(2*time.Second))
	ops := []codec.Op{opCreate, opPush1, opPush2}

	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	baseRule := state.Rule{OpType: "push", OpVersion: 1, Field: "base_sha", Target: "base", Strategy: "append", ValueType: "git-oid", ObjectType: "widget"}
	headRule := state.Rule{OpType: "push", OpVersion: 1, Field: "head_sha", Target: "head", Strategy: "append", ValueType: "git-oid", ObjectType: "widget"}
	rules := []state.Rule{titleRule, baseRule, headRule}

	// state.Fold's own base/head lists are independent and unaligned: "head"
	// gets exactly one entry (from opPush2), never a placeholder for
	// opPush1's omission. This is exactly the shape that made the old
	// positional-zip pairing wrong — there is nothing in these two lists
	// alone to say which head value belongs with which base value.
	want, err := state.Fold(ops, rules)
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	wantBase, _ := want.State["base"].([]any)
	wantHead, _ := want.State["head"].([]any)
	if !reflect.DeepEqual(wantBase, []any{"aaa", "bbb"}) {
		t.Fatalf("test setup: state.Fold's base = %#v, want [\"aaa\" \"bbb\"]", want.State["base"])
	}
	if !reflect.DeepEqual(wantHead, []any{"ccc"}) {
		t.Fatalf("test setup: state.Fold's head = %#v, want [\"ccc\"] — only opPush2 wrote head", want.State["head"])
	}

	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	enumRes := &dag.EnumerateResult{
		Ops: map[string][]codec.Op{"w-1": ops},
		Cursors: dag.CursorSet{
			"refs/writ/0123456789abcdef/widget": "op-push-2",
		},
		DecodedCommits: len(ops),
	}

	rulesByType := map[string][]state.Rule{"widget": rules}
	if _, err := db.Refresh(store, projection.WithSchema(rulesByType), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	type entry struct {
		opSeq int
		value string
	}
	query := func(table string) []entry {
		t.Helper()
		rows, err := db.DB().Query("SELECT op_seq, value FROM "+table+" WHERE object_id = ? ORDER BY op_seq ASC, entry_idx ASC", "w-1")
		if err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		defer rows.Close()
		var out []entry
		for rows.Next() {
			var e entry
			if err := rows.Scan(&e.opSeq, &e.value); err != nil {
				t.Fatalf("scan %s: %v", table, err)
			}
			out = append(out, e)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate %s: %v", table, err)
		}
		return out
	}

	gotBase := query("o_widget__base")
	gotHead := query("o_widget__head")

	if len(gotBase) != 2 {
		t.Fatalf("o_widget__base has %d rows, want 2 (one per push op): %+v", len(gotBase), gotBase)
	}
	if len(gotHead) != 1 {
		t.Fatalf("o_widget__head has %d rows, want 1 (only opPush2 wrote head): %+v", len(gotHead), gotHead)
	}
	if gotBase[0].value != "aaa" || gotBase[1].value != "bbb" {
		t.Fatalf("o_widget__base values = %+v, want [aaa bbb]", gotBase)
	}
	if gotHead[0].value != "ccc" {
		t.Fatalf("o_widget__head values = %+v, want [ccc]", gotHead)
	}
	// Cross-target pairing: opPush2's row in "base" and its row in "head"
	// must share one op_seq — the pairing that used to be the row itself is
	// now carried as this shared column instead.
	if gotBase[1].opSeq != gotHead[0].opSeq {
		t.Fatalf("opPush2's base row op_seq = %d, head row op_seq = %d, want equal — cross-target pairing must survive by op_seq, not by row co-location", gotBase[1].opSeq, gotHead[0].opSeq)
	}
	// opPush1's op_seq must not appear in "head" at all — an op writing
	// only one of two co-located targets must shift nothing in the other.
	if gotBase[0].opSeq == gotHead[0].opSeq {
		t.Fatalf("opPush1's base row op_seq (%d) collides with head's only row — an omitted field must not be paired with a later op's value", gotBase[0].opSeq)
	}
}

// TestAppendAmbiguousFieldBothEntriesMaterialize is WRIT-201's own shape,
// re-run against WRIT-212's row-per-entry table: two append rules binding
// one target ("note") to two different Fields under one exact (op_type,
// op_version) envelope. spec/fold.md §5 rules that every matching rule
// applies, in canonical rule order, so state.Fold contributes *both*
// fields' entries to one list.
//
// Before WRIT-212, the shared one-row-per-op/one-column-per-target append
// table could not hold two entries for one target from one op, so "note"
// was declined (WRIT-201) and only the group-mate "tags" kept a table.
// Row-per-entry removes that limit: "note" now materializes both entries,
// in canonical rule order (ascending field, since op_type and op_version
// tie), and nothing is withheld any more. Order-independent, as before.
func TestAppendAmbiguousFieldBothEntriesMaterialize(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeWidgetOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	opNote := makeWidgetOp("op-note-1", []string{"op-create-1"}, "note", map[string]any{"a": "xa", "b": "yb", "tag": "t1"}, base.Add(1*time.Second))
	ops := []codec.Op{opCreate, opNote}

	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	noteFieldA := state.Rule{OpType: "note", OpVersion: 1, Field: "a", Target: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	noteFieldB := state.Rule{OpType: "note", OpVersion: 1, Field: "b", Target: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	tagRule := state.Rule{OpType: "note", OpVersion: 1, Field: "tag", Target: "tags", Strategy: "append", ValueType: "string", ObjectType: "widget"}

	// state.Fold applies every matching rule, in canonical rule order
	// (ascending (op_type, op_version, field)), so it takes both entries
	// whichever way the rule slice is ordered — the probe that makes clear
	// the projection now matches it rather than declining.
	want, err := state.Fold(ops, []state.Rule{titleRule, tagRule, noteFieldB, noteFieldA})
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	wantNotes, ok := want.State["note"].([]any)
	if !ok || !reflect.DeepEqual(wantNotes, []any{"xa", "yb"}) {
		t.Fatalf("test setup: state.Fold's note = %#v, want [\"xa\", \"yb\"]", want.State["note"])
	}
	wantTags, ok := want.State["tags"].([]any)
	if !ok || !reflect.DeepEqual(wantTags, []any{"t1"}) {
		t.Fatalf("test setup: state.Fold's tags = %#v, want [\"t1\"]", want.State["tags"])
	}

	orderings := map[string][]state.Rule{
		"a-then-b": {titleRule, noteFieldA, noteFieldB, tagRule},
		"b-then-a": {titleRule, tagRule, noteFieldB, noteFieldA},
	}

	for name, rules := range orderings {
		t.Run(name, func(t *testing.T) {
			_, store := createTestStore(t, "0123456789abcdef")

			db, err := projection.Open(":memory:")
			if err != nil {
				t.Fatalf("Open(:memory:) failed: %v", err)
			}
			defer db.Close()

			enumRes := &dag.EnumerateResult{
				Ops: map[string][]codec.Op{"w-1": ops},
				Cursors: dag.CursorSet{
					"refs/writ/0123456789abcdef/widget": "op-note-1",
				},
				DecodedCommits: len(ops),
			}

			rulesByType := map[string][]state.Rule{"widget": rules}
			if _, err := db.Refresh(store, projection.WithSchema(rulesByType), projection.WithEnumOverrideForTest(enumRes)); err != nil {
				t.Fatalf("Refresh failed: %v", err)
			}

			gotNote := queryAppendValues(t, db.DB(), "o_widget__note")
			if !reflect.DeepEqual(gotNote, []string{"xa", "yb"}) {
				t.Fatalf("o_widget__note = %v, want [xa yb] in canonical rule order regardless of rule-slice order", gotNote)
			}
			gotTags := queryAppendValues(t, db.DB(), "o_widget__tags")
			if !reflect.DeepEqual(gotTags, []string{"t1"}) {
				t.Fatalf("o_widget__tags = %v, want [t1]", gotTags)
			}

			var unknownFields sql.NullString
			if err := db.DB().QueryRow("SELECT unknown_fields FROM o_widget WHERE object_id = ?", "w-1").Scan(&unknownFields); err != nil {
				t.Fatalf("query o_widget: %v", err)
			}
			if unknownFields.Valid {
				t.Fatalf("o_widget.unknown_fields = %q, want NULL — nothing is withheld any more, so nothing should land here", unknownFields.String)
			}

			var unknownOpCount int
			if err := db.DB().QueryRow("SELECT COUNT(*) FROM unknown_ops WHERE object_id = ?", "w-1").Scan(&unknownOpCount); err != nil {
				t.Fatalf("query unknown_ops: %v", err)
			}
			if unknownOpCount != 0 {
				t.Fatalf("unknown_ops count = %d, want 0", unknownOpCount)
			}
		})
	}
}

// TestAppendWildcardVersionOrdersByCanonicalRule is the ticket's own folded-in
// defect: the pre-WRIT-212 append-group collision check compared
// (op_type, op_version) by literal Go struct equality as a map key, so a
// wildcard-version rule (op_version 0) sharing a target with a
// specific-version rule under the same op_type never collided — the
// group's table was built as if the two rules were unrelated, and one of
// them silently lost its column. Under row-per-entry there is nothing to
// detect any more: an op matches both rules (opMatchesRuleLite treats
// op_version 0 as a wildcard on either side), and both contribute rows, in
// canonical rule order (spec/fold.md §5: ascending op_type, then
// op_version, then field) — here (annotate, v0, note) before (annotate, v2,
// body), since op_version dominates field once op_type ties.
func TestAppendWildcardVersionOrdersByCanonicalRule(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeVersionedWidgetOp("op-create-1", nil, "create", 1, map[string]any{"title": "T"}, base)
	opAnnotate := makeVersionedWidgetOp("op-annotate-1", []string{"op-create-1"}, "annotate", 2, map[string]any{"note": "n0", "body": "b2"}, base.Add(1*time.Second))
	ops := []codec.Op{opCreate, opAnnotate}

	titleRule := state.Rule{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	wildcardRule := state.Rule{OpType: "annotate", OpVersion: 0, Field: "note", Target: "remark", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	specificRule := state.Rule{OpType: "annotate", OpVersion: 2, Field: "body", Target: "remark", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	rules := []state.Rule{titleRule, wildcardRule, specificRule}

	want, err := state.Fold(ops, rules)
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	wantRemark, ok := want.State["remark"].([]any)
	if !ok || !reflect.DeepEqual(wantRemark, []any{"n0", "b2"}) {
		t.Fatalf("test setup: state.Fold's remark = %#v, want [\"n0\", \"b2\"]", want.State["remark"])
	}

	_, store := createTestStore(t, "0123456789abcdef")
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	enumRes := &dag.EnumerateResult{
		Ops:            map[string][]codec.Op{"w-1": ops},
		Cursors:        dag.CursorSet{"refs/writ/0123456789abcdef/widget": "op-annotate-1"},
		DecodedCommits: len(ops),
	}
	if _, err := db.Refresh(store, projection.WithSchema(map[string][]state.Rule{"widget": rules}), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	got := queryAppendValues(t, db.DB(), "o_widget__remark")
	if !reflect.DeepEqual(got, []string{"n0", "b2"}) {
		t.Fatalf("o_widget__remark = %v, want [n0 b2] — the wildcard rule's entry must sort first (canonical rule order), and both must materialize (the wildcard-version collision-detection gap this ticket folds in)", got)
	}
}

// TestAppendArrayFieldFlattensToOneRowPerElement pins the first of the
// ticket's three folded-in latent defects: engine/internal/fold's
// appendAccumulator.Apply flattens an array-valued write into one fold
// entry per element (an op writing ["a","b"] contributes two entries, not
// one array-valued entry). The old shared append table stored the whole
// array in one cell via columnValue, silently misrepresenting it; the
// row-per-entry writer must flatten exactly the same way.
func TestAppendArrayFieldFlattensToOneRowPerElement(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeWidgetOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	opNote := makeWidgetOp("op-note-1", []string{"op-create-1"}, "note", map[string]any{"note": []any{"a", "b"}}, base.Add(1*time.Second))
	ops := []codec.Op{opCreate, opNote}

	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	noteRule := state.Rule{OpType: "note", OpVersion: 1, Field: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	rules := []state.Rule{titleRule, noteRule}

	want, err := state.Fold(ops, rules)
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	wantNote, ok := want.State["note"].([]any)
	if !ok || !reflect.DeepEqual(wantNote, []any{"a", "b"}) {
		t.Fatalf("test setup: state.Fold's note = %#v, want [\"a\", \"b\"] (flattened)", want.State["note"])
	}

	_, store := createTestStore(t, "0123456789abcdef")
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	enumRes := &dag.EnumerateResult{
		Ops:            map[string][]codec.Op{"w-1": ops},
		Cursors:        dag.CursorSet{"refs/writ/0123456789abcdef/widget": "op-note-1"},
		DecodedCommits: len(ops),
	}
	if _, err := db.Refresh(store, projection.WithSchema(map[string][]state.Rule{"widget": rules}), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	got := queryAppendValues(t, db.DB(), "o_widget__note")
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("o_widget__note = %v, want [a b] — one row per array element in the column's declared type, not one row holding a JSON array", got)
	}
}

// TestAppendTypedColumnStoresDeclaredType pins the plan's acceptance item 3
// — entries land "in the column's declared type" — for the per-element
// typed conversion writeAppendRows performs.
//
// That conversion is a path this ticket creates. Before the row-per-entry
// table, an array reaching a typed append target was handed whole to
// columnValue, which falls off every typed case's switch for a []any and
// returns nil, so the single cell written was NULL and no declared type was
// exercised per element at all. Now each element is converted individually
// against the target's resolved value_type, and two halves have to agree
// for the stored cell to read back as its declared type: the conversion in
// materialize.go (columnValue(plan.ValueType, e)) and the column's SQL type
// in ddl.go (sqlType(r.ValueType)).
//
// So the assertions are on typeof(value) as SQLite sees it and on the
// declared column type, not on the value alone: a value comparison cannot
// tell the integer 1 from the text "1", and an INTEGER column silently
// holding TEXT "true"/"false" answers `WHERE value = 1` with nothing.
// Dropping either half — converting as untyped, or declaring the column
// TEXT — reddens this test; nothing else in the tree covers a typed
// non-string append column, since a target bound by rules that disagree on
// value_type (version_bump_test.go) resolves to untyped by WRIT-205 and
// never reaches a typed case.
//
// The third target is untyped and carries a nested array, which pins the
// other half of the flattening contract in the same run: writeAppendRows
// flattens exactly one level, mirroring engine/internal/fold's
// appendAccumulator.Apply (`a.list = append(a.list, slice...)`), so an
// inner array stays one entry rather than being flattened recursively into
// its own elements.
func TestAppendTypedColumnStoresDeclaredType(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeWidgetOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	opNote := makeWidgetOp("op-note-1", []string{"op-create-1"}, "note", map[string]any{
		"flag":  []any{true, false},
		"count": []any{float64(1), float64(2)},
		"raw":   []any{[]any{"a", "b"}, "c"},
	}, base.Add(1*time.Second))
	ops := []codec.Op{opCreate, opNote}

	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	flagRule := state.Rule{OpType: "note", OpVersion: 1, Field: "flag", Target: "flags", Strategy: "append", ValueType: "bool", ObjectType: "widget"}
	countRule := state.Rule{OpType: "note", OpVersion: 1, Field: "count", Target: "counts", Strategy: "append", ValueType: "int", ObjectType: "widget"}
	rawRule := state.Rule{OpType: "note", OpVersion: 1, Field: "raw", Target: "raw_entries", Strategy: "append", ObjectType: "widget"}
	rules := []state.Rule{titleRule, flagRule, countRule, rawRule}

	want, err := state.Fold(ops, rules)
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	for _, tc := range []struct {
		target string
		want   []any
	}{
		{"flags", []any{true, false}},
		{"counts", []any{float64(1), float64(2)}},
		{"raw_entries", []any{[]any{"a", "b"}, "c"}},
	} {
		got, ok := want.State[tc.target].([]any)
		if !ok || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("test setup: state.Fold's %s = %#v, want %#v", tc.target, want.State[tc.target], tc.want)
		}
	}

	_, store := createTestStore(t, "0123456789abcdef")
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	enumRes := &dag.EnumerateResult{
		Ops:            map[string][]codec.Op{"w-1": ops},
		Cursors:        dag.CursorSet{"refs/writ/0123456789abcdef/widget": "op-note-1"},
		DecodedCommits: len(ops),
	}
	if _, err := db.Refresh(store, projection.WithSchema(map[string][]state.Rule{"widget": rules}), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	for _, tc := range []struct {
		table     string
		columnSQL string
		want      []typedAppendCell
	}{
		{
			table:     "o_widget__flags",
			columnSQL: "INTEGER",
			want: []typedAppendCell{
				{Value: "1", Type: "integer"},
				{Value: "0", Type: "integer"},
			},
		},
		{
			table:     "o_widget__counts",
			columnSQL: "INTEGER",
			want: []typedAppendCell{
				{Value: "1", Type: "integer"},
				{Value: "2", Type: "integer"},
			},
		},
		{
			table:     "o_widget__raw_entries",
			columnSQL: "TEXT",
			want: []typedAppendCell{
				{Value: `["a","b"]`, Type: "text"},
				{Value: "c", Type: "text"},
			},
		},
	} {
		if got := appendValueColumnType(t, db.DB(), tc.table); got != tc.columnSQL {
			t.Errorf("%s.value declared type = %q, want %q — sqlType must key off the target's resolved value_type", tc.table, got, tc.columnSQL)
		}
		got := queryTypedAppendCells(t, db.DB(), tc.table)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s cells = %+v, want %+v — every element converted against the target's declared value_type", tc.table, got, tc.want)
		}
	}
}

// typedAppendCell is one append entry's stored cell as SQLite reports it:
// the value rendered as text alongside typeof(), which is what distinguishes
// the integer 1 from the text "1" in an INTEGER column.
type typedAppendCell struct {
	Value string
	Type  string
}

func queryTypedAppendCells(t *testing.T, db *sql.DB, table string) []typedAppendCell {
	t.Helper()
	rows, err := db.Query("SELECT value, typeof(value) FROM "+table+" WHERE object_id = ? ORDER BY op_seq ASC, entry_idx ASC", "w-1")
	if err != nil {
		t.Fatalf("query %s: %v", table, err)
	}
	defer rows.Close()
	var out []typedAppendCell
	for rows.Next() {
		var cell typedAppendCell
		if err := rows.Scan(&cell.Value, &cell.Type); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		out = append(out, cell)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", table, err)
	}
	return out
}

// appendValueColumnType reports the SQL type an append table's value column
// was declared with, which is what gives the stored cell its affinity.
func appendValueColumnType(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	rows, err := db.Query("SELECT name, type FROM pragma_table_info(?)", table)
	if err != nil {
		t.Fatalf("pragma_table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, sqlType string
		if err := rows.Scan(&name, &sqlType); err != nil {
			t.Fatalf("scan pragma_table_info(%s): %v", table, err)
		}
		if name == "value" {
			return sqlType
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate pragma_table_info(%s): %v", table, err)
	}
	t.Fatalf("%s has no value column", table)
	return ""
}

// TestAppendExplicitNullContributesNoRow pins the end-to-end outcome for an
// explicit JSON null on a matched append field: no row lands, and the
// entries either side of it are unaffected.
//
// It does not pin the writer's own null check, and does not redden if that
// check is removed. The null never reaches writeAppendRows:
// engine/internal/fold's Uninterpretable quarantines the whole op (a null
// on any matched field, of any strategy, spec/fold.md §7.1), so the
// materializer's skip set drops it one level up — and codec's producer
// validation refuses a JSON null body value outright, so this shape is
// reachable only through WithEnumOverrideForTest, never through
// store.Append. What this test covers is that the two layers agree: the
// projection lands exactly what state.Fold does.
//
// The writer's `raw == nil` branch — a defensive mirror of
// appendAccumulator.Apply, and the second of the ticket's three folded-in
// latent defects (the old shared append table gated on presence alone, `if
// v, ok := body[field]; ok`, and wrote a NULL cell) — is pinned directly,
// with an empty skip set, by materialize_internal_test.go's
// TestWriteAppendRowsSkipsExplicitNullAtTheWriter.
func TestAppendExplicitNullContributesNoRow(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeWidgetOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	opNote1 := makeWidgetOp("op-note-1", []string{"op-create-1"}, "note", map[string]any{"note": "x"}, base.Add(1*time.Second))
	opNote2 := makeWidgetOp("op-note-2", []string{"op-note-1"}, "note", map[string]any{"note": nil}, base.Add(2*time.Second))
	ops := []codec.Op{opCreate, opNote1, opNote2}

	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	noteRule := state.Rule{OpType: "note", OpVersion: 1, Field: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	rules := []state.Rule{titleRule, noteRule}

	want, err := state.Fold(ops, rules)
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	wantNote, ok := want.State["note"].([]any)
	if !ok || !reflect.DeepEqual(wantNote, []any{"x"}) {
		t.Fatalf("test setup: state.Fold's note = %#v, want [\"x\"] — the explicit null contributes nothing", want.State["note"])
	}

	_, store := createTestStore(t, "0123456789abcdef")
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	enumRes := &dag.EnumerateResult{
		Ops:            map[string][]codec.Op{"w-1": ops},
		Cursors:        dag.CursorSet{"refs/writ/0123456789abcdef/widget": "op-note-2"},
		DecodedCommits: len(ops),
	}
	if _, err := db.Refresh(store, projection.WithSchema(map[string][]state.Rule{"widget": rules}), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	got := queryAppendValues(t, db.DB(), "o_widget__note")
	if !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("o_widget__note = %v, want [x] — an explicit JSON null must contribute no row, matching the fold", got)
	}
}

// TestAppendEmptyListContributesNoRow pins the plan's own deliberate,
// reviewer-bait tradeoff: an op writing an append target with an empty list
// contributes zero rows, indistinguishable via the append table alone from
// the target never having been written — even though the fold records `[]`
// (written-but-empty, spec/fold.md §5.5) and so still knows the difference.
// No sentinel row is added for this on purpose (see writeAppendRows and the
// PR description): the alternative reintroduces exactly the overloaded-NULL
// ambiguity this redesign exists to remove.
func TestAppendEmptyListContributesNoRow(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeWidgetOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	opNote := makeWidgetOp("op-note-1", []string{"op-create-1"}, "note", map[string]any{"note": []any{}}, base.Add(1*time.Second))
	ops := []codec.Op{opCreate, opNote}

	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	noteRule := state.Rule{OpType: "note", OpVersion: 1, Field: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	rules := []state.Rule{titleRule, noteRule}

	want, err := state.Fold(ops, rules)
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	wantNote, ok := want.State["note"].([]any)
	if !ok || len(wantNote) != 0 {
		t.Fatalf("test setup: state.Fold's note = %#v, want an empty (but present) list — the fold still knows this was written", want.State["note"])
	}

	_, store := createTestStore(t, "0123456789abcdef")
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	enumRes := &dag.EnumerateResult{
		Ops:            map[string][]codec.Op{"w-1": ops},
		Cursors:        dag.CursorSet{"refs/writ/0123456789abcdef/widget": "op-note-1"},
		DecodedCommits: len(ops),
	}
	if _, err := db.Refresh(store, projection.WithSchema(map[string][]state.Rule{"widget": rules}), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	got := queryAppendValues(t, db.DB(), "o_widget__note")
	if len(got) != 0 {
		t.Fatalf("o_widget__note = %v, want no rows — an op writing an empty list contributes zero rows, indistinguishable from never having been written (the plan's own known, deliberate tradeoff)", got)
	}
}

// appendFoldParityRules and appendFoldParityOps build the shape
// TestAppendFoldParityAcrossTargets and
// TestAppendMaterializationDeterministicUnderRuleShuffle share: three
// append targets exercising a plain single-rule shape ("notes", including
// an array write), the two-fields-one-envelope shape ("tags", WRIT-201), and
// the wildcard-op_version shape ("revisions", this ticket's own folded-in
// gap) — plus two quarantined ops, so op_seq carries real gaps: one
// unrecognized op type ("escalate"), and one explicit JSON null on a
// matched append field, which engine/internal/fold's Uninterpretable
// already rejects at the whole-op granularity (spec/fold.md §7.1, existing
// and unrelated to this ticket — a null on any matched field, of any
// strategy, quarantines the entire op, not just that field's write) before
// the op ever reaches an accumulator's Apply.
func appendFoldParityRules() []state.Rule {
	return []state.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"},
		{OpType: "note", OpVersion: 1, Field: "note", Target: "notes", Strategy: "append", ValueType: "string", ObjectType: "widget"},
		{OpType: "annotate", OpVersion: 1, Field: "a", Target: "tags", Strategy: "append", ValueType: "string", ObjectType: "widget"},
		{OpType: "annotate", OpVersion: 1, Field: "b", Target: "tags", Strategy: "append", ValueType: "string", ObjectType: "widget"},
		{OpType: "publish", OpVersion: 0, Field: "note_a", Target: "revisions", Strategy: "append", ValueType: "string", ObjectType: "widget"},
		{OpType: "publish", OpVersion: 2, Field: "note_b", Target: "revisions", Strategy: "append", ValueType: "string", ObjectType: "widget"},
	}
}

func appendFoldParityOps() []codec.Op {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeVersionedWidgetOp("op-create-1", nil, "create", 1, map[string]any{"title": "T"}, base)
	opNote1 := makeVersionedWidgetOp("op-note-1", []string{"op-create-1"}, "note", 1, map[string]any{"note": "n1"}, base.Add(1*time.Second))
	opNote2 := makeVersionedWidgetOp("op-note-2", []string{"op-note-1"}, "note", 1, map[string]any{"note": []any{"n2a", "n2b"}}, base.Add(2*time.Second))
	opNote3 := makeVersionedWidgetOp("op-note-3", []string{"op-note-2"}, "note", 1, map[string]any{"note": nil}, base.Add(3*time.Second))
	opEscalate := makeVersionedWidgetOp("op-escalate-1", []string{"op-note-3"}, "escalate", 1, map[string]any{"reason": "urgent"}, base.Add(4*time.Second))
	opAnnotate := makeVersionedWidgetOp("op-annotate-1", []string{"op-escalate-1"}, "annotate", 1, map[string]any{"a": "xa1", "b": "yb1"}, base.Add(5*time.Second))
	opPublish := makeVersionedWidgetOp("op-publish-1", []string{"op-annotate-1"}, "publish", 2, map[string]any{"note_a": "pa1", "note_b": "pb1"}, base.Add(6*time.Second))
	opNote4 := makeVersionedWidgetOp("op-note-4", []string{"op-publish-1"}, "note", 1, map[string]any{"note": "n4"}, base.Add(7*time.Second))
	return []codec.Op{opCreate, opNote1, opNote2, opNote3, opEscalate, opAnnotate, opPublish, opNote4}
}

// TestAppendFoldParityAcrossTargets is the plan's headline invariant: for
// every append target, the table's rows ordered by (op_seq, entry_idx) must
// equal state.Fold's own list for that target, element-for-element. This is
// what keeps the append tables a view of the fold rather than a second
// answer, across the array-flattening, two-fields-one-target and
// wildcard-op_version shapes at once, with two genuinely quarantined ops
// (real op_seq gaps) interleaved among them.
//
// It also pins op_seq's own contract, which the value-only parity check
// above is blind to: op_seq is each contributing op's index in
// dag.Order(ops) — the object's total order L — and not a running counter
// over the ops that survive quarantine. The two quarantined ops here sit at
// real positions in L, so the surviving rows must carry the gaps rather
// than compacting around them. Without this, a counter that renumbers
// around skipped ops passes every other test in the suite, and op_seq
// quietly becomes a function of the schema as well as the log: installing
// or removing a rule that changes what quarantines would renumber every
// later entry under a consumer that had recorded one.
//
// It also covers the droppable-cache bound as far as a synthetic op set
// can: a second, completely independent in-memory database, built from the
// identical op history via its own cold Refresh, reproduces byte-identical
// rows in every append table. db.Rebuild cannot stand in here — it re-walks
// the real store via store.EnumerateSince(nil), ignoring
// WithEnumOverrideForTest, which this op set (never actually written to the
// git store) never reaches — so a fresh database per pass is the strongest
// form of this check available to a test built this way (the same
// limitation TestAppendMaterializationDeterministicUnderRuleShuffle works
// around below). A real db.Rebuild over append tables, against ops actually
// written to a git store, is TestAppendTablesSurviveDropAndRebuild.
func TestAppendFoldParityAcrossTargets(t *testing.T) {
	ops := appendFoldParityOps()
	rules := appendFoldParityRules()

	want, err := state.Fold(ops, rules)
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	wantNotes, _ := want.State["notes"].([]any)
	wantTags, _ := want.State["tags"].([]any)
	wantRevisions, _ := want.State["revisions"].([]any)
	if !reflect.DeepEqual(wantNotes, []any{"n1", "n2a", "n2b", "n4"}) {
		t.Fatalf("test setup: state.Fold's notes = %#v, want [n1 n2a n2b n4]", wantNotes)
	}
	if !reflect.DeepEqual(wantTags, []any{"xa1", "yb1"}) {
		t.Fatalf("test setup: state.Fold's tags = %#v, want [xa1 yb1]", wantTags)
	}
	if !reflect.DeepEqual(wantRevisions, []any{"pa1", "pb1"}) {
		t.Fatalf("test setup: state.Fold's revisions = %#v, want [pa1 pb1]", wantRevisions)
	}
	wantUnknown := map[string]bool{"op-note-3": true, "op-escalate-1": true}
	if len(want.UnknownOps) != len(wantUnknown) {
		t.Fatalf("test setup: state.Fold's UnknownOps = %+v, want exactly %v", want.UnknownOps, wantUnknown)
	}
	for _, u := range want.UnknownOps {
		if !wantUnknown[u.Commit] {
			t.Fatalf("test setup: state.Fold's UnknownOps = %+v, want exactly %v", want.UnknownOps, wantUnknown)
		}
	}

	// seqOf is the ground truth op_seq is measured against: each op's index
	// in the object's total order L, computed by the same dag.Order the
	// materializer folds through. Quarantined ops occupy positions here too
	// — that is exactly what makes the gaps below real.
	order, err := dag.Order(ops)
	if err != nil {
		t.Fatalf("dag.Order failed: %v", err)
	}
	seqOf := make(map[string]int, len(order))
	for i, op := range order {
		seqOf[op.ID] = i
	}
	wantEntries := map[string][]appendEntry{
		"o_widget__notes": {
			{OpSeq: seqOf["op-note-1"], EntryIdx: 0, Value: "n1"},
			{OpSeq: seqOf["op-note-2"], EntryIdx: 0, Value: "n2a"},
			{OpSeq: seqOf["op-note-2"], EntryIdx: 1, Value: "n2b"},
			{OpSeq: seqOf["op-note-4"], EntryIdx: 0, Value: "n4"},
		},
		"o_widget__tags": {
			{OpSeq: seqOf["op-annotate-1"], EntryIdx: 0, Value: "xa1"},
			{OpSeq: seqOf["op-annotate-1"], EntryIdx: 1, Value: "yb1"},
		},
		"o_widget__revisions": {
			{OpSeq: seqOf["op-publish-1"], EntryIdx: 0, Value: "pa1"},
			{OpSeq: seqOf["op-publish-1"], EntryIdx: 1, Value: "pb1"},
		},
	}
	// The positions the two quarantined ops hold in L. No append table may
	// carry a row at either, and — the half a compacting counter would break
	// — every op after them must still sit at its own index in L, not at one
	// reduced by the number of ops skipped before it.
	quarantinedSeqs := map[int]string{
		seqOf["op-note-3"]:     "op-note-3",
		seqOf["op-escalate-1"]: "op-escalate-1",
	}

	rulesByType := map[string][]state.Rule{"widget": rules}
	enumRes := &dag.EnumerateResult{
		Ops:            map[string][]codec.Op{"w-1": ops},
		Cursors:        dag.CursorSet{"refs/writ/0123456789abcdef/widget": "op-note-4"},
		DecodedCommits: len(ops),
	}

	checkParity := func(t *testing.T, stage string, rawDB *sql.DB) {
		t.Helper()
		toStrings := func(vals []any) []string {
			out := make([]string, len(vals))
			for i, v := range vals {
				out[i], _ = v.(string)
			}
			return out
		}
		checks := []struct {
			table string
			want  []any
		}{
			{"o_widget__notes", wantNotes},
			{"o_widget__tags", wantTags},
			{"o_widget__revisions", wantRevisions},
		}
		for _, c := range checks {
			got := queryAppendValues(t, rawDB, c.table)
			want := toStrings(c.want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: %s rows = %v, want (state.Fold) %v", stage, c.table, got, want)
			}

			gotEntries := queryAppendEntries(t, rawDB, c.table, "w-1")
			if !reflect.DeepEqual(gotEntries, wantEntries[c.table]) {
				t.Fatalf("%s: %s (op_seq, entry_idx, value) rows = %+v, want %+v — op_seq is each op's index in dag.Order's L, with the quarantined ops' positions left as gaps, not a counter compacting around them (WRIT-212)", stage, c.table, gotEntries, wantEntries[c.table])
			}
			for _, e := range gotEntries {
				if id, quarantined := quarantinedSeqs[e.OpSeq]; quarantined {
					t.Fatalf("%s: %s carries a row at op_seq %d, the position %s (quarantined into unknown_ops) holds in L", stage, c.table, e.OpSeq, id)
				}
			}
		}
	}

	for _, stage := range []string{"first build", "second, independent build"} {
		_, store := createTestStore(t, "0123456789abcdef")
		db, err := projection.Open(":memory:")
		if err != nil {
			t.Fatalf("[%s] Open(:memory:) failed: %v", stage, err)
		}
		defer db.Close()

		if _, err := db.Refresh(store, projection.WithSchema(rulesByType), projection.WithEnumOverrideForTest(enumRes)); err != nil {
			t.Fatalf("[%s] Refresh failed: %v", stage, err)
		}
		checkParity(t, stage, db.DB())
	}
}

// TestAppendQuarantinedOpWithValidAppendValueContributesNoRow pins
// writeAppendRows' quarantined-op skip — the one mechanism keeping an op
// the fold refused out of the append tables — against the only op shape
// that actually exercises it.
//
// Every other quarantined op in this file is quarantined for a reason that
// independently means no append rule would contribute at it anyway: an
// unrecognized op type no append rule matches (op-escalate-1), an explicit
// null the writer's own `raw == nil` check drops (op-note-3 in
// appendFoldParityOps), or rules the projection's schema does not carry at
// all (TestAppendTablesSurviveDropAndRebuild's tag op). So none of them
// redden if the skip is deleted.
//
// This one does, because engine/internal/fold's Uninterpretable works at
// whole-op granularity (spec/fold.md §7.1): if *any* matched rule rejects
// the body, the entire op is quarantined, including writes at fields whose
// own rules would have accepted them. op-note-2 below carries a perfectly
// valid append value at "note" and a non-bool at "flag", whose sibling
// tombstone rule matches the same op type — so the fold quarantines it and
// its append value never enters state.Fold's list. Without the skip the
// writer would still see the op and write "should-not-appear" into
// o_widget__notes: the append table would carry an entry the fold never
// blessed, which is the droppable-cache invariant (the projection holds
// nothing the fold does not produce) rather than a cosmetic one, and
// exactly the projection-as-a-second-answer divergence the row-per-entry
// redesign exists to prevent.
func TestAppendQuarantinedOpWithValidAppendValueContributesNoRow(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeVersionedWidgetOp("op-create-1", nil, "create", 1, map[string]any{"title": "T"}, base)
	opGood := makeVersionedWidgetOp("op-note-1", []string{"op-create-1"}, "note", 1, map[string]any{"note": "good"}, base.Add(1*time.Second))
	// The op the skip is for: a legal append value at "note", and a value
	// the sibling tombstone rule at "flag" cannot consume.
	opMixed := makeVersionedWidgetOp("op-note-2", []string{"op-note-1"}, "note", 1, map[string]any{"note": "should-not-appear", "flag": "not-a-bool"}, base.Add(2*time.Second))
	opAfter := makeVersionedWidgetOp("op-note-3", []string{"op-note-2"}, "note", 1, map[string]any{"note": "after"}, base.Add(3*time.Second))
	ops := []codec.Op{opCreate, opGood, opMixed, opAfter}

	rules := []state.Rule{
		{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"},
		{OpType: "note", OpVersion: 1, Field: "note", Target: "notes", Strategy: "append", ValueType: "string", ObjectType: "widget"},
		{OpType: "note", OpVersion: 1, Field: "flag", Target: "flag", Strategy: "tombstone", ValueType: "bool", ObjectType: "widget"},
	}

	want, err := state.Fold(ops, rules)
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	wantNotes, _ := want.State["notes"].([]any)
	if !reflect.DeepEqual(wantNotes, []any{"good", "after"}) {
		t.Fatalf("test setup: state.Fold's notes = %#v, want [good after] — op-note-2's append value must not reach the fold's list", wantNotes)
	}
	if len(want.UnknownOps) != 1 || want.UnknownOps[0].Commit != "op-note-2" {
		t.Fatalf("test setup: state.Fold's UnknownOps = %+v, want exactly op-note-2 (quarantined by its sibling tombstone rule)", want.UnknownOps)
	}

	order, err := dag.Order(ops)
	if err != nil {
		t.Fatalf("dag.Order failed: %v", err)
	}
	seqOf := make(map[string]int, len(order))
	for i, op := range order {
		seqOf[op.ID] = i
	}

	_, store := createTestStore(t, "0123456789abcdef")
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	enumRes := &dag.EnumerateResult{
		Ops:            map[string][]codec.Op{"w-1": ops},
		Cursors:        dag.CursorSet{"refs/writ/0123456789abcdef/widget": "op-note-3"},
		DecodedCommits: len(ops),
	}
	if _, err := db.Refresh(store, projection.WithSchema(map[string][]state.Rule{"widget": rules}), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	// The full (op_seq, entry_idx, value) row set, not just the values: the
	// quarantined op's position in L must be a gap, and op-note-3 must keep
	// its own index rather than sliding into it.
	got := queryAppendEntries(t, db.DB(), "o_widget__notes", "w-1")
	wantEntries := []appendEntry{
		{OpSeq: seqOf["op-note-1"], EntryIdx: 0, Value: "good"},
		{OpSeq: seqOf["op-note-3"], EntryIdx: 0, Value: "after"},
	}
	if !reflect.DeepEqual(got, wantEntries) {
		t.Fatalf("o_widget__notes rows = %+v, want %+v — op-note-2 is quarantined by its sibling tombstone rule, so its append value must not land in the table even though the append rule itself would have accepted it (WRIT-212)", got, wantEntries)
	}
}

// TestAppendMaterializationDeterministicUnderRuleShuffle extends
// TestGeneratedDDLIsDeterministic (DDL/digest only) and the old
// TestAppendGroupContentIsDeterministic (one shuffle) to the full
// materialized content, shuffled several times: the same op history, folded
// against several independently-shuffled orderings of the identical rule
// set, must produce byte-identical DumpTables output every time. Building
// each pass in a fresh in-memory database is this file's substitute for a
// real db.Rebuild, which — like TestAppendFoldParityAcrossTargets — a
// synthetic op set reaching Refresh through WithEnumOverrideForTest cannot
// call: Rebuild re-walks the git store, which was never written. The real
// drop-and-rebuild is TestAppendTablesSurviveDropAndRebuild.
func TestAppendMaterializationDeterministicUnderRuleShuffle(t *testing.T) {
	ops := appendFoldParityOps()
	baseRules := appendFoldParityRules()

	run := func(t *testing.T, rules []state.Rule) map[string][]map[string]any {
		t.Helper()
		_, store := createTestStore(t, "0123456789abcdef")
		db, err := projection.Open(":memory:")
		if err != nil {
			t.Fatalf("Open(:memory:) failed: %v", err)
		}
		defer db.Close()

		enumRes := &dag.EnumerateResult{
			Ops:            map[string][]codec.Op{"w-1": ops},
			Cursors:        dag.CursorSet{"refs/writ/0123456789abcdef/widget": "op-note-4"},
			DecodedCommits: len(ops),
		}
		if _, err := db.Refresh(store, projection.WithSchema(map[string][]state.Rule{"widget": rules}), projection.WithEnumOverrideForTest(enumRes)); err != nil {
			t.Fatalf("Refresh failed: %v", err)
		}
		dump, err := db.DumpTables()
		if err != nil {
			t.Fatalf("DumpTables: %v", err)
		}
		return dump
	}

	want := run(t, append([]state.Rule(nil), baseRules...))

	for i := 0; i < 3; i++ {
		shuffled := append([]state.Rule(nil), baseRules...)
		rand.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		got := run(t, shuffled)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffle %d: DumpTables differs from the unshuffled rule order — materialized content is order-dependent\nwant: %+v\ngot:  %+v", i, want, got)
		}
	}
}

// appendRebuildRules is testRules() with widget's "tag" rules removed. The
// producer vocabulary createTestStore's store validates writes against
// (appendRules()) still declares them, so a "tag" op is a perfectly legal
// write that this projection instance cannot interpret — it quarantines
// into unknown_ops and leaves a real gap in op_seq. A repo resolves the
// producer vocabulary and a projection's rule index independently, so the
// two differing is a state writ reaches, not a fiction (see appendRules'
// own doc).
func appendRebuildRules() map[string][]state.Rule {
	rules := testRules()
	kept := make([]state.Rule, 0, len(rules["widget"]))
	for _, r := range rules["widget"] {
		if r.OpType == "tag" {
			continue
		}
		kept = append(kept, r)
	}
	rules["widget"] = kept
	return rules
}

// TestAppendTablesSurviveDropAndRebuild is the plan's acceptance item 8 for
// the append tables: drop the projection file outright and db.Rebuild
// reproduces it exactly — the same DumpTables, and the same (op_seq,
// entry_idx, value) rows in every append table, gaps included.
//
// It is a distinct property from the second independent forward build
// TestAppendFoldParityAcrossTargets runs, and cannot be checked there.
// A forward build exercises Refresh over ops handed straight to it;
// Rebuild truncates and re-walks the real git store through
// readOpsForObject before re-deriving dag.Order, and it is that path the
// droppable-cache invariant is about — so this test writes its ops to a
// real store via store.Append rather than through
// WithEnumOverrideForTest, which Rebuild ignores.
//
// TestDropAndRebuildReproducesFoldedState (local_test.go) runs the same
// drop-and-rebuild but appends only create/update ops, so both its dumps
// have empty append tables and it says nothing about them.
func TestAppendTablesSurviveDropAndRebuild(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	tempDir := t.TempDir()
	projPath := filepath.Join(tempDir, "projection.db")
	localPath := filepath.Join(tempDir, "local.db")

	db, err := projection.Open(projPath, projection.WithLocalPath(localPath))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	const objID = "w-rebuild"
	const (
		oid1 = "0000000000000000000000000000000000000001"
		oid2 = "0000000000000000000000000000000000000002"
		oid3 = "0000000000000000000000000000000000000003"
		oid4 = "0000000000000000000000000000000000000004"
		oid5 = "0000000000000000000000000000000000000005"
	)

	// One Refresh per Append, so the incremental leg is genuinely
	// incremental. testRules declares "revision"'s base and head as two
	// separate append targets written under one envelope, so step 1 and
	// step 4 each contribute to both tables at a shared op_seq, and step 3
	// to head alone.
	steps := []codec.Envelope{
		makeWidgetEnv(objID, "create", map[string]any{"title": "Rebuild Widget", "description": "initial"}),
		makeWidgetEnv(objID, "revision", map[string]any{"base": oid1, "head": oid2}),
		makeWidgetEnv(objID, "tag", map[string]any{"add": []any{"urgent"}}),
		makeWidgetEnv(objID, "revision", map[string]any{"head": oid3}),
		makeWidgetEnv(objID, "revision", map[string]any{"base": oid4, "head": oid5}),
	}
	var quarantinedOpID string
	for i, env := range steps {
		op, err := store.Append(ctx, env, nil)
		if err != nil {
			t.Fatalf("store.Append step %d (%s) failed: %v", i, env.OpType, err)
		}
		if env.OpType == "tag" {
			quarantinedOpID = op.ID
		}
		if _, err := db.Refresh(store, projection.WithSchema(appendRebuildRules())); err != nil {
			t.Fatalf("Refresh step %d failed: %v", i, err)
		}
	}

	// The ops form a linear chain in append order, so each step's index
	// above is its index in the object's total order L: create 0, revision
	// 1, the quarantined tag op 2, revision 3, revision 4. op_seq 2 is
	// therefore absent from both tables and every later op keeps its own
	// index rather than sliding down one.
	wantBase := []appendEntry{
		{OpSeq: 1, EntryIdx: 0, Value: oid1},
		{OpSeq: 4, EntryIdx: 0, Value: oid4},
	}
	wantHead := []appendEntry{
		{OpSeq: 1, EntryIdx: 0, Value: oid2},
		{OpSeq: 3, EntryIdx: 0, Value: oid3},
		{OpSeq: 4, EntryIdx: 0, Value: oid5},
	}

	checkAppendRows := func(t *testing.T, stage string, rawDB *sql.DB) {
		t.Helper()
		for _, c := range []struct {
			table string
			want  []appendEntry
		}{
			{"o_widget__base", wantBase},
			{"o_widget__head", wantHead},
		} {
			got := queryAppendEntries(t, rawDB, c.table, objID)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("%s: %s rows = %+v, want %+v", stage, c.table, got, c.want)
			}
		}
		var gotUnknown string
		if err := rawDB.QueryRow("SELECT op_id FROM unknown_ops WHERE object_id = ?", objID).Scan(&gotUnknown); err != nil {
			t.Fatalf("%s: query unknown_ops: %v", stage, err)
		}
		if gotUnknown != quarantinedOpID {
			t.Fatalf("%s: unknown_ops op_id = %s, want %s (the tag op this schema cannot interpret)", stage, gotUnknown, quarantinedOpID)
		}
	}

	checkAppendRows(t, "incremental", db.DB())

	incrementalDump, err := db.DumpTables()
	if err != nil {
		t.Fatalf("DumpTables incremental failed: %v", err)
	}

	// Drop the cache outright: close the handle and delete the file.
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if err := os.Remove(projPath); err != nil {
		t.Fatalf("Remove projection.db failed: %v", err)
	}

	dbFresh, err := projection.Open(projPath, projection.WithLocalPath(localPath))
	if err != nil {
		t.Fatalf("Open dbFresh failed: %v", err)
	}
	defer dbFresh.Close()

	stats, err := dbFresh.Rebuild(store, projection.WithSchema(appendRebuildRules()))
	if err != nil {
		t.Fatalf("Rebuild failed: %v", err)
	}
	if !stats.Rebuilt || stats.ObjectsTouched != 1 {
		t.Fatalf("unexpected rebuild stats: %+v", stats)
	}

	checkAppendRows(t, "rebuild", dbFresh.DB())

	coldDump, err := dbFresh.DumpTables()
	if err != nil {
		t.Fatalf("DumpTables cold failed: %v", err)
	}
	if !reflect.DeepEqual(incrementalDump, coldDump) {
		t.Fatalf("incremental dump != cold dump after dropping the projection and rebuilding:\nincremental: %+v\ncold: %+v", incrementalDump, coldDump)
	}
}
