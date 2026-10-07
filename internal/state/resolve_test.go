package state_test

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/state"
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

	var expectedRules []state.Rule
	for _, r := range allRules {
		if r.Vocabulary == "schema-ops" {
			expectedRules = append(expectedRules, state.Rule{
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

	builtIn := state.SchemaRules()
	if !reflect.DeepEqual(builtIn, expectedRules) {
		t.Fatalf("SchemaRules() drifted from published schema-ops field-rules.json:\n got:  %+v\n want: %+v", builtIn, expectedRules)
	}
}

func mkField(typ, opType string, opVersion int64, field, strategy string) state.SchemaField {
	return state.SchemaField{Name: field, OpType: opType, OpVersion: opVersion, Strategy: strategy, ValueType: "string"}
}

// TestSchemaInstallable_AgreesWithResolverDrop is the anti-drift device
// WRIT-291 adds so a third read-side copy of resolveSchemaTypes' two
// whole-object drop gates (the namespace-grammar gate, WRIT-253, and the
// derived-id gate, WRIT-254) cannot silently diverge from the resolver
// again the way cmd/writ/schema.go's schemaNamespaces once did (it copied
// only the derived-id gate). state.SchemaInstallable is now the one
// predicate both resolveSchemaTypes and schemaNamespaces gate on; this
// test proves it agrees with resolveSchemaTypes' own decision by checking
// it against RulesFromSchemas' conflicts directly, over one shape per
// gate combination, rather than trusting that the two can never drift
// apart just because one now calls the other.
//
// A whole-object drop always shows up in RulesFromSchemas' conflicts as
// one with an empty ObjectType: both the namespace-grammar and the
// derived-id gate in resolveSchemaTypes report that shape, and nothing
// else does -- a per-type or per-field conflict always names the
// non-empty ObjectType it was raised against.
func TestSchemaInstallable_AgreesWithResolverDrop(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		objectID  string
	}{
		{
			name:      "grammar-valid namespace, derived id",
			namespace: "acme",
			objectID:  "schema:acme",
		},
		{
			name:      "grammar-invalid namespace, derived id",
			namespace: "a') OR 1 --",
			objectID:  "schema:a') OR 1 --",
		},
		{
			name:      "grammar-valid namespace, foreign (non-derived) id",
			namespace: "acme",
			objectID:  "foreign-evil-schema",
		},
		{
			name:      "empty namespace, bare derived id",
			namespace: "",
			objectID:  "schema:",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sch := state.Schema{ObjectID: tc.objectID, Namespace: tc.namespace}
			installable := state.SchemaInstallable(sch)

			_, conflicts := state.RulesFromSchemas([]state.Schema{sch})
			var droppedWholesale bool
			for _, c := range conflicts {
				if c.ObjectType == "" && slices.Contains(c.ObjectIDs, sch.ObjectID) {
					droppedWholesale = true
					break
				}
			}

			if installable == droppedWholesale {
				t.Fatalf("state.SchemaInstallable(%+v) = %v, but RulesFromSchemas reported a whole-object drop for it = %v -- these must always disagree, since Installable means NOT dropped", sch, installable, droppedWholesale)
			}
		})
	}
}

func TestRulesFromSchemas_ObjectTypeCollisionInstallsNoRules(t *testing.T) {
	a := state.Schema{
		ObjectID:  "sch-a",
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "acme.standup", Fields: []state.SchemaField{mkField("standup", "create", 1, "summary", "lww")}},
		},
	}
	b := state.Schema{
		ObjectID:  "sch-b",
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "acme.standup", Fields: []state.SchemaField{mkField("standup", "create", 1, "notes", "lww")}},
		},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{b, a})

	if _, ok := rules["acme.standup"]; ok {
		t.Fatalf("expected no rules installed for acme.standup, got %+v", rules["acme.standup"])
	}
	if len(conflicts) != 2 {
		t.Fatalf("expected exactly 2 conflicts (one id-mismatch drop per object), got %+v", conflicts)
	}
	gotIDs := make([]string, 0, 2)
	for _, c := range conflicts {
		if c.ObjectType != "" {
			t.Errorf("expected an id-mismatch conflict naming no ObjectType, got %+v", c)
		}
		if c.Namespace != "acme" {
			t.Errorf("expected the conflict to name namespace acme, got %+v", c)
		}
		if len(c.ObjectIDs) != 1 {
			t.Errorf("expected exactly one ObjectID per id-mismatch conflict, got %+v", c)
		}
		gotIDs = append(gotIDs, c.ObjectIDs...)
	}
	sort.Strings(gotIDs)
	if want := []string{"sch-a", "sch-b"}; !reflect.DeepEqual(gotIDs, want) {
		t.Errorf("conflicts named ObjectIDs %v, want %v", gotIDs, want)
	}

	dataOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "acme.standup", OpType: "create", OpVersion: 1,
			Body: json.RawMessage(`{"summary":"hello"}`),
		},
		ID: "op-1",
	}
	objState, err := state.Fold([]codec.Op{dataOp}, rules["acme.standup"])
	if err != nil {
		t.Fatalf("Fold on the withheld object_type must not error, got: %v", err)
	}
	if len(objState.UnknownOps) != 1 || objState.UnknownOps[0].Commit != "op-1" {
		t.Fatalf("expected op-1 to fall through to UnknownOps, got %+v", objState)
	}
}

func TestRulesFromSchemas_DifferentNamespacesSameBareTypeBothInstall(t *testing.T) {
	acme := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "acme.code-review", Fields: []state.SchemaField{mkField("code-review", "create", 1, "summary", "lww")}},
		},
	}
	bigco := state.Schema{
		ObjectID:  "schema:bigco",
		Namespace: "bigco",
		Types: []state.SchemaType{
			{Name: "bigco.code-review", Fields: []state.SchemaField{mkField("code-review", "create", 1, "notes", "lww")}},
		},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{bigco, acme})
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts between two different namespaces' same-bare-name types, got %+v", conflicts)
	}
	if got := rules["acme.code-review"]; len(got) != 1 || got[0].Field != "summary" {
		t.Fatalf("expected acme.code-review's own rule installed, got %+v", got)
	}
	if got := rules["bigco.code-review"]; len(got) != 1 || got[0].Field != "notes" {
		t.Fatalf("expected bigco.code-review's own rule installed, got %+v", got)
	}
}

