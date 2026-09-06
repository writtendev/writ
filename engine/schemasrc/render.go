package schemasrc

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec"
)

// Render renders folded schema state back to canonical writ.schema source
// text (spec/schema-source.md). It is the reverse of Compile, but only up
// to the semantic round-trip promise: comments and source layout have no
// representation in the log (WRIT-187 correction 1) and so cannot survive
// a Compile/FoldSchema/Render cycle; Format is what preserves those, on a
// source file directly.
//
// Ordering follows FoldSchema's own sort exactly: types by name; fields
// within a type by (op_type, op_version, field). Op blocks are
// reconstructed by grouping every (op_type, op_version) pair present —
// whether from a define-op, a define-field, or both — into equivalence
// classes sharing an identical field-rule set and description, each
// class rendered as one `op ... { }` block, ordered by its lowest
// (op_type, op_version). UnknownOps are not declarations and are never
// rendered.
func Render(s state.Schema) ([]byte, error) {
	// namespace, a type name, and an op_type are each a bare
	// object_type/op_type-shaped wire string (schema-ops.md §4) with no
	// pattern of its own on the wire — object_type: "op" is fully
	// wire-legal and folds straight through — so, exactly like a field
	// name, target, enum member, or lattice element (validateFieldForRender
	// above), each must be validated as a legal source identifier before
	// any text naming it is written; otherwise Render emits `type op {`
	// for a conforming producer's state and Parse rejects it as a
	// reserved word (round-2 review of WRIT-187 PR #157, finding 2).
	if err := validateNameForRender(s.Namespace, namespacePattern, "namespace"); err != nil {
		return nil, err
	}

	var b strings.Builder

	fmt.Fprintf(&b, "namespace %s\n", s.Namespace)
	if s.Description != "" {
		fmt.Fprintf(&b, "description %s\n", quoteString(s.Description))
	}

	for _, t := range s.Types {
		b.WriteByte('\n')
		if err := renderType(&b, t); err != nil {
			return nil, err
		}
	}

	return []byte(b.String()), nil
}

func renderType(b *strings.Builder, t state.SchemaType) error {
	if err := validateNameForRender(t.Name, typeNamePattern, "type name"); err != nil {
		return err
	}
	if t.Deprecated {
		fmt.Fprintf(b, "type %s deprecated {\n", t.Name)
	} else {
		fmt.Fprintf(b, "type %s {\n", t.Name)
	}
	if t.Description != "" {
		fmt.Fprintf(b, "  description %s\n", quoteString(t.Description))
	}

	groups, err := groupOps(t)
	if err != nil {
		return fmt.Errorf("schemasrc: render type %q: %w", t.Name, err)
	}

	for i, g := range groups {
		if i > 0 || t.Description != "" {
			b.WriteByte('\n')
		}
		if err := renderOpBlock(b, g); err != nil {
			return err
		}
	}

	b.WriteString("}\n")
	return nil
}

// opGroup is one equivalence class of (op_type, op_version) pairs sharing
// an identical field-rule set and description, rendered as one op block.
type opGroup struct {
	Ops         []opKey
	Description string
	Fields      []state.SchemaField // any one member's field list; identical across the group
}

// fieldView is a SchemaField's declarative content, without the
// (op_type, op_version) it happens to be filed under — the comparison
// basis for whether two op declarations belong in the same rendered
// block.
type fieldView struct {
	Name       string
	ValueType  string
	Enum       []string
	MaxLength  int64
	Strategy   string
	Key        []string
	KeyTypes   map[string]string
	Lattice    []string
	Target     string
	Deprecated bool
}

func toFieldView(f state.SchemaField) fieldView {
	return fieldView{
		Name: f.Name, ValueType: f.ValueType, Enum: f.Enum, MaxLength: f.MaxLength,
		Strategy: f.Strategy, Key: f.Key, KeyTypes: f.KeyTypes, Lattice: f.Lattice,
		Target: f.Target, Deprecated: f.Deprecated,
	}
}

