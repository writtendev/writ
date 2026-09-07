package writ_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine"
)

func TestAPIShapeNoGitInternalsLeak(t *testing.T) {
	// Root types and handles to reflect over
	targets := []any{
		writ.Store{},
		writ.Reviews{},
		writ.Issues{},
		writ.Comments{},
		writ.Drafts{},
		writ.Draft{},
		writ.DraftFilter{},
		writ.ReadState{},
		writ.Query{},
		writ.SyncResult{},
		writ.SyncStatus{},
		writ.ReviewResult{},
		writ.IssueResult{},
		writ.CommentResult{},
		writ.ObjectResult{},
		writ.ResolvedPosition{},
		writ.Author{},
		writ.NewReview{},
		writ.ReviewEdit{},
		writ.ReviewStatus{},
		writ.NewIssue{},
		writ.IssueEdit{},
		writ.IssueState{},
		writ.NewComment{},
		writ.CommentResolve{},
		writ.ReviewFilter{},
		writ.IssueFilter{},
		writ.CommentFilter{},
		writ.ObjectFilter{},
		writ.RefreshStats{},
		writ.ObjectChange{},
		writ.Event{},
		writ.EventKind(""),
		writ.Objects{},
		writ.NewOp{},
		writ.Object{},
		writ.SchemaType{},
		writ.SchemaField{},
		writ.SchemaOp{},
		writ.Review{},
		writ.Revision{},
		writ.Approval{},
		writ.CIStatus{},
		writ.Comment{},
		writ.CommentSubject{},
		writ.CommentThread{},
		writ.Issue{},
		writ.Link{},
		writ.Project{},
		writ.Cycle{},
		writ.Writer{},
	}

	for _, target := range targets {
		typ := reflect.TypeOf(target)
		checkType(t, typ, make(map[reflect.Type]bool))

		// Check pointer methods
		ptrTyp := reflect.PointerTo(typ)
		checkType(t, ptrTyp, make(map[reflect.Type]bool))
	}
}

func checkType(t *testing.T, typ reflect.Type, visited map[reflect.Type]bool) {
	if typ == nil || visited[typ] {
		return
	}
	visited[typ] = true

	// Check methods
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		if !m.IsExported() {
			continue
		}

		mType := m.Type
		// Check parameter types
		for p := 0; p < mType.NumIn(); p++ {
			inType := mType.In(p)
			assertNoGitLeak(t, typ.String()+"."+m.Name+" input", inType)
		}
		// Check return types
		for r := 0; r < mType.NumOut(); r++ {
			outType := mType.Out(r)
			assertNoGitLeak(t, typ.String()+"."+m.Name+" output", outType)
		}
	}

	// Check struct fields if struct
	if typ.Kind() == reflect.Struct {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			assertNoGitLeak(t, typ.String()+"."+f.Name+" field", f.Type)
			checkType(t, f.Type, visited)
		}
	} else if typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		checkType(t, typ.Elem(), visited)
	}
}

var forbiddenPackagePrefixes = []string{
	"github.com/go-git/go-git",
	"github.com/writtendev/writ/engine/dag.",
	"github.com/writtendev/writ/engine/identity.",
	"plumbing.",
	"object.",
	"storer.",
}

// TestObjectOmitsOpCommitSHAs pins the narrower of the two claims Object's
// own doc comment makes: Object never reaches writ.OpRef by name — in
// particular, it never regrows ObjectState.TotalOrder or any other field of
// that shape (round 1's own mutation, re-adding `TotalOrder []state.OpRef`,
// is exactly what this test catches). A package-prefix denylist
// (assertNoGitLeak above) cannot express this: writ.OpRef is legitimate
// public API surface on ObjectState (returned by Fold, for a caller who
// supplied ops and so has asked for op-shaped output), so banning
// engine/state wholesale would false-positive on that intentional exposure
// — and OpRef.Commit is an ordinary string besides, invisible to a check
// keyed on package path. So this checks the narrower, named-type invariant
// directly by walking Object's own field graph for reflect.TypeOf(OpRef{}).
//
// What this does NOT check: Object.UnknownOps' entries (state.UnknownOp)
// deliberately carry a Commit field of their own — the same op commit SHA
// OpRef.Commit holds, by design (see Object's doc comment) — and
// reachesType cannot see it, both because it is a different named type than
// OpRef and because a plain string is invisible to a check that only
// recognizes OpRef by identity. That is not a gap in this test; it is the
// one documented exception the doc comment states. reachesType's own
// remaining blind spots (an any-typed field, a differently-named string
// type) are noted on its doc comment.
func TestObjectOmitsOpCommitSHAs(t *testing.T) {
	opRefType := reflect.TypeOf(writ.OpRef{})
	if reachesType(reflect.TypeOf(writ.Object{}), opRefType, make(map[reflect.Type]bool)) {
		t.Errorf("writ.Object reaches writ.OpRef: Object must not carry a raw OpRef (see Object's doc comment) — a caller who asked only for an object id never asked for one")
	}
}

