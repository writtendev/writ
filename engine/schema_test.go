package writ_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	writ "github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/schemasrc"
	"github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec"
)

// TestSchemaRulesDriftGuard proves state.SchemaRules() and the published
// testdata/schema-ops/field-rules.json are the same table — the check that
// keeps "hard-coded in the engine" from silently diverging from "normative
// in the spec" (spec/schema-ops.md §7's bootstrap step 2).
func TestSchemaRulesDriftGuard(t *testing.T) {
	allRules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules failed: %v", err)
	}

	var expectedRules []writ.Rule
	for _, r := range allRules {
		if r.Vocabulary == "schema-ops" {
			expectedRules = append(expectedRules, writ.Rule{
				OpType:    r.OpType,
				OpVersion: r.OpVersion,
				Field:     r.Field,
				Target:    r.Target,
				Strategy:  r.Strategy,
				Key:       r.Key,
				Lattice:   r.Lattice,
				ValueType: r.ValueType,
				Enum:      r.Enum,
				MaxLength: r.MaxLength,
				KeyTypes:  r.KeyTypes,
			})
		}
	}

	builtIn := writ.SchemaRules()
	if !reflect.DeepEqual(builtIn, expectedRules) {
		t.Fatalf("SchemaRules() drifted from published schema-ops field-rules.json:\n got:  %+v\n want: %+v", builtIn, expectedRules)
	}
}

func mkField(typ, opType string, opVersion int64, field, strategy string) writ.SchemaField {
	return writ.SchemaField{Name: field, OpType: opType, OpVersion: opVersion, Strategy: strategy, ValueType: "string"}
}

func TestRulesFromSchemas_ObjectTypeCollisionInstallsNoRules(t *testing.T) {
	a := state.Schema{
		ObjectID:  "sch-a",
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "standup", Fields: []state.SchemaField{mkField("standup", "create", 1, "summary", "lww")}},
		},
	}
	b := state.Schema{
		ObjectID:  "sch-b",
		Namespace: "beta",
		Types: []state.SchemaType{
			{Name: "standup", Fields: []state.SchemaField{mkField("standup", "create", 1, "notes", "lww")}},
		},
	}

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{b, a})

	if _, ok := rules["standup"]; ok {
		t.Fatalf("expected no rules installed for the contested object_type, got %+v", rules["standup"])
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 conflict, got %+v", conflicts)
	}
	c := conflicts[0]
	if c.ObjectType != "standup" {
		t.Errorf("conflict ObjectType = %q, want standup", c.ObjectType)
	}
	wantIDs := []string{"sch-a", "sch-b"}
	gotIDs := append([]string(nil), c.ObjectIDs...)
	sort.Strings(gotIDs)
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Errorf("conflict ObjectIDs = %v, want %v", gotIDs, wantIDs)
	}

	// §7.1 / FC-1 / FC-12: withholding rules for a contested object_type
	// must never surface as a fold error for the ops that type's own data
	// writes. Fold(dataOps, rules["standup"]) — rules["standup"] absent,
	// same as an unresolvable object_type — must return a nil error with
	// every op quarantined as unknown, exactly the absent-schema path.
	dataOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "standup", OpType: "create", OpVersion: 1,
			Body: json.RawMessage(`{"summary":"hello"}`),
		},
		ID: "op-1",
	}
	objState, err := writ.Fold([]codec.Op{dataOp}, rules["standup"])
	if err != nil {
		t.Fatalf("Fold on the contested object_type must not error, got: %v", err)
	}
	if len(objState.UnknownOps) != 1 || objState.UnknownOps[0].Commit != "op-1" {
		t.Fatalf("expected op-1 to fall through to UnknownOps, got %+v", objState)
	}
}

func TestRulesFromSchemas_NamespaceCollisionDoesNotWithholdRulesAlone(t *testing.T) {
	a := state.Schema{
		ObjectID:  "sch-a",
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "standup", Fields: []state.SchemaField{mkField("standup", "create", 1, "summary", "lww")}},
		},
	}
	b := state.Schema{
		ObjectID:  "sch-b",
		Namespace: "acme", // same namespace, different type: cosmetic collision only
		Types: []state.SchemaType{
			{Name: "retro", Fields: []state.SchemaField{mkField("retro", "create", 1, "notes", "lww")}},
		},
	}

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a, b})

	if _, ok := rules["standup"]; !ok {
		t.Errorf("expected rules installed for 'standup' despite the namespace collision, got %+v", rules)
	}
	if _, ok := rules["retro"]; !ok {
		t.Errorf("expected rules installed for 'retro' despite the namespace collision, got %+v", rules)
	}

	var sawNamespaceConflict bool
	for _, c := range conflicts {
		if c.Namespace == "acme" && c.ObjectType == "" {
			sawNamespaceConflict = true
		}
	}
	if !sawNamespaceConflict {
		t.Errorf("expected a namespace-only conflict reported, got %+v", conflicts)
	}
}

func TestRulesFromSchemas_SchemaCannotBeRedefinedFromTheLog(t *testing.T) {
	a := state.Schema{
		ObjectID: "sch-a",
		Types: []state.SchemaType{
			{Name: "schema", Fields: []state.SchemaField{mkField("schema", "create", 1, "namespace", "create-once")}},
		},
	}

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if _, ok := rules["schema"]; ok {
		t.Fatalf("expected no rules installed for a log-defined 'schema' type, got %+v", rules["schema"])
	}
	if len(conflicts) != 1 || conflicts[0].ObjectType != "schema" {
		t.Fatalf("expected a single 'schema cannot be redefined' conflict, got %+v", conflicts)
	}
}

func TestRulesFromSchemas_InvalidRuleDroppedNotInstalled(t *testing.T) {
	a := state.Schema{
		ObjectID: "sch-a",
		Types: []state.SchemaType{
			{Name: "standup", Fields: []state.SchemaField{
				mkField("standup", "create", 1, "summary", ""),        // strategy:"" - invalid
				mkField("standup", "create", 1, "notes", "keyed-lww"), // keyed-lww with no key - invalid
				mkField("standup", "create", 1, "owner", "bogus"),     // strategy:"bogus" - not in the catalogue at all
			}},
		},
	}

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if got := rules["standup"]; len(got) != 0 {
		t.Fatalf("expected all three invalid rules dropped, got %+v", got)
	}
	if len(conflicts) != 3 {
		t.Fatalf("expected 3 reported invalid-rule conflicts, got %+v", conflicts)
	}

	// §9's security boundary: none of these ever reach Fold as a rule, so
	// the consuming object's own data ops must not hard-error — they fall
	// through to UnknownOps exactly as if no rule existed at all. This is
	// the assertion the plan's §3 acceptance criteria named and round-1
	// found missing: no test ever called Fold with the resolved rules.
	dataOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "standup", OpType: "create", OpVersion: 1,
			Body: json.RawMessage(`{"summary":"hello","notes":"hi","owner":"alice"}`),
		},
		ID: "op-1",
	}
	objState, err := writ.Fold([]codec.Op{dataOp}, rules["standup"])
	if err != nil {
		t.Fatalf("Fold must never see an unknown-strategy rule, got error: %v", err)
	}
	if len(objState.UnknownOps) != 1 || objState.UnknownOps[0].Commit != "op-1" {
		t.Fatalf("expected op-1 to fall through to UnknownOps, got %+v", objState)
	}
}

