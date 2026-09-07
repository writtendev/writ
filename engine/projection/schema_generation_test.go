package projection_test

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/engine/state"
)

// TestNoSDLCTypeInGeneratedDDL makes the ticket's whole premise executable:
// no CREATE TABLE string literal anywhere in the package's non-test sources
// names an SDLC vocabulary type. It parses every non-test .go file in this
// directory and inspects string literals directly (not comments, not
// identifiers) — the DDL generator builds its CREATE TABLE text with a
// strings.Builder over descriptor-supplied names, so this should hold
// trivially, but the test pins it so a future hand-written literal creeps
// in loudly rather than silently.
func TestNoSDLCTypeInGeneratedDDL(t *testing.T) {
	forbidden := []string{
		"review", "issue", "comment", "project", "cycle",
		"document", "section", "label", "workflow-state", "workflow_state", "settings",
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	for _, pkg := range pkgs {
		for filename, file := range pkg.Files {
			if strings.HasSuffix(filename, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val, err := strconv.Unquote(lit.Value)
				if err != nil {
					// Raw (backtick) strings unquote fine via strconv too;
					// anything that fails to unquote isn't a plain string
					// literal DDL text could live in.
					return true
				}
				if !strings.Contains(strings.ToUpper(val), "CREATE TABLE") {
					return true
				}
				lower := strings.ToLower(val)
				for _, f := range forbidden {
					if strings.Contains(lower, f) {
						t.Errorf("%s: CREATE TABLE string literal mentions SDLC type %q: %.200s", filename, f, val)
					}
				}
				return true
			})
		}
	}
}

func tableExists(t *testing.T, db *projection.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&n); err != nil {
		t.Fatalf("check table %s: %v", name, err)
	}
	return n > 0
}

// TestSchemaChangeDropsAndRebuilds is the droppable-cache answer, tested
// directly at the ApplySchema level: applying schema A creates its table,
// applying schema B (a different object type entirely) drops A's table,
// creates B's, and records needs_rebuild so the next Refresh takes the
// full-rebuild path.
func TestSchemaChangeDropsAndRebuilds(t *testing.T) {
	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	rulesA := map[string][]state.Rule{
		"widget": {{OpType: "create", Field: "name", Strategy: "lww", ValueType: "string", ObjectType: "widget"}},
	}
	if err := db.ApplySchema(rulesA); err != nil {
		t.Fatalf("ApplySchema A: %v", err)
	}
	if !tableExists(t, db, "o_widget") {
		t.Fatalf("expected o_widget to exist after applying schema A")
	}

	rulesB := map[string][]state.Rule{
		"gadget": {{OpType: "create", Field: "name", Strategy: "lww", ValueType: "string", ObjectType: "gadget"}},
	}
	if err := db.ApplySchema(rulesB); err != nil {
		t.Fatalf("ApplySchema B: %v", err)
	}
	if tableExists(t, db, "o_widget") {
		t.Fatalf("expected o_widget to be dropped after applying schema B")
	}
	if !tableExists(t, db, "o_gadget") {
		t.Fatalf("expected o_gadget to exist after applying schema B")
	}

	var needsRebuild string
	if err := db.DB().QueryRow("SELECT value FROM meta WHERE key = 'needs_rebuild'").Scan(&needsRebuild); err != nil {
		t.Fatalf("query needs_rebuild: %v", err)
	}
	if needsRebuild != "1" {
		t.Fatalf("expected needs_rebuild = '1' after a schema change, got %q", needsRebuild)
	}

	// Applying A again is a no-op equal digest, but resurrects o_widget
	// (dropped above) via its own drop/recreate cycle.
	if err := db.ApplySchema(rulesA); err != nil {
		t.Fatalf("ApplySchema A again: %v", err)
	}
	if !tableExists(t, db, "o_widget") || tableExists(t, db, "o_gadget") {
		t.Fatalf("expected re-applying schema A to restore o_widget and drop o_gadget")
	}
}

func labelCreateEnv(objectID, name string) codec.Envelope {
	body, _ := json.Marshal(map[string]any{"name": name})
	env := codec.Envelope{ObjectID: objectID, ObjectType: "label", OpType: "create", OpVersion: 1, Body: body}
	raw, _ := codec.EncodePayload(env)
	env.Raw = raw
	return env
}

