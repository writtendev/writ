package schemasrc

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/spec"
)

// opVersionString is the single place in this package that formats an
// op_version body field. spec/schema-ops.md §3.1's op_version_string
// pattern (^[1-9][0-9]*$) has to hold on the wire for every define-op,
// define-field, and deprecate-field body this package emits — the WRIT-186
// round-1 bug was exactly this check missing, so "01" and "1" collapsed
// onto the same fold key and made resolution order-dependent. Funnelling
// every emission through one strconv.FormatInt call is what keeps "01"
// from ever having a second path to the wire: v is always a positive
// int64 parsed by parsePositiveInt (source) or already validated here
// (Compile), and FormatInt never produces a leading zero or emptiness.
func opVersionString(v int64) string {
	return strconv.FormatInt(v, 10)
}

// opKey identifies one define-op declaration within a type: (op_type, op_version).
type opKey struct {
	opType    string
	opVersion int64
}

// fieldKey identifies one define-field declaration within a type:
// (op_type, op_version, field) — spec/schema-ops.md's define-field key.
type fieldKey struct {
	opType    string
	opVersion int64
	field     string
}

// compiledOp carries one type's resolved op declaration, gathered from
// whichever OpBlock header named it.
type compiledOp struct {
	key         opKey
	description string
	pos         Position
}

// compiledField carries one type's resolved field declaration.
type compiledField struct {
	key        fieldKey
	field      *Field
	pos        Position
	deprecated bool
}

// Compile emits the spec/schema-ops.md v1 op sequence a parsed writ.schema
// file declares. objectID is an explicit, required parameter with no
// default; Compile itself derives nothing from f.Namespace — the caller
// derives it for a brand-new object (schema:<namespace>, WRIT-199) and
// otherwise reuses the existing one. Reusing the same object_id across
// successive applies is load-bearing (WRIT-191) — applying under a fresh
// id would create a second schema object binding the same object_types,
// which RulesFromSchemas treats as a collision and responds to by
// withholding all rules for those types.
//
// Compile's own emission order is deterministic and canonically sorted
// (create; then, per type in name order, define-type, deprecate-type,
// every define-op sorted by (op_type, op_version), every define-field
// sorted by (op_type, op_version, field), every deprecate-field in the
// same field order) regardless of how the source file arranges its op
// blocks or field lines — which is what makes Compile(Parse(src))
// deterministic and canonically ordered on its own terms, not merely
// stable for one unchanged source file, and is what lets the
// semantic-round-trip promise (spec/schema-source.md) hold: Render always
// reconstructs this same canonical order from folded state, so
// recompiling a parse of that rendering reaches the identical sequence.
//
// Every emitted define-field is validated through spec.ValidateFieldRule
// before Compile returns: the parser already enforces the single-field
// grammar-level invariants (value-type/strategy pairing, key(...) only on
// keyed-lww, and so on), but the per-rule invariants ValidateFieldRule
// alone knows — lattice elements being a subset of the declared enum,
// tombstone requiring value_type bool, key_types covering exactly its key
// columns — are checked here, so a file that would produce a rule
// RulesFromSchemas later drops is rejected at compile time, with a line
// and column, instead of silently vanishing at resolve time.
//
// One further check spans more than one rule, so ValidateFieldRule cannot
// see it on its own: define-fields within the same type sharing a
// TargetKey() (the declared target, or the field name if undeclared) but
// disagreeing on a merge attribute (spec/schema-ops.md §8, `fold.md` §5) —
// a version bump that changes strategy without also declaring a distinct
// target is the WRIT-198 class of this, but the check is set-level, not
// pairwise (WRIT-211): it partitions each target's rules into
// (op_type, field) version-bump classes first, so which pair a disagreement
// is reported against never depends on declaration order. compileType holds
// every field of the type at once, so this is checked here too, across the
// type's fields in canonical order, rather than deferred to the resolver,
// which would otherwise withhold the colliding rules silently once the ops
// are already signed and unremovable in the log.
func Compile(f *File, objectID string) ([]codec.Envelope, error) {
	if f == nil {
		return nil, fmt.Errorf("schemasrc: Compile: nil file")
	}
	if objectID == "" {
		return nil, fmt.Errorf("schemasrc: Compile: objectID is required")
	}

	var envs []codec.Envelope

	createBody := map[string]any{"namespace": f.Namespace}
	if f.Description != "" {
		createBody["description"] = f.Description
	}
	env, err := envelope(objectID, "create", createBody)
	if err != nil {
		return nil, err
	}
	envs = append(envs, env)

	types := append([]*Type(nil), f.Types...)
	sort.Slice(types, func(i, j int) bool { return types[i].Name < types[j].Name })

	seenTypeNames := make(map[string]bool)
	for _, t := range types {
		if seenTypeNames[t.Name] {
			return nil, &SyntaxError{File: f.Name, Line: t.Pos.Line, Col: t.Pos.Col, Msg: fmt.Sprintf("type %q is declared more than once", t.Name)}
		}
		seenTypeNames[t.Name] = true

		typeEnvs, err := compileType(f.Name, objectID, t)
		if err != nil {
			return nil, err
		}
		envs = append(envs, typeEnvs...)
	}

	return envs, nil
}

