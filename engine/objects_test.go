package writ_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
)

// waypointSchemaSrc declares "waypoint": an object type writ.Open's engine
// has no Go type, typed reducer, or projection reader for at all — the type
// this file's round-trip test exercises Objects.Create/Apply/Get and
// Query.Objects against, with no Go type named after it anywhere.
const waypointSchemaSrc = `namespace acme
description "A type writ has never heard of"

type waypoint {
  description "A location marker"

  op mark 1 {
    title  string(200)  lww
    note   text         lww
  }

  op tag 1 {
    add     [string]  set-observed-remove  target(tags)
    remove  [string]  set-observed-remove  target(tags)
  }
}
`

// TestObjectsCreateApplyGetRoundTrip_NeverHeardOfType is the ticket's
// central acceptance test: a schema declares an object type through
// writ.schema that writ's engine has never heard of (no Go struct, no
// typed reducer, no typed reader — "waypoint" appears nowhere in Go source
// outside this test file), and the generic Objects API round-trips it —
// Create, then Apply, then Get — with Query.Objects finding it by type and
// full-text search. Since WRIT-194 that is the only kind of type there is:
// `schema` aside, every object type comes from the log.
func TestObjectsCreateApplyGetRoundTrip_NeverHeardOfType(t *testing.T) {
	ctx := context.Background()
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	envs := compileTestSchema(t, "sch-waypoint", waypointSchemaSrc)
	if err := store.ApplySchema(ctx, envs); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Create: objectType and op.Type both come from the schema; Version 0
	// resolves to the type's one declared "mark" version.
	id, err := store.Objects.Create(ctx, "waypoint", writ.NewOp{
		Type: "mark",
		Fields: map[string]any{
			"title": "Basecamp",
			"note":  "Start here",
		},
	})
	if err != nil {
		t.Fatalf("Objects.Create failed: %v", err)
	}
	if id == "" {
		t.Fatal("Objects.Create returned an empty object id")
	}

	// Apply: a further op against the object just created. "add" is the
	// body field name tag.1 declares; the OR-set pair collapses onto one
	// target, "tags" (WRIT-198), which is what Get below must read back
	// under — not "add".
	if err := store.Objects.Apply(ctx, id, writ.NewOp{
		Type:   "tag",
		Fields: map[string]any{"add": []string{"beta", "alpha"}},
	}); err != nil {
		t.Fatalf("Objects.Apply failed: %v", err)
	}

	obj, err := store.Objects.Get(ctx, id)
	if err != nil {
		t.Fatalf("Objects.Get failed: %v", err)
	}
	if obj.ObjectID != id {
		t.Errorf("ObjectID = %q, want %q", obj.ObjectID, id)
	}
	if obj.ObjectType != "waypoint" {
		t.Errorf("ObjectType = %q, want %q", obj.ObjectType, "waypoint")
	}
	if obj.Fields["title"] != "Basecamp" {
		t.Errorf("Fields[title] = %v, want Basecamp", obj.Fields["title"])
	}
	if obj.Fields["note"] != "Start here" {
		t.Errorf("Fields[note] = %v, want %q", obj.Fields["note"], "Start here")
	}
	if _, ok := obj.Fields["add"]; ok {
		t.Errorf("Fields carries write-side key %q; NewOp.Fields' field name must not survive into the read-side target key", "add")
	}
	tags, ok := obj.Fields["tags"].([]string)
	if !ok || !reflect.DeepEqual(tags, []string{"alpha", "beta"}) {
		t.Errorf("Fields[tags] = %#v, want []string{alpha, beta}", obj.Fields["tags"])
	}
	if len(obj.UnknownOps) != 0 {
		t.Errorf("UnknownOps = %+v, want none — both ops are fully declared", obj.UnknownOps)
	}

	// Query.Objects finds it by type and by full-text search over the
	// generic, descriptor-driven Text clause (engine/projection/query.go).
	results, err := store.Query.Objects(writ.ObjectFilter{
		Type: []string{"waypoint"},
		Text: "Basecamp",
	})
	if err != nil {
		t.Fatalf("Query.Objects failed: %v", err)
	}
	if len(results) != 1 || results[0].ObjectID != id {
		t.Fatalf("Query.Objects(Type: waypoint, Text: Basecamp) = %+v, want [%s]", results, id)
	}
}