func TestRulesFromSchemas_UnqualifiedConsumerTypeDroppedNotInstalled(t *testing.T) {
	sch := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "standup", Fields: []state.SchemaField{mkField("standup", "create", 1, "summary", "lww")}},
			{Name: "bigco.retro", Fields: []state.SchemaField{mkField("retro", "create", 1, "notes", "lww")}},
			{Name: "acme.foo.bar", Fields: []state.SchemaField{mkField("foo.bar", "create", 1, "title", "lww")}},
		},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{sch})
	if _, ok := rules["standup"]; ok {
		t.Errorf("expected the unqualified bare type dropped, got %+v", rules["standup"])
	}
	if _, ok := rules["bigco.retro"]; ok {
		t.Errorf("expected the foreign-namespace-qualified type dropped, got %+v", rules["bigco.retro"])
	}
	if _, ok := rules["acme.foo.bar"]; ok {
		t.Errorf("expected the multi-dot type dropped, got %+v", rules["acme.foo.bar"])
	}
	if len(conflicts) != 3 {
		t.Fatalf("expected 3 conflicts (one per bad declaration), got %+v", conflicts)
	}

	kinds := make(map[string]state.SchemaConflictKind, len(conflicts))
	for _, c := range conflicts {
		kinds[c.ObjectType] = c.Kind
	}
	if kinds["standup"] != state.SchemaConflictTypeUnqualified {
		t.Errorf("standup's conflict Kind does not name the namespace-qualification failure: %q", kinds["standup"])
	}
	if kinds["bigco.retro"] != state.SchemaConflictTypeUnqualified {
		t.Errorf("bigco.retro's conflict Kind does not name the namespace-qualification failure: %q", kinds["bigco.retro"])
	}
	if kinds["acme.foo.bar"] != state.SchemaConflictTypeUngrammatical {
		t.Errorf("acme.foo.bar's conflict Kind does not name the object_type grammar failure: %q", kinds["acme.foo.bar"])
	}
}

func TestRulesFromSchemas_UngrammaticalDeclarationDroppedNotInstalled(t *testing.T) {
	tests := []struct {
		name                 string
		namespace            string
		hostileType          string
		wantSiblingInstalled bool
	}{
		{
			name:                 "quote breaks out of a SQL string literal",
			namespace:            "acme",
			hostileType:          "acme.it's",
			wantSiblingInstalled: true,
		},
		{
			name:                 "NUL byte truncates generated SQL text",
			namespace:            "acme",
			hostileType:          "acme.x\x00y",
			wantSiblingInstalled: true,
		},
		{
			name:                 "quote and shell metacharacters",
			namespace:            "acme",
			hostileType:          `acme.Foo Bar"; DROP`,
			wantSiblingInstalled: true,
		},
		{
			name:                 "dot-lock exclusion (ref-unwritable)",
			namespace:            "acme",
			hostileType:          "acme.lock",
			wantSiblingInstalled: true,
		},
		{
			name:                 "65-char second segment exceeds the per-segment bound",
			namespace:            "acme",
			hostileType:          "acme." + strings.Repeat("a", 65),
			wantSiblingInstalled: true,
		},
		{
			name:                 "ungrammatical namespace withholds every type it declares",
			namespace:            "a') OR 1 --",
			hostileType:          "a') OR 1 --.z",
			wantSiblingInstalled: false,
		},
		{
			name:                 "namespace itself carries a dot",
			namespace:            "acme.b",
			hostileType:          "acme.b.c",
			wantSiblingInstalled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			siblingType := tc.namespace + ".gadget"
			sch := state.Schema{
				ObjectID:  "schema:" + tc.namespace,
				Namespace: tc.namespace,
				Types: []state.SchemaType{
					{Name: tc.hostileType, Fields: []state.SchemaField{mkField(tc.hostileType, "create", 1, "title", "lww")}},
					{Name: siblingType, Fields: []state.SchemaField{mkField(siblingType, "create", 1, "title", "lww")}},
				},
			}

			rules, conflicts := state.RulesFromSchemas([]state.Schema{sch})

			if _, ok := rules[tc.hostileType]; ok {
				t.Errorf("expected the hostile declaration dropped, got %+v", rules[tc.hostileType])
			}
			if len(conflicts) != 1 {
				t.Fatalf("expected exactly 1 conflict, got %+v", conflicts)
			}

			_, siblingInstalled := rules[siblingType]
			if siblingInstalled != tc.wantSiblingInstalled {
				t.Errorf("sibling type %q installed=%v, want %v (conflicts: %+v)", siblingType, siblingInstalled, tc.wantSiblingInstalled, conflicts)
			}
		})
	}
}

func TestRulesFromSchemas_NonDerivedIDObjectDroppedNotInstalled(t *testing.T) {
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "acme.standup", Fields: []state.SchemaField{mkField("standup", "create", 1, "summary", "lww")}},
		},
	}
	b := state.Schema{
		ObjectID:  "sch-b",
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "acme.retro", Fields: []state.SchemaField{mkField("retro", "create", 1, "notes", "lww")}},
		},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{a, b})

	if _, ok := rules["acme.standup"]; !ok {
		t.Errorf("expected rules installed for 'acme.standup' from the properly-derived object, got %+v", rules)
	}
	if _, ok := rules["acme.retro"]; ok {
		t.Errorf("expected no rules installed for 'acme.retro': its schema object was dropped, got %+v", rules)
	}

	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 conflict (b's id-mismatch drop), got %+v", conflicts)
	}
	c := conflicts[0]
	if c.ObjectType != "" || c.Namespace != "acme" || len(c.ObjectIDs) != 1 || c.ObjectIDs[0] != "sch-b" {
		t.Errorf("expected an id-mismatch conflict naming only sch-b, got %+v", c)
	}
}