// TestRulesFromSchemas_InvalidOpTypeGrammarDroppedNotInstalled pins
// spec/schema-ops.md §11's grammar gate: a define-field's or define-op's
// declared op_type must satisfy the same wire grammar
// spec/op-envelope.md pins for an envelope's own op_type
// (^[a-z][a-z0-9-]*$, at most opTypeMaxLength characters), or the
// candidate rule/op is dropped and reported as a SchemaConflict, never
// installed — exactly like an invalid strategy or value_type already is.
//
// A conforming producer can never actually reach this through
// Store.ApplySchema: spec/schemas/schema-ops.schema.json's op_type_name
// pattern already enforces the identical grammar on a
// define-field/define-op body's own op_type field, so ApplySchema
// refuses a bad op_type before state.FoldSchema ever sees it. This gate
// is for a non-conforming peer's schema object that bypassed producer
// validation and still folds cleanly — the same "rogue schema" scenario
// TestSchemaObjectAlwaysValidatesAgainstBootstrapTable covers for a
// different rule — which is why this is exercised directly against
// resolveSchemaTypes's inputs (state.Schema Go values) rather than
// through the envelope path.
func TestRulesFromSchemas_InvalidOpTypeGrammarDroppedNotInstalled(t *testing.T) {
	sch := state.Schema{
		ObjectID: "sch-a",
		Types: []state.SchemaType{
			{
				Name: "standup",
				Fields: []state.SchemaField{
					mkField("standup", "Bad_Type", 1, "summary", "lww"),            // uppercase/underscore - invalid
					mkField("standup", strings.Repeat("a", 65), 1, "notes", "lww"), // over opTypeMaxLength - invalid
					mkField("standup", "create", 1, "owner", "lww"),                // valid, control
				},
				Ops: []state.SchemaOp{
					{OpType: "UPPER", OpVersion: 1},
					{OpType: strings.Repeat("b", 65), OpVersion: 1},
					{OpType: "define-me", OpVersion: 1}, // valid, control
				},
			},
		},
	}

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{sch})
	got := rules["standup"]
	if len(got) != 1 || got[0].Field != "owner" {
		t.Fatalf("expected only the grammatically valid field rule installed, got %+v", got)
	}
	if len(conflicts) != 4 {
		t.Fatalf("expected 4 conflicts for the two grammar-invalid define-field op_types and the two grammar-invalid define-op op_types, got %+v", conflicts)
	}
	for _, c := range conflicts {
		if !strings.Contains(c.Reason, "not a valid op type") {
			t.Errorf("conflict reason does not name the grammar violation: %+v", c)
		}
	}

	vocabularies, _ := writ.VocabulariesFromSchemas([]state.Schema{sch})
	voc, ok := vocabularies["standup"]
	if !ok || !voc.Declared {
		t.Fatalf("expected standup Declared, got %+v", voc)
	}
	wantOpTypes := map[codec.OpVersionKey]bool{
		{OpType: "create", OpVersion: 1}:    true, // from the valid define-field
		{OpType: "define-me", OpVersion: 1}: true, // from the valid define-op
	}
	if !reflect.DeepEqual(voc.OpTypes, wantOpTypes) {
		t.Fatalf("expected only the grammatically valid op types installed, got %+v", voc.OpTypes)
	}

	// The same §9 security boundary as TestRulesFromSchemas_InvalidRuleDroppedNotInstalled:
	// an op signed under the grammar-invalid define-op's op_type falls
	// through to UnknownOps, never a hard fold error.
	dataOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "standup", OpType: "UPPER", OpVersion: 1,
			Body: json.RawMessage(`{}`),
		},
		ID: "op-1",
	}
	objState, err := writ.Fold([]codec.Op{dataOp}, rules["standup"])
	if err != nil {
		t.Fatalf("Fold must never see a rule for a grammar-invalid op_type, got error: %v", err)
	}
	if len(objState.UnknownOps) != 1 || objState.UnknownOps[0].Commit != "op-1" {
		t.Fatalf("expected op-1 to fall through to UnknownOps, got %+v", objState)
	}
}

// TestRulesFromSchemas_DeprecatedFieldStaysActiveForFolding proves the
// round-1 fix for finding 2: deprecated:true is metadata discouraging new
// writes, not a removal (spec/schema-ops.md §5, §8; AGENTS.md "old clients
// must not destroy new clients' data"). Deprecating a field must not make
// already-signed data written under it vanish from folded state, and must
// not reclassify the ops that wrote it as UnknownOps.
func TestRulesFromSchemas_DeprecatedFieldStaysActiveForFolding(t *testing.T) {
	active := mkField("standup", "create", 1, "summary", "lww")
	a := state.Schema{
		ObjectID: "sch-a",
		Types:    []state.SchemaType{{Name: "standup", Fields: []state.SchemaField{active}}},
	}
	deprecated := active
	deprecated.Deprecated = true
	b := state.Schema{
		ObjectID: "sch-a",
		Types:    []state.SchemaType{{Name: "standup", Fields: []state.SchemaField{deprecated}}},
	}

	rulesBefore, conflictsBefore := writ.RulesFromSchemas([]state.Schema{a})
	rulesAfter, conflictsAfter := writ.RulesFromSchemas([]state.Schema{b})

	if len(conflictsBefore) != 0 || len(conflictsAfter) != 0 {
		t.Fatalf("expected no conflicts either way, got before=%+v after=%+v", conflictsBefore, conflictsAfter)
	}

	got := rulesAfter["standup"]
	if len(got) != 1 {
		t.Fatalf("expected the deprecated field's rule still installed, got %+v", got)
	}
	if !got[0].Deprecated {
		t.Errorf("expected Deprecated carried through onto the resolved Rule, got %+v", got[0])
	}

	// The data op that wrote "summary" yesterday must fold to the same
	// state today, whether or not the field has since been deprecated —
	// and it must not fall into UnknownOps in either case.
	dataOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "standup", OpType: "create", OpVersion: 1,
			Body: json.RawMessage(`{"summary":"important"}`),
		},
		ID: "op-1",
	}

	stateBefore, err := writ.Fold([]codec.Op{dataOp}, rulesBefore["standup"])
	if err != nil {
		t.Fatalf("Fold before deprecation failed: %v", err)
	}
	if len(stateBefore.UnknownOps) != 0 {
		t.Fatalf("expected op-1 known before deprecation, got unknown_ops=%+v", stateBefore.UnknownOps)
	}
	if stateBefore.State["summary"] != "important" {
		t.Fatalf("expected summary=%q before deprecation, got %+v", "important", stateBefore.State)
	}

	stateAfter, err := writ.Fold([]codec.Op{dataOp}, rulesAfter["standup"])
	if err != nil {
		t.Fatalf("Fold after deprecation failed: %v", err)
	}
	if len(stateAfter.UnknownOps) != 0 {
		t.Fatalf("deprecate-field must not reclassify op-1 as unknown, got unknown_ops=%+v", stateAfter.UnknownOps)
	}
	if !reflect.DeepEqual(stateBefore.State, stateAfter.State) {
		t.Fatalf("deprecation changed the folded state: before=%+v after=%+v", stateBefore.State, stateAfter.State)
	}
}

func TestRulesFromSchemas_VersionBumpSameTargetSameStrategyOK(t *testing.T) {
	a := state.Schema{
		ObjectID: "sch-a",
		Types: []state.SchemaType{{Name: "widget", Fields: []state.SchemaField{
			mkField("widget", "widget-op", 1, "value", "lww"),
			mkField("widget", "widget-op", 2, "value", "lww"),
		}}},
	}
	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %+v", conflicts)
	}
	if got := rules["widget"]; len(got) != 2 {
		t.Fatalf("expected both version-1 and version-2 rules installed under the shared target, got %+v", got)
	}
}

// TestRulesFromSchemas_VersionBumpNewStrategySameTargetRejected pins
// WRIT-211's no-survivors response: a version bump that changes strategy
// while reusing a target withholds every rule bound to that target, not
// only the later one. Before WRIT-211 this dropped only the version-2 rule
// and kept version-1 installed — a "declared first wins" outcome that
// happened to work here only because there were exactly two rules; the
// same response now applies whether the target is bound by two rules or
// twenty, so it needs no per-arity special case.
func TestRulesFromSchemas_VersionBumpNewStrategySameTargetRejected(t *testing.T) {
	v1 := mkField("widget", "widget-op", 1, "value", "lww")
	v2 := mkField("widget", "widget-op", 2, "value", "set-union") // same target ("value"), different strategy: rejected
	a := state.Schema{
		ObjectID: "sch-a",
		Types:    []state.SchemaType{{Name: "widget", Fields: []state.SchemaField{v1, v2}}},
	}
	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if got := rules["widget"]; len(got) != 0 {
		t.Fatalf("expected no rules installed for the target, got %+v", got)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict reporting the withheld target, got %+v", conflicts)
	}
}

func TestRulesFromSchemas_VersionBumpNewStrategyDistinctTargetOK(t *testing.T) {
	v1 := mkField("widget", "widget-op", 1, "value", "lww")
	v2 := mkField("widget", "widget-op", 2, "value", "set-union")
	v2.Target = "value_v2"
	a := state.Schema{
		ObjectID: "sch-a",
		Types:    []state.SchemaType{{Name: "widget", Fields: []state.SchemaField{v1, v2}}},
	}
	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts once the version bump declares a distinct target, got %+v", conflicts)
	}
	if got := rules["widget"]; len(got) != 2 {
		t.Fatalf("expected both rules installed, got %+v", got)
	}
}

