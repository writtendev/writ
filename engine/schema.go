package writ

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/dag"
	"github.com/writtendev/writ/internal/state"
)

// SchemaField represents one field declaration within a schema-declared
// type's op vocabulary (v1), keyed by (op_type, op_version, field) — the
// same tuple a published field-rules.json entry is keyed by.
type SchemaField struct {
	Name       string            `json:"field"`
	OpType     string            `json:"op_type"`
	OpVersion  int64             `json:"op_version"`
	ValueType  string            `json:"value_type,omitempty"`
	Enum       []string          `json:"enum,omitempty"`
	MaxLength  int64             `json:"max_length,omitempty"`
	Strategy   string            `json:"strategy,omitempty"`
	Key        []string          `json:"key,omitempty"`
	KeyTypes   map[string]string `json:"key_types,omitempty"`
	Lattice    []string          `json:"lattice,omitempty"`
	Target     string            `json:"target,omitempty"`
	Deprecated bool              `json:"deprecated,omitempty"`
}

// SchemaOp represents one op type declared within a schema-declared type's
// vocabulary (v1), keyed by (op_type, op_version).
type SchemaOp struct {
	OpType      string `json:"op_type"`
	OpVersion   int64  `json:"op_version"`
	Description string `json:"description,omitempty"`
}

// SchemaType represents one object type declared by a schema object (v1).
// Name is the bare wire object_type: namespace and type name are declared
// on the schema object but do not reach the wire (spec/schema-ops.md
// §Namespace and object_type binding).
type SchemaType struct {
	Name        string        `json:"type"`
	Description string        `json:"description,omitempty"`
	Deprecated  bool          `json:"deprecated,omitempty"`
	Fields      []SchemaField `json:"fields,omitempty"`
	Ops         []SchemaOp    `json:"ops,omitempty"`
}

// Schema represents the materialized state of a `schema` collaborative
// object (v1), produced by FoldSchema.
type Schema struct {
	ObjectID    string       `json:"object_id"`
	Namespace   string       `json:"namespace,omitempty"`
	Description string       `json:"description,omitempty"`
	Types       []SchemaType `json:"types,omitempty"`
	UnknownOps  []UnknownOp  `json:"unknown_ops,omitempty"`
}

// SchemaConflictKind is a closed catalogue of the reasons schema resolution
// can produce a SchemaConflict (spec/schema-ops.md §6). The set is closed
// for this spec version, and a caller branches on Kind, never on Reason's
// wording (WRIT-335) -- Reason is the only field of a SchemaConflict whose
// wording is not pinned; Kind, together with which of ObjectType and
// Namespace are set and what ObjectIDs holds (spec/schema-ops.md §6's
// per-kind table), is spec-defined and conformance-relevant.
type SchemaConflictKind string

const (
	// SchemaConflictNamespaceUngrammatical: a schema object's namespace
	// fails the namespace grammar; none of its types were installed.
	SchemaConflictNamespaceUngrammatical SchemaConflictKind = "namespace-ungrammatical"

	// SchemaConflictObjectIDMismatch: a schema object's own ObjectID does
	// not match the derived form for its namespace; the whole object is
	// dropped.
	SchemaConflictObjectIDMismatch SchemaConflictKind = "object-id-mismatch"

	// SchemaConflictSchemaRedefined: a define-type attempts to redefine the
	// engine's built-in bootstrap type "schema".
	SchemaConflictSchemaRedefined SchemaConflictKind = "schema-redefined"

	// SchemaConflictTypeUngrammatical: a define-type's declared name fails
	// the object_type grammar.
	SchemaConflictTypeUngrammatical SchemaConflictKind = "type-ungrammatical"

	// SchemaConflictTypeUnqualified: a define-type's declared name is not
	// qualified with its own schema object's namespace.
	SchemaConflictTypeUnqualified SchemaConflictKind = "type-unqualified"

	// SchemaConflictOpTypeUngrammatical: a define-field's or a define-op's
	// declared op_type fails the op_type grammar.
	SchemaConflictOpTypeUngrammatical SchemaConflictKind = "op-type-ungrammatical"

	// SchemaConflictOpTypeReserved: a define-field's or a define-op's
	// declared op_type names a format-reserved op type (such as "merge").
	SchemaConflictOpTypeReserved SchemaConflictKind = "op-type-reserved"

	// SchemaConflictRuleInvalid: spec.ValidateFieldRule rejects a field
	// rule; it is dropped and not installed.
	SchemaConflictRuleInvalid SchemaConflictKind = "rule-invalid"

	// SchemaConflictKeyColumnDisagreement: spec.CheckKeyColumnAgreement
	// finds two or more rules disagreeing on a shared key column (including
	// the dual-role tombstone/key-column case); every participating rule is
	// withheld together.
	SchemaConflictKeyColumnDisagreement SchemaConflictKind = "key-column-disagreement"

	// SchemaConflictTargetDisagreement: spec.CheckTargetAgreement finds two
	// or more rules disagreeing on a shared target; every participating
	// rule is withheld together.
	SchemaConflictTargetDisagreement SchemaConflictKind = "target-disagreement"

	// SchemaConflictValueTypeUnknown: a rule's value_type or a key_types
	// entry names something outside this reader's value-type catalogue
	// (spec/value-types.md). Unlike every other kind, the rule is not
	// withheld: it is installed untyped (WRIT-334), and this is a warning,
	// not a drop.
	SchemaConflictValueTypeUnknown SchemaConflictKind = "value-type-unknown"
)

