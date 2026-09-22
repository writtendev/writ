package writ_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/writtendev/writ/engine"
)

func TestAPIShapeNoGitInternalsLeak(t *testing.T) {
	// Root types and handles to reflect over
	targets := []any{
		writ.Store{},
		writ.Query{},
		writ.SyncResult{},
		writ.SyncStatus{},
		writ.ObjectResult{},
		writ.Author{},
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
		writ.Writer{},
		writ.SchemaSource{},
		writ.InitOptions{},
		writ.InitResult{},
		writ.RemoteInit{},
		writ.TrustStoreStatus(""),
		writ.RejectError{},
		writ.VerificationOutcome(""),
		writ.FailureKind(""),
	}

	report := func(format string, args ...any) { t.Errorf(format, args...) }

	for _, target := range targets {
		typ := reflect.TypeOf(target)
		checkType(report, typ, make(map[reflect.Type]bool))

		// Check pointer methods
		ptrTyp := reflect.PointerTo(typ)
		checkType(report, ptrTyp, make(map[reflect.Type]bool))
	}
}

// checkType walks typ's exported methods and, for a struct, its exported
// fields — recursing through pointer, slice, array, chan, and map
// key/value element types so a composite field's own named type is
// reached before it is tested — reporting any git-plumbing leak found
// along the way through report. A method's parameter and result types get
// exactly the same treatment a field does: assertNoGitLeak catches an
// immediate leak (the parameter or result itself, or one hiding behind an
// unnamed wrapper or a named container's key/value), and the checkType
// recursion alongside it walks into a named-struct parameter or result's
// own fields the same way it walks a field's type. Without that second
// call, a go-git type sitting inside a plain struct passed to or returned
// from a method was never inspected at all — assertNoGitLeak has no case
// for reflect.Struct, so it stops at the parameter's own (unforbidden)
// PkgPath and never looks at what the struct holds (WRIT-298). report is
// threaded through rather than a *testing.T directly so a capturing
// caller (TestAPILeakGuardBites) can collect findings instead of failing
// the test outright.
func checkType(report func(format string, args ...any), typ reflect.Type, visited map[reflect.Type]bool) {
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
			assertNoGitLeak(report, typ.String()+"."+m.Name+" input", inType, make(map[reflect.Type]bool))
			checkType(report, inType, visited)
		}
		// Check return types
		for r := 0; r < mType.NumOut(); r++ {
			outType := mType.Out(r)
			assertNoGitLeak(report, typ.String()+"."+m.Name+" output", outType, make(map[reflect.Type]bool))
			checkType(report, outType, visited)
		}
	}

	// Check struct fields if struct, or recurse into a composite's
	// element type(s) otherwise.
	switch typ.Kind() {
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			assertNoGitLeak(report, typ.String()+"."+f.Name+" field", f.Type, make(map[reflect.Type]bool))
			checkType(report, f.Type, visited)
		}
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		checkType(report, typ.Elem(), visited)
	case reflect.Map:
		checkType(report, typ.Key(), visited)
		checkType(report, typ.Elem(), visited)
	}
}

// forbiddenPackagePrefixes asserts writ's one narrow claim: go-git plumbing
// must not reach a caller of the public API. Root-package aliases to
// internal types are intentional (WRIT-287) — writ.Op, writ.Envelope,
// writ.Rejection, writ.RejectReason, and the rest exist precisely so a
// caller never has to name internal/codec, internal/dag, or internal/state
// — so this list is necessarily package-granular, not type-granular:
// banning an internal package wholesale would false-positive on exactly
// the aliasing WRIT-287 designed (internal/codec and internal/projection
// are deliberately absent for that reason, and internal/dag joined them in
// WRIT-298 once writ.Rejection made its exposure through RefreshStats.Rejections
// intentional too). It also cannot be worked around by aliasing instead of
// omitting: reflection sees through a type alias, so writ.Rejection and
// dag.Rejection are the same reflect.Type, indistinguishable to this test.
// An intentional exposure is therefore expressed by leaving its package off
// this list, never by wrapping or aliasing it.
var forbiddenPackagePrefixes = []string{
	"github.com/go-git/go-git",
	"github.com/writtendev/writ/internal/identity",
	"github.com/writtendev/writ/internal/sync",
}