func TestRulesFromSchemas_SchemaCannotBeRedefinedFromTheLog(t *testing.T) {
	a := state.Schema{
		ObjectID: "schema:",
		Types: []state.SchemaType{
			{Name: "schema", Fields: []state.SchemaField{mkField("schema", "create", 1, "namespace", "create-once")}},
		},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	if _, ok := rules["schema"]; ok {
		t.Fatalf("expected no rules installed for a log-defined 'schema' type, got %+v", rules["schema"])
	}
	if len(conflicts) != 1 || conflicts[0].ObjectType != "schema" {
		t.Fatalf("expected a single 'schema cannot be redefined' conflict, got %+v", conflicts)
	}
}

func TestRulesFromSchemas_InvalidRuleDroppedNotInstalled(t *testing.T) {
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "acme.standup", Fields: []state.SchemaField{
				mkField("standup", "create", 1, "summary", ""),
				mkField("standup", "create", 1, "notes", "keyed-lww"),
				mkField("standup", "create", 1, "owner", "bogus"),
			}},
		},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	if got := rules["acme.standup"]; len(got) != 0 {
		t.Fatalf("expected all three invalid rules dropped, got %+v", got)
	}
	if len(conflicts) != 3 {
		t.Fatalf("expected 3 reported invalid-rule conflicts, got %+v", conflicts)
	}

	dataOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "acme.standup", OpType: "create", OpVersion: 1,
			Body: json.RawMessage(`{"summary":"hello","notes":"hi","owner":"alice"}`),
		},
		ID: "op-1",
	}
	objState, err := state.Fold([]codec.Op{dataOp}, rules["acme.standup"])
	if err != nil {
		t.Fatalf("Fold must never see an unknown-strategy rule, got error: %v", err)
	}
	if len(objState.UnknownOps) != 1 || objState.UnknownOps[0].Commit != "op-1" {
		t.Fatalf("expected op-1 to fall through to UnknownOps, got %+v", objState)
	}
}

func TestRulesFromSchemas_InvalidOpTypeGrammarDroppedNotInstalled(t *testing.T) {
	sch := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{
				Name: "acme.standup",
				Fields: []state.SchemaField{
					mkField("standup", "Bad_Type", 1, "summary", "lww"),
					mkField("standup", strings.Repeat("a", 65), 1, "notes", "lww"),
					mkField("standup", "create", 1, "owner", "lww"),
				},
				Ops: []state.SchemaOp{
					{OpType: "UPPER", OpVersion: 1},
					{OpType: strings.Repeat("b", 65), OpVersion: 1},
					{OpType: "define-me", OpVersion: 1},
				},
			},
		},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{sch})
	got := rules["acme.standup"]
	if len(got) != 1 || got[0].Field != "owner" {
		t.Fatalf("expected only the grammatically valid field rule installed, got %+v", got)
	}
	if len(conflicts) != 4 {
		t.Fatalf("expected 4 conflicts for the two grammar-invalid define-field op_types and the two grammar-invalid define-op op_types, got %+v", conflicts)
	}
	for _, c := range conflicts {
		if c.Kind != state.SchemaConflictOpTypeUngrammatical {
			t.Errorf("conflict Kind does not name the grammar violation: %+v", c)
		}
	}

	vocabularies, _ := state.VocabulariesFromSchemas([]state.Schema{sch})
	voc, ok := vocabularies["acme.standup"]
	if !ok || !voc.Declared {
		t.Fatalf("expected acme.standup Declared, got %+v", voc)
	}
	wantOpTypes := map[codec.OpVersionKey]bool{
		{OpType: "create", OpVersion: 1}:    true,
		{OpType: "define-me", OpVersion: 1}: true,
	}
	if !reflect.DeepEqual(voc.OpTypes, wantOpTypes) {
		t.Fatalf("expected only the grammatically valid op types installed, got %+v", voc.OpTypes)
	}

	dataOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "acme.standup", OpType: "UPPER", OpVersion: 1,
			Body: json.RawMessage(`{}`),
		},
		ID: "op-1",
	}
	objState, err := state.Fold([]codec.Op{dataOp}, rules["acme.standup"])
	if err != nil {
		t.Fatalf("Fold must never see a rule for a grammar-invalid op_type, got error: %v", err)
	}
	if len(objState.UnknownOps) != 1 || objState.UnknownOps[0].Commit != "op-1" {
		t.Fatalf("expected op-1 to fall through to UnknownOps, got %+v", objState)
	}
}

func TestRulesFromSchemas_ReservedOpTypeMergeDroppedNotInstalled(t *testing.T) {
	sch := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{
				Name: "acme.standup",
				Fields: []state.SchemaField{
					{Name: "field1", OpType: "merge", OpVersion: 1, ValueType: "string", Strategy: "lww"},
					{Name: "owner", OpType: "create", OpVersion: 1, ValueType: "string", Strategy: "lww"},
				},
				Ops: []state.SchemaOp{
					{OpType: "merge", OpVersion: 1},
					{OpType: "define-me", OpVersion: 1},
				},
			},
		},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{sch})
	got := rules["acme.standup"]
	if len(got) != 1 || got[0].Field != "owner" {
		t.Fatalf("expected only the control field rule installed, got %+v", got)
	}
	if len(conflicts) != 2 {
		t.Fatalf("expected 2 conflicts for define-field merge and define-op merge, got %+v", conflicts)
	}
	for _, c := range conflicts {
		if c.Kind != state.SchemaConflictOpTypeReserved {
			t.Errorf("conflict Kind does not name the reserved op_type violation: %+v", c)
		}
	}

	vocabularies, _ := state.VocabulariesFromSchemas([]state.Schema{sch})
	voc, ok := vocabularies["acme.standup"]
	if !ok || !voc.Declared {
		t.Fatalf("expected acme.standup Declared, got %+v", voc)
	}
	wantOpTypes := map[codec.OpVersionKey]bool{
		{OpType: "create", OpVersion: 1}:    true,
		{OpType: "define-me", OpVersion: 1}: true,
	}
	if !reflect.DeepEqual(voc.OpTypes, wantOpTypes) {
		t.Fatalf("expected only non-reserved op types installed, got %+v", voc.OpTypes)
	}
}