// SchemaConflict records a conflict found while resolving schema objects into rules (spec/schema-ops.md §6).
type SchemaConflict struct {
	// Kind is the closed conflict code (see SchemaConflictKind). Always
	// set: every construction site names one.
	Kind SchemaConflictKind `json:"kind"`
	// ObjectType is set on every Kind except SchemaConflictNamespaceUngrammatical
	// and SchemaConflictObjectIDMismatch, which drop a whole schema object
	// before any of its declared types is looked at (spec/schema-ops.md
	// §6's per-kind table).
	ObjectType string `json:"object_type,omitempty"`
	// Namespace is assigned only on the five declaration-level kinds --
	// SchemaConflictNamespaceUngrammatical, SchemaConflictObjectIDMismatch,
	// SchemaConflictSchemaRedefined, SchemaConflictTypeUngrammatical, and
	// SchemaConflictTypeUnqualified -- never on a per-rule or per-target
	// kind (spec/schema-ops.md §6's per-kind table).
	Namespace string `json:"namespace,omitempty"`
	// ObjectIDs names the schema objects involved: two for a collision
	// between schema objects, one for a single object's own invalid rule or
	// its attempt to redefine `schema` itself.
	ObjectIDs []string `json:"object_ids"`
	// Reason is human-readable and informative only; its wording is not a
	// contract and may change in any release. Branch on Kind, never Reason.
	Reason string `json:"reason"`
}

// Schema folds every `schema` object present in the log and returns their
// materialized state, one entry per schema object, ordered by ObjectID.
//
// Schema reads from the DAG directly, not the projection cache: the
// projection is a droppable cache (ARCHITECTURE.md §The six machines #5),
// and nothing about resolving field rules from the log may depend on it
// having been built or refreshed.
func (s *Store) Schema(ctx context.Context) ([]Schema, error) {
	if s == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	ts, _ := s.currentTrustStore()
	enumRes, err := s.dagStore.Enumerate(
		dag.VerifyOnly(func(op codec.Op) bool { return op.ObjectType == "schema" }),
		dag.WithLiveTrustStore(ts),
	)
	if err != nil {
		return nil, fmt.Errorf("writ: enumerate schema objects: %w", err)
	}

	var schemas []Schema
	for objectID, ops := range enumRes.Ops {
		if !anyOpHasObjectType(ops, "schema") {
			continue
		}
		sch, err := state.FoldSchema(ops)
		if err != nil {
			return nil, fmt.Errorf("writ: fold schema object %s: %w", objectID, err)
		}
		schemas = append(schemas, fromStateSchema(sch))
	}

	sort.Slice(schemas, func(i, j int) bool { return schemas[i].ObjectID < schemas[j].ObjectID })
	return schemas, nil
}

// vocabFreshnessWindow is how long Store.vocabulariesForAppend trusts a
// cached producer-vocabularies snapshot without re-deriving it from
// dag.Chains (WRIT-202).
const vocabFreshnessWindow = 100 * time.Millisecond

type vocabSnapshot struct {
	vocab codec.Vocabularies
	rules map[string][]state.Rule
	types state.ResolvedSchemaTypes
}

