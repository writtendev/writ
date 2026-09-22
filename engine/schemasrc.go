package writ

import (
	"fmt"

	"github.com/writtendev/writ/internal/schemasrc"
)

// SchemaSource is a parsed writ.schema working-tree source file
// (spec/schema-source.md, WRIT-187): the human-editable form `writ schema
// plan`/`apply` compile against the schema objects folded from the log.
//
// It is deliberately opaque rather than an alias for *schemasrc.File.
// schemasrc.File is a full AST — Type, OpBlock, OpDecl, Field, ValueType,
// ValueTypeKind and their comment-attachment fields exist to serve
// schemasrc.Format, a formatter this front end does not expose — and
// aliasing it would pin roughly fifty exported fields at v0.1.0 to serve
// a consumer, cmd/writ's buildSchemaPlan, that reads exactly one of them.
// SchemaSource exposes exactly the three operations that consumer needs:
// Namespace, Compile, and (via ParseSchemaSource) construction. No AST
// reaches the public surface (WRIT-310, VISION.md's typed-codegen
// non-goal).
type SchemaSource struct {
	file *schemasrc.File
}

// ParseSchemaSource parses one writ.schema source file. name is used only
// to prefix error messages (conventionally "writ.schema", or a path).
//
// A parse failure is returned as a SchemaErrorList (almost always, one
// entry per malformed line) or, for a lex-level failure, a single
// *SchemaSyntaxError — never wrapped, so a caller can errors.As either
// shape directly, exactly as cmd/writ's renderSchemaError does.
func ParseSchemaSource(name string, src []byte) (*SchemaSource, error) {
	f, err := schemasrc.Parse(name, src)
	if err != nil {
		return nil, err
	}
	return &SchemaSource{file: f}, nil
}

// Namespace returns the namespace this source file declares
// (spec/schema-ops.md §2's bare-form grammar).
func (s *SchemaSource) Namespace() string {
	if s == nil {
		return ""
	}
	return s.file.Namespace
}

// Compile compiles s into the canonical spec/schema-ops.md v1 op sequence
// that would bring a schema object identified by objectID in line with
// what s declares — every create/define-type/define-op/define-field op a
// fresh apply of s would need, in canonical order. It performs no I/O and
// appends nothing; a caller compares the result against a schema object's
// already-folded state to compute what, if anything, to append.
func (s *SchemaSource) Compile(objectID string) ([]Envelope, error) {
	if s == nil {
		return nil, fmt.Errorf("writ: schema source is nil")
	}
	return schemasrc.Compile(s.file, objectID)
}

// RenderSchemaSource renders s — folded schema object state, typically
// from Store.Schema or Store.SchemaAfterApply — back to canonical
// writ.schema source text (spec/schema-source.md). It is the reverse of
// SchemaSource.Compile, but only up to the semantic round-trip promise:
// comments and source layout have no representation in the log and so
// cannot survive a Compile/Fold/RenderSchemaSource cycle.
//
// A package-level function, not a method on Schema: Schema is a type
// alias for state.Schema (WRIT-287), so an exported method reachable
// through it would surface in api/engine.txt as a promise on the
// internal type itself, not on the opaque front end this ticket is
// scoped to (WRIT-310 plan §5, "the alias trap").
func RenderSchemaSource(s Schema) ([]byte, error) {
	return schemasrc.Render(s)
}

// ValidateNamespace reports whether name satisfies the same namespace
// grammar a `namespace` declaration enforces at parse time
// (spec/schema-ops.md §2: non-empty, at most 64 characters, matching
// ^[a-z][a-z0-9-]*$, and not a reserved word), without a source file to
// parse it from. cmd/writ's `writ init` uses this to validate a namespace
// a human supplies for a starter writ.schema before writing it; see
// schemasrc.ValidateNamespace's own doc comment for why synthesizing a
// one-line source file and parsing it back is not a safe substitute (a
// namespace containing a newline would inject further declarations
// instead of being rejected).
func ValidateNamespace(name string) error {
	return schemasrc.ValidateNamespace(name)
}

// SchemaSyntaxError is one parse failure, carrying the line and column a
// human editing writ.schema by hand needs to find it (1-based, counted in
// Unicode code points, per spec/value-types.md §Length units).
type SchemaSyntaxError = schemasrc.SyntaxError

// SchemaErrorList collects every SchemaSyntaxError a parse produced, so a
// hand-edited file reports more than its first mistake. ParseSchemaSource
// returns this shape directly (not wrapped) when parsing fails past the
// lexer.
type SchemaErrorList = schemasrc.ErrorList
