package spec_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Writ's folded state must be reproducible byte-for-byte by an implementation
// written in any language from spec/fold.md alone. Go's value-to-string
// conversions are the one thing in this codebase that can quietly destroy
// that: `fmt.Sprint` on an `any` renders a map as `map[email:carol@example.com]`
// and a slice as `[a@b.com]`, which are Go syntax. Both of those shipped as
// normative fold keys (WRIT-124), and `fmt.Sprint(nil)` shipped as the string
// `<nil>` in an assignee set (WRIT-126). `strconv` is the same hazard with a
// different spelling: `strconv.FormatFloat(1e21, 'g', -1, 64)` is `1e+21`,
// whose exponent form is Go's choice and not JSON's.
//
// The conformance corpus cannot catch this class, and that is why this test
// exists rather than another fixture. The corpus byte-compares two Go
// implementations against each other, so where both render the same way they
// agree perfectly — measured on main at f324f58, `spec.Fold` and `writ.Fold`
// emitted `map[email:carol@example.com]` as the same fold key, and every
// vector passed. Agreement is not the property at stake; implementability
// elsewhere is, and only reading the source can see the difference.
//
// It is load-bearing beyond the code it names. Now that a declared field
// carrying a non-string is uninterpretable (spec/fold.md §7.1), the stringifying
// code is unreachable for the very inputs that would expose it, so
// reintroducing `fmt.Sprint` on an item path leaves every merge vector passing.
// The vectors cannot see this regression any more. This test is what does.
//
// The rule has two halves, because rendering has two doors:
//
//  1. On a fold value path, `fmt.Errorf` is the only symbol of `fmt` or
//     `strconv` that may be *named* — called, aliased, or passed as a value.
//     Nothing else in either package turns a value into bytes that a reader in
//     another language could be expected to reproduce. Where a fold needs a
//     value as a string, the value is a string — an op whose declared field
//     carries anything else is uninterpretable and never reaches a reducer — so
//     a type assertion says what is meant and this says so when it does not.
//  2. The result of `fmt.Errorf` is an error to return, not a string to use.
//     `fmt.Errorf("%v", item).Error()` is `fmt.Sprint(item)` spelled through the
//     one allowed call, so no method may be invoked on a rendering call's
//     result, and `.Error()` may not be *called* on these paths. Fold code
//     wraps errors with `%w` and returns them; it never needs their text, and
//     `.Error()` is the only door out of an `error` and into a string that does
//     not go back through half 1.
//
// Naming, not calling, is the unit of half 1 deliberately. `var render =
// fmt.Sprint` is not a call expression at any point where `render(it)` appears,
// so a check that inspects calls sees an ordinary local function and reports
// nothing.
//
// Half 2 is the other way round, and the wording above is narrow for that
// reason: it is about *calling* `.Error()`, not naming it. `render := err.Error`
// followed by `render()` is not reported, and is recorded as a known-uncovered
// case in TestNoGoRenderingLintBites rather than closed. Closing it needs type
// information — a rule that flagged every `.Error` selector would flag
// `spec/vectors.go:50`, where `v.Error` is a struct field — and go/types is a
// disproportionate price for a spelling nobody writes by accident.
//
// What the rule does not reach, so that nobody mistakes the only guard for a
// complete one: a rendering helper defined in a package that is not itself a
// fold value path but is imported by one, and rendering that goes through
// `encoding/json`, `text/template` or `reflect` instead of `fmt` or `strconv`.
// `encoding/json` cannot simply be added to the list — `spec/reffold.go` and
// `engine/internal/fold/strategy.go` both marshal a `[]string` into a grouping
// key that is never serialized — and banning it would cost the zero-exceptions
// property this list has.
//
// Scoped to the fold, deliberately. Elsewhere in the repo `fmt.Sprintf` builds
// log lines, CLI output and map keys that are never anybody's normative bytes.
//
// Scoped by *package*, also deliberately. Naming `reffold.go` alone guarded one
// file of a package whose other files compile into the same namespace: a
// `renderItem` helper dropped in any sibling file is callable from `reffold.go`
// with nothing to see at the call site. `spec/fieldrules.go` had a `fmt.Sprintf`
// on exactly those terms until it was rewritten to use a composite map key, so
// this list can name the whole package with no exceptions to keep.
var foldValuePaths = []string{
	// The spec package, whole. The test binary runs with spec/ as its working
	// directory, so "." is spec/ — reffold.go and every file that shares its
	// package namespace.
	".",
	"../internal/fold",
	"../internal/state",
}