// TestRulesFromSchemas_CrossOpTypeTargetReuseWithDifferentValueTypeRejected
// pins WRIT-198's widened collision check, made transitive and
// no-survivors by WRIT-211: two fields sharing a target (here the default,
// the field name) across different op_types must agree on value_type too,
// not just strategy. Before WRIT-198's widening this was accepted silently
// — the exact shape of the five colliding targets WRIT-198 found, all of
// which agreed on strategy and disagreed on value_type. Before WRIT-211,
// the resolver additionally picked "create"'s rule as a survivor because it
// happened to sort first in canonical (op_type, op_version, field) order —
// an accident of the two op_types' names, not of the schema's own
// agreement — rather than withholding the whole target the way it now does.
func TestRulesFromSchemas_CrossOpTypeTargetReuseWithDifferentValueTypeRejected(t *testing.T) {
	v1 := mkField("widget", "create", 1, "owner", "lww")
	v1.ValueType = "person-ref"
	v2 := mkField("widget", "assign", 1, "owner", "lww")
	v2.ValueType = "object-ref"
	a := state.Schema{
		ObjectID: "sch-a",
		Types:    []state.SchemaType{{Name: "widget", Fields: []state.SchemaField{v1, v2}}},
	}
	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if got := rules["widget"]; len(got) != 0 {
		t.Fatalf("expected no rules installed for the target, got %+v", got)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict reporting the withheld target (same strategy, different value_type, cross op_type), got %+v", conflicts)
	}
}

// TestRulesFromSchemas_VersionBumpValueTypeOnlyOK is the positive control:
// an op_version bump of the same (op_type, field) may freely change
// value_type under the shared default target, exactly as
// spec/schema-ops.md §8 and spec/fold.md §5 permit — the carve-out
// TestRulesFromSchemas_CrossOpTypeTargetReuseWithDifferentValueTypeRejected
// does not extend to.
func TestRulesFromSchemas_VersionBumpValueTypeOnlyOK(t *testing.T) {
	v1 := mkField("widget", "widget-op", 1, "value", "lww")
	v1.ValueType = "string"
	v2 := mkField("widget", "widget-op", 2, "value", "lww")
	v2.ValueType = "int"
	a := state.Schema{
		ObjectID: "sch-a",
		Types:    []state.SchemaType{{Name: "widget", Fields: []state.SchemaField{v1, v2}}},
	}
	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts for a version bump changing only value_type, got %+v", conflicts)
	}
	if got := rules["widget"]; len(got) != 2 {
		t.Fatalf("expected both version-1 and version-2 rules installed, got %+v", got)
	}
}

// TestRulesFromSchemas_ThreeRuleTargetSharingIsOrderIndependent pins
// WRIT-211's fix directly with the ticket's own three-rule vector — the
// smallest input that distinguishes a transitive shared-target agreement
// relation from the non-transitive one it replaced:
//
//   - (configure, v1, mode, string) and (configure, v2, mode, int) are a
//     version-bump class of one another (same op_type and field): taken
//     alone they would be permitted to disagree on value_type, §8's
//     carve-out.
//   - (reset, v1, value, target: mode, string) shares their target but
//     belongs to neither's class.
//
// Once a target is bound by more than one class, the carve-out is void for
// every rule on it (spec.CheckTargetAgreement), so the whole target — all
// three rules — must be withheld. That answer must not depend on the order
// RulesFromSchemas is handed the type's fields in: the superseded
// candidate-vs-bound form dropped a different one of the three depending on
// that order, which is the defect WRIT-211 fixed. This shuffles both the
// field order within the type and the (single-element) schema slice, and
// asserts the resolved rules, the conflicts, and the resulting folded
// ObjectState are byte-identical across every permutation.
func TestRulesFromSchemas_ThreeRuleTargetSharingIsOrderIndependent(t *testing.T) {
	v1 := mkField("widget", "configure", 1, "mode", "lww")
	v1.ValueType = "string"
	v2 := mkField("widget", "configure", 2, "mode", "lww")
	v2.ValueType = "int"
	v3 := mkField("widget", "reset", 1, "value", "lww")
	v3.ValueType = "string"
	v3.Target = "mode"
	allFields := []state.SchemaField{v1, v2, v3}

	dataOps := []codec.Op{
		{
			Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "widget", OpType: "configure", OpVersion: 1, Body: json.RawMessage(`{"mode":"legacy-status"}`)},
			ID:       "configure-1",
		},
		{
			Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "widget", OpType: "configure", OpVersion: 2, Body: json.RawMessage(`{"mode":7}`)},
			ID:       "configure-2",
		},
		{
			Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "widget", OpType: "reset", OpVersion: 1, Body: json.RawMessage(`{"value":"legacy-status"}`)},
			ID:       "reset-1",
		},
	}

	r := rand.New(rand.NewSource(211))
	var wantRules map[string][]writ.Rule
	var wantConflicts []writ.SchemaConflict
	var wantState writ.ObjectState

	for i := 0; i < 30; i++ {
		fields := append([]state.SchemaField(nil), allFields...)
		r.Shuffle(len(fields), func(a, b int) { fields[a], fields[b] = fields[b], fields[a] })

		schemas := []state.Schema{{
			ObjectID: "sch-a",
			Types:    []state.SchemaType{{Name: "widget", Fields: fields}},
		}}
		// Permuting the (single-element) schema slice too: spec/schema-ops.md
		// §7 step 5 declares the resolver order-independent in the schema
		// objects it is handed, not only in the fields within one of them.
		r.Shuffle(len(schemas), func(a, b int) { schemas[a], schemas[b] = schemas[b], schemas[a] })

		rules, conflicts := writ.RulesFromSchemas(schemas)
		if got := rules["widget"]; len(got) != 0 {
			t.Fatalf("permutation #%d: expected the whole target withheld, got %+v", i, got)
		}
		if len(conflicts) != 1 {
			t.Fatalf("permutation #%d: expected exactly 1 conflict, got %+v", i, conflicts)
		}

		objState, err := writ.Fold(dataOps, rules["widget"])
		if err != nil {
			t.Fatalf("permutation #%d: Fold: %v", i, err)
		}
		if len(objState.UnknownOps) != 3 {
			t.Fatalf("permutation #%d: expected all 3 ops to fall through as unknown, got %+v", i, objState.UnknownOps)
		}

		if i == 0 {
			wantRules, wantConflicts, wantState = rules, conflicts, objState
			continue
		}
		if !reflect.DeepEqual(rules, wantRules) {
			t.Fatalf("permutation #%d: RulesFromSchemas order-dependence:\n got:  %+v\nwant: %+v", i, rules, wantRules)
		}
		if !reflect.DeepEqual(conflicts, wantConflicts) {
			t.Fatalf("permutation #%d: conflict order-dependence:\n got:  %+v\nwant: %+v", i, conflicts, wantConflicts)
		}
		if !reflect.DeepEqual(objState, wantState) {
			t.Fatalf("permutation #%d: folded state order-dependence:\n got:  %+v\nwant: %+v", i, objState, wantState)
		}
	}
}