func TestRulesFromSchemas_InvalidTargetOrKeyGrammarDroppedNotInstalled(t *testing.T) {
	sch := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{
				Name: "acme.standup",
				Fields: []state.SchemaField{
					{Name: "summary", OpType: "create", OpVersion: 1, Strategy: "lww", ValueType: "string"},
					{Name: "code", OpType: "create", OpVersion: 1, Strategy: "lww", ValueType: "string", Target: "identifier"},
					{Name: "owner", OpType: "create", OpVersion: 1, Strategy: "lww", ValueType: "string", Target: "bad-target"},
					{Name: "tags", OpType: "create", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string", Key: []string{"Bad Col"}, KeyTypes: map[string]string{"Bad Col": "string"}},

					{Name: "alpha", OpType: "set-alpha", OpVersion: 1, Strategy: "lww", ValueType: "string", Target: "shared"},
					{Name: "beta", OpType: "set-beta", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
						Key: []string{"Bad Col2"}, KeyTypes: map[string]string{"Bad Col2": "string"}, Target: "shared"},

					{Name: "gamma", OpType: "set-subject", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
						Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"}},
					{Name: "delta", OpType: "set-subject", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
						Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "string"}, Target: "bad-target2"},
				},
			},
		},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{sch})
	got := rules["acme.standup"]
	if len(got) != 4 {
		t.Fatalf("expected only the four grammatically valid fields installed, got %+v", got)
	}
	gotFields := map[string]bool{}
	for _, r := range got {
		gotFields[r.Field] = true
	}
	for _, want := range []string{"summary", "code", "alpha", "gamma"} {
		if !gotFields[want] {
			t.Fatalf("expected %s installed, got %+v", want, got)
		}
	}
	if len(conflicts) != 4 {
		t.Fatalf("expected 4 conflicts, one per bad rule (owner, tags, beta, delta), got %+v", conflicts)
	}
	for _, c := range conflicts {
		if c.Kind != state.SchemaConflictRuleInvalid {
			t.Errorf("conflict Kind does not name a dropped rule: %+v", c)
		}
	}

	dataOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "acme.standup", OpType: "create", OpVersion: 1,
			Body: json.RawMessage(`{"summary":"hi","code":"abc","owner":"alice","Bad Col":"x","tags":"y"}`),
		},
		ID: "op-1",
	}
	objState, err := state.Fold([]codec.Op{dataOp}, rules["acme.standup"])
	if err != nil {
		t.Fatalf("Fold must never see a rule for a grammar-invalid target or key column, got error: %v", err)
	}
	if got := objState.State["summary"]; got != "hi" {
		t.Fatalf("expected summary to fold normally, got %+v", got)
	}
	if got := objState.State["identifier"]; got != "abc" {
		t.Fatalf("expected code's target identifier to fold normally, got %+v", got)
	}

	alphaOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "acme.standup", OpType: "set-alpha", OpVersion: 1,
			Body: json.RawMessage(`{"alpha":"left"}`),
		},
		ID: "op-2",
	}
	gammaOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "acme.standup", OpType: "set-subject", OpVersion: 1,
			Body: json.RawMessage(`{"gamma":"urgent","subject":"person-1","delta":"ignored"}`),
		},
		ID: "op-3",
	}
	objState2, err := state.Fold([]codec.Op{alphaOp, gammaOp}, rules["acme.standup"])
	if err != nil {
		t.Fatalf("Fold on alpha/gamma's own ops: %v", err)
	}
	if len(objState2.UnknownOps) != 0 {
		t.Fatalf("expected alpha's and gamma's ops to fold normally, got unknown ops %+v", objState2.UnknownOps)
	}
	if got := objState2.State["shared"]; got != "left" {
		t.Fatalf("expected alpha's target shared to fold normally, got %+v", got)
	}
	gammaEntries, _ := objState2.State["gamma"].([]any)
	if len(gammaEntries) != 1 {
		t.Fatalf("expected gamma's keyed-lww state to fold normally, got %+v", objState2.State["gamma"])
	}
	entry, _ := gammaEntries[0].(map[string]any)
	key, _ := entry["key"].([]string)
	if len(key) != 1 || key[0] != "person-1" || entry["value"] != "urgent" {
		t.Fatalf("expected gamma's keyed-lww state to fold normally, got %+v", entry)
	}
}

func TestRulesFromSchemas_DeprecatedFieldStaysActiveForFolding(t *testing.T) {
	active := mkField("standup", "create", 1, "summary", "lww")
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.standup", Fields: []state.SchemaField{active}}},
	}
	deprecated := active
	deprecated.Deprecated = true
	b := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.standup", Fields: []state.SchemaField{deprecated}}},
	}

	rulesBefore, conflictsBefore := state.RulesFromSchemas([]state.Schema{a})
	rulesAfter, conflictsAfter := state.RulesFromSchemas([]state.Schema{b})

	if len(conflictsBefore) != 0 || len(conflictsAfter) != 0 {
		t.Fatalf("expected no conflicts either way, got before=%+v after=%+v", conflictsBefore, conflictsAfter)
	}

	got := rulesAfter["acme.standup"]
	if len(got) != 1 {
		t.Fatalf("expected the deprecated field's rule still installed, got %+v", got)
	}
	if !got[0].Deprecated {
		t.Errorf("expected Deprecated carried through onto the resolved Rule, got %+v", got[0])
	}

	dataOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "acme.standup", OpType: "create", OpVersion: 1,
			Body: json.RawMessage(`{"summary":"important"}`),
		},
		ID: "op-1",
	}

	stateBefore, err := state.Fold([]codec.Op{dataOp}, rulesBefore["acme.standup"])
	if err != nil {
		t.Fatalf("Fold before deprecation failed: %v", err)
	}
	if len(stateBefore.UnknownOps) != 0 {
		t.Fatalf("expected op-1 known before deprecation, got unknown_ops=%+v", stateBefore.UnknownOps)
	}
	if stateBefore.State["summary"] != "important" {
		t.Fatalf("expected summary=%q before deprecation, got %+v", "important", stateBefore.State)
	}

	stateAfter, err := state.Fold([]codec.Op{dataOp}, rulesAfter["acme.standup"])
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
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{
			mkField("widget", "widget-op", 1, "value", "lww"),
			mkField("widget", "widget-op", 2, "value", "lww"),
		}}},
	}
	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %+v", conflicts)
	}
	if got := rules["acme.widget"]; len(got) != 2 {
		t.Fatalf("expected both version-1 and version-2 rules installed under the shared target, got %+v", got)
	}
}

