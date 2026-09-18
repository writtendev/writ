package identity_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestLoadReturnsCarryTheReason makes WRIT-143's invariant structural rather
// than conventional.
//
// The invariant — an Identity with an empty PersonID always carries a non-nil
// PersonIDErr — is upheld in Load by convention: a failure returns loadFailed,
// which sets the field, or baseIdent, which already carries whatever
// DerivePersonID said. Nothing stopped the next early return from being
// `return Identity{}, err`, which is precisely the shape the ticket is about,
// and TestLoad_EmptyPersonIDAlwaysExplained enumerates Load's returns by hand
// — the same blind spot as the code. It shipped one return short, and that
// return could have been broken with the suite green.
//
// So read the returns out of the source instead of listing them. Every failing
// return in Load must hand back one of the two shapes that carry a reason, and
// the one return that succeeds must build an Identity that sets PersonIDErr.
//
// What this test enforces, stated exactly, because a guard whose docstring
// overstates it is worse than no guard:
//
//   - Load's results are unnamed. Named results let a deferred closure blank
//     PersonIDErr after the return statement has been written, which no check
//     over return statements can see.
//   - Neither loadFailed nor baseIdent is rebound inside Load. Both are
//     accepted here by name, so a local `loadFailed := func(error) Identity`
//     would turn an accepted shape into a bare Identity{}, and baseIdent
//     reassigned after DerivePersonID would do the same.
//   - baseIdent is assigned once, from a composite literal that sets
//     PersonIDErr, and none of its fields is assigned afterwards. The
//     literal is the whole of what the return check trusts, and a field
//     assignment is not a rebinding: `baseIdent.PersonIDErr = nil` on the
//     line after the literal left the assignment count at one, satisfied
//     the composite-literal requirement, and blanked the field anyway. So
//     the check looks past *ast.Ident on the left-hand side.
//   - No return hands back loadFailed(nil): the argument is the reason, and
//     nil for it reproduces the exact defect — PersonID "" with PersonIDErr
//     nil — through an expression this check would otherwise accept.
//
// What it cannot see, and what the sweep in identity_test.go remains the test
// of: the values those expressions actually evaluate to. loadFailed(err) with
// a genuinely nil err in flight is a runtime fact, not a syntactic one.
//
// Deliberately narrow: one function, two accepted expressions, no
// configuration. It is a lint in the shape of engine/internal/fold's
// imports_test.go, not a framework.
func TestLoadReturnsCarryTheReason(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "identity.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse identity.go: %v", err)
	}

	var load *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Recv == nil && fn.Name.Name == "Load" {
			load = fn
			break
		}
	}
	if load == nil {
		t.Fatal("no func Load in identity.go: this test guards Load's returns and has lost track of it")
	}

	// Named results are checked before the returns are, because they defeat
	// the return check outright: with `func Load(...) (ident Identity, err
	// error)` a deferred closure can blank ident.PersonIDErr without any
	// return statement being touched.
	if load.Type.Results != nil {
		for _, field := range load.Type.Results.List {
			if len(field.Names) > 0 {
				t.Errorf("%s: Load must not use named results — a deferred closure can rewrite the returned Identity after the return statement, which the check below cannot see",
					fset.Position(field.Pos()))
			}
		}
	}

	// Both accepted expressions are accepted by name, so a local binding of
	// either name inside Load makes this check meaningless. Neither is ever
	// legitimately rebound: loadFailed is a package-level func, and baseIdent
	// is built once, from DerivePersonID's output.
	baseIdentAssignments := 0
	checkBinding := func(lhs ast.Expr, rhs ast.Expr) {
		// A field assignment is not a rebinding, and checking only
		// *ast.Ident missed it entirely: baseIdent stayed assigned once,
		// from the composite literal this test demands, while the next line
		// set the field back to nil. The return check accepts baseIdent by
		// name on the strength of that literal, so nothing may edit what the
		// literal produced.
		if root, sel := rootIdent(lhs); sel != nil {
			if root != nil && root.Name == "baseIdent" {
				t.Errorf("%s: Load assigns to baseIdent.%s — the return check accepts baseIdent by name because of the composite literal it is built from, and an assignment to one of its fields is invisible to that (WRIT-143)",
					fset.Position(sel.Pos()), sel.Sel.Name)
			}
			return
		}
		name, ok := lhs.(*ast.Ident)
		if !ok {
			return
		}
		switch name.Name {
		case "loadFailed":
			t.Errorf("%s: Load rebinds loadFailed — the return check below accepts that name, so shadowing it lets a failing return hand back anything",
				fset.Position(name.Pos()))
		case "baseIdent":
			baseIdentAssignments++
			if !setsPersonIDErr(rhs) {
				t.Errorf("%s: baseIdent must be built from a composite literal that sets PersonIDErr — the return check accepts it on the strength of that field",
					fset.Position(name.Pos()))
			}
		}
	}

	var failing, succeeding int
	ast.Inspect(load.Body, func(n ast.Node) bool {
		switch stmt := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range stmt.Lhs {
				var rhs ast.Expr
				if len(stmt.Rhs) == len(stmt.Lhs) {
					rhs = stmt.Rhs[i]
				}
				checkBinding(lhs, rhs)
			}
			return true
		case *ast.DeclStmt:
			decl, ok := stmt.Decl.(*ast.GenDecl)
			if !ok {
				return true
			}
			for _, spec := range decl.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range value.Names {
					var rhs ast.Expr
					if i < len(value.Values) {
						rhs = value.Values[i]
					}
					checkBinding(name, rhs)
				}
			}
			return true
		}

		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		// Nested function literals return from themselves, not from Load —
		// but the walk descends into them anyway, to catch a binding declared
		// inside one, so their returns have to be filtered out here instead of
		// by pruning the branch.
		if !returnsFromLoad(load, ret) {
			return true
		}
		if len(ret.Results) != 2 {
			t.Errorf("%s: return with %d results, want 2 (Identity, error)", fset.Position(ret.Pos()), len(ret.Results))
			return true
		}

		if ident, ok := ret.Results[1].(*ast.Ident); ok && ident.Name == "nil" {
			succeeding++
			// The success return is the one that may build a literal, and it
			// gets the same requirement baseIdent does: Load can succeed with
			// an empty PersonID — DerivePersonID's error is not fatal — so
			// the literal has to carry the reason too.
			if !setsPersonIDErr(ret.Results[0]) && !isAccepted(ret.Results[0]) {
				t.Errorf("%s: Load's succeeding return must set PersonIDErr: DerivePersonID can fail without failing Load, and an empty PersonID still carries the reason it is empty (WRIT-143)",
					fset.Position(ret.Pos()))
			}
			return true
		}
		failing++

		switch got := ret.Results[0].(type) {
		case *ast.CallExpr:
			if fn, ok := got.Fun.(*ast.Ident); ok && fn.Name == "loadFailed" {
				// loadFailed(nil) passes the name check and produces exactly
				// the shape the ticket names: PersonID "" with PersonIDErr
				// nil. The argument is the whole point of the call.
				if len(got.Args) == 1 {
					if arg, ok := got.Args[0].(*ast.Ident); ok && arg.Name == "nil" {
						t.Errorf("%s: loadFailed(nil) hands back an Identity with an empty PersonID and no reason — pass the error that caused the failure (WRIT-143)",
							fset.Position(got.Pos()))
					}
				}
				return true
			}
		case *ast.Ident:
			// baseIdent is built after DerivePersonID has run, so it already
			// carries PersonIDErr when the identifier did not resolve. The
			// binding check above is what keeps that true.
			if got.Name == "baseIdent" {
				return true
			}
		}
		t.Errorf("%s: a failing return in Load must hand back loadFailed(err) or baseIdent, so that an empty PersonID carries the reason it is empty (WRIT-143); a bare Identity{} here is the defect the ticket names",
			fset.Position(ret.Pos()))
		return true
	})

	// Both counts are asserted so that a Load rewritten into something this
	// walk no longer sees fails loudly instead of passing vacuously.
	if succeeding != 1 {
		t.Errorf("Load has %d returns with a nil error, want exactly 1", succeeding)
	}
	if failing == 0 {
		t.Error("Load has no failing returns: this test found nothing to check")
	}
	if baseIdentAssignments != 1 {
		t.Errorf("Load assigns baseIdent %d times, want exactly 1: the return check accepts the identifier, so a later reassignment would go unseen", baseIdentAssignments)
	}
}

