package writ_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
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
)

// TestSchemaInstallable_AgreesWithConflictsDrop is the anti-drift device
// proving writ.SchemaInstallable agrees with writ.SchemaConflicts' drop decision.
func TestSchemaInstallable_AgreesWithConflictsDrop(t *testing.T) {
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
			sch := writ.Schema{ObjectID: tc.objectID, Namespace: tc.namespace}
			installable := writ.SchemaInstallable(sch)

			conflicts := writ.SchemaConflicts([]writ.Schema{sch})
			var droppedWholesale bool
			for _, c := range conflicts {
				if c.ObjectType == "" && slices.Contains(c.ObjectIDs, sch.ObjectID) {
					droppedWholesale = true
					break
				}
			}

			if installable == droppedWholesale {
				t.Fatalf("writ.SchemaInstallable(%+v) = %v, but SchemaConflicts reported a whole-object drop for it = %v -- these must always disagree, since Installable means NOT dropped", sch, installable, droppedWholesale)
			}
		})
	}
}

// writeForeignSchemaOp authors one op directly to the git repository's
// object store and advances the writer's ref to it, deliberately
// bypassing this build's own codec.SignOp and producer validation so tests
// can simulate an op that was authored by a newer or different client
// version. It writes an unsigned commit, author timestamp stamped sequentially
// by seq (seconds from a fixed epoch) and parent pointing at parent so
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

