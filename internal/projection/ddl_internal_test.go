package projection

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/writtendev/writ/internal/state"
	"github.com/writtendev/writ/spec"
)

// loadTestRules is a schema-shaped rule index covering every table shape
// this file's tests care about: an OR-set pair per target, two keyed-lww key
// groups (one of them retargeting a field name the other also uses), and an
// append target pair, each with its own row-per-entry table. Writ hard-codes no object type but `schema`,
// so there is no built-in vocabulary to derive one from — a consumer
// declares these in the log, and the projection generates tables from
// whatever it finds there.
//
// An internal, white-box test file in package projection cannot reach a
// _test.go symbol in the external projection_test package, so this stays a
// separate copy from that package's own testRules().
func loadTestRules(t *testing.T) map[string][]state.Rule {
	t.Helper()
	return map[string][]state.Rule{
		"widget": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"},
			{OpType: "assign", OpVersion: 1, Field: "add", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref", ObjectType: "widget"},
			{OpType: "assign", OpVersion: 1, Field: "remove", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref", ObjectType: "widget"},
			{OpType: "tag", OpVersion: 1, Field: "add", Target: "tags", Strategy: "set-observed-remove", ValueType: "string", ObjectType: "widget"},
			{OpType: "tag", OpVersion: 1, Field: "remove", Target: "tags", Strategy: "set-observed-remove", ValueType: "string", ObjectType: "widget"},
			{OpType: "revision", OpVersion: 1, Field: "base", Strategy: "append", ValueType: "git-oid", ObjectType: "widget"},
			{OpType: "revision", OpVersion: 1, Field: "head", Strategy: "append", ValueType: "git-oid", ObjectType: "widget"},
			{OpType: "endorse", OpVersion: 1, Field: "revision", Strategy: "keyed-lww", Key: []string{"subject", "revision"}, KeyTypes: map[string]string{"subject": "person-ref", "revision": "git-oid"}, ValueType: "git-oid", ObjectType: "widget"},
			{OpType: "check", OpVersion: 1, Field: "revision", Target: "check_revision", Strategy: "keyed-lww", Key: []string{"revision", "name"}, KeyTypes: map[string]string{"revision": "git-oid", "name": "string"}, ValueType: "git-oid", ObjectType: "widget"},
			{OpType: "check", OpVersion: 1, Field: "state", Strategy: "keyed-lww", Key: []string{"revision", "name"}, KeyTypes: map[string]string{"revision": "git-oid", "name": "string"}, ValueType: "string", ObjectType: "widget"},
		},
		"gadget": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "gadget"},
			{OpType: "assign", OpVersion: 1, Field: "add", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref", ObjectType: "gadget"},
			{OpType: "assign", OpVersion: 1, Field: "remove", Target: "assignees", Strategy: "set-observed-remove", ValueType: "person-ref", ObjectType: "gadget"},
			{OpType: "tag", OpVersion: 1, Field: "add", Target: "tags", Strategy: "set-observed-remove", ValueType: "string", ObjectType: "gadget"},
			{OpType: "tag", OpVersion: 1, Field: "remove", Target: "tags", Strategy: "set-observed-remove", ValueType: "string", ObjectType: "gadget"},
		},
	}
}

// TestGeneratedDDLIsDeterministic is the ticket's own executable statement
// of its premise: the same rule index, and a shuffled copy of it, must
// produce byte-identical DDL and digest. Two implementations generating
// different DDL from one schema would be the same class of bug as a
// non-deterministic fold.
func TestGeneratedDDLIsDeterministic(t *testing.T) {
	rules := loadTestRules(t)

	d1, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}

	shuffled := make(map[string][]state.Rule, len(rules))
	for objectType, rs := range rules {
		cp := append([]state.Rule(nil), rs...)
		rand.Shuffle(len(cp), func(i, j int) { cp[i], cp[j] = cp[j], cp[i] })
		shuffled[objectType] = cp
	}

	d2, err := buildDescriptor(shuffled)
	if err != nil {
		t.Fatalf("buildDescriptor (shuffled): %v", err)
	}

	if d1.createSQL() != d2.createSQL() {
		t.Fatalf("DDL differs between declaration order and shuffled order:\n--- original ---\n%s\n--- shuffled ---\n%s", d1.createSQL(), d2.createSQL())
	}
	if d1.digest != d2.digest {
		t.Fatalf("digest differs between declaration order (%s) and shuffled order (%s)", d1.digest, d2.digest)
	}
	if string(d1.canonicalJSON) != string(d2.canonicalJSON) {
		t.Fatalf("canonical JSON differs between declaration order and shuffled order")
	}
	if d1.digest == "" {
		t.Fatalf("expected a non-empty digest")
	}
}

