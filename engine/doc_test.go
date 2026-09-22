package writ_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestDocCompleteness(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	files := map[string]*ast.File{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", name, err)
		}
		files[name] = file
	}
	if len(files) == 0 {
		t.Fatal("no non-test .go files found in directory")
	}

	// Check package level doc in at least one file
	hasPkgDoc := false
	for _, file := range files {
		if file.Doc != nil && strings.TrimSpace(file.Doc.Text()) != "" {
			hasPkgDoc = true
			break
		}
	}
	if !hasPkgDoc {
		t.Errorf("Package writ lacks documentation comment")
	}

	// Inspect all AST declarations across all files
	for filename, file := range files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if ast.IsExported(d.Name.Name) {
					// Check if receiver (method) is on an unexported type
					if d.Recv != nil && len(d.Recv.List) > 0 {
						recvType := d.Recv.List[0].Type
						if star, ok := recvType.(*ast.StarExpr); ok {
							recvType = star.X
						}
						if ident, ok := recvType.(*ast.Ident); ok {
							if !ast.IsExported(ident.Name) {
								continue
							}
						}
					}
					if d.Doc == nil || strings.TrimSpace(d.Doc.Text()) == "" {
						t.Errorf("%s: Exported function/method %s lacks doc comment", filename, d.Name.Name)
					}
				}

			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if ast.IsExported(s.Name.Name) {
							hasDoc := (s.Doc != nil && strings.TrimSpace(s.Doc.Text()) != "") ||
								(d.Doc != nil && strings.TrimSpace(d.Doc.Text()) != "")
							if !hasDoc {
								t.Errorf("%s: Exported type %s lacks doc comment", filename, s.Name.Name)
							}
						}

					case *ast.ValueSpec:
						for _, name := range s.Names {
							if ast.IsExported(name.Name) {
								hasDoc := (s.Doc != nil && strings.TrimSpace(s.Doc.Text()) != "") ||
									(d.Doc != nil && strings.TrimSpace(d.Doc.Text()) != "")
								if !hasDoc {
									t.Errorf("%s: Exported identifier %s lacks doc comment", filename, name.Name)
								}
							}
						}
					}
				}
			}
		}
	}
}
