package projection_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/projection"
	"github.com/writtendev/writ/internal/state"
)

// makeVersionedOp is makeWidgetOp (append_group_test.go) generalized over
// object id and op_version: WRIT-205's version-bump shapes need two objects
// (one per version, so a disagreeing version's values are pinned
// independently of the other) and an op_version other than makeWidgetEnv's
// hardcoded 1.
func makeVersionedOp(objID, id string, parents []string, opType string, opVersion int64, body map[string]any, when time.Time) codec.Op {
	bodyRaw, _ := json.Marshal(body)
	env := codec.Envelope{
		ObjectID:   objID,
		ObjectType: "widget",
		OpType:     opType,
		OpVersion:  opVersion,
		Body:       bodyRaw,
	}
	raw, _ := codec.EncodePayload(env)
	env.Raw = raw
	author := codec.Identity{Name: "Test Writer", Email: "writer@example.com", When: when}
	return codec.Op{
		Envelope:  env,
		ID:        id,
		Parents:   parents,
		Author:    author,
		Committer: author,
		Message:   "writ: " + opType + " widget/" + objID + "\n",
	}
}

// TestScalarLWWVersionBumpIsDeterministic is WRIT-205's own executable
// statement for the plain scalar lww path: "note" is declared under
// op_version 1 with value_type "string" and under op_version 2 with
// value_type "int" — a version bump of the same (op_type, field) legally
// changing value_type, per spec/fieldrules.go's carve-out and
// spec/schema-ops.md §8. Before this fix, buildTypeDescriptor's reps[tk]
// took ValueType off whichever of the two rules a slice happened to present
// first, so the generated column's SQL type — and so whether the other
// version's folded value survived columnValue's type coercion — depended on
// rule-slice order.
//
// Two objects, one written under each version, assert both halves: the
// createSQL/digest checks below pin the DDL half (an untyped f_note column,
// identically under both rule orderings), and noteW1/noteW2 pin that
// neither version's value comes back NULL under either ordering.
//
// Only the DDL half still catches a reps[tk] regression. Since WRIT-279 gave
// columnValue's int/number/bool fallthroughs `return toText(v)` instead of
// nil, a disagreeing version's value no longer comes back NULL even when
// ddl.go's WRIT-205 widening is reverted and an ordering picks "int" as
// tk's representative: it lands as the text "alpha" in the resulting
// INTEGER-affinity column instead, and noteW1/noteW2 both stay green.
// Verified by reverting the ddl.go widening alone, WRIT-279's materialize.go
// fix left in place: the createSQL assertion below reddens (f_note
// INTEGER, not TEXT) while the content assertions still pass. The content
// half's WRIT-205-era regression signal is subsumed by that fix — it is no
// longer a second, independent guard against reps[tk] picking the wrong
// representative, only a (still-true) NULL-freedom check.
//
// Per spec/value-types.md's "untyped" definition and
// spec/forward-compatibility.md §"Targets a projection declines" (the
// decline permission is conditioned on storage that cannot express what the
// fold blesses — untyped TEXT storage can), the correct column is untyped
// TEXT, not either version's own type and not a widened third type.
func TestScalarLWWVersionBumpIsDeterministic(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()

	opCreate1 := makeVersionedOp("w-1", "op-create-1", nil, "create", 1, map[string]any{"title": "T1"}, base)
	opNoteV1 := makeVersionedOp("w-1", "op-note-v1-1", []string{"op-create-1"}, "note", 1, map[string]any{"note": "alpha"}, base.Add(1*time.Second))

	opCreate2 := makeVersionedOp("w-2", "op-create-2", nil, "create", 1, map[string]any{"title": "T2"}, base)
	opNoteV2 := makeVersionedOp("w-2", "op-note-v2-1", []string{"op-create-2"}, "note", 2, map[string]any{"note": 7}, base.Add(1*time.Second))

	titleRule := state.Rule{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	noteV1Rule := state.Rule{OpType: "note", OpVersion: 1, Field: "note", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	noteV2Rule := state.Rule{OpType: "note", OpVersion: 2, Field: "note", Strategy: "lww", ValueType: "int", ObjectType: "widget"}

	rulesOriginal := map[string][]state.Rule{"widget": {titleRule, noteV1Rule, noteV2Rule}}
	rulesReordered := map[string][]state.Rule{"widget": {noteV2Rule, titleRule, noteV1Rule}}

	type result struct {
		digest    string
		createSQL string
		noteW1    sql.NullString
		noteW2    sql.NullString
	}

	run := func(t *testing.T, rules map[string][]state.Rule) result {
		t.Helper()
		_, store := createTestStore(t, "0123456789abcdef")

		db, err := projection.Open(":memory:")
		if err != nil {
			t.Fatalf("Open(:memory:) failed: %v", err)
		}
		defer db.Close()

		enumRes := &dag.EnumerateResult{
			Ops: map[string][]codec.Op{
				"w-1": {opCreate1, opNoteV1},
				"w-2": {opCreate2, opNoteV2},
			},
			Cursors: dag.CursorSet{
				"refs/writ/0123456789abcdef/widget": "op-note-v2-1",
			},
			DecodedCommits: 4,
		}

		if _, err := db.Refresh(store, projection.WithSchema(rules), projection.WithEnumOverrideForTest(enumRes)); err != nil {
			t.Fatalf("Refresh failed: %v", err)
		}

		var digest string
		if err := db.DB().QueryRow("SELECT value FROM meta WHERE key = 'schema_digest'").Scan(&digest); err != nil {
			t.Fatalf("query schema_digest: %v", err)
		}

		var createSQL string
		if err := db.DB().QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'o_widget'").Scan(&createSQL); err != nil {
			t.Fatalf("query o_widget DDL: %v", err)
		}

		var noteW1, noteW2 sql.NullString
		if err := db.DB().QueryRow("SELECT f_note FROM o_widget WHERE object_id = ?", "w-1").Scan(&noteW1); err != nil {
			t.Fatalf("query o_widget.f_note for w-1: %v", err)
		}
		if err := db.DB().QueryRow("SELECT f_note FROM o_widget WHERE object_id = ?", "w-2").Scan(&noteW2); err != nil {
			t.Fatalf("query o_widget.f_note for w-2: %v", err)
		}

		return result{digest: digest, createSQL: createSQL, noteW1: noteW1, noteW2: noteW2}
	}

	gotOriginal := run(t, rulesOriginal)
	gotReordered := run(t, rulesReordered)

	if !strings.Contains(gotOriginal.createSQL, "f_note TEXT") {
		t.Fatalf("o_widget DDL (original rule order) = %q, want an untyped (TEXT) f_note column", gotOriginal.createSQL)
	}
	if !strings.Contains(gotReordered.createSQL, "f_note TEXT") {
		t.Fatalf("o_widget DDL (reordered rules) = %q, want an untyped (TEXT) f_note column", gotReordered.createSQL)
	}
	if gotOriginal.digest == "" || gotOriginal.digest != gotReordered.digest {
		t.Fatalf("schema digest differs between rule orderings: %q (original) vs %q (reordered)", gotOriginal.digest, gotReordered.digest)
	}

	for _, got := range []struct {
		label string
		r     result
	}{{"original", gotOriginal}, {"reordered", gotReordered}} {
		if !got.r.noteW1.Valid || got.r.noteW1.String != "alpha" {
			t.Fatalf("[%s] o_widget.f_note for w-1 (op_version 1, string) = %v (valid=%v), want \"alpha\" with no NULL", got.label, got.r.noteW1.String, got.r.noteW1.Valid)
		}
		if !got.r.noteW2.Valid || got.r.noteW2.String != "7" {
			t.Fatalf("[%s] o_widget.f_note for w-2 (op_version 2, int) = %v (valid=%v), want \"7\" with no NULL", got.label, got.r.noteW2.String, got.r.noteW2.Valid)
		}
	}
}

// TestAppendVersionBumpIsDeterministic is the append-strategy equivalent of
// TestScalarLWWVersionBumpIsDeterministic: WRIT-205's Linear description
// says round 4 reproduced the reps[tk] defect "on the append path and on the
// plain scalar lww path", so both need their own materialized-content
// determinism test, not just the scalar one.
//
// "note-v1" (op_version implicit 1 via makeWidgetEnv) declares value_type
// "string" and "note-v2" declares value_type "int" for the same append
// target "note" — the same carve-out as the scalar case, resolved the same
// way (WRIT-205's resolved[tk].ValueType, widening to untyped when bound
// rules disagree). Both ops land in the one target's own row-per-entry
// table, o_widget__note (WRIT-212), so this also exercises writeAppendRows's
// per-row value conversion against a resolved, not representative, column
// type.
func TestAppendVersionBumpIsDeterministic(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeWidgetOp("op-create-1", nil, "create", map[string]any{"title": "T"}, base)
	opNoteV1 := makeWidgetOp("op-note-v1-1", []string{"op-create-1"}, "note-v1", map[string]any{"note": "alpha"}, base.Add(1*time.Second))
	opNoteV2 := makeWidgetOp("op-note-v2-1", []string{"op-note-v1-1"}, "note-v2", map[string]any{"note": 7}, base.Add(2*time.Second))
	ops := []codec.Op{opCreate, opNoteV1, opNoteV2}

	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	noteV1Rule := state.Rule{OpType: "note-v1", Field: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	noteV2Rule := state.Rule{OpType: "note-v2", Field: "note", Strategy: "append", ValueType: "int", ObjectType: "widget"}

	rulesOriginal := map[string][]state.Rule{"widget": {titleRule, noteV1Rule, noteV2Rule}}
	rulesReordered := map[string][]state.Rule{"widget": {noteV2Rule, titleRule, noteV1Rule}}

	type result struct {
		digest    string
		createSQL string
		notes     []sql.NullString
	}

	run := func(t *testing.T, rules map[string][]state.Rule) result {
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

		var digest string
		if err := db.DB().QueryRow("SELECT value FROM meta WHERE key = 'schema_digest'").Scan(&digest); err != nil {
			t.Fatalf("query schema_digest: %v", err)
		}

		var createSQL string
		if err := db.DB().QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'o_widget__note'").Scan(&createSQL); err != nil {
			t.Fatalf("query o_widget__note DDL: %v", err)
		}

		rows, err := db.DB().Query("SELECT value FROM o_widget__note WHERE object_id = ? ORDER BY op_seq ASC, entry_idx ASC", "w-1")
		if err != nil {
			t.Fatalf("query o_widget__note: %v", err)
		}
		defer rows.Close()
		var notes []sql.NullString
		for rows.Next() {
			var n sql.NullString
			if err := rows.Scan(&n); err != nil {
				t.Fatalf("scan o_widget__note row: %v", err)
			}
			notes = append(notes, n)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("iterate o_widget__note: %v", err)
		}

		return result{digest: digest, createSQL: createSQL, notes: notes}
	}

	gotOriginal := run(t, rulesOriginal)
	gotReordered := run(t, rulesReordered)

	if !strings.Contains(gotOriginal.createSQL, "value TEXT") {
		t.Fatalf("o_widget__note DDL (original rule order) = %q, want an untyped (TEXT) value column", gotOriginal.createSQL)
	}
	if !strings.Contains(gotReordered.createSQL, "value TEXT") {
		t.Fatalf("o_widget__note DDL (reordered rules) = %q, want an untyped (TEXT) value column", gotReordered.createSQL)
	}
	if gotOriginal.digest == "" || gotOriginal.digest != gotReordered.digest {
		t.Fatalf("schema digest differs between rule orderings: %q (original) vs %q (reordered)", gotOriginal.digest, gotReordered.digest)
	}

	for _, got := range []struct {
		label string
		r     result
	}{{"original", gotOriginal}, {"reordered", gotReordered}} {
		if len(got.r.notes) != 2 {
			t.Fatalf("[%s] o_widget__note has %d rows, want 2 (one per envelope)", got.label, len(got.r.notes))
		}
		if !got.r.notes[0].Valid || got.r.notes[0].String != "alpha" {
			t.Fatalf("[%s] o_widget__note row 0 (note-v1, string) = %v (valid=%v), want \"alpha\" with no NULL", got.label, got.r.notes[0].String, got.r.notes[0].Valid)
		}
		if !got.r.notes[1].Valid || got.r.notes[1].String != "7" {
			t.Fatalf("[%s] o_widget__note row 1 (note-v2, int) = %v (valid=%v), want \"7\" with no NULL", got.label, got.r.notes[1].String, got.r.notes[1].Valid)
		}
	}
}

// TestKeyedLWWVersionBumpKeyDisagreementDeclines is the plan's third test:
// a keyed-lww target ("verdict") whose bound rules disagree on Key across a
// version bump — endorse op_version 1 keys it on (subject, revision),
// op_version 2 keys it on (subject, commit) — both within one
// versionBumpClass (same op_type and field, spec/fieldrules.go's
// FindTargetDisagreement).
//
// When this test was written, spec.CheckTargetAgreement's version-bump
// carve-out permitted this disagreement exactly as freely as a disagreeing
// value_type (spec/schema-ops.md §8), and the point being pinned was that
// buildTypeDescriptor still had to decline the target outright rather than
// widen it the way it widens value_type: a fixed set of "k_"-prefixed
// group-table columns is genuinely unrepresentable for two different key
// tuples (spec/fold.md §5 #8 keys each op on its own rule's key list), so
// v2's op would mis-key into v1's columns rather than merely mis-type them.
//
// WRIT-234 closed that carve-out: Key and KeyTypes are no longer on §8's
// "MAY freely change" list, so spec.CheckTargetAgreement now refuses this
// exact disagreement, and writ.RulesFromSchemas withholds the whole target
// before it ever reaches this package — a schema resolved out of the log
// cannot produce the rule set this test builds. This test still compiles
// and still pins a real behaviour, because it constructs its rules
// caller-side, handing them to projection.WithSchema directly rather than
// resolving them from a log-sourced schema: buildTypeDescriptor's Key-
// disagreement decline is kept for exactly that caller-supplied surface
// (see its own comment in ddl.go), the same surface WRIT-239 answered —
// writ.Fold now refuses, rather than panics on, a caller-supplied rule
// table binding one keyed-lww target to rules whose Key tuples disagree
// in length. This test does exercise fold's comparator: materialize.go
// hands state.Fold the unfiltered rule slice (WithheldTargets is only
// consulted afterward, for unknown_fields), so a keyedLWWAccumulator is
// constructed for the withheld "verdict" target and reaches Result()
// with both v1's and v2's entries. It survives that only because both
// verdict rules' key tuples are kept at the same arity (2) — a
// differing-arity pair now makes fold refuse the rule table outright
// (WRIT-239) rather than reach that comparator at all — so this test is
// deliberately shaped to exercise the fold path without tripping over
// it; do not widen either tuple without preserving that equality.
//
// "revision" is a second target sharing verdict's v1 key tuple
// (subject, revision) with no disagreement of its own, standing in for the
// bound the decline is supposed to respect: the type itself, and any target
// that merely shares a table with the withheld one, must keep materializing
// normally — declining one target must not cost a consumer every table and
// row for the type (buildTypeDescriptor's doc comment; the same bound
// WRIT-201's append-side decline already respects).
func TestKeyedLWWVersionBumpKeyDisagreementDeclines(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	opCreate := makeVersionedOp("w-1", "op-create-1", nil, "create", 1, map[string]any{"title": "T"}, base)
	opEndorseV1 := makeVersionedOp("w-1", "op-endorse-v1-1", []string{"op-create-1"}, "endorse", 1,
		map[string]any{"subject": "alice", "revision": "deadbeef", "verdict": "yes"}, base.Add(1*time.Second))
	opEndorseV2 := makeVersionedOp("w-1", "op-endorse-v2-1", []string{"op-endorse-v1-1"}, "endorse", 2,
		map[string]any{"subject": "alice", "commit": "cafebabe", "verdict": "no"}, base.Add(2*time.Second))

	titleRule := state.Rule{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	revisionRule := state.Rule{OpType: "endorse", OpVersion: 1, Field: "revision", Strategy: "keyed-lww", Key: []string{"subject", "revision"}, ValueType: "git-oid", ObjectType: "widget"}
	verdictV1Rule := state.Rule{OpType: "endorse", OpVersion: 1, Field: "verdict", Strategy: "keyed-lww", Key: []string{"subject", "revision"}, ValueType: "enum", Enum: []string{"yes", "no"}, ObjectType: "widget"}
	verdictV2Rule := state.Rule{OpType: "endorse", OpVersion: 2, Field: "verdict", Strategy: "keyed-lww", Key: []string{"subject", "commit"}, ValueType: "enum", Enum: []string{"yes", "no"}, ObjectType: "widget"}

	rules := map[string][]state.Rule{"widget": {titleRule, revisionRule, verdictV1Rule, verdictV2Rule}}

	_, store := createTestStore(t, "0123456789abcdef")
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	enumRes := &dag.EnumerateResult{
		Ops: map[string][]codec.Op{"w-1": {opCreate, opEndorseV1, opEndorseV2}},
		Cursors: dag.CursorSet{
			"refs/writ/0123456789abcdef/widget": "op-endorse-v2-1",
		},
		DecodedCommits: 3,
	}

	if _, err := db.Refresh(store, projection.WithSchema(rules), projection.WithEnumOverrideForTest(enumRes)); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	// The type itself materializes normally.
	var title sql.NullString
	if err := db.DB().QueryRow("SELECT f_title FROM o_widget WHERE object_id = ?", "w-1").Scan(&title); err != nil {
		t.Fatalf("query o_widget.f_title: %v", err)
	}
	if !title.Valid || title.String != "T" {
		t.Fatalf("o_widget.f_title = %v (valid=%v), want \"T\" — declining verdict must not cost the type its own row", title.String, title.Valid)
	}

	// verdict's own body field lands in unknown_fields (last write wins,
	// so op_version 2's "no" is what survives), not silently dropped.
	var unknownFields sql.NullString
	if err := db.DB().QueryRow("SELECT unknown_fields FROM o_widget WHERE object_id = ?", "w-1").Scan(&unknownFields); err != nil {
		t.Fatalf("query o_widget.unknown_fields: %v", err)
	}
	if !unknownFields.Valid {
		t.Fatalf("o_widget.unknown_fields is NULL, want it to carry the withheld \"verdict\" field")
	}
	var uf map[string]any
	if err := json.Unmarshal([]byte(unknownFields.String), &uf); err != nil {
		t.Fatalf("unmarshal unknown_fields %q: %v", unknownFields.String, err)
	}
	if v, ok := uf["verdict"]; !ok || v != "no" {
		t.Fatalf("unknown_fields[\"verdict\"] = %v (present=%v), want \"no\" (op_version 2's value, last write wins)", v, ok)
	}

	// The group-mate target ("revision", agreeing on the v1 key tuple)
	// keeps its column and its row: sharing a group table with a withheld
	// target costs it nothing.
	var kSubject, kRevision, fRevision sql.NullString
	row := db.DB().QueryRow("SELECT k_subject, k_revision, f_revision FROM o_widget__k_subject_revision WHERE object_id = ?", "w-1")
	if err := row.Scan(&kSubject, &kRevision, &fRevision); err != nil {
		t.Fatalf("query o_widget__k_subject_revision: %v", err)
	}
	if kSubject.String != "alice" || kRevision.String != "deadbeef" || fRevision.String != "deadbeef" {
		t.Fatalf("o_widget__k_subject_revision row = (subject=%q, revision=%q, f_revision=%q), want (alice, deadbeef, deadbeef)", kSubject.String, kRevision.String, fRevision.String)
	}

	// verdict itself got no column anywhere: not on the (subject,
	// revision) group table it would have shared with "revision" ...
	var groupSQL string
	if err := db.DB().QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'o_widget__k_subject_revision'").Scan(&groupSQL); err != nil {
		t.Fatalf("query o_widget__k_subject_revision DDL: %v", err)
	}
	if strings.Contains(groupSQL, "f_verdict") {
		t.Fatalf("o_widget__k_subject_revision DDL = %q, must not contain f_verdict — a withheld target must get no column", groupSQL)
	}

	// ... and no group table was generated for v2's own (subject, commit)
	// key tuple either: the whole target is withheld, not partially
	// represented under whichever version's key happened to survive.
	var soleKeyTables int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'o_widget__k_subject_commit'").Scan(&soleKeyTables); err != nil {
		t.Fatalf("query sqlite_master for o_widget__k_subject_commit: %v", err)
	}
	if soleKeyTables != 0 {
		t.Fatalf("o_widget__k_subject_commit table exists, want none — a withheld keyed-lww target must not get a group table under either disagreeing key")
	}
}

