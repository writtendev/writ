package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// allowedInternalImports is the guard's exact allowlist (WRIT-311, WRIT-296
// plan §3): every other github.com/writtendev/writ/internal/... package
// knows about writ's data model -- ops, refs, schemas, identity, signing,
// or git -- and a shipped cmd/writ file reaching one of them directly is a
// caller with private powers, contradicting ARCHITECTURE.md §Public API
// shape's "nothing gets private powers." These two do not:
//
//   - internal/textdiff is a generic line differ (schema.go's `writ schema
//     plan` diff) with no knowledge of writ's data model. It cannot move
//     under cmd/writ/internal/ because spec/fixtures/diff.go imports it
//     too, and exporting a line-diff function from the engine would be
//     framework-building for a capability that isn't engine capability by
//     any reading.
//   - internal/version is the writ binary's own build string (main.go's
//     `writ --version`), stamped by release tooling ldflags. It is not
//     engine capability either, and exporting it would make writ's build
//     metadata a v0.1.0 API promise with no consumer.
var allowedInternalImports = map[string]bool{
	"github.com/writtendev/writ/internal/textdiff": true,
	"github.com/writtendev/writ/internal/version":  true,
}

// forbiddenImportPrefix is every internal package this guard polices:
// codec, dag, fold, gitdir, identity, order, packidx, person, projection,
// resolve, schemasrc, state, sync, textsafe, value, anchorshape, and the
// rest of what the module root's /internal/ holds (WRIT-287). It does not
// match cmd/writ's own cmd/writ/internal/wire, a different package tree
// entirely (github.com/writtendev/writ/cmd/writ/internal/...) that this
// guard has no opinion on.
const forbiddenImportPrefix = "github.com/writtendev/writ/internal/"

// TestNoInternalImportsOutsideAllowlist is WRIT-311's guard (WRIT-296 plan
// §3): no non-test file under cmd/writ/ may import any
// github.com/writtendev/writ/internal/... package except the two named in
// allowedInternalImports, both allowlisted above with the reason. It is
// what proves cmd/writ is built on the public engine API rather than
// merely happening to be, today.
//
// Test files are explicitly out of scope, deliberately, not by oversight:
// init_test.go, sync_test.go, object_test.go, schema_test.go,
// textsafe_render_test.go, json_golden_test.go, main_test.go,
// testenv_test.go and others reach into internal/gittest, internal/codec,
// internal/canonicaljson, internal/dag and others to build fixtures --
// constructing a hostile op commit by hand to test the reader's rejection
// of it, say. A test doing that is not the shipped writ binary having
// private powers; it is exercising the binary's behaviour against inputs
// only a test harness can construct. Purging those imports would multiply
// this ticket's size for no architectural gain, and re-litigating that by
// widening this guard's scope to test files is exactly the "finish the
// job" this comment exists to head off. If cmd/writ ever needs a truly
// internals-free test suite, that is a new, separate ticket to argue for,
// not a quiet extension of this one's predicate.
func TestNoInternalImportsOutsideAllowlist(t *testing.T) {
	fset := token.NewFileSet()
	var findings []string

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// testdata holds fixtures, not Go source; walking into it is
			// harmless (no .go files live there today) but skipping it
			// keeps this from ever depending on that staying true.
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(importPath, forbiddenImportPrefix) {
				continue
			}
			if allowedInternalImports[importPath] {
				continue
			}
			findings = append(findings, path+": imports "+importPath)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking cmd/writ: %v", err)
	}

	if len(findings) > 0 {
		t.Errorf("non-test file(s) under cmd/writ/ import an internal/ package outside the allowlist (internal/textdiff, internal/version):\n  %s", strings.Join(findings, "\n  "))
	}
}