func TestRulesFromSchemas_VersionBumpNewStrategySameTargetRejected(t *testing.T) {
	v1 := mkField("widget", "widget-op", 1, "value", "lww")
	v2 := mkField("widget", "widget-op", 2, "value", "set-union")
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{v1, v2}}},
	}
	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	if got := rules["acme.widget"]; len(got) != 0 {
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
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{v1, v2}}},
	}
	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts once the version bump declares a distinct target, got %+v", conflicts)
	}
	if got := rules["acme.widget"]; len(got) != 2 {
		t.Fatalf("expected both rules installed, got %+v", got)
	}
}

func TestRulesFromSchemas_CrossOpTypeTargetReuseWithDifferentValueTypeRejected(t *testing.T) {
	v1 := mkField("widget", "create", 1, "owner", "lww")
	v1.ValueType = "person-ref"
	v2 := mkField("widget", "assign", 1, "owner", "lww")
	v2.ValueType = "object-ref"
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{v1, v2}}},
	}
	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	if got := rules["acme.widget"]; len(got) != 0 {
		t.Fatalf("expected no rules installed for the target, got %+v", got)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict reporting the withheld target (same strategy, different value_type, cross op_type), got %+v", conflicts)
	}
}

func TestRulesFromSchemas_VersionBumpValueTypeOnlyOK(t *testing.T) {
	v1 := mkField("widget", "widget-op", 1, "value", "lww")
	v1.ValueType = "string"
	v2 := mkField("widget", "widget-op", 2, "value", "lww")
	v2.ValueType = "int"
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{v1, v2}}},
	}
	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts for a version bump changing only value_type, got %+v", conflicts)
	}
	if got := rules["acme.widget"]; len(got) != 2 {
		t.Fatalf("expected both version-1 and version-2 rules installed, got %+v", got)
	}
}

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
			Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "acme.widget", OpType: "configure", OpVersion: 1, Body: json.RawMessage(`{"mode":"legacy-status"}`)},
			ID:       "configure-1",
		},
		{
			Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "acme.widget", OpType: "configure", OpVersion: 2, Body: json.RawMessage(`{"mode":7}`)},
			ID:       "configure-2",
		},
		{
			Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "acme.widget", OpType: "reset", OpVersion: 1, Body: json.RawMessage(`{"value":"legacy-status"}`)},
			ID:       "reset-1",
		},
	}

	r := rand.New(rand.NewSource(211))
	var wantRules map[string][]state.Rule
	var wantConflicts []state.SchemaConflict
	var wantState state.ObjectState

	for i := 0; i < 30; i++ {
		fields := append([]state.SchemaField(nil), allFields...)
		r.Shuffle(len(fields), func(a, b int) { fields[a], fields[b] = fields[b], fields[a] })

		schemas := []state.Schema{{
			ObjectID:  "schema:acme",
			Namespace: "acme",
			Types:     []state.SchemaType{{Name: "acme.widget", Fields: fields}},
		}}
		r.Shuffle(len(schemas), func(a, b int) { schemas[a], schemas[b] = schemas[b], schemas[a] })

		rules, conflicts := state.RulesFromSchemas(schemas)
		if got := rules["acme.widget"]; len(got) != 0 {
			t.Fatalf("permutation #%d: expected the whole target withheld, got %+v", i, got)
		}
		if len(conflicts) != 1 {
			t.Fatalf("permutation #%d: expected exactly 1 conflict, got %+v", i, conflicts)
		}

		objState, err := state.Fold(dataOps, rules["acme.widget"])
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

func TestRulesFromSchemas_VersionBumpKeyArityDisagreementWithholdsTarget(t *testing.T) {
	titleField := mkField("gadget", "create", 1, "title", "lww")

	verdictV1 := mkField("gadget", "approval", 1, "verdict", "keyed-lww")
	verdictV1.ValueType = "enum"
	verdictV1.Enum = []string{"approve", "block"}
	verdictV1.Key = []string{"subject", "revision"}
	verdictV1.KeyTypes = map[string]string{"subject": "string", "revision": "string"}

	verdictV2 := mkField("gadget", "approval", 2, "verdict", "keyed-lww")
	verdictV2.ValueType = "enum"
	verdictV2.Enum = []string{"approve", "block"}
	verdictV2.Key = []string{"subject"}
	verdictV2.KeyTypes = map[string]string{"subject": "string"}

	allFields := []state.SchemaField{titleField, verdictV1, verdictV2}

	dataOps := []codec.Op{
		{
			Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "acme.gadget", OpType: "create", OpVersion: 1, Body: json.RawMessage(`{"title":"T"}`)},
			ID:       "create-1",
		},
		{
			Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "acme.gadget", OpType: "approval", OpVersion: 1, Body: json.RawMessage(`{"subject":"user:alice","revision":"aaa","verdict":"approve"}`)},
			ID:       "approval-v1",
		},
		{
			Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "acme.gadget", OpType: "approval", OpVersion: 2, Body: json.RawMessage(`{"subject":"user:alice","verdict":"block"}`)},
			ID:       "approval-v2",
		},
	}

	r := rand.New(rand.NewSource(234))
	var wantRules map[string][]state.Rule
	var wantConflicts []state.SchemaConflict
	var wantState state.ObjectState

	for i := 0; i < 20; i++ {
		fields := append([]state.SchemaField(nil), allFields...)
		r.Shuffle(len(fields), func(a, b int) { fields[a], fields[b] = fields[b], fields[a] })

		schemas := []state.Schema{{
			ObjectID:  "schema:acme",
			Namespace: "acme",
			Types:     []state.SchemaType{{Name: "acme.gadget", Fields: fields}},
		}}

		rules, conflicts := state.RulesFromSchemas(schemas)
		got := rules["acme.gadget"]
		if len(got) != 1 {
			t.Fatalf("permutation #%d: expected exactly 1 rule installed (title's), got %+v", i, got)
		}
		if got[0].TargetKey() != "title" {
			t.Fatalf("permutation #%d: expected the surviving rule to be \"title\", got %+v", i, got[0])
		}
		if len(conflicts) != 1 {
			t.Fatalf("permutation #%d: expected exactly 1 conflict, got %+v", i, conflicts)
		}

		objState, err := state.Fold(dataOps, rules["acme.gadget"])
		if err != nil {
			t.Fatalf("permutation #%d: Fold: %v", i, err)
		}
		if objState.State["title"] != "T" {
			t.Fatalf("permutation #%d: expected title to fold normally, got state %+v", i, objState.State)
		}
		if len(objState.UnknownOps) != 2 {
			t.Fatalf("permutation #%d: expected both approval ops to fall through as unknown, got %+v", i, objState.UnknownOps)
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

func TestRulesFromSchemas_SharedKeyColumnDisagreementIsOrderIndependent(t *testing.T) {
	aa := state.SchemaField{
		Name: "aa", OpType: "approve", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
		Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"},
	}
	mm := state.SchemaField{
		Name: "mm", OpType: "approve", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
		Key: []string{"subject", "phase"}, KeyTypes: map[string]string{"subject": "string", "phase": "string"},
	}
	zz := state.SchemaField{
		Name: "zz", OpType: "approve", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
		Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"},
	}
	allFields := []state.SchemaField{aa, mm, zz}

	dataOps := []codec.Op{
		{
			Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "acme.widget", OpType: "approve", OpVersion: 1, Body: json.RawMessage(`{"aa":"yes","subject":"p-1"}`)},
			ID:       "approve-aa",
		},
		{
			Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "acme.widget", OpType: "approve", OpVersion: 1, Body: json.RawMessage(`{"mm":"7","subject":"p-1","phase":"beta"}`)},
			ID:       "approve-mm",
		},
		{
			Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "acme.widget", OpType: "approve", OpVersion: 1, Body: json.RawMessage(`{"zz":"no","subject":"p-1"}`)},
			ID:       "approve-zz",
		},
	}

	r := rand.New(rand.NewSource(214))
	var wantRules map[string][]state.Rule
	var wantConflicts []state.SchemaConflict
	var wantState state.ObjectState

	for i := 0; i < 30; i++ {
		fields := append([]state.SchemaField(nil), allFields...)
		r.Shuffle(len(fields), func(a, b int) { fields[a], fields[b] = fields[b], fields[a] })

		schemas := []state.Schema{{
			ObjectID:  "schema:acme",
			Namespace: "acme",
			Types:     []state.SchemaType{{Name: "acme.widget", Fields: fields}},
		}}
		r.Shuffle(len(schemas), func(a, b int) { schemas[a], schemas[b] = schemas[b], schemas[a] })

		rules, conflicts := state.RulesFromSchemas(schemas)
		if got := rules["acme.widget"]; len(got) != 0 {
			t.Fatalf("permutation #%d: expected every rule bound to the disagreeing key column withheld, got %+v", i, got)
		}
		if len(conflicts) != 1 {
			t.Fatalf("permutation #%d: expected exactly 1 conflict, got %+v", i, conflicts)
		}

		objState, err := state.Fold(dataOps, rules["acme.widget"])
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

func schemaFromDefineFields(t *testing.T, defs []map[string]any) state.Schema {
	t.Helper()
	var ops []codec.Op
	base := time.Unix(0, 0).UTC()
	add := func(opType string, body map[string]any) {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s body: %v", opType, err)
		}
		i := len(ops)
		id := fmt.Sprintf("synthetic-%06d", i)
		var parents []string
		if i > 0 {
			parents = []string{ops[i-1].ID}
		}
		ops = append(ops, codec.Op{
			Envelope: codec.Envelope{ObjectID: "schema:acme", ObjectType: "schema", OpType: opType, OpVersion: 1, Body: raw},
			ID:       id,
			Parents:  parents,
			Author:   codec.Identity{When: base.Add(time.Duration(i) * time.Second)},
		})
	}
	add("create", map[string]any{"namespace": "acme"})
	add("define-type", map[string]any{"type": "acme.widget"})
	add("define-op", map[string]any{"type": "acme.widget", "op_type": "approve", "op_version": "1"})
	for _, d := range defs {
		add("define-field", d)
	}
	sch, err := state.FoldSchema(ops)
	if err != nil {
		t.Fatalf("FoldSchema: %v", err)
	}
	return sch
}