// TestColumnBudgetRefreshMaterializesWithheldTargetToUnknownFields is
// WRIT-256's Refresh-level reproduction: a schema declaring one type with
// 1999 lww string targets used to make buildTypeDescriptor emit a type
// table with 2001 columns, one past modernc sqlite's compiled
// SQLITE_MAX_COLUMN. That table's CREATE failed outright ("too many
// columns"), which meant writ.Open failed on a cold cache and Refresh
// failed forever after on a warm one — for every type, not just this one,
// since ApplySchema/Refresh builds every type's DDL in one pass. The fix
// declines the one target that overflows the budget instead, so Refresh
// must now succeed, the surviving 1998 targets keep their columns, the
// declined target's body field lands in unknown_fields rather than being
// dropped, and — the ddl.go WithheldTargets comment's own bound — an
// unrelated second type in the same schema is unaffected.
func TestColumnBudgetRefreshMaterializesWithheldTargetToUnknownFields(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()

	const n = 1999
	widgetRules := make([]state.Rule, n)
	for i := 0; i < n; i++ {
		field := fmt.Sprintf("f%04d", i)
		widgetRules[i] = state.Rule{OpType: "create", OpVersion: 1, Field: field, Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	}
	gadgetRule := state.Rule{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "gadget"}
	schemaRules := map[string][]state.Rule{"widget": widgetRules, "gadget": {gadgetRule}}

	// The op writes the first field ("f0000", which survives the budget)
	// and the last ("f1998", sorted last among 1999 targets and so the one
	// the first-fit budget declines) in a single op, exercising both
	// paths from one write.
	opCreateWidget := makeVersionedOp("w-1", "op-create-widget-1", nil, "create", 1,
		map[string]any{"f0000": "first", "f1998": "last"}, base)

	// makeVersionedOp hardcodes ObjectType "widget", so the unrelated
	// second type's op is built by hand here instead.
	gadgetBody, _ := json.Marshal(map[string]any{"title": "G"})
	gadgetEnv := codec.Envelope{ObjectID: "g-1", ObjectType: "gadget", OpType: "create", OpVersion: 1, Body: gadgetBody}
	gadgetRaw, _ := codec.EncodePayload(gadgetEnv)
	gadgetEnv.Raw = gadgetRaw
	gadgetAuthor := codec.Identity{Name: "Test Writer", Email: "writer@example.com", When: base}
	opCreateGadget := codec.Op{
		Envelope: gadgetEnv, ID: "op-create-gadget-1",
		Author: gadgetAuthor, Committer: gadgetAuthor,
		Message: "writ: create gadget/g-1\n",
	}

	runRefresh := func(t *testing.T) (*projection.DB, string) {
		t.Helper()
		_, store := createTestStore(t, "0123456789abcdef")
		db, err := projection.Open(":memory:")
		if err != nil {
			t.Fatalf("Open(:memory:) failed: %v", err)
		}
		enumRes := &dag.EnumerateResult{
			Ops: map[string][]codec.Op{
				"w-1": {opCreateWidget},
				"g-1": {opCreateGadget},
			},
			Cursors: dag.CursorSet{
				"refs/writ/0123456789abcdef/widget": "op-create-widget-1",
				"refs/writ/0123456789abcdef/gadget": "op-create-gadget-1",
			},
			DecodedCommits: 2,
		}
		if _, err := db.Refresh(store, projection.WithSchema(schemaRules), projection.WithEnumOverrideForTest(enumRes)); err != nil {
			t.Fatalf("Refresh failed (want it to succeed even though widget declares %d targets on one type): %v", n, err)
		}
		var ddl string
		if err := db.DB().QueryRow("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'o_widget'").Scan(&ddl); err != nil {
			t.Fatalf("query o_widget DDL: %v", err)
		}
		return db, ddl
	}

	db1, ddl1 := runRefresh(t)
	defer db1.Close()

	var f0000 sql.NullString
	if err := db1.DB().QueryRow("SELECT f_f0000 FROM o_widget WHERE object_id = ?", "w-1").Scan(&f0000); err != nil {
		t.Fatalf("query o_widget.f_f0000: %v", err)
	}
	if !f0000.Valid || f0000.String != "first" {
		t.Fatalf("o_widget.f_f0000 = %v (valid=%v), want \"first\"", f0000.String, f0000.Valid)
	}

	var unknownFields sql.NullString
	if err := db1.DB().QueryRow("SELECT unknown_fields FROM o_widget WHERE object_id = ?", "w-1").Scan(&unknownFields); err != nil {
		t.Fatalf("query o_widget.unknown_fields: %v", err)
	}
	if !unknownFields.Valid {
		t.Fatalf("o_widget.unknown_fields is NULL, want it to carry the withheld \"f1998\" field")
	}
	var uf map[string]any
	if err := json.Unmarshal([]byte(unknownFields.String), &uf); err != nil {
		t.Fatalf("unmarshal unknown_fields %q: %v", unknownFields.String, err)
	}
	if v, ok := uf["f1998"]; !ok || v != "last" {
		t.Fatalf("unknown_fields[\"f1998\"] = %v (present=%v), want \"last\" — the declined target's field must land here, not be dropped", v, ok)
	}

	var unknownOpsCount int
	if err := db1.DB().QueryRow("SELECT COUNT(*) FROM unknown_ops WHERE object_id = ?", "w-1").Scan(&unknownOpsCount); err != nil {
		t.Fatalf("query unknown_ops: %v", err)
	}
	if unknownOpsCount != 0 {
		t.Fatalf("unknown_ops has %d row(s) for w-1, want 0 — a rule matched the op, so it must not be quarantined", unknownOpsCount)
	}

	var gadgetTitle sql.NullString
	if err := db1.DB().QueryRow("SELECT f_title FROM o_gadget WHERE object_id = ?", "g-1").Scan(&gadgetTitle); err != nil {
		t.Fatalf("query o_gadget.f_title: %v", err)
	}
	if !gadgetTitle.Valid || gadgetTitle.String != "G" {
		t.Fatalf("o_gadget.f_title = %v (valid=%v), want \"G\" — an unrelated type in the same schema must materialize normally", gadgetTitle.String, gadgetTitle.Valid)
	}

	db2, ddl2 := runRefresh(t)
	defer db2.Close()
	if ddl1 != ddl2 {
		t.Fatalf("o_widget DDL differs between two independent Refreshes on fresh :memory: databases (drop-and-rebuild determinism):\n--- first ---\n%s\n--- second ---\n%s", ddl1, ddl2)
	}
}
