package writ

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/dag"
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

// vocabularies resolves the log-sourced producer vocabularies
// (VocabulariesFromSchemas), memoised behind a fingerprint over the repo's
// discovered chains (dag.Chains): a fetch or a local "schema" append moves
// at least one chain's tip in a way that can change what a schema object
// resolves to, so it invalidates the cache; nothing else does. A cache
// hit costs one IterReferences pass (Chains) plus a fingerprint
// comparison — no fold, no full log walk.
//
// The naive version of that statement is false on the write path, which is
// the only path that calls this: every Append moves the writer's own
// chain's tip too, so without help every single append would look like an
// invalidating change and pay for a full Schema/Enumerate fold, the exact
// per-append cost this cache exists to avoid. Store.noteAppend is the
// help — dag.WithChainObserver tells this Store, after each successful
// local append, which chain moved and to what, and Append never accepts a
// "schema" ObjectType there (checkBeforeAppend and dag.Store.Append both
// skip the resolver for it, spec/schema-ops.md §7) — so noteAppend can
// roll a non-"schema" append's chain forward in the cached snapshot
// in place, keeping the fingerprint in step with reality without
// re-deriving it, and correctly drop the cache on a "schema" append so
// this function's own comparison below does the (correctly expensive)
// re-resolve.
//
// This is what dag.WithProducerVocabularies's resolver and
// checkBeforeAppend both call, so Append and the multi-append callers that
// guard it always see the same vocabularies (TestCheckBeforeAppendAgreesWithAppend).
func (s *Store) vocabularies(ctx context.Context) (codec.Vocabularies, error) {
	if s == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}

	chains, err := dag.Chains(s.storer)
	if err != nil {
		return nil, fmt.Errorf("writ: resolve vocabularies: chains: %w", err)
	}
	fp := fingerprintChains(chains)

	s.vocabMu.Lock()
	if s.vocabCache != nil && fp == s.vocabFingerprint {
		cached := s.vocabCache
		s.vocabMu.Unlock()
		return cached, nil
	}
	s.vocabMu.Unlock()

	schemas, err := s.Schema(ctx)
	if err != nil {
		return nil, fmt.Errorf("writ: resolve vocabularies: %w", err)
	}
	vocabularies, _ := VocabulariesFromSchemas(schemas)
	logRules, _ := RulesFromSchemas(schemas)
	rules := mergeRules(builtinRules(), logRules)

	s.vocabMu.Lock()
	s.vocabCache = vocabularies
	s.ruleCache = rules
	s.vocabChains = chains
	s.vocabFingerprint = fp
	s.vocabMu.Unlock()

	return vocabularies, nil
}

// rules resolves the fold-rule index a projection ApplySchema/Refresh/Rebuild
// call consumes: the built-in vocabulary overlaid by whatever the log
// declares for the same object_type, log wins per type. It is memoised
// alongside vocabularies (same cache-miss branch, same dag.Chains
// fingerprint, same invalidation via noteAppend), so calling this on every
// Refresh costs one Chains pass and a fingerprint comparison, not a fold.
//
// Installing the log's declaration wherever it exists, rather than treating
// a log-vs-built-in name collision as a conflict that installs neither,
// mirrors WRIT-188's own tier-2-over-tier-3 producer-validation precedence:
// the alternative would blank a repo's projection the moment someone runs
// `writ schema apply`, since nothing declares the built-in types in the log
// yet.
func (s *Store) rules(ctx context.Context) (map[string][]Rule, error) {
	if _, err := s.vocabularies(ctx); err != nil {
		return nil, err
	}
	s.vocabMu.Lock()
	cached := s.ruleCache
	s.vocabMu.Unlock()
	return cached, nil
}

// mergeRules overlays log onto base, replacing base's entire rule list for
// any object_type the log declares — not merging field-by-field — because
// RulesFromSchemas already returns, per object_type, the complete installed
// rule set for that type (every non-contested field across every schema
// object binding it), and a partial merge would let a stale built-in rule
// for a field the log's schema silently omits keep firing.
func mergeRules(base, log map[string][]Rule) map[string][]Rule {
	out := make(map[string][]Rule, len(base)+len(log))
	for t, rs := range base {
		out[t] = rs
	}
	for t, rs := range log {
		out[t] = rs
	}
	return out
}