// TestAPILeakGuardBites proves TestAPIShapeNoGitInternalsLeak's walk
// actually fires rather than reporting green because it checks nothing
// (WRIT-298) — a green run against a corrected forbiddenPackagePrefixes
// proves nothing on its own; this test is what does the proving in CI.
func TestAPILeakGuardBites(t *testing.T) {
	t.Run("go-git type on an exported field", func(t *testing.T) {
		type leaky struct {
			Hash plumbing.Hash
		}
		var findings []string
		report := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

		checkType(report, reflect.TypeOf(leaky{}), make(map[reflect.Type]bool))

		if len(findings) == 0 {
			t.Fatal("expected a finding for leaky.Hash (plumbing.Hash), got none — the guard is not firing")
		}
		if !containsSubstring(findings, "leaky.Hash field") {
			t.Errorf("findings did not name leaky.Hash field: %v", findings)
		}
	})

	// The two subtests below each pin one of checkType's and
	// assertNoGitLeak's own map-recursion branches independently: a single
	// map[string]plumbing.Hash fixture cannot do this, because
	// plumbing.Hash has exported value-receiver methods (IsZero, String),
	// so checkType's map branch reaching plumbing.Hash directly rediscovers
	// the leak through the method-input path even with assertNoGitLeak's
	// own map branch deleted, and vice versa — either branch alone still
	// finds it, so deleting either one leaves such a fixture green. See
	// WRIT-298 round 1 review for the mutation table.
	t.Run("go-git type behind a map, reached by checkType's own recursion", func(t *testing.T) {
		// carrier itself is an ordinary local struct with no go-git
		// methods of its own — only checkType recursing into the map's
		// element type ever reaches carrier's field, so this fixture is
		// blind to assertNoGitLeak's map branch (deleting it changes
		// nothing here) and pins checkType's `case reflect.Map` alone:
		// delete that, and checkType never walks into carrier at all.
		type carrier struct {
			Hash plumbing.Hash
		}
		type leakyMap struct {
			Carriers map[string]carrier
		}
		var findings []string
		report := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

		checkType(report, reflect.TypeOf(leakyMap{}), make(map[reflect.Type]bool))

		if len(findings) == 0 {
			t.Fatal("expected a finding for leakyMap.Carriers (map[string]carrier, carrier.Hash is plumbing.Hash), got none — checkType must recurse into a map's element type to ever reach carrier's own fields")
		}
		if !containsSubstring(findings, "carrier.Hash field") {
			t.Errorf("findings did not name carrier.Hash field: %v", findings)
		}
	})

	t.Run("go-git type behind a chan, reached by checkType's own recursion", func(t *testing.T) {
		// carrier itself is an ordinary local struct with no go-git
		// methods of its own — only checkType recursing into the chan's
		// element type ever reaches carrier's field, so this pins
		// checkType's `case reflect.Chan` alone: delete chan from that
		// switch (leaving pointer/slice/array/map), and checkType never
		// walks into carrier at all, while the same field shape as a
		// slice, array, pointer, or map already reddens.
		type carrier struct {
			Hash plumbing.Hash
		}
		type leakyChan struct {
			C chan carrier
		}
		var findings []string
		report := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

		checkType(report, reflect.TypeOf(leakyChan{}), make(map[reflect.Type]bool))

		if len(findings) == 0 {
			t.Fatal("expected a finding for leakyChan.C (chan carrier, carrier.Hash is plumbing.Hash), got none — checkType must recurse into a chan's element type to ever reach carrier's own fields")
		}
		if !containsSubstring(findings, "carrier.Hash field") {
			t.Errorf("findings did not name carrier.Hash field: %v", findings)
		}
	})

	t.Run("go-git type behind a map, reached by assertNoGitLeak's own recursion", func(t *testing.T) {
		// object.Signature has no value-receiver methods and no
		// go-git-typed fields of its own (Name, Email are string; When is
		// time.Time), so checkType's own recursion into it, once reached,
		// finds nothing — only assertNoGitLeak's map branch asserting
		// directly on the map's value type ever catches this fixture,
		// pinning that branch alone: delete it, and this goes to zero
		// findings regardless of checkType's own map recursion.
		type leakyMap struct {
			Sigs map[string]object.Signature
		}
		var findings []string
		report := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

		checkType(report, reflect.TypeOf(leakyMap{}), make(map[reflect.Type]bool))

		if len(findings) == 0 {
			t.Fatal("expected a finding for leakyMap.Sigs (map[string]object.Signature), got none — object.Signature has no value-receiver methods and no go-git-typed fields of its own, so only assertNoGitLeak's own map key/value recursion can ever assert on it directly")
		}
		if !containsSubstring(findings, "leakyMap.Sigs field") {
			t.Errorf("findings did not name leakyMap.Sigs field: %v", findings)
		}
	})

	t.Run("go-git type behind a writ-named container", func(t *testing.T) {
		// Sigs is a named slice, not a map: reaching it stops the old
		// break-at-first-named-type read at Sigs's own (local, not
		// forbidden) PkgPath, unless assertNoGitLeak also recurses into a
		// named pointer/slice/array/chan's element the same way it does
		// for a map's key and value. Round 1 review found this coverage
		// missing; this pins it against a regression.
		type Sigs []object.Signature
		type namedContainer struct {
			S Sigs
		}
		var findings []string
		report := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

		checkType(report, reflect.TypeOf(namedContainer{}), make(map[reflect.Type]bool))

		if len(findings) == 0 {
			t.Fatal("expected a finding for namedContainer.S (Sigs, a named []object.Signature), got none — a named pointer/slice/array/chan's own PkgPath is never forbidden, so assertNoGitLeak must also recurse into its element to ever reach object.Signature")
		}
		if !containsSubstring(findings, "namedContainer.S field") {
			t.Errorf("findings did not name namedContainer.S field: %v", findings)
		}
	})

	t.Run("self-referential named composite does not crash the walk", func(t *testing.T) {
		// A named type whose own element is itself (type L []L, type P
		// *P, type M map[string]M) is a legal Go type. Round 2 review
		// found assertNoGitLeak's element recursion carried no visited
		// guard, unlike checkType and reachesType above, so reaching any
		// of these three shapes didn't report zero findings — it sent
		// the process into "fatal error: stack overflow" and took the
		// whole engine test binary down with it. Each shape below sits
		// next to a plumbing.Hash field so this test proves both halves
		// at once: the self-reference no longer crashes the walk, and
		// the walk still finds the real leak sitting beside it.
		type selfSlice []selfSlice
		type selfPtr *selfPtr
		type selfMap map[string]selfMap

		type recursive struct {
			Slice selfSlice
			Ptr   selfPtr
			Map   selfMap
			Hash  plumbing.Hash
		}
		var findings []string
		report := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

		checkType(report, reflect.TypeOf(recursive{}), make(map[reflect.Type]bool))

		if !containsSubstring(findings, "recursive.Hash field") {
			t.Fatalf("expected a finding for recursive.Hash (plumbing.Hash) despite the self-referential sibling fields, got: %v", findings)
		}
	})

	t.Run("named map type's own package is tested, not just its key/value", func(t *testing.T) {
		// The map branch used to return before the typ.Name() != ""
		// check ran, so a *named* map type's own PkgPath was never
		// tested — only its key and value were. forbiddenNamedMap is
		// declared right here, so this test registers this file's own
		// package as forbidden for its duration (restored on cleanup)
		// rather than adding a fixture type to a real internal package
		// just to prove the point; the ordering bug being pinned does
		// not care which package is on the list.
		type forbiddenNamedMap map[string]string
		type wrapper struct {
			M forbiddenNamedMap
		}

		forbidden := reflect.TypeOf(forbiddenNamedMap{}).PkgPath()
		forbiddenPackagePrefixes = append(forbiddenPackagePrefixes, forbidden)
		t.Cleanup(func() {
			forbiddenPackagePrefixes = forbiddenPackagePrefixes[:len(forbiddenPackagePrefixes)-1]
		})

		var findings []string
		report := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

		checkType(report, reflect.TypeOf(wrapper{}), make(map[reflect.Type]bool))

		if len(findings) == 0 {
			t.Fatal("expected a finding for wrapper.M (forbiddenNamedMap, whose own declaring package now matches forbiddenPackagePrefixes), got none — the map branch must test the named map type's own PkgPath before recursing into its key/value")
		}
		if !containsSubstring(findings, "wrapper.M field") {
			t.Errorf("findings did not name wrapper.M field: %v", findings)
		}
	})

	t.Run("intentional aliases stay clean", func(t *testing.T) {
		// writ.RefreshStats carries dag.Rejection (via Rejections) and
		// codec.RejectReason (via Rejection.Reason), plus the projection
		// types it already aliases — every one of them intentional
		// (WRIT-287, and WRIT-298's own ruling for the dag.Rejection
		// case). A corrected matcher firing on any of them would be
		// exactly the trap WRIT-287's round 1 fell into: a stricter
		// check catching intentional API instead of a real leak.
		var findings []string
		report := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

		checkType(report, reflect.TypeOf(writ.RefreshStats{}), make(map[reflect.Type]bool))

		if len(findings) != 0 {
			t.Errorf("writ.RefreshStats produced findings, want none: %v", findings)
		}
	})

	t.Run("go-git type inside a named-struct method parameter", func(t *testing.T) {
		// assertNoGitLeak has no case for reflect.Struct: on a struct
		// parameter it checks only that struct type's own (unforbidden)
		// PkgPath and stops, never looking at what the struct holds.
		// Round 4 review found this a real coverage hole, not a false
		// sentence to soften — a go-git type sitting inside a
		// named-struct method parameter reached zero findings before
		// this test, and before checkType's method loop (above) also
		// recursed structurally into the parameter/result type the same
		// way it already does for a field. methodParamHolder.Take's
		// carrier parameter (methodParamLeak, declared at package scope
		// below since Go methods cannot be declared inside a test
		// function body) exercises exactly that path: reverting the
		// `checkType(report, inType, visited)` / `checkType(report,
		// outType, visited)` calls added alongside checkType's
		// assertNoGitLeak calls on a method's parameter and result types
		// makes this subtest fail again.
		var findings []string
		report := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

		checkType(report, reflect.TypeOf(methodParamHolder{}), make(map[reflect.Type]bool))

		if len(findings) == 0 {
			t.Fatal("expected a finding for methodParamLeak.Hash (plumbing.Hash), reached only through methodParamHolder.Take's parameter type, got none — checkType must recurse structurally into a method's parameter and result types, the same way it recurses into a field's type, to ever reach a struct hiding behind a method signature")
		}
		if !containsSubstring(findings, "methodParamLeak.Hash field") {
			t.Errorf("findings did not name methodParamLeak.Hash field: %v", findings)
		}
	})

	t.Run("writ.Envelope and writ.Schema, newly reached by the method-signature walk, stay clean", func(t *testing.T) {
		// Before this walk covered method parameter and result types
		// structurally, writ.Envelope (Store.ApplySchema's and
		// Store.SchemaAfterApply's parameter, []codec.Envelope) and
		// writ.Schema (Store.Schema's and Store.SchemaAfterApply's
		// result) were parameter/result-only types: unreachable from any
		// walked target's own field graph, so their fields were never
		// inspected by TestAPIShapeNoGitInternalsLeak at all. Now that
		// checkType reaches them, both stay clean — codec.Envelope's
		// fields are string, int64, []byte, json.RawMessage, and
		// map[string]json.RawMessage; state.Schema's are string, []
		// SchemaType, and []state.UnknownOp, and state.UnknownOp's own
		// fields (reached transitively through Schema.UnknownOps) are
		// all string/int64 too — none of it go-git. Pinned here so a
		// regression introducing a go-git-typed field into either one is
		// caught by this test rather than discovered only once someone
		// notices the shipped API.
		var findings []string
		report := func(format string, args ...any) { findings = append(findings, fmt.Sprintf(format, args...)) }

		checkType(report, reflect.TypeOf(writ.Envelope{}), make(map[reflect.Type]bool))
		checkType(report, reflect.TypeOf(writ.Schema{}), make(map[reflect.Type]bool))

		if len(findings) != 0 {
			t.Errorf("writ.Envelope / writ.Schema produced findings, want none: %v", findings)
		}
	})
}