func compileType(fileName, objectID string, t *Type) ([]codec.Envelope, error) {
	var envs []codec.Envelope

	defineTypeBody := map[string]any{"type": t.Name}
	if t.Description != "" {
		defineTypeBody["description"] = t.Description
	}
	env, err := envelope(objectID, "define-type", defineTypeBody)
	if err != nil {
		return nil, err
	}
	envs = append(envs, env)

	if t.Deprecated {
		env, err := envelope(objectID, "deprecate-type", map[string]any{"type": t.Name, "deprecated": true})
		if err != nil {
			return nil, err
		}
		envs = append(envs, env)
	}

	ops := make(map[opKey]*compiledOp)
	var opOrder []opKey
	fields := make(map[fieldKey]*compiledField)
	var fieldOrder []fieldKey

	for _, block := range t.Blocks {
		for _, decl := range block.Ops {
			k := opKey{opType: decl.OpType, opVersion: decl.OpVersion}
			if _, dup := ops[k]; dup {
				return nil, &SyntaxError{File: fileName, Line: decl.Pos.Line, Col: decl.Pos.Col,
					Msg: fmt.Sprintf("op %q version %d is declared more than once for type %q", decl.OpType, decl.OpVersion, t.Name)}
			}
			ops[k] = &compiledOp{key: k, description: block.Description, pos: decl.Pos}
			opOrder = append(opOrder, k)
		}

		for _, field := range block.Fields {
			for _, decl := range block.Ops {
				k := fieldKey{opType: decl.OpType, opVersion: decl.OpVersion, field: field.Name}
				if _, dup := fields[k]; dup {
					return nil, &SyntaxError{File: fileName, Line: field.Pos.Line, Col: field.Pos.Col,
						Msg: fmt.Sprintf("field %q is declared more than once for (%s, %d) on type %q", field.Name, decl.OpType, decl.OpVersion, t.Name)}
				}
				fields[k] = &compiledField{key: k, field: field, pos: field.Pos, deprecated: field.Deprecated}
				fieldOrder = append(fieldOrder, k)
			}
		}
	}

	sort.Slice(opOrder, func(i, j int) bool { return opKeyLess(opOrder[i], opOrder[j]) })
	for _, k := range opOrder {
		co := ops[k]
		body := map[string]any{"type": t.Name, "op_type": k.opType, "op_version": opVersionString(k.opVersion)}
		if co.description != "" {
			body["description"] = co.description
		}
		env, err := envelope(objectID, "define-op", body)
		if err != nil {
			return nil, err
		}
		envs = append(envs, env)
	}

	sort.Slice(fieldOrder, func(i, j int) bool { return fieldKeyLess(fieldOrder[i], fieldOrder[j]) })

	// Build every field's body and derived rule first, in canonical order,
	// binding each into targetBindings as it goes — but withhold judgment on
	// any of them (§8, spec/fold.md §5's shared-target agreement) until
	// every field in the type has been seen. Running the check
	// incrementally, candidate-against-bound as each field arrived, is
	// exactly the pairwise form WRIT-211 replaced: whether a version-bump
	// carve-out applied depended on which prior a candidate happened to be
	// compared against first. Checking each target's whole rule set once,
	// after the loop, makes the answer a function of the type's fields
	// alone.
	type fieldRuleEntry struct {
		key  fieldKey
		body map[string]any
	}
	entries := make([]fieldRuleEntry, 0, len(fieldOrder))
	targetBindings := make(map[string][]spec.FieldRule) // TargetKey() -> every rule bound to it
	for _, k := range fieldOrder {
		cf := fields[k]
		body, err := fieldBody(t.Name, k, cf.field)
		if err != nil {
			return nil, err
		}
		rule, err := validateFieldBody(fileName, cf, body)
		if err != nil {
			return nil, err
		}
		entries = append(entries, fieldRuleEntry{key: k, body: body})
		targetBindings[rule.TargetKey()] = append(targetBindings[rule.TargetKey()], rule)
	}

	if err := checkTargetAgreement(fileName, fields, targetBindings); err != nil {
		return nil, err
	}

	for _, e := range entries {
		env, err := envelope(objectID, "define-field", e.body)
		if err != nil {
			return nil, err
		}
		envs = append(envs, env)
	}

	for _, k := range fieldOrder {
		cf := fields[k]
		if !cf.deprecated {
			continue
		}
		env, err := envelope(objectID, "deprecate-field", map[string]any{
			"type": t.Name, "op_type": k.opType, "op_version": opVersionString(k.opVersion),
			"field": k.field, "deprecated": true,
		})
		if err != nil {
			return nil, err
		}
		envs = append(envs, env)
	}

	return envs, nil
}

