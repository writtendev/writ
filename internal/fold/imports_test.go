package fold_test

import (
	"go/parser"
	"go/token"
	"os"
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
// applies transitively, because value imports nothing but person,
// engine/internal/anchorshape, and the standard library:
//
//   - person imports golang.org/x/text/unicode/norm and
//     golang.org/x/text/cases, because the normalization rule
//     spec/identifiers.md pins is defined over Unicode tables that neither
//     the standard library nor this repository carries. Neither x/text
//     package performs I/O: both are table-driven computation over generated
//     Unicode data — no filesystem, no network, no processes, no clock.
//     cases additionally reaches x/text/language, itself table lookup over
//     language tags.
//   - person also imports the root-level internal/textsafe (WRIT-137), the
//     forbidden-code-point table shared with cmd/writ's display-side
//     escaping. textsafe imports only strings, already on this package's own
//     allowlist, so this adds nothing fold could not already reach.
//   - anchorshape (WRIT-252) is the structural well-formedness predicate the
//     anchor value type's producer check shares with engine/resolve's
//     read-side pre-check. It is stdlib-only by its own doc comment and adds
//     nothing to this closure beyond one more leaf.
//   - The transitive closure through value and person does contain os,
//     reached through fmt, which x/text and value's own error messages both
//     use for formatting. That grants fold nothing new in practice: this
//     test reads fold's own imports, so fold calling os directly would still
//     mean an import here that this list rejects, and value's own source
//     imports fmt, regexp, unicode/utf8, person, anchorshape, and nothing
//     else. Keep it that way.
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
	`"github.com/writtendev/writ/internal/codec"`: true,
	`"github.com/writtendev/writ/internal/value"`: true,
}

func TestImportsAllowlist(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			pathVal := imp.Path.Value
			if !allowedImports[pathVal] {
				t.Errorf("%s imports forbidden package %s (fold must remain pure and free of I/O)", filepath.Base(name), pathVal)
			}
		}
	}
}
