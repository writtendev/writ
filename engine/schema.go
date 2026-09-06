package writ

import (
	"context"
	"fmt"
	"sort"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec"
)

// Schema is the materialized state of a `schema` collaborative object (v1).
type Schema = state.Schema

// SchemaType is a schema-declared object type (v1).
type SchemaType = state.SchemaType

// SchemaField is a schema-declared field within a type's op vocabulary (v1).
type SchemaField = state.SchemaField

// SchemaOp is a schema-declared op type within a type's vocabulary (v1).
type SchemaOp = state.SchemaOp

// SchemaConflict records a load-bearing collision between schema objects,
// found by RulesFromSchemas. No winner is ever picked: on an ObjectType
// collision, RulesFromSchemas installs no rules at all for that object_type,
// so its ops fall through the absent-schema path to UnknownOp (FC-1, FC-12)
// exactly as if no schema had defined it.
type SchemaConflict struct {
	// ObjectType is set for an object_type collision (two schema objects
	// binding the same bare type) and for a single field-rule validation
	// failure; empty for a namespace-only collision.
	ObjectType string `json:"object_type,omitempty"`
	// Namespace is set for a namespace collision, and echoed on an
	// object_type collision when known.
	Namespace string `json:"namespace,omitempty"`
	// ObjectIDs names the schema objects involved: two for a collision
	// between schema objects, one for a single object's own invalid rule or
	// its attempt to redefine `schema` itself.
	ObjectIDs []string `json:"object_ids"`
	Reason    string   `json:"reason"`
}

// Schema folds every `schema` object present in the log and returns their
// materialized state, one entry per schema object, ordered by ObjectID.
//
// Schema reads from the DAG directly, not the projection cache: the
// projection is a droppable cache (ARCHITECTURE.md §The six machines #5),
// and nothing about resolving field rules from the log may depend on it
// having been built or refreshed.
func (s *Store) Schema(ctx context.Context) ([]state.Schema, error) {
	if s == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}

	enumRes, err := s.dagStore.Enumerate()
	if err != nil {
		return nil, fmt.Errorf("writ: enumerate schema objects: %w", err)
	}

	var schemas []state.Schema
	for objectID, ops := range enumRes.Ops {
		if !anyOpHasObjectType(ops, "schema") {
			continue
		}
		sch, err := state.FoldSchema(ops)
		if err != nil {
			return nil, fmt.Errorf("writ: fold schema object %s: %w", objectID, err)
		}
		schemas = append(schemas, sch)
	}

	sort.Slice(schemas, func(i, j int) bool { return schemas[i].ObjectID < schemas[j].ObjectID })
	return schemas, nil
}

// anyOpHasObjectType is a cheap discovery filter, not a fold decision:
// FoldSchema itself quarantines any op whose object_type or op_version does
// not match `schema`/1, exactly as every other typed reducer quarantines a
// mismatched op, so an object_id shared — in error, or by a hostile writer —
// between schema and non-schema ops still folds correctly; it just reports
// the foreign ops as unknown rather than silently omitting the object here.
func anyOpHasObjectType(ops []codec.Op, objectType string) bool {
	for _, op := range ops {
		if op.ObjectType == objectType {
			return true
		}
	}
	return false
}