func opKeyLess(a, b opKey) bool {
	if a.opType != b.opType {
		return a.opType < b.opType
	}
	return a.opVersion < b.opVersion
}

func fieldKeyLess(a, b fieldKey) bool {
	if a.opType != b.opType {
		return a.opType < b.opType
	}
	if a.opVersion != b.opVersion {
		return a.opVersion < b.opVersion
	}
	return a.field < b.field
}

// fieldBody builds one define-field op body from an AST field and the
// (op_type, op_version) it is being emitted for.
func fieldBody(typeName string, k fieldKey, f *Field) (map[string]any, error) {
	body := map[string]any{
		"type": typeName, "op_type": k.opType, "op_version": opVersionString(k.opVersion),
		"field": k.field, "strategy": f.Strategy,
	}

	switch f.ValueType.Kind {
	case ValueTypeNone:
		// untyped: no value_type key at all.
	case ValueTypeScalar:
		body["value_type"] = f.ValueType.Name
		if f.ValueType.MaxLength > 0 {
			body["max_length"] = f.ValueType.MaxLength
		}
	case ValueTypeEnum:
		body["value_type"] = "enum"
		body["enum"] = append([]string(nil), f.ValueType.Enum...)
	case ValueTypeCollection:
		body["value_type"] = f.ValueType.Name
	default:
		return nil, fmt.Errorf("schemasrc: field %q: unknown value-type kind %d", k.field, f.ValueType.Kind)
	}

	if f.Strategy == "lattice" {
		body["lattice"] = append([]string(nil), f.Lattice...)
	}

	if f.Strategy == "keyed-lww" {
		keyCols := make([]string, 0, len(f.Key))
		keyTypes := make(map[string]string, len(f.Key))
		for _, kc := range f.Key {
			keyCols = append(keyCols, kc.Name)
			keyTypes[kc.Name] = kc.ValueType
		}
		body["key"] = keyCols
		body["key_types"] = keyTypes
	}

	if f.Target != "" {
		body["target"] = f.Target
	}

	return body, nil
}

