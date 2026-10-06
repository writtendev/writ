// Command apisurface writes the exported API of a package tree as sorted,
// diffable text.
//
// Usage:
//
//	apisurface ./engine
//
// It walks the directory for packages — skipping internal, testdata, vendor
// and dot- or underscore-prefixed directories — parses the non-test files of
// each, and prints every exported declaration in a fixed order.
//
// The output is derived from the source text alone: no type checking, no
// export data, no absolute paths, and nothing that varies with the Go version
// or the machine. That is the point. `make api-check` regenerates this listing
// and diffs it against api/engine.txt, so any difference means the baseline is
// stale rather than that the toolchain moved.
//
// A type alias into the module's own packages (`type X = pkg.Y`) is expanded:
// the listing prints Y's struct fields, interface methods or underlying type,
// and its exported methods under X, since those are what X's callers get and a
// bare alias line would hide every field later added to Y. The target is read
// from the module's source, following alias chains, so the output stays
// AST-only and host-independent. An alias to the standard library or a third
// party prints as spelled. An alias of a pointer to, or an instantiation of, an
// in-module type is an error rather than a pass-through, since it is not
// expanded. `make api-compat` does not see the fields of an aliased internal
// type either, so this expansion is the only guard for them.
//
// Reading the source rather than the type-checked package buys that
// reproducibility at a known cost: five known blind spots where the baseline
// records how a symbol is spelled rather than what it resolves to. Blind spot
// 3 is closed by refusing to run on constrained non-test files; the remaining
// four stay open because closing them requires full type-checking, defeating the
// AST-only reproducibility guarantee across toolchains. All four open blind
// spots are covered by `make api-compat` (apidiff against the merge base):
//
//   - Promoted and unexported-receiver methods (open): Only methods declared on
//     exported types are listed. A method promoted from an embedded type, or
//     declared on an unexported type that an exported function returns, is
//     reachable by callers but invisible here. Resolving promoted methods and
//     unexported types across packages requires full type checking. Covered by
//     `make api-compat`.
//   - Implicit const values and multi-name vars (open): A const spec with no
//     values prints without one, so inserting a constant into an iota block
//     renumbers every constant after it with no representation in the file
//     beyond the added line. Evaluating constant expressions and iota increments
//     requires type checking and constant evaluation. Similarly, multi-name var
//     specs with shared value lists (e.g. `var P, Q = pair()`) are unhandled by
//     AST splitting and latent in engine. Covered by `make api-compat`.
//   - Build constraints and GOOS/GOARCH filenames (closed): Non-test files
//     carrying build constraints (//go:build or // +build) or GOOS/GOARCH-suffixed
//     names are refused. Honouring constraints with go/build's default context
//     would make output depend on the host's GOOS/GOARCH, which is the one thing
//     this tool exists not to do. Refusing to run keeps output strictly
//     host-independent without requiring a platform policy. Test files (*_test.go)
//     remain permitted to carry build tags.
//   - Source spelling vs. semantics (open): Symbols are recorded as the source
//     spells them, not as they resolve. A private import alias leaks into the
//     listing, so renaming it churns the baseline without an API change. Exported
//     vars declared without a type record no type, so changes to what they alias
//     do not show. Covered by `make api-compat`.
//   - Loss of struct comparability (open): Adding a non-comparable field (a
//     slice, map, or func) to a comparable struct breaks ==, map keys, and
//     switch statements. The listing records field declarations, not the
//     comparability property. When unexported fields already exist, adding an
//     unexported non-comparable field produces no diff in the baseline at all.
//     Determining comparability across embedded types requires full type-checking.
//     Covered by `make api-compat`.
package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("apisurface: ")

	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: apisurface DIR")
		os.Exit(2)
	}
	out := bufio.NewWriter(os.Stdout)
	if err := run(out, os.Args[1]); err != nil {
		log.Fatal(err)
	}
	if err := out.Flush(); err != nil {
		log.Fatal(err)
	}
}