// TestStoreSchemaFoldsEveryLoggedSchemaObject exercises Store.Schema
// end-to-end: ops are appended directly (there is no write path for schema
// ops in this ticket, spec/schema-ops.md §1.2), and Store.Schema is proven
// to fold every schema object present in the log, ordered by ObjectID,
// reading from the DAG rather than the projection cache.
func TestStoreSchemaFoldsEveryLoggedSchemaObject(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)

	ident, err := identity.Load(context.Background(), dir)
	if err != nil {
		t.Fatalf("identity.Load failed: %v", err)
	}

	dagStore, err := dag.Open(dir, ident)
	if err != nil {
		t.Fatalf("dag.Open failed: %v", err)
	}

	ctx := context.Background()
	appendSchemaOp := func(objectID, opType string, body map[string]any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		env := codec.Envelope{
			ObjectID:   objectID,
			ObjectType: "schema",
			OpType:     opType,
			OpVersion:  1,
			Body:       raw,
		}
		if _, err := dagStore.Append(ctx, env, nil); err != nil {
			t.Fatalf("append %s/%s failed: %v", objectID, opType, err)
		}
	}

	appendSchemaOp("sch-b", "create", map[string]any{"namespace": "beta"})
	appendSchemaOp("sch-a", "create", map[string]any{"namespace": "acme"})
	appendSchemaOp("sch-a", "define-type", map[string]any{"type": "standup"})

	store, err := writ.Open(dir)
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	defer store.Close()

	schemas, err := store.Schema(ctx)
	if err != nil {
		t.Fatalf("Store.Schema failed: %v", err)
	}
	if len(schemas) != 2 {
		t.Fatalf("expected 2 schema objects, got %d: %+v", len(schemas), schemas)
	}
	if schemas[0].ObjectID != "sch-a" || schemas[1].ObjectID != "sch-b" {
		t.Fatalf("expected schemas ordered by ObjectID (sch-a, sch-b), got (%s, %s)", schemas[0].ObjectID, schemas[1].ObjectID)
	}
	if schemas[0].Namespace != "acme" {
		t.Errorf("schemas[0].Namespace = %q, want acme", schemas[0].Namespace)
	}
	if len(schemas[0].Types) != 1 || schemas[0].Types[0].Name != "standup" {
		t.Errorf("schemas[0].Types = %+v, want [standup]", schemas[0].Types)
	}
	if schemas[1].Namespace != "beta" {
		t.Errorf("schemas[1].Namespace = %q, want beta", schemas[1].Namespace)
	}
}

// compileTestSchema parses and compiles a small writ.schema source into an
// envelope sequence under objectID, failing the test on any error.
func compileTestSchema(t testing.TB, objectID, src string) []codec.Envelope {
	t.Helper()
	f, err := schemasrc.Parse("writ.schema", []byte(src))
	if err != nil {
		t.Fatalf("schemasrc.Parse failed: %v", err)
	}
	envs, err := schemasrc.Compile(f, objectID)
	if err != nil {
		t.Fatalf("schemasrc.Compile failed: %v", err)
	}
	return envs
}

const testSchemaSrc = `namespace acme
description "Acme's vocabulary"

type standup {
  description "A daily standup update"

  op create 1 {
    title  string(200)  lww
  }
}
`

func TestApplySchema_RejectsNonSchemaObjectType(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	defer store.Close()

	env := codec.Envelope{ObjectID: "sch-a", ObjectType: "widget", OpType: "create", OpVersion: 1, Body: []byte(`{}`)}
	if err := store.ApplySchema(context.Background(), []codec.Envelope{env}); err == nil {
		t.Fatal("expected error for non-schema object_type, got nil")
	}
}

func TestApplySchema_RejectsWrongOpVersion(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	defer store.Close()

	env := codec.Envelope{ObjectID: "sch-a", ObjectType: "schema", OpType: "create", OpVersion: 2, Body: []byte(`{"namespace":"acme"}`)}
	if err := store.ApplySchema(context.Background(), []codec.Envelope{env}); err == nil {
		t.Fatal("expected error for op_version != 1, got nil")
	}
}

func TestApplySchema_RejectsMixedObjectIDs(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	defer store.Close()

	envs := []codec.Envelope{
		{ObjectID: "sch-a", ObjectType: "schema", OpType: "create", OpVersion: 1, Body: []byte(`{"namespace":"acme"}`)},
		{ObjectID: "sch-b", ObjectType: "schema", OpType: "define-type", OpVersion: 1, Body: []byte(`{"type":"standup"}`)},
	}
	if err := store.ApplySchema(context.Background(), envs); err == nil {
		t.Fatal("expected error for mixed object ids, got nil")
	}
}

func TestApplySchema_EmptyEnvsNoop(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	defer store.Close()

	if err := store.ApplySchema(context.Background(), nil); err != nil {
		t.Fatalf("ApplySchema with no envelopes should be a no-op, got: %v", err)
	}
	schemas, err := store.Schema(context.Background())
	if err != nil {
		t.Fatalf("Store.Schema failed: %v", err)
	}
	if len(schemas) != 0 {
		t.Fatalf("expected no schema objects, got %+v", schemas)
	}
}

// TestApplySchema_AppendsAndFolds proves ApplySchema's appended ops fold to
// exactly what schemasrc.Compile declared, and that SchemaFromEnvelopes'
// in-memory fold of the same compiled sequence agrees with Store.Schema's
// fold of what actually landed in the DAG — the property `writ schema plan`
// depends on to render a post-apply preview without appending anything.
func TestApplySchema_AppendsAndFolds(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	defer store.Close()

	envs := compileTestSchema(t, "sch-acme", testSchemaSrc)

	ctx := context.Background()
	if err := store.ApplySchema(ctx, envs); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	schemas, err := store.Schema(ctx)
	if err != nil {
		t.Fatalf("Store.Schema failed: %v", err)
	}
	if len(schemas) != 1 {
		t.Fatalf("expected 1 schema object, got %d: %+v", len(schemas), schemas)
	}
	logSchema := schemas[0]
	if logSchema.Namespace != "acme" {
		t.Errorf("Namespace = %q, want acme", logSchema.Namespace)
	}
	if len(logSchema.Types) != 1 || logSchema.Types[0].Name != "standup" {
		t.Fatalf("Types = %+v, want [standup]", logSchema.Types)
	}

	memSchema, err := writ.SchemaFromEnvelopes(envs)
	if err != nil {
		t.Fatalf("SchemaFromEnvelopes failed: %v", err)
	}
	// ObjectID is the only field that would legitimately differ if
	// SchemaFromEnvelopes derived it independently; it doesn't (both come
	// from the same envelopes), so a straight comparison is exact.
	if !reflect.DeepEqual(logSchema, memSchema) {
		t.Fatalf("SchemaFromEnvelopes disagrees with the log's own fold:\nlog: %+v\nmem: %+v", logSchema, memSchema)
	}
}

