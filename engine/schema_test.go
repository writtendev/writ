package writ_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	writ "github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/codec/canonicaljson"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/identity"
	"github.com/writtendev/writ/internal/schemasrc"
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

			_, conflicts := writ.RulesFromSchemas([]state.Schema{sch})
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

// TestRulesFromSchemas_ObjectTypeCollisionInstallsNoRules pinned, before
// WRIT-254, the collision that remained reachable after WRIT-217: two
// schema objects sharing one namespace ("acme") that both declare a type
// named "standup" both bind the identical qualified wire type
// "acme.standup" and contend for it. WRIT-254 change 2 closes that
// collision a layer earlier: neither "sch-a" nor "sch-b" is the derived
// id "schema:acme" their shared namespace requires
// (spec/identifiers.md's schema carve-out), so resolveSchemaTypes drops
// both wholesale before their types are ever compared to each other —
// the object_type-collision branch this test used to exercise is
// unreachable for a namespace-qualified type now (see
// engine/schema.go's own comment on that branch). The name stays
// accurate — no rules install for "acme.standup" either way — but the
// mechanism and the conflict shape changed: two id-mismatch conflicts,
// one per object, instead of one object_type-tagged conflict naming
// both. (Two different namespaces declaring the same bare type name
// still never collide at all — see
// TestRulesFromSchemas_DifferentNamespacesSameBareTypeBothInstall.)
func TestRulesFromSchemas_ObjectTypeCollisionInstallsNoRules(t *testing.T) {
	a := state.Schema{
		ObjectID:  "sch-a", // deliberately not "schema:acme" — see doc comment
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "acme.standup", Fields: []state.SchemaField{mkField("standup", "create", 1, "summary", "lww")}},
		},
	}
	b := state.Schema{
		ObjectID:  "sch-b", // deliberately not "schema:acme" either
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "acme.standup", Fields: []state.SchemaField{mkField("standup", "create", 1, "notes", "lww")}},
		},
	}

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{b, a})

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

	// §7.1 / FC-1 / FC-12: withholding rules for acme.standup must never
	// surface as a fold error for the ops that type's own data writes.
	// Fold(dataOps, rules["acme.standup"]) — rules["acme.standup"] absent,
	// same as an unresolvable object_type — must return a nil error with
	// every op quarantined as unknown, exactly the absent-schema path.
	dataOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "acme.standup", OpType: "create", OpVersion: 1,
			Body: json.RawMessage(`{"summary":"hello"}`),
		},
		ID: "op-1",
	}
	objState, err := writ.Fold([]codec.Op{dataOp}, rules["acme.standup"])
	if err != nil {
		t.Fatalf("Fold on the withheld object_type must not error, got: %v", err)
	}
	if len(objState.UnknownOps) != 1 || objState.UnknownOps[0].Commit != "op-1" {
		t.Fatalf("expected op-1 to fall through to UnknownOps, got %+v", objState)
	}
}

// TestRulesFromSchemas_DifferentNamespacesSameBareTypeBothInstall is
// WRIT-217's central acceptance test at the resolver level: two schema
// objects, "acme" and "bigco", each declaring a type whose bare source
// name is "code-review", qualify to two distinct wire types
// ("acme.code-review", "bigco.code-review") and never contend — both
// install cleanly, with zero conflicts. Before this ticket, both would
// have bound the identical bare object_type "code-review" and
// TestRulesFromSchemas_ObjectTypeCollisionInstallsNoRules's withholding
// would have applied to both; qualification is what makes independent
// schema packages installable side by side (the distribution model this
// restructuring exists to enable).
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

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{bigco, acme})
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

// TestRulesFromSchemas_UnqualifiedConsumerTypeDroppedNotInstalled pins
// WRIT-217's answer to "is the qualified form ever optional?": no. A
// declared type whose name does not carry its own schema's namespace
// prefix — bare, qualified under someone else's namespace, or carrying
// more than one dot — is dropped and reported as a conflict, exactly like
// any other invalid declaration, never installed. This is the
// resolver-level gate that actually closes the global object_type
// namespace: the envelope grammar alone cannot enforce it
// (spec/op-envelope.md has no notion of namespace), so this is the one
// place that does.
func TestRulesFromSchemas_UnqualifiedConsumerTypeDroppedNotInstalled(t *testing.T) {
	// One schema object, one namespace, three bad declarations — kept to a
	// single, correctly-derived ObjectID so the only conflicts reachable
	// are the three namespace-qualification failures under test, with no
	// id-mismatch drop (TestRulesFromSchemas_NonDerivedIDObjectDroppedNotInstalled)
	// muddying the count.
	sch := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "standup", Fields: []state.SchemaField{mkField("standup", "create", 1, "summary", "lww")}},
			{Name: "bigco.retro", Fields: []state.SchemaField{mkField("retro", "create", 1, "notes", "lww")}},
			{Name: "acme.foo.bar", Fields: []state.SchemaField{mkField("foo.bar", "create", 1, "title", "lww")}},
		},
	}

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{sch})
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

	reasons := make(map[string]string, len(conflicts))
	for _, c := range conflicts {
		reasons[c.ObjectType] = c.Reason
	}
	// "standup" and "bigco.retro" are grammar-legal object types — each is
	// a single, correctly-shaped segment or two — so both are caught only
	// by the qualification check (§2, §6.4), not by WRIT-253's newer
	// object_type grammar gate.
	if !strings.Contains(reasons["standup"], "not qualified with this schema object's own namespace") {
		t.Errorf("standup's conflict reason does not name the namespace-qualification failure: %q", reasons["standup"])
	}
	if !strings.Contains(reasons["bigco.retro"], "not qualified with this schema object's own namespace") {
		t.Errorf("bigco.retro's conflict reason does not name the namespace-qualification failure: %q", reasons["bigco.retro"])
	}
	// "acme.foo.bar" carries two dots, which the object_type grammar
	// (spec/op-envelope.md: at most one) already refuses on its own —
	// WRIT-253's grammar gate runs before the qualification check and
	// catches it first, so this one is reported as an invalid object
	// type instead. Both checks would have dropped it either way.
	if !strings.Contains(reasons["acme.foo.bar"], "not a valid object type") {
		t.Errorf("acme.foo.bar's conflict reason does not name the object_type grammar failure: %q", reasons["acme.foo.bar"])
	}
}

// TestRulesFromSchemas_UngrammaticalDeclarationDroppedNotInstalled pins
// WRIT-253's resolver gate: nothing upstream of resolveSchemaTypes checks
// a schema op's declared "type" or a schema object's own "namespace"
// against the object_type/namespace grammar, so a hand-crafted define-type
// (or a peer that bypassed producer validation) is otherwise free to
// declare bytes that break out of a SQL string literal (engine/projection)
// or a shell word (cmd/writ completion, WRIT-250) once installed. Each
// case is exactly one of WRIT-253's own repro shapes, kept to a single
// schema object with one well-formed sibling type declared alongside the
// hostile one, so the same test also pins that a malformed declaration
// never takes a legitimate sibling down with it — except when the
// grammar failure is the schema object's own namespace: there is no
// sibling to save, since every type it declares is unqualifiable under
// an ungrammatical namespace (the same reasoning the namespace-grammar
// gate itself documents).
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

			rules, conflicts := writ.RulesFromSchemas([]state.Schema{sch})

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

// writeForeignSchemaOp appends a raw "schema" op commit directly to
// refs/writ/<writerID>/schema in dir's repository, bypassing
// engine/codec.BuildCommit entirely -- and with it the bootstrap JSON
// Schema validation dag.Store.Append always runs for object_type "schema"
// (validateAgainstBootstrap), which enforces spec/schemas/schema-ops.schema.json's
// type_name pattern on a define-type's own "type" field. A conforming
// Store.ApplySchema call can therefore never carry an ungrammatical
// declared type past the producer boundary at all: the scenario WRIT-253's
// resolver gate defends against is "nothing on the read path checks a
// schema op's declared type... against the grammar this section requires
// of them" (spec/schema-ops.md §2) -- a hand-crafted commit, or a
// non-conforming peer, that never went through a conforming producer in
// the first place. Mirrors cmd/writ/textsafe_render_test.go's
// writeForeignOp, which does the same thing for a hostile person-ref
// value, for the identical reason.
//
// parent is the previous op's commit hash in this chain (empty for the
// first op), and seq spaces out each commit's author timestamp so the
// causal chain and total order agree unambiguously. The new commit's hash
// is returned so the caller can chain the next op onto it.
func writeForeignSchemaOp(t *testing.T, dir, writerID, parent, objectID, opType string, body map[string]any, seq int) string {
	t.Helper()

	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("opening repo at %s: %v", dir, err)
	}

	raw, err := json.Marshal(map[string]any{
		"object_id":   objectID,
		"object_type": "schema",
		"op_type":     opType,
		"op_version":  1,
		"body":        body,
	})
	if err != nil {
		t.Fatalf("marshal op payload: %v", err)
	}
	canon, err := canonicaljson.Marshal(raw)
	if err != nil {
		t.Fatalf("canonicalize op payload: %v", err)
	}

	when := time.Date(2026, 3, 1, 0, 0, seq, 0, time.UTC)
	who := codec.Identity{Name: "Foreign Client", Email: "foreign@example.com", When: when}
	var parents []string
	if parent != "" {
		parents = []string{parent}
	}
	commit := &codec.Commit{
		Parents:   parents,
		Author:    who,
		Committer: who,
		Message:   fmt.Sprintf("writ: %s schema/%s\n", opType, objectID),
		Tree:      []codec.TreeEntry{{Name: "op.json", Mode: "100644", Data: canon}},
	}

	hash, err := codec.WriteCommit(context.Background(), repo.Storer, commit, nil)
	if err != nil {
		t.Fatalf("writing foreign schema commit: %v", err)
	}

	refName := plumbing.ReferenceName(fmt.Sprintf("refs/writ/%s/schema", writerID))
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, hash)); err != nil {
		t.Fatalf("setting ref %s: %v", refName, err)
	}
	return hash.String()
}

