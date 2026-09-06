package schemasrc

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/writtendev/writ/engine/state"
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
