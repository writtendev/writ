// Package schemasrc parses writ.schema — the human-editable working-tree
// source form of a schema object (spec/schema-source.md, WRIT-187) — into
// an AST, compiles that AST into the spec/schema-ops.md v1 op sequence,
// renders folded schema state back to canonical source text, and formats
// a source file in place while preserving its comments.
//
// This package is public, not internal: cmd/writ consumes it (WRIT-191),
// and AGENTS.md forbids anything built on top of writ reaching into
// internals.
//
// The surface syntax this package parses is informative. The normative
// artefact is spec/schema-ops.md's op vocabulary; an independent
// implementation of writ has to agree on the ops this package compiles
// to, not on this grammar, and need not parse writ.schema files at all.
//
// # The fence
//
// The language this package parses declares types, value types, merge
// strategies, and relations — nothing else (ARCHITECTURE.md §Schema
// layer, WRIT-184 decision 6). There is no expression, call, conditional,
// import, extends, or annotation production anywhere in the parser: a
// computed field or a hook has no syntax to be written in. The keyword
// table (lex.go) is closed and enumerated; TestKeywordsAreClosed fails by
// name the moment it grows.
//
// # What round-trips, and what does not
//
// Comments and source layout have no representation in the op vocabulary
// (spec/schema-ops.md's define-field body has no description property,
// and state.FoldSchema sorts and discards source order) and so cannot
// survive a log round-trip. This package promises two precise things
// instead of the one imprecise promise the DSL might suggest:
//
//   - A semantic round-trip through the op vocabulary:
//     Compile(Parse(Render(FoldSchema(Compile(Parse(src)))))) reproduces
//     Compile(Parse(src)) byte for byte.
//   - A comment-preserving Format that reprints a source file canonically
//     in place: Format(Format(src)) == Format(src), and every comment
//     survives.
//
// Prose that must persist through the log is written as an explicit
// `description "..."`, which is data, not a comment.
package schemasrc