// TestStoreHostileDeclaredTypeOmittedAndProjectionIntact checks that hostile declared types never reach Types() or break object listings.
func TestStoreHostileDeclaredTypeOmittedAndProjectionIntact(t *testing.T) {
	store, ctx, dir := openStoreWithCoreSchema(t)

	widgetID, err := store.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Legitimate Widget"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(widget) failed: %v", err)
	}
	liveNoteID, err := store.Objects.Create(ctx, "acme.note", writ.NewOp{
		Type: "create",
		Fields: map[string]any{
			"text":    "a live note",
			"subject": map[string]string{"object_type": "acme.widget", "object_id": widgetID},
		},
	})
	if err != nil {
		t.Fatalf("Objects.Create(live note) failed: %v", err)
	}
	deletedNoteID, err := store.Objects.Create(ctx, "acme.note", writ.NewOp{
		Type: "create",
		Fields: map[string]any{
			"text":    "a note about to be soft-deleted",
			"subject": map[string]string{"object_type": "acme.widget", "object_id": widgetID},
		},
	})
	if err != nil {
		t.Fatalf("Objects.Create(deleted note) failed: %v", err)
	}
	if err := store.Objects.Apply(ctx, deletedNoteID, writ.NewOp{Type: "delete"}); err != nil {
		t.Fatalf("Objects.Apply(delete) failed: %v", err)
	}

	const (
		foreignWriterID = "fedcba9876543210"
		hostileLimit    = `a') OR 1 --`
		hostileDelete   = `acme.x'); DELETE FROM objects; --`
		// hostileNamespace is WRIT-253's own repro shape at the
		// namespace level, not just the type level: a schema object
		// whose namespace itself carries SQL break-out syntax, and
		// hostileNSType is a type declared under it.
		hostileNamespace = `a') OR 1 --`
		hostileNSType    = hostileNamespace + `.z`
	)

	p := writeForeignSchemaOp(t, dir, foreignWriterID, "", "sch-hostile", "create", map[string]any{"namespace": "acme"}, 0)
	p = writeForeignSchemaOp(t, dir, foreignWriterID, p, "sch-hostile", "define-type", map[string]any{"type": hostileLimit}, 1)
	p = writeForeignSchemaOp(t, dir, foreignWriterID, p, "sch-hostile", "define-op", map[string]any{"type": hostileLimit, "op_type": "archive", "op_version": "1"}, 2)
	p = writeForeignSchemaOp(t, dir, foreignWriterID, p, "sch-hostile", "define-field", map[string]any{
		"type": hostileLimit, "op_type": "archive", "op_version": "1",
		"field": "archived", "value_type": "bool", "strategy": "tombstone",
	}, 3)
	p = writeForeignSchemaOp(t, dir, foreignWriterID, p, "sch-hostile", "define-type", map[string]any{"type": hostileDelete}, 4)
	p = writeForeignSchemaOp(t, dir, foreignWriterID, p, "sch-hostile", "define-op", map[string]any{"type": hostileDelete, "op_type": "archive", "op_version": "1"}, 5)
	p = writeForeignSchemaOp(t, dir, foreignWriterID, p, "sch-hostile", "define-field", map[string]any{
		"type": hostileDelete, "op_type": "archive", "op_version": "1",
		"field": "archived", "value_type": "bool", "strategy": "tombstone",
	}, 6)

	p = writeForeignSchemaOp(t, dir, foreignWriterID, p, "sch-hostile-ns", "create", map[string]any{"namespace": hostileNamespace}, 7)
	p = writeForeignSchemaOp(t, dir, foreignWriterID, p, "sch-hostile-ns", "define-type", map[string]any{"type": hostileNSType}, 8)
	p = writeForeignSchemaOp(t, dir, foreignWriterID, p, "sch-hostile-ns", "define-op", map[string]any{"type": hostileNSType, "op_type": "archive", "op_version": "1"}, 9)
	_ = writeForeignSchemaOp(t, dir, foreignWriterID, p, "sch-hostile-ns", "define-field", map[string]any{
		"type": hostileNSType, "op_type": "archive", "op_version": "1",
		"field": "archived", "value_type": "bool", "strategy": "tombstone",
	}, 10)

	if _, err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh (with hostile declarations in the log) failed: %v", err)
	}

	types, err := store.Types(ctx)
	if err != nil {
		t.Fatalf("Store.Types failed: %v", err)
	}
	for _, typ := range types {
		if typ.Name == hostileLimit || typ.Name == hostileDelete || typ.Name == hostileNSType {
			t.Errorf("Store.Types installed the hostile declared type %q: %+v", typ.Name, typ)
		}
	}

	// No Type filter on any of the three listings below: filtering to
	// only the legitimate types (as an earlier revision of this test
	// did) makes objectsNotDeletedClause's restrictTypes argument
	// exclude every hostile type's clause before the query is even
	// built (engine/projection/query.go) -- these assertions could then
	// never catch a regression that let a hostile declared type reach
	// installed rules, because the clause responsible for the
	// tombstone/LIMIT behavior under test would simply never be
	// emitted. The default, unrestricted listing walks every installed
	// type's clause instead, schema objects included -- sch-acme (aka
	// coreSchemaObjectID), sch-hostile, and sch-hostile-ns are objects
	// like any other (store_test.go's coreSchemaObjectID doc comment) --
	// which is what actually exercises the fix.
	wantLiveIDs := map[string]bool{
		widgetID: true, liveNoteID: true,
		coreSchemaObjectID: true, "sch-hostile": true, "sch-hostile-ns": true,
	}

	live, err := store.Query.Objects(writ.ObjectFilter{})
	if err != nil {
		t.Fatalf("Query.Objects (default) failed: %v", err)
	}
	if len(live) != len(wantLiveIDs) {
		t.Fatalf("Query.Objects (default) = %d results, want %d (widget + live note + the three schema objects): %+v", len(live), len(wantLiveIDs), live)
	}
	for _, o := range live {
		if o.ObjectID == deletedNoteID {
			t.Errorf("Query.Objects (default) included the soft-deleted note %s despite the hostile declarations in the log: %+v", deletedNoteID, live)
		}
		if !wantLiveIDs[o.ObjectID] {
			t.Errorf("Query.Objects (default) returned unexpected object %s: %+v", o.ObjectID, live)
		}
	}

	limited, err := store.Query.Objects(writ.ObjectFilter{Limit: 1})
	if err != nil {
		t.Fatalf("Query.Objects (Limit: 1) failed: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("Query.Objects (Limit: 1) = %d results, want exactly 1", len(limited))
	}

	all, err := store.Query.Objects(writ.ObjectFilter{IncludeDeleted: true})
	if err != nil {
		t.Fatalf("Query.Objects (IncludeDeleted: true) failed: %v", err)
	}
	wantAllIDs := map[string]bool{
		widgetID: true, liveNoteID: true, deletedNoteID: true,
		coreSchemaObjectID: true, "sch-hostile": true, "sch-hostile-ns": true,
	}
	if len(all) != len(wantAllIDs) {
		t.Fatalf("Query.Objects (IncludeDeleted: true) = %d results, want %d (the objects table must stay intact): %+v", len(all), len(wantAllIDs), all)
	}
	for _, o := range all {
		if !wantAllIDs[o.ObjectID] {
			t.Errorf("Query.Objects (IncludeDeleted: true) returned unexpected object %s: %+v", o.ObjectID, all)
		}
	}
}

// TestRulesFromSchemas_NonDerivedIDObjectDroppedNotInstalled replaces the
// old TestRulesFromSchemas_NamespaceCollisionDoesNotWithholdRulesAlone,
// which pinned spec/schema-ops.md's now-deleted §6 conflict kind 2: two
// schema objects sharing a namespace but binding different types used to
// be "a weaker, mostly cosmetic case" that withheld nothing. WRIT-254
// change 2 deletes that conflict kind rather than leaving it standing
// (a house rule: a superseded decision's old form is deleted, not
// bridged to) — two schema objects can no longer both survive sharing a
// namespace at all, because at most one ObjectID can equal
// "schema:" + that namespace, so what this test now pins is the drop
// itself: b's non-derived id gets it dropped wholesale, with one
// id-mismatch conflict and none of its fields installed, while a's
// correctly-derived id keeps it entirely unaffected by b's presence —
// the same "one bad object never sinks a legitimate sibling" property
// the old test cared about, reached by the new mechanism.
func TestRulesFromSchemas_NonDerivedIDObjectDroppedNotInstalled(t *testing.T) {
	a := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "acme.standup", Fields: []state.SchemaField{mkField("standup", "create", 1, "summary", "lww")}},
		},
	}
	b := state.Schema{
		ObjectID:  "sch-b", // deliberately not "schema:acme"
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "acme.retro", Fields: []state.SchemaField{mkField("retro", "create", 1, "notes", "lww")}},
		},
	}

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a, b})

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
	// ObjectID "schema:" is the derived form for an empty (never-set)
	// namespace — required so this object clears WRIT-254 change 2's
	// schema-object-id gate and actually reaches the per-type loop this
	// test exercises, rather than being dropped wholesale for an
	// unrelated reason before "schema" is ever looked at.
	a := state.Schema{
		ObjectID: "schema:",
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
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{Name: "acme.standup", Fields: []state.SchemaField{
				mkField("standup", "create", 1, "summary", ""),        // strategy:"" - invalid
				mkField("standup", "create", 1, "notes", "keyed-lww"), // keyed-lww with no key - invalid
				mkField("standup", "create", 1, "owner", "bogus"),     // strategy:"bogus" - not in the catalogue at all
			}},
		},
	}

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if got := rules["acme.standup"]; len(got) != 0 {
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
			ObjectID: "obj-1", ObjectType: "acme.standup", OpType: "create", OpVersion: 1,
			Body: json.RawMessage(`{"summary":"hello","notes":"hi","owner":"alice"}`),
		},
		ID: "op-1",
	}
	objState, err := writ.Fold([]codec.Op{dataOp}, rules["acme.standup"])
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
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{
				Name: "acme.standup",
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
	got := rules["acme.standup"]
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
	voc, ok := vocabularies["acme.standup"]
	if !ok || !voc.Declared {
		t.Fatalf("expected acme.standup Declared, got %+v", voc)
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
			ObjectID: "obj-1", ObjectType: "acme.standup", OpType: "UPPER", OpVersion: 1,
			Body: json.RawMessage(`{}`),
		},
		ID: "op-1",
	}
	objState, err := writ.Fold([]codec.Op{dataOp}, rules["acme.standup"])
	if err != nil {
		t.Fatalf("Fold must never see a rule for a grammar-invalid op_type, got error: %v", err)
	}
	if len(objState.UnknownOps) != 1 || objState.UnknownOps[0].Commit != "op-1" {
		t.Fatalf("expected op-1 to fall through to UnknownOps, got %+v", objState)
	}
}

// TestRulesFromSchemas_InvalidTargetOrKeyGrammarDroppedNotInstalled pins
// WRIT-203: a define-field's target and every keyed-lww key column now
// share field's own identifier grammar (^[a-z][a-z0-9_]*$, max 64 chars),
// gated inside spec.ValidateFieldRule, so resolveSchemaTypes's per-field
// pass 1 drops a rule declaring either exactly as it already drops one
// declaring an unknown strategy — never reaching pass 2's key-column
// agreement check or pass 3's target-agreement check. A malformed target
// or key column is a defect of one rule: dropping it must not withhold a
// sibling rule that legitimately binds its own, well-formed target.
//
// summary/code/owner/tags alone would not discriminate *where* the check
// runs: each malformed rule there is alone in its byTarget/byKeyColumn
// group, so the assertions below would pass identically whether the
// grammar check lived in pass 1, or was folded into pass 2's
// CheckKeyColumnAgreement or pass 3's CheckTargetAgreement instead (round
// 2 finding 1 on WRIT-203's PR). alpha/beta and gamma/delta close that
// gap: each pairs a malformed rule with a legitimate sibling it would
// poison if the grammar check ever moved into the grouping pass that
// sees them together.
//
//   - alpha (good) and beta (bad) both bind target "shared" — the same
//     shape pass 3 groups by. beta's key column "Bad Col2" fails the
//     grammar, so pass 1 drops beta before byTarget is even built, and
//     alpha installs alone. Move the check into CheckTargetAgreement and
//     beta survives to pass 3, where it and alpha disagree on strategy
//     (lww vs keyed-lww) — CheckTargetAgreement then withholds both,
//     wrongly taking alpha down with it.
//   - gamma (good) and delta (bad) share op_type/op_version "set-subject"/1
//     and both bind key column "subject" — the shape pass 2 groups by.
//     delta's target "bad-target2" fails the grammar, so pass 1 drops
//     delta before byKeyColumn is built, and gamma installs alone. Move
//     the check into CheckKeyColumnAgreement and delta survives to pass
//     2, where it and gamma disagree on key_types for "subject"
//     (person-ref vs string) — CheckKeyColumnAgreement then withholds
//     both, wrongly taking gamma down with it.
func TestRulesFromSchemas_InvalidTargetOrKeyGrammarDroppedNotInstalled(t *testing.T) {
	sch := state.Schema{
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types: []state.SchemaType{
			{
				Name: "acme.standup",
				Fields: []state.SchemaField{
					{Name: "summary", OpType: "create", OpVersion: 1, Strategy: "lww", ValueType: "string"},                                                                                // valid, control
					{Name: "code", OpType: "create", OpVersion: 1, Strategy: "lww", ValueType: "string", Target: "identifier"},                                                             // valid target, sibling
					{Name: "owner", OpType: "create", OpVersion: 1, Strategy: "lww", ValueType: "string", Target: "bad-target"},                                                            // hyphen not in field_name's grammar - invalid
					{Name: "tags", OpType: "create", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string", Key: []string{"Bad Col"}, KeyTypes: map[string]string{"Bad Col": "string"}}, // space and uppercase - invalid

					// Discriminates pass 1 from pass 3 (shared target).
					{Name: "alpha", OpType: "set-alpha", OpVersion: 1, Strategy: "lww", ValueType: "string", Target: "shared"}, // valid, would collide with beta if beta ever reached pass 3
					{Name: "beta", OpType: "set-beta", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
						Key: []string{"Bad Col2"}, KeyTypes: map[string]string{"Bad Col2": "string"}, Target: "shared"}, // invalid key column; also targets "shared"

					// Discriminates pass 1 from pass 2 (shared key column).
					{Name: "gamma", OpType: "set-subject", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
						Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "person-ref"}}, // valid, would collide with delta if delta ever reached pass 2
					{Name: "delta", OpType: "set-subject", OpVersion: 1, Strategy: "keyed-lww", ValueType: "string",
						Key: []string{"subject"}, KeyTypes: map[string]string{"subject": "string"}, Target: "bad-target2"}, // invalid target; also binds key column "subject"
				},
			},
		},
	}

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{sch})
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
		if !strings.Contains(c.Reason, "is invalid and was not installed") {
			t.Errorf("conflict reason does not name a dropped rule: %+v", c)
		}
	}

	// §9's security boundary, exactly as the op_type-grammar and
	// invalid-strategy siblings above assert: neither dropped rule ever
	// reaches Fold, so the ops that would have written them fall through
	// to UnknownOps rather than hard-erroring.
	dataOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "acme.standup", OpType: "create", OpVersion: 1,
			Body: json.RawMessage(`{"summary":"hi","code":"abc","owner":"alice","Bad Col":"x","tags":"y"}`),
		},
		ID: "op-1",
	}
	objState, err := writ.Fold([]codec.Op{dataOp}, rules["acme.standup"])
	if err != nil {
		t.Fatalf("Fold must never see a rule for a grammar-invalid target or key column, got error: %v", err)
	}
	if got := objState.State["summary"]; got != "hi" {
		t.Fatalf("expected summary to fold normally, got %+v", got)
	}
	if got := objState.State["identifier"]; got != "abc" {
		t.Fatalf("expected code's target identifier to fold normally, got %+v", got)
	}

	// alpha and gamma survive pass 1 alongside their poisoned siblings and
	// fold normally under their own, unrelated ops.
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
	objState2, err := writ.Fold([]codec.Op{alphaOp, gammaOp}, rules["acme.standup"])
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

