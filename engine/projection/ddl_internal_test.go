package projection

import (
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/state"
)

// loadTestRules is a schema-shaped rule index covering every table shape
// this file's tests care about: an OR-set pair per target, two keyed-lww key
// groups (one of them retargeting a field name the other also uses), and an
// append group over two fields. Writ hard-codes no object type but `schema`,
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

// TestAmbiguousAppendFieldWithholdsTargetNotGroup covers two append-strategy
// rules binding the same target key to two different Fields under one exact
// (op_type, op_version) envelope. WRIT-201 made that shape normative:
// spec/fold.md §5 rules that every matching rule applies, in canonical rule
// order, so one op writing both fields appends both entries to one list.
// The append group's table — one row per op, one column per member target —
// structurally cannot hold two entries for one target from one op, so that
// target is withheld.
//
// What this pins is the scope of the withhold: the one unrepresentable
// target, never the type and never the group (§Targets a projection declines). Two earlier scopes
// were both too wide. WRIT-189 round 5 withheld the whole type, on the
// premise that state.Fold picked one of the two values deterministically and
// the projection merely could not tell which; WRIT-201 falsified that
// premise (fold picks neither — it takes both). WRIT-201 round 1 narrowed it
// to the append group, which is still wider than the unrepresentable unit:
// buildAppendGroups unions every target co-occurring in an envelope, so the
// wholly representable "tags" here — one field, one envelope, exactly the
// shape the group table has always held — lost its table and rows purely
// because "note" shares the envelope (round 2 MEDIUM-1).
//
// So: the type table and its unrelated targets build as usual, "tags" keeps
// a table of its own, only "note" lands in WithheldTargets for materialize
// to route into unknown_fields, and the surviving group's name and envelope
// set are derived from the members that survived. Order-independent, as
// before.
func TestAmbiguousAppendFieldWithholdsTargetNotGroup(t *testing.T) {
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
				t.Fatalf("object type \"widget\" was withheld entirely; only the unrepresentable target should be")
			}
			if !td.WithheldTargets["note"] {
				t.Fatalf("WithheldTargets = %v, want the \"note\" target recorded", td.WithheldTargets)
			}
			if td.WithheldTargets["tags"] {
				t.Fatalf("WithheldTargets = %v: \"tags\" is representable and must not be collateral (§Targets a projection declines)", td.WithheldTargets)
			}
			if len(td.AppendGroups) != 1 {
				t.Fatalf("AppendGroups = %+v, want exactly one — \"tags\" alone", td.AppendGroups)
			}
			g := td.AppendGroups[0]
			if g.Table != "o_widget__tags" {
				t.Fatalf("group table = %q, want \"o_widget__tags\" (named for the retained members only)", g.Table)
			}
			if len(g.Members) != 1 || g.Members[0].Key != "tags" || g.Members[0].Column != "f_tags" {
				t.Fatalf("group members = %+v, want just the \"tags\"/f_tags member", g.Members)
			}
			wantEnv := []appendGroupEnvelope{{OpType: "note", OpVersion: 1}}
			if !reflect.DeepEqual(g.Envelopes, wantEnv) {
				t.Fatalf("group envelopes = %+v, want %+v", g.Envelopes, wantEnv)
			}
			var names []string
			for _, tbl := range desc.tables {
				names = append(names, tbl.Name)
			}
			sort.Strings(names)
			want := []string{"o_widget", "o_widget__tags"}
			if !reflect.DeepEqual(names, want) {
				t.Fatalf("generated tables = %v, want %v (no o_widget__note, no o_widget__note_tags)", names, want)
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
// op-envelope's grammar (^[a-z][a-z0-9-]*$), and "widget--base-head"
// generates the exact same table name ("o_widget__base_head") as widget's
// own base/head append-group table (ddl.go's appendGroupPlan — round 2
// MAJOR-1 folded the separate "base" and "head" child tables into one
// shared table, o_widget__base_head, so that is what a collision has to
// target now). buildDescriptor used to return a hard error for this — which
// bricked buildDescriptor's caller chain (ApplySchema -> writ.Open)
// permanently, since nothing can be removed from the log to fix it. Data
// someone else wrote must never brick the repository (the same ruling
// WRIT-188 round 3 established): the colliding type is withheld exactly
// like an invalid target above, and every other type's tables — including
// the one it collided with — are unaffected.
func TestCollidingIdentifierWithholdsTables(t *testing.T) {
	rules := loadTestRules(t)
	rules["widget--base-head"] = []state.Rule{
		{OpType: "create", Field: "title", Strategy: "lww", ValueType: "string", ObjectType: "widget--base-head"},
	}

	desc, err := buildDescriptor(rules)
	if err != nil {
		t.Fatalf("buildDescriptor: %v", err)
	}

	if _, ok := desc.types["widget--base-head"]; ok {
		t.Fatalf("expected colliding type %q to be withheld (no tables), but it has tables", "widget--base-head")
	}
	widgetTD, ok := desc.types["widget"]
	if !ok {
		t.Fatalf("expected declared type %q to keep its tables", "widget")
	}
	foundBase := false
	for _, ct := range widgetTD.Children {
		if ct.Name == "o_widget__base_head" {
			foundBase = true
		}
	}
	if !foundBase {
		t.Fatalf("expected widget to still own o_widget__base_head, got children %+v", widgetTD.Children)
	}

	seen := make(map[string]bool)
	for _, tbl := range desc.tables {
		if seen[tbl.Name] {
			t.Fatalf("duplicate table %q in descriptor", tbl.Name)
		}
		seen[tbl.Name] = true
	}
}