// renderingPackages are the packages a fold value path may not render values
// with, mapped to the symbols it may still name in them.
//
// engine/internal/fold additionally carries an import allowlist
// (engine/internal/fold/imports_test.go) that keeps strconv out of that package
// entirely. engine/state and spec have no such allowlist, which is why this
// check names the package rather than relying on one.
var renderingPackages = map[string]map[string]bool{
	"fmt":     {"Errorf": true},
	"strconv": {},
}

func TestNoGoRenderingOnFoldValuePaths(t *testing.T) {
	var checked int
	for _, target := range foldValuePaths {
		for _, path := range goFilesUnder(t, target) {
			checked++
			for _, finding := range goRenderingFindings(t, path) {
				t.Error(finding)
			}
		}
	}
	// A path that silently matched nothing — a rename, a moved package — would
	// make this test pass by covering nothing at all.
	if checked < len(foldValuePaths) {
		t.Fatalf("guarded only %d files across %d paths; a fold value path has moved",
			checked, len(foldValuePaths))
	}
}

// TestNoGoRenderingLintBites pins the check against every way found so far of
// putting Go rendering back on the OR-set item path while this file stays
// green. A lint nobody has attacked is a lint nobody has measured, and this one
// is now the only guard on WRIT-124's defect: the reject rule filters
// non-strings upstream, so the merge vectors are blind to a reintroduced
// `fmt.Sprint` there and go on passing.
//
// None of these are hypothetical. Each was written into spec/reffold.go, built
// cleanly, and left `go test ./...` and `go vet ./...` green against the
// check's earlier shape:
//
//   - `import . "fmt"` turns `fmt.Sprint(item)` into a bare `Sprint(item)`,
//     which is not a selector expression at all. Nothing else in this
//     repository flags a dot import: there is no .golangci.yml, and no linter
//     enabled by default rejects one.
//   - `var render = fmt.Sprint` binds the function as a value. `render(it)` is
//     a call of a package-local identifier, indistinguishable at the call site
//     from any other helper — which is why half 1 of the rule is about naming
//     a symbol, not calling one.
//   - `fmt.Errorf("%v", it).Error()` reaches Go's formatting through the one
//     call the rule allows, and `err := fmt.Errorf(...)` then `err.Error()`
//     reaches it in two statements.
//
// The fourth evasion is not a source shape but a coverage hole, so it is pinned
// against foldValuePaths rather than against a sample file: a `renderItem`
// helper in a *sibling file of the same package* is callable from reffold.go
// with nothing to see at the call site. See TestFoldValuePathsCoverWholePackages.
//
// The last case in this test is the one that is *not* pinned: `.Error` bound as
// a method value gets past half 2, and is recorded here so the record is where
// the reader is rather than in someone's memory.
func TestNoGoRenderingLintBites(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "qualified fmt.Sprint",
			src: "package p\n\nimport \"fmt\"\n\n" +
				"func f(v any) string { return fmt.Sprint(v) }\n",
			want: "fmt.Sprint",
		},
		{
			name: "renamed fmt import",
			src: "package p\n\nimport gofmt \"fmt\"\n\n" +
				"func f(v any) string { return gofmt.Sprint(v) }\n",
			want: "gofmt.Sprint",
		},
		{
			name: "dot-imported fmt",
			src: "package p\n\nimport . \"fmt\"\n\n" +
				"func f(v any) string { return Sprint(v) }\n",
			want: "dot-imports",
		},
		{
			name: "strconv rendering",
			src: "package p\n\nimport \"strconv\"\n\n" +
				"func f(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }\n",
			want: "strconv.FormatFloat",
		},
		{
			name: "fmt.Sprint bound as a function value",
			src: "package p\n\nimport \"fmt\"\n\nvar render = fmt.Sprint\n\n" +
				"func f(items []any) []string {\n" +
				"\tvar out []string\n" +
				"\tfor _, it := range items {\n" +
				"\t\tout = append(out, render(it))\n" +
				"\t}\n" +
				"\treturn out\n}\n",
			want: "fmt.Sprint",
		},
		{
			name: "fmt.Sprint passed as an argument",
			src: "package p\n\nimport \"fmt\"\n\n" +
				"func apply(f func(...any) string, v any) string { return f(v) }\n\n" +
				"func g(v any) string { return apply(fmt.Sprint, v) }\n",
			want: "fmt.Sprint",
		},
		{
			name: "Error on the result of fmt.Errorf",
			src: "package p\n\nimport \"fmt\"\n\n" +
				"func f(v any) string { return fmt.Errorf(\"%v\", v).Error() }\n",
			want: "on the result of fmt.Errorf",
		},
		{
			name: "Error on an fmt.Errorf bound to a variable",
			src: "package p\n\nimport \"fmt\"\n\n" +
				"func f(v any) string {\n\terr := fmt.Errorf(\"%v\", v)\n\treturn err.Error()\n}\n",
			want: ".Error() turns an error into a string",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "sample.go")
			if err := os.WriteFile(path, []byte(tc.src), 0o644); err != nil {
				t.Fatalf("writing sample: %v", err)
			}
			findings := goRenderingFindings(t, path)
			if len(findings) == 0 {
				t.Fatalf("check reported nothing for %s; it covers nothing", tc.name)
			}
			if !strings.Contains(strings.Join(findings, "\n"), tc.want) {
				t.Errorf("finding does not name %q:\n%s", tc.want, strings.Join(findings, "\n"))
			}
		})
	}

	// And the converse: the one allowed call must not be reported, or the
	// check would be noise nobody could keep green.
	path := filepath.Join(t.TempDir(), "allowed.go")
	src := "package p\n\nimport \"fmt\"\n\nfunc f() error { return fmt.Errorf(\"boom\") }\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing sample: %v", err)
	}
	if findings := goRenderingFindings(t, path); len(findings) != 0 {
		t.Errorf("fmt.Errorf reported: %s", strings.Join(findings, "\n"))
	}

	// And one evasion that is recorded rather than closed, so the next reader
	// finds it written down instead of rediscovering it. Half 2 matches a call
	// whose function is an `.Error` selector, so binding the method as a value
	// and calling the binding walks past it. Written into the real set-union
	// item path in spec/reffold.go it builds, vets, and leaves
	// `go test ./spec/... ./engine/... -count=1` green, lint included;
	// `add(callIt(e.Error))` gets through on the same terms. Closing it needs
	// go/types: a rule that flagged every `.Error` selector would flag
	// spec/vectors.go, where `v.Error` is a struct field, and that is a
	// disproportionate price for a spelling nobody reaches for by accident.
	// If this assertion ever fails, the hole has been closed — delete this
	// block and drop the caveat from the rule's docstring above.
	path = filepath.Join(t.TempDir(), "methodvalue.go")
	src = "package p\n\nimport \"fmt\"\n\nfunc f(v any) string {\n" +
		"\te := fmt.Errorf(\"%v\", v)\n\trender := e.Error\n\treturn render()\n}\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("writing sample: %v", err)
	}
	if findings := goRenderingFindings(t, path); len(findings) != 0 {
		t.Errorf("the `.Error` method value is now reported; the docstring and this block "+
			"still describe it as uncovered:\n%s", strings.Join(findings, "\n"))
	}
}

