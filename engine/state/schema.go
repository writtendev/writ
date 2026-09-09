// Package state's schema.go folds the one object type writ hard-codes:
// `schema` (ARCHITECTURE.md §Schema layer, WRIT-186). Every other object
// type is data a `schema` object writes into the log, declared by a
// consumer; `schema` itself is the single permitted exception, because a
// schema has to exist before anything else can be typed. SchemaRules is
// the engine's own built-in table: it is the only rule set that never
// comes from the log (spec/schema-ops.md §Bootstrap), and this file
// survives WRIT-194's deletion of the embedded SDLC vocabulary tables.
package state

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
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

// fieldKey identifies one define-field / deprecate-field declaration:
// (type, op_type, op_version, field), the same tuple spec/schema-ops.md
// declares as the define-field keyed-lww key.
type schemaFieldKey struct {
	typ, opType, opVersion, field string
}

// schemaOpKey identifies one define-op declaration: (type, op_type, op_version).
type schemaOpKey struct {
	typ, opType, opVersion string
}

// FoldSchema executes deterministic fold reduction on an input set of
// operations for a schema collaborative object, returning the materialized
// Schema state. Every declared field folds through the closed strategy
// catalogue exactly as any other collaborative object does — create-once
// and lww for `create`, keyed-lww for everything else — so this reducer
// needs no strategy the fold driver does not already have.
func FoldSchema(ops []codec.Op) (Schema, error) {
	if len(ops) == 0 {
		return Schema{}, nil
	}

	orderedOps, err := fold.OrderWithTStar(ops)
	if err != nil {
		return Schema{}, err
	}

	var sch Schema
	if ops[0].ObjectID != "" {
		sch.ObjectID = ops[0].ObjectID
	}

	var unknownOps []UnknownOp
	rules := internalRules(SchemaRules())

	var namespaceSet bool

	typeNames := make(map[string]bool)
	typeDescriptions := make(map[string]string)
	typeDeprecated := make(map[string]bool)

	opNames := make(map[schemaOpKey]bool)
	opDescriptions := make(map[schemaOpKey]string)

	fieldNames := make(map[schemaFieldKey]bool)
	fieldValueType := make(map[schemaFieldKey]string)
	fieldEnum := make(map[schemaFieldKey][]string)
	fieldMaxLength := make(map[schemaFieldKey]int64)
	fieldStrategy := make(map[schemaFieldKey]string)
	fieldKeyCols := make(map[schemaFieldKey][]string)
	fieldKeyTypes := make(map[schemaFieldKey]map[string]string)
	fieldLattice := make(map[schemaFieldKey][]string)
	fieldTarget := make(map[schemaFieldKey]string)
	fieldDeprecated := make(map[schemaFieldKey]bool)

	for _, o := range orderedOps {
		op := o.Op
		if op.ObjectType != "schema" || op.OpVersion != 1 {
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     op.ID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
			})
			continue
		}

		var body map[string]any
		if len(op.Body) > 0 {
			if err := json.Unmarshal(op.Body, &body); err != nil {
				return Schema{}, fmt.Errorf("fold schema: unmarshaling op %s body: %w", op.ID, err)
			}
		}
		if body == nil {
			body = make(map[string]any)
		}

		// A field with a declared rule carrying a value its strategy cannot
		// consume makes the whole op uninterpretable (spec/fold.md §7.1). It
		// is quarantined here on exactly the terms the generic driver
		// applies, so the typed reducer and fold.Fold reject the same ops.
		if fold.Uninterpretable(op, body, rules) {
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     op.ID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
			})
			continue
		}

		// define-op, define-field, and deprecate-field carry op_version as a
		// keyed-lww key component, encoded as a decimal string
		// (spec/schemas/schema-ops.schema.json's op_version_string pattern
		// ^[1-9][0-9]*$: no leading zero). That pattern is producer-side —
		// the fold path never validates payloads against the JSON schema —
		// so a non-conforming peer's ref can still carry "01" alongside a
		// conforming "1". toInt64 would map both to the same int64, and two
		// declarations that are supposed to be distinct would collapse into
		// one indistinguishable-by-sort pair (WRIT-186 round-1 finding 1:
		// this is what made FoldSchema non-deterministic). Quarantine the
		// op instead of silently coercing it.
		if op.OpType == "define-op" || op.OpType == "define-field" || op.OpType == "deprecate-field" {
			if !canonicalOpVersion(stringField(body, "op_version")) {
				unknownOps = append(unknownOps, UnknownOp{
					Commit:     op.ID,
					ObjectType: op.ObjectType,
					OpType:     op.OpType,
					OpVersion:  op.OpVersion,
				})
				continue
			}
		}

		switch op.OpType {
		case "create":
			if !namespaceSet {
				if v, ok := body["namespace"].(string); ok {
					sch.Namespace = v
					namespaceSet = true
				}
			}
			if v, ok := body["description"].(string); ok {
				sch.Description = v
			}

		case "define-type":
			typ, _ := body["type"].(string)
			if typ == "" {
				break
			}
			typeNames[typ] = true
			if v, ok := body["description"].(string); ok {
				typeDescriptions[typ] = v
			}

		case "deprecate-type":
			typ, _ := body["type"].(string)
			if typ == "" {
				break
			}
			if v, ok := body["deprecated"].(bool); ok {
				typeDeprecated[typ] = v
			}

		case "define-op":
			key := schemaOpKey{
				typ:       stringField(body, "type"),
				opType:    stringField(body, "op_type"),
				opVersion: stringField(body, "op_version"),
			}
			opNames[key] = true
			if v, ok := body["description"].(string); ok {
				opDescriptions[key] = v
			}

		case "define-field":
			key := schemaFieldKey{
				typ:       stringField(body, "type"),
				opType:    stringField(body, "op_type"),
				opVersion: stringField(body, "op_version"),
				field:     stringField(body, "field"),
			}
			fieldNames[key] = true
			if v, ok := body["value_type"].(string); ok {
				fieldValueType[key] = v
			}
			if v, ok := stringSlice(body["enum"]); ok {
				fieldEnum[key] = v
			}
			if v, ok := body["max_length"]; ok {
				fieldMaxLength[key] = toInt64(v)
			}
			if v, ok := body["strategy"].(string); ok {
				fieldStrategy[key] = v
			}
			if v, ok := stringSlice(body["key"]); ok {
				fieldKeyCols[key] = v
			}
			if v, ok := stringStringMap(body["key_types"]); ok {
				fieldKeyTypes[key] = v
			}
			if v, ok := stringSlice(body["lattice"]); ok {
				fieldLattice[key] = v
			}
			if v, ok := body["target"].(string); ok {
				fieldTarget[key] = v
			}

		case "deprecate-field":
			key := schemaFieldKey{
				typ:       stringField(body, "type"),
				opType:    stringField(body, "op_type"),
				opVersion: stringField(body, "op_version"),
				field:     stringField(body, "field"),
			}
			if v, ok := body["deprecated"].(bool); ok {
				fieldDeprecated[key] = v
			}

		default:
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     op.ID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
			})
		}
	}

	sch.UnknownOps = unknownOps

	// Keys, not the SchemaField/SchemaOp values built from them, are what get
	// sorted: a schemaFieldKey/schemaOpKey carries op_version as the raw
	// canonical decimal string, and sorting on that (via compareOpVersion)
	// is genuinely total for any two distinct keys sharing a type — two
	// declarations differing only in op_version compare unequal because
	// their strings differ, unlike sorting the already-int64-converted
	// SchemaField/SchemaOp, where distinct canonical strings of unbounded
	// length could in principle convert to the same int64 (WRIT-186 round-1
	// finding 1). Building the struct only after the order is fixed makes
	// sort.Slice's instability moot: a comparator that never ties needs no
	// stable sort and leaves no room for map-iteration order to leak
	// through.
	fieldKeysByType := make(map[string][]schemaFieldKey)
	for key := range fieldNames {
		fieldKeysByType[key.typ] = append(fieldKeysByType[key.typ], key)
	}
	fieldsByType := make(map[string][]SchemaField)
	for typ, keys := range fieldKeysByType {
		sort.Slice(keys, func(i, j int) bool { return fieldKeyLess(keys[i], keys[j]) })
		fs := make([]SchemaField, 0, len(keys))
		for _, key := range keys {
			fs = append(fs, SchemaField{
				Name:       key.field,
				OpType:     key.opType,
				OpVersion:  toInt64(key.opVersion),
				ValueType:  fieldValueType[key],
				Enum:       fieldEnum[key],
				MaxLength:  fieldMaxLength[key],
				Strategy:   fieldStrategy[key],
				Key:        fieldKeyCols[key],
				KeyTypes:   fieldKeyTypes[key],
				Lattice:    fieldLattice[key],
				Target:     fieldTarget[key],
				Deprecated: fieldDeprecated[key],
			})
		}
		fieldsByType[typ] = fs
	}

	opKeysByType := make(map[string][]schemaOpKey)
	for key := range opNames {
		opKeysByType[key.typ] = append(opKeysByType[key.typ], key)
	}
	opsByType := make(map[string][]SchemaOp)
	for typ, keys := range opKeysByType {
		sort.Slice(keys, func(i, j int) bool { return opKeyLess(keys[i], keys[j]) })
		os := make([]SchemaOp, 0, len(keys))
		for _, key := range keys {
			os = append(os, SchemaOp{
				OpType:      key.opType,
				OpVersion:   toInt64(key.opVersion),
				Description: opDescriptions[key],
			})
		}
		opsByType[typ] = os
	}

	// A type surfaces in Schema.Types once it is named by a define-type op,
	// a define-field op, or a define-op op: nothing requires define-type to
	// precede the others, and a field or op declaration must not be
	// silently dropped for lacking one.
	seenTypes := make(map[string]bool)
	var typeList []string
	addType := func(t string) {
		if t != "" && !seenTypes[t] {
			seenTypes[t] = true
			typeList = append(typeList, t)
		}
	}
	for t := range typeNames {
		addType(t)
	}
	for t := range fieldsByType {
		addType(t)
	}
	for t := range opsByType {
		addType(t)
	}
	sort.Strings(typeList)

	for _, typ := range typeList {
		sch.Types = append(sch.Types, SchemaType{
			Name:        typ,
			Description: typeDescriptions[typ],
			Deprecated:  typeDeprecated[typ],
			Fields:      fieldsByType[typ],
			Ops:         opsByType[typ],
		})
	}

	return sch, nil
}

