package schemasrc

import (
	"fmt"
	"strconv"
	"strings"
)

// Format parses src and reprints it canonically: every comment is
// preserved, and everything else — spacing, quoting, field and modifier
// order within a line — is normalized. Unlike Render, Format walks the
// parsed AST directly rather than folded state, so it preserves the
// file's own type, op-block, and field arrangement rather than
// recomputing FoldSchema's canonical grouping; Format(Format(src)) ==
// Format(src) because reprinting an already-canonical AST changes
// nothing.
func Format(name string, src []byte) ([]byte, error) {
	f, err := Parse(name, src)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	writeComments(&b, "", f.LeadingComments)
	fmt.Fprintf(&b, "namespace %s\n", f.Namespace)
	if f.Description != "" {
		fmt.Fprintf(&b, "description %s\n", quoteString(f.Description))
	}

	for _, t := range f.Types {
		b.WriteByte('\n')
		writeComments(&b, "", t.LeadingComments)
		formatType(&b, t)
	}

	return []byte(b.String()), nil
}

func writeComments(b *strings.Builder, indent string, comments []string) {
	for _, c := range comments {
		b.WriteString(indent)
		b.WriteByte('#')
		if c != "" {
			b.WriteByte(' ')
			b.WriteString(c)
		}
		b.WriteByte('\n')
	}
}

func formatType(b *strings.Builder, t *Type) {
	if t.Deprecated {
		fmt.Fprintf(b, "type %s deprecated {\n", t.Name)
	} else {
		fmt.Fprintf(b, "type %s {\n", t.Name)
	}
	if t.Description != "" {
		fmt.Fprintf(b, "  description %s\n", quoteString(t.Description))
	}
	for i, ob := range t.Blocks {
		if i > 0 || t.Description != "" {
			b.WriteByte('\n')
		}
		writeComments(b, "  ", ob.LeadingComments)
		formatOpBlock(b, ob)
	}
	b.WriteString("}\n")
}

func formatOpBlock(b *strings.Builder, ob *OpBlock) {
	b.WriteString("  op ")
	for i, o := range ob.Ops {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%s %d", o.OpType, o.OpVersion)
	}
	b.WriteString(" {\n")
	if ob.Description != "" {
		fmt.Fprintf(b, "    description %s\n", quoteString(ob.Description))
	}
	for _, field := range ob.Fields {
		writeComments(b, "    ", field.LeadingComments)
		formatField(b, field)
	}
	b.WriteString("  }\n")
}

func formatField(b *strings.Builder, f *Field) {
	b.WriteString("    ")
	b.WriteString(f.Name)
	b.WriteByte(' ')
	b.WriteString(formatValueType(f.ValueType, f.Strategy))
	b.WriteByte(' ')
	b.WriteString(formatStrategy(f.Strategy, f.Lattice))
	if len(f.Key) > 0 {
		b.WriteString(" key(")
		for i, kc := range f.Key {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "%s %s", kc.Name, kc.ValueType)
		}
		b.WriteByte(')')
	}
	if f.Target != "" {
		fmt.Fprintf(b, " target(%s)", f.Target)
	}
	if f.Deprecated {
		b.WriteString(" deprecated")
	}
	if f.TrailingComment != "" {
		b.WriteString("  #")
		b.WriteByte(' ')
		b.WriteString(f.TrailingComment)
	}
	b.WriteByte('\n')
}

func formatValueType(vt ValueType, strategy string) string {
	switch vt.Kind {
	case ValueTypeNone:
		return "untyped"
	case ValueTypeEnum:
		return "enum(" + strings.Join(vt.Enum, ", ") + ")"
	case ValueTypeCollection:
		return "[" + vt.Name + "]"
	default: // ValueTypeScalar
		if vt.MaxLength > 0 {
			return vt.Name + "(" + strconv.FormatInt(vt.MaxLength, 10) + ")"
		}
		return vt.Name
	}
}

func formatStrategy(strategy string, lattice []string) string {
	if strategy == "lattice" {
		return "lattice(" + strings.Join(lattice, ", ") + ")"
	}
	return strategy
}