func TestRulesFromSchemas_KeyColumnVerdictDoesNotTurnOnFieldNames(t *testing.T) {
	mm := map[string]any{
		"type": "acme.widget", "op_type": "approve", "op_version": "1", "field": "mm",
		"value_type": "string", "strategy": "keyed-lww",
		"key": []string{"subject", "phase"}, "key_types": map[string]string{"subject": "string", "phase": "string"},
	}
	dissenter := func(name string) map[string]any {
		return map[string]any{
			"type": "acme.widget", "op_type": "approve", "op_version": "1", "field": name,
			"value_type": "string", "strategy": "keyed-lww",
			"key": []string{"subject"}, "key_types": map[string]string{"subject": "person-ref"},
		}
	}

	for _, name := range []string{"aa", "zz"} {
		t.Run("dissenting field named "+name, func(t *testing.T) {
			schemas := []state.Schema{schemaFromDefineFields(t, []map[string]any{dissenter(name), mm})}
			rules, conflicts := state.RulesFromSchemas(schemas)
			if got := rules["acme.widget"]; len(got) != 0 {
				t.Fatalf("expected both rules bound to the disagreeing key column withheld, got %+v", got)
			}
			if len(conflicts) != 1 {
				t.Fatalf("expected exactly 1 conflict, got %+v", conflicts)
			}
			op := codec.Op{
				Envelope: codec.Envelope{ObjectID: "obj-1", ObjectType: "acme.widget", OpType: "approve", OpVersion: 1, Body: json.RawMessage(`{"` + name + `":"yes","subject":"p-1"}`)},
				ID:       "approve-1",
			}
			objState, err := state.Fold([]codec.Op{op}, rules["acme.widget"])
			if err != nil {
				t.Fatalf("Fold: %v", err)
			}
			if len(objState.UnknownOps) != 1 {
				t.Fatalf("expected the op to fall through as unknown, got %+v", objState.UnknownOps)
			}
		})
	}
}

