package projection_test

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/engine/state"
)

// makeWidgetEnv and makeWidgetOp build ops for a synthetic "widget" object
// type directly, the same way makeReviewEnv does for "review" in
// refresh_test.go: no schema validates a "widget" op body, so this
// reaches Refresh with no producer-validation gate to route around,
// regardless of whether the body shape below is one a real schema would
// ever declare.
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

// TestAppendGroupContentIsDeterministic is WRIT-189 round 3 MAJOR-1's own
// executable statement: an append target legally declared under two
// envelopes — here two different op_types, "note-v1" and "note-v2", both
// agreeing on strategy and value_type and both targeting the same field
// "note" (spec/schema-ops.md §8's "two different op_types ... that happen
// to reuse the same target" carve-out, which requires exactly this
// agreement) — must have every envelope's ops materialized, and the result
// must not depend on which order buildTypeDescriptor happens to see the two
// rules in.
//
// Before this fix, buildTypeDescriptor keyed the whole append group off
// reps[tk] — one representative rule picked by encounter order in the
// rules slice — so only whichever envelope came first contributed rows:
// the other op's row was silently dropped, and which one survived flipped
// with the rules slice's order even though the generated DDL and its
// digest stayed byte-identical either way (the digest cannot see a content
// difference, only a schema one). This test builds the identical ops
// against two rule-index orderings of the same rules and checks the
// materialized o_widget__note rows are identical AND complete (both ops
// present) — verified by temporarily reverting the ddl.go/materialize.go
// fix locally, which turns this red (one row, not two, and content flips
// between the two orderings) before turning it back green.
func TestAppendGroupContentIsDeterministic(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeWidgetOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	opNoteV1 := makeWidgetOp("op-note-v1-1", []string{"op-create-1"}, "note-v1", map[string]any{"note": "a"}, base.Add(1*time.Second))
	opNoteV2 := makeWidgetOp("op-note-v2-1", []string{"op-note-v1-1"}, "note-v2", map[string]any{"note": "b"}, base.Add(2*time.Second))
	ops := []codec.Op{opCreate, opNoteV1, opNoteV2}

	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	noteV1Rule := state.Rule{OpType: "note-v1", Field: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	noteV2Rule := state.Rule{OpType: "note-v2", Field: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}

	// Two rule-index orderings of the identical rule set: buildDescriptor's
	// reps map used to pick whichever of noteV1Rule/noteV2Rule it saw first
	// as target "note"'s sole representative.
	rulesOriginal := map[string][]state.Rule{"widget": {titleRule, noteV1Rule, noteV2Rule}}
	rulesReordered := map[string][]state.Rule{"widget": {noteV2Rule, titleRule, noteV1Rule}}

	// state.Fold is the reference this generic table has to match: it
	// groups matched rules by target key alone (spec/fold.md §5), not by
	// op_type/op_version, so both ops contribute to the one "note" list
	// regardless of which of the two rules "represents" the target.
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

		rows, err := db.DB().Query("SELECT idx, COALESCE(f_note, '') FROM o_widget__note WHERE object_id = ? ORDER BY idx ASC", "w-1")
		if err != nil {
			t.Fatalf("query o_widget__note: %v", err)
		}
		defer rows.Close()

		var notes []string
		for rows.Next() {
			var idx int
			var note string
			if err := rows.Scan(&idx, &note); err != nil {
				t.Fatalf("scan o_widget__note row: %v", err)
			}
			notes = append(notes, note)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate o_widget__note: %v", err)
		}
		return notes
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

// TestAppendGroupTargetDiffersFromField is WRIT-189 round 4 MAJOR-1's own
// executable statement: an append rule whose declared target(...) differs
// from its field must still materialize the value the op body actually
// carries under that field name, not under the target key.
//
// Two append targets, "base" and "head", both declared under the same
// envelope (op_type "push", op_version 1) so they land in one shared
// appendGroupPlan table (buildAppendGroups unions same-envelope targets) —
// "base" is written from body field "base_sha" and "head" from body field
// "head_sha", exactly review's own base/head shape but with target(...)
// used, which the built-in review rules never do on an append target
// (that gap is why three rounds walked past this). writeAppendGroupRows
// used to read body[m.Key] — m.Key is state.Rule.TargetKey(), "base" or
// "head" — instead of body[field], so both columns materialized NULL even
// though the op body plainly carried "aaa" and "bbb" under "base_sha" and
// "head_sha". Reverting the fieldForOp fix locally (reading body[m.Key]
// again) turns this red: both f_base and f_head come back empty while
// state.Fold below still reports the values, exactly the round 4 probe.
func TestAppendGroupTargetDiffersFromField(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeWidgetOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	opPush := makeWidgetOp("op-push-1", []string{"op-create-1"}, "push", map[string]any{"base_sha": "aaa", "head_sha": "bbb"}, base.Add(1*time.Second))
	ops := []codec.Op{opCreate, opPush}

	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	baseRule := state.Rule{OpType: "push", OpVersion: 1, Field: "base_sha", Target: "base", Strategy: "append", ValueType: "git-oid", ObjectType: "widget"}
	headRule := state.Rule{OpType: "push", OpVersion: 1, Field: "head_sha", Target: "head", Strategy: "append", ValueType: "git-oid", ObjectType: "widget"}
	rules := []state.Rule{titleRule, baseRule, headRule}

	// The probe: state.Fold is what the materialized table has to agree
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

	var idx int
	var fBase, fHead sql.NullString
	row := db.DB().QueryRow("SELECT idx, f_base, f_head FROM o_widget__base_head WHERE object_id = ?", "w-1")
	if err := row.Scan(&idx, &fBase, &fHead); err != nil {
		t.Fatalf("query o_widget__base_head: %v", err)
	}

	if !fBase.Valid || fBase.String != "aaa" {
		t.Fatalf("o_widget__base_head.f_base = %v (valid=%v), want \"aaa\" — target(...) differing from field was read by target key instead of field name", fBase.String, fBase.Valid)
	}
	if !fHead.Valid || fHead.String != "bbb" {
		t.Fatalf("o_widget__base_head.f_head = %v (valid=%v), want \"bbb\" — target(...) differing from field was read by target key instead of field name", fHead.String, fHead.Valid)
	}
}

// TestAppendGroupOmittedFieldPairing restores, generically, the coverage
// WRIT-189 round 2's MAJOR-1 finding pinned in a since-deleted per-type
// test (WRIT-195 round 1 MEDIUM finding): a multi-field
// append group where one op writes only one of the group's fields, the
// other field entirely absent rather than written empty.
//
// Before WRIT-189 round 2's fix, Reviews/Review zipped two independent
// append child tables (o_review__base and o_review__head) back into pairs
// by local list position — an op that wrote only "base" shifted every
// later op's "head" value out of step with its own "base", mispairing both
// while still reporting a plausible-looking row count. ddl.go's
// appendGroupPlan fixed this generically by materializing one row per
// contributing op (writeAppendGroupRows), with SQL NULL for a field that
// particular op didn't write, so the pairing is fixed at write time by
// construction: reading the table in idx order gives the right base/head
// pair for each op regardless of which fields any single op wrote.
//
// This test pins that generic per-op correspondence directly — using the
// synthetic "widget" object type's "push" envelope (target(...) used, same
// as TestAppendGroupTargetDiffersFromField) rather than review's
// base/head — and cross-checks it against state.Fold's own (independent,
// per-target, unaligned) base/head lists: state.Fold has no concept of
// pairing across targets at all, so the correspondence guarantee lives
// entirely in the generic table's one-row-per-op structure, not in the pure
// fold.
func TestAppendGroupOmittedFieldPairing(t *testing.T) {
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

	rows, err := db.DB().Query("SELECT idx, f_base, f_head FROM o_widget__base_head WHERE object_id = ? ORDER BY idx ASC", "w-1")
	if err != nil {
		t.Fatalf("query o_widget__base_head: %v", err)
	}
	defer rows.Close()

	type row struct {
		idx        int
		base, head sql.NullString
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.idx, &r.base, &r.head); err != nil {
			t.Fatalf("scan o_widget__base_head row: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate o_widget__base_head: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("o_widget__base_head has %d rows, want 2 (one per push op): %+v", len(got), got)
	}
	// Row 0 is opPush1: base="aaa", head omitted (NULL) — not shifted to
	// pair with opPush2's head.
	if !got[0].base.Valid || got[0].base.String != "aaa" {
		t.Fatalf("row 0 f_base = %v (valid=%v), want \"aaa\"", got[0].base.String, got[0].base.Valid)
	}
	if got[0].head.Valid {
		t.Fatalf("row 0 f_head = %q, want NULL (opPush1 never wrote head_sha) — an omitted field must not be paired with a later op's value", got[0].head.String)
	}
	// Row 1 is opPush2: both fields present.
	if !got[1].base.Valid || got[1].base.String != "bbb" {
		t.Fatalf("row 1 f_base = %v (valid=%v), want \"bbb\"", got[1].base.String, got[1].base.Valid)
	}
	if !got[1].head.Valid || got[1].head.String != "ccc" {
		t.Fatalf("row 1 f_head = %v (valid=%v), want \"ccc\"", got[1].head.String, got[1].head.Valid)
	}
}

// TestAppendGroupAmbiguousFieldFallsToUnknownOps is WRIT-189 round 5's
// MAJOR-1 finding at materialize level: ddl_internal_test.go's
// TestAmbiguousAppendFieldWithholdsTables pins that buildDescriptor withholds
// a type whose append rules bind one target to two different Fields under
// one exact (op_type, op_version) envelope; this test pins the consequence a
// caller of Refresh actually observes — the object's ops land in
// unknown_ops (no o_widget table exists at all to hold a silently-NULLed
// row), for both rule orderings, even though the value ("yb") is plainly
// present in the op's own body and state.Fold folds it successfully.
func TestAppendGroupAmbiguousFieldFallsToUnknownOps(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeWidgetOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	opNote := makeWidgetOp("op-note-1", []string{"op-create-1"}, "note", map[string]any{"b": "yb"}, base.Add(1*time.Second))
	ops := []codec.Op{opCreate, opNote}

	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	noteFieldA := state.Rule{OpType: "note", OpVersion: 1, Field: "a", Target: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	noteFieldB := state.Rule{OpType: "note", OpVersion: 1, Field: "b", Target: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}

	// state.Fold itself resolves this shape deterministically regardless of
	// rule order — the probe that makes clear this is a projection-side
	// withhold, not a fold-level rejection.
	want, err := state.Fold(ops, []state.Rule{titleRule, noteFieldA, noteFieldB})
	if err != nil {
		t.Fatalf("state.Fold failed: %v", err)
	}
	wantNotes, ok := want.State["note"].([]any)
	if !ok || !reflect.DeepEqual(wantNotes, []any{"yb"}) {
		t.Fatalf("test setup: state.Fold's note = %#v, want [\"yb\"]", want.State["note"])
	}

	orderings := map[string][]state.Rule{
		"a-then-b": {titleRule, noteFieldA, noteFieldB},
		"b-then-a": {titleRule, noteFieldB, noteFieldA},
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

			var tableCount int
			if err := db.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'o_widget'").Scan(&tableCount); err != nil {
				t.Fatalf("query sqlite_master: %v", err)
			}
			if tableCount != 0 {
				t.Fatalf("expected o_widget to be withheld (no table), but it exists")
			}

			var objType string
			if err := db.DB().QueryRow("SELECT object_type FROM objects WHERE object_id = ?", "w-1").Scan(&objType); err != nil {
				t.Fatalf("query objects: %v", err)
			}
			if objType != "widget" {
				t.Fatalf("objects.object_type = %q, want \"widget\"", objType)
			}

			rows, err := db.DB().Query("SELECT op_id FROM unknown_ops WHERE object_id = ? ORDER BY op_index ASC", "w-1")
			if err != nil {
				t.Fatalf("query unknown_ops: %v", err)
			}
			defer rows.Close()
			var opIDs []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					t.Fatalf("scan unknown_ops row: %v", err)
				}
				opIDs = append(opIDs, id)
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("iterate unknown_ops: %v", err)
			}
			want := []string{"op-create-1", "op-note-1"}
			if !reflect.DeepEqual(opIDs, want) {
				t.Fatalf("unknown_ops op_ids = %#v, want %#v — withheld type's ops must all fall to unknown_ops", opIDs, want)
			}
		})
	}
}