func (s *Store) vocabularies(ctx context.Context) (vocabSnapshot, error) {
	if s == nil {
		return vocabSnapshot{}, fmt.Errorf("writ: store is nil")
	}

	s.vocabMu.Lock()
	gen := s.vocabGen
	s.vocabMu.Unlock()

	chains, err := dag.Chains(s.storer)
	if err != nil {
		return vocabSnapshot{}, fmt.Errorf("writ: resolve vocabularies: chains: %w", err)
	}
	fp := fingerprintChains(chains)

	s.vocabMu.Lock()
	if s.vocabCache != nil && fp == s.vocabFingerprint {
		cached := vocabSnapshot{vocab: s.vocabCache, rules: s.ruleCache, types: s.typesCache}
		s.vocabObservedAt = s.clock()
		s.vocabMu.Unlock()
		return cached, nil
	}
	s.vocabMu.Unlock()

	schemas, err := s.Schema(ctx)
	if err != nil {
		return vocabSnapshot{}, fmt.Errorf("writ: resolve vocabularies: %w", err)
	}
	stateSchemas := make([]state.Schema, len(schemas))
	for i, sc := range schemas {
		stateSchemas[i] = toStateSchema(sc)
	}
	vocabularies, _ := state.VocabulariesFromSchemas(stateSchemas)
	rules, _ := state.RulesFromSchemas(stateSchemas)
	res := state.ResolveSchemaTypes(stateSchemas)
	snap := vocabSnapshot{vocab: vocabularies, rules: rules, types: res}

	now := s.clock()

	s.vocabMu.Lock()
	if s.vocabGen == gen {
		s.vocabCache = vocabularies
		s.ruleCache = rules
		s.typesCache = res
		s.vocabChains = chains
		s.vocabFingerprint = fp
		s.vocabObservedAt = now
	}
	s.vocabMu.Unlock()

	return snap, nil
}

func (s *Store) vocabulariesForAppend(ctx context.Context) (codec.Vocabularies, error) {
	if s == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}

	s.vocabMu.Lock()
	if s.vocabCache != nil && !s.vocabObservedAt.IsZero() && s.clock().Sub(s.vocabObservedAt) < vocabFreshnessWindow {
		v := s.vocabCache
		s.vocabMu.Unlock()
		return v, nil
	}
	s.vocabMu.Unlock()

	snap, err := s.vocabularies(ctx)
	if err != nil {
		return nil, err
	}
	return snap.vocab, nil
}

func (s *Store) invalidateVocabularies() {
	s.vocabMu.Lock()
	defer s.vocabMu.Unlock()
	s.vocabCache = nil
	s.ruleCache = nil
	s.typesCache = state.ResolvedSchemaTypes{}
	s.vocabChains = nil
	s.vocabFingerprint = ""
	s.vocabObservedAt = time.Time{}
	s.vocabGen++
}

func (s *Store) rules(ctx context.Context) (map[string][]state.Rule, error) {
	snap, err := s.vocabularies(ctx)
	if err != nil {
		return nil, err
	}
	return snap.rules, nil
}

func (s *Store) declaredTypes(ctx context.Context) (state.ResolvedSchemaTypes, error) {
	snap, err := s.vocabularies(ctx)
	if err != nil {
		return state.ResolvedSchemaTypes{}, err
	}
	return snap.types, nil
}

func (s *Store) noteAppend(objectType string, newTip plumbing.Hash) {
	s.vocabMu.Lock()
	defer s.vocabMu.Unlock()

	if objectType == "schema" {
		s.vocabChains = nil
		s.vocabFingerprint = ""
		s.vocabObservedAt = time.Time{}
		s.vocabGen++
		return
	}
	if s.vocabChains == nil {
		return
	}

	refName := dag.LocalRefName(s.identity.WriterID, objectType).String()
	chain := s.vocabChains[refName]
	chain.Tip = newTip
	s.vocabChains[refName] = chain
	s.vocabFingerprint = fingerprintChains(s.vocabChains)
}

func fingerprintChains(chains map[string]dag.DiscoveredChain) string {
	names := make([]string, 0, len(chains))
	for name := range chains {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("chains:")
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte('\n')
		b.WriteString(chains[name].Tip.String())
		b.WriteByte('\n')
	}
	return b.String()
}

func (s *Store) checkBeforeAppend(ctx context.Context, envs ...codec.Envelope) error {
	for _, env := range envs {
		if err := codec.ValidateBody(env, nil); err != nil {
			return err
		}
	}
	return nil
}