func TestRulesFromSchemas_DualRoleTombstoneFieldRefused(t *testing.T) {
	flag := state.SchemaField{
		Name: "flag", OpType: "approve", OpVersion: 1, Strategy: "tombstone", ValueType: "bool",
	}
	verdict := state.SchemaField{
		Name: "verdict", OpType: "approve", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
		Key: []string{"flag"}, KeyTypes: map[string]string{"flag": "bool"},
	}

	for _, tc := range []struct {
		name   string
		fields []state.SchemaField
	}{
		{name: "tombstone declared first", fields: []state.SchemaField{flag, verdict}},
		{name: "key column declared first", fields: []state.SchemaField{verdict, flag}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := state.Schema{
				ObjectID:  "schema:acme",
				Namespace: "acme",
				Types:     []state.SchemaType{{Name: "acme.widget", Fields: tc.fields}},
			}
			rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
			if got := rules["acme.widget"]; len(got) != 0 {
				t.Fatalf("expected both rules withheld, got %+v", got)
			}
			if len(conflicts) != 1 {
				t.Fatalf("expected exactly 1 conflict naming the unsatisfiable combination, got %+v", conflicts)
			}
			if conflicts[0].Kind != state.SchemaConflictKeyColumnDisagreement {
				t.Fatalf("conflict Kind should name the key-column disagreement, got %q", conflicts[0].Kind)
			}
		})
	}
}

func TestRulesFromSchemas_SharedKeyColumnAgreementOK(t *testing.T) {
	verdict := state.SchemaField{
		Name: "verdict", OpType: "approve", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
		Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"},
	}
	score := state.SchemaField{
		Name: "score", OpType: "approve", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
		Key: []string{"subject", "phase"}, KeyTypes: map[string]string{"subject": "person-ref", "phase": "string"},
	}
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{verdict, score}}},
	}
	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts when the shared key column agrees, got %+v", conflicts)
	}
	if got := rules["acme.widget"]; len(got) != 2 {
		t.Fatalf("expected both fields installed, got %+v", got)
	}
}

func TestRulesFromSchemas_UnrecognizedValueTypeDemotedNotWithheld(t *testing.T) {
	f := mkField("widget", "set-ident", 1, "ident", "lww")
	f.ValueType = "x-uuid"
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{f}}},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	got := rules["acme.widget"]
	if len(got) != 1 {
		t.Fatalf("expected the rule installed (demoted, not withheld), got %+v", got)
	}
	if got[0].ValueType != "" {
		t.Fatalf("expected ValueType demoted to \"\", got %q", got[0].ValueType)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 warning conflict, got %+v", conflicts)
	}
	if !strings.Contains(conflicts[0].Reason, `value_type "x-uuid"`) || !strings.Contains(conflicts[0].Reason, "installed untyped") {
		t.Fatalf("conflict reason should name the demoted value_type and say it was installed untyped, got %q", conflicts[0].Reason)
	}

	vocabularies, vocConflicts := state.VocabulariesFromSchemas([]state.Schema{a})
	if !reflect.DeepEqual(vocConflicts, conflicts) {
		t.Fatalf("VocabulariesFromSchemas conflicts = %+v, want identical to RulesFromSchemas' %+v", vocConflicts, conflicts)
	}
	vocFields := vocabularies["acme.widget"].Fields[codec.OpVersionKey{OpType: "set-ident", OpVersion: 1}]
	if len(vocFields) != 1 || vocFields[0].ValueType != "x-uuid" {
		t.Fatalf("expected Vocabulary.Fields to keep the raw, undemoted value_type \"x-uuid\", got %+v", vocFields)
	}
}

func TestRulesFromSchemas_PerPositionDemotion(t *testing.T) {
	score := state.SchemaField{
		Name: "score", OpType: "set-score", OpVersion: 1, Strategy: "keyed-lww",
		ValueType: "person-ref", Key: []string{"who"}, KeyTypes: map[string]string{"who": "x-handle"},
	}
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{score}}},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	got := rules["acme.widget"]
	if len(got) != 1 {
		t.Fatalf("expected the rule installed, got %+v", got)
	}
	if got[0].ValueType != "person-ref" {
		t.Fatalf("expected the rule's own value_type to stay person-ref (recognized), got %q", got[0].ValueType)
	}
	if got[0].KeyTypes["who"] != "" {
		t.Fatalf("expected key_types[who] demoted to \"\", got %q", got[0].KeyTypes["who"])
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 warning conflict naming only the key_types entry, got %+v", conflicts)
	}
	if strings.Contains(conflicts[0].Reason, `value_type "person-ref"`) {
		t.Fatalf("conflict must not name the recognized value_type as demoted: %q", conflicts[0].Reason)
	}
	if !strings.Contains(conflicts[0].Reason, `key_types["who"] = "x-handle"`) {
		t.Fatalf("conflict should name the demoted key_types entry, got %q", conflicts[0].Reason)
	}
}

func TestRulesFromSchemas_SharedTargetBothUnrecognizedSameTypeInstalled(t *testing.T) {
	v1 := mkField("widget", "create", 1, "owner", "lww")
	v1.ValueType = "x-uuid"
	v2 := mkField("widget", "assign", 1, "owner", "lww")
	v2.ValueType = "x-uuid"
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{v1, v2}}},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	got := rules["acme.widget"]
	if len(got) != 2 {
		t.Fatalf("expected both same-unrecognized-type rules installed, got %+v", got)
	}
	for _, r := range got {
		if r.ValueType != "" {
			t.Errorf("expected both rules demoted, got ValueType %q on %+v", r.ValueType, r)
		}
	}
	if len(conflicts) != 2 {
		t.Fatalf("expected 2 warning conflicts, one per demoted rule, got %+v", conflicts)
	}
}