// reachesType reports whether target is typ itself or reachable from typ by
// walking pointers, slices, arrays, map keys/values, and every struct
// field's static type — exported or not — plus, for an interface-kinded
// field, only the interface type itself (never whatever concrete value it
// might hold at runtime, which reflection over a static type cannot see).
// It does NOT see: a SHA or other sensitive value carried as a
// differently-named type (a bare string, or a named string type such as
// `type CommitSHA string` — only exact type identity with target is
// checked, never the underlying kind); or anything reachable only through
// an interface value actually populated at runtime (map[string]any and
// other `any`-typed fields, such as Object.Fields, are walked as the empty
// interface type only — never through to a concrete value). It DOES see an
// unexported embedded carrier's own fields, in contrast to
// TestAPIShapeNoGitInternalsLeak's own checkType above, which skips
// unexported fields for its own, different purpose (walking exported public
// API surface only) — reflect.StructField.Type needs no instance value and
// so carries no visibility restriction, only reading a value out of an
// unexported field does. Widen deliberately, not by accident, if a future
// caller needs more.
func reachesType(typ, target reflect.Type, visited map[reflect.Type]bool) bool {
	if typ == nil || visited[typ] {
		return false
	}
	visited[typ] = true

	if typ == target {
		return true
	}

	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return reachesType(typ.Elem(), target, visited)
	case reflect.Map:
		return reachesType(typ.Key(), target, visited) || reachesType(typ.Elem(), target, visited)
	case reflect.Struct:
		// Every field's static type, exported or not: reflect.StructField.Type
		// needs no instance value and so carries no visibility restriction —
		// only reading a *value* out of an unexported field does. Walking
		// unexported fields too (rather than skipping them, as
		// TestAPIShapeNoGitInternalsLeak's own checkType above deliberately
		// does for its very different purpose) is what lets this see an
		// embedded unexported carrier struct, which Go's own field promotion
		// would otherwise make invisible to a check that only looked at
		// IsExported().
		for i := 0; i < typ.NumField(); i++ {
			if reachesType(typ.Field(i).Type, target, visited) {
				return true
			}
		}
	case reflect.Interface:
		// No case here reaches further: a static reflect.Type for an
		// interface-kinded field carries no information about what concrete
		// value it might hold at runtime, so nothing but the interface type
		// itself (already checked above, typ == target) is reachable from
		// it. Documented explicitly rather than left as a silent
		// fallthrough to the same "return false" every other unhandled Kind
		// takes, since this is the one Kind that matters most in principle:
		// Object.Fields is map[string]any, and an any-typed field is a
		// completely open door this test, or any test walking reflect.Type
		// alone, cannot see through.
	}
	return false
}

func assertNoGitLeak(t *testing.T, context string, typ reflect.Type) {
	t.Helper()
	if typ == nil {
		return
	}

	// Unwrap pointer / slice / array / map
	for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		typ = typ.Elem()
	}

	typeName := typ.String()
	pkgPath := typ.PkgPath()

	for _, forbidden := range forbiddenPackagePrefixes {
		if strings.Contains(typeName, forbidden) || strings.Contains(pkgPath, forbidden) {
			t.Errorf("API boundary leak in %s: type %q (package %q) leaks internal git/engine type", context, typeName, pkgPath)
		}
	}
}
