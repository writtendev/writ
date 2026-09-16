package anchorshape_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// allowedImports is empty because anchorshape.go itself imports nothing at
// all today, not even the standard library — SideWellFormed and
// RangeContextWellFormed are pure operations over Go's generic JSON
// representation (map[string]any, []any, string, float64, bool, nil) and
// built-in int/float64 arithmetic. This test is the fence for the package
// doc comment's "This package is stdlib-only" claim (engine/internal/value
// and engine/resolve both rely on that claim to admit anchorshape into
// their own no-I/O fences: engine/resolve/imports_test.go and
// engine/internal/fold/imports_test.go's transitive-closure comment): a
// future change that needs a new import must add it here, and the review
// that approves the addition can then judge whether it is really
// standard-library-only, rather than the claim drifting from what the code
// actually does with nothing to catch it (WRIT-252 round 3 finding).
var allowedImports = map[string]bool{}

func TestImportsAllowlist(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	for _, pkg := range pkgs {
		for filename, file := range pkg.Files {
			if strings.HasSuffix(filename, "_test.go") {
				continue
			}
			for _, imp := range file.Imports {
				pathVal := imp.Path.Value
				if !allowedImports[pathVal] {
					t.Errorf("%s imports forbidden package %s (anchorshape must stay stdlib-only, per its own doc comment and the fence both engine/internal/value and engine/resolve rely on)", filepath.Base(filename), pathVal)
				}
			}
		}
	}
}