// RulesFromSchemas is the pure, I/O-free resolver that turns every folded
// schema object present in a repo into the []Rule shape the generic fold
// driver (Fold(ops, rules)) already consumes generically — the same shape
// state.SchemaRules and every other built-in *Rules() function already
// return. It lives in package writ, not engine/internal/fold, because it
// needs spec.ValidateFieldRule and the fold package's import allowlist does
// not include spec (ARCHITECTURE.md §Schema layer keeps fold pure).
//
// Two things this resolves, neither of them the fold's job:
//
//   - Rule validation (spec/schema-ops.md §Rule validation, WRIT-196). The
//     fold path performs no value-type checking (spec/value-types.md
//     §Producer-side and reader-tolerant), so a define-field op carrying
//     strategy:"" or strategy:"bogus" folds cleanly into schema state, and
//     handing that rule to Fold reaches NewAccumulator as a hard error. Every
//     candidate rule is validated through spec.ValidateFieldRule before it
//     can be installed; one that fails is dropped and reported here, never
//     handed to the fold driver.
//   - Conflicts (spec/schema-ops.md §Conflicts). object_type is what reaches
//     the wire — namespace never does (Correction 2) — so the load-bearing
//     collision is two schema objects binding the same bare object_type.
//     Neither schema's rules are installed for the contested type: no winner
//     is picked, and its ops fall through the absent-schema path to
//     UnknownOp. Two schemas declaring the same namespace is a weaker,
//     mostly cosmetic case, reported alongside the first but never
//     withholding rules on its own. A version bump that changes strategy
//     while reusing a target already bound to a different strategy
//     (spec/fold.md §5, spec/schema-ops.md §Evolution) is rejected the same
//     way: the rule is dropped and the collision reported.
//
// Schema objects are visited in ascending ObjectID order so two conforming
// implementations build the same index from the same input regardless of
// enumeration order.
func RulesFromSchemas(schemas []state.Schema) (map[string][]Rule, []SchemaConflict) {
	sorted := append([]state.Schema(nil), schemas...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ObjectID < sorted[j].ObjectID })

	boundBy := make(map[string]string)        // object_type -> owning schema ObjectID
	namespaceOwner := make(map[string]string) // namespace -> owning schema ObjectID
	contested := make(map[string]bool)        // object_type -> withheld from installation
	var conflicts []SchemaConflict

	for _, sch := range sorted {
		if sch.Namespace != "" {
			if owner, ok := namespaceOwner[sch.Namespace]; ok {
				if owner != sch.ObjectID {
					conflicts = append(conflicts, SchemaConflict{
						Namespace: sch.Namespace,
						ObjectIDs: []string{owner, sch.ObjectID},
						Reason:    fmt.Sprintf("namespace %q is declared by more than one schema object", sch.Namespace),
					})
				}
			} else {
				namespaceOwner[sch.Namespace] = sch.ObjectID
			}
		}

		for _, t := range sch.Types {
			if t.Name == "schema" {
				contested["schema"] = true
				conflicts = append(conflicts, SchemaConflict{
					ObjectType: "schema",
					Namespace:  sch.Namespace,
					ObjectIDs:  []string{sch.ObjectID},
					Reason:     "schema is the engine's built-in bootstrap type and cannot be redefined from the log",
				})
				continue
			}
			owner, bound := boundBy[t.Name]
			if !bound {
				boundBy[t.Name] = sch.ObjectID
				continue
			}
			if owner != sch.ObjectID && !contested[t.Name] {
				contested[t.Name] = true
				conflicts = append(conflicts, SchemaConflict{
					ObjectType: t.Name,
					ObjectIDs:  []string{owner, sch.ObjectID},
					Reason:     fmt.Sprintf("object_type %q is bound by more than one schema object", t.Name),
				})
			}
		}
	}

	rules := make(map[string][]Rule)
	for _, sch := range sorted {
		for _, t := range sch.Types {
			if t.Name == "schema" || contested[t.Name] {
				continue
			}

			targetStrategy := make(map[string]string)
			var typeRules []Rule
			for _, f := range t.Fields {
				// deprecated:true is metadata discouraging new writes, not a
				// removal (spec/schema-ops.md §5, §8): the rule stays
				// installed and active for folding so ops already signed
				// under it keep folding to the same state, with Deprecated
				// carried onto the resolved Rule for a producer or UI to
				// read.
				r := Rule{
					OpType:     f.OpType,
					OpVersion:  f.OpVersion,
					Field:      f.Name,
					Target:     f.Target,
					Strategy:   f.Strategy,
					Key:        f.Key,
					Lattice:    f.Lattice,
					ValueType:  f.ValueType,
					Enum:       f.Enum,
					MaxLength:  f.MaxLength,
					KeyTypes:   f.KeyTypes,
					Deprecated: f.Deprecated,
				}

				sr := spec.FieldRule{
					OpType: r.OpType, OpVersion: r.OpVersion, Field: r.Field, Target: r.Target,
					Strategy: r.Strategy, Key: r.Key, Lattice: r.Lattice, ValueType: r.ValueType,
					Enum: r.Enum, MaxLength: r.MaxLength, KeyTypes: r.KeyTypes,
				}
				if err := spec.ValidateFieldRule(sr); err != nil {
					conflicts = append(conflicts, SchemaConflict{
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     fmt.Sprintf("field rule (%s, %d, %s) is invalid and was not installed: %v", r.OpType, r.OpVersion, r.Field, err),
					})
					continue
				}

				targetKey := r.TargetKey()
				if prior, ok := targetStrategy[targetKey]; ok {
					if prior != r.Strategy {
						conflicts = append(conflicts, SchemaConflict{
							ObjectType: t.Name,
							ObjectIDs:  []string{sch.ObjectID},
							Reason: fmt.Sprintf(
								"field rule (%s, %d, %s) reuses target %q already bound to strategy %q with a different strategy %q; a version bump that changes strategy must declare a distinct target",
								r.OpType, r.OpVersion, r.Field, targetKey, prior, r.Strategy),
						})
						continue
					}
				} else {
					targetStrategy[targetKey] = r.Strategy
				}

				typeRules = append(typeRules, r)
			}

			if len(typeRules) > 0 {
				rules[t.Name] = typeRules
			}
		}
	}

	return rules, conflicts
}
