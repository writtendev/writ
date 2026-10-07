package state

import (
	"regexp"
	"sort"
	"strings"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/spec"
)

// SchemaConflictKind is a closed catalogue of the reasons resolveSchemaTypes
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

// SchemaConflict records a conflict resolveSchemaTypes found while resolving
// schema objects into rules (spec/schema-ops.md §6): a load-bearing
// collision between schema objects, an invalid declaration, or — the one
// exception — a warning that a rule was installed untyped. Kind is the
// closed, conformance-relevant code; Reason is human-readable, informative,
// and free to change wording in any release, including this one (WRIT-335).
// For every Kind except SchemaConflictValueTypeUnknown, RulesFromSchemas
// installs no rule at all for what the conflict names: no winner is ever
// picked, and any op naming a withheld object_type/rule falls through the
// absent-schema path to UnknownOp (FC-1, FC-12) exactly as if no schema had
// defined it.
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
	// kind (spec/schema-ops.md §6's per-kind table). Assigned is not the
	// same as present on the wire: the schema object's own folded
	// namespace can itself be empty (a create that never set one, or was
	// quarantined for disagreeing with a derived object id --
	// spec/schema-ops.md §3.4), and omitempty drops that the same as any
	// other zero value. SchemaConflictNamespaceUngrammatical is the one
	// exception -- its own gate never fires with an empty namespace -- so
	// it alone is guaranteed non-empty here.
	Namespace string `json:"namespace,omitempty"`
	// ObjectIDs names the schema objects involved: two for a collision
	// between schema objects, one for a single object's own invalid rule or
	// its attempt to redefine `schema` itself.
	ObjectIDs []string `json:"object_ids"`
	// Reason is human-readable and informative only; its wording is not a
	// contract and may change in any release. Branch on Kind, never Reason.
	Reason string `json:"reason"`
}

// opTypeGrammar mirrors the op_type rule spec/schemas/op-envelope.schema.json
// pins for the wire field (`^[a-z][a-z0-9-]*$`, max OpTypeMaxLength
// characters). Nothing upstream of resolveSchemaTypes enforces this for a
// log-declared op_type — spec.ValidateFieldRule checks OpType is non-empty
// but not its grammar, because op_type's grammar belongs to the envelope
// (spec/op-envelope.md), not to a field rule — so an op authored under a
// schema-declared op_type failing this grammar could never be written
// through the ordinary envelope path in the first place; catching it here
// buys a clearer error and a rule index that cannot be keyed by an
// unwritable op type, not a new security boundary (spec/schema-ops.md §9).
// field, target, and key, by contrast, ARE field-rule properties, so their
// grammar is gated inside spec.ValidateFieldRule itself rather than by a
// twin of this function — see that function's identifierGrammar doc.
var opTypeGrammar = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// OpTypeMaxLength is the maximum length of an op_type.
const OpTypeMaxLength = 64

// ValidOpTypeGrammar reports whether opType satisfies the op_type grammar.
func ValidOpTypeGrammar(opType string) bool {
	return opType != "" && len(opType) <= OpTypeMaxLength && opTypeGrammar.MatchString(opType)
}