func run(w io.Writer, root string) error {
	modRoot, modPath, err := module(root)
	if err != nil {
		return err
	}
	dirs, err := packageDirs(root)
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		return fmt.Errorf("no packages under %s", root)
	}

	fmt.Fprintf(w, "# Exported API of %s and its public subpackages.\n", importPath(modRoot, modPath, root))
	fmt.Fprintln(w, "#")
	fmt.Fprintln(w, "# Generated by `make api`; do not edit by hand. `make api-check` fails when")
	fmt.Fprintln(w, "# this file no longer matches the source, so a public API change lands as a")
	fmt.Fprintln(w, "# diff here. A line that changes or disappears is usually a breaking change,")
	fmt.Fprintln(w, "# and so are three kinds of added line: a method added to an exported")
	fmt.Fprintln(w, "# interface; a constant inserted into an iota block, which renumbers the ones")
	fmt.Fprintln(w, "# after it; and a slice, map or func field added to a struct that was")
	fmt.Fprintln(w, "# comparable, which stops every == on it from compiling — and which leaves no")
	fmt.Fprintln(w, "# diff here at all when the field is unexported and the struct already had")
	fmt.Fprintln(w, "# unexported fields. `make api-compat` classifies the delta.")

	l := &loader{modRoot: modRoot, modPath: modPath, cache: map[string]*pkgSource{}}
	for _, dir := range dirs {
		if err := l.renderPackage(w, dir, importPath(modRoot, modPath, dir)); err != nil {
			return err
		}
	}
	return nil
}

// module finds the go.mod governing dir and returns its directory and the
// module path it declares.
func module(dir string) (root, path string, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	for {
		name := filepath.Join(abs, "go.mod")
		b, err := os.ReadFile(name)
		if err == nil {
			path, err := modulePath(b)
			if err != nil {
				return "", "", fmt.Errorf("%s: %w", name, err)
			}
			return abs, path, nil
		}
		if !os.IsNotExist(err) {
			return "", "", err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", "", fmt.Errorf("no go.mod above %s", dir)
		}
		abs = parent
	}
}

// modulePath reads the module line out of a go.mod. The full grammar is the go
// command's business; the one directive this needs is the first one in the
// file.
func modulePath(gomod []byte) (string, error) {
	for _, line := range strings.Split(string(gomod), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest), nil
		}
	}
	return "", fmt.Errorf("no module directive")
}

// importPath names the package in dir. Both failures below are unreachable in
// practice — dir came from a walk rooted under modRoot — and both would
// otherwise put a plausible but wrong import path into the file that is the
// committed definition of the API, so they end the program instead.
func importPath(modRoot, modPath, dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		log.Fatalf("%s: %v", dir, err)
	}
	rel, err := filepath.Rel(modRoot, abs)
	if err != nil {
		log.Fatalf("%s relative to %s: %v", abs, modRoot, err)
	}
	if rel == "." {
		return modPath
	}
	return modPath + "/" + filepath.ToSlash(rel)
}

// packageDirs lists, in path order, the directories under root that hold a
// package callers outside the module can import.
func packageDirs(root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != root {
			switch name := d.Name(); {
			case name == "internal", name == "testdata", name == "vendor":
				return fs.SkipDir
			case strings.HasPrefix(name, "."), strings.HasPrefix(name, "_"):
				return fs.SkipDir
			}
		}
		has, err := hasSourceFiles(path)
		if err != nil {
			return err
		}
		if has {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(dirs)
	return dirs, nil
}

func hasSourceFiles(dir string) (bool, error) {
	names, err := sourceFiles(dir)
	return len(names) > 0, err
}

// sourceFiles lists the non-test Go files in dir, in name order.
func sourceFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, filepath.Join(dir, name))
	}
	sort.Strings(names)
	return names, nil
}

// checkFileConstraints refuses any non-test file that carries build constraints,
// either via a GOOS/GOARCH filename suffix or a directive comment (//go:build, // +build).
func checkFileConstraints(path string) error {
	if reason := checkFilenameConstraint(path); reason != "" {
		return fmt.Errorf("%s: build constraints are not supported (%s)", path, reason)
	}
	return checkBuildDirectives(path)
}

