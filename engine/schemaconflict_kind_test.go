package writ_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// schemaConflictKindIssue is one construction site checkSchemaConflictKinds
// flags: a SchemaConflict{...} composite literal -- named, or elided-type
// inside a []SchemaConflict{...} slice literal -- that omits Kind: or sets
// it to the empty string.
type schemaConflictKindIssue struct {
	pos token.Position
	msg string
}

// checkSchemaConflictKinds walks file and returns one issue per
// SchemaConflict composite literal that has no Kind: key or sets
// Kind: "", plus how many such literals it examined. The empty string is
// never a valid SchemaConflictKind (spec/schema-ops.md §6's closed
// catalogue), so it is exactly as unpinned a construction site as no Kind
// key at all.
//
// A literal counts whether it spells out its own type
// (SchemaConflict{...}) or elides it as an element of a
// []SchemaConflict{...} slice literal ({...} inside the slice, Go
// inferring SchemaConflict for it) -- go/ast gives the two shapes
// different nodes (the outer form's lit.Type is an *ast.Ident, the inner
// form's is nil), so a check that only recognizes the first misses a
// SchemaConflict{...} written inside a slice literal entirely, which
// round 2's review flagged as reachable and not yet covered.
//
// Shared by TestEverySchemaConflictLiteralSetsKind (the real
// engine/*.go guard) and the synthetic-source tests below, which pin
// that shared logic catches an elided-type slice element and an
// explicit empty Kind without hand-editing and reverting production code
// each round to prove it.
func checkSchemaConflictKinds(fset *token.FileSet, file *ast.File) (issues []schemaConflictKindIssue, examined int) {
	check := func(lit *ast.CompositeLit) {
		examined++
		hasKind := false
		empty := false
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Kind" {
				continue
			}
			hasKind = true
			if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING && bl.Value == `""` {
				empty = true
			}
			break
		}
		switch {
		case !hasKind:
			issues = append(issues, schemaConflictKindIssue{fset.Position(lit.Pos()), "SchemaConflict{...} literal has no Kind: key (spec/schema-ops.md §6: Kind is always set)"})
		case empty:
			issues = append(issues, schemaConflictKindIssue{fset.Position(lit.Pos()), "SchemaConflict{...} literal sets Kind: \"\" (spec/schema-ops.md §6: the empty string is not a member of the closed kind catalogue)"})
		}
	}

	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}

		if ident, ok := lit.Type.(*ast.Ident); ok && ident.Name == "SchemaConflict" {
			check(lit)
			return true
		}

		// []SchemaConflict{{...}, {...}}: each element is itself a
		// CompositeLit, but with an elided Type (nil) -- the slice
		// literal's own Elt type supplies SchemaConflict, so the branch
		// above never matches it as a "SchemaConflict{...}" literal on
		// its own. Every element here is exactly as much a construction
		// site as a spelled-out one.
		if arr, ok := lit.Type.(*ast.ArrayType); ok {
			if eltIdent, ok := arr.Elt.(*ast.Ident); ok && eltIdent.Name == "SchemaConflict" {
				for _, e := range lit.Elts {
					if elit, ok := e.(*ast.CompositeLit); ok {
						check(elit)
					}
				}
			}
		}
		return true
	})

	return issues, examined
}

// TestEverySchemaConflictLiteralSetsKind is a static guard, not a golden
// one: TestSchemaDrivenFoldFamily's schemaConflictKinds check
// (spec/fixtures/schemadriven_test.go) only ever sees a construction site
// that some fixture's schema actually reaches, so a new SchemaConflict{...}
// literal that forgets Kind -- or sets it to "" -- can sit unreached by
// any corpus fixture and pass every golden test regardless (WRIT-335).
// This test instead parses every non-test .go file in this package and
// fails if any SchemaConflict{...} composite literal, spelled out or
// elided inside a []SchemaConflict{...} slice literal, omits a Kind: key
// or sets one to the empty string.
func TestEverySchemaConflictLiteralSetsKind(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	var files []*ast.File
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

	seen := 0
	for _, file := range files {
		issues, examined := checkSchemaConflictKinds(fset, file)
		seen += examined
		for _, issue := range issues {
			t.Errorf("%s: %s", issue.pos, issue.msg)
		}
	}
	if seen == 0 {
		t.Fatal("found no SchemaConflict{...} composite literal to check -- test is vacuous, engine/schema.go must have moved or been renamed")
	}
}

// TestSchemaConflictKindGuardCatchesElidedSliceElement is a mutation proof
// for the []SchemaConflict{{...}} branch checkSchemaConflictKinds added in
// round 2: a slice element that elides its own type and never sets Kind
// must still be reported, on synthetic source so this pins the checker's
// own behavior rather than hand-editing and reverting engine/schema.go
// every round.
func TestSchemaConflictKindGuardCatchesElidedSliceElement(t *testing.T) {
	const src = `package writ

func f() []SchemaConflict {
	return []SchemaConflict{
		{ObjectType: "acme.gadget"},
	}
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	issues, examined := checkSchemaConflictKinds(fset, file)
	if examined != 1 {
		t.Fatalf("examined = %d, want 1 elided-type slice element found", examined)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1 (elided-type slice element missing Kind): %+v", len(issues), issues)
	}
}

// TestSchemaConflictKindGuardAllowsElidedSliceElementWithKind is the
// control for the mutation above: the same elided-type slice shape, but
// with Kind set, must report nothing -- otherwise the elided-type
// detection itself would be a false-positive machine, flagging every
// []SchemaConflict{...} literal regardless of content.
func TestSchemaConflictKindGuardAllowsElidedSliceElementWithKind(t *testing.T) {
	const src = `package writ

func f() []SchemaConflict {
	return []SchemaConflict{
		{Kind: SchemaConflictRuleInvalid, ObjectType: "acme.gadget"},
	}
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	issues, examined := checkSchemaConflictKinds(fset, file)
	if examined != 1 {
		t.Fatalf("examined = %d, want 1 elided-type slice element found", examined)
	}
	if len(issues) != 0 {
		t.Fatalf("got %d issues, want 0: %+v", len(issues), issues)
	}
}

// TestSchemaConflictKindGuardCatchesEmptyKind is a mutation proof for the
// explicit Kind: "" branch checkSchemaConflictKinds added in round 2: a
// literal that sets Kind but to the empty string must still be reported,
// the same "unpinned construction site" as no Kind key at all.
func TestSchemaConflictKindGuardCatchesEmptyKind(t *testing.T) {
	const src = `package writ

func f() SchemaConflict {
	return SchemaConflict{Kind: ""}
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	issues, examined := checkSchemaConflictKinds(fset, file)
	if examined != 1 {
		t.Fatalf("examined = %d, want 1 SchemaConflict{...} literal found", examined)
	}
	if len(issues) != 1 {
		t.Fatalf("got %d issues, want 1 (explicit empty Kind): %+v", len(issues), issues)
	}
}