// TestFoldValuePathsCoverWholePackages pins the fourth evasion, which no sample
// file can express because it is not a source shape: a helper in a sibling file
// of a guarded package. `reffold.go` and `spec/fieldrules.go` compile into one
// namespace, so
//
//	// spec/foldhelpers.go
//	func renderItem(v any) string { return fmt.Sprint(v) }
//
// makes `items = append(items, renderItem(it))` in reffold.go legal, green and
// invisible to a list that names `reffold.go` alone — which is what this list
// named until it named the package instead. Demonstrated by writing exactly
// that file: with foldValuePaths naming the package the check reports it, and
// with foldValuePaths naming reffold.go it reported nothing.
//
// So: every non-test Go file in every package that holds a guarded file must
// itself be guarded. A new file in spec/, engine/state or engine/internal/fold
// is covered the moment it is written, with nothing to remember.
func TestFoldValuePathsCoverWholePackages(t *testing.T) {
	guarded := make(map[string]bool)
	dirs := make(map[string]bool)
	for _, target := range foldValuePaths {
		for _, path := range goFilesUnder(t, target) {
			abs, err := filepath.Abs(path)
			if err != nil {
				t.Fatalf("resolving %s: %v", path, err)
			}
			guarded[abs] = true
			dirs[filepath.Dir(abs)] = true
		}
	}
	if len(dirs) == 0 {
		t.Fatal("no fold value path resolved to a directory")
	}

	for dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		var siblings int
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			siblings++
			if !guarded[filepath.Join(dir, name)] {
				t.Errorf("%s shares a package with a guarded fold value path but is not itself guarded.\n"+
					"A rendering helper defined here is callable from the guarded file with nothing to "+
					"see at the call site. Add the package directory to foldValuePaths rather than "+
					"individual files.", filepath.Join(dir, name))
			}
		}
		if siblings == 0 {
			t.Errorf("%s holds no non-test Go files; a fold value path has moved", dir)
		}
	}
}