// checkFilenameConstraint reports whether a non-test file name matches any of:
//
//	*_GOOS.go
//	*_GOARCH.go
//	*_GOOS_GOARCH.go
//
// returning a description of the matched suffix, or "" if unconstrained.
func checkFilenameConstraint(path string) string {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "_test.go") {
		return ""
	}
	name, ok := strings.CutSuffix(base, ".go")
	if !ok {
		return ""
	}
	i := strings.Index(name, "_")
	if i < 0 {
		return ""
	}
	name = name[i:]
	l := strings.Split(name, "_")
	n := len(l)
	if n >= 3 && knownOS[l[n-2]] && knownArch[l[n-1]] {
		return fmt.Sprintf("GOOS/GOARCH suffix %q", l[n-2]+"_"+l[n-1])
	}
	if n >= 2 && knownOS[l[n-1]] {
		return fmt.Sprintf("GOOS suffix %q", l[n-1])
	}
	if n >= 2 && knownArch[l[n-1]] {
		return fmt.Sprintf("GOARCH suffix %q", l[n-1])
	}
	return ""
}

// checkBuildDirectives scans the comments before the package clause of a Go file
// and returns an error if any //go:build or legacy // +build directive is found.
func checkBuildDirectives(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(s.Text(), "\ufeff"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "package ") || strings.HasPrefix(line, "package\t") || line == "package" {
			break
		}
		if constraint.IsGoBuild(line) || constraint.IsPlusBuild(line) {
			return fmt.Errorf("%s: build constraints are not supported (%s)", path, line)
		}
	}
	return s.Err()
}

var knownOS = map[string]bool{
	"aix":       true,
	"android":   true,
	"darwin":    true,
	"dragonfly": true,
	"freebsd":   true,
	"hurd":      true,
	"illumos":   true,
	"ios":       true,
	"js":        true,
	"linux":     true,
	"nacl":      true,
	"netbsd":    true,
	"openbsd":   true,
	"plan9":     true,
	"solaris":   true,
	"wasip1":    true,
	"windows":   true,
	"zos":       true,
}

var knownArch = map[string]bool{
	"386":         true,
	"amd64":       true,
	"amd64p32":    true,
	"arm":         true,
	"armbe":       true,
	"arm64":       true,
	"arm64be":     true,
	"loong64":     true,
	"mips":        true,
	"mipsle":      true,
	"mips64":      true,
	"mips64le":    true,
	"mips64p32":   true,
	"mips64p32le": true,
	"ppc":         true,
	"ppc64":       true,
	"ppc64le":     true,
	"riscv":       true,
	"riscv64":     true,
	"s390":        true,
	"s390x":       true,
	"sparc":       true,
	"sparc64":     true,
	"wasm":        true,
}

// pkgSource is a parsed package: its package clause name and its non-test
// files.
type pkgSource struct {
	name  string
	files []*ast.File
}

// loader parses the packages of one module, once each, so that a type alias
// into another package of the module can be expanded from that package's
// source (see resolveAlias) under the same rules as the listed packages.
type loader struct {
	modRoot, modPath string
	cache            map[string]*pkgSource
}

// load parses the non-test files of dir, refusing build constraints and
// disagreeing package names.
func (l *loader) load(dir string) (*pkgSource, error) {
	if p, ok := l.cache[dir]; ok {
		return p, nil
	}
	names, err := sourceFiles(dir)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		if err := checkFileConstraints(name); err != nil {
			return nil, err
		}
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s: no Go files", dir)
	}
	// Every non-test file in a directory must agree on its package name.
	// Disagreement is an error rather than a coin toss: a listing of the
	// wrong package's symbols is a baseline the gate would then defend.
	pkgName := files[0].Name.Name
	for i, f := range files[1:] {
		if f.Name.Name != pkgName {
			return nil, fmt.Errorf("%s: %s declares package %s but %s declares package %s "+
				"(every non-test file has to agree)",
				dir, filepath.Base(names[0]), pkgName, filepath.Base(names[i+1]), f.Name.Name)
		}
	}
	p := &pkgSource{name: pkgName, files: files}
	l.cache[dir] = p
	return p, nil
}

// moduleDir maps an import path inside the module to its directory.
func (l *loader) moduleDir(path string) (string, bool) {
	if path == l.modPath {
		return l.modRoot, true
	}
	rest, ok := strings.CutPrefix(path, l.modPath+"/")
	if !ok {
		return "", false
	}
	return filepath.Join(l.modRoot, filepath.FromSlash(rest)), true
}

// packageName reads only the package clause of dir's first non-test file. An
// unrenamed import is known by that name, not by the last path element. Reading
// just the clause keeps an import the file never aliases from being parsed, or
// refused, as a whole package.
func (l *loader) packageName(dir string) (string, error) {
	names, err := sourceFiles(dir)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return "", fmt.Errorf("%s: no Go files", dir)
	}
	f, err := parser.ParseFile(token.NewFileSet(), names[0], nil, parser.PackageClauseOnly)
	if err != nil {
		return "", err
	}
	return f.Name.Name, nil
}