// builtinRulesOnce computes the still-shipping built-in vocabulary's fold
// rules exactly once per process, delegating to state.BuiltinRules() — the
// one shared construction production and every test package needing "a
// rule index declaring the current built-in types" builds from
// (WRIT-189 round 2 MINOR-4: this used to be a fourth, separately drifting
// copy of what three test packages already built via
// spec/fixtures.BuiltinRules(), so the tests validated an index production
// never actually built). Rule is a type alias for state.Rule (engine/fold.go),
// so state.BuiltinRules()'s map[string][]state.Rule needs no conversion.
var builtinRulesOnce = sync.OnceValue(func() map[string][]Rule {
	out, err := state.BuiltinRules()
	if err != nil {
		panic(fmt.Errorf("writ: loading built-in field rules: %w", err))
	}
	return out
})

func builtinRules() map[string][]Rule {
	return builtinRulesOnce()
}

// noteAppend rolls the cached producer-vocabularies fingerprint forward
// after a local append succeeds (wired as dag.WithChainObserver in Open),
// instead of leaving Store.vocabularies to notice a stale fingerprint on
// the very next call and pay for a full Schema/Enumerate fold to
// re-resolve a chain move that could not have changed the schema.
//
// Appending anything other than "schema" to this writer's own chain can
// never add, remove, or alter a schema object — dag.Store.Append and
// checkBeforeAppend both refuse to resolve vocabularies for object_type
// "schema" in the first place (spec/schema-ops.md §7), and every other
// object type is irrelevant to what a schema object folds to — so the
// resolved vocabularies themselves stay valid; only the moved chain's tip
// in the cached snapshot, and the fingerprint derived from it, need to
// catch up so the next real dag.Chains() comparison agrees and hits.
//
// Appending "schema" itself takes the opposite path: it is exactly the
// case that can change what VocabulariesFromSchemas resolves, so rather
// than recompute anything here — which would put the very log I/O this
// cache exists to get off the append hot path right back onto it — this
// just drops the cached snapshot. The next Store.vocabularies call then
// sees its own freshly computed fingerprint disagree with the (now
// absent) cache and pays for one full resolve, correctly, because this
// append — unlike the many non-"schema" ones surrounding it — really
// might have moved the schema.
func (s *Store) noteAppend(objectType string, newTip plumbing.Hash) {
	s.vocabMu.Lock()
	defer s.vocabMu.Unlock()

	if objectType == "schema" {
		s.vocabChains = nil
		return
	}
	if s.vocabChains == nil {
		// Nothing cached yet to roll forward; the next vocabularies() call
		// populates it from scratch regardless.
		return
	}

	// fingerprintChains reads only Tip, so that is all this needs to set;
	// Ref is left zero rather than reconstructed for a value nothing consults.
	refName := dag.LocalRefName(s.identity.WriterID, objectType).String()
	chain := s.vocabChains[refName]
	chain.Tip = newTip
	s.vocabChains[refName] = chain
	s.vocabFingerprint = fingerprintChains(s.vocabChains)
}

