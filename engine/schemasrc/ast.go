package schemasrc

// Position is a 1-based line and 1-based column, counted in Unicode code
// points (spec/value-types.md §Length units), so a column here is the same
// unit as every other bound writ declares.
type Position struct {
	Line int
	Col  int
}

// File is the root of a parsed writ.schema source file.
type File struct {
	// Name is the name Parse was called with (conventionally
	// "writ.schema", or a path) — carried here so a Compile error can
	// report a position against the same file name a Parse error would
	// have used.
	Name        string
	Namespace   string
	Description string
	Types       []*Type

	// LeadingComments are comment lines preceding `namespace`.
	LeadingComments []string
	Pos             Position
}

// Type is one declared object type: `type <name> [deprecated] { ... }`.
type Type struct {
	Name        string
	Description string
	Deprecated  bool
	Blocks      []*OpBlock

	LeadingComments []string
	Pos             Position
}

// OpDecl names one op within an OpBlock's header, e.g. the `create 1` in
// `op create 1, update 1 { ... }`.
type OpDecl struct {
	OpType    string
	OpVersion int64
	Pos       Position
}

// OpBlock declares one or more op types sharing one field list.
type OpBlock struct {
	Ops         []OpDecl
	Description string
	Fields      []*Field

	LeadingComments []string
	Pos             Position
}

// ValueTypeKind distinguishes the value-type-expr productions
// (spec/schema-source.md §Grammar).
type ValueTypeKind int

const (
	// ValueTypeNone is the explicit `untyped` keyword: no value type
	// declared (WRIT-187 correction 3).
	ValueTypeNone ValueTypeKind = iota
	// ValueTypeScalar is a bare catalogue name, optionally parameterised
	// by max_length (`string(200)`).
	ValueTypeScalar
	// ValueTypeEnum is `enum(a, b, ...)`.
	ValueTypeEnum
	// ValueTypeCollection is `[name]`: the element type for a
	// set-union/set-observed-remove field.
	ValueTypeCollection
)

// ValueType is a parsed value-type-expr.
type ValueType struct {
	Kind ValueTypeKind
	Name string   // catalogue member, for Scalar and Collection
	Enum []string // ValueTypeEnum only

	// MaxLength is string(n)/text(n)'s parameter; 0 means undeclared.
	MaxLength int64
}

// KeyCol is one column of a `key(...)` modifier: its name and declared
// value type.
type KeyCol struct {
	Name      string
	ValueType string
}

// Field is one declared field within an OpBlock.
type Field struct {
	Name      string
	ValueType ValueType
	Strategy  string // catalogue strategy name, including "lattice"

	// Lattice holds the lattice(...) elements; set only when
	// Strategy == "lattice".
	Lattice []string
	// Key holds the key(...) columns; set only when Strategy == "keyed-lww".
	Key []KeyCol
	// Target is the target(...) modifier, or "" if undeclared.
	Target     string
	Deprecated bool

	LeadingComments []string
	TrailingComment string
	Pos             Position
}