// TestOrSetPairCollapsesToOneTable pins the WRIT-198 shape this ticket's
// generator depends on: widget and gadget each declare assign/add +
// assign/remove and tag/add + tag/remove as one target key apiece, so each
// gets exactly one assignees and one tags child table — not two identical
// ones — and the check key group carries the retargeted f_check_revision
// column, not a colliding f_revision shared with endorse's.
func TestOrSetPairCollapsesToOneTable(t *testing.T) {
	rules := loadTestRules(t)
	desc, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}

	for _, objectType := range []string{"widget", "gadget"} {
		td, ok := desc.types[objectType]
		if !ok {
			t.Fatalf("expected type %q to have tables", objectType)
		}
		var assigneeTables, tagTables []string
		for _, c := range td.Children {
			if strings.HasSuffix(c.Name, "__assignees") {
				assigneeTables = append(assigneeTables, c.Name)
			}
			if strings.HasSuffix(c.Name, "__tags") {
				tagTables = append(tagTables, c.Name)
			}
		}
		if len(assigneeTables) != 1 {
			t.Errorf("%s: expected exactly 1 assignees child table, got %v", objectType, assigneeTables)
		}
		if len(tagTables) != 1 {
			t.Errorf("%s: expected exactly 1 tags child table, got %v", objectType, tagTables)
		}
	}

	widgetTD := desc.types["widget"]
	var checkGroup *ddlTable
	for i := range widgetTD.Children {
		if widgetTD.Children[i].Name == "o_widget__k_revision_name" {
			checkGroup = &widgetTD.Children[i]
		}
	}
	if checkGroup == nil {
		t.Fatalf("expected o_widget__k_revision_name (check key group) among widget's children: %+v", widgetTD.Children)
	}
	hasCheckRevision, hasPlainRevision := false, false
	for _, c := range checkGroup.Columns {
		if c.Name == "f_check_revision" {
			hasCheckRevision = true
		}
		if c.Name == "f_revision" {
			hasPlainRevision = true
		}
	}
	if !hasCheckRevision {
		t.Errorf("expected check key group to carry retargeted column f_check_revision, got columns %+v", checkGroup.Columns)
	}
	if hasPlainRevision {
		t.Errorf("check key group must not carry f_revision (that belongs to the endorse key group, o_widget__k_subject_revision)")
	}

	var endorseGroup *ddlTable
	for i := range widgetTD.Children {
		if widgetTD.Children[i].Name == "o_widget__k_subject_revision" {
			endorseGroup = &widgetTD.Children[i]
		}
	}
	if endorseGroup == nil {
		t.Fatalf("expected o_widget__k_subject_revision (endorse key group) among widget's children: %+v", widgetTD.Children)
	}
	foundPlainRevision := false
	for _, c := range endorseGroup.Columns {
		if c.Name == "f_revision" {
			foundPlainRevision = true
		}
	}
	if !foundPlainRevision {
		t.Errorf("expected endorse key group to carry f_revision, got columns %+v", endorseGroup.Columns)
	}
}