// importedPackage loads the in-module package that sel's qualifier names in f.
// It returns nil when the qualifier is not an import of one.
func (l *loader) importedPackage(f *ast.File, sel *ast.SelectorExpr) (*pkgSource, error) {
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return nil, nil
	}
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		dir, ok := l.moduleDir(path)
		if !ok {
			continue
		}
		var local string
		if imp.Name != nil {
			local = imp.Name.Name
		} else if local, err = l.packageName(dir); err != nil {
			return nil, err
		}
		if local == x.Name {
			return l.load(dir)
		}
	}
	return nil, nil
}

// aliasTarget is a type declaration reached from an alias: the spec, the file
// whose imports its own selectors resolve against, and its package.
type aliasTarget struct {
	pkg  *pkgSource
	file *ast.File
	spec *ast.TypeSpec
}

// findType returns the declaration of the type name in pkg, or nil.
func findType(pkg *pkgSource, name string) *aliasTarget {
	for _, f := range pkg.files {
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.GenDecl)
			if !ok || d.Tok != token.TYPE {
				continue
			}
			for _, s := range d.Specs {
				if ts := s.(*ast.TypeSpec); ts.Name.Name == name {
					return &aliasTarget{pkg: pkg, file: f, spec: ts}
				}
			}
		}
	}
	return nil
}

// resolveAlias follows `type X = pkg.Y` through the module's own source, hop by
// hop with each file's own import table, to the last declaration that is in the
// module. A hop inside a package that is itself an alias (`type Y = y`) is
// followed too. It returns nil when ts is not an alias into the module: a target
// in the standard library or a third party prints as spelled. A selector that
// names no type, a cycle, or an alias whose type is a pointer to or an
// instantiation of an in-module type, a form not expanded here, is an error,
// because passing any of them through would leave the fields of an exported
// type out of the baseline.
func (l *loader) resolveAlias(f *ast.File, ts *ast.TypeSpec) (*aliasTarget, error) {
	var (
		cur  *aliasTarget
		seen = map[*ast.TypeSpec]bool{ts: true}
	)
	for {
		if !ts.Assign.IsValid() {
			return cur, nil
		}
		var next *aliasTarget
		switch t := ts.Type.(type) {
		case *ast.SelectorExpr:
			pkg, err := l.importedPackage(f, t)
			if err != nil || pkg == nil {
				return cur, err
			}
			if next = findType(pkg, t.Sel.Name); next == nil {
				return nil, fmt.Errorf("alias %s: package %s declares no type %s", ts.Name.Name, pkg.name, t.Sel.Name)
			}
		case *ast.Ident:
			// A bare name is a same-package hop, followed only inside a chain:
			// at the top the listed package prints it as spelled. A name that
			// is no declaration of the package is predeclared.
			if cur == nil {
				return nil, nil
			}
			if next = findType(cur.pkg, t.Name); next == nil {
				return cur, nil
			}
		default:
			if sel := wrappedSelector(ts.Type); sel != nil {
				pkg, err := l.importedPackage(f, sel)
				if err != nil {
					return nil, err
				}
				if pkg != nil {
					return nil, fmt.Errorf("alias %s: %s names a type in module package %s in a form api-check does not expand; "+
						"alias the type itself (`= %s`) and expand it there", ts.Name.Name, expr(ts.Type), pkg.name, expr(sel))
				}
			}
			return cur, nil
		}
		if seen[next.spec] {
			return nil, fmt.Errorf("alias %s: cycle through %s", ts.Name.Name, expr(ts.Type))
		}
		seen[next.spec] = true
		cur, f, ts = next, next.file, next.spec
	}
}

// wrappedSelector returns the selector a type reaches through pointers,
// parentheses and instantiations (`*pkg.Y`, `pkg.G[int]`), or nil.
func wrappedSelector(e ast.Expr) *ast.SelectorExpr {
	for {
		switch t := e.(type) {
		case *ast.SelectorExpr:
			return t
		case *ast.StarExpr:
			e = t.X
		case *ast.ParenExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		default:
			return nil
		}
	}
}