// SchemaRules returns the built-in field merge rules for the schema
// vocabulary (v1). This is the one rule table that never comes from the
// log (spec/schema-ops.md §Bootstrap): every other object type's rules are
// resolved from folded schema objects by engine/schema.go's
// RulesFromSchemas, but schema itself has to exist before anything else
// can be typed.
func SchemaRules() []Rule {
	fieldKeyTypes := map[string]string{"type": "string", "op_type": "string", "op_version": "string", "field": "string"}
	opKeyTypes := map[string]string{"type": "string", "op_type": "string", "op_version": "string"}
	typeKeyTypes := map[string]string{"type": "string"}
	fieldKey := []string{"type", "op_type", "op_version", "field"}
	opKey := []string{"type", "op_type", "op_version"}
	typeKey := []string{"type"}

	return []Rule{
		{OpType: "create", OpVersion: 1, Field: "namespace", Strategy: "create-once", ValueType: "string"},
		{OpType: "create", OpVersion: 1, Field: "description", Strategy: "lww", ValueType: "string"},

		{OpType: "define-type", OpVersion: 1, Field: "type", Target: "type_name", Strategy: "keyed-lww", Key: typeKey, KeyTypes: typeKeyTypes, ValueType: "string"},
		{OpType: "define-type", OpVersion: 1, Field: "description", Target: "type_description", Strategy: "keyed-lww", Key: typeKey, KeyTypes: typeKeyTypes, ValueType: "string"},

		{OpType: "deprecate-type", OpVersion: 1, Field: "deprecated", Target: "type_deprecated", Strategy: "keyed-lww", Key: typeKey, KeyTypes: typeKeyTypes, ValueType: "bool"},

		{OpType: "define-op", OpVersion: 1, Field: "op_type", Target: "op_name", Strategy: "keyed-lww", Key: opKey, KeyTypes: opKeyTypes, ValueType: "string"},
		{OpType: "define-op", OpVersion: 1, Field: "description", Target: "op_description", Strategy: "keyed-lww", Key: opKey, KeyTypes: opKeyTypes, ValueType: "string"},

		{OpType: "define-field", OpVersion: 1, Field: "field", Target: "field_name", Strategy: "keyed-lww", Key: fieldKey, KeyTypes: fieldKeyTypes, ValueType: "string"},
		{OpType: "define-field", OpVersion: 1, Field: "value_type", Target: "field_value_type", Strategy: "keyed-lww", Key: fieldKey, KeyTypes: fieldKeyTypes, ValueType: "string"},
		{OpType: "define-field", OpVersion: 1, Field: "enum", Target: "field_enum", Strategy: "keyed-lww", Key: fieldKey, KeyTypes: fieldKeyTypes},
		{OpType: "define-field", OpVersion: 1, Field: "max_length", Target: "field_max_length", Strategy: "keyed-lww", Key: fieldKey, KeyTypes: fieldKeyTypes, ValueType: "int"},
		{OpType: "define-field", OpVersion: 1, Field: "strategy", Target: "field_strategy", Strategy: "keyed-lww", Key: fieldKey, KeyTypes: fieldKeyTypes, ValueType: "string"},
		{OpType: "define-field", OpVersion: 1, Field: "key", Target: "field_key", Strategy: "keyed-lww", Key: fieldKey, KeyTypes: fieldKeyTypes},
		{OpType: "define-field", OpVersion: 1, Field: "key_types", Target: "field_key_types", Strategy: "keyed-lww", Key: fieldKey, KeyTypes: fieldKeyTypes},
		{OpType: "define-field", OpVersion: 1, Field: "lattice", Target: "field_lattice", Strategy: "keyed-lww", Key: fieldKey, KeyTypes: fieldKeyTypes},
		{OpType: "define-field", OpVersion: 1, Field: "target", Target: "field_target", Strategy: "keyed-lww", Key: fieldKey, KeyTypes: fieldKeyTypes, ValueType: "string"},

		{OpType: "deprecate-field", OpVersion: 1, Field: "deprecated", Target: "field_deprecated", Strategy: "keyed-lww", Key: fieldKey, KeyTypes: fieldKeyTypes, ValueType: "bool"},
	}
}

