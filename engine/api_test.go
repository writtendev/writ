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
		writ.Group{},
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
