package fold_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// engine/internal/value is on this list for the closed value-type catalogue
// (spec/value-types.md) fold applies: NormalizesValue/Items/Key on Rule
// decide *whether* a position normalizes, and value.Normalize does the one
// normalization the catalogue defines (person-ref, delegating to
// engine/internal/person). "strings" itself is off the list: no non-test file
// here imports it any more.
//
// WRIT-185 moved person behind value: no non-test file here imports person
// directly any more. What WRIT-117 recorded about person's reach still
// applies transitively, because value imports nothing but person and the
// standard library:
//
//   - person imports golang.org/x/text/unicode/norm and
//     golang.org/x/text/cases, because the normalization rule
//     spec/identifiers.md pins is defined over Unicode tables that neither
//     the standard library nor this repository carries. Neither x/text
//     package performs I/O: both are table-driven computation over generated
//     Unicode data — no filesystem, no network, no processes, no clock.
//     cases additionally reaches x/text/language, itself table lookup over
//     language tags.
//   - The transitive closure through value and person does contain os,
//     reached through fmt, which x/text and value's own error messages both
//     use for formatting. That grants fold nothing new in practice: this
//     test reads fold's own imports, so fold calling os directly would still
//     mean an import here that this list rejects, and value's own source
//     imports fmt, regexp, unicode/utf8, person, and nothing else. Keep it
//     that way.
//   - For scale: fold's own closure already contained os, net *and os/exec*
//     before any of this, through engine/codec and golang.org/x/crypto/ssh.
//     value (like person before it) is not what made fold's closure
//     non-stdlib; what this list buys is that reaching any of it still takes
//     an import that shows up here.
//
// fold's own import list is otherwise unchanged by all of this.
var allowedImports = map[string]bool{
	`"container/heap"`: true,
	`"encoding/json"`:  true,
	`"errors"`:         true,
	`"fmt"`:            true,
	`"sort"`:           true,
	`"github.com/writtendev/writ/engine/codec"`:          true,
	`"github.com/writtendev/writ/engine/internal/value"`: true,
}

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
					t.Errorf("%s imports forbidden package %s (fold must remain pure and free of I/O)", filepath.Base(filename), pathVal)
				}
			}
		}
	}
}