// TestObjectsGet_FieldsSurviveRebuildAndCacheDeletion pins that Objects.Get
// reads the DAG, never the projection (Store.Schema sets the precedent, for
// the same reason: the projection is a droppable cache): the same object
// folds to the same Fields whether the projection cache is freshly rebuilt
// or entirely absent from disk.
func TestObjectsGet_FieldsSurviveRebuildAndCacheDeletion(t *testing.T) {
	ctx := context.Background()
	dir, _ := setupConfiguredRepo(t)
	cacheDir := t.TempDir()

	store, err := writ.Open(dir, writ.WithSigner(dummySigner()), writ.WithCacheDir(cacheDir))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	applyCoreSchema(t, ctx, store)

	id, err := store.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Cache independence"},
	})
	if err != nil {
		t.Fatalf("Objects.Create failed: %v", err)
	}

	before, err := store.Objects.Get(ctx, id)
	if err != nil {
		t.Fatalf("Objects.Get (before) failed: %v", err)
	}

	if _, err := store.Rebuild(ctx); err != nil {
		t.Fatalf("Store.Rebuild failed: %v", err)
	}
	afterRebuild, err := store.Objects.Get(ctx, id)
	if err != nil {
		t.Fatalf("Objects.Get (after Rebuild) failed: %v", err)
	}
	if !reflect.DeepEqual(before.Fields, afterRebuild.Fields) {
		t.Fatalf("Fields changed after Store.Rebuild: before %#v, after %#v", before.Fields, afterRebuild.Fields)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// The cache file may simply be deleted (Store.Rebuild's own doc
	// comment) — reopening against an empty cacheDir proves Get depends on
	// none of it, since nothing here ever calls Refresh/Rebuild again.
	store2, err := writ.Open(dir,
		writ.WithSigner(dummySigner()),
		writ.WithCacheDir(t.TempDir()),
		writ.WithoutAutoRefresh(),
	)
	if err != nil {
		t.Fatalf("re-Open with a fresh cache dir failed: %v", err)
	}
	defer store2.Close()

	afterFreshCache, err := store2.Objects.Get(ctx, id)
	if err != nil {
		t.Fatalf("Objects.Get (fresh cache) failed: %v", err)
	}
	if !reflect.DeepEqual(before.Fields, afterFreshCache.Fields) {
		t.Fatalf("Fields changed with an empty projection cache: before %#v, after %#v", before.Fields, afterFreshCache.Fields)
	}
}

// TestObjectsCreate_AmbiguousVersionRefused pins NewOp.Version's zero-value
// resolution: it is unambiguous only when the object type declares exactly
// one version of the named op type.
func TestObjectsCreate_AmbiguousVersionRefused(t *testing.T) {
	const src = `namespace acme
description "A type declaring two versions of one op"

type widgetv {
  op make 1 {
    title  string(50)  lww
  }

  op make 2 {
    title  string(100)  lww
  }
}
`
	ctx := context.Background()
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	envs := compileTestSchema(t, "sch-widgetv", src)
	if err := store.ApplySchema(ctx, envs); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	if _, err := store.Objects.Create(ctx, "widgetv", writ.NewOp{
		Type:   "make",
		Fields: map[string]any{"title": "ambiguous"},
	}); err == nil {
		t.Fatal("Objects.Create with an ambiguous op version: expected an error, got none")
	} else if !strings.Contains(err.Error(), "specify NewOp.Version") {
		t.Fatalf("Objects.Create with an ambiguous op version: error %q does not name the fix", err.Error())
	}

	id, err := store.Objects.Create(ctx, "widgetv", writ.NewOp{
		Type:    "make",
		Version: 2,
		Fields:  map[string]any{"title": "explicit version"},
	})
	if err != nil {
		t.Fatalf("Objects.Create with an explicit version failed: %v", err)
	}
	obj, err := store.Objects.Get(ctx, id)
	if err != nil {
		t.Fatalf("Objects.Get failed: %v", err)
	}
	if obj.Fields["title"] != "explicit version" {
		t.Errorf("Fields[title] = %v, want %q", obj.Fields["title"], "explicit version")
	}
}

// TestObjectsApply_TargetKeyDiffersFromWriteField pins the read-side half
// of WRIT-198 against a schema whose add/remove pair declares an explicit
// target: coreSchemaSrc collapses widget's assign.add/assign.remove onto
// "assignees", so a caller writing NewOp.Fields["add"] must read the same
// data back from Object.Fields["assignees"], never Object.Fields["add"].
func TestObjectsApply_TargetKeyDiffersFromWriteField(t *testing.T) {
	store, ctx, _ := openStoreWithCoreSchema(t)

	id, err := store.Objects.Create(ctx, "widget", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Target key example"},
	})
	if err != nil {
		t.Fatalf("Objects.Create failed: %v", err)
	}

	if err := store.Objects.Apply(ctx, id, writ.NewOp{
		Type:   "assign",
		Fields: map[string]any{"add": []string{"user:alice"}},
	}); err != nil {
		t.Fatalf("Objects.Apply failed: %v", err)
	}

	obj, err := store.Objects.Get(ctx, id)
	if err != nil {
		t.Fatalf("Objects.Get failed: %v", err)
	}
	if _, ok := obj.Fields["add"]; ok {
		t.Error("Fields carries write-side key \"add\"; must read back under the target key \"assignees\" instead")
	}
	assignees, ok := obj.Fields["assignees"].([]string)
	if !ok || len(assignees) != 1 || assignees[0] != "user:alice" {
		t.Errorf("Fields[assignees] = %#v, want [user:alice]", obj.Fields["assignees"])
	}
}