// withoutType returns a copy of rules with objectType removed — simulating
// a projection instance whose resolved schema does not (yet, or no longer)
// declare a type the log's producer vocabulary is perfectly happy to
// accept: RulesFromSchemas and the embedded producer vocabulary are
// resolved independently, so a real gap between them (a fetch landing new
// objects before this process re-resolves, or an object type a schema
// stopped declaring) is exactly what this simulates without needing to
// smuggle a truly undeclared object type past dag.Store.Append's own
// producer validation.
func withoutType(rules map[string][]state.Rule, objectType string) map[string][]state.Rule {
	out := make(map[string][]state.Rule, len(rules))
	for t, rs := range rules {
		if t == objectType {
			continue
		}
		out[t] = rs
	}
	return out
}

// TestUndeclaredTypeOpsLandInUnknownOps covers both halves of "nothing
// declares this type" for the projection's own resolution: a real,
// producer-valid object type this projection instance's schema simply does
// not mention, and — even when the full built-in vocabulary is installed —
// a `schema` object itself, which is excluded from the rule index by
// construction (spec/schema-ops.md §1.2) and must keep landing in
// unknown_ops rather than being picked up as an ordinary declared type.
func TestUndeclaredTypeOpsLandInUnknownOps(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	env := labelCreateEnv("lbl-1", "Label One")
	if _, err := store.Append(ctx, env, nil); err != nil {
		t.Fatalf("Append label op: %v", err)
	}

	schemaEnv := codec.Envelope{ObjectID: "sch-1", ObjectType: "schema", OpType: "create", OpVersion: 1, Body: json.RawMessage(`{"namespace":"test-ns"}`)}
	raw, _ := codec.EncodePayload(schemaEnv)
	schemaEnv.Raw = raw
	if _, err := store.Append(ctx, schemaEnv, nil); err != nil {
		t.Fatalf("Append schema op: %v", err)
	}

	rules := withoutType(testRules(), "label")
	if _, err := db.Refresh(store, projection.WithSchema(rules)); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	for _, objID := range []string{"lbl-1", "sch-1"} {
		var count int
		if err := db.DB().QueryRow("SELECT COUNT(*) FROM unknown_ops WHERE object_id = ?", objID).Scan(&count); err != nil {
			t.Fatalf("query unknown_ops for %s: %v", objID, err)
		}
		if count != 1 {
			t.Errorf("expected 1 unknown_ops row for %s, got %d", objID, count)
		}
	}

	if tableExists(t, db, "o_label") {
		t.Errorf("expected no o_label table: this projection instance's schema does not declare label")
	}
}

// TestSchemaShrinkMovesOpsToUnknownOps: a type removed from the rule index
// loses its table, and on the rebuild its ops reappear in unknown_ops.
// Nothing in the log is lost; the cache simply stops interpreting what
// nothing declares.
func TestSchemaShrinkMovesOpsToUnknownOps(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	defer db.Close()

	env := labelCreateEnv("lbl-1", "Label One")
	if _, err := store.Append(ctx, env, nil); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if _, err := db.Refresh(store, projection.WithSchema(testRules())); err != nil {
		t.Fatalf("Refresh with label declared: %v", err)
	}
	if !tableExists(t, db, "o_label") {
		t.Fatalf("expected o_label to exist")
	}
	var name string
	if err := db.DB().QueryRow("SELECT f_name FROM o_label WHERE object_id = ?", "lbl-1").Scan(&name); err != nil {
		t.Fatalf("query o_label: %v", err)
	}
	if name != "Label One" {
		t.Fatalf("expected name 'Label One', got %q", name)
	}
	var before int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM unknown_ops WHERE object_id = ?", "lbl-1").Scan(&before); err != nil {
		t.Fatalf("query unknown_ops before shrink: %v", err)
	}
	if before != 0 {
		t.Fatalf("expected 0 unknown_ops rows for lbl-1 while declared, got %d", before)
	}

	// Shrink: this projection instance's schema no longer declares "label".
	if _, err := db.Refresh(store, projection.WithSchema(withoutType(testRules(), "label"))); err != nil {
		t.Fatalf("Refresh with label removed: %v", err)
	}

	if tableExists(t, db, "o_label") {
		t.Fatalf("expected o_label to be dropped once nothing declares it")
	}
	var after int
	if err := db.DB().QueryRow("SELECT COUNT(*) FROM unknown_ops WHERE object_id = ?", "lbl-1").Scan(&after); err != nil {
		t.Fatalf("query unknown_ops after shrink: %v", err)
	}
	if after != 1 {
		t.Fatalf("expected lbl-1's op to reappear in unknown_ops after the shrink, got %d rows", after)
	}
}
