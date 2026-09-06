package writ_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	writ "github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
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
			}},
		},
	}

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if got := rules["standup"]; len(got) != 0 {
		t.Fatalf("expected both invalid rules dropped, got %+v", got)
	}
	if len(conflicts) != 2 {
		t.Fatalf("expected 2 reported invalid-rule conflicts, got %+v", conflicts)
	}
}

func TestRulesFromSchemas_DeprecatedFieldExcluded(t *testing.T) {
	f := mkField("standup", "create", 1, "summary", "lww")
	f.Deprecated = true
	a := state.Schema{
		ObjectID: "sch-a",
		Types:    []state.SchemaType{{Name: "standup", Fields: []state.SchemaField{f}}},
	}

	rules, _ := writ.RulesFromSchemas([]state.Schema{a})
	if got := rules["standup"]; len(got) != 0 {
		t.Fatalf("expected a deprecated field to install no rule, got %+v", got)
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

func TestRulesFromSchemas_VersionBumpNewStrategySameTargetRejected(t *testing.T) {
	v1 := mkField("widget", "widget-op", 1, "value", "lww")
	v2 := mkField("widget", "widget-op", 2, "value", "set-union") // same target ("value"), different strategy: rejected
	a := state.Schema{
		ObjectID: "sch-a",
		Types:    []state.SchemaType{{Name: "widget", Fields: []state.SchemaField{v1, v2}}},
	}
	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	got := rules["widget"]
	if len(got) != 1 || got[0].OpVersion != 1 {
		t.Fatalf("expected only the version-1 rule installed, got %+v", got)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict reporting the rejected version-2 rule, got %+v", conflicts)
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