// renderPackage writes the exported surface of the package in dir.
func (l *loader) renderPackage(w io.Writer, dir, path string) error {
	pkg, err := l.load(dir)
	if err != nil {
		return err
	}
	pkgName, files := pkg.name, pkg.files

	var (
		consts  []string
		vars    []string
		funcs   []string
		types   []typeDecl
		methods = map[string][]string{}
	)
	for _, f := range files {
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				switch d.Tok {
				case token.CONST:
					consts = append(consts, valueLines(d)...)
				case token.VAR:
					vars = append(vars, valueLines(d)...)
				case token.TYPE:
					ts, err := l.typeDecls(f, d)
					if err != nil {
						return err
					}
					types = append(types, ts...)
				}
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv == nil {
					funcs = append(funcs, "func "+d.Name.Name+signature(d.Type))
					continue
				}
				recv := d.Recv.List[0].Type
				base := receiverName(recv)
				if base == "" || !ast.IsExported(base) {
					continue
				}
				methods[base] = append(methods[base], methodLine(d, expr(recv)))
			}
		}
	}

	sort.Strings(consts)
	sort.Strings(vars)
	sort.Strings(funcs)
	sort.Slice(types, func(i, j int) bool { return types[i].name < types[j].name })

	fmt.Fprintf(w, "\npackage %s // %s\n", pkgName, path)

	for _, group := range [][]string{consts, vars, funcs} {
		if len(group) == 0 {
			continue
		}
		fmt.Fprintln(w)
		for _, line := range group {
			fmt.Fprintln(w, line)
		}
	}
	for _, t := range types {
		fmt.Fprintln(w)
		for _, line := range t.lines {
			fmt.Fprintln(w, line)
		}
		ms := append(methods[t.name], t.methods...)
		sort.Strings(ms)
		for _, m := range ms {
			fmt.Fprintln(w, m)
		}
	}
	return nil
}

type typeDecl struct {
	name    string
	lines   []string
	methods []string // an expanded alias's methods; others' come from the package's FuncDecls
}

// methodLine renders a method declaration with the given receiver type.
func methodLine(d *ast.FuncDecl, recv string) string {
	return "func (" + recv + ") " + d.Name.Name + signature(d.Type)
}

// valueLines renders the exported names of a const or var declaration.
//
// A const spec with no values repeats the previous spec's expression list
// (`A Kind = iota` followed by a bare `B`). The declared type carries over and
// is printed; the repeated expression is not, because printing `= iota` again
// would read as if both constants were zero.
func valueLines(d *ast.GenDecl) []string {
	var (
		lines    []string
		lastType ast.Expr
	)
	for _, s := range d.Specs {
		vs, ok := s.(*ast.ValueSpec)
		if !ok {
			continue
		}
		typ, implicit := vs.Type, false
		if d.Tok == token.CONST && len(vs.Values) == 0 {
			if typ == nil {
				typ = lastType
			}
			implicit = true
		}
		lastType = typ
		for i, n := range vs.Names {
			if !n.IsExported() {
				continue
			}
			line := d.Tok.String() + " " + n.Name
			if typ != nil {
				line += " " + expr(typ)
			}
			if !implicit && i < len(vs.Values) {
				line += " = " + expr(vs.Values[i])
			}
			lines = append(lines, line)
		}
	}
	return lines
}

func (l *loader) typeDecls(f *ast.File, d *ast.GenDecl) ([]typeDecl, error) {
	var out []typeDecl
	for _, s := range d.Specs {
		ts, ok := s.(*ast.TypeSpec)
		if !ok || !ts.Name.IsExported() {
			continue
		}
		target, err := l.resolveAlias(f, ts)
		if err != nil {
			return nil, err
		}
		if target == nil {
			out = append(out, typeDecl{name: ts.Name.Name, lines: typeLines(ts)})
			continue
		}
		out = append(out, aliasDecl(ts, target))
	}
	return out, nil
}

// aliasDecl renders an alias into the module as if the alias were the type it
// names: the head keeps the alias as written, the body is the target's, and
// the target's exported methods follow under the alias's name, as a caller
// writes them. Field and signature types keep the target source's spelling.
func aliasDecl(ts *ast.TypeSpec, target *aliasTarget) typeDecl {
	head := "type " + ts.Name.Name + " = " + expr(ts.Type)
	decl := typeDecl{name: ts.Name.Name, lines: typeBody(head, target.spec.Type)}
	for _, f := range target.pkg.files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || !fd.Name.IsExported() || receiverName(fd.Recv.List[0].Type) != target.spec.Name.Name {
				continue
			}
			recv := ts.Name.Name
			if _, ptr := fd.Recv.List[0].Type.(*ast.StarExpr); ptr {
				recv = "*" + recv
			}
			decl.methods = append(decl.methods, methodLine(fd, recv))
		}
	}
	return decl
}