// rootIdent peels an assignment target down to the identifier it is rooted at,
// returning that identifier and the outermost selector when there was one:
// baseIdent.PersonIDErr gives (baseIdent, .PersonIDErr), and a bare baseIdent
// gives (baseIdent, nil). Either may be nil for a target rooted at something
// that is not an identifier, which this check has nothing to say about.
func rootIdent(e ast.Expr) (*ast.Ident, *ast.SelectorExpr) {
	outer, ok := e.(*ast.SelectorExpr)
	if !ok {
		return nil, nil
	}
	for {
		switch x := e.(type) {
		case *ast.SelectorExpr:
			e = x.X
		case *ast.Ident:
			return x, outer
		default:
			return nil, outer
		}
	}
}

// isAccepted reports whether e is one of the two expressions a return in Load
// may hand back as its Identity.
func isAccepted(e ast.Expr) bool {
	switch got := e.(type) {
	case *ast.CallExpr:
		fn, ok := got.Fun.(*ast.Ident)
		return ok && fn.Name == "loadFailed"
	case *ast.Ident:
		return got.Name == "baseIdent"
	}
	return false
}

// setsPersonIDErr reports whether e is a composite literal with a PersonIDErr
// field.
func setsPersonIDErr(e ast.Expr) bool {
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return false
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "PersonIDErr" {
			return true
		}
	}
	return false
}

// returnsFromLoad reports whether ret returns from load itself rather than
// from a function literal nested inside it. The walk descends into literals to
// find bindings, so it meets their returns too.
func returnsFromLoad(load *ast.FuncDecl, ret *ast.ReturnStmt) bool {
	inner := false
	ast.Inspect(load.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		if lit.Pos() <= ret.Pos() && ret.End() <= lit.End() {
			inner = true
		}
		return true
	})
	return !inner
}