// Types returns the vocabulary actually in effect right now: every object
// type the log declares, its fields, and its ops.
func (s *Store) Types(ctx context.Context) ([]SchemaType, error) {
	if s == nil {
		return nil, fmt.Errorf("writ: store is nil")
	}
	if err := s.checkClosed(); err != nil {
		return nil, err
	}

	res, err := s.declaredTypes(ctx)
	if err != nil {
		return nil, fmt.Errorf("writ: resolve types: %w", err)
	}

	sortedNames := make([]string, 0, len(res.Declared))
	for name := range res.Declared {
		if name == "schema" {
			continue
		}
		sortedNames = append(sortedNames, name)
	}
	sort.Strings(sortedNames)

	types := make([]SchemaType, 0, len(sortedNames))
	for _, name := range sortedNames {
		types = append(types, schemaTypeFromResolved(name, res))
	}
	return types, nil
}

type schemaOpVersionKey struct {
	OpType    string
	OpVersion int64
}

func schemaTypeFromResolved(name string, res state.ResolvedSchemaTypes) SchemaType {
	var fields []SchemaField
	for _, f := range res.Fields[name] {
		fields = append(fields, SchemaField{
			Name:       f.Name,
			OpType:     f.OpType,
			OpVersion:  f.OpVersion,
			ValueType:  f.ValueType,
			Enum:       f.Enum,
			MaxLength:  f.MaxLength,
			Strategy:   f.Strategy,
			Key:        f.Key,
			KeyTypes:   f.KeyTypes,
			Lattice:    f.Lattice,
			Target:     f.Target,
			Deprecated: f.Deprecated,
		})
	}
	sort.Slice(fields, func(i, j int) bool {
		if fields[i].OpType != fields[j].OpType {
			return fields[i].OpType < fields[j].OpType
		}
		if fields[i].OpVersion != fields[j].OpVersion {
			return fields[i].OpVersion < fields[j].OpVersion
		}
		return fields[i].Name < fields[j].Name
	})

	seen := make(map[schemaOpVersionKey]bool)
	var ops []SchemaOp
	for _, o := range res.Ops[name] {
		key := schemaOpVersionKey{o.OpType, o.OpVersion}
		if !seen[key] {
			seen[key] = true
			ops = append(ops, SchemaOp{
				OpType:      o.OpType,
				OpVersion:   o.OpVersion,
				Description: o.Description,
			})
		}
	}
	for _, f := range fields {
		key := schemaOpVersionKey{f.OpType, f.OpVersion}
		if !seen[key] {
			seen[key] = true
			ops = append(ops, SchemaOp{OpType: f.OpType, OpVersion: f.OpVersion})
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].OpType != ops[j].OpType {
			return ops[i].OpType < ops[j].OpType
		}
		return ops[i].OpVersion < ops[j].OpVersion
	})

	return SchemaType{
		Name:        name,
		Description: res.Descriptions[name],
		Deprecated:  res.DeprecatedTypes[name],
		Fields:      fields,
		Ops:         ops,
	}
}

// ApplySchema appends a compiled `schema` op sequence through the ordinary signed producer path.
func (s *Store) ApplySchema(ctx context.Context, envs []Envelope) error {
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

	codecEnvs := toCodecEnvelopes(envs)
	if err := s.checkBeforeAppend(ctx, codecEnvs...); err != nil {
		return fmt.Errorf("writ: apply schema: %w", wrapRejectError(err))
	}

	enumRes, err := s.dagStore.Enumerate(dag.VerifyOnly(func(codec.Op) bool { return false }))
	if err != nil {
		return fmt.Errorf("writ: apply schema: enumerate: %w", err)
	}
	frontier := schemaFrontier(enumRes.Ops[objectID])

	for i, env := range codecEnvs {
		var parents []string
		if i == 0 {
			parents = frontier
		}
		if _, err := s.dagStore.Append(ctx, env, parents); err != nil {
			return fmt.Errorf("writ: apply schema: append %s: %w", env.OpType, wrapRejectError(err))
		}
	}

	_ = s.maybeAutoRefresh(ctx)
	return nil
}