// TestInvalidTargetWithholdsTables covers the generator's grammar gate:
// target and keyed-lww key components have no grammar in the wire schema
// (op-envelope.schema.json constrains only field), so an arbitrary
// log-declared string could otherwise become an arbitrary SQL identifier.
// A type with a failing target or key component gets no tables at all,
// rather than an ambiguous or unsafe identifier — its objects fall to
// unknown_ops, the same no-winner idiom RulesFromSchemas already uses for a
// contested object_type.
func TestInvalidTargetWithholdsTables(t *testing.T) {
	tests := []struct {
		name  string
		rules map[string][]state.Rule
	}{
		{
			name: "invalid target identifier",
			rules: map[string][]state.Rule{
				"widget": {
					{OpType: "create", Field: "Title", Strategy: "lww", ValueType: "string", ObjectType: "widget"},
				},
			},
		},
		{
			name: "invalid keyed-lww key component",
			rules: map[string][]state.Rule{
				"gadget": {
					{OpType: "link", Field: "target", Strategy: "keyed-lww", Key: []string{"Target"}, ValueType: "object-ref", KeyTypes: map[string]string{"Target": "object-ref"}, ObjectType: "gadget"},
				},
			},
		},
		{
			name: "target too long",
			rules: map[string][]state.Rule{
				"sprocket": {
					{OpType: "create", Field: strings.Repeat("a", 65), Strategy: "lww", ValueType: "string", ObjectType: "sprocket"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc, err := buildDescriptor(tt.rules)
			if err != nil {
				t.Fatalf("buildDescriptor: %v", err)
			}
			for objectType := range tt.rules {
				if _, ok := desc.types[objectType]; ok {
					t.Fatalf("expected object type %q to be withheld (no tables), but it has tables", objectType)
				}
			}
			if len(desc.tables) != 0 {
				t.Fatalf("expected no generated tables, got %+v", desc.tables)
			}
		})
	}
}

// TestIntraTypeCollisionWithholdsTables is WRIT-189 round 2's MAJOR-2
// finding: round 1 only closed the cross-type collision (two different
// object types generating the same table name — TestCollidingIdentifierWithholdsTables
// above). Three more shapes collide entirely within one type's own
// declarations, and children (the table-name -> ddlTable map
// buildTypeDescriptor accumulates its child tables in) used to silently
// dedupe them before identCollision ever ran: whichever declaration wrote
// last decided the table's shape, and every earlier target's plan kept
// pointing at columns that table no longer had, hard-erroring at
// materialize ("no such column") and failing every Refresh from then on,
// forever, from one legal schema object. Each case below produces two
// declarations that generate the identical table name from a different
// construction:
//
//   - a set-union target literally named "k_target" vs a keyed-lww group
//     keyed on ["target"] (both generate "..._k_target")
//   - two keyed-lww groups whose Key arities differ but join to the same
//     string — ["a_b"] vs ["a", "b"] (both generate "..._k_a_b")
//   - an untyped lww target's generic members table vs a set-union target
//     literally named "<target>__members" (both generate
//     "..._<target>__members")
//
// insertChild (ddl.go) now refuses the second write to a name the first
// already claimed, withholding the whole type exactly like the cross-type
// case and every other withholding reason in this file — never a corrupted
// partial table.
func TestIntraTypeCollisionWithholdsTables(t *testing.T) {
	tests := []struct {
		name  string
		rules map[string][]state.Rule
	}{
		{
			name: "set-union target vs keyed-lww key",
			rules: map[string][]state.Rule{
				"widget": {
					{OpType: "tag", Field: "k_target", Strategy: "set-union", ValueType: "string", ObjectType: "widget"},
					{OpType: "link", Field: "relation", Strategy: "keyed-lww", Key: []string{"target"}, ValueType: "string", KeyTypes: map[string]string{"target": "object-ref"}, ObjectType: "widget"},
				},
			},
		},
		{
			name: "keyed-lww key arities joining to the same string",
			rules: map[string][]state.Rule{
				"gadget": {
					{OpType: "x1", Field: "v1", Strategy: "keyed-lww", Key: []string{"a_b"}, ValueType: "string", KeyTypes: map[string]string{"a_b": "string"}, ObjectType: "gadget"},
					{OpType: "x2", Field: "v2", Strategy: "keyed-lww", Key: []string{"a", "b"}, ValueType: "string", KeyTypes: map[string]string{"a": "string", "b": "string"}, ObjectType: "gadget"},
				},
			},
		},
		{
			name: "untyped members table vs set-union target",
			rules: map[string][]state.Rule{
				"sprocket": {
					{OpType: "create", Field: "subject", Strategy: "lww", ValueType: "", ObjectType: "sprocket"},
					{OpType: "tag", Field: "subject__members", Strategy: "set-union", ValueType: "string", ObjectType: "sprocket"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			desc, err := buildDescriptor(tt.rules)
			if err != nil {
				t.Fatalf("buildDescriptor: %v", err)
			}
			for objectType := range tt.rules {
				if _, ok := desc.types[objectType]; ok {
					t.Fatalf("expected object type %q to be withheld (no tables), but it has tables", objectType)
				}
			}
			if len(desc.tables) != 0 {
				t.Fatalf("expected no generated tables, got %+v", desc.tables)
			}
		})
	}
}

// TestAmbiguousAppendFieldMaterializesBothEntries covers two append-strategy
// rules binding the same target key ("note") to two different Fields under
// one exact (op_type, op_version) envelope — the WRIT-201 shape: spec/fold.md
// §5 rules that every matching rule applies, in canonical rule order, so one
// op writing both fields appends both entries to one list.
//
// Before WRIT-212, the shared one-row-per-op/one-column-per-target append
// table could not hold two entries for one target from one op, so "note"
// was withheld into WithheldTargets and only the group-mate "tags" (reached
// by one field under the same envelope) kept a table. Row-per-entry removes
// the table shape that made this unrepresentable: "note" now gets its own
// AppendTable, fed by both rules in canonical rule order (ascending field,
// since op_type and op_version tie), so it is materialized rather than
// withheld — nothing lands in WithheldTargets here any more, and "tags"
// keeps its own separate table exactly as it always did (targets no longer
// share tables at all, so there is no group to protect it from). The actual
// materialized row content and order is pinned at the DB level by
// TestAppendAmbiguousFieldBothEntriesMaterialize (append_entries_test.go).
func TestAmbiguousAppendFieldMaterializesBothEntries(t *testing.T) {
	titleRule := state.Rule{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget"}
	noteFieldA := state.Rule{OpType: "note", OpVersion: 1, Field: "a", Target: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	noteFieldB := state.Rule{OpType: "note", OpVersion: 1, Field: "b", Target: "note", Strategy: "append", ValueType: "string", ObjectType: "widget"}
	tagRule := state.Rule{OpType: "note", OpVersion: 1, Field: "tag", Target: "tags", Strategy: "append", ValueType: "string", ObjectType: "widget"}

	orderings := map[string][]state.Rule{
		"a-then-b": {titleRule, noteFieldA, noteFieldB, tagRule},
		"b-then-a": {titleRule, tagRule, noteFieldB, noteFieldA},
	}

	for name, rules := range orderings {
		t.Run(name, func(t *testing.T) {
			desc, err := buildDescriptor(map[string][]state.Rule{"widget": rules})
			if err != nil {
				t.Fatalf("buildDescriptor: %v", err)
			}
			td, ok := desc.types["widget"]
			if !ok {
				t.Fatalf("object type \"widget\" was withheld entirely")
			}
			if len(td.WithheldTargets) != 0 {
				t.Fatalf("WithheldTargets = %v, want none — both \"note\" and \"tags\" are representable under row-per-entry", td.WithheldTargets)
			}

			notePlan, ok := td.Targets["note"]
			if !ok || notePlan.AppendTable == "" {
				t.Fatalf("targets[\"note\"] = %+v, want an AppendTable of its own", notePlan)
			}
			if notePlan.AppendTable != "o_widget__note" {
				t.Fatalf("note's AppendTable = %q, want \"o_widget__note\"", notePlan.AppendTable)
			}
			if len(notePlan.AppendRules) != 2 || notePlan.AppendRules[0].Field != "a" || notePlan.AppendRules[1].Field != "b" {
				t.Fatalf("note's AppendRules = %+v, want [a, b] in canonical (field-ascending) order", notePlan.AppendRules)
			}

			tagsPlan, ok := td.Targets["tags"]
			if !ok || tagsPlan.AppendTable != "o_widget__tags" {
				t.Fatalf("targets[\"tags\"] = %+v, want AppendTable \"o_widget__tags\"", tagsPlan)
			}

			var names []string
			for _, tbl := range desc.tables {
				names = append(names, tbl.Name)
			}
			sort.Strings(names)
			want := []string{"o_widget", "o_widget__note", "o_widget__tags"}
			if !reflect.DeepEqual(names, want) {
				t.Fatalf("generated tables = %v, want %v", names, want)
			}
			var hasTitle bool
			for _, c := range td.Table.Columns {
				if c.Name == "f_title" {
					hasTitle = true
				}
			}
			if !hasTitle {
				t.Fatalf("o_widget columns = %+v, want the unrelated f_title target still materialized", td.Table.Columns)
			}
		})
	}
}

// TestCollidingIdentifierWithholdsTables is WRIT-189 round 1's MAJOR-3
// finding: a log-declared object type is free to pick any name legal under
// op-envelope's grammar (^[a-z][a-z0-9-]*$), and "widget--base" generates
// the exact same table name ("o_widget__base") as widget's own "base"
// append target (WRIT-212: every append target gets a row-per-entry table
// of its own, tableName + "__" + target, the same construction a
// collection target already uses — "o_widget__base_head" was the shared
// table two append targets used to fold into before this, which is what
// this collision test used to target). buildDescriptor used to return a
// hard error for this — which bricked buildDescriptor's caller chain
// (ApplySchema -> writ.Open) permanently, since nothing can be removed from
// the log to fix it. Data someone else wrote must never brick the
// repository (the same ruling WRIT-188 round 3 established): the colliding
// type is withheld exactly like an invalid target above, and every other
// type's tables — including the one it collided with — are unaffected.
func TestCollidingIdentifierWithholdsTables(t *testing.T) {
	rules := loadTestRules(t)
	rules["widget--base"] = []state.Rule{
		{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget--base"},
	}

	desc, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}

	if _, ok := desc.types["widget--base"]; ok {
		t.Fatalf("expected colliding type %q to be withheld (no tables), but it has tables", "widget--base")
	}
	widgetTD, ok := desc.types["widget"]
	if !ok {
		t.Fatalf("expected declared type %q to keep its tables", "widget")
	}
	foundBase := false
	for _, ct := range widgetTD.Children {
		if ct.Name == "o_widget__base" {
			foundBase = true
		}
	}
	if !foundBase {
		t.Fatalf("expected widget to still own o_widget__base, got children %+v", widgetTD.Children)
	}

	seen := make(map[string]bool)
	for _, tbl := range desc.tables {
		if seen[tbl.Name] {
			t.Fatalf("duplicate table %q in descriptor", tbl.Name)
		}
		seen[tbl.Name] = true
	}
}

// TestIdentPatternMatchesWireGrammar ties identPattern to the grammar
// spec/schemas/schema-ops.schema.json now pins for target and keyed-lww key
// components (WRIT-203) so the two copies cannot drift silently. identPattern
// is deliberately its own copy rather than an exported helper from spec (no
// new public surface for one small regexp) — this is the "cheap test"
// substitute the ticket asks for instead.
func TestIdentPatternMatchesWireGrammar(t *testing.T) {
	raw, err := spec.FS.ReadFile("schemas/schema-ops.schema.json")
	if err != nil {
		t.Fatalf("reading schema-ops.schema.json: %v", err)
	}
	var doc struct {
		Defs map[string]struct {
			Pattern   string `json:"pattern"`
			MaxLength int    `json:"maxLength"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding schema-ops.schema.json: %v", err)
	}
	targetName, ok := doc.Defs["target_name"]
	if !ok {
		t.Fatal("schema-ops.schema.json has no $defs/target_name")
	}
	if targetName.Pattern != identPattern.String() {
		t.Errorf("identPattern %q no longer matches schema-ops.schema.json's target_name pattern %q", identPattern.String(), targetName.Pattern)
	}
	if targetName.MaxLength != identMaxLength {
		t.Errorf("identMaxLength %d no longer matches schema-ops.schema.json's target_name maxLength %d", identMaxLength, targetName.MaxLength)
	}
	keyColumnName, ok := doc.Defs["key_column_name"]
	if !ok {
		t.Fatal("schema-ops.schema.json has no $defs/key_column_name")
	}
	if keyColumnName.Pattern != identPattern.String() {
		t.Errorf("identPattern %q no longer matches schema-ops.schema.json's key_column_name pattern %q", identPattern.String(), keyColumnName.Pattern)
	}
	if keyColumnName.MaxLength != identMaxLength {
		t.Errorf("identMaxLength %d no longer matches schema-ops.schema.json's key_column_name maxLength %d", identMaxLength, keyColumnName.MaxLength)
	}
}

// makeLWWFieldRules builds n distinct lww string-valued targets on
// objectType, zero-padded ("f0000", "f0001", ...) so their sorted order —
// the order buildTypeDescriptor's column budget walks them in — is obvious
// from the name alone. Shared by the maxTableColumns tests below (WRIT-256).
func makeLWWFieldRules(objectType string, n int) []state.Rule {
	rules := make([]state.Rule, n)
	for i := range rules {
		field := fmt.Sprintf("f%04d", i)
		rules[i] = state.Rule{OpType: "create", Field: field, Strategy: "lww", ValueType: "string", ObjectType: objectType}
	}
	return rules
}

// TestColumnBudgetFitsUnderLimit is the boundary just below maxTableColumns:
// 1998 scalar targets, plus the type table's two fixed columns (object_id,
// unknown_fields), land exactly on 2000 — the limit itself, not one under
// it — so every target must still get its column and none may be withheld.
func TestColumnBudgetFitsUnderLimit(t *testing.T) {
	rules := map[string][]state.Rule{"widget": makeLWWFieldRules("widget", 1998)}
	desc, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}
	td, ok := desc.types["widget"]
	if !ok {
		t.Fatalf("expected widget to have tables")
	}
	if got := len(td.Table.Columns); got != maxTableColumns {
		t.Fatalf("o_widget has %d columns, want exactly %d", got, maxTableColumns)
	}
	if len(td.WithheldTargets) != 0 {
		t.Fatalf("WithheldTargets = %v, want none — 1998 targets plus object_id and unknown_fields is exactly %d", td.WithheldTargets, maxTableColumns)
	}
}

// TestColumnBudgetWithholdsOverflowTarget is the ticket's own reproduction:
// one target more (1999) pushes the type table to 2001 columns, which
// SQLite's compiled limit refuses outright ("too many columns"), bricking
// writ.Open and every Refresh for the whole repository before this fix
// (WRIT-256). The generator must instead decline the one target that
// doesn't fit — sorted last, "f1998" — and keep the other 1998.
func TestColumnBudgetWithholdsOverflowTarget(t *testing.T) {
	rules := map[string][]state.Rule{"widget": makeLWWFieldRules("widget", 1999)}
	desc, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}
	td, ok := desc.types["widget"]
	if !ok {
		t.Fatalf("expected widget to have tables — the type itself must materialize even though one target doesn't")
	}
	if got := len(td.Table.Columns); got != maxTableColumns {
		t.Fatalf("o_widget has %d columns, want exactly %d", got, maxTableColumns)
	}
	want := map[string]bool{"f1998": true}
	if !reflect.DeepEqual(td.WithheldTargets, want) {
		t.Fatalf("WithheldTargets = %v, want %v", td.WithheldTargets, want)
	}
	if _, ok := td.Targets["f1998"]; ok {
		t.Fatalf("targets[\"f1998\"] present, want none — a withheld target must get no targetPlan")
	}
	for i := 0; i < 1998; i++ {
		field := fmt.Sprintf("f%04d", i)
		if _, ok := td.Targets[field]; !ok {
			t.Fatalf("targets[%q] missing, want it to still materialize", field)
		}
	}
}

// TestColumnBudgetPositionBoundaryFirstFit pins first-fit, not
// stop-at-first-overflow: a position-valued target costs two columns
// (f_<target> and f_<target>__op_id), so it can be the one thing that
// doesn't fit even when a later, cheaper target would. 1997 filler targets
// leave exactly one column of budget; "g_pos" (position, costs 2) doesn't
// fit and is withheld, but "h_last" (costs 1), sorted after it, still takes
// the remaining slot.
func TestColumnBudgetPositionBoundaryFirstFit(t *testing.T) {
	rules := makeLWWFieldRules("widget", 1997) // f0000..f1996, one column each
	rules = append(rules,
		state.Rule{OpType: "move", Field: "g_pos", Strategy: "lww", ValueType: "position", ObjectType: "widget"},
		state.Rule{OpType: "create", Field: "h_last", Strategy: "lww", ValueType: "string", ObjectType: "widget"},
	)

	desc, err := buildDescriptor(map[string][]state.Rule{"widget": rules})
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}
	td, ok := desc.types["widget"]
	if !ok {
		t.Fatalf("expected widget to have tables")
	}
	if got := len(td.Table.Columns); got != maxTableColumns {
		t.Fatalf("o_widget has %d columns, want exactly %d", got, maxTableColumns)
	}
	want := map[string]bool{"g_pos": true}
	if !reflect.DeepEqual(td.WithheldTargets, want) {
		t.Fatalf("WithheldTargets = %v, want %v — g_pos costs 2 columns and only 1 remained, h_last costs 1 and still fits after it", td.WithheldTargets, want)
	}
	if _, ok := td.Targets["h_last"]; !ok {
		t.Fatalf("targets[\"h_last\"] missing, want first-fit to still take the slot g_pos couldn't")
	}
	if plan, ok := td.Targets["g_pos"]; ok {
		t.Fatalf("targets[\"g_pos\"] = %+v, want none — a withheld target must get no targetPlan", plan)
	}
}

// TestColumnBudgetDeclinedUntypedTargetGetsNoMembersTable covers the plan's
// ordering requirement directly: the budget decision must happen before any
// column, targetPlan, members child table, or anchor ref is created for the
// declined target. An untyped (ValueType "") lww target normally gets a
// generic "__members" child table (buildTypeDescriptor's lww/create-once
// case) — if withholding happened after that table was inserted, the type
// would end up with an orphaned child table pointing at a target with no
// column and no plan.
func TestColumnBudgetDeclinedUntypedTargetGetsNoMembersTable(t *testing.T) {
	rules := makeLWWFieldRules("widget", 1998) // fills the budget exactly
	rules = append(rules, state.Rule{OpType: "create", Field: "z_extra", Strategy: "lww", ValueType: "", ObjectType: "widget"})

	desc, err := buildDescriptor(map[string][]state.Rule{"widget": rules})
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}
	td, ok := desc.types["widget"]
	if !ok {
		t.Fatalf("expected widget to have tables")
	}
	if !td.WithheldTargets["z_extra"] {
		t.Fatalf("WithheldTargets = %v, want it to contain \"z_extra\"", td.WithheldTargets)
	}
	if _, ok := td.Targets["z_extra"]; ok {
		t.Fatalf("targets[\"z_extra\"] present, want none")
	}
	wantMembers := "o_widget__z_extra__members"
	for _, c := range td.Children {
		if c.Name == wantMembers {
			t.Fatalf("children contains %q, want no members table for a withheld target", wantMembers)
		}
	}
}

// TestColumnBudgetWithholdsOverflowGroupMembers covers the keyed-lww group
// table's own, separate column budget: object_id plus one column per key
// component are fixed, leaving the rest for member columns. 1999 members
// sharing one single-component key overflow a group table by exactly one —
// the first 1998 (sorted) keep their columns, the last is withheld, and the
// group table itself still forms with ≤2000 columns for the members that
// survive.
func TestColumnBudgetWithholdsOverflowGroupMembers(t *testing.T) {
	n := 1999
	rules := make([]state.Rule, n)
	for i := range rules {
		field := fmt.Sprintf("m%04d", i)
		rules[i] = state.Rule{
			OpType: "check", Field: field, Strategy: "keyed-lww",
			Key: []string{"k"}, KeyTypes: map[string]string{"k": "string"},
			ValueType: "string", ObjectType: "widget",
		}
	}

	desc, err := buildDescriptor(map[string][]state.Rule{"widget": rules})
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}
	td, ok := desc.types["widget"]
	if !ok {
		t.Fatalf("expected widget to have tables")
	}

	var group *ddlTable
	for i := range td.Children {
		if td.Children[i].Name == "o_widget__k_k" {
			group = &td.Children[i]
		}
	}
	if group == nil {
		t.Fatalf("expected o_widget__k_k (the key group table) among widget's children: %+v", td.Children)
	}
	if got := len(group.Columns); got != maxTableColumns {
		t.Fatalf("o_widget__k_k has %d columns, want exactly %d (object_id + k_k + 1998 surviving members)", got, maxTableColumns)
	}

	want := map[string]bool{"m1998": true}
	if !reflect.DeepEqual(td.WithheldTargets, want) {
		t.Fatalf("WithheldTargets = %v, want %v", td.WithheldTargets, want)
	}
	if _, ok := td.Targets["m1998"]; ok {
		t.Fatalf("targets[\"m1998\"] present, want none — the overflow member must get no targetPlan")
	}
	for i := 0; i < 1998; i++ {
		field := fmt.Sprintf("m%04d", i)
		plan, ok := td.Targets[field]
		if !ok || plan.GroupTable != "o_widget__k_k" {
			t.Fatalf("targets[%q] = %+v, want a surviving member of o_widget__k_k", field, plan)
		}
	}
}

// TestColumnBudgetIsDeterministic mirrors TestGeneratedDDLIsDeterministic
// for the overflow case specifically: the same 1999-target rule set,
// shuffled, must decline the same target ("f1998", sorted last, not
// whichever one happened to be presented last) and produce byte-identical
// DDL and digest either way.
func TestColumnBudgetIsDeterministic(t *testing.T) {
	rules := map[string][]state.Rule{"widget": makeLWWFieldRules("widget", 1999)}

	d1, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}

	shuffled := make(map[string][]state.Rule, len(rules))
	for objectType, rs := range rules {
		cp := append([]state.Rule(nil), rs...)
		rand.Shuffle(len(cp), func(i, j int) { cp[i], cp[j] = cp[j], cp[i] })
		shuffled[objectType] = cp
	}

	d2, err := buildDescriptor(shuffled)
	if err != nil {
		t.Fatalf("buildDescriptor (shuffled): %v", err)
	}

	if d1.createSQL() != d2.createSQL() {
		t.Fatalf("DDL differs between declaration order and shuffled order")
	}
	if d1.digest != d2.digest {
		t.Fatalf("digest differs between declaration order (%s) and shuffled order (%s)", d1.digest, d2.digest)
	}
	td1, td2 := d1.types["widget"], d2.types["widget"]
	if !reflect.DeepEqual(td1.WithheldTargets, td2.WithheldTargets) {
		t.Fatalf("WithheldTargets differs between declaration order (%v) and shuffled order (%v)", td1.WithheldTargets, td2.WithheldTargets)
	}
	want := map[string]bool{"f1998": true}
	if !reflect.DeepEqual(td1.WithheldTargets, want) {
		t.Fatalf("WithheldTargets = %v, want %v regardless of input order", td1.WithheldTargets, want)
	}
}