// goFilesUnder returns the non-test Go files at target, which is either one
// file or a directory.
func goFilesUnder(t *testing.T, target string) []string {
	t.Helper()
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("fold value path %s is gone; update this test deliberately: %v", target, err)
	}
	if !info.IsDir() {
		return []string{target}
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("reading %s: %v", target, err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(target, name))
	}
	if len(files) == 0 {
		t.Fatalf("fold value path %s holds no non-test Go files; update this test deliberately", target)
	}
	return files
}

// goRenderingFindings returns one finding per call into a rendering package
// that the fold value path rule forbids, plus one per dot import of such a
// package.
func goRenderingFindings(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	var findings []string

	// Track the local name each rendering package is imported under: a file
	// that renames it must not slip past the selector check, and one that
	// dot-imports it makes every call a bare identifier the selector check
	// cannot see at all. A dot import is therefore the finding.
	localNames := make(map[string]string) // local name -> package path
	var dotImported []string
	for _, imp := range file.Imports {
		pkg := strings.Trim(imp.Path.Value, `"`)
		if _, forbidden := renderingPackages[pkg]; !forbidden {
			continue
		}
		local := pkg
		if imp.Name != nil {
			local = imp.Name.Name
		}
		switch local {
		case "_":
			continue
		case ".":
			dotImported = append(dotImported, pkg)
			continue
		}
		localNames[local] = pkg
	}

	sort.Strings(dotImported)
	for _, pkg := range dotImported {
		findings = append(findings, fmt.Sprintf(
			"%s: dot-imports %q on a fold value path.\n"+
				"A dot import makes every call to it an unqualified identifier, which this check "+
				"cannot distinguish from a local function — %s.Sprint(v) written as Sprint(v) would "+
				"go unreported. Import it under its own name so the rule below can be enforced.",
			fset.Position(imp0Pos(file, pkg)), pkg, pkg))
	}

	// renderingSelector reports the package a selector expression names a symbol
	// of, if any: `fmt.Sprint` under any local import name, whether it is being
	// called, assigned to a variable, or passed as an argument.
	renderingSelector := func(e ast.Expr) (sel *ast.SelectorExpr, pkg string, ok bool) {
		s, isSel := e.(*ast.SelectorExpr)
		if !isSel {
			return nil, "", false
		}
		ident, isIdent := s.X.(*ast.Ident)
		if !isIdent {
			return nil, "", false
		}
		p, forbidden := localNames[ident.Name]
		if !forbidden {
			return nil, "", false
		}
		return s, p, true
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			// Half 1: any *reference* to a rendering symbol, not only a call
			// of one. `var render = fmt.Sprint` binds the function itself, and
			// the later `render(it)` carries no trace of fmt at the call site.
			sel, pkg, ok := renderingSelector(node)
			if !ok || renderingPackages[pkg][sel.Sel.Name] {
				return true
			}
			findings = append(findings, fmt.Sprintf(
				"%s: %s.%s renders a value using Go's own formatting on a fold value path.\n"+
					"Folded state must be reproducible from spec/fold.md by an implementation in any "+
					"language, and Go's rendering of a map, a slice, nil or a float exponent is Go "+
					"syntax that no other implementation produces. Assert the type you mean instead; "+
					"fmt.Errorf is the only symbol of fmt or strconv that may be named here, and "+
					"naming it — calling it, aliasing it, passing it — is what this reports.",
				fset.Position(sel.Pos()), exprName(sel.X), sel.Sel.Name))

		case *ast.CallExpr:
			// Half 2: the result of the one allowed call is an error to
			// return, not a string to use. Both doors out of it are closed —
			// a method invoked directly on the call's result, and .Error()
			// anywhere, which is what the two-statement spelling reaches for.
			method, isSel := node.Fun.(*ast.SelectorExpr)
			if !isSel {
				return true
			}
			if inner, isCall := method.X.(*ast.CallExpr); isCall {
				if sel, _, ok := renderingSelector(inner.Fun); ok {
					findings = append(findings, fmt.Sprintf(
						"%s: .%s() on the result of %s.%s renders a value on a fold value path.\n"+
							"fmt.Errorf(\"%%v\", item).Error() is fmt.Sprint(item) spelled through the one "+
							"allowed call. The result of fmt.Errorf is an error to return, not a string to "+
							"use; wrap with %%w and return it.",
						fset.Position(node.Pos()), method.Sel.Name, exprName(sel.X), sel.Sel.Name))
					return true
				}
			}
			if method.Sel.Name == "Error" && len(node.Args) == 0 {
				findings = append(findings, fmt.Sprintf(
					"%s: .Error() turns an error into a string on a fold value path.\n"+
						"It is the one door out of an error that does not go back through fmt, so it is "+
						"closed here too: fmt.Errorf(\"%%v\", item) assigned to a variable and then "+
						".Error()-ed is fmt.Sprint(item) in two statements. Fold code wraps errors with "+
						"%%w and returns them; it never needs their text.",
					fset.Position(node.Pos())))
			}
		}
		return true
	})

	return findings
}

// exprName renders the left-hand side of a selector for a message: the local
// import name in every case this check reports, since renderingSelector only
// matches a bare identifier.
func exprName(e ast.Expr) string {
	if ident, ok := e.(*ast.Ident); ok {
		return ident.Name
	}
	return "?"
}

// imp0Pos returns the position of the import of pkg, for a readable message.
func imp0Pos(file *ast.File, pkg string) token.Pos {
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) == pkg {
			return imp.Pos()
		}
	}
	return file.Pos()
}