func typeLines(ts *ast.TypeSpec) []string {
	head := "type " + ts.Name.Name + typeParams(ts.TypeParams)
	if ts.Assign.IsValid() {
		head += " ="
	}
	return typeBody(head, ts.Type)
}

// typeBody renders typ after head: a struct's fields, an interface's methods,
// or any other type as spelled.
func typeBody(head string, typ ast.Expr) []string {
	var (
		body []string
		kind string
	)
	switch t := typ.(type) {
	case *ast.StructType:
		kind, body = "struct", structFields(t)
	case *ast.InterfaceType:
		kind, body = "interface", interfaceMethods(t)
	default:
		return []string{head + " " + expr(typ)}
	}
	if len(body) == 0 {
		return []string{head + " " + kind + "{}"}
	}
	lines := []string{head + " " + kind + " {"}
	lines = append(lines, body...)
	return append(lines, "}")
}

func structFields(st *ast.StructType) []string {
	var lines []string
	unexported := false
	for _, f := range st.Fields.List {
		suffix := expr(f.Type)
		if f.Tag != nil {
			suffix += " " + f.Tag.Value
		}
		if len(f.Names) == 0 { // embedded
			if name := receiverName(f.Type); name == "" || !ast.IsExported(name) {
				unexported = true
				continue
			}
			lines = append(lines, "\t"+suffix)
			continue
		}
		for _, n := range f.Names {
			if !n.IsExported() {
				unexported = true
				continue
			}
			lines = append(lines, "\t"+n.Name+" "+suffix)
		}
	}
	if unexported {
		lines = append(lines, "\t// unexported fields")
	}
	return lines
}

func interfaceMethods(it *ast.InterfaceType) []string {
	var lines []string
	unexported := false
	for _, f := range it.Methods.List {
		if len(f.Names) == 0 { // embedded interface, or a type set
			lines = append(lines, "\t"+expr(f.Type))
			continue
		}
		ft, ok := f.Type.(*ast.FuncType)
		if !ok {
			continue
		}
		for _, n := range f.Names {
			if !n.IsExported() {
				unexported = true
				continue
			}
			lines = append(lines, "\t"+n.Name+signature(ft))
		}
	}
	if unexported {
		lines = append(lines, "\t// unexported methods")
	}
	return lines
}

func typeParams(fl *ast.FieldList) string {
	if fl == nil || len(fl.List) == 0 {
		return ""
	}
	var params []string
	for _, f := range fl.List {
		var names []string
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
		params = append(params, strings.TrimSpace(strings.Join(names, ", ")+" "+expr(f.Type)))
	}
	return "[" + strings.Join(params, ", ") + "]"
}

// signature renders a function type as it follows a name: "(a int) error".
func signature(ft *ast.FuncType) string {
	return strings.TrimPrefix(expr(ft), "func")
}

// receiverName is the declared type's name, for a method receiver or an
// embedded field: Comments for *Comments, Op for codec.Op, Set for Set[T].
func receiverName(e ast.Expr) string {
	for {
		switch t := e.(type) {
		case *ast.StarExpr:
			e = t.X
		case *ast.IndexExpr:
			e = t.X
		case *ast.IndexListExpr:
			e = t.X
		case *ast.SelectorExpr:
			return t.Sel.Name
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

// expr renders a syntax node on one line. The printer is given an empty file
// set so it has no source positions to reproduce; the join is a backstop for
// nodes it still breaks across lines.
//
// A printer failure ends the program rather than yielding a placeholder: a
// type or signature that renders as something else is a wrong entry in the
// baseline, which is worse than no baseline at all.
func expr(e ast.Expr) string {
	var b strings.Builder
	if err := printer.Fprint(&b, token.NewFileSet(), e); err != nil {
		log.Fatalf("printing %T: %v", e, err)
	}
	lines := strings.Split(b.String(), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	return strings.Join(lines, " ")
}