// TestStoreTypes_ReportsRawUnrecognizedValueType pins that Store.Types --
// the schema-in-the-log shape a caller reads back (spec/op-envelope.md
// §Producer validation, "the shapes callers see come from the schema in
// the log", AGENTS.md) -- reports a field's value_type exactly as declared,
// undemoted, even when it names something outside this build's own
// spec.KnownValueTypes. The declaration is written as a foreign/newer
// writer's op, bypassing this build's own producer validation entirely
// (writeForeignSchemaOp), the same way a real newer writer's schema would
// already be sitting in the log before this build ever opened it.
func TestStoreTypes_ReportsRawUnrecognizedValueType(t *testing.T) {
	store, ctx, dir := openStoreWithCoreSchema(t)

	p := writeForeignSchemaOp(t, dir, "fedcba9876543210", "", "schema:newer", "create", map[string]any{"namespace": "newer"}, 0)
	p = writeForeignSchemaOp(t, dir, "fedcba9876543210", p, "schema:newer", "define-type", map[string]any{"type": "newer.gadget"}, 1)
	p = writeForeignSchemaOp(t, dir, "fedcba9876543210", p, "schema:newer", "define-op", map[string]any{"type": "newer.gadget", "op_type": "set-ident", "op_version": "1"}, 2)
	_ = writeForeignSchemaOp(t, dir, "fedcba9876543210", p, "schema:newer", "define-field", map[string]any{
		"type": "newer.gadget", "op_type": "set-ident", "op_version": "1",
		"field": "ident", "value_type": "x-uuid", "strategy": "lww",
	}, 3)

	if _, err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	types, err := store.Types(ctx)
	if err != nil {
		t.Fatalf("Store.Types failed: %v", err)
	}
	ty, ok := findSchemaType(types, "newer.gadget")
	if !ok {
		t.Fatalf("Store.Types: declared type %q missing from %+v", "newer.gadget", types)
	}
	if len(ty.Fields) != 1 || ty.Fields[0].Name != "ident" || ty.Fields[0].ValueType != "x-uuid" {
		t.Fatalf("Store.Types.Fields = %+v, want one field {ident, x-uuid} reported raw, undemoted", ty.Fields)
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

func compileTestSchema(t testing.TB, objectID, src string) []writ.Envelope {
	t.Helper()
	parsed, err := writ.ParseSchemaSource("writ.schema", []byte(src))
	if err != nil {
		t.Fatalf("writ.ParseSchemaSource failed: %v", err)
	}
	envs, err := parsed.Compile(objectID)
	if err != nil {
		t.Fatalf("parsed.Compile failed: %v", err)
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

	env := writ.Envelope{ObjectID: "sch-a", ObjectType: "widget", OpType: "create", OpVersion: 1, Body: []byte(`{}`)}
	if err := store.ApplySchema(context.Background(), []writ.Envelope{env}); err == nil {
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

	env := writ.Envelope{ObjectID: "sch-a", ObjectType: "schema", OpType: "create", OpVersion: 2, Body: []byte(`{"namespace":"acme"}`)}
	if err := store.ApplySchema(context.Background(), []writ.Envelope{env}); err == nil {
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

	envs := []writ.Envelope{
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
func schemaEnv(t *testing.T, objectID, opType string, body map[string]any) writ.Envelope {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return writ.Envelope{
		ObjectID:   objectID,
		ObjectType: "schema",
		OpType:     opType,
		OpVersion:  1,
		Body:       raw,
	}
}

func toCodecEnvelope(e writ.Envelope) codec.Envelope {
	return codec.Envelope{
		ObjectID:   e.ObjectID,
		ObjectType: e.ObjectType,
		OpType:     e.OpType,
		OpVersion:  e.OpVersion,
		Body:       e.Body,
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
func assertSchemaAfterApplyMatchesRealApply(t *testing.T, store *writ.Store, objectID string, delta []writ.Envelope) {
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
		delta := []writ.Envelope{
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
		initial := []writ.Envelope{
			schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
			schemaEnv(t, "schema:acme", "define-field", map[string]any{
				"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "title",
				"value_type": "string", "strategy": "lww",
			}),
		}
		if err := store.ApplySchema(ctx, initial); err != nil {
			t.Fatalf("initial ApplySchema failed: %v", err)
		}
		delta := []writ.Envelope{
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
		initial := []writ.Envelope{
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
		if err := store.ApplySchema(ctx, []writ.Envelope{
			schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
		}); err != nil {
			t.Fatalf("ApplySchema for the first object failed: %v", err)
		}
		delta := []writ.Envelope{
			schemaEnv(t, "schema:beta", "create", map[string]any{"namespace": "beta"}),
			schemaEnv(t, "schema:beta", "define-type", map[string]any{"type": "beta.retro"}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "schema:beta", delta)
	})

	t.Run("delta back to the first while the tip sits on the second", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		if err := store.ApplySchema(ctx, []writ.Envelope{
			schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"}),
		}); err != nil {
			t.Fatalf("ApplySchema for the first object failed: %v", err)
		}
		if err := store.ApplySchema(ctx, []writ.Envelope{
			schemaEnv(t, "schema:beta", "create", map[string]any{"namespace": "beta"}),
		}); err != nil {
			t.Fatalf("ApplySchema for the second object failed: %v", err)
		}
		delta := []writ.Envelope{
			schemaEnv(t, "schema:acme", "define-type", map[string]any{"type": "acme.standup"}),
		}
		assertSchemaAfterApplyMatchesRealApply(t, store, "schema:acme", delta)
	})

	t.Run("re-declared field with widened attributes", func(t *testing.T) {
		store := newStore(t)
		ctx := context.Background()
		initial := []writ.Envelope{
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
		delta := []writ.Envelope{
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
		initial := []writ.Envelope{
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
		delta := []writ.Envelope{
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
		createOp, err := dagA.Append(ctx, toCodecEnvelope(schemaEnv(t, "schema:acme", "create", map[string]any{"namespace": "acme"})), nil)
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
		if _, err := dagB.Append(ctx, toCodecEnvelope(schemaEnv(t, "schema:acme", "define-type", map[string]any{"type": "acme.standup"})), []string{createOp.ID}); err != nil {
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

		delta := []writ.Envelope{
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
	initial := []writ.Envelope{
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
	delta := []writ.Envelope{
		schemaEnv(t, objectID, "define-field", map[string]any{
			"type": "acme.standup", "op_type": "create", "op_version": "1", "field": "state",
			"value_type": "string", "strategy": "lww",
		}),
	}

	// "planned": the file's own declaration folded as though it were the
	// object's entire history — SchemaFromEnvelopes' documented contract,
	// and cmd/writ/schema.go's own name for this value.
	compiled := []writ.Envelope{
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
	// to do its job: SchemaConflicts must report a conflict for the honest
	// state (the stale enum makes the rule invalid) and none for the
	// file-alone state (which never carried it). A future buildSchemaPlan
	// that passed planned where honestPlanned belongs would see the second
	// outcome for both, and never learn that the apply it is about to make
	// would silently drop a field's rule.
	honestConflicts := writ.SchemaConflicts([]writ.Schema{honestPlanned})
	if len(honestConflicts) == 0 {
		t.Fatalf("expected SchemaConflicts to report the stale-enum invalid rule for the honest fold, got none")
	}
	plannedConflicts := writ.SchemaConflicts([]writ.Schema{planned})
	if len(plannedConflicts) != 0 {
		t.Fatalf("expected SchemaConflicts to report no conflicts for the clean file-alone fold, got %+v", plannedConflicts)
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

	schemaEnvs := []writ.Envelope{
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

// TestUndeclaredObjectTypeIsRefused is the undeclared tier (3): an
// object_type no schema in the log declares is refused.
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

	if err := store.ApplySchema(ctx, []writ.Envelope{
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
	if err := store.ApplySchema(ctx, []writ.Envelope{
		schemaEnv(t, "schema:rogue", "create", map[string]any{"namespace": "rogue"}),
		schemaEnv(t, "schema:rogue", "define-type", map[string]any{"type": "schema"}),
	}); err != nil {
		t.Fatalf("ApplySchema (attempted schema redefinition) failed: %v", err)
	}

	schemas, err := store.Schema(ctx)
	if err != nil {
		t.Fatalf("Store.Schema failed: %v", err)
	}
	vocabularies, err := writ.StoreVocabularies(store, ctx)
	if err != nil {
		t.Fatalf("StoreVocabularies failed: %v", err)
	}
	conflicts := writ.SchemaConflicts(schemas)
	if len(conflicts) == 0 {
		t.Fatalf("expected a conflict for the attempted redefinition of \"schema\", got none")
	}
	if _, ok := vocabularies["schema"]; ok {
		t.Fatalf("VocabulariesFromSchemas must never key \"schema\" at all, got %+v", vocabularies["schema"])
	}

	// A second, ordinary schema object write must still succeed: it is
	// itself object_type "schema", validated at tier 1 regardless of the
	// rogue redefinition attempt sitting in the log.
	if err := store.ApplySchema(ctx, []writ.Envelope{
		schemaEnv(t, "schema:ok", "create", map[string]any{"namespace": "ok"}),
	}); err != nil {
		t.Fatalf("ApplySchema (ordinary schema write) failed after a rogue redefinition attempt: %v", err)
	}

	// And a direct Append of a "schema" op through the store's own dag.Store
	// succeeds the same way.
	dagStore := writ.StoreDAGStore(store)
	if _, err := dagStore.Append(ctx, toCodecEnvelope(schemaEnv(t, "schema:ok", "define-type", map[string]any{"type": "widget"})), nil); err != nil {
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
	if err := store.ApplySchema(ctx, []writ.Envelope{
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

	if err := store.ApplySchema(ctx, []writ.Envelope{
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

	if err := store.ApplySchema(ctx, []writ.Envelope{
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
	if err := store.ApplySchema(ctx, []writ.Envelope{
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

	if err := store.ApplySchema(ctx, []writ.Envelope{
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
	if err := store.ApplySchema(ctx, []writ.Envelope{
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
	conflicts := writ.SchemaConflicts(schemas)
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts between acme.standup and other.standup, got %+v", conflicts)
	}
	types, err := store.Types(ctx)
	if err != nil {
		t.Fatalf("Store.Types failed: %v", err)
	}
	var hasAcme, hasOther bool
	for _, typ := range types {
		if typ.Name == "acme.standup" {
			hasAcme = true
		}
		if typ.Name == "other.standup" {
			hasOther = true
		}
	}
	if !hasAcme {
		t.Fatalf("expected type installed for acme.standup, got %+v", types)
	}
	if !hasOther {
		t.Fatalf("expected type installed for other.standup, got %+v", types)
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

// TestSchemaIndexNameShapesDoNotBrickReaders is WRIT-322's end-to-end repro.
// One legal schema in the pusher's own namespace used to generate two
// CREATE INDEX statements of the same name — type "a" with target "b_f_c"
// against type "a-f-b" with target "c" — SQLite refused the second,
// ApplySchema failed, and from then on every Refresh, every Query, and every
// fresh Open of a clone failed the same way, with nothing in the log
// removable to fix it. Both types must now be installed and queryable, here
// and on a fresh clone.
func TestSchemaIndexNameShapesDoNotBrickReaders(t *testing.T) {
	store, ctx, dir := openStoreWithCoreSchema(t)

	if _, err := store.Objects.Create(ctx, "acme.widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Still Queryable"},
	}); err != nil {
		t.Fatalf("Objects.Create(widget) failed: %v", err)
	}

	const src = `namespace bigco

type a {
  op create 1 {
    b_f_c string lww
  }
}

type a-f-b {
  op create 1 {
    c string lww
  }
}
`
	if err := store.ApplySchema(ctx, compileTestSchema(t, "schema:bigco", src)); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	if _, err := store.Refresh(ctx); err != nil {
		t.Fatalf("Refresh after the schema declaring both types failed: %v", err)
	}

	// A distinct needle per type: Text filters search the type's generated
	// columns, so a type withheld from the projection would not match its own
	// needle even though its object row exists.
	needles := map[string]string{"acme.widget": "Still Queryable", "bigco.a": "needle-a", "bigco.a-f-b": "needle-afb"}
	for typ, field := range map[string]string{"bigco.a": "b_f_c", "bigco.a-f-b": "c"} {
		if _, err := store.Objects.Create(ctx, typ, writ.NewOp{
			Type:   "create",
			Fields: map[string]any{field: needles[typ]},
		}); err != nil {
			t.Fatalf("Objects.Create(%s) failed: %v", typ, err)
		}
	}

	assertQueryable := func(label string, s *writ.Store) {
		t.Helper()
		for typ, needle := range needles {
			got, err := s.Query.Objects(writ.ObjectFilter{Type: []string{typ}, Text: needle})
			if err != nil {
				t.Fatalf("%s: Query.Objects(%s) failed: %v", label, typ, err)
			}
			if len(got) != 1 {
				t.Fatalf("%s: Query.Objects(%s) = %d objects, want 1: %+v", label, typ, len(got), got)
			}
		}
	}
	assertQueryable("writer", store)

	// A fresh clone has no cache: Open runs ApplySchema over the whole log.
	cloneDir := t.TempDir()
	runGitCmd(t, cloneDir, "init")
	runGitCmd(t, cloneDir, "config", "user.name", "Bob Test")
	runGitCmd(t, cloneDir, "config", "user.email", "bob@example.com")
	runGitCmd(t, cloneDir, "config", "writ.writerId", "fedcba9876543210")
	runGitCmd(t, cloneDir, "fetch", dir, "+refs/writ/*:refs/writ/*")
	clone, err := writ.Open(cloneDir)
	if err != nil {
		t.Fatalf("writ.Open on a fresh clone failed: %v", err)
	}
	defer clone.Close()
	assertQueryable("fresh clone", clone)
}