// TestRulesFromSchemas_DeprecatedFieldStaysActiveForFolding proves the
// round-1 fix for finding 2: deprecated:true is metadata discouraging new
// writes, not a removal (spec/schema-ops.md §5, §8; AGENTS.md "old clients
// must not destroy new clients' data"). Deprecating a field must not make
// already-signed data written under it vanish from folded state, and must
// not reclassify the ops that wrote it as UnknownOps.
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

	rulesBefore, conflictsBefore := writ.RulesFromSchemas([]state.Schema{a})
	rulesAfter, conflictsAfter := writ.RulesFromSchemas([]state.Schema{b})

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

	// The data op that wrote "summary" yesterday must fold to the same
	// state today, whether or not the field has since been deprecated —
	// and it must not fall into UnknownOps in either case.
	dataOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID: "obj-1", ObjectType: "acme.standup", OpType: "create", OpVersion: 1,
			Body: json.RawMessage(`{"summary":"important"}`),
		},
		ID: "op-1",
	}

	stateBefore, err := writ.Fold([]codec.Op{dataOp}, rulesBefore["acme.standup"])
	if err != nil {
		t.Fatalf("Fold before deprecation failed: %v", err)
	}
	if len(stateBefore.UnknownOps) != 0 {
		t.Fatalf("expected op-1 known before deprecation, got unknown_ops=%+v", stateBefore.UnknownOps)
	}
	if stateBefore.State["summary"] != "important" {
		t.Fatalf("expected summary=%q before deprecation, got %+v", "important", stateBefore.State)
	}

	stateAfter, err := writ.Fold([]codec.Op{dataOp}, rulesAfter["acme.standup"])
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
	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %+v", conflicts)
	}
	if got := rules["acme.widget"]; len(got) != 2 {
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
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{v1, v2}}},
	}
	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
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
	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts once the version bump declares a distinct target, got %+v", conflicts)
	}
	if got := rules["acme.widget"]; len(got) != 2 {
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
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{v1, v2}}},
	}
	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if got := rules["acme.widget"]; len(got) != 0 {
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
		ObjectID:  "schema:acme",
		Namespace: "acme",
		Types:     []state.SchemaType{{Name: "acme.widget", Fields: []state.SchemaField{v1, v2}}},
	}
	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts for a version bump changing only value_type, got %+v", conflicts)
	}
	if got := rules["acme.widget"]; len(got) != 2 {
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
	var wantRules map[string][]writ.Rule
	var wantConflicts []writ.SchemaConflict
	var wantState writ.ObjectState

	for i := 0; i < 30; i++ {
		fields := append([]state.SchemaField(nil), allFields...)
		r.Shuffle(len(fields), func(a, b int) { fields[a], fields[b] = fields[b], fields[a] })

		schemas := []state.Schema{{
			ObjectID:  "schema:acme",
			Namespace: "acme",
			Types:     []state.SchemaType{{Name: "acme.widget", Fields: fields}},
		}}
		// Permuting the (single-element) schema slice too: spec/schema-ops.md
		// §7 step 5 declares the resolver order-independent in the schema
		// objects it is handed, not only in the fields within one of them.
		r.Shuffle(len(schemas), func(a, b int) { schemas[a], schemas[b] = schemas[b], schemas[a] })

		rules, conflicts := writ.RulesFromSchemas(schemas)
		if got := rules["acme.widget"]; len(got) != 0 {
			t.Fatalf("permutation #%d: expected the whole target withheld, got %+v", i, got)
		}
		if len(conflicts) != 1 {
			t.Fatalf("permutation #%d: expected exactly 1 conflict, got %+v", i, conflicts)
		}

		objState, err := writ.Fold(dataOps, rules["acme.widget"])
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

// TestRulesFromSchemas_VersionBumpKeyArityDisagreementWithholdsTarget pins
// WRIT-234's ruling directly: a keyed-lww target ("verdict") whose two
// rules share a versionBumpClass (same op_type "approval" and field
// "verdict") but declare key tuples of different arity —
// (subject, revision) under op_version 1, (subject) alone under op_version
// 2. Before this ticket, key and key_types were on spec/schema-ops.md §8's
// "MAY freely change" list, so both rules resolved and installed; the
// resolved schema then handed engine/internal/fold.keyedLWWAccumulator a
// target whose entries carry key tuples of two different lengths, which
// makes Result's sort comparator read past the end of the shorter one.
// Closing the carve-out (spec/fieldrules.go's FindTargetDisagreement) makes
// spec.CheckTargetAgreement reject the pair as an ordinary "key" attribute
// disagreement, so resolveSchemaTypes' existing pass-3 withhold (the same
// path a strategy or lattice disagreement already takes) drops both rules
// before RulesFromSchemas ever emits them: no keyed-lww accumulator is
// constructed for "verdict" at all, and the panic becomes structurally
// unreachable for a log-resolved schema rather than handled.
//
// A sibling target ("title", an ordinary lww field on a different op)
// proves the withhold is scoped to "verdict" alone: declining one target
// must not cost the type its other fields. This shuffles the type's field
// order and asserts the resolved rules, the conflicts, and the folded
// ObjectState are byte-identical across every permutation — the same
// standard WRIT-211's three-rule vector above is held to, since which pair
// of rules a resolver compares first must not affect whether the whole
// target is withheld.
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
	var wantRules map[string][]writ.Rule
	var wantConflicts []writ.SchemaConflict
	var wantState writ.ObjectState

	for i := 0; i < 20; i++ {
		fields := append([]state.SchemaField(nil), allFields...)
		r.Shuffle(len(fields), func(a, b int) { fields[a], fields[b] = fields[b], fields[a] })

		schemas := []state.Schema{{
			ObjectID:  "schema:acme",
			Namespace: "acme",
			Types:     []state.SchemaType{{Name: "acme.gadget", Fields: fields}},
		}}

		rules, conflicts := writ.RulesFromSchemas(schemas)
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

		objState, err := writ.Fold(dataOps, rules["acme.gadget"])
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

// TestRulesFromSchemas_SharedKeyColumnDisagreementIsOrderIndependent is the
// key-column twin of TestRulesFromSchemas_ThreeRuleTargetSharingIsOrderIndependent
// above, and pins WRIT-214 round 5's fix. Three keyed-lww rules under one
// (op_type, op_version) all name the key column "subject":
//
//   - (approve, v1, aa) keyed on key(subject) typed person-ref,
//   - (approve, v1, mm) keyed on key(subject, phase) typed string,
//   - (approve, v1, zz) keyed on key(subject) typed person-ref.
//
// Each passes ValidateFieldRule, and each binds a distinct target, so
// spec.CheckTargetAgreement has nothing to say about any of them: the only
// thing wrong is that two of the three disagree about what "subject" is.
// The superseded candidate-vs-bound check answered that by keeping
// whichever rule arrived first and dropping every later dissenter, so the
// installed rule set, the conflict count, and the folded state all turned
// on the order the type's fields were handed over — aa-first installs two
// rules and reports one conflict, mm-first installs one and reports two.
// Set-level, the answer is the same either way: no winner is picked, every
// rule participating in the column is withheld, and one conflict is
// reported. This shuffles both the field order within the type and the
// (single-element) schema slice, and asserts the resolved rules, the
// conflicts, and the resulting folded ObjectState are identical across
// every permutation.
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
	var wantRules map[string][]writ.Rule
	var wantConflicts []writ.SchemaConflict
	var wantState writ.ObjectState

	for i := 0; i < 30; i++ {
		fields := append([]state.SchemaField(nil), allFields...)
		r.Shuffle(len(fields), func(a, b int) { fields[a], fields[b] = fields[b], fields[a] })

		schemas := []state.Schema{{
			ObjectID:  "schema:acme",
			Namespace: "acme",
			Types:     []state.SchemaType{{Name: "acme.widget", Fields: fields}},
		}}
		r.Shuffle(len(schemas), func(a, b int) { schemas[a], schemas[b] = schemas[b], schemas[a] })

		rules, conflicts := writ.RulesFromSchemas(schemas)
		if got := rules["acme.widget"]; len(got) != 0 {
			t.Fatalf("permutation #%d: expected every rule bound to the disagreeing key column withheld, got %+v", i, got)
		}
		if len(conflicts) != 1 {
			t.Fatalf("permutation #%d: expected exactly 1 conflict, got %+v", i, conflicts)
		}

		objState, err := writ.Fold(dataOps, rules["acme.widget"])
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

// schemaFromDefineFields folds one schema object declaring type "widget"
// with op (approve, v1) and the given define-field bodies, through the
// same path the log takes (state.FoldSchema, via writ.SchemaFromEnvelopes)
// rather than by constructing state.Schema directly. That matters for
// TestRulesFromSchemas_KeyColumnVerdictDoesNotTurnOnFieldNames below: fold
// sorts a type's fields canonically by (op_type, op_version, field), so
// the *name* of a field is what decides the order the resolver sees it in,
// and a resolver whose verdict depends on arrival order is one whose
// verdict depends on what the author happened to call a field.
func schemaFromDefineFields(t *testing.T, defs []map[string]any) writ.Schema {
	t.Helper()
	var envs []codec.Envelope
	add := func(opType string, body map[string]any) {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s body: %v", opType, err)
		}
		envs = append(envs, codec.Envelope{ObjectID: "schema:acme", ObjectType: "schema", OpType: opType, OpVersion: 1, Body: raw})
	}
	add("create", map[string]any{"namespace": "acme"})
	add("define-type", map[string]any{"type": "acme.widget"})
	add("define-op", map[string]any{"type": "acme.widget", "op_type": "approve", "op_version": "1"})
	for _, d := range defs {
		add("define-field", d)
	}
	sch, err := writ.SchemaFromEnvelopes(envs)
	if err != nil {
		t.Fatalf("SchemaFromEnvelopes: %v", err)
	}
	return sch
}

// TestRulesFromSchemas_KeyColumnVerdictDoesNotTurnOnFieldNames is the other
// half of the property above, stated the way it is actually observable from
// the log: two schemas identical but for one field's *name* must resolve to
// the same verdict. fold sorts a type's fields by (op_type, op_version,
// field) before the resolver ever sees them (state.FoldSchema), so under
// the superseded candidate-vs-bound check renaming a field from "aa" to
// "zz" — changing nothing else — moved it from first to last in that sort
// and handed the key column to a different rule: "aa" installed, "zz"
// installed the rule that had dropped it, and the op that folded cleanly in
// one schema was quarantined in the other. Set-level, both name choices
// give the identical answer: the column disagrees, so every rule bound to
// it is withheld and the op is unknown either way.
func TestRulesFromSchemas_KeyColumnVerdictDoesNotTurnOnFieldNames(t *testing.T) {
	// mm sorts between "aa" and "zz", so the two variants below differ in
	// which rule fold's canonical sort presents first and in nothing else.
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
			schemas := []writ.Schema{schemaFromDefineFields(t, []map[string]any{dissenter(name), mm})}
			rules, conflicts := writ.RulesFromSchemas(schemas)
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
			objState, err := writ.Fold([]codec.Op{op}, rules["acme.widget"])
			if err != nil {
				t.Fatalf("Fold: %v", err)
			}
			if len(objState.UnknownOps) != 1 {
				t.Fatalf("expected the op to fall through as unknown, got %+v", objState.UnknownOps)
			}
		})
	}
}