// fingerprintChains derives a cheap fingerprint string over every
// discovered writ chain's ref name and tip hash, sorted for determinism:
// any commit authored locally or fetched from a peer moves at least one
// chain's tip and so changes this string; nothing else does.
func fingerprintChains(chains map[string]dag.DiscoveredChain) string {
	names := make([]string, 0, len(chains))
	for name := range chains {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte('\n')
		b.WriteString(chains[name].Tip.String())
		b.WriteByte('\n')
	}
	return b.String()
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

	if err := s.checkBeforeAppend(ctx, envs...); err != nil {
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

// opTypeGrammar mirrors the op_type rule spec/schemas/op-envelope.schema.json
// pins for the wire field (`^[a-z][a-z0-9-]*$`, max opTypeMaxLength
// characters). Nothing upstream of resolveSchemaTypes enforces this for a
// log-declared op_type — spec.ValidateFieldRule checks only that OpType is
// non-empty — so an op authored under a schema-declared op_type failing
// this grammar could never be written through the ordinary envelope path
// in the first place; catching it here buys a clearer error and a rule
// index that cannot be keyed by an unwritable op type, not a new security
// boundary (spec/schema-ops.md §9).
var opTypeGrammar = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

const opTypeMaxLength = 64

func validOpTypeGrammar(opType string) bool {
	return opType != "" && len(opType) <= opTypeMaxLength && opTypeGrammar.MatchString(opType)
}

// resolvedSchemaTypes is the shared collision/validation pass over folded
// schema objects (spec/schema-ops.md §6, §7, §9), computed once and
// consumed by both RulesFromSchemas (the fold engine's []Rule shape) and
// VocabulariesFromSchemas (the producer's codec.Vocabularies shape) so
// neither call site has to infer "contested" from SchemaConflict's shape,
// or answer "is this type declared at all" from whether rules[t] happens
// to be non-empty.
type resolvedSchemaTypes struct {
	// declared lists every object type at least one schema object binds,
	// via define-type, define-field, or define-op — contested or not, and
	// regardless of whether it ends up with any installed fields or ops.
	// A type declared with no fields (define-type/define-op alone) is
	// still declared: VocabulariesFromSchemas must not treat that the same
	// as "no schema in the log ever named this type"
	// (TestDeclaredTypeWithNoFieldsIsWritable).
	declared map[string]bool
	// contested lists every object type two or more schema objects bind.
	// Neither schema's rules are installed for it (§6): fields[t] and
	// ops[t] are absent, and VocabulariesFromSchemas reports
	// Vocabulary{Contested: true} for it.
	contested map[string]bool
	// fields holds, per non-contested non-"schema" object type, every
	// field declaration that survived grammar, spec.ValidateFieldRule, and
	// spec.CheckTargetCollision — the original state.SchemaField, not the
	// spec.FieldRule built from it for validation, so Deprecated and the
	// rest of its shape are not lost building it back into a Rule.
	fields map[string][]state.SchemaField
	// ops holds, per non-contested non-"schema" object type, every
	// define-op declaration that survived the same grammar check.
	ops map[string][]state.SchemaOp
	// boundBy maps a non-contested object type to the ObjectID of the one
	// schema object that binds it, so a producer rejection
	// (VocabulariesFromSchemas -> codec.Vocabularies) can name which
	// schema is responsible.
	boundBy   map[string]string
	conflicts []SchemaConflict
}

// toFieldRule builds the spec.FieldRule form of a schema-declared field,
// used to run it through spec.ValidateFieldRule and
// spec.CheckTargetCollision (both defined against that type) and, for a
// non-contested type, to populate a codec.Vocabulary's Fields directly:
// spec.FieldRule is the field-rule currency engine/codec already imports
// spec for.
func toFieldRule(objectType string, f state.SchemaField) spec.FieldRule {
	return spec.FieldRule{
		OpType: f.OpType, OpVersion: f.OpVersion, Field: f.Name, Target: f.Target,
		Strategy: f.Strategy, Key: f.Key, Lattice: f.Lattice, ValueType: f.ValueType,
		Enum: f.Enum, MaxLength: f.MaxLength, KeyTypes: f.KeyTypes,
		ObjectType: objectType,
	}
}

// resolveSchemaTypes runs the collision pass (§6) and, for every type that
// survives it, the per-field validation pass (§7 step 4, §9) exactly once,
// in ascending schema ObjectID order so two conforming implementations
// build the same result from the same input regardless of enumeration
// order. RulesFromSchemas and VocabulariesFromSchemas are thin, disjoint
// projections of this shared result: neither reruns the pass, and neither
// can disagree with the other about what is contested or declared.
func resolveSchemaTypes(schemas []state.Schema) resolvedSchemaTypes {
	sorted := append([]state.Schema(nil), schemas...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ObjectID < sorted[j].ObjectID })

	boundBy := make(map[string]string)        // object_type -> owning schema ObjectID
	namespaceOwner := make(map[string]string) // namespace -> owning schema ObjectID
	contested := make(map[string]bool)        // object_type -> withheld from installation
	declared := make(map[string]bool)
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
			declared[t.Name] = true
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

	fields := make(map[string][]state.SchemaField)
	ops := make(map[string][]state.SchemaOp)
	for _, sch := range sorted {
		for _, t := range sch.Types {
			if t.Name == "schema" || contested[t.Name] {
				continue
			}

			targetBindings := make(map[string][]spec.FieldRule)
			var typeFields []state.SchemaField
			for _, f := range t.Fields {
				sr := toFieldRule(t.Name, f)

				if !validOpTypeGrammar(sr.OpType) {
					conflicts = append(conflicts, SchemaConflict{
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     fmt.Sprintf("define-field op_type %q is not a valid op type (must match ^[a-z][a-z0-9-]*$, max %d chars) and was not installed", sr.OpType, opTypeMaxLength),
					})
					continue
				}

				if err := spec.ValidateFieldRule(sr); err != nil {
					conflicts = append(conflicts, SchemaConflict{
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     fmt.Sprintf("field rule (%s, %d, %s) is invalid and was not installed: %v", sr.OpType, sr.OpVersion, sr.Field, err),
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

				typeFields = append(typeFields, f)
			}

			var typeOps []state.SchemaOp
			for _, o := range t.Ops {
				if !validOpTypeGrammar(o.OpType) {
					conflicts = append(conflicts, SchemaConflict{
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     fmt.Sprintf("define-op op_type %q is not a valid op type (must match ^[a-z][a-z0-9-]*$, max %d chars) and was not installed", o.OpType, opTypeMaxLength),
					})
					continue
				}
				typeOps = append(typeOps, o)
			}

			if len(typeFields) > 0 {
				fields[t.Name] = typeFields
			}
			if len(typeOps) > 0 {
				ops[t.Name] = typeOps
			}
		}
	}

	return resolvedSchemaTypes{
		declared:  declared,
		contested: contested,
		fields:    fields,
		ops:       ops,
		boundBy:   boundBy,
		conflicts: conflicts,
	}
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
// enumeration order. RulesFromSchemas's signature is unchanged by
// WRIT-188 — it is now a thin projection of the shared resolveSchemaTypes
// pass rather than running the pass itself — but its behavior is not: the
// grammar gate resolveSchemaTypes now applies to every define-field and
// define-op op_type (spec/schema-ops.md §11) drops a rule RulesFromSchemas
// used to install and reports a SchemaConflict it used to stay silent on,
// for any op_type that fails ^[a-z][a-z0-9-]*$ or exceeds opTypeMaxLength.
func RulesFromSchemas(schemas []state.Schema) (map[string][]Rule, []SchemaConflict) {
	res := resolveSchemaTypes(schemas)

	rules := make(map[string][]Rule)
	for typeName, typeFields := range res.fields {
		var typeRules []Rule
		for _, f := range typeFields {
			// deprecated:true is metadata discouraging new writes, not a
			// removal (spec/schema-ops.md §5, §8): the rule stays
			// installed and active for folding so ops already signed
			// under it keep folding to the same state, with Deprecated
			// carried onto the resolved Rule for a producer or UI to
			// read.
			typeRules = append(typeRules, Rule{
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
				ObjectType: typeName,
			})
		}
		if len(typeRules) > 0 {
			rules[typeName] = typeRules
		}
	}

	return rules, res.conflicts
}

// VocabulariesFromSchemas resolves every folded schema object present in a
// repo into the codec.Vocabularies shape the generic producer validator
// (engine/codec's BuildCommit/ValidateBody) checks tier 2 of
// spec/op-envelope.md's five-tier precedence against — the log-sourced
// counterpart to RulesFromSchemas's fold-rule shape, built from the exact
// same collision/validation pass (resolveSchemaTypes) so the two can never
// disagree about what is contested or declared.
//
// "schema" is never a key in the returned map: spec/schema-ops.md §7's
// bootstrap exception means it always validates against the engine's
// built-in table, never the log, and engine/codec enforces that directly
// (validateProducerOp's unconditional top-level check) rather than
// consulting this map for it — resolveSchemaTypes already refuses to treat
// a log schema's attempt to redefine "schema" as anything but a conflict,
// so it never reaches res.declared with fields or ops installed either.
func VocabulariesFromSchemas(schemas []state.Schema) (codec.Vocabularies, []SchemaConflict) {
	res := resolveSchemaTypes(schemas)

	vocabularies := make(codec.Vocabularies, len(res.declared))
	for typeName := range res.declared {
		if typeName == "schema" {
			continue
		}
		if res.contested[typeName] {
			vocabularies[typeName] = codec.Vocabulary{Contested: true}
			continue
		}

		v := codec.Vocabulary{
			Declared:       true,
			SchemaObjectID: res.boundBy[typeName],
			OpTypes:        make(map[codec.OpVersionKey]bool),
			Fields:         make(map[codec.OpVersionKey][]spec.FieldRule),
		}
		for _, f := range res.fields[typeName] {
			key := codec.OpVersionKey{OpType: f.OpType, OpVersion: f.OpVersion}
			v.OpTypes[key] = true
			v.Fields[key] = append(v.Fields[key], toFieldRule(typeName, f))
		}
		for _, o := range res.ops[typeName] {
			v.OpTypes[codec.OpVersionKey{OpType: o.OpType, OpVersion: o.OpVersion}] = true
		}
		vocabularies[typeName] = v
	}

	return vocabularies, res.conflicts
}
