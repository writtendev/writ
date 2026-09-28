package writ_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestEverySchemaConflictLiteralSetsKind is a static guard, not a golden
// one: TestSchemaDrivenFoldFamily's schemaConflictKinds check
// (spec/fixtures/schemadriven_test.go) only ever sees a construction site
// that some fixture's schema actually reaches, so a new SchemaConflict{...}
// literal that forgets Kind can sit unreached by any corpus fixture and pass
// every golden test regardless (WRIT-335). This test instead parses every
// non-test .go file in this package and fails if any SchemaConflict{...}
// composite literal omits a Kind: key, independent of whether any fixture
// ever reaches it.
func TestEverySchemaConflictLiteralSetsKind(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var files []*ast.File
	seen := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", name, err)
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		t.Fatal("no non-test .go files found in directory")
	}

	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			ident, ok := lit.Type.(*ast.Ident)
			if !ok || ident.Name != "SchemaConflict" {
				return true
			}
			seen++
			hasKind := false
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Kind" {
					hasKind = true
					break
				}
			}
			if !hasKind {
				pos := fset.Position(lit.Pos())
				t.Errorf("%s: SchemaConflict{...} literal has no Kind: key (spec/schema-ops.md §6: Kind is always set)", pos)
			}
			return true
		})
	}
	if seen == 0 {
		t.Fatal("found no SchemaConflict{...} composite literal to check -- test is vacuous, engine/schema.go must have moved or been renamed")
	}
}