func schemaFrontier(ops []codec.Op) []string {
	if len(ops) == 0 {
		return nil
	}

	isParent := make(map[string]bool)
	for _, op := range ops {
		for _, p := range op.Parents {
			isParent[p] = true
		}
	}

	var frontier []string
	for _, op := range ops {
		if !isParent[op.ID] {
			frontier = append(frontier, op.ID)
		}
	}
	sort.Strings(frontier)
	return frontier
}

// SchemaFromEnvelopes folds a compiled op sequence in memory, returning the materialized Schema state.
func SchemaFromEnvelopes(envs []Envelope) (Schema, error) {
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
			Envelope: codec.Envelope{
				ObjectID:   env.ObjectID,
				ObjectType: env.ObjectType,
				OpType:     env.OpType,
				OpVersion:  env.OpVersion,
				Body:       env.Body,
			},
			ID:      id,
			Parents: parents,
			Author:  codec.Identity{When: base.Add(time.Duration(i) * time.Second)},
		}
		parent = id
	}

	sch, err := state.FoldSchema(ops)
	if err != nil {
		return Schema{}, err
	}
	return fromStateSchema(sch), nil
}

// SchemaAfterApply folds objectID's ops already in the log together with delta.
func (s *Store) SchemaAfterApply(ctx context.Context, objectID string, delta []Envelope) (Schema, error) {
	if s == nil {
		return Schema{}, fmt.Errorf("writ: store is nil")
	}

	ts, _ := s.currentTrustStore()
	enumRes, err := s.dagStore.Enumerate(
		dag.VerifyOnly(func(op codec.Op) bool { return op.ObjectID == objectID }),
		dag.WithLiveTrustStore(ts),
	)
	if err != nil {
		return Schema{}, fmt.Errorf("writ: schema after apply: enumerate: %w", err)
	}
	currentOps := enumRes.Ops[objectID]

	if len(delta) == 0 {
		if len(currentOps) == 0 {
			return Schema{}, nil
		}
		sch, err := state.FoldSchema(currentOps)
		if err != nil {
			return Schema{}, err
		}
		return fromStateSchema(sch), nil
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
			Envelope: codec.Envelope{
				ObjectID:   env.ObjectID,
				ObjectType: env.ObjectType,
				OpType:     env.OpType,
				OpVersion:  env.OpVersion,
				Body:       env.Body,
			},
			ID:      id,
			Parents: parents,
			Author:  codec.Identity{When: base.Add(time.Duration(i) * time.Second)},
		}
		parent = id
	}

	allOps := make([]codec.Op, 0, len(currentOps)+len(deltaOps))
	allOps = append(allOps, currentOps...)
	allOps = append(allOps, deltaOps...)

	sch, err := state.FoldSchema(allOps)
	if err != nil {
		return Schema{}, err
	}
	return fromStateSchema(sch), nil
}

func anyOpHasObjectType(ops []codec.Op, objectType string) bool {
	for _, op := range ops {
		if op.ObjectType == objectType {
			return true
		}
	}
	return false
}

// SchemaConflicts reports conflicts among the given schemas (spec/schema-ops.md §6).
func SchemaConflicts(schemas []Schema) []SchemaConflict {
	stateSchemas := make([]state.Schema, len(schemas))
	for i, s := range schemas {
		stateSchemas[i] = toStateSchema(s)
	}
	_, conflicts := state.RulesFromSchemas(stateSchemas)
	return fromStateConflicts(conflicts)
}

// SchemaInstallable reports whether sch survives the two whole-object drop
// gates: the namespace-grammar gate and the derived-id gate.
func SchemaInstallable(s Schema) bool {
	return state.SchemaInstallable(toStateSchema(s))
}

// DeriveSchemaObjectID returns the schema object id for namespace:
// "schema:" + namespace, per spec/identifiers.md's schema carve-out.
func DeriveSchemaObjectID(namespace string) string {
	return state.DeriveSchemaObjectID(namespace)
}

