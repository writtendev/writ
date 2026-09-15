package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// infrastructureFiles are internal cmd/writ source files that provide CLI
// framework and tooling infrastructure rather than porcelain command output:
//   - porcelain.go implements the porcelain printing helpers
//   - commands.go defines CLI command dispatch and usage synopsis rendering
//   - completion.go generates shell completion scripts (bash, zsh, fish)
//   - docs.go generates markdown reference documentation
//   - help.go prints generic help usage
//   - init.go handles repository initialization and git config setup
//   - main.go handles process entrypoint and top-level flag parsing
//   - store.go handles repository opening and error escaping via renderErr
//
// All other non-test Go source files in cmd/writ (such as object.go, schema.go,
// sync.go, and any newly added command files) are inspected dynamically.
var infrastructureFiles = map[string]bool{
	"porcelain.go":  true,
	"commands.go":   true,
	"completion.go": true,
	"docs.go":       true,
	"help.go":       true,
	"init.go":       true,
	"main.go":       true,
	"store.go":      true,
}

// findPorcelainDir finds the directory containing the cmd/writ source files.
func findPorcelainDir(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat("object.go"); err == nil {
		return "."
	}
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return filepath.Clean(filepath.Join(".", "..", "..", "cmd", "writ"))
	}
	return filepath.Join(strings.TrimSpace(string(out)), "cmd", "writ")
}

// isFprintCall reports whether call is a call to fmt.Fprint, fmt.Fprintf,
// or fmt.Fprintln.
func isFprintCall(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok || ident.Name != "fmt" {
		return "", false
	}
	switch sel.Sel.Name {
	case "Fprint", "Fprintf", "Fprintln":
		return "fmt." + sel.Sel.Name, true
	default:
		return "", false
	}
}

// isDocumentedExemption reports whether a call to fmt.Fprint* has an explicit
// documented exemption comment either on the line preceding the call or on the
// same line.
func isDocumentedExemption(fset *token.FileSet, file *ast.File, call *ast.CallExpr) bool {
	callLine := fset.Position(call.Pos()).Line
	for _, cg := range file.Comments {
		startLine := fset.Position(cg.Pos()).Line
		endLine := fset.Position(cg.End()).Line
		if (endLine == callLine-1 || (startLine <= callLine && callLine <= endLine)) &&
			strings.Contains(strings.ToLower(cg.Text()), "exemption") {
			return true
		}
	}
	return false
}

// checkPorcelainPrintSites inspects an AST for unescaped fmt.Fprint* calls and
// returns a list of violation descriptions.
func checkPorcelainPrintSites(fset *token.FileSet, file *ast.File) []string {
	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		funcName, isFprint := isFprintCall(call)
		if !isFprint {
			return true
		}
		if isDocumentedExemption(fset, file, call) {
			return true
		}
		pos := fset.Position(call.Pos())
		violations = append(violations, pos.String()+": unescaped "+funcName+" call; use porcelainf or porcelainln instead")
		return true
	})
	return violations
}

// TestPorcelainPrintSites inspects cmd/writ non-test files that render human
// porcelain output for bare fmt.Fprint* calls to prevent foreign string spoofing
// routes from reopening. Documented exemptions (such as pre-escaped unified
// diffs or internal strings.Builder formatting) are allowed when marked with an
// "exemption:" comment.
func TestPorcelainPrintSites(t *testing.T) {
	dir := findPorcelainDir(t)
	fset := token.NewFileSet()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read dir %s: %v", dir, err)
	}

	var inspected []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if infrastructureFiles[name] {
			continue
		}

		inspected = append(inspected, name)
		path := filepath.Join(dir, name)
		node, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", path, err)
		}

		violations := checkPorcelainPrintSites(fset, node)
		for _, v := range violations {
			t.Errorf("%s", v)
		}
	}

	// Verify that key command files are present and inspected.
	for _, req := range []string{"object.go", "schema.go", "sync.go"} {
		found := false
		for _, name := range inspected {
			if name == req {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected %s to be inspected, but it was not found among inspected files: %v", req, inspected)
		}
	}
}

func TestPorcelainPrintSites_NegativeAndExemptions(t *testing.T) {
	fset := token.NewFileSet()
	src := `package test
import "fmt"
import "io"

func bad(w io.Writer) {
	fmt.Fprintf(w, "bad %s\n", "foo")
}

func goodExempt(w io.Writer, diff string) {
	// exemption: pre-escaped diff
	fmt.Fprint(w, diff)
}
`
	node, err := parser.ParseFile(fset, "negative_test.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse negative_test.go: %v", err)
	}

	violations := checkPorcelainPrintSites(fset, node)
	if len(violations) != 1 {
		t.Fatalf("expected exactly 1 violation for bad(), got %d: %v", len(violations), violations)
	}
	if !strings.Contains(violations[0], "negative_test.go") || !strings.Contains(violations[0], "fmt.Fprintf") {
		t.Errorf("unexpected violation text: %s", violations[0])
	}
}
