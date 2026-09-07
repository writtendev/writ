package projection

import (
	"fmt"
	"sync"

	"github.com/writtendev/writ/engine/state"
)

// builtinShapeDigestsOnce is every built-in object type's reference table
// shape (ddl.go's typeSnapshot, map-valued and so independent of rule or
// column declaration order), computed once per process from
// state.BuiltinRules() the same way buildDescriptor computes an installed
// type's own shape, and reduced to its canonical JSON encoding —
// map-keyed-so-sorted, exactly as buildDescriptor's own canonicalJSON/digest
// already relies on — so requireBuiltinShape can compare two shapes with a
// byte comparison instead of engine/projection's forbidden "reflect" import
// (TestImportsAllowlist). Comparing against this rather than against the
// built-in Rule slices directly matters: two rule slices producing an
// identical generated shape can still differ in slice order
// (RulesFromSchemas and state.BuiltinRules build theirs independently),
// which would false-positive a naive comparison of the rules themselves on
// an ordinary, un-redeclared type. Each reference reuses buildTypeDescriptor's
// own td.shapeJSON rather than re-marshaling — this already runs once per
// process, but there is no reason for it to duplicate that computation
// either.
var builtinShapeDigestsOnce = sync.OnceValue(func() map[string][]byte {
	rules, err := state.BuiltinRules()
	if err != nil {
		return nil
	}
	out := make(map[string][]byte, len(rules))
	for objectType, rs := range rules {
		td, _, ok, err := buildTypeDescriptor(objectType, rs, map[string]bool{})
		if err != nil || !ok || td == nil {
			continue
		}
		out[objectType] = td.shapeJSON
	}
	return out
})

// requireBuiltinShape refuses to let a typed reader (Reviews, Issues,
// Comments, and the rest of query.go's hand-written-SQL readers) run
// against an object type a log-declared schema has redeclared.
//
// mergeRules (engine/schema.go) replaces a built-in type's whole rule list
// with whatever the log declares for the same name — "log wins per type" is
// the plan's own rule, correct, and not something this changes: a consumer
// that redeclares "review" should see its table reshaped. What is wrong is
// the failure mode a typed reader hits when that happens: its SQL was
// generated against the built-in shape and has hard-coded column
// literals (r.f_title and the rest), so a reshaped o_review makes every
// call fail with an opaque "no such column" — reachable through the
// shipped `writ schema apply` and, once applied, permanent (nothing about
// the schema log ever reverts a redeclaration on its own). WRIT-192 deletes
// every typed reader in this file, so the fix here stays small and
// obvious: detect the mismatch before running the hand-written SQL and fail
// with a clear, named error instead — never adapt the query to whatever
// shape happens to be installed.
func (d *DB) requireBuiltinShape(objectType string) error {
	if d == nil {
		return nil
	}
	desc := d.descriptor()
	if desc == nil {
		return nil
	}
	installed, ok := desc.types[objectType]
	if !ok {
		// No schema resolved this type at all (or ddl.go withheld it for a
		// collision or an invalid identifier): a different, pre-existing
		// failure mode — "no such table" — not this one, and not something
		// a typed reader can usefully distinguish from "no schema yet"
		// without reaching into why the type is absent.
		return nil
	}
	ref, ok := builtinShapeDigestsOnce()[objectType]
	if !ok {
		// Not a built-in type at all (a typed reader is never called for
		// one), or state.BuiltinRules() itself failed — either way this
		// guard has nothing to compare against, so it stays out of the way.
		return nil
	}
	// installed.shapeJSON is computed once, in buildTypeDescriptor, rather
	// than re-marshaled on every call here (WRIT-189 round 3 MINOR-4).
	if string(installed.shapeJSON) != string(ref) {
		return fmt.Errorf("projection: object type %q was redeclared by a log-declared schema; this reader is generated for the built-in %q shape and no longer matches the installed one — query engine/projection's generic Object API, or writ.Schema, instead", objectType, objectType)
	}
	return nil
}