// methodParamLeak and methodParamHolder exist solely so
// TestAPILeakGuardBites' "go-git type inside a named-struct method
// parameter" subtest can exercise a go-git type sitting inside a struct
// passed as a method parameter — Go methods cannot be declared inside a
// test function body, so both are declared here at package scope instead.
// methodParamLeak itself has no go-git methods of its own and is never a
// field of any walked target; only methodParamHolder.Take's parameter
// ever reaches it.
type methodParamLeak struct {
	Hash plumbing.Hash
}

type methodParamHolder struct{}

// Take's sole purpose is to put methodParamLeak behind a method
// signature for the subtest above.
func (methodParamHolder) Take(c methodParamLeak) {}

func containsSubstring(haystack []string, substr string) bool {
	for _, s := range haystack {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
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

// assertNoGitLeak reports, through report, whether typ (or, for a
// composite, a named type reachable from it) has a package path matching
// one of forbiddenPackagePrefixes. Matching is on PkgPath alone, at package
// granularity: pkgPath == forbidden (the package itself) or
// strings.HasPrefix(pkgPath, forbidden+"/") (a subpackage of it) — never a
// substring match, and never on typ.String(), so a short local name like
// "plumbing" cannot accidentally shadow-match an unrelated identifier the
// way a bare "plumbing." substring check once could.
//
// Reaching a named type does not stop the walk: after checking a named
// pointer, slice, array, chan, or map's own PkgPath, the walk continues
// into its element (map key and value both) — so a writ-named container
// over a forbidden type, such as `type Sigs []object.Signature` or
// `type P *object.Signature`, is still caught even though the container's
// own PkgPath is this package, not go-git's. checkType's own NumMethod
// loop, above, is what reaches a method's parameter and result types
// before calling into this function — and it does the same for an
// interface's method set, since reflect.Type.NumMethod and Method work
// the same way whether typ is concrete or an interface. checkType then
// recurses into that same parameter or result type itself, exactly as it
// does for a field, so a named-struct parameter or result's own fields
// are walked too — assertNoGitLeak alone never would, since it has no
// case for reflect.Struct. Between the two functions, a forbidden type
// reached through a struct field, a container element, a method
// signature, a named-struct parameter or result's own fields, or an
// interface's method set is found.
//
// visited guards against a self-referential named composite (`type L
// []L`, `type P *P`, `type M map[string]M`) recursing forever — the same
// convention checkType and reachesType above use, and for the same
// reason: without it, a type like this doesn't report a finding, it
// crashes the test binary with a stack overflow.
func assertNoGitLeak(report func(format string, args ...any), context string, typ reflect.Type, visited map[reflect.Type]bool) {
	if typ == nil || visited[typ] {
		return
	}
	visited[typ] = true

	// Unwrap an *unnamed* pointer/slice/array/chan down to the type the
	// walk actually tests, stopping the moment a named type is reached: a
	// named type (plumbing.Hash, itself Kind Array) is the thing under
	// test, not a wrapper around it, and unwrapping past it would throw
	// away its identity and check its unnamed element type (byte) instead
	// — never matching anything. A map (named or not) isn't unwrapped
	// here — its key and value are two independent branches a single
	// reassignment can't express — so it falls straight through to the
	// pkgPath check below (a no-op for an unnamed map, whose PkgPath is
	// always "") and is recursed into from the trailing switch, the same
	// as every other kind this walk continues past a named type for.
	// Checking Kind() == reflect.Map here and returning early, the way an
	// earlier version of this loop did, would test the map's key and
	// value but never the map type's own PkgPath, missing exactly the
	// named-map-in-a-forbidden-package case a plain named struct already
	// catches.
	for {
		if typ.Name() != "" {
			break
		}
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
			typ = typ.Elem()
			continue
		}
		break
	}

	pkgPath := typ.PkgPath()

	for _, forbidden := range forbiddenPackagePrefixes {
		if pkgPath == forbidden || strings.HasPrefix(pkgPath, forbidden+"/") {
			report("API boundary leak in %s: type %q (package %q) leaks internal git/engine type", context, typ.String(), pkgPath)
		}
	}

	// A *named* pointer, slice, array, chan, or map (e.g. `type Sigs
	// []object.Signature` or `type M map[string]object.Signature`) had
	// its own PkgPath checked just above like any other named type — but
	// that check alone would miss a forbidden type hiding behind a
	// writ-named container, since the container's own package is this
	// one, never go-git's. Recurse into its element (map key and value
	// both, unconditionally — an unnamed map reaches here too, having
	// skipped the pkgPath check above only in the sense that it was
	// always a no-op for it) the same way a named container's does.
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
		assertNoGitLeak(report, context, typ.Elem(), visited)
	case reflect.Map:
		assertNoGitLeak(report, context, typ.Key(), visited)
		assertNoGitLeak(report, context, typ.Elem(), visited)
	}
}