// validateFieldBody re-derives the spec.FieldRule this body describes and
// runs it through spec.ValidateFieldRule — the same function every
// vocabulary's field-rules.json is validated through — so a rule this
// package would emit but RulesFromSchemas would later drop is rejected
// here instead, with the source position that produced it. It returns the
// derived rule so checkTargetAgreement can run the one cross-field check
// ValidateFieldRule cannot see (it validates one rule at a time) without
// re-deriving it.
func validateFieldBody(fileName string, cf *compiledField, body map[string]any) (spec.FieldRule, error) {
	rule := spec.FieldRule{
		OpType:    cf.key.opType,
		OpVersion: cf.key.opVersion,
		Field:     cf.key.field,
		Strategy:  cf.field.Strategy,
	}
	if s, ok := body["value_type"].(string); ok {
		rule.ValueType = s
	}
	if v, ok := body["enum"].([]string); ok {
		rule.Enum = v
	}
	if v, ok := body["max_length"].(int64); ok {
		rule.MaxLength = v
	}
	if v, ok := body["key"].([]string); ok {
		rule.Key = v
	}
	if v, ok := body["key_types"].(map[string]string); ok {
		rule.KeyTypes = v
	}
	if v, ok := body["lattice"].([]string); ok {
		rule.Lattice = v
	}
	if v, ok := body["target"].(string); ok {
		rule.Target = v
	}

	if err := spec.ValidateFieldRule(rule); err != nil {
		return rule, &SyntaxError{File: fileName, Line: cf.pos.Line, Col: cf.pos.Col, Msg: err.Error()}
	}
	return rule, nil
}

// checkTargetAgreement rejects a type whose fields include two or more
// rules sharing a target (spec.FieldRule.TargetKey: the declared target, or
// the field name if undeclared) that disagree on a merge attribute
// spec.CheckTargetAgreement holds shared targets to — the exact disagreement
// engine/schema.go's resolveSchemaTypes detects at resolve time by
// withholding every rule bound to the target and recording a SchemaConflict
// nobody on the `apply` path is obliged to inspect (spec/schema-ops.md §8,
// `fold.md` §5's shared-target agreement rule). compileType already holds
// the whole type when this runs, so — contrary to what an earlier draft of
// spec/schema-source.md §7 claimed — there is no missing information that
// would force this check to wait for the resolver; it runs once per target,
// over every rule targetBindings accumulated across the type's fields, in
// ascending target-key order so which of possibly several bad targets is
// reported first is deterministic. Compile REJECTS the file rather than
// withholding the target the way the resolver does — a source file is
// authored, not folded — and the SyntaxError names the position of the
// later-declared rule (canonical (op_type, op_version, field) order,
// fieldRuleOrderLess) among the disagreeing pair spec.FindTargetDisagreement
// names, the one whose version bump (or reused target) actually goes wrong.
func checkTargetAgreement(fileName string, fields map[fieldKey]*compiledField, targetBindings map[string][]spec.FieldRule) error {
	targetKeys := make([]string, 0, len(targetBindings))
	for tk := range targetBindings {
		targetKeys = append(targetKeys, tk)
	}
	sort.Strings(targetKeys)

	for _, tk := range targetKeys {
		err := spec.CheckTargetAgreement(tk, targetBindings[tk])
		if err == nil {
			continue
		}
		if d, ok := spec.FindTargetDisagreement(tk, targetBindings[tk]); ok {
			laterKey := fieldKey{opType: d.B.OpType, opVersion: d.B.OpVersion, field: d.B.Field}
			if cf, ok := fields[laterKey]; ok {
				return &SyntaxError{File: fileName, Line: cf.pos.Line, Col: cf.pos.Col, Msg: err.Error()}
			}
		}
		return &SyntaxError{File: fileName, Msg: err.Error()}
	}
	return nil
}

// envelope builds one schema-ops v1 codec.Envelope from a body map,
// canonically encoded.
func envelope(objectID, opType string, body map[string]any) (codec.Envelope, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return codec.Envelope{}, fmt.Errorf("schemasrc: marshaling %s body: %w", opType, err)
	}
	return codec.Envelope{
		ObjectID:   objectID,
		ObjectType: "schema",
		OpType:     opType,
		OpVersion:  1,
		Body:       raw,
	}, nil
}