// TestApplySchema_SecondApplyOfSameSequenceAppendsNoNewOps is the engine-level
// half of the CLI's central idempotence test (WRIT-191): re-appending an
// already-applied delta is exactly the "empty delta" case cmd/writ's `plan`
// is responsible for computing, but ApplySchema itself has no notion of
// delta — it appends whatever it is given. This pins the other half: an
// empty delta (as `plan` would compute for an up-to-date file) really is a
// no-op at the engine layer, leaving the object's ops, and hence its fold,
// unchanged.
func TestApplySchema_SecondApplyOfSameSequenceAppendsNoNewOps(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	defer store.Close()

	envs := compileTestSchema(t, "sch-acme", testSchemaSrc)
	ctx := context.Background()
	if err := store.ApplySchema(ctx, envs); err != nil {
		t.Fatalf("first ApplySchema failed: %v", err)
	}

	before, err := store.Schema(ctx)
	if err != nil {
		t.Fatalf("Store.Schema failed: %v", err)
	}

	// An empty delta: nothing here for `plan` to append a second time.
	if err := store.ApplySchema(ctx, nil); err != nil {
		t.Fatalf("second ApplySchema (empty delta) failed: %v", err)
	}

	after, err := store.Schema(ctx)
	if err != nil {
		t.Fatalf("Store.Schema failed: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("schema state changed after a no-op apply:\nbefore: %+v\nafter: %+v", before, after)
	}
}

// schemaEnv builds a schema-object envelope for the given op body, used
// throughout the SchemaAfterApply tests below to construct real or proposed
// ops directly, without going through schemasrc.Compile.
func schemaEnv(t *testing.T, objectID, opType string, body map[string]any) codec.Envelope {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return codec.Envelope{
		ObjectID:   objectID,
		ObjectType: "schema",
		OpType:     opType,
		OpVersion:  1,
		Body:       raw,
	}
}

// findSchemaByID returns the Schema with the given ObjectID from schemas, or
// the zero Schema if absent.
func findSchemaByID(schemas []writ.Schema, objectID string) writ.Schema {
	for _, s := range schemas {
		if s.ObjectID == objectID {
			return s
		}
	}
	return writ.Schema{}
}

// assertSchemaAfterApplyMatchesRealApply is the equivalence property job 1
// of the round-4 regression net for WRIT-191's round-3 major
// (cmd/writ/schema.go's conflictsIntroducedByApply, the caller
// Store.SchemaAfterApply exists for): SchemaAfterApply(ctx, objectID, delta)
// must always equal what a real ApplySchema(ctx, delta) followed by
// Store.Schema(ctx) actually produces. SchemaAfterApply is called first,
// before delta is ever appended, exactly as `writ schema plan` calls it —
// nothing about the honest fold's correctness may depend on delta already
// being in the log.
func assertSchemaAfterApplyMatchesRealApply(t *testing.T, store *writ.Store, objectID string, delta []codec.Envelope) {
	t.Helper()
	ctx := context.Background()

	honest, err := store.SchemaAfterApply(ctx, objectID, delta)
	if err != nil {
		t.Fatalf("SchemaAfterApply failed: %v", err)
	}

	if err := store.ApplySchema(ctx, delta); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	schemas, err := store.Schema(ctx)
	if err != nil {
		t.Fatalf("Store.Schema failed: %v", err)
	}
	real := findSchemaByID(schemas, objectID)

	if !reflect.DeepEqual(honest, real) {
		t.Fatalf("SchemaAfterApply disagrees with a real ApplySchema followed by Store.Schema:\nhonest: %+v\nreal:   %+v", honest, real)
	}
}

// TestSchemaAfterApply_MatchesRealApply is job 1 of the round-4 regression
// net for WRIT-191's round-3 major: Store.SchemaAfterApply must always equal
// what a real ApplySchema of the same delta, followed by Store.Schema,
// actually produces — nothing else proves this permanently, and reverting
// cmd/writ/schema.go's buildSchemaPlan to pass the file-alone fold instead
// of SchemaAfterApply's honest one (the round-3 major, verbatim) left every
// existing test green. The round-4 reviewer verified this property
// empirically over these eight scenarios, chosen to stress every shape
// SchemaAfterApply's own doc comment claims to handle — no prior ops, prior
// ops with a partial delta, no delta at all, more than one schema object
// sharing this writer's single per-object-type ref, a keyed-lww attribute
// that widens, one that narrows and leaves a stale register, and a genuine
// cross-writer fork — and found every one reflect.DeepEqual; this makes that
// verification permanent instead of throwaway.
func TestSchemaAfterApply_MatchesRealApply(t *testing.T) {
	newStore := func(t *testing.T) *writ.Store {
		t.Helper()
		dir, _ := setupConfiguredRepo(t)
		store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
		if err != nil {
			t.Fatalf("writ.Open failed: %v", err)
		}
		t.Cleanup(func() { store.Close() })
		return store
	}

	t.Run("fresh mint", func(t *testing.T) {
		store := newStore(t)
		delta := []codec.Envelope{
			schemaEnv(t, "sch-fresh", "create", map[string]any{"namespace": "acme"}),
			schemaEnv(t, "sch-fresh", "define-type", map[string]any{"type": "standup"}),
			schemaEnv(t, "sch-fresh", "define-field", map[string]any{
				"type": "standup", "op_type": "create", "op_version": "1", "field": "title",
				"value_type": "string", "strategy": "lww",
			}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "sch-fresh", delta)
	})

	t.Run("partial multi-op delta on an object with prior ops", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		initial := []codec.Envelope{
			schemaEnv(t, "sch-partial", "create", map[string]any{"namespace": "acme"}),
			schemaEnv(t, "sch-partial", "define-field", map[string]any{
				"type": "standup", "op_type": "create", "op_version": "1", "field": "title",
				"value_type": "string", "strategy": "lww",
			}),
		}
		if err := store.ApplySchema(ctx, initial); err != nil {
			t.Fatalf("initial ApplySchema failed: %v", err)
		}
		delta := []codec.Envelope{
			schemaEnv(t, "sch-partial", "define-type", map[string]any{"type": "standup", "description": "A daily standup update"}),
			schemaEnv(t, "sch-partial", "define-field", map[string]any{
				"type": "standup", "op_type": "create", "op_version": "1", "field": "body",
				"value_type": "text", "strategy": "multi-value",
			}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "sch-partial", delta)
	})

	t.Run("empty delta", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		initial := []codec.Envelope{
			schemaEnv(t, "sch-empty-delta", "create", map[string]any{"namespace": "acme"}),
			schemaEnv(t, "sch-empty-delta", "define-field", map[string]any{
				"type": "standup", "op_type": "create", "op_version": "1", "field": "title",
				"value_type": "string", "strategy": "lww",
			}),
		}
		if err := store.ApplySchema(ctx, initial); err != nil {
			t.Fatalf("initial ApplySchema failed: %v", err)
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "sch-empty-delta", nil)
	})

	t.Run("second object minted while the writer's chain tip sits on the first", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		if err := store.ApplySchema(ctx, []codec.Envelope{
			schemaEnv(t, "sch-first", "create", map[string]any{"namespace": "acme"}),
		}); err != nil {
			t.Fatalf("ApplySchema for the first object failed: %v", err)
		}
		delta := []codec.Envelope{
			schemaEnv(t, "sch-second", "create", map[string]any{"namespace": "beta"}),
			schemaEnv(t, "sch-second", "define-type", map[string]any{"type": "retro"}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "sch-second", delta)
	})

	t.Run("delta back to the first while the tip sits on the second", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		if err := store.ApplySchema(ctx, []codec.Envelope{
			schemaEnv(t, "sch-first", "create", map[string]any{"namespace": "acme"}),
		}); err != nil {
			t.Fatalf("ApplySchema for the first object failed: %v", err)
		}
		if err := store.ApplySchema(ctx, []codec.Envelope{
			schemaEnv(t, "sch-second", "create", map[string]any{"namespace": "beta"}),
		}); err != nil {
			t.Fatalf("ApplySchema for the second object failed: %v", err)
		}
		delta := []codec.Envelope{
			schemaEnv(t, "sch-first", "define-type", map[string]any{"type": "standup"}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "sch-first", delta)
	})

	t.Run("re-declared field with widened attributes", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		initial := []codec.Envelope{
			schemaEnv(t, "sch-widen", "create", map[string]any{"namespace": "acme"}),
			schemaEnv(t, "sch-widen", "define-field", map[string]any{
				"type": "standup", "op_type": "create", "op_version": "1", "field": "title",
				"value_type": "string", "strategy": "lww", "max_length": 50,
			}),
		}
		if err := store.ApplySchema(ctx, initial); err != nil {
			t.Fatalf("initial ApplySchema failed: %v", err)
		}
		// Widened: max_length grows. strategy is required on every
		// define-field body (spec/schemas/schema-ops.schema.json), but
		// value_type is omitted here — it must survive from the prior op,
		// since state.FoldSchema's define-field case merges each attribute
		// independently (spec/schema-ops.md §8).
		delta := []codec.Envelope{
			schemaEnv(t, "sch-widen", "define-field", map[string]any{
				"type": "standup", "op_type": "create", "op_version": "1", "field": "title",
				"strategy": "lww", "max_length": 200,
			}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "sch-widen", delta)
	})

	t.Run("narrowing define-field that leaves a stale attribute", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		initial := []codec.Envelope{
			schemaEnv(t, "sch-narrow", "create", map[string]any{"namespace": "acme"}),
			schemaEnv(t, "sch-narrow", "define-field", map[string]any{
				"type": "standup", "op_type": "create", "op_version": "1", "field": "state",
				"value_type": "string", "enum": []string{"open", "done"}, "strategy": "lww",
			}),
		}
		if err := store.ApplySchema(ctx, initial); err != nil {
			t.Fatalf("initial ApplySchema failed: %v", err)
		}
		// Narrowed: enum is omitted from this body. Because the register is
		// only overwritten when the later op's body actually carries that
		// key, the stale enum from the first op survives in the honest fold
		// — the round-3 major's exact reproduction (this scenario also
		// anchors TestSchemaAfterApply_HonestFoldDivergesFromFileAloneFold,
		// below).
		delta := []codec.Envelope{
			schemaEnv(t, "sch-narrow", "define-field", map[string]any{
				"type": "standup", "op_type": "create", "op_version": "1", "field": "state",
				"value_type": "string", "strategy": "lww",
			}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "sch-narrow", delta)
	})

	t.Run("two writers: writer 1's chain tip sits inside the object but not its frontier", func(t *testing.T) {
		dir, _ := setupConfiguredRepo(t)
		ctx := context.Background()

		identA, err := identity.Load(ctx, dir)
		if err != nil {
			t.Fatalf("identity.Load failed: %v", err)
		}
		dagA, err := dag.Open(dir, identA)
		if err != nil {
			t.Fatalf("dag.Open (writer A) failed: %v", err)
		}
		createOp, err := dagA.Append(ctx, schemaEnv(t, "sch-multi", "create", map[string]any{"namespace": "acme"}), nil)
		if err != nil {
			t.Fatalf("writer A create append failed: %v", err)
		}

		// Writer B appends directly atop the object's real frontier
		// (createOp): createOp remains writer A's own chain tip (writer A
		// has not written anything since), but it now has a child from a
		// different writer, so it is no longer the object's frontier.
		identB := identity.Identity{
			WriterID: identity.WriterID("fedcba9876543210"),
			Author:   identity.Author{Name: "Bob Test", Email: "bob@example.com"},
		}
		dagB, err := dag.Open(dir, identB)
		if err != nil {
			t.Fatalf("dag.Open (writer B) failed: %v", err)
		}
		if _, err := dagB.Append(ctx, schemaEnv(t, "sch-multi", "define-type", map[string]any{"type": "standup"}), []string{createOp.ID}); err != nil {
			t.Fatalf("writer B append failed: %v", err)
		}

		// The store under test opens as writer A (from git config): its own
		// chain tip is still createOp, stale relative to the object's real
		// frontier (writer B's op) — schemaFrontier must compute the real
		// cross-writer frontier, not trust the writer's own chain tip.
		store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
		if err != nil {
			t.Fatalf("writ.Open failed: %v", err)
		}
		defer store.Close()

		delta := []codec.Envelope{
			schemaEnv(t, "sch-multi", "define-field", map[string]any{
				"type": "standup", "op_type": "create", "op_version": "1", "field": "title",
				"value_type": "string", "strategy": "lww",
			}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "sch-multi", delta)
	})
}

// TestSchemaAfterApply_HonestFoldDivergesFromFileAloneFold is job 2 of the
// round-4 regression net for WRIT-191's round-3 major: a case where
// SchemaFromEnvelopes' file-alone fold (cmd/writ/schema.go's `planned`) and
// Store.SchemaAfterApply's honest fold (`honestPlanned`) genuinely diverge —
// job 1, above, only proves they usually agree, and agreement alone cannot
// tell a future reader that the divergent case still matters.
//
// The narrowing scenario is the reproduction: the log already holds a
// define-field declaring an enum, and the proposed delta redeclares the same
// field without one. state.FoldSchema's define-field case only overwrites a
// register when the later op's body actually carries that key
// (spec/schema-ops.md §8), so the honest fold, which sees both ops, still
// carries the stale enum; the file-alone fold, which only ever sees the
// clean, self-contained declaration, does not.
//
// That divergence is exactly what cmd/writ/schema.go's
// conflictsIntroducedByApply relies on honestPlanned to expose: value_type
// "string" together with a non-empty enum is a rule spec.ValidateFieldRule
// rejects, so RulesFromSchemas drops it and reports a conflict when
// resolving the honest state — and reports none for the clean file-alone
// state, which is exactly the silent-corruption path the round-3 major
// fixed. This is reproduced directly against the engine, rather than through
// cmd/writ, because cmd/writ's own schemaRemovals check runs first and
// refuses every narrowing before an apply could ever reach
// conflictsIntroducedByApply with one: the honest fold's difference is
// unreachable from the CLI for this scenario, so the engine is the only
// place this mechanism can be pinned.
func TestSchemaAfterApply_HonestFoldDivergesFromFileAloneFold(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	const objectID = "sch-divergence"
	initial := []codec.Envelope{
		schemaEnv(t, objectID, "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, objectID, "define-field", map[string]any{
			"type": "standup", "op_type": "create", "op_version": "1", "field": "state",
			"value_type": "string", "enum": []string{"open", "done"}, "strategy": "lww",
		}),
	}
	if err := store.ApplySchema(ctx, initial); err != nil {
		t.Fatalf("initial ApplySchema failed: %v", err)
	}

	// The delta a real apply would append: the file no longer declares an
	// enum for "state".
	delta := []codec.Envelope{
		schemaEnv(t, objectID, "define-field", map[string]any{
			"type": "standup", "op_type": "create", "op_version": "1", "field": "state",
			"value_type": "string", "strategy": "lww",
		}),
	}

	// "planned": the file's own declaration folded as though it were the
	// object's entire history — SchemaFromEnvelopes' documented contract,
	// and cmd/writ/schema.go's own name for this value.
	compiled := []codec.Envelope{
		schemaEnv(t, objectID, "create", map[string]any{"namespace": "acme"}),
		delta[0],
	}
	planned, err := writ.SchemaFromEnvelopes(compiled)
	if err != nil {
		t.Fatalf("SchemaFromEnvelopes failed: %v", err)
	}

	// "honestPlanned": the real history plus the same delta.
	honestPlanned, err := store.SchemaAfterApply(ctx, objectID, delta)
	if err != nil {
		t.Fatalf("SchemaAfterApply failed: %v", err)
	}

	if reflect.DeepEqual(planned, honestPlanned) {
		t.Fatalf("expected planned and honestPlanned to diverge on the stale enum, got identical: %+v", planned)
	}

	findStateField := func(sch writ.Schema) *writ.SchemaField {
		for _, ty := range sch.Types {
			for i := range ty.Fields {
				if ty.Fields[i].Name == "state" {
					return &ty.Fields[i]
				}
			}
		}
		return nil
	}

	plannedField := findStateField(planned)
	if plannedField == nil || len(plannedField.Enum) != 0 {
		t.Fatalf("expected the file-alone fold to carry no enum for \"state\", got %+v", plannedField)
	}
	honestField := findStateField(honestPlanned)
	if honestField == nil || !reflect.DeepEqual(honestField.Enum, []string{"open", "done"}) {
		t.Fatalf("expected the honest fold to still carry the stale enum for \"state\", got %+v", honestField)
	}

	// The divergence is exactly what conflictsIntroducedByApply needs to see
	// to do its job: RulesFromSchemas must report a conflict for the honest
	// state (the stale enum makes the rule invalid) and none for the
	// file-alone state (which never carried it). A future buildSchemaPlan
	// that passed planned where honestPlanned belongs would see the second
	// outcome for both, and never learn that the apply it is about to make
	// would silently drop a field's rule.
	_, honestConflicts := writ.RulesFromSchemas([]state.Schema{honestPlanned})
	if len(honestConflicts) == 0 {
		t.Fatalf("expected RulesFromSchemas to report the stale-enum invalid rule for the honest fold, got none")
	}
	_, plannedConflicts := writ.RulesFromSchemas([]state.Schema{planned})
	if len(plannedConflicts) != 0 {
		t.Fatalf("expected RulesFromSchemas to report no conflicts for the clean file-alone fold, got %+v", plannedConflicts)
	}
}

// --- WRIT-188: producer validation drives off the schema in the log ---

// openWritableStore is the common setup for the producer-precedence tests
// below: a configured, signable repo with a real writ.Store, so
// Store.ApplySchema and the store's own wired dag.Store (writ.StoreDAGStore)
// exercise the exact same producer path a real caller would.
func openWritableStore(t *testing.T) (*writ.Store, context.Context) {
	t.Helper()
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, context.Background()
}

// TestDeclaredTypeWithNoFieldsIsWritable pins the len(typeRules) > 0 hole
// named in the WRIT-188 ticket's plan: a type declared by define-op alone
// (no define-field at all) is still Declared in
// writ.VocabulariesFromSchemas, so tier 2 of spec/op-envelope.md's
// producer precedence must accept an op of that declared op type with an
// empty body — not fall through to tier 4's refusal because rules[t]
// happens to be empty.
func TestDeclaredTypeWithNoFieldsIsWritable(t *testing.T) {
	store, ctx := openWritableStore(t)

	schemaEnvs := []codec.Envelope{
		schemaEnv(t, "sch-widget", "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, "sch-widget", "define-type", map[string]any{"type": "widget"}),
		schemaEnv(t, "sch-widget", "define-op", map[string]any{"type": "widget", "op_type": "create", "op_version": "1"}),
	}
	if err := store.ApplySchema(ctx, schemaEnvs); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	dagStore := writ.StoreDAGStore(store)
	env := codec.Envelope{
		ObjectID:   "widget-1",
		ObjectType: "widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{}`),
	}
	if _, err := dagStore.Append(ctx, env, nil); err != nil {
		t.Fatalf("Append refused a declared op type on a fieldless declared type: %v", err)
	}
}

// TestContestedObjectTypeStaysWritable is the ruling's carve-out
// (spec/op-envelope.md §Producer validation, tier 3), pinned at the
// engine/dag.Store.Append level: two schema objects binding the same bare
// object_type install no rules (spec/schema-ops.md §6) and the type is
// Contested in writ.VocabulariesFromSchemas, but the write must still
// succeed, unvalidated — the opposite of what an earlier draft of this
// ticket proposed (fail-closed on a contested type would be a *permanent*
// write outage, since nothing is ever removed from the log and there is no
// resolution step; see the ticket's RULED block). The reader still
// degrades a contested type's ops to UnknownOp (spec/schema-ops.md §6),
// unaffected by this test — the write/read asymmetry is the point.
func TestContestedObjectTypeStaysWritable(t *testing.T) {
	store, ctx := openWritableStore(t)

	if err := store.ApplySchema(ctx, []codec.Envelope{
		schemaEnv(t, "sch-a", "create", map[string]any{"namespace": "alpha"}),
		schemaEnv(t, "sch-a", "define-type", map[string]any{"type": "standup"}),
	}); err != nil {
		t.Fatalf("ApplySchema sch-a failed: %v", err)
	}
	if err := store.ApplySchema(ctx, []codec.Envelope{
		schemaEnv(t, "sch-b", "create", map[string]any{"namespace": "beta"}),
		schemaEnv(t, "sch-b", "define-type", map[string]any{"type": "standup"}),
	}); err != nil {
		t.Fatalf("ApplySchema sch-b failed: %v", err)
	}

	// Confirm the type really is contested before relying on that to test
	// tier 3: RulesFromSchemas installs no rules for it.
	schemas, err := store.Schema(ctx)
	if err != nil {
		t.Fatalf("Store.Schema failed: %v", err)
	}
	rules, conflicts := writ.RulesFromSchemas(schemas)
	if _, ok := rules["standup"]; ok {
		t.Fatalf("expected no rules installed for the contested type, got %+v", rules["standup"])
	}
	if len(conflicts) == 0 {
		t.Fatalf("expected a conflict for the contested object_type, got none")
	}

	dagStore := writ.StoreDAGStore(store)
	env := codec.Envelope{
		ObjectID:   "standup-1",
		ObjectType: "standup",
		OpType:     "anything-at-all",
		OpVersion:  1,
		Body:       json.RawMessage(`{"unvalidated":true}`),
	}
	if _, err := dagStore.Append(ctx, env, nil); err != nil {
		t.Fatalf("Append refused a write to a contested object_type; it must be permitted unvalidated: %v", err)
	}
}

// TestUndeclaredObjectTypeIsRefused is the genuine-absence tier (4): an
// object_type no schema in the log declares is refused — distinct from
// TestContestedObjectTypeStaysWritable's tier 3, so the two cannot be
// collapsed by accident.
func TestUndeclaredObjectTypeIsRefused(t *testing.T) {
	store, ctx := openWritableStore(t)

	dagStore := writ.StoreDAGStore(store)
	env := codec.Envelope{
		ObjectID:   "w-1",
		ObjectType: "widget",
		OpType:     "sprocket",
		OpVersion:  7,
		Body:       json.RawMessage(`{"anything":[1,2,3]}`),
	}
	if _, err := dagStore.Append(ctx, env, nil); err == nil {
		t.Fatal("Append accepted an object_type declared by no schema in the log")
	}
}

// TestLogSchemaGovernsDeclaredTypeExclusively pins tier 2: a repo whose
// log narrowly declares "gadget" (title only, no description) has that
// declaration govern the type outright. There is no second, wider source
// for a producer to fall back on — writ embeds a vocabulary for `schema`
// alone — so a body carrying a field the log does not declare is refused,
// and the error names the schema object responsible.
func TestLogSchemaGovernsDeclaredTypeExclusively(t *testing.T) {
	store, ctx := openWritableStore(t)

	if err := store.ApplySchema(ctx, []codec.Envelope{
		schemaEnv(t, "sch-gadget", "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, "sch-gadget", "define-type", map[string]any{"type": "gadget"}),
		schemaEnv(t, "sch-gadget", "define-op", map[string]any{"type": "gadget", "op_type": "create", "op_version": "1"}),
		schemaEnv(t, "sch-gadget", "define-field", map[string]any{
			"type": "gadget", "op_type": "create", "op_version": "1",
			"field": "title", "value_type": "string", "strategy": "lww",
		}),
	}); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	dagStore := writ.StoreDAGStore(store)

	// A body the narrow log schema fully covers: accepted.
	if _, err := dagStore.Append(ctx, codec.Envelope{
		ObjectID:   "g-1",
		ObjectType: "gadget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial"}`),
	}, nil); err != nil {
		t.Fatalf("Append refused a body the log schema fully declares: %v", err)
	}

	// A body carrying a field the log schema does not declare at all:
	// refused, naming the schema object.
	_, err := dagStore.Append(ctx, codec.Envelope{
		ObjectID:   "g-2",
		ObjectType: "gadget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial","description":"extra"}`),
	}, nil)
	if err == nil {
		t.Fatal("Append accepted a field the log schema does not declare")
	}
	if !strings.Contains(err.Error(), "sch-gadget") {
		t.Errorf("error does not name the responsible schema object (sch-gadget): %v", err)
	}
}

// TestSchemaObjectAlwaysValidatesAgainstBootstrapTable pins
// spec/schema-ops.md §7's single permitted exception: object_type
// "schema" always validates against the engine's built-in table, never
// the log, even when a schema object in the log attempts to redefine it
// (RulesFromSchemas/VocabulariesFromSchemas already refuse to install
// anything for that attempt — engine/schema.go's
// resolveSchemaTypes — but this pins that the *producer path* never even
// consults the log for it in the first place). Checked through both
// Store.ApplySchema and the store's own dag.Store.Append, since both are
// producer-boundary callers.
func TestSchemaObjectAlwaysValidatesAgainstBootstrapTable(t *testing.T) {
	store, ctx := openWritableStore(t)

	// A schema object that attempts to redefine "schema" itself: folds
	// fine (state.FoldSchema has no opinion), and is reported as a
	// permanent conflict by the resolver, but ApplySchema itself — which
	// writes only object_type "schema" ops — must not be disrupted by it.
	if err := store.ApplySchema(ctx, []codec.Envelope{
		schemaEnv(t, "sch-rogue", "create", map[string]any{"namespace": "rogue"}),
		schemaEnv(t, "sch-rogue", "define-type", map[string]any{"type": "schema"}),
	}); err != nil {
		t.Fatalf("ApplySchema (attempted schema redefinition) failed: %v", err)
	}

	schemas, err := store.Schema(ctx)
	if err != nil {
		t.Fatalf("Store.Schema failed: %v", err)
	}
	vocabularies, conflicts := writ.VocabulariesFromSchemas(schemas)
	if len(conflicts) == 0 {
		t.Fatalf("expected a conflict for the attempted redefinition of \"schema\", got none")
	}
	if _, ok := vocabularies["schema"]; ok {
		t.Fatalf("VocabulariesFromSchemas must never key \"schema\" at all, got %+v", vocabularies["schema"])
	}

	// A second, ordinary schema object write must still succeed: it is
	// itself object_type "schema", validated at tier 1 regardless of the
	// rogue redefinition attempt sitting in the log.
	if err := store.ApplySchema(ctx, []codec.Envelope{
		schemaEnv(t, "sch-ok", "create", map[string]any{"namespace": "ok"}),
	}); err != nil {
		t.Fatalf("ApplySchema (ordinary schema write) failed after a rogue redefinition attempt: %v", err)
	}

	// And a direct Append of a "schema" op through the store's own dag.Store
	// succeeds the same way.
	dagStore := writ.StoreDAGStore(store)
	if _, err := dagStore.Append(ctx, schemaEnv(t, "sch-ok", "define-type", map[string]any{"type": "widget"}), nil); err != nil {
		t.Fatalf("Append of an ordinary schema op failed after a rogue redefinition attempt: %v", err)
	}
}

// TestVocabulariesCacheStaysWarmAcrossNonSchemaAppends is the regression
// net for the MAJOR-1 finding: round 1 measured every Append moving its
// own writer chain's tip, which the vocabularies cache fingerprinted
// itself against, so the very next Append always looked like an
// invalidating change and re-ran a full Schema/Enumerate fold —
// 0.8ms/append flat on main versus 6.3ms growing to 34.2ms/append on this
// branch over 400 ops.
// BenchmarkVocabulariesCache demonstrates the wall-clock fix, but
// wall-clock timing is not something this suite should gate on; this test
// asserts the underlying invariant directly and deterministically instead.
//
// VocabulariesFromSchemas always builds a fresh map (resolveSchemaTypes ->
// make(codec.Vocabularies, ...)), so a cache hit is provable without
// timing anything: it must return the exact same map instance the first
// resolve produced, never a new one. If a non-"schema" append ever starts
// invalidating the cache again, this fails on the very first regression
// rather than on a timing threshold someone has to keep re-tuning.
func TestVocabulariesCacheStaysWarmAcrossNonSchemaAppends(t *testing.T) {
	store, ctx := openWritableStore(t)
	dagStore := writ.StoreDAGStore(store)

	// The ops below are ordinary, non-"schema" ops, which means they need
	// an object type some schema in the log declares before the producer
	// will accept them at all.
	applyCoreSchema(t, ctx, store)

	firstVocab, err := writ.StoreVocabularies(store, ctx)
	if err != nil {
		t.Fatalf("StoreVocabularies failed: %v", err)
	}
	firstAddr := reflect.ValueOf(firstVocab).Pointer()

	for i := 0; i < 20; i++ {
		env := codec.Envelope{
			ObjectID:   fmt.Sprintf("w-%d", i),
			ObjectType: "widget",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"T"}`),
		}
		if _, err := dagStore.Append(ctx, env, nil); err != nil {
			t.Fatalf("Append %d failed: %v", i, err)
		}

		vocab, err := writ.StoreVocabularies(store, ctx)
		if err != nil {
			t.Fatalf("StoreVocabularies after append %d failed: %v", i, err)
		}
		if got := reflect.ValueOf(vocab).Pointer(); got != firstAddr {
			t.Fatalf("append %d of a non-\"schema\" op invalidated the vocabularies cache (map address changed from %#x to %#x): a full Schema/Enumerate re-resolve ran when the fingerprint should have been rolled forward instead", i, firstAddr, got)
		}
	}

	// Control: a "schema" append must still invalidate — proves the test
	// above is not passing merely because nothing ever invalidates.
	if err := store.ApplySchema(ctx, []codec.Envelope{
		schemaEnv(t, "sch-a", "create", map[string]any{"namespace": "acme"}),
	}); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	afterSchema, err := writ.StoreVocabularies(store, ctx)
	if err != nil {
		t.Fatalf("StoreVocabularies after schema append failed: %v", err)
	}
	if reflect.ValueOf(afterSchema).Pointer() == firstAddr {
		t.Fatalf("a \"schema\" append did not invalidate the vocabularies cache")
	}
}

// findSchemaType returns the SchemaType named name from types, or the zero
// SchemaType and false if absent.
func findSchemaType(types []writ.SchemaType, name string) (writ.SchemaType, bool) {
	for _, ty := range types {
		if ty.Name == name {
			return ty, true
		}
	}
	return writ.SchemaType{}, false
}

// TestStoreTypes_ReturnsExactlyWhatTheLogDeclares pins Store.Types' basic
// contract: on a repository that has never run `writ schema apply` it
// returns nothing at all, because `schema` aside writ declares no object
// type of its own. Once a log schema declares a type, Types reflects it,
// with the declared field and op — the same data Objects.Create's
// zero-Version resolution consumes.
func TestStoreTypes_ReturnsExactlyWhatTheLogDeclares(t *testing.T) {
	store, ctx := openWritableStore(t)

	before, err := store.Types(ctx)
	if err != nil {
		t.Fatalf("Store.Types failed: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("Store.Types on a repository with no schema objects at all = %+v, want nothing", before)
	}

	if err := store.ApplySchema(ctx, []codec.Envelope{
		schemaEnv(t, "sch-gizmo", "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, "sch-gizmo", "define-type", map[string]any{"type": "gizmo"}),
		schemaEnv(t, "sch-gizmo", "define-op", map[string]any{"type": "gizmo", "op_type": "spin", "op_version": "1"}),
		schemaEnv(t, "sch-gizmo", "define-field", map[string]any{
			"type": "gizmo", "op_type": "spin", "op_version": "1",
			"field": "speed", "value_type": "int", "strategy": "lww",
		}),
	}); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	after, err := store.Types(ctx)
	if err != nil {
		t.Fatalf("Store.Types after ApplySchema failed: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("Store.Types after ApplySchema = %+v, want exactly the one declared type", after)
	}
	gizmo, ok := findSchemaType(after, "gizmo")
	if !ok {
		t.Fatalf("Store.Types after ApplySchema: declared type %q missing from %+v", "gizmo", after)
	}
	if len(gizmo.Fields) != 1 || gizmo.Fields[0].Name != "speed" || gizmo.Fields[0].OpType != "spin" || gizmo.Fields[0].ValueType != "int" {
		t.Fatalf("gizmo.Fields = %+v, want one field {speed, spin, int}", gizmo.Fields)
	}
	if len(gizmo.Ops) != 1 || gizmo.Ops[0].OpType != "spin" || gizmo.Ops[0].OpVersion != 1 {
		t.Fatalf("gizmo.Ops = %+v, want one op {spin, 1}", gizmo.Ops)
	}
}

// TestStoreTypes_DescriptionAndDeprecated pins round 1 MEDIUM-2's fix
// (schemaTypeFromResolved carrying Description/Deprecated through from
// resolveSchemaTypes, rather than leaving them structurally zero): the
// entire point of that fix had no test of its own until round 2's MEDIUM-1
// finding, and deleting the two lines that set them left the whole suite
// green. Covers a type description, an op description, and deprecate-type
// together on an ordinary log-declared type, and a define-op-only type
// (schema_test.go's own TestDeclaredTypeWithNoFieldsIsWritable /
// objects_test.go's TestObjectsCreate_DeclaredTypeWithNoFieldsIsCreatable
// shape) to confirm the fieldless path carries them too.
func TestStoreTypes_DescriptionAndDeprecated(t *testing.T) {
	store, ctx := openWritableStore(t)

	if err := store.ApplySchema(ctx, []codec.Envelope{
		schemaEnv(t, "sch-widget", "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, "sch-widget", "define-type", map[string]any{"type": "widget", "description": "A widget type"}),
		schemaEnv(t, "sch-widget", "define-op", map[string]any{"type": "widget", "op_type": "spin", "op_version": "1", "description": "spin it"}),
		schemaEnv(t, "sch-widget", "define-field", map[string]any{
			"type": "widget", "op_type": "spin", "op_version": "1",
			"field": "speed", "value_type": "int", "strategy": "lww",
		}),
		schemaEnv(t, "sch-widget", "deprecate-type", map[string]any{"type": "widget", "deprecated": true}),
	}); err != nil {
		t.Fatalf("ApplySchema (widget) failed: %v", err)
	}
	if err := store.ApplySchema(ctx, []codec.Envelope{
		schemaEnv(t, "sch-beacon", "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, "sch-beacon", "define-type", map[string]any{"type": "beacon", "description": "A fieldless beacon type"}),
		schemaEnv(t, "sch-beacon", "define-op", map[string]any{"type": "beacon", "op_type": "ping", "op_version": "1"}),
	}); err != nil {
		t.Fatalf("ApplySchema (beacon) failed: %v", err)
	}

	types, err := store.Types(ctx)
	if err != nil {
		t.Fatalf("Store.Types failed: %v", err)
	}

	widget, ok := findSchemaType(types, "widget")
	if !ok {
		t.Fatal("Store.Types: declared type \"widget\" missing")
	}
	if widget.Description != "A widget type" {
		t.Errorf("widget.Description = %q, want %q", widget.Description, "A widget type")
	}
	if !widget.Deprecated {
		t.Errorf("widget.Deprecated = false, want true (deprecate-type ran)")
	}
	if len(widget.Ops) != 1 || widget.Ops[0].Description != "spin it" {
		t.Errorf("widget.Ops = %+v, want exactly one op carrying description %q", widget.Ops, "spin it")
	}

	beacon, ok := findSchemaType(types, "beacon")
	if !ok {
		t.Fatal("Store.Types: define-op-only type \"beacon\" missing")
	}
	if beacon.Description != "A fieldless beacon type" {
		t.Errorf("beacon.Description = %q, want %q", beacon.Description, "A fieldless beacon type")
	}
	if beacon.Deprecated {
		t.Errorf("beacon.Deprecated = true, want false (never deprecated)")
	}
}