// TestObjectsGet_NotFound pins Get's ErrNotFound for an object id with no
// ops at all.
func TestObjectsGet_NotFound(t *testing.T) {
	ctx := context.Background()
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	if _, err := store.Objects.Get(ctx, "nonexistent-object-id"); err != writ.ErrNotFound {
		t.Fatalf("Objects.Get(nonexistent): err = %v, want ErrNotFound", err)
	}
}

// TestObjectsCreate_ValidationErrors pins Create/Apply's own basic argument
// checks — object type, op type, and object id must be non-empty — which
// run before anything reaches dagStore.Append.
func TestObjectsCreate_ValidationErrors(t *testing.T) {
	ctx := context.Background()
	dir, _ := setupConfiguredRepo(t)
	store, err := writ.Open(dir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	if _, err := store.Objects.Create(ctx, "", writ.NewOp{Type: "create"}); err == nil {
		t.Error("Objects.Create with an empty object type: expected an error, got none")
	}
	if _, err := store.Objects.Create(ctx, "widget", writ.NewOp{}); err == nil {
		t.Error("Objects.Create with an empty op type: expected an error, got none")
	}
	if err := store.Objects.Apply(ctx, "", writ.NewOp{Type: "update"}); err == nil {
		t.Error("Objects.Apply with an empty object id: expected an error, got none")
	}
	if err := store.Objects.Apply(ctx, "some-id", writ.NewOp{}); err == nil {
		t.Error("Objects.Apply with an empty op type: expected an error, got none")
	}
	if _, err := store.Objects.Get(ctx, ""); err == nil {
		t.Error("Objects.Get with an empty object id: expected an error, got none")
	}
}

// TestObjectsCreate_DeclaredTypeWithNoFieldsIsCreatable is WRIT-192 round 1
// MAJOR-2: a type declared by define-op alone (no define-field at all) is
// exactly the shape TestDeclaredTypeWithNoFieldsIsWritable (schema_test.go)
// already pins the producer accepts — Objects.Create with Version 0 must
// resolve and accept it too, not refuse with "is not declared by the
// installed vocabulary" when it demonstrably is. resolveOpVersion
// (engine/objects.go) answers that from Store.Types, so this is really a
// Store.Types test: a define-op-only type must appear in Types with its op,
// even though no rule (field) index entry names it.
func TestObjectsCreate_DeclaredTypeWithNoFieldsIsCreatable(t *testing.T) {
	store, ctx := openWritableStore(t)

	schemaEnvs := []codec.Envelope{
		schemaEnv(t, "sch-widget", "create", map[string]any{"namespace": "acme"}),
		schemaEnv(t, "sch-widget", "define-type", map[string]any{"type": "widget"}),
		schemaEnv(t, "sch-widget", "define-op", map[string]any{"type": "widget", "op_type": "create", "op_version": "1"}),
	}
	if err := store.ApplySchema(ctx, schemaEnvs); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	types, err := store.Types(ctx)
	if err != nil {
		t.Fatalf("Store.Types failed: %v", err)
	}
	var widget *writ.SchemaType
	for i := range types {
		if types[i].Name == "widget" {
			widget = &types[i]
		}
	}
	if widget == nil {
		t.Fatalf("Store.Types does not list define-op-only type %q at all: %+v", "widget", types)
	}
	if len(widget.Ops) != 1 || widget.Ops[0].OpType != "create" || widget.Ops[0].OpVersion != 1 {
		t.Fatalf("widget.Ops = %+v, want exactly [{OpType: create, OpVersion: 1}]", widget.Ops)
	}

	id, err := store.Objects.Create(ctx, "widget", writ.NewOp{Type: "create"})
	if err != nil {
		t.Fatalf("Objects.Create on a define-op-only declared type: %v", err)
	}

	// Get must not error either: define-op alone installs no fold rule
	// (RulesFromSchemas' rules are per-field, spec/fold.md §7), so the op
	// correctly folds to UnknownOp — the write/read asymmetry
	// TestContestedObjectTypeStaysWritable documents elsewhere for a
	// contested type applies here for the same reason. What MAJOR-2 is
	// about is Create refusing the write at all with a false "not declared"
	// error; that Get then reports the op as unknown is the correct,
	// unrelated forward-compatibility answer, not a regression to assert
	// against.
	obj, err := store.Objects.Get(ctx, id)
	if err != nil {
		t.Fatalf("Objects.Get failed: %v", err)
	}
	if obj.ObjectType != "widget" {
		t.Errorf("ObjectType = %q, want %q", obj.ObjectType, "widget")
	}
}
