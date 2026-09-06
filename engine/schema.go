package writ

import (
	"context"
	"fmt"
	"sort"
	"time"

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

// ApplySchema appends a compiled `schema` op sequence (schemasrc.Compile's
// output, ordinarily) through the ordinary signed producer path, exactly as
// any other multi-op write does (Reviews.Create is the model): every
// envelope is validated before the first is appended, so a sequence that
// would fail part-way never writes its earlier ops either.
//
// Every envelope in envs must share one ObjectID and be ObjectType
// "schema", OpVersion 1 — cmd/writ's `schema apply` is the only caller
// today, and schemasrc.Compile only ever emits that shape, but ApplySchema
// checks it anyway because a public append path takes what a caller hands
// it, not what today's one caller happens to send.
//
// The first append carries the target object's current frontier (the ops
// with no child within that object, across every writer, exactly what
// Reviews.Update and friends pass via projection.Frontier — schema has no
// projection support, so this is computed directly from the DAG) as causal
// parents, so a fresh sequence extending an object other writers have
// already written to causally follows their ops rather than racing them
// blind. Every later append in the same call passes no causal parents: it
// inherits causality through its own writer-chain parent, which Append sets
// automatically to the op this same call just wrote — the same construction
// Reviews.Create uses for its two-op sequence.
//
// An empty envs appends nothing and returns nil. A failure part-way through
// leaves a partially applied schema, which is additive and not corrupt
// (nothing written is ever wrong, only incomplete): re-running the smaller
// remaining delta finishes the job.
func (s *Store) ApplySchema(ctx context.Context, envs []codec.Envelope) error {
	if s == nil {
		return fmt.Errorf("writ: store is nil")
	}
	if err := s.ensureWritable(); err != nil {
		return err
	}
	if len(envs) == 0 {
		return nil
	}

	objectID := envs[0].ObjectID
	for i, env := range envs {
		if env.ObjectType != "schema" {
			return fmt.Errorf("writ: apply schema: envelope %d has object_type %q, want \"schema\"", i, env.ObjectType)
		}
		if env.OpVersion != 1 {
			return fmt.Errorf("writ: apply schema: envelope %d has op_version %d, want 1", i, env.OpVersion)
		}
		if env.ObjectID != objectID {
			return fmt.Errorf("writ: apply schema: mixed object ids %q and %q", objectID, env.ObjectID)
		}
	}

	if err := checkBeforeAppend(envs...); err != nil {
		return fmt.Errorf("writ: apply schema: %w", err)
	}

	enumRes, err := s.dagStore.Enumerate()
	if err != nil {
		return fmt.Errorf("writ: apply schema: enumerate: %w", err)
	}
	frontier := schemaFrontier(enumRes.Ops[objectID])

	for i, env := range envs {
		var parents []string
		if i == 0 {
			parents = frontier
		}
		if _, err := s.dagStore.Append(ctx, env, parents); err != nil {
			return fmt.Errorf("writ: apply schema: append %s: %w", env.OpType, err)
		}
	}

	_ = s.maybeAutoRefresh(ctx)
	return nil
}

// schemaFrontier returns the op commits within ops that no other op in ops
// names as a parent — the same "no child dependencies within this object"
// definition projection.DB.Frontier uses, computed directly over an
// in-memory op slice because schema ops are never materialized into the
// projection (spec/schema-ops.md §1.2: they land in unknown_ops).
func schemaFrontier(ops []codec.Op) []string {
	if len(ops) == 0 {
		return nil
	}
	hasChild := make(map[string]bool, len(ops))
	for _, op := range ops {
		for _, p := range op.Parents {
			hasChild[p] = true
		}
	}
	var frontier []string
	for _, op := range ops {
		if !hasChild[op.ID] {
			frontier = append(frontier, op.ID)
		}
	}
	sort.Strings(frontier)
	return frontier
}

// SchemaFromEnvelopes folds a compiled op sequence (schemasrc.Compile's
// output, ordinarily) in memory — no DAG access, no I/O — so a caller can
// render the schema state a proposed apply would produce without appending
// anything. `writ schema plan` is the intended caller: it folds the current
// log state via Store.Schema, folds the proposed post-apply state via
// SchemaFromEnvelopes, and renders both for comparison.
//
// Every envelope must share one ObjectID and be ObjectType "schema". They
// are wired into a synthetic linear chain in the order given, with strictly
// increasing synthetic author timestamps, so OrderWithTStar's total order
// matches input order exactly — the same construction
// engine/schemasrc's own tests use (envelopesToOps) to drive
// state.FoldSchema directly, reproduced here because this function needs
// no I/O and must not depend on a test-only helper in another package.
func SchemaFromEnvelopes(envs []codec.Envelope) (Schema, error) {
	if len(envs) == 0 {
		return Schema{}, nil
	}

	objectID := envs[0].ObjectID
	ops := make([]codec.Op, len(envs))
	base := time.Unix(0, 0).UTC()
	var parent string
	for i, env := range envs {
		if env.ObjectType != "schema" {
			return Schema{}, fmt.Errorf("writ: SchemaFromEnvelopes: envelope %d has object_type %q, want \"schema\"", i, env.ObjectType)
		}
		if env.ObjectID != objectID {
			return Schema{}, fmt.Errorf("writ: SchemaFromEnvelopes: mixed object ids %q and %q", objectID, env.ObjectID)
		}

		id := fmt.Sprintf("synthetic-%06d", i)
		var parents []string
		if parent != "" {
			parents = []string{parent}
		}
		ops[i] = codec.Op{
			Envelope: env,
			ID:       id,
			Parents:  parents,
			Author:   codec.Identity{When: base.Add(time.Duration(i) * time.Second)},
		}
		parent = id
	}

	return state.FoldSchema(ops)
}

// SchemaAfterApply folds objectID's ops already in the log (if any)
// together with delta, a proposed sequence of envelopes to append next
// (cmd/writ's schemaDelta output, ordinarily), and returns the schema
// state a real ApplySchema of that same delta would actually produce — no
// I/O, nothing written.
//
// This differs from SchemaFromEnvelopes, which folds a sequence as though
// it were the object's *entire* history. state.FoldSchema's define-field
// case merges each attribute (value_type, enum, max_length, lattice, key,
// key_types, target) independently, overwriting a register only when a
// later op's body actually carries that key (spec/schema-ops.md §8's
// keyed-lww semantics) — so an attribute an earlier op set and a later
// op's body omits survives the fold. Folding delta alone, detached from
// the real history it would land on top of, shows only what delta itself
// declares; it cannot show an attribute the log already holds re-surfacing
// because delta's body doesn't mention it. A caller that needs to know the
// schema state the log will really hold after appending delta — `writ
// schema plan`'s conflict check is the intended caller — has to fold the
// real history and the proposal together, which is exactly what this
// function does.
//
// delta is wired onto the object's real frontier exactly as ApplySchema
// wires a fresh append: the first envelope's synthetic op takes the
// frontier as its parents, so it folds causally after every op already in
// the log, and each later envelope chains off the synthetic op before it —
// the same construction ApplySchema itself uses (and schemaFrontier
// computes), reproduced here because this function must not write
// anything.
func (s *Store) SchemaAfterApply(ctx context.Context, objectID string, delta []codec.Envelope) (Schema, error) {
	if s == nil {
		return Schema{}, fmt.Errorf("writ: store is nil")
	}

	enumRes, err := s.dagStore.Enumerate()
	if err != nil {
		return Schema{}, fmt.Errorf("writ: schema after apply: enumerate: %w", err)
	}
	currentOps := enumRes.Ops[objectID]

	if len(delta) == 0 {
		if len(currentOps) == 0 {
			return Schema{}, nil
		}
		return state.FoldSchema(currentOps)
	}

	frontier := schemaFrontier(currentOps)
	base := time.Unix(0, 0).UTC()
	var parent string
	deltaOps := make([]codec.Op, len(delta))
	for i, env := range delta {
		if env.ObjectType != "schema" {
			return Schema{}, fmt.Errorf("writ: schema after apply: envelope %d has object_type %q, want \"schema\"", i, env.ObjectType)
		}
		if env.ObjectID != objectID {
			return Schema{}, fmt.Errorf("writ: schema after apply: envelope %d has object id %q, want %q", i, env.ObjectID, objectID)
		}

		id := fmt.Sprintf("synthetic-after-apply-%06d", i)
		var parents []string
		switch {
		case i == 0:
			parents = frontier
		case parent != "":
			parents = []string{parent}
		}
		deltaOps[i] = codec.Op{
			Envelope: env,
			ID:       id,
			Parents:  parents,
			Author:   codec.Identity{When: base.Add(time.Duration(i) * time.Second)},
		}
		parent = id
	}

	allOps := make([]codec.Op, 0, len(currentOps)+len(deltaOps))
	allOps = append(allOps, currentOps...)
	allOps = append(allOps, deltaOps...)

	return state.FoldSchema(allOps)
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

			targetBindings := make(map[string][]spec.FieldRule)
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
					ObjectType: t.Name,
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

				if err := spec.CheckTargetCollision(targetBindings, sr); err != nil {
					conflicts = append(conflicts, SchemaConflict{
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     err.Error(),
					})
					continue
				}
				targetBindings[sr.TargetKey()] = append(targetBindings[sr.TargetKey()], sr)

				typeRules = append(typeRules, r)
			}

			if len(typeRules) > 0 {
				rules[t.Name] = typeRules
			}
		}
	}

	return rules, conflicts
}