func TestRulesFromSchemas_SharedTargetDifferentUnrecognizedTypesWithheldNoWarning(t *testing.T) {
	v1 := mkField("widget", "create", 1, "owner", "lww")
	v1.ValueType = "x-uuid"
	v2 := mkField("widget", "assign", 1, "owner", "lww")
	v2.ValueType = "x-other"
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{v1, v2}}},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	if got := rules["acme.widget"]; len(got) != 0 {
		t.Fatalf("expected both rules withheld (disagreeing raw value_type), got %+v", got)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 withhold conflict and no separate demotion warning, got %+v", conflicts)
	}
	if conflicts[0].Kind == state.SchemaConflictValueTypeUnknown {
		t.Fatalf("a withheld rule must never also be reported as installed/demoted: %+v", conflicts[0])
	}
}

func TestRulesFromSchemas_InvalidAndUnrecognizedRuleGetsOnlyDropConflict(t *testing.T) {
	f := mkField("widget", "set-ident", 1, "ident", "bogus-strategy")
	f.ValueType = "x-uuid"
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{f}}},
	}

	rules, conflicts := state.RulesFromSchemas([]state.Schema{a})
	if got := rules["acme.widget"]; len(got) != 0 {
		t.Fatalf("expected the invalid rule dropped, got %+v", got)
	}
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 drop conflict, got %+v", conflicts)
	}
	if conflicts[0].Kind != state.SchemaConflictRuleInvalid {
		t.Fatalf("expected the pass-1 drop kind rule-invalid, got %q", conflicts[0].Kind)
	}
}

func TestRulesFromSchemas_EveryReachableKindReachedOnce(t *testing.T) {
	unrecognizedIdent := mkField("widget", "set-ident", 1, "ident", "lww")
	unrecognizedIdent.ValueType = "x-uuid"

	for _, tc := range []struct {
		name    string
		schemas []state.Schema
		want    state.SchemaConflictKind
	}{
		{
			name: "namespace-ungrammatical",
			schemas: []state.Schema{{
				ObjectID:  "schema:Bad Namespace",
				Namespace: "Bad Namespace",
			}},
			want: state.SchemaConflictNamespaceUngrammatical,
		},
		{
			name: "object-id-mismatch",
			schemas: []state.Schema{{
				ObjectID:  "rogue-object",
				Namespace: "acme",
			}},
			want: state.SchemaConflictObjectIDMismatch,
		},
		{
			name: "schema-redefined",
			schemas: []state.Schema{{
				ObjectID:  "schema:acme",
				Namespace: "acme",
				Types:     []state.SchemaType{{Name: "schema"}},
			}},
			want: state.SchemaConflictSchemaRedefined,
		},
		{
			name: "type-ungrammatical",
			schemas: []state.Schema{{
				ObjectID:  "schema:acme",
				Namespace: "acme",
				Types:     []state.SchemaType{{Name: "acme.Foo"}},
			}},
			want: state.SchemaConflictTypeUngrammatical,
		},
		{
			name: "type-unqualified",
			schemas: []state.Schema{{
				ObjectID:  "schema:acme",
				Namespace: "acme",
				Types:     []state.SchemaType{{Name: "standup"}},
			}},
			want: state.SchemaConflictTypeUnqualified,
		},
		{
			name: "op-type-ungrammatical",
			schemas: []state.Schema{{
				ObjectID:  "schema:acme",
				Namespace: "acme",
				Types: []state.SchemaType{{
					Name:   "acme.widget",
					Fields: []state.SchemaField{mkField("widget", "Bad_Type", 1, "summary", "lww")},
				}},
			}},
			want: state.SchemaConflictOpTypeUngrammatical,
		},
		{
			name: "rule-invalid",
			schemas: []state.Schema{{
				ObjectID:  "schema:acme",
				Namespace: "acme",
				Types: []state.SchemaType{{
					Name:   "acme.widget",
					Fields: []state.SchemaField{mkField("widget", "set-status", 1, "status", "bogus-strategy")},
				}},
			}},
			want: state.SchemaConflictRuleInvalid,
		},
		{
			name: "key-column-disagreement",
			schemas: []state.Schema{{
				ObjectID:  "schema:acme",
				Namespace: "acme",
				Types: []state.SchemaType{{
					Name: "acme.widget",
					Fields: []state.SchemaField{
						{Name: "aa", OpType: "approve", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
							Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"}},
						{Name: "mm", OpType: "approve", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
							Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "string"}},
					},
				}},
			}},
			want: state.SchemaConflictKeyColumnDisagreement,
		},
		{
			name: "target-disagreement",
			schemas: []state.Schema{{
				ObjectID:  "schema:acme",
				Namespace: "acme",
				Types: []state.SchemaType{{
					Name: "acme.gadget",
					Fields: []state.SchemaField{
						{Name: "mode", OpType: "configure", OpVersion: 1, Strategy: "lww", ValueType: "string"},
						{Name: "mode", OpType: "configure", OpVersion: 2, Strategy: "set-union", ValueType: "string"},
					},
				}},
			}},
			want: state.SchemaConflictTargetDisagreement,
		},
		{
			name: "value-type-unknown",
			schemas: []state.Schema{{
				ObjectID:  "schema:acme",
				Namespace: "acme",
				Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{unrecognizedIdent}}},
			}},
			want: state.SchemaConflictValueTypeUnknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, conflicts := state.RulesFromSchemas(tc.schemas)
			if len(conflicts) != 1 {
				t.Fatalf("expected exactly 1 conflict, got %+v", conflicts)
			}
			if conflicts[0].Kind != tc.want {
				t.Fatalf("expected Kind %q, got %q (full conflict: %+v)", tc.want, conflicts[0].Kind, conflicts[0])
			}
		})
	}
}