// groupOps computes t's rendered op blocks: the union of every
// (op_type, op_version) named by t.Ops or t.Fields, grouped by identical
// (description, field-rule-set) content, ordered by each group's lowest
// (op_type, op_version).
func groupOps(t state.SchemaType) ([]opGroup, error) {
	opDescription := make(map[opKey]string)
	present := make(map[opKey]bool)
	for _, o := range t.Ops {
		k := opKey{opType: o.OpType, opVersion: o.OpVersion}
		opDescription[k] = o.Description
		present[k] = true
	}

	fieldsByOp := make(map[opKey][]state.SchemaField)
	for _, f := range t.Fields {
		k := opKey{opType: f.OpType, opVersion: f.OpVersion}
		fieldsByOp[k] = append(fieldsByOp[k], f)
		present[k] = true
	}

	var keys []opKey
	for k := range present {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return opKeyLess(keys[i], keys[j]) })

	var groups []opGroup
	assigned := make(map[opKey]bool)
	for _, k := range keys {
		if assigned[k] {
			continue
		}
		g := opGroup{Ops: []opKey{k}, Description: opDescription[k], Fields: fieldsByOp[k]}
		assigned[k] = true
		for _, other := range keys {
			if assigned[other] {
				continue
			}
			if opDescription[other] != g.Description {
				continue
			}
			if !sameFieldSet(g.Fields, fieldsByOp[other]) {
				continue
			}
			g.Ops = append(g.Ops, other)
			assigned[other] = true
		}
		groups = append(groups, g)
	}

	sort.Slice(groups, func(i, j int) bool { return opKeyLess(groups[i].Ops[0], groups[j].Ops[0]) })
	for i := range groups {
		sort.Slice(groups[i].Ops, func(a, b int) bool { return opKeyLess(groups[i].Ops[a], groups[i].Ops[b]) })
	}
	return groups, nil
}

func sameFieldSet(a, b []state.SchemaField) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !reflect.DeepEqual(toFieldView(a[i]), toFieldView(b[i])) {
			return false
		}
	}
	return true
}

func renderOpBlock(b *strings.Builder, g opGroup) error {
	for _, k := range g.Ops {
		if err := validateNameForRender(k.opType, opTypeNamePattern, "op type name"); err != nil {
			return err
		}
	}
	b.WriteString("  op ")
	for i, k := range g.Ops {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%s %d", k.opType, k.opVersion)
	}
	b.WriteString(" {\n")
	if g.Description != "" {
		fmt.Fprintf(b, "    description %s\n", quoteString(g.Description))
	}
	for _, f := range g.Fields {
		if err := renderField(b, f); err != nil {
			return err
		}
	}
	b.WriteString("  }\n")
	return nil
}

func renderField(b *strings.Builder, f state.SchemaField) error {
	if err := validateFieldForRender(f); err != nil {
		return fmt.Errorf("field %q: %w", f.Name, err)
	}
	valueType, err := renderValueType(f)
	if err != nil {
		return fmt.Errorf("field %q: %w", f.Name, err)
	}
	b.WriteString("    ")
	b.WriteString(f.Name)
	b.WriteByte(' ')
	b.WriteString(valueType)
	b.WriteByte(' ')
	b.WriteString(renderStrategy(f))
	if f.Strategy == "keyed-lww" {
		b.WriteString(" key(")
		for i, col := range f.Key {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "%s %s", col, f.KeyTypes[col])
		}
		b.WriteByte(')')
	}
	if f.Target != "" {
		fmt.Fprintf(b, " target(%s)", f.Target)
	}
	if f.Deprecated {
		b.WriteString(" deprecated")
	}
	b.WriteByte('\n')
	return nil
}

// identLexPattern matches whatever lex.go's isIdentStart/isIdentCont
// accept as a bare identifier token, independent of any further naming
// convention a specific grammar production layers on top. enum(...),
// lattice(...), and a key(...) column name are all parsed via
// expectIdentAny alone, with no accompanying validateName call (parse.go),
// so this is the exact bar Render must clear for those slots to guarantee
// Parse accepts the result back — nothing stricter is required, but
// nothing looser is safe either (spaces and grammar punctuation break the
// lexer, per spec/schemas/schema-ops.schema.json's define_field_body: it
// puts no pattern on enum items or target, so both are otherwise
// wire-legal with a space).
var identLexPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// validateNameForRender applies the same constraints Parse's validateName
// enforces on a name in this slot (parse.go): pattern is the slot's own
// naming pattern (spec/schema-source.md §3.3) — fieldNamePattern for a
// field name or target(...), namespacePattern for the namespace,
// typeNamePattern for a type name, opTypeNamePattern for an op type —
// plus the reserved-word exclusion and the length limit, both shared
// across every slot. Render must clear this stricter bar (not just
// identLexPattern) for every one of these five slots, because
// parseField/parseTargetModifier/parseFile/parseType/parseOpBlock all
// route through validateName rather than a bare expectIdentAny.
func validateNameForRender(name string, pattern *regexp.Regexp, what string) error {
	if isKeyword(name) {
		return fmt.Errorf("%s %q is a reserved word and would not parse back", what, name)
	}
	if len(name) > maxNameLength {
		return fmt.Errorf("%s %q is %d characters, over the %d-character limit and would not parse back", what, name, len(name), maxNameLength)
	}
	if !pattern.MatchString(name) {
		return fmt.Errorf("%s %q would not parse back; must match %s", what, name, pattern.String())
	}
	return nil
}