// objectTypeGrammar mirrors engine/dag/refs.go's objectTypeRegexp
// (spec/op-envelope.md's object_type field): a bare segment, or two such
// segments joined by exactly one dot, each capped at 64 characters.
// Nothing upstream of resolveSchemaTypes enforces this for a schema
// op's declared type name — codec.DecodeCommit's ValidateEnvelope bounds
// only the *op's own* object_type ("schema"), never the "type" string
// carried inside a define-type op's body (WRIT-253) — so a hand-crafted
// define-type is otherwise free to declare any bytes at all. Duplicated
// locally rather than exported from engine/dag, the same local-duplicate
// style opTypeGrammar above already uses and for the same reason: a
// schema-op grammar that happens to resemble a lower-layer one is not a
// caller of it.
var objectTypeGrammar = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}(\.[a-z][a-z0-9-]{0,63})?$`)

// ObjectTypeGrammarMaxLength mirrors engine/dag/refs.go's
// objectTypeMaxLength: two 64-character segments plus the separating dot.
const ObjectTypeGrammarMaxLength = 129

// ValidObjectTypeGrammar reports whether typeName satisfies every
// requirement spec/schema-ops.md §4.2 places on a define-type op's `type`:
// the object_type grammar and length bound (spec/op-envelope.md), and the
// ".lock" exclusion — git rejects a ref path component ending in ".lock",
// so a type name ending that way is grammar-legal but ref-unwritable
// (engine/dag/refs.go's objectTypeEndsInDotLock).
func ValidObjectTypeGrammar(typeName string) bool {
	return typeName != "" &&
		len(typeName) <= ObjectTypeGrammarMaxLength &&
		objectTypeGrammar.MatchString(typeName) &&
		!strings.HasSuffix(typeName, ".lock")
}

// DeclarationInstallable reports whether a declared type name could ever
// be installed for a schema object carrying the given namespace: the
// namespace itself must be grammar-valid (ValidNamespaceGrammar),
// the type name must satisfy the object_type grammar, and the
// type name must be qualified with that namespace (WRIT-217).
// resolveSchemaTypes' two passes share this exact predicate — the first
// to decide what may be declared or bound at all (with its
// own per-reason SchemaConflict), the second to decide what may
// contribute fields/ops/descriptions — so a type gated out by either
// grammar check can never reach buildTypeDescriptor by either path, the
// same way an unqualified declaration already could not (WRIT-253).
func DeclarationInstallable(typeName, namespace string) bool {
	return ValidNamespaceGrammar(namespace) && ValidObjectTypeGrammar(typeName) && TypeIsQualifiedForNamespace(typeName, namespace)
}

// TypeIsQualifiedForNamespace reports whether a declared type name is
// exactly "<namespace>.<segment>" for a non-empty, single-segment
// remainder (WRIT-217): the resolver-level gate that closes the global
// object_type namespace the envelope grammar alone cannot, since
// spec/op-envelope.md's object_type pattern only admits an optional dot
// and has no notion of which schema object's namespace, if any, a given
// declaration is entitled to use. An empty namespace (a schema whose own
// "create" op never set one) qualifies nothing — there is no prefix to
// require agreement with, so every type name it declares is refused here,
// not silently admitted as bare. A remainder carrying its own dot (a
// multi-dot type name, e.g. "acme.foo.bar" under namespace "acme") is
// refused too: it is not the single segment §6.4 requires, and admitting
// it would install a type whose ops engine/dag's objectTypeRegexp can
// never write and that schemasrc.Render cannot round-trip.
//
// This function only ever answers the qualification question: it does not
// itself check that typeName or namespace are grammar-valid strings at
// all (a caller can, and ValidObjectTypeGrammar/ValidNamespaceGrammar
// do, find "a') OR 1 --" every bit as prefix-matchable as "gadget").
// Grammar is ValidObjectTypeGrammar's and ValidNamespaceGrammar's
// job (WRIT-253); DeclarationInstallable is what combines all three, and
// is what every caller inside resolveSchemaTypes actually gates on.
func TypeIsQualifiedForNamespace(typeName, namespace string) bool {
	if namespace == "" {
		return false
	}
	prefix := namespace + "."
	if !strings.HasPrefix(typeName, prefix) || len(typeName) <= len(prefix) {
		return false
	}
	return !strings.Contains(typeName[len(prefix):], ".")
}

// ResolvedSchemaTypes is the shared collision/validation pass over folded
// schema objects (spec/schema-ops.md §6, §7, §9), computed once and
// consumed by both RulesFromSchemas (the fold engine's []Rule shape) and
// VocabulariesFromSchemas (the producer's codec.Vocabularies shape) so
// neither call site has to answer "is this type declared at all" from
// whether rules[t] happens to be non-empty.
type ResolvedSchemaTypes struct {
	// Declared lists every object type at least one schema object binds,
	// via define-type, define-field, or define-op, regardless of whether
	// it ends up with any installed fields or ops.
	Declared map[string]bool
	// Fields holds, per non-"schema" object type, every field declaration
	// that survived grammar, spec.ValidateFieldRule, and
	// spec.CheckTargetAgreement — the original SchemaField, not the
	// spec.FieldRule built from it for validation, so Deprecated and the
	// rest of its shape are not lost building it back into a Rule. This
	// includes a field whose ValueType, or a KeyTypes entry, is outside
	// this build's own spec.KnownValueTypes (WRIT-334): ValidateFieldRule
	// tolerates that structurally, so a field is withheld here only for
	// one of the reasons above, never for naming a type this reader
	// doesn't recognize.
	Fields map[string][]SchemaField
	// Ops holds, per non-"schema" object type, every define-op declaration
	// that survived the same grammar check.
	Ops map[string][]SchemaOp
	// Descriptions holds, per non-"schema" object type, the type's own
	// Description (set on define-type, empty when never given one).
	Descriptions map[string]string
	// DeprecatedTypes holds, per non-"schema" object type, the type's own
	// Deprecated (set by deprecate-type).
	DeprecatedTypes map[string]bool
	// BoundBy maps an object type to the ObjectID of the one schema object
	// that binds it, so a producer rejection
	// (VocabulariesFromSchemas -> codec.Vocabularies) can name which schema
	// is responsible.
	BoundBy   map[string]string
	Conflicts []SchemaConflict
}

// keyColumnKey identifies one key column within one (op_type, op_version) —
// the unit spec.CheckKeyColumnAgreement is run over, and the unit a
// disagreement withholds every participating rule for.
type keyColumnKey struct {
	codec.OpVersionKey
	Column string
}

// keyColumnKeyLess orders key columns so the conflicts a schema resolves to
// are reported in an order that is a function of the schema alone, never of
// map iteration or declaration order (WRIT-186).
func keyColumnKeyLess(a, b keyColumnKey) bool {
	if a.OpType != b.OpType {
		return a.OpType < b.OpType
	}
	if a.OpVersion != b.OpVersion {
		return a.OpVersion < b.OpVersion
	}
	return a.Column < b.Column
}

// toFieldRule builds the spec.FieldRule form of a schema-declared field,
// used to run it through spec.ValidateFieldRule and spec.CheckTargetAgreement
// (both defined against that type) and to populate a codec.Vocabulary's
// Fields directly: spec.FieldRule is the field-rule currency engine/codec
// already imports spec for.
func toFieldRule(objectType string, f SchemaField) spec.FieldRule {
	return spec.FieldRule{
		OpType: f.OpType, OpVersion: f.OpVersion, Field: f.Name, Target: f.Target,
		Strategy: f.Strategy, Key: f.Key, Lattice: f.Lattice, ValueType: f.ValueType,
		Enum: f.Enum, MaxLength: f.MaxLength, KeyTypes: f.KeyTypes,
		ObjectType: objectType,
	}
}

// ResolveSchemaTypes runs the collision pass (§6) and, for every type that
// survives it, the per-field validation pass (§7 step 4, §9) exactly once,
// in ascending schema ObjectID order so two conforming implementations
// build the same result from the same input regardless of enumeration
// order. RulesFromSchemas and VocabulariesFromSchemas are thin, disjoint
// projections of this shared result: neither reruns the pass, and neither
// can disagree with the other about what is declared.
func ResolveSchemaTypes(schemas []Schema) ResolvedSchemaTypes {
	sorted := append([]Schema(nil), schemas...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ObjectID < sorted[j].ObjectID })

	boundBy := make(map[string]string) // object_type -> owning schema ObjectID
	declared := make(map[string]bool)
	var conflicts []SchemaConflict

	for _, sch := range sorted {
		if sch.Namespace != "" && !ValidNamespaceGrammar(sch.Namespace) {
			conflicts = append(conflicts, SchemaConflict{
				Kind:      SchemaConflictNamespaceUngrammatical,
				Namespace: sch.Namespace,
				ObjectIDs: []string{sch.ObjectID},
				Reason:    "namespace " + quote(sch.Namespace) + " is not a valid namespace (must match ^[a-z][a-z0-9-]*$, max " + formatInt(NamespaceGrammarMaxLength) + " chars) and none of its types were installed",
			})
			continue
		}

		if !SchemaObjectIDMatchesNamespace(sch) {
			conflicts = append(conflicts, SchemaConflict{
				Kind:      SchemaConflictObjectIDMismatch,
				Namespace: sch.Namespace,
				ObjectIDs: []string{sch.ObjectID},
				Reason:    "schema object id " + quote(sch.ObjectID) + " does not match the derived form " + quote(DeriveSchemaObjectID(sch.Namespace)) + " for namespace " + quote(sch.Namespace) + " and none of its types were installed",
			})
			continue
		}

		for _, t := range sch.Types {
			if t.Name == "schema" {
				declared[t.Name] = true
				conflicts = append(conflicts, SchemaConflict{
					Kind:       SchemaConflictSchemaRedefined,
					ObjectType: "schema",
					Namespace:  sch.Namespace,
					ObjectIDs:  []string{sch.ObjectID},
					Reason:     "schema is the engine's built-in bootstrap type and cannot be redefined from the log",
				})
				continue
			}

			if !ValidObjectTypeGrammar(t.Name) {
				conflicts = append(conflicts, SchemaConflict{
					Kind:       SchemaConflictTypeUngrammatical,
					ObjectType: t.Name,
					Namespace:  sch.Namespace,
					ObjectIDs:  []string{sch.ObjectID},
					Reason:     "define-type " + quote(t.Name) + " is not a valid object type (must match ^[a-z][a-z0-9-]{0,63}(\\.[a-z][a-z0-9-]{0,63})?$, max " + formatInt(ObjectTypeGrammarMaxLength) + " chars, and not end in \".lock\") and was not installed",
				})
				continue
			}

			if !TypeIsQualifiedForNamespace(t.Name, sch.Namespace) {
				conflicts = append(conflicts, SchemaConflict{
					Kind:       SchemaConflictTypeUnqualified,
					ObjectType: t.Name,
					Namespace:  sch.Namespace,
					ObjectIDs:  []string{sch.ObjectID},
					Reason:     "define-type " + quote(t.Name) + " is not qualified with this schema object's own namespace " + quote(sch.Namespace) + " (must be " + quote(sch.Namespace+".<type>") + ") and was not installed",
				})
				continue
			}
			declared[t.Name] = true
			boundBy[t.Name] = sch.ObjectID
		}
	}

	fields := make(map[string][]SchemaField)
	ops := make(map[string][]SchemaOp)
	descriptions := make(map[string]string)
	deprecatedTypes := make(map[string]bool)
	for _, sch := range sorted {
		for _, t := range sch.Types {
			if t.Name == "schema" || !DeclarationInstallable(t.Name, sch.Namespace) || !SchemaObjectIDMatchesNamespace(sch) {
				continue
			}

			descriptions[t.Name] = t.Description
			deprecatedTypes[t.Name] = t.Deprecated

			var survivingFields []SchemaField
			var survivingRules []spec.FieldRule
			for _, f := range t.Fields {
				sr := toFieldRule(t.Name, f)

				if !ValidOpTypeGrammar(sr.OpType) {
					conflicts = append(conflicts, SchemaConflict{
						Kind:       SchemaConflictOpTypeUngrammatical,
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     "define-field op_type " + quote(sr.OpType) + " is not a valid op type (must match ^[a-z][a-z0-9-]*$, max " + formatInt(OpTypeMaxLength) + " chars) and was not installed",
					})
					continue
				}

				if sr.OpType == "merge" {
					conflicts = append(conflicts, SchemaConflict{
						Kind:       SchemaConflictOpTypeReserved,
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     "define-field op_type " + quote(sr.OpType) + " is a format-reserved op type and was not installed",
					})
					continue
				}

				if err := spec.ValidateFieldRule(sr); err != nil {
					conflicts = append(conflicts, SchemaConflict{
						Kind:       SchemaConflictRuleInvalid,
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     "field rule (" + sr.OpType + ", " + formatInt(sr.OpVersion) + ", " + sr.Field + ") is invalid and was not installed: " + errorString(err),
					})
					continue
				}

				survivingFields = append(survivingFields, f)
				survivingRules = append(survivingRules, sr)
			}

			withheld := make([]bool, len(survivingFields))

			byKeyColumn := make(map[keyColumnKey][]int)
			var keyColumnKeys []keyColumnKey
			byOpVersion := make(map[codec.OpVersionKey][]int)
			for i, sr := range survivingRules {
				ovk := codec.OpVersionKey{OpType: sr.OpType, OpVersion: sr.OpVersion}
				byOpVersion[ovk] = append(byOpVersion[ovk], i)
			}
			for ovk, idxs := range byOpVersion {
				inScope := make([]spec.FieldRule, len(idxs))
				for j, idx := range idxs {
					inScope[j] = survivingRules[idx]
				}
				for _, col := range spec.KeyColumnsBound(inScope) {
					ck := keyColumnKey{OpVersionKey: ovk, Column: col}
					for _, idx := range idxs {
						if spec.ParticipatesInKeyColumn(survivingRules[idx], col) {
							byKeyColumn[ck] = append(byKeyColumn[ck], idx)
						}
					}
					keyColumnKeys = append(keyColumnKeys, ck)
				}
			}
			sort.Slice(keyColumnKeys, func(i, j int) bool { return keyColumnKeyLess(keyColumnKeys[i], keyColumnKeys[j]) })

			for _, ck := range keyColumnKeys {
				idxs := byKeyColumn[ck]
				columnRules := make([]spec.FieldRule, len(idxs))
				for j, idx := range idxs {
					columnRules[j] = survivingRules[idx]
				}
				if err := spec.CheckKeyColumnAgreement(ck.Column, columnRules); err != nil {
					conflicts = append(conflicts, SchemaConflict{
						Kind:       SchemaConflictKeyColumnDisagreement,
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     errorString(err),
					})
					for _, idx := range idxs {
						withheld[idx] = true
					}
				}
			}

			byTarget := make(map[string][]int)
			for i, sr := range survivingRules {
				if withheld[i] {
					continue
				}
				byTarget[sr.TargetKey()] = append(byTarget[sr.TargetKey()], i)
			}
			targetKeys := make([]string, 0, len(byTarget))
			for tk := range byTarget {
				targetKeys = append(targetKeys, tk)
			}
			sort.Strings(targetKeys)

			for _, tk := range targetKeys {
				idxs := byTarget[tk]
				targetRules := make([]spec.FieldRule, len(idxs))
				for j, idx := range idxs {
					targetRules[j] = survivingRules[idx]
				}
				if err := spec.CheckTargetAgreement(tk, targetRules); err != nil {
					conflicts = append(conflicts, SchemaConflict{
						Kind:       SchemaConflictTargetDisagreement,
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     errorString(err),
					})
					for _, idx := range idxs {
						withheld[idx] = true
					}
				}
			}

			var typeFields []SchemaField
			for i, f := range survivingFields {
				if !withheld[i] {
					typeFields = append(typeFields, f)
				}
			}

			for _, f := range typeFields {
				if positions := demotedPositions(f); len(positions) > 0 {
					conflicts = append(conflicts, SchemaConflict{
						Kind:       SchemaConflictValueTypeUnknown,
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     "field rule (" + f.OpType + ", " + formatInt(f.OpVersion) + ", " + f.Name + ") declares " + strings.Join(positions, ", ") + ", not in this reader's value-type catalogue; installed untyped at " + pluralPosition(len(positions)) + " (spec/schema-ops.md §10)",
					})
				}
			}

			var typeOps []SchemaOp
			for _, o := range t.Ops {
				if !ValidOpTypeGrammar(o.OpType) {
					conflicts = append(conflicts, SchemaConflict{
						Kind:       SchemaConflictOpTypeUngrammatical,
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     "define-op op_type " + quote(o.OpType) + " is not a valid op type (must match ^[a-z][a-z0-9-]*$, max " + formatInt(OpTypeMaxLength) + " chars) and was not installed",
					})
					continue
				}

				if o.OpType == "merge" {
					conflicts = append(conflicts, SchemaConflict{
						Kind:       SchemaConflictOpTypeReserved,
						ObjectType: t.Name,
						ObjectIDs:  []string{sch.ObjectID},
						Reason:     "define-op op_type " + quote(o.OpType) + " is a format-reserved op type and was not installed",
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

	return ResolvedSchemaTypes{
		Declared:        declared,
		Fields:          fields,
		Ops:             ops,
		Descriptions:    descriptions,
		DeprecatedTypes: deprecatedTypes,
		BoundBy:         boundBy,
		Conflicts:       conflicts,
	}
}

// RulesFromSchemas is the pure, I/O-free resolver that turns every folded
// schema object present in a repo into the []Rule shape the generic fold
// driver (Fold(ops, rules)) already consumes generically — the same shape
// SchemaRules and every other built-in *Rules() function already return.
func RulesFromSchemas(schemas []Schema) (map[string][]Rule, []SchemaConflict) {
	res := ResolveSchemaTypes(schemas)

	rules := make(map[string][]Rule)
	for typeName, typeFields := range res.Fields {
		var typeRules []Rule
		for _, f := range typeFields {
			valueType := f.ValueType
			if valueType != "" && !spec.KnownValueTypes[valueType] {
				valueType = ""
			}
			keyTypes := demoteKeyTypes(f.KeyTypes)

			typeRules = append(typeRules, Rule{
				OpType:     f.OpType,
				OpVersion:  f.OpVersion,
				Field:      f.Name,
				Target:     f.Target,
				Strategy:   f.Strategy,
				Key:        f.Key,
				Lattice:    f.Lattice,
				ValueType:  valueType,
				Enum:       f.Enum,
				MaxLength:  f.MaxLength,
				KeyTypes:   keyTypes,
				Deprecated: f.Deprecated,
				ObjectType: typeName,
			})
		}
		if len(typeRules) > 0 {
			rules[typeName] = typeRules
		}
	}

	return rules, res.Conflicts
}

func demoteKeyTypes(kt map[string]string) map[string]string {
	if len(kt) == 0 {
		return kt
	}
	demoted := make(map[string]string, len(kt))
	for col, valueType := range kt {
		if valueType != "" && !spec.KnownValueTypes[valueType] {
			valueType = ""
		}
		demoted[col] = valueType
	}
	return demoted
}

func demotedPositions(f SchemaField) []string {
	var positions []string
	if f.ValueType != "" && !spec.KnownValueTypes[f.ValueType] {
		positions = append(positions, "value_type "+quote(f.ValueType))
	}
	var cols []string
	for col, valueType := range f.KeyTypes {
		if valueType != "" && !spec.KnownValueTypes[valueType] {
			cols = append(cols, col)
		}
	}
	sort.Strings(cols)
	for _, col := range cols {
		positions = append(positions, "key_types["+quote(col)+"] = "+quote(f.KeyTypes[col]))
	}
	return positions
}

func pluralPosition(n int) string {
	if n == 1 {
		return "that position"
	}
	return "those positions"
}

// VocabulariesFromSchemas resolves every folded schema object present in a
// repo into the codec.Vocabularies shape the generic producer validator
// (engine/codec's BuildCommit/ValidateBody) checks tier 2 of
// spec/op-envelope.md's three-tier precedence against — the log-sourced
// counterpart to RulesFromSchemas's fold-rule shape, built from the exact
// same collision/validation pass (ResolveSchemaTypes) so the two can never
// disagree about what is declared.
func VocabulariesFromSchemas(schemas []Schema) (codec.Vocabularies, []SchemaConflict) {
	res := ResolveSchemaTypes(schemas)

	vocabularies := make(codec.Vocabularies, len(res.Declared))
	for typeName := range res.Declared {
		if typeName == "schema" {
			continue
		}

		v := codec.Vocabulary{
			Declared:       true,
			SchemaObjectID: res.BoundBy[typeName],
			OpTypes:        make(map[codec.OpVersionKey]bool),
			Fields:         make(map[codec.OpVersionKey][]spec.FieldRule),
		}
		for _, f := range res.Fields[typeName] {
			key := codec.OpVersionKey{OpType: f.OpType, OpVersion: f.OpVersion}
			v.OpTypes[key] = true
			v.Fields[key] = append(v.Fields[key], toFieldRule(typeName, f))
		}
		for _, o := range res.Ops[typeName] {
			v.OpTypes[codec.OpVersionKey{OpType: o.OpType, OpVersion: o.OpVersion}] = true
		}
		vocabularies[typeName] = v
	}

	return vocabularies, res.Conflicts
}

func quote(s string) string {
	return `"` + s + `"`
}

func formatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	get := err.Error
	return get()
}