// stringField returns body[key] as a string, or "" if absent or not a string.
func stringField(body map[string]any, key string) string {
	s, _ := body[key].(string)
	return s
}

// stringSlice best-effort converts a decoded JSON value into a []string,
// skipping non-string elements. The generic fold map is the normative
// representation of a keyed-lww register (spec/fold.md §5.8: "registers
// hold values... stored verbatim"); this typed view is lossy on a
// malformed producer's output the same way every other typed struct in
// this package is, and Schema.UnknownOps never reports for it, because a
// register value's *shape* is orthogonal to whether the op that wrote it
// was interpretable.
func stringSlice(raw any) ([]string, bool) {
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	case []string:
		return v, true
	}
	return nil, false
}

// stringStringMap best-effort converts a decoded JSON value into a
// map[string]string, skipping non-string values. See stringSlice.
func stringStringMap(raw any) (map[string]string, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out, true
}

// toInt64 converts a decoded JSON number (float64) or a decimal string
// (the op_version body encoding, spec/schema-ops.md §Envelope binding) to
// an int64. Anything else, including a string that does not parse, yields
// zero: the generic fold map keeps the original value regardless.
func toInt64(raw any) int64 {
	switch v := raw.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case string:
		var n int64
		var ok bool
		if n, ok = parseDecimal(v); ok {
			return n
		}
	}
	return 0
}