func toStateSchema(s Schema) state.Schema {
	var types []state.SchemaType
	if s.Types != nil {
		types = make([]state.SchemaType, len(s.Types))
		for i, t := range s.Types {
			var fields []state.SchemaField
			if t.Fields != nil {
				fields = make([]state.SchemaField, len(t.Fields))
				for j, f := range t.Fields {
					fields[j] = state.SchemaField{
						Name:       f.Name,
						OpType:     f.OpType,
						OpVersion:  f.OpVersion,
						ValueType:  f.ValueType,
						Enum:       f.Enum,
						MaxLength:  f.MaxLength,
						Strategy:   f.Strategy,
						Key:        f.Key,
						KeyTypes:   f.KeyTypes,
						Lattice:    f.Lattice,
						Target:     f.Target,
						Deprecated: f.Deprecated,
					}
				}
			}
			var ops []state.SchemaOp
			if t.Ops != nil {
				ops = make([]state.SchemaOp, len(t.Ops))
				for j, o := range t.Ops {
					ops[j] = state.SchemaOp{
						OpType:      o.OpType,
						OpVersion:   o.OpVersion,
						Description: o.Description,
					}
				}
			}
			types[i] = state.SchemaType{
				Name:        t.Name,
				Description: t.Description,
				Deprecated:  t.Deprecated,
				Fields:      fields,
				Ops:         ops,
			}
		}
	}
	var uops []state.UnknownOp
	if s.UnknownOps != nil {
		uops = make([]state.UnknownOp, len(s.UnknownOps))
		for i, u := range s.UnknownOps {
			uops[i] = state.UnknownOp{
				Commit:       u.Commit,
				ObjectType:   u.ObjectType,
				OpType:       u.OpType,
				OpVersion:    u.OpVersion,
				Verification: u.Verification,
			}
		}
	}
	return state.Schema{
		ObjectID:    s.ObjectID,
		Namespace:   s.Namespace,
		Description: s.Description,
		Types:       types,
		UnknownOps:  uops,
	}
}

func fromStateSchema(s state.Schema) Schema {
	var types []SchemaType
	if s.Types != nil {
		types = make([]SchemaType, len(s.Types))
		for i, t := range s.Types {
			var fields []SchemaField
			if t.Fields != nil {
				fields = make([]SchemaField, len(t.Fields))
				for j, f := range t.Fields {
					fields[j] = SchemaField{
						Name:       f.Name,
						OpType:     f.OpType,
						OpVersion:  f.OpVersion,
						ValueType:  f.ValueType,
						Enum:       f.Enum,
						MaxLength:  f.MaxLength,
						Strategy:   f.Strategy,
						Key:        f.Key,
						KeyTypes:   f.KeyTypes,
						Lattice:    f.Lattice,
						Target:     f.Target,
						Deprecated: f.Deprecated,
					}
				}
			}
			var ops []SchemaOp
			if t.Ops != nil {
				ops = make([]SchemaOp, len(t.Ops))
				for j, o := range t.Ops {
					ops[j] = SchemaOp{
						OpType:      o.OpType,
						OpVersion:   o.OpVersion,
						Description: o.Description,
					}
				}
			}
			types[i] = SchemaType{
				Name:        t.Name,
				Description: t.Description,
				Deprecated:  t.Deprecated,
				Fields:      fields,
				Ops:         ops,
			}
		}
	}
	var uops []UnknownOp
	if s.UnknownOps != nil {
		uops = make([]UnknownOp, len(s.UnknownOps))
		for i, u := range s.UnknownOps {
			uops[i] = UnknownOp{
				Commit:       u.Commit,
				ObjectType:   u.ObjectType,
				OpType:       u.OpType,
				OpVersion:    u.OpVersion,
				Verification: u.Verification,
			}
		}
	}
	return Schema{
		ObjectID:    s.ObjectID,
		Namespace:   s.Namespace,
		Description: s.Description,
		Types:       types,
		UnknownOps:  uops,
	}
}

func fromStateConflicts(conflicts []state.SchemaConflict) []SchemaConflict {
	if conflicts == nil {
		return nil
	}
	out := make([]SchemaConflict, len(conflicts))
	for i, c := range conflicts {
		out[i] = SchemaConflict{
			Kind:       SchemaConflictKind(c.Kind),
			ObjectType: c.ObjectType,
			Namespace:  c.Namespace,
			ObjectIDs:  c.ObjectIDs,
			Reason:     c.Reason,
		}
	}
	return out
}

func toCodecEnvelopes(envs []Envelope) []codec.Envelope {
	if envs == nil {
		return nil
	}
	out := make([]codec.Envelope, len(envs))
	for i, env := range envs {
		out[i] = codec.Envelope{
			ObjectID:   env.ObjectID,
			ObjectType: env.ObjectType,
			OpType:     env.OpType,
			OpVersion:  env.OpVersion,
			Body:       env.Body,
		}
	}
	return out
}