// validateIdentForRender checks only that s would lex as a bare
// identifier token — see identLexPattern.
func validateIdentForRender(s, what string) error {
	if !identLexPattern.MatchString(s) {
		return fmt.Errorf("%s %q would not lex as an identifier and would not parse back; must match %s", what, s, identLexPattern.String())
	}
	return nil
}

// validateFieldForRender rejects, before any text is written, every
// declaration this grammar has no spelling for or that Parse would then
// reject — the wire's define_field_body has no pattern on enum, lattice,
// key, or target (spec/schemas/schema-ops.schema.json), and FoldSchema
// does no catalogue validation of strategy or value_type, so all of this
// is reachable only from a non-conforming writer's log, never from
// Compile's own output. Naming the offending declaration (which field,
// which part) is the point: WRIT-191's `plan` diffs a log's rendering
// against the working tree and would otherwise write out a writ.schema
// `apply` cannot parse back, with no indication of which declaration
// broke it.
func validateFieldForRender(f state.SchemaField) error {
	if err := validateNameForRender(f.Name, fieldNamePattern, "field name"); err != nil {
		return err
	}
	if !spec.KnownCatalogueStrategies[f.Strategy] {
		return fmt.Errorf("unknown merge strategy %q", f.Strategy)
	}
	if f.ValueType != "" && !spec.KnownValueTypes[f.ValueType] {
		return fmt.Errorf("unknown value type %q", f.ValueType)
	}
	for _, m := range f.Enum {
		if err := validateIdentForRender(m, "enum member"); err != nil {
			return err
		}
	}
	for _, m := range f.Lattice {
		if err := validateIdentForRender(m, "lattice element"); err != nil {
			return err
		}
	}
	if f.Target != "" {
		if err := validateNameForRender(f.Target, fieldNamePattern, "target"); err != nil {
			return err
		}
	}
	for _, col := range f.Key {
		if err := validateIdentForRender(col, "key column"); err != nil {
			return err
		}
		if kt := f.KeyTypes[col]; !spec.KnownValueTypes[kt] {
			return fmt.Errorf("key column %q declares unknown value type %q", col, kt)
		}
	}
	return nil
}

// renderValueType renders a field's value-type-expr. It errors, rather
// than emitting text Parse would reject or silently reinterpret, for the
// two combinations this grammar has no production for: max_length or an
// enum value_type on a collection-strategy field. Neither combination is
// reachable through Compile — the DSL's [<name>] production carries no
// parameters and only a bare identifier — so this path is only reachable
// rendering a foreign log's schema state.
func renderValueType(f state.SchemaField) (string, error) {
	if f.ValueType == "" {
		return "untyped", nil
	}
	isCollection := f.Strategy == "set-union" || f.Strategy == "set-observed-remove"
	if isCollection {
		if f.MaxLength > 0 {
			return "", fmt.Errorf("max_length is not representable on a collection value type (strategy %q)", f.Strategy)
		}
		if f.ValueType == "enum" {
			return "", fmt.Errorf("value_type enum is not representable on a collection value type (strategy %q)", f.Strategy)
		}
		return "[" + f.ValueType + "]", nil
	}
	if f.ValueType == "enum" {
		return "enum(" + strings.Join(f.Enum, ", ") + ")", nil
	}
	name := f.ValueType
	if f.MaxLength > 0 && (f.ValueType == "string" || f.ValueType == "text") {
		name = name + "(" + strconv.FormatInt(f.MaxLength, 10) + ")"
	}
	return name, nil
}

func renderStrategy(f state.SchemaField) string {
	if f.Strategy == "lattice" {
		return "lattice(" + strings.Join(f.Lattice, ", ") + ")"
	}
	return f.Strategy
}

// quoteString renders s as a writ.schema string literal: double-quoted,
// with '"' and '\' escaped and no other transformation (comments and
// layout are the only things this grammar declines to round-trip; the
// text of a description is data, and is preserved verbatim).
func quoteString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