// parseDecimal parses a non-negative decimal integer string without
// pulling in strconv's broader grammar (hex, underscores, signs) for what
// is, by schema, already `^[1-9][0-9]*$`.
func parseDecimal(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

// canonicalOpVersion reports whether s is op_version's canonical decimal
// encoding (spec/schemas/schema-ops.schema.json's op_version_string
// pattern ^[1-9][0-9]*$): digits only, no leading zero, non-empty. A
// conforming producer never writes anything else, but the fold path does
// not consult the JSON schema — that validation is producer-side — so a
// non-conforming peer's ref can still carry "01", "" or "1a"; FoldSchema
// quarantines any define-op/define-field/deprecate-field op whose
// op_version fails this check rather than folding it in under a key that
// collides with a canonically-encoded one.
func canonicalOpVersion(s string) bool {
	if _, ok := parseDecimal(s); !ok {
		return false
	}
	return s[0] != '0'
}

// compareOpVersion orders two canonical op_version decimal strings (no
// leading zero, no bound on digit count beyond the schema's maxLength: 16)
// by numeric value without converting either to int64 first: a shorter
// canonical decimal string is always numerically smaller regardless of
// digit content, and two same-length canonical strings compare correctly
// byte-for-byte. This is total for any two distinct canonical strings and
// never overflows, unlike comparing toInt64 conversions of arbitrarily
// long digit strings.
func compareOpVersion(a, b string) int {
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// fieldKeyLess orders schemaFieldKeys within one type. Unlike ordering the
// already-built SchemaField values (whose OpVersion is an int64 conversion
// that two distinct canonical op_version strings could in principle both
// produce for an oversized digit string), this compares the key's raw
// components directly, so two distinct keys — guaranteed to differ in at
// least field, opType, or opVersion, since fieldNames is keyed by exactly
// this struct — always compare unequal. That totality is what makes
// sort.Slice deterministic here regardless of the map-iteration order the
// keys arrive in (WRIT-186 round-1 finding 1).
func fieldKeyLess(a, b schemaFieldKey) bool {
	if a.opType != b.opType {
		return a.opType < b.opType
	}
	if c := compareOpVersion(a.opVersion, b.opVersion); c != 0 {
		return c < 0
	}
	return a.field < b.field
}

// opKeyLess orders schemaOpKeys within one type, on the same total-ordering
// basis as fieldKeyLess.
func opKeyLess(a, b schemaOpKey) bool {
	if a.opType != b.opType {
		return a.opType < b.opType
	}
	return compareOpVersion(a.opVersion, b.opVersion) < 0
}