// TestRulesFromSchemas_DualRoleTombstoneFieldRefused pins WRIT-214 round 5's
// second finding: a field with strategy tombstone whose name is also another
// rule's keyed-lww key column resolves to a rule combination no value can
// ever satisfy — fold's tombstone reducer requires the raw body value to be
// a JSON boolean, and the key-column floor requires the same body value to
// be a JSON string. It used to install cleanly and then refuse every write,
// which is the declarable-but-unwritable shape this ticket exists to remove,
// merely relocated. The resolver now reports it as a conflict where the
// schema resolves, and withholds both rules — the same "no winner is picked"
// response a disagreeing column gets, since neither rule is at fault alone.
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
			rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
			if got := rules["acme.widget"]; len(got) != 0 {
				t.Fatalf("expected both rules withheld, got %+v", got)
			}
			if len(conflicts) != 1 {
				t.Fatalf("expected exactly 1 conflict naming the unsatisfiable combination, got %+v", conflicts)
			}
			if !strings.Contains(conflicts[0].Reason, "tombstone") {
				t.Fatalf("conflict should name the tombstone strategy, got %q", conflicts[0].Reason)
			}
		})
	}
}

// TestRulesFromSchemas_SharedKeyColumnAgreementOK is the positive control:
// two keyed-lww fields under the same (op_type, op_version) that share a key
// column name but agree on its key_types entry are both installed, with no
// conflict — CheckKeyColumnAgreement only refuses disagreement, not sharing
// itself (writ's own bootstrap schema-ops table relies on exactly this: every
// keyed-lww rule scoped to one schemaFieldKey shares "type" typed string).
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
	rules, conflicts := writ.RulesFromSchemas([]state.Schema{a})
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts when the shared key column agrees, got %+v", conflicts)
	}
	if got := rules["acme.widget"]; len(got) != 2 {
		t.Fatalf("expected both fields installed, got %+v", got)
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

	appendSchemaOp("schema:beta", "create", map[string]any{"namespace": "beta"})
	appendSchemaOp("schema:acme", "create", map[string]any{"namespace": "acme"})
	appendSchemaOp("schema:acme", "define-type", map[string]any{"type": "acme.standup"})

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
	if schemas[0].ObjectID != "schema:acme" || schemas[1].ObjectID != "schema:beta" {
		t.Fatalf("expected schemas ordered by ObjectID (schema:acme, schema:beta), got (%s, %s)", schemas[0].ObjectID, schemas[1].ObjectID)
	}
	if schemas[0].Namespace != "acme" {
		t.Errorf("schemas[0].Namespace = %q, want acme", schemas[0].Namespace)
	}
	if len(schemas[0].Types) != 1 || schemas[0].Types[0].Name != "acme.standup" {
		t.Errorf("schemas[0].Types = %+v, want [acme.standup]", schemas[0].Types)
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

	envs := compileTestSchema(t, "schema:acme", testSchemaSrc)

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
	if len(logSchema.Types) != 1 || logSchema.Types[0].Name != "acme.standup" {
		t.Fatalf("Types = %+v, want [acme.standup]", logSchema.Types)
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

	envs := compileTestSchema(t, "schema:acme", testSchemaSrc)
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
			schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
			schemaEnv(t, "schema:acme", "define-type", map[string]any{"type": "acme.standup"}),
			schemaEnv(t, "schema:acme", "define-field", map[string]any{
				"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "title",
				"value_type": "string", "strategy": "lww",
			}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "schema:acme", delta)
	})

	t.Run("partial multi-op delta on an object with prior ops", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		initial := []codec.Envelope{
			schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
			schemaEnv(t, "schema:acme", "define-field", map[string]any{
				"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "title",
				"value_type": "string", "strategy": "lww",
			}),
		}
		if err := store.ApplySchema(ctx, initial); err != nil {
			t.Fatalf("initial ApplySchema failed: %v", err)
		}
		delta := []codec.Envelope{
			schemaEnv(t, "schema:acme", "define-type", map[string]any{"type": "acme.standup", "description": "A daily standup update"}),
			schemaEnv(t, "schema:acme", "define-field", map[string]any{
				"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "body",
				"value_type": "text", "strategy": "multi-value",
			}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "schema:acme", delta)
	})

	t.Run("empty delta", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		initial := []codec.Envelope{
			schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
			schemaEnv(t, "schema:acme", "define-field", map[string]any{
				"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "title",
				"value_type": "string", "strategy": "lww",
			}),
		}
		if err := store.ApplySchema(ctx, initial); err != nil {
			t.Fatalf("initial ApplySchema failed: %v", err)
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "schema:acme", nil)
	})

	t.Run("second object minted while the writer's chain tip sits on the first", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		if err := store.ApplySchema(ctx, []codec.Envelope{
			schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
		}); err != nil {
			t.Fatalf("ApplySchema for the first object failed: %v", err)
		}
		delta := []codec.Envelope{
			schemaEnv(t, "schema:beta", "create", map[string]any{"namespace": "beta"}),
			schemaEnv(t, "schema:beta", "define-type", map[string]any{"type": "beta.retro"}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "schema:beta", delta)
	})

	t.Run("delta back to the first while the tip sits on the second", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		if err := store.ApplySchema(ctx, []codec.Envelope{
			schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
		}); err != nil {
			t.Fatalf("ApplySchema for the first object failed: %v", err)
		}
		if err := store.ApplySchema(ctx, []codec.Envelope{
			schemaEnv(t, "schema:beta", "create", map[string]any{"namespace": "beta"}),
		}); err != nil {
			t.Fatalf("ApplySchema for the second object failed: %v", err)
		}
		delta := []codec.Envelope{
			schemaEnv(t, "schema:acme", "define-type", map[string]any{"type": "acme.standup"}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "schema:acme", delta)
	})

	t.Run("re-declared field with widened attributes", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		initial := []codec.Envelope{
			schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
			schemaEnv(t, "schema:acme", "define-field", map[string]any{
				"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "title",
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
			schemaEnv(t, "schema:acme", "define-field", map[string]any{
				"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "title",
				"strategy": "lww", "max_length": 200,
			}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "schema:acme", delta)
	})

	t.Run("narrowing define-field that leaves a stale attribute", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		initial := []codec.Envelope{
			schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
			schemaEnv(t, "schema:acme", "define-field", map[string]any{
				"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "state",
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
			schemaEnv(t, "schema:acme", "define-field", map[string]any{
				"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "state",
				"value_type": "string", "strategy": "lww",
			}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "schema:acme", delta)
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
		createOp, err := dagA.Append(ctx, schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}), nil)
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
		if _, err := dagB.Append(ctx, schemaEnv(t, "schema:acme", "define-type", map[string]any{"type": "acme.standup"}), []string{createOp.ID}); err != nil {
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
			schemaEnv(t, "schema:acme", "define-field", map[string]any{
				"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "title",
				"value_type": "string", "strategy": "lww",
			}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "schema:acme", delta)
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

	const objectID = "schema:acme"
	initial := []codec.Envelope{
		schemaEnv(t, objectID, "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, objectID, "define-field", map[string]any{
			"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "state",
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
			"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "state",
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
		schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, "schema:acme", "define-type", map[string]any{"type": "acme.widget"}),
		schemaEnv(t, "schema:acme", "define-op", map[string]any{"type": "acme.widget", "op_type": "create", "op_version": "1"}),
	}
	if err := store.ApplySchema(ctx, schemaEnvs); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	dagStore := writ.StoreDAGStore(store)
	env := codec.Envelope{
		ObjectID:   "widget-1",
		ObjectType: "acme.widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{}`),
	}
	if _, err := dagStore.Append(ctx, env, nil); err != nil {
		t.Fatalf("Append refused a declared op type on a fieldless declared type: %v", err)
	}
}

// TestContestedObjectTypeStaysWritable is the ruling's carve-out
// (spec/op-envelope.md §Producer validation, tier 3): a codec.Vocabulary
// with Contested set permits the write, unvalidated — the opposite of
// what an earlier draft of the WRIT-199 ticket proposed (fail-closed on a
// contested type would be a *permanent* write outage, since nothing is
// ever removed from the log and there is no resolution step; see that
// ticket's RULED block). The reader still degrades a contested type's ops
// to UnknownOp (spec/schema-ops.md §6), unaffected by this test — the
// write/read asymmetry is the point.
//
// Before WRIT-254 this test built its Contested vocabulary the same way a
// real repository's log could: two schema objects sharing a namespace and
// binding the same bare type name. WRIT-254 change 2 closes that route —
// engine/schema.go's resolveSchemaTypes now drops any schema object whose
// id disagrees with "schema:" + its own namespace before its types are
// ever compared to another object's, so two schema objects can no longer
// share a namespace and survive to contest a type
// (TestRulesFromSchemas_ObjectTypeCollisionInstallsNoRules pins the new
// shape). writ.VocabulariesFromSchemas can therefore no longer produce
// Vocabulary{Contested: true} for any type — the engine's own resolver
// path to tier 3 is closed (flagged in the PR description as a question
// for Matt: whether to keep the mechanism at all). Per Matt's ruling,
// codec.Vocabulary.Contested and tier 3 itself stay: this test now builds
// the Contested vocabulary by hand, the way a caller who resolves
// Vocabularies some other way still could, and pins that engine/dag and
// engine/codec still honor it correctly — the mechanism, not the
// engine's one way of reaching it, is what this test is really about.
func TestContestedObjectTypeStaysWritable(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	ident, err := identity.Load(ctx, dir)
	if err != nil {
		t.Fatalf("identity.Load failed: %v", err)
	}

	resolve := func() (codec.Vocabularies, error) {
		return codec.Vocabularies{
			"alpha.standup": codec.Vocabulary{Contested: true},
		}, nil
	}

	dagStore, err := dag.Open(dir, ident, dag.WithProducerVocabularies(resolve))
	if err != nil {
		t.Fatalf("dag.Open failed: %v", err)
	}

	env := codec.Envelope{
		ObjectID:   "standup-1",
		ObjectType: "alpha.standup",
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
		schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, "schema:acme", "define-type", map[string]any{"type": "acme.gadget"}),
		schemaEnv(t, "schema:acme", "define-op", map[string]any{"type": "acme.gadget", "op_type": "create", "op_version": "1"}),
		schemaEnv(t, "schema:acme", "define-field", map[string]any{
			"type": "acme.gadget", "op_type": "create", "op_version": "1",
			"field": "title", "value_type": "string", "strategy": "lww",
		}),
	}); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	dagStore := writ.StoreDAGStore(store)

	// A body the narrow log schema fully covers: accepted.
	if _, err := dagStore.Append(ctx, codec.Envelope{
		ObjectID:   "g-1",
		ObjectType: "acme.gadget",
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
		ObjectType: "acme.gadget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"Initial","description":"extra"}`),
	}, nil)
	if err == nil {
		t.Fatal("Append accepted a field the log schema does not declare")
	}
	if !strings.Contains(err.Error(), "schema:acme") {
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
		schemaEnv(t, "schema:rogue", "create", map[string]any{"namespace": "rogue"}),
		schemaEnv(t, "schema:rogue", "define-type", map[string]any{"type": "schema"}),
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
		schemaEnv(t, "schema:ok", "create", map[string]any{"namespace": "ok"}),
	}); err != nil {
		t.Fatalf("ApplySchema (ordinary schema write) failed after a rogue redefinition attempt: %v", err)
	}

	// And a direct Append of a "schema" op through the store's own dag.Store
	// succeeds the same way.
	dagStore := writ.StoreDAGStore(store)
	if _, err := dagStore.Append(ctx, schemaEnv(t, "schema:ok", "define-type", map[string]any{"type": "widget"}), nil); err != nil {
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
			ObjectType: "acme.widget",
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
		schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
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

// --- WRIT-202: producer-vocabularies append-path freshness window ---
//
// The tests below use the injected clock (writ.SetStoreClock) rather than
// real sleeps, so nothing here gates on wall time: every "inside the
// window" assertion holds the clock at the exact instant the cache was
// warmed (delta zero), and every "after the window" assertion advances it
// past vocabFreshnessWindow explicitly.

// TestVocabulariesForAppend_LocalSchemaAppendForcesFreshResolve pins the
// load-bearing detail of WRIT-202's fix: Store.noteAppend's "schema"
// branch must zero vocabObservedAt, not just vocabChains, so a local
// schema append is never served back out of vocabulariesForAppend's
// freshness window it just invalidated — even with the clock frozen and
// never advancing past the window on its own.
//
// writ.WithoutAutoRefresh() is not incidental here, and this test is inert
// without it. Store.ApplySchema ends in maybeAutoRefresh, so under the
// default autoRefresh a Refresh -> rules -> vocabularies pass re-resolves
// and re-stamps on its own the moment the schema lands: the assertion
// below is then satisfied by the auto-refresh whether or not noteAppend
// zeroed anything, and the round-1 reviewer confirmed by mutation that
// this test used to stay green with that line deleted outright. Without
// auto-refresh the invalidation is the only thing that can force the
// resolve — and that is also the configuration genuinely at risk, since
// WithoutAutoRefresh is the documented hot-loop caller, the one that
// applies a schema and then appends inside the same 100ms.
func TestVocabulariesForAppend_LocalSchemaAppendForcesFreshResolve(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	store, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithoutAutoRefresh())
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	defer store.Close()

	// Based on the real clock, not an arbitrary fixed date: Store.Open's
	// own initial-rules resolve (when it runs, on a fresh projection
	// cache) stamps vocabObservedAt with the real time.Now, and a frozen
	// instant far from "now" would make the window's Sub arithmetic go
	// negative and read as perpetually fresh instead of testing anything.
	frozen := time.Now()
	writ.SetStoreClock(store, func() time.Time { return frozen })

	// Warms (or re-stamps, if Store.Open's own initial-rules resolve
	// already warmed it) vocabObservedAt = frozen.
	first, err := writ.StoreVocabulariesForAppend(store, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend failed: %v", err)
	}
	firstAddr := reflect.ValueOf(first).Pointer()
	if v, ok := first["acme.gizmo"]; ok && v.Declared {
		t.Fatalf("acme.gizmo unexpectedly already declared before this test wrote it")
	}

	if err := store.ApplySchema(ctx, compileTestSchema(t, "schema:acme", `namespace acme
description "local schema append forces a fresh resolve"

type gizmo {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// The clock has not moved at all. If vocabObservedAt survived the
	// schema append, this would still read as inside the window and
	// return the now-stale cached map instance.
	second, err := writ.StoreVocabulariesForAppend(store, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend after schema append failed: %v", err)
	}
	if got := reflect.ValueOf(second).Pointer(); got == firstAddr {
		t.Fatalf("a local \"schema\" append did not force a fresh resolve on the very next vocabulariesForAppend call, despite the clock never advancing past the window: got the same map instance (%#x)", got)
	}
	if v, ok := second["acme.gizmo"]; !ok || !v.Declared {
		t.Fatalf("the fresh resolve after a local \"schema\" append does not see the type that append declared")
	}
}

// TestVocabulariesForAppend_ZeroStampNeverReadsAsFresh reaches the one
// conjunct of vocabulariesForAppend's window check that nothing else
// exercises: !s.vocabObservedAt.IsZero(). On a cold cache the nil
// vocabCache short-circuits ahead of it, and with a real clock a zero
// stamp is ~2000 years stale and the elapsed comparison decides. The
// single state that actually reaches the guard is the one noteAppend's
// "schema" branch leaves behind — vocabCache still populated, the stamp
// zeroed — read by a clock at or near the zero time, where
// clock().Sub(zero) is not a large positive duration. Round 1 confirmed by
// mutation that deleting the guard left the whole suite green; it does not
// survive this test.
func TestVocabulariesForAppend_ZeroStampNeverReadsAsFresh(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	// Same reason as the test above: without auto-refresh, nothing but the
	// zeroed stamp stands between the schema append and a served-stale
	// snapshot.
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithoutAutoRefresh())
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	defer store.Close()

	frozen := time.Now()
	writ.SetStoreClock(store, func() time.Time { return frozen })

	first, err := writ.StoreVocabulariesForAppend(store, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (warm) failed: %v", err)
	}
	firstAddr := reflect.ValueOf(first).Pointer()

	if err := store.ApplySchema(ctx, compileTestSchema(t, "schema:acme", `namespace acme
description "zero freshness stamp never reads as fresh"

type gizmo {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// vocabCache is still populated (the "schema" branch drops vocabChains
	// and the stamp, not the snapshot), and the stamp is now the zero
	// time.Time. Reading it with a clock at the zero time makes
	// clock().Sub(vocabObservedAt) exactly zero — inside the window by the
	// elapsed comparison alone. Only the IsZero guard refuses it.
	writ.SetStoreClock(store, func() time.Time { return time.Time{} })

	second, err := writ.StoreVocabulariesForAppend(store, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (zero stamp, zero clock) failed: %v", err)
	}
	if got := reflect.ValueOf(second).Pointer(); got == firstAddr {
		t.Fatalf("a zero vocabObservedAt read as fresh under a zero clock and served the pre-apply snapshot (same map instance %#x): the IsZero guard is gone", got)
	}
	if v, ok := second["acme.gizmo"]; !ok || !v.Declared {
		t.Fatalf("the resolve forced by the zero stamp does not see the type the schema append declared")
	}
}

// TestVocabulariesForAppend_ColdCacheAlwaysResolves pins the other
// mandatory-freshness case in WRIT-202's plan: a Store whose
// vocabulariesForAppend has never been called (vocabCache nil) must
// resolve on its first call regardless of what the clock reads — a nil
// cache is never fresh, whatever time it is. Reopening against the same on-disk
// projection cache (WithCacheDir) after a prior Open already populated it
// skips Open's own internal initial-rules resolve (projDB.HasGeneratedTables
// is true), so this Store's vocabCache is genuinely nil at the point the
// test calls vocabulariesForAppend for the first time.
func TestVocabulariesForAppend_ColdCacheAlwaysResolves(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()
	cacheDir := t.TempDir()

	warm, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(cacheDir))
	if err != nil {
		t.Fatalf("warm-up Open failed: %v", err)
	}
	applyCoreSchema(t, ctx, warm)
	if err := warm.Close(); err != nil {
		t.Fatalf("warm-up Close failed: %v", err)
	}

	store, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(cacheDir))
	if err != nil {
		t.Fatalf("re-Open failed: %v", err)
	}
	defer store.Close()

	// A clock frozen at the zero Go time, to make the point that on a cold
	// cache the clock is not consulted at all: the window check leads with
	// s.vocabCache != nil, which short-circuits before either the IsZero
	// guard or the elapsed-time comparison is reached. So this test pins
	// the nil-cache conjunct and only that — an earlier version of this
	// comment claimed the zero clock was exercising the IsZero guard,
	// which it never could. The guard has its own test
	// (TestVocabulariesForAppend_ZeroStampNeverReadsAsFresh), because the
	// only state that reaches it is a *populated* cache whose stamp
	// noteAppend's "schema" branch just zeroed.
	writ.SetStoreClock(store, func() time.Time { return time.Time{} })

	vocab, err := writ.StoreVocabulariesForAppend(store, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (cold) failed: %v", err)
	}
	if v, ok := vocab["acme.widget"]; !ok || !v.Declared {
		t.Fatalf("cold-cache vocabulariesForAppend did not resolve ground truth: acme.widget from the reopened log is missing")
	}
}

// TestVocabulariesForAppend_SecondHandleRisk pins the accepted risk the
// WRIT-202 ruling names rather than hides: two writ.Store handles open on
// one repository (a CLI plus a watching client, writ's normal case) can
// disagree for up to vocabFreshnessWindow, because vocabulariesForAppend's
// window is scoped to the handle that warmed it and Store.noteAppend's
// chain-observer wiring is per-Store — handle A's append never rolls
// handle B's snapshot forward or invalidates it. Handle B does not see
// handle A's schema change while B's clock stays inside the window, and
// does see it once B's clock advances past the window. This is the
// ruling's accepted trade working as designed — do not "fix" it by
// widening what vocabulariesForAppend observes; see its doc comment in
// engine/schema.go.
func TestVocabulariesForAppend_SecondHandleRisk(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	handleA, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle A failed: %v", err)
	}
	defer handleA.Close()

	handleB, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle B failed: %v", err)
	}
	defer handleB.Close()

	// Based on the real clock, not an arbitrary fixed date: Store.Open's
	// own initial-rules resolve (when it runs, on a fresh projection
	// cache) stamps vocabObservedAt with the real time.Now, and a frozen
	// instant far from "now" would make the window's Sub arithmetic go
	// negative and read as perpetually fresh instead of testing anything.
	frozen := time.Now()
	writ.SetStoreClock(handleB, func() time.Time { return frozen })

	// Warm B's cache at exactly the frozen instant, before A writes
	// anything B doesn't already know about.
	before, err := writ.StoreVocabulariesForAppend(handleB, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (warm) failed: %v", err)
	}
	if v, ok := before["acme.gizmo"]; ok && v.Declared {
		t.Fatalf("acme.gizmo unexpectedly already declared before handle A wrote it")
	}

	if err := handleA.ApplySchema(ctx, compileTestSchema(t, "schema:acme", `namespace acme
description "second-handle risk test vocabulary"

type gizmo {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
		t.Fatalf("handle A ApplySchema failed: %v", err)
	}

	// Still inside the window (B's clock frozen at the exact instant B
	// was warmed): B must NOT see A's change yet.
	inside, err := writ.StoreVocabulariesForAppend(handleB, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (inside window) failed: %v", err)
	}
	if v, ok := inside["acme.gizmo"]; ok && v.Declared {
		t.Fatalf("handle B saw handle A's schema change inside the freshness window: the accepted risk did not hold")
	}

	// Advance B's clock past vocabFreshnessWindow (100ms): the very next
	// call must re-derive and see A's change.
	writ.SetStoreClock(handleB, func() time.Time { return frozen.Add(101 * time.Millisecond) })
	after, err := writ.StoreVocabulariesForAppend(handleB, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (after window) failed: %v", err)
	}
	if v, ok := after["acme.gizmo"]; !ok || !v.Declared {
		t.Fatalf("handle B still does not see handle A's schema change after the freshness window elapsed")
	}
}

// TestVocabulariesForAppend_NonSchemaAppendsNeverExtendTheWindow pins the
// half of Store.noteAppend's contract that its own comment calls
// load-bearing and that round 1 found nothing tested: the non-"schema"
// branch rolls this writer's chain tip and fingerprint forward, but must
// never stamp vocabObservedAt. Adding s.vocabObservedAt = s.clock() there
// makes the freshness window unbounded rather than 100ms — every append
// pushes the deadline out again, so a handle in a steady append loop never
// re-observes a peer's schema change at all, and the "bounded by the
// window" claim in ARCHITECTURE.md and spec/op-envelope.md quietly becomes
// false. That is the whole distance between the risk this ticket's ruling
// accepted (bounded staleness) and one nobody ruled on. Round 1 confirmed
// the mutant survives every other test in the suite, this one included
// before it existed.
//
// Handle B appends in a loop with its clock advanced 10ms per iteration
// while handle A declares a type mid-loop; B must re-observe at exactly
// 100ms of simulated time, no later and no sooner.
func TestVocabulariesForAppend_NonSchemaAppendsNeverExtendTheWindow(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	handleA, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle A failed: %v", err)
	}
	defer handleA.Close()

	// acme.widget, so handle B's loop below has an ordinary non-"schema"
	// op type its producer pre-flight will accept.
	applyCoreSchema(t, ctx, handleA)

	handleB, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle B failed: %v", err)
	}
	defer handleB.Close()

	// Based on the real clock, for the same reason the tests above are.
	frozen := time.Now()
	now := frozen
	writ.SetStoreClock(handleB, func() time.Time { return now })

	// Warm through the reader entry point, which always re-derives and so
	// always re-stamps: vocabObservedAt is then exactly `frozen`, and the
	// 100ms assertion at the bottom is arithmetic rather than a race with
	// however long ago Store.Open's own initial-rules resolve stamped it.
	if _, err := writ.StoreVocabularies(handleB, ctx); err != nil {
		t.Fatalf("StoreVocabularies (warm) failed: %v", err)
	}
	warm, err := writ.StoreVocabulariesForAppend(handleB, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (warm) failed: %v", err)
	}
	if v, ok := warm["peer.gizmo"]; ok && v.Declared {
		t.Fatalf("peer.gizmo unexpectedly already declared before handle A wrote it")
	}

	dagB := writ.StoreDAGStore(handleB)
	const stepMillis = 10
	sawAtMillis := 0
	for i := 1; i <= 45; i++ {
		env := codec.Envelope{
			ObjectID:   fmt.Sprintf("w-%d", i),
			ObjectType: "acme.widget",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"title":"T"}`),
		}
		if _, err := dagB.Append(ctx, env, nil); err != nil {
			t.Fatalf("append %d failed: %v", i, err)
		}

		// Well inside B's first window, so nothing about when A writes can
		// be what makes B notice.
		if i == 3 {
			if err := handleA.ApplySchema(ctx, compileTestSchema(t, "schema:peer", `namespace peer
description "peer vocabulary for the unbounded-window test"

type gizmo {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
				t.Fatalf("handle A ApplySchema failed: %v", err)
			}
		}

		now = frozen.Add(time.Duration(i*stepMillis) * time.Millisecond)

		vocab, err := writ.StoreVocabulariesForAppend(handleB, ctx)
		if err != nil {
			t.Fatalf("StoreVocabulariesForAppend at %dms failed: %v", i*stepMillis, err)
		}
		if v, ok := vocab["peer.gizmo"]; ok && v.Declared {
			sawAtMillis = i * stepMillis
			break
		}
	}

	if sawAtMillis == 0 {
		t.Fatalf("handle B never re-observed handle A's schema change across 45 appends and 450ms of simulated time: a non-\"schema\" append is extending vocabulariesForAppend's freshness window, which makes the staleness unbounded rather than bounded by vocabFreshnessWindow")
	}
	if sawAtMillis != 100 {
		t.Fatalf("handle B re-observed handle A's schema change at %dms of simulated time, want exactly 100ms (vocabFreshnessWindow measured from the warm, unmoved by the appends in between)", sawAtMillis)
	}
}

// TestVocabularies_FingerprintHitRestampsTheAppendWindow pins the stamp on
// Store.vocabularies' fingerprint-hit path, the other line round 1 found
// untested. A fingerprint hit is a real dag.Chains pass against ground
// truth, so it must re-stamp vocabObservedAt exactly as a full resolve
// does; without the stamp, only a full resolve ever refreshes the window,
// and every append past the first window pays a whole ref walk again —
// which is the entire performance claim WRIT-202 makes.
//
// The observable is deliberately the negative one, because a re-stamp is
// visible only as staleness the window is *supposed* to have: after a
// reader's fingerprint hit at 50ms, an append-path call at 120ms is 70ms
// into a fresh window and must still be serving the cached snapshot.
// Delete the stamp and it is 120ms into the warm's window instead, and
// re-resolves. The 160ms control below keeps this from passing merely
// because the peer's change never lands at all.
func TestVocabularies_FingerprintHitRestampsTheAppendWindow(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	handleA, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle A failed: %v", err)
	}
	defer handleA.Close()
	applyCoreSchema(t, ctx, handleA)

	handleB, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle B failed: %v", err)
	}
	defer handleB.Close()

	frozen := time.Now()
	now := frozen
	writ.SetStoreClock(handleB, func() time.Time { return now })

	if _, err := writ.StoreVocabulariesForAppend(handleB, ctx); err != nil {
		t.Fatalf("StoreVocabulariesForAppend (warm) failed: %v", err)
	}

	// 50ms: an ordinary reader call. Nothing in the repository has moved
	// since the warm, so this is the fingerprint-hit path — and it must
	// re-stamp the window it just re-verified.
	now = frozen.Add(50 * time.Millisecond)
	if _, err := writ.StoreVocabularies(handleB, ctx); err != nil {
		t.Fatalf("StoreVocabularies (fingerprint hit) failed: %v", err)
	}

	if err := handleA.ApplySchema(ctx, compileTestSchema(t, "schema:peer", `namespace peer
description "peer vocabulary for the fingerprint-hit re-stamp test"

type gizmo {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
		t.Fatalf("handle A ApplySchema failed: %v", err)
	}

	// 120ms: 70ms after the fingerprint hit, so inside the window it
	// re-stamped. Without that stamp this is 120ms after the warm, outside
	// the window, and B re-resolves and sees peer.gizmo.
	now = frozen.Add(120 * time.Millisecond)
	inside, err := writ.StoreVocabulariesForAppend(handleB, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (inside re-stamped window) failed: %v", err)
	}
	if v, ok := inside["peer.gizmo"]; ok && v.Declared {
		t.Fatalf("handle B re-derived at 120ms, only 70ms after a fingerprint-hit reader call: that hit did not re-stamp vocabObservedAt, so the append path pays a full dag.Chains pass every window instead of amortising across one")
	}

	// 160ms: 110ms after the fingerprint hit, past the window. Control —
	// proves the assertion above is about the window, not about the peer's
	// change being invisible for some other reason.
	now = frozen.Add(160 * time.Millisecond)
	after, err := writ.StoreVocabulariesForAppend(handleB, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (after re-stamped window) failed: %v", err)
	}
	if v, ok := after["peer.gizmo"]; !ok || !v.Declared {
		t.Fatalf("handle B still does not see handle A's schema change 110ms after the fingerprint hit")
	}
}

// TestVocabulariesForAppend_FullResolveStampsTheAppendWindow pins the
// stamp on Store.vocabularies' *full-resolve* path — the other half of
// WRIT-202's "stamps vocabObservedAt on BOTH branches", and the one line
// round 2 found still had no regression net: deleting it left the whole
// package green. Without it the call that pays for a full resolve never
// opens a window, so the very next append delegates again and pays a
// second dag.Chains ref walk — one extra walk per invalidation cycle,
// silently, with nothing failing.
//
// Reaching that branch deterministically takes an invalidation first:
// Store.Open's own initial-rules resolve leaves a fresh stamp behind, so
// on an untouched handle every call is a window hit and the full-resolve
// branch is never taken at all. Handle B's own "schema" append is the
// invalidation — noteAppend zeroes vocabObservedAt and vocabChains
// together — so the next call must take the full resolve.
//
// The observable is the same negative one as the fingerprint-hit test
// above, and for the same reason: a stamp is visible only as staleness the
// window is supposed to have. At 50ms B must still be serving the snapshot
// its full resolve produced. Delete the stamp and the zero the schema
// append left is still there, the IsZero guard forces a delegate, and B
// re-resolves and sees peer.gizmo. The 150ms control keeps this from
// passing merely because A's change never landed.
func TestVocabulariesForAppend_FullResolveStampsTheAppendWindow(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	handleA, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle A failed: %v", err)
	}
	defer handleA.Close()

	// writ.WithoutAutoRefresh() is load-bearing here for the same reason it
	// is on TestVocabulariesForAppend_LocalSchemaAppendForcesFreshResolve:
	// under the default autoRefresh, ApplySchema's maybeAutoRefresh runs a
	// Refresh -> rules -> vocabularies pass that resolves and stamps on its
	// own, so the branch under test would be reached by that pass rather
	// than by the call the assertions below look at.
	handleB, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()), writ.WithoutAutoRefresh())
	if err != nil {
		t.Fatalf("Open handle B failed: %v", err)
	}
	defer handleB.Close()

	frozen := time.Now()
	now := frozen
	writ.SetStoreClock(handleB, func() time.Time { return now })

	if err := handleB.ApplySchema(ctx, compileTestSchema(t, "schema:bee", `namespace bee
description "handle B's own vocabulary, appended to invalidate B's cache"

type widget {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
		t.Fatalf("handle B ApplySchema failed: %v", err)
	}

	// t=0: the call the invalidation forces. This is the full resolve, and
	// on shipped code it stamps vocabObservedAt = frozen.
	warm, err := writ.StoreVocabulariesForAppend(handleB, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (full resolve) failed: %v", err)
	}
	if v, ok := warm["bee.widget"]; !ok || !v.Declared {
		t.Fatalf("the resolve after handle B's own schema append does not see the type that append declared: this call was not the full-resolve branch the test needs")
	}
	if v, ok := warm["peer.gizmo"]; ok && v.Declared {
		t.Fatalf("peer.gizmo unexpectedly already declared before handle A wrote it")
	}

	if err := handleA.ApplySchema(ctx, compileTestSchema(t, "schema:peer", `namespace peer
description "peer vocabulary for the full-resolve stamp test"

type gizmo {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
		t.Fatalf("handle A ApplySchema failed: %v", err)
	}

	// 50ms: half a window after the full resolve, so B must still be served
	// out of the window that resolve opened — no ref access, no peer.gizmo.
	now = frozen.Add(50 * time.Millisecond)
	inside, err := writ.StoreVocabulariesForAppend(handleB, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (inside the full resolve's window) failed: %v", err)
	}
	if v, ok := inside["peer.gizmo"]; ok && v.Declared {
		t.Fatalf("handle B re-derived at 50ms, half a window after paying for a full resolve: that resolve did not stamp vocabObservedAt, so every invalidation costs a second dag.Chains ref walk on the very next append")
	}

	// 150ms: past the window. Control — proves the assertion above is about
	// the window, not about the peer's change being invisible for some other
	// reason.
	now = frozen.Add(150 * time.Millisecond)
	after, err := writ.StoreVocabulariesForAppend(handleB, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend (after the full resolve's window) failed: %v", err)
	}
	if v, ok := after["peer.gizmo"]; !ok || !v.Declared {
		t.Fatalf("handle B still does not see handle A's schema change 150ms after its own full resolve")
	}
}

// TestNoteAppend_SchemaAppendLeavesNoFingerprintForAParkedReader nets the
// vocabFingerprint clear in Store.noteAppend's "schema" branch (WRIT-202
// review round 5). Zeroing vocabObservedAt is not enough on its own: a
// reader on another goroutine of the *same* handle computes its dag.Chains
// fingerprint outside vocabMu, so one that read the refs before a schema
// append and reaches the lock after it still matches the cached
// pre-append fingerprint, takes vocabularies' fingerprint-hit branch, and
// re-stamps the window on a snapshot that append already invalidated —
// hiding this handle's own schema append from its own append-path
// pre-flight for the rest of the window. Clearing the fingerprint makes
// that reader miss instead, so it resolves against the log as it now
// stands.
//
// Deterministic, not timing-dependent: StoreParkNextChainsScan holds the
// reader's ref scan still after it has read the (pre-append) refs, the
// ApplySchema lands entirely inside that gap, and only then is the reader
// released — so the interleaving is forced by the test rather than raced
// for. The clock is frozen, so the final pre-flight is a window hit either
// way and the only thing under assertion is *which* snapshot that hit
// serves.
//
// It nets fingerprintChains' leading marker as well, and deliberately runs
// against a repository with no writ chains yet — the bootstrap case. Without
// the marker an empty chain set fingerprints to "", which is the same value
// the clear writes, so the clear would be a no-op here and the parked reader
// would match anyway. Delete either line and this test fails.
func TestNoteAppend_SchemaAppendLeavesNoFingerprintForAParkedReader(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	// Auto-refresh off for the same reason as the other window tests: a
	// Refresh riding along on ApplySchema would resolve and re-stamp on its
	// own, and the assertion below would be satisfied by that rather than
	// by anything noteAppend did.
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithoutAutoRefresh())
	if err != nil {
		t.Fatalf("writ.Open failed: %v", err)
	}
	defer store.Close()

	frozen := time.Now()
	writ.SetStoreClock(store, func() time.Time { return frozen })

	// Warm the cache so vocabCache, vocabChains, and vocabFingerprint all
	// hold the pre-append values the parked reader is about to match
	// against.
	if _, err := writ.StoreVocabularies(store, ctx); err != nil {
		t.Fatalf("StoreVocabularies (warm) failed: %v", err)
	}

	parked, release, restore := writ.StoreParkNextChainsScan(store)
	defer restore()
	defer release()

	readerDone := make(chan error, 1)
	go func() {
		_, err := writ.StoreVocabularies(store, ctx)
		readerDone <- err
	}()

	// The reader is now holding the refs as they stood before the append,
	// and has not yet reached vocabMu.
	<-parked

	if err := store.ApplySchema(ctx, compileTestSchema(t, "schema:acme", `namespace acme
description "schema append landing while a reader's chains scan is parked"

type gizmo {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// noteAppend has run. Let the reader finish and write its snapshot back.
	release()
	if err := <-readerDone; err != nil {
		t.Fatalf("parked StoreVocabularies failed: %v", err)
	}

	// The clock never moved, so this is a window hit whatever happened
	// above. With the fingerprint left standing, the reader re-stamped the
	// pre-append snapshot and this hit serves it; with the fingerprint
	// cleared, the reader missed, re-resolved against the post-append log,
	// and this hit serves that.
	got, err := writ.StoreVocabulariesForAppend(store, ctx)
	if err != nil {
		t.Fatalf("StoreVocabulariesForAppend failed: %v", err)
	}
	if v, ok := got["acme.gizmo"]; !ok || !v.Declared {
		t.Fatalf("this handle's own append-path pre-flight does not see this handle's own schema append: a reader parked across the append matched the fingerprint noteAppend's \"schema\" branch left standing and re-stamped the pre-append snapshot")
	}
}

// TestNoteAppend_NonSchemaBranchNeverBumpsVocabGen pins the WRIT-238 round-1
// minor finding on noteAppend's non-"schema" branch: it rolls the cached
// fingerprint forward in place and must leave vocabGen untouched, because a
// bump there is invisible to every sequential test (it clears neither the
// cache nor vocabObservedAt, so a fingerprint hit and the append-path
// window both still land normally) and only costs anything against a
// derive already in flight, whose write-back a spurious bump would then
// skip — the exact amortisation loss Store.noteAppend's own doc comment
// says this cache exists to avoid, silently reintroduced one ordinary
// append at a time.
func TestNoteAppend_NonSchemaBranchNeverBumpsVocabGen(t *testing.T) {
	store, ctx := openWritableStore(t)
	dagStore := writ.StoreDAGStore(store)

	applyCoreSchema(t, ctx, store)

	// Warm the cache so this append rolls forward through the same branch
	// every ordinary append takes in practice.
	if _, err := writ.StoreVocabularies(store, ctx); err != nil {
		t.Fatalf("StoreVocabularies (warm) failed: %v", err)
	}

	before := writ.StoreVocabGen(store)

	env := codec.Envelope{
		ObjectID:   "w-0",
		ObjectType: "acme.widget",
		OpType:     "create",
		OpVersion:  1,
		Body:       json.RawMessage(`{"title":"T"}`),
	}
	if _, err := dagStore.Append(ctx, env, nil); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	if after := writ.StoreVocabGen(store); after != before {
		t.Fatalf("a non-\"schema\" append bumped vocabGen from %d to %d: noteAppend's non-\"schema\" branch must leave it alone — only the \"schema\" branch and invalidateVocabularies may bump it", before, after)
	}
}

// TestInvalidateVocabularies_BumpsVocabGen pins the WRIT-238 round-1 minor
// finding on invalidateVocabularies: deleting its vocabGen++ leaves every
// other test in the package green, because the fingerprint clear it also
// performs already stops a later, sequential reader from matching a stale
// fingerprint. The bump is the only thing that protects a derive already
// past its own fingerprint check when a Store.Sync fetch invalidates the
// cache out from under it — the Sync half of the race WRIT-238 closes,
// alongside noteAppend's local-"schema" half, which
// TestVocabularies_WriteBackRaceCannotInstallAPreChangeSnapshot already
// pins.
func TestInvalidateVocabularies_BumpsVocabGen(t *testing.T) {
	store, ctx := openWritableStore(t)
	applyCoreSchema(t, ctx, store)

	if _, err := writ.StoreVocabularies(store, ctx); err != nil {
		t.Fatalf("StoreVocabularies (warm) failed: %v", err)
	}

	before := writ.StoreVocabGen(store)
	writ.StoreInvalidateVocabularies(store)
	after := writ.StoreVocabGen(store)

	if after == before {
		t.Fatalf("invalidateVocabularies left vocabGen at %d: a derive already in flight when a Store.Sync fetch invalidates the cache could then install its own pre-fetch snapshot over the invalidation, exactly the race WRIT-238 closes on the local-append side", before)
	}
}

// TestVocabularies_WriteBackRaceCannotInstallAPreChangeSnapshot pins the
// WRIT-238 fix: a vocabularies() derive that read dag.Chains before a local
// "schema" append invalidated the cache must not, on reaching its own
// write-back afterward, overwrite that invalidation with its own pre-change
// snapshot — freshness stamp included, which would re-arm
// vocabulariesForAppend's window over stale data and let this handle's own
// append-path pre-flight refuse an op against a type this same handle's own
// ApplySchema just declared.
//
// The interleaving is forced, not raced. StoreParkNextChainsScan cannot
// reach the gap this needs: it parks only the ref scan at the top of
// vocabularies, before the fold, and a scan released there re-folds the
// post-change log correctly, reproducing nothing (see its own doc comment
// and WRIT-238's ticket for why). The seam this test uses instead is
// SetStoreClock, in the gap WRIT-238 moved the write-back's s.clock() read
// into: a one-shot closure fires exactly once, from that read, and
// synchronously runs handle B's own ApplySchema declaring acme.gizmo —
// outside vocabMu, so no deadlock, and guarded against the re-entrant clock
// reads ApplySchema's own append and auto-refresh perform on the same
// goroutine. Handle A's own, unrelated schema append runs first and plainly
// (not through the closure) to move the real chains behind handle B's
// back, which is what makes handle B's own subsequent vocabularies() call
// take the full-resolve branch honestly: the fingerprint-hit branch stamps
// under vocabMu, and firing the closure from inside that branch would
// deadlock against ApplySchema's own noteAppend.
func TestVocabularies_WriteBackRaceCannotInstallAPreChangeSnapshot(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	handleA, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle A failed: %v", err)
	}
	defer handleA.Close()

	// Auto-refresh stays on for handle B (WRIT-238's plan is explicit about
	// this): the point of this test is that the racing derive's stale
	// write-back beats a ground-truth refresh that already ran and got it
	// right, not that refresh never got a chance to run.
	handleB, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle B failed: %v", err)
	}
	defer handleB.Close()

	frozen := time.Now()
	writ.SetStoreClock(handleB, func() time.Time { return frozen })

	// Warm handle B's cache against the log as it stands before either
	// handle writes anything schema-related below.
	if _, err := writ.StoreVocabularies(handleB, ctx); err != nil {
		t.Fatalf("StoreVocabularies (warm) failed: %v", err)
	}

	// Handle A moves the real chains behind handle B's back: an ordinary,
	// complete, synchronous schema append that handle B's own noteAppend
	// never hears about, since that chain-observer wiring is per-Store.
	if err := handleA.ApplySchema(ctx, compileTestSchema(t, "schema:peer", `namespace peer
description "unrelated change that moves handle B's chains behind its back"

type widget {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
		t.Fatalf("handle A ApplySchema failed: %v", err)
	}

	// The one-shot closure: on its first call it declares acme.gizmo on
	// handle B, synchronously, then guards against re-entry so it does not
	// recurse when ApplySchema's own append and auto-refresh read the clock
	// again on their way through.
	var firing bool
	writ.SetStoreClock(handleB, func() time.Time {
		if firing {
			return frozen
		}
		firing = true
		if err := handleB.ApplySchema(ctx, compileTestSchema(t, "schema:acme", `namespace acme
description "declared while a derive of the pre-change log is mid-flight"

type gizmo {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
			t.Fatalf("handle B ApplySchema (racing) failed: %v", err)
		}
		return frozen
	})

	// The racing derive: dag.Chains here reads the post-peer.widget,
	// pre-gizmo log (handle A's append already landed; the closure above
	// has not fired yet), which misses handle B's own cached fingerprint
	// and takes the full-resolve branch — folding that same pre-gizmo log —
	// before the closure fires mid-flight, inside this call's own
	// write-back gap, and moves the log again underneath it.
	stale, err := writ.StoreVocabularies(handleB, ctx)
	if err != nil {
		t.Fatalf("StoreVocabularies (racing derive) failed: %v", err)
	}
	if v, ok := stale["acme.gizmo"]; ok && v.Declared {
		t.Fatalf("the racing derive's own return value already sees acme.gizmo: it did not read the log before the racing ApplySchema landed, so this test is not exercising the interleaving it claims to")
	}

	// The append-path pre-flight, with an explicit Version so
	// Objects.Create does not resolve it through Store.Types first —
	// Store.Types would repair the cache via its own ground-truth
	// vocabularies() call before this ever reached the append's own
	// producer check, passing regardless of the fix under test (WRIT-238's
	// plan flags this as the trap: the same test written against Version 0
	// passes without the fix).
	if _, err := handleB.Objects.Create(ctx, "acme.gizmo", writ.NewOp{
		Type: "create", Version: 1, Fields: map[string]any{"title": "T"},
	}); err != nil {
		t.Fatalf("handle B Objects.Create(acme.gizmo) failed: a derive that read the log before this handle's own ApplySchema declared acme.gizmo overwrote that declaration's correctly-refreshed cache with its own pre-change snapshot, re-arming the append-path freshness window over stale data: %v", err)
	}
}

// TestRulesAndDeclaredTypes_SkippedWriteBackStillReturnsTheFreshDerive pins
// WRIT-238 round-1 item 4, the part the PR body calls "what makes the fix
// correct, not just smaller": Store.rules and Store.declaredTypes return
// the vocabSnapshot their own call to Store.vocabularies produced, never a
// cache read back afterward. The alternative the plan considered and
// rejected — call vocabularies(ctx) for effect, then re-read
// ruleCache/typesCache under a second lock — leaves every other test in
// this package green (the whole-suite run in round 1's finding proved it),
// because ruleCache/typesCache normally agree with what the same call just
// derived. They stop agreeing exactly when a write-back is skipped: this
// derive's own fold already read acme.gizmo (handle A declared it before
// this call started), but a generation-check failure keeps that result out
// of the cache, so a caller that read the cache back instead would get
// whatever it held before this call, not what this call found.
//
// The interleaving forces that gap with StoreInvalidateVocabularies
// directly, rather than through a second handle's ApplySchema like
// TestVocabularies_WriteBackRaceCannotInstallAPreChangeSnapshot: no
// auto-refresh runs afterward to paper over a stale cache by repopulating
// it, so what Store.rules/Store.declaredTypes return here is entirely
// their own doing, not a side effect of the fix's other half.
//
// The gap is forced twice, once per call below, not once: a single
// skipped write-back leaves ruleCache/typesCache merely uninstalled-into,
// not cleared, and StoreInvalidateVocabularies drops vocabFingerprint
// along with them, so the very next vocabularies call is guaranteed a
// fingerprint miss -- a full resolve that, absent a second invalidation,
// installs cleanly and repopulates typesCache with the correct answer
// before Types ever reads it back. A mutant that reverts
// Store.declaredTypes alone to a cache read-back would pass against a
// single-fire version of this test for exactly that reason: the Types
// call's own fresh resolve papers over the stale cache on its way to a
// correct answer, the same way a second handle's ApplySchema plus an
// auto-refresh papers over it on the append path in
// TestVocabularies_WriteBackRaceCannotInstallAPreChangeSnapshot. Firing
// again on the Types call's own write-back closes that gap, so ruleCache
// and typesCache alike stay exactly as stale as the first invalidation
// left them (pre-gizmo, from the warm call below) through both
// assertions.
func TestRulesAndDeclaredTypes_SkippedWriteBackStillReturnsTheFreshDerive(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	handleA, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle A failed: %v", err)
	}
	defer handleA.Close()

	handleB, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle B failed: %v", err)
	}
	defer handleB.Close()

	frozen := time.Now()
	writ.SetStoreClock(handleB, func() time.Time { return frozen })

	// Warm handle B's rule/type cache against the log before acme.gizmo
	// exists, so ruleCache/typesCache hold a table without it — the stale
	// value a read-back implementation would hand back once the write-back
	// below is skipped.
	if _, err := writ.StoreRules(handleB, ctx); err != nil {
		t.Fatalf("StoreRules (warm) failed: %v", err)
	}

	// Handle A declares acme.gizmo: an ordinary, complete, synchronous
	// schema append on a different handle, moving the real chains behind
	// handle B's back exactly as in the write-back race test above.
	if err := handleA.ApplySchema(ctx, compileTestSchema(t, "schema:acme", `namespace acme
description "declared on another handle while B's rules()/declaredTypes() derive is mid-flight"

type gizmo {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
		t.Fatalf("handle A ApplySchema failed: %v", err)
	}

	// Fires from each of its first two calls, not just the first: the
	// StoreRules call below needs one invalidation to force its write-back
	// gap, and the Types call after it needs a second, independent one, or
	// its own full resolve installs cleanly and repopulates typesCache
	// with the correct answer before a read-back could ever see it stale
	// (see the doc comment above). Both calls land from the write-back's
	// clock read on Store.vocabularies' full-resolve branch, outside
	// vocabMu, which is why StoreInvalidateVocabularies (it takes vocabMu
	// itself) cannot deadlock against either of them; capped at two calls
	// so this closure never fires from Store.vocabularies' fingerprint-hit
	// branch instead, which reads the clock from inside vocabMu and would
	// self-deadlock against StoreInvalidateVocabularies taking the same
	// lock.
	var fires int
	writ.SetStoreClock(handleB, func() time.Time {
		if fires < 2 {
			fires++
			writ.StoreInvalidateVocabularies(handleB)
		}
		return frozen
	})

	// The racing derive: dag.Chains here sees handle A's already-landed
	// gizmo commit, misses handle B's own cached fingerprint, and folds the
	// post-gizmo log on the full-resolve branch — before the closure fires
	// mid-flight, at the write-back's clock read, and invalidates the cache
	// underneath it. The generation check must then see the mismatch and
	// skip the install, leaving nothing to repopulate ruleCache/typesCache
	// afterward.
	rules, err := writ.StoreRules(handleB, ctx)
	if err != nil {
		t.Fatalf("StoreRules (racing derive) failed: %v", err)
	}
	if fires < 1 {
		t.Fatalf("the clock closure never fired: this derive took the fingerprint-hit branch instead of a full resolve, so it never reached the write-back gap this test needs")
	}
	if _, ok := rules["acme.gizmo"]; !ok {
		t.Fatalf("Store.rules returned a table without acme.gizmo even though this call's own fold read the log after handle A's ApplySchema landed: it must hand back what it just derived, not read a cache a skipped write-back left stale (or, on a cache never populated, nil)")
	}

	// The second racing derive, forced the same way: StoreInvalidateVocabularies
	// cleared vocabFingerprint along with the cache above, so this call is
	// also guaranteed a fingerprint miss and a full resolve — and the
	// closure's second fire invalidates that resolve's write-back too,
	// keeping typesCache exactly as stale as the first invalidation left
	// it (pre-gizmo) rather than letting this call's own successful
	// install repopulate it with the correct answer first.
	types, err := handleB.Types(ctx)
	if err != nil {
		t.Fatalf("Types failed: %v", err)
	}
	if fires < 2 {
		t.Fatalf("the clock closure fired only once: the Types call took the fingerprint-hit branch instead of a second full resolve, so it never reached the write-back gap this test needs for Store.declaredTypes")
	}
	found := false
	for _, ty := range types {
		if ty.Name == "acme.gizmo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Types (via Store.declaredTypes) did not see acme.gizmo even though this call's own fold read the log after handle A's ApplySchema landed: it must hand back what this call just derived, not read a cache its own skipped write-back left stale")
	}
}

// TestTypes_AlwaysSeesAnotherHandlesSchemaChange pins WRIT-202 item 4's
// conservative split: Store.Types (through Store.declaredTypes and
// Store.vocabularies) must keep re-deriving ground truth on every call,
// never inheriting vocabulariesForAppend's append-path freshness window.
// Handle B's clock is frozen throughout and never advances — this is the
// test that fails the moment someone later routes readers through the
// windowed entry point.
func TestTypes_AlwaysSeesAnotherHandlesSchemaChange(t *testing.T) {
	dir, _ := setupConfiguredRepo(t)
	ctx := context.Background()

	handleA, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle A failed: %v", err)
	}
	defer handleA.Close()

	handleB, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(t.TempDir()))
	if err != nil {
		t.Fatalf("Open handle B failed: %v", err)
	}
	defer handleB.Close()

	// Based on the real clock, not an arbitrary fixed date: Store.Open's
	// own initial-rules resolve (when it runs, on a fresh projection
	// cache) stamps vocabObservedAt with the real time.Now, and a frozen
	// instant far from "now" would make the window's Sub arithmetic go
	// negative and read as perpetually fresh instead of testing anything.
	frozen := time.Now()
	writ.SetStoreClock(handleB, func() time.Time { return frozen })

	before, err := handleB.Types(ctx)
	if err != nil {
		t.Fatalf("handle B Types (before) failed: %v", err)
	}
	if _, ok := findSchemaType(before, "acme.gizmo"); ok {
		t.Fatalf("acme.gizmo unexpectedly already declared before handle A wrote it")
	}

	if err := handleA.ApplySchema(ctx, compileTestSchema(t, "schema:acme", `namespace acme
description "readers keep re-deriving test vocabulary"

type gizmo {
  op create 1 {
    title string(200) lww
  }
}
`)); err != nil {
		t.Fatalf("handle A ApplySchema failed: %v", err)
	}

	// B's clock has not moved a single tick — the append-path window
	// would still call a cache from before this instant "fresh", but
	// Types must not consult that window at all.
	after, err := handleB.Types(ctx)
	if err != nil {
		t.Fatalf("handle B Types (after) failed: %v", err)
	}
	if _, ok := findSchemaType(after, "acme.gizmo"); !ok {
		t.Fatalf("handle B's Types did not see handle A's schema change immediately: a reader inherited the append-path freshness window")
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
		schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, "schema:acme", "define-type", map[string]any{"type": "acme.gizmo"}),
		schemaEnv(t, "schema:acme", "define-op", map[string]any{"type": "acme.gizmo", "op_type": "spin", "op_version": "1"}),
		schemaEnv(t, "schema:acme", "define-field", map[string]any{
			"type": "acme.gizmo", "op_type": "spin", "op_version": "1",
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
	gizmo, ok := findSchemaType(after, "acme.gizmo")
	if !ok {
		t.Fatalf("Store.Types after ApplySchema: declared type %q missing from %+v", "acme.gizmo", after)
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
		schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, "schema:acme", "define-type", map[string]any{"type": "acme.widget", "description": "A widget type"}),
		schemaEnv(t, "schema:acme", "define-op", map[string]any{"type": "acme.widget", "op_type": "spin", "op_version": "1", "description": "spin it"}),
		schemaEnv(t, "schema:acme", "define-field", map[string]any{
			"type": "acme.widget", "op_type": "spin", "op_version": "1",
			"field": "speed", "value_type": "int", "strategy": "lww",
		}),
		schemaEnv(t, "schema:acme", "deprecate-type", map[string]any{"type": "acme.widget", "deprecated": true}),
	}); err != nil {
		t.Fatalf("ApplySchema (widget) failed: %v", err)
	}
	if err := store.ApplySchema(ctx, []codec.Envelope{
		schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, "schema:acme", "define-type", map[string]any{"type": "acme.beacon", "description": "A fieldless beacon type"}),
		schemaEnv(t, "schema:acme", "define-op", map[string]any{"type": "acme.beacon", "op_type": "ping", "op_version": "1"}),
	}); err != nil {
		t.Fatalf("ApplySchema (beacon) failed: %v", err)
	}

	types, err := store.Types(ctx)
	if err != nil {
		t.Fatalf("Store.Types failed: %v", err)
	}

	widget, ok := findSchemaType(types, "acme.widget")
	if !ok {
		t.Fatal("Store.Types: declared type \"acme.widget\" missing")
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

	beacon, ok := findSchemaType(types, "acme.beacon")
	if !ok {
		t.Fatal("Store.Types: define-op-only type \"acme.beacon\" missing")
	}
	if beacon.Description != "A fieldless beacon type" {
		t.Errorf("beacon.Description = %q, want %q", beacon.Description, "A fieldless beacon type")
	}
	if beacon.Deprecated {
		t.Errorf("beacon.Deprecated = true, want false (never deprecated)")
	}
}

// TestProjectionHazardB_QualifiedTypesFromDifferentNamespacesGetOwnTables
// is WRIT-217 hazard B's own acceptance check, end to end against a real
// SQLite projection, not merely "does it not crash": engine/projection/
// ddl.go builds a generated table name as "o_" + strings.ReplaceAll(
// objectType, "-", "_") — a qualified object_type's dot survives that
// replace verbatim — so left unquoted, "o_acme.standup" parses in SQLite
// as table "standup" in schema "o_acme" rather than one table literally
// named "o_acme.standup". Two different namespaces declaring the same
// bare type name ("standup") must project into two distinct, independently
// queryable tables, and a Rebuild (drop-and-recreate, since the
// projection is a droppable cache) must not disturb that.
func TestProjectionHazardB_QualifiedTypesFromDifferentNamespacesGetOwnTables(t *testing.T) {
	store, ctx := openWritableStore(t)

	if err := store.ApplySchema(ctx, []codec.Envelope{
		schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, "schema:acme", "define-type", map[string]any{"type": "acme.standup"}),
		schemaEnv(t, "schema:acme", "define-op", map[string]any{"type": "acme.standup", "op_type": "create", "op_version": "1"}),
		schemaEnv(t, "schema:acme", "define-field", map[string]any{
			"type": "acme.standup", "op_type": "create", "op_version": "1",
			"field": "title", "value_type": "string", "strategy": "lww",
		}),
	}); err != nil {
		t.Fatalf("ApplySchema (acme) failed: %v", err)
	}
	if err := store.ApplySchema(ctx, []codec.Envelope{
		schemaEnv(t, "schema:other", "create", map[string]any{"namespace": "other"}),
		schemaEnv(t, "schema:other", "define-type", map[string]any{"type": "other.standup"}),
		schemaEnv(t, "schema:other", "define-op", map[string]any{"type": "other.standup", "op_type": "create", "op_version": "1"}),
		schemaEnv(t, "schema:other", "define-field", map[string]any{
			"type": "other.standup", "op_type": "create", "op_version": "1",
			"field": "title", "value_type": "string", "strategy": "lww",
		}),
	}); err != nil {
		t.Fatalf("ApplySchema (other) failed: %v", err)
	}

	schemas, err := store.Schema(ctx)
	if err != nil {
		t.Fatalf("Store.Schema failed: %v", err)
	}
	rules, conflicts := writ.RulesFromSchemas(schemas)
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts between acme.standup and other.standup, got %+v", conflicts)
	}
	if _, ok := rules["acme.standup"]; !ok {
		t.Fatalf("expected rules installed for acme.standup, got %+v", rules)
	}
	if _, ok := rules["other.standup"]; !ok {
		t.Fatalf("expected rules installed for other.standup, got %+v", rules)
	}

	acmeID, err := store.Objects.Create(ctx, "acme.standup", writ.NewOp{
		Type: "create", Fields: map[string]any{"title": "Acme standup"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(acme.standup) failed: %v", err)
	}
	otherID, err := store.Objects.Create(ctx, "other.standup", writ.NewOp{
		Type: "create", Fields: map[string]any{"title": "Other standup"},
	})
	if err != nil {
		t.Fatalf("Objects.Create(other.standup) failed: %v", err)
	}

	// The projection is a droppable cache: drop and rebuild it, then
	// confirm both objects still resolve to their own type's own data —
	// this is what actually exercises the generated DDL and its quoting,
	// not just the create-time write path.
	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Store.Rebuild failed: %v", err)
	}

	acmeObj, err := store.Objects.Get(ctx, acmeID)
	if err != nil {
		t.Fatalf("Objects.Get(acme) after rebuild failed: %v", err)
	}
	if acmeObj.ObjectType != "acme.standup" || acmeObj.Fields["title"] != "Acme standup" {
		t.Fatalf("acme object after rebuild = %+v, want type acme.standup, title \"Acme standup\"", acmeObj)
	}

	otherObj, err := store.Objects.Get(ctx, otherID)
	if err != nil {
		t.Fatalf("Objects.Get(other) after rebuild failed: %v", err)
	}
	if otherObj.ObjectType != "other.standup" || otherObj.Fields["title"] != "Other standup" {
		t.Fatalf("other object after rebuild = %+v, want type other.standup, title \"Other standup\"", otherObj)
	}

	// Query.Objects filtered to one qualified type must not leak the
	// other's rows — the exact symptom hazard B's unquoted "o_acme.standup"
	// (read by SQLite as table "standup" in schema "o_acme") would produce
	// if it happened to resolve to something other than an outright error.
	acmeResults, err := store.Query.Objects(writ.ObjectFilter{Type: []string{"acme.standup"}})
	if err != nil {
		t.Fatalf("Query.Objects(acme.standup) failed: %v", err)
	}
	if len(acmeResults) != 1 || acmeResults[0].ObjectID != acmeID {
		t.Fatalf("Query.Objects(acme.standup) = %+v, want exactly [%s]", acmeResults, acmeID)
	}

	otherResults, err := store.Query.Objects(writ.ObjectFilter{Type: []string{"other.standup"}})
	if err != nil {
		t.Fatalf("Query.Objects(other.standup) failed: %v", err)
	}
	if len(otherResults) != 1 || otherResults[0].ObjectID != otherID {
		t.Fatalf("Query.Objects(other.standup) = %+v, want exactly [%s]", otherResults, otherID)
	}

	// Belt-and-braces: two distinct generated tables, each named with the
	// dot verbatim ("o_acme.standup", "o_other.standup" — hyphen still
	// maps to underscore, the dot survives), not one table shared by
	// coincidence of an unquoted schema-qualified name.
	rows, err := writ.StoreProjection(store).DB().Query(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'o\_%' ESCAPE '\' ORDER BY name`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	defer rows.Close()
	var tableNames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tableNames = append(tableNames, name)
	}
	if !slices.Contains(tableNames, "o_acme.standup") || !slices.Contains(tableNames, "o_other.standup") {
		t.Fatalf("generated tables = %v, want both \"o_acme.standup\" and \"o_other.standup\"", tableNames)
	}
}
