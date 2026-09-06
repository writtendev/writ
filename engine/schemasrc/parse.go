package schemasrc

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/writtendev/writ/spec"
)

// Grammar patterns, shared with the wire grammar they compile to
// (spec/schemas/schema-ops.schema.json): a declared type's bare name is
// used verbatim as object_type, so it shares object_type's grammar
// exactly, and the same is true of an op's op_type.
var (
	namespacePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	typeNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	opTypeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	fieldNamePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

const maxNameLength = 64

// Parse parses one writ.schema source file into an AST. name is used only
// to prefix error messages (conventionally "writ.schema", or a path).
//
// Parse never panics on any input, including invalid UTF-8 or truncated
// multi-byte sequences (exercised by FuzzParse): every lexer and parser
// branch either consumes at least one token or reports a SyntaxError.
func Parse(name string, src []byte) (*File, error) {
	toks, err := lexAll(name, src)
	if err != nil {
		return nil, err
	}
	p := &parser{name: name, toks: toks}
	f := p.parseFile()
	if len(p.errs) > 0 {
		return nil, p.errs
	}
	return f, nil
}

type parser struct {
	name     string
	toks     []token
	pos      int
	lastLine int
	errs     ErrorList
}

func (p *parser) peek() token {
	return p.toks[p.pos]
}

func (p *parser) advanceTok() token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	p.lastLine = t.Pos.Line
	return t
}

func (p *parser) errorf(pos Position, format string, args ...any) {
	p.errs = append(p.errs, &SyntaxError{File: p.name, Line: pos.Line, Col: pos.Col, Msg: fmt.Sprintf(format, args...)})
}

// describeToken renders a token for an "expected X, found Y" message.
func describeToken(t token) string {
	switch t.Kind {
	case tokEOF:
		return "end of file"
	case tokIdent:
		return fmt.Sprintf("%q", t.Text)
	case tokNumber:
		return fmt.Sprintf("number %q", t.Text)
	case tokString:
		return "string literal"
	case tokLBrace:
		return "'{'"
	case tokRBrace:
		return "'}'"
	case tokLParen:
		return "'('"
	case tokRParen:
		return "')'"
	case tokLBracket:
		return "'['"
	case tokRBracket:
		return "']'"
	case tokComma:
		return "','"
	case tokComment:
		return "comment"
	default:
		return "token"
	}
}

// expect consumes the next token if it has kind, else reports a
// SyntaxError naming what was expected.
func (p *parser) expect(kind tokenKind, what string) (token, bool) {
	if p.peek().Kind == kind {
		return p.advanceTok(), true
	}
	p.errorf(p.peek().Pos, "expected %s, found %s", what, describeToken(p.peek()))
	return token{}, false
}

// expectIdentAny consumes the next token as any identifier (not
// necessarily a specific keyword).
func (p *parser) expectIdentAny(what string) (token, bool) {
	return p.expect(tokIdent, what)
}

// expectKeyword consumes the next token if it is the identifier kw.
func (p *parser) expectKeyword(kw string) (Position, bool) {
	if p.peek().Kind == tokIdent && p.peek().Text == kw {
		t := p.advanceTok()
		return t.Pos, true
	}
	p.errorf(p.peek().Pos, "expected %q, found %s", kw, describeToken(p.peek()))
	return Position{}, false
}

// collectLeading gathers zero or more consecutive comment tokens as the
// leading comments of whatever declaration follows.
func (p *parser) collectLeading() []string {
	var out []string
	for p.peek().Kind == tokComment {
		out = append(out, p.advanceTok().Text)
	}
	return out
}

// trailingCommentSameLine consumes and returns a comment immediately
// following the just-parsed declaration on the same source line, or "" if
// the next token is not such a comment. A comment on its own line, even
// immediately after, is a leading comment of the next declaration instead
// (collectLeading picks it up there).
func (p *parser) trailingCommentSameLine() string {
	if p.peek().Kind == tokComment && p.peek().Pos.Line == p.lastLine {
		return p.advanceTok().Text
	}
	return ""
}

// skipUntilRBraceAtDepth0 discards tokens after an unrecoverable error
// until it consumes the `}` that closes the innermost brace-delimited
// block the parser was inside (nested braces are tracked so a stray `}`
// belonging to a deeper, already-malformed construct does not end the
// recovery early). It stops at EOF rather than looping forever.
func (p *parser) skipUntilRBraceAtDepth0() {
	depth := 0
	for {
		t := p.peek()
		if t.Kind == tokEOF {
			return
		}
		if t.Kind == tokLBrace {
			depth++
		}
		if t.Kind == tokRBrace {
			if depth == 0 {
				p.advanceTok()
				return
			}
			depth--
		}
		p.advanceTok()
	}
}

func (p *parser) parseFile() *File {
	f := &File{Name: p.name, Pos: Position{Line: 1, Col: 1}}
	f.LeadingComments = p.collectLeading()

	pos, ok := p.expectKeyword("namespace")
	if !ok {
		return f
	}
	f.Pos = pos
	nameTok, ok := p.expectIdentAny("namespace")
	if ok {
		validateName(p, nameTok, namespacePattern, "namespace")
		f.Namespace = nameTok.Text
	}
	f.NamespaceTrailingComment = p.trailingCommentSameLine()

	// A comment between `namespace` and `description` (or between
	// `namespace` and the first `type`, if there is no description) has
	// nowhere dedicated to attach: it is folded into the file's own
	// leading comments rather than dropped, so Format still preserves it
	// even though its exact position shifts to the top of the file.
	f.LeadingComments = append(f.LeadingComments, p.collectLeading()...)
	if p.peek().Kind == tokIdent && p.peek().Text == "description" {
		f.Description, _ = p.parseDescription()
		f.DescriptionTrailingComment = p.trailingCommentSameLine()
	}

	for {
		leading := p.collectLeading()
		if p.peek().Kind == tokEOF {
			f.TrailingComments = leading
			return f
		}
		if p.peek().Kind == tokIdent && p.peek().Text == "type" {
			t := p.parseType()
			if t != nil {
				// Comments before the `type` keyword come first; parseType
				// may itself have appended comments it found before an
				// inner description that had nowhere else to attach.
				t.LeadingComments = append(leading, t.LeadingComments...)
				f.Types = append(f.Types, t)
			}
			continue
		}
		p.errorf(p.peek().Pos, "unexpected %s at top level; expected 'type'", describeToken(p.peek()))
		p.advanceTok()
	}
}

// validateName reports a SyntaxError if tok's text does not match
// pattern or exceeds maxNameLength, or is a reserved keyword.
func validateName(p *parser, tok token, pattern *regexp.Regexp, what string) {
	if isKeyword(tok.Text) {
		p.errorf(tok.Pos, "%q is a reserved word and cannot be used as a %s", tok.Text, what)
		return
	}
	if len(tok.Text) > maxNameLength {
		p.errorf(tok.Pos, "%s %q is %d characters, over the %d-character limit", what, tok.Text, len(tok.Text), maxNameLength)
		return
	}
	if !pattern.MatchString(tok.Text) {
		p.errorf(tok.Pos, "invalid %s %q; must match %s", what, tok.Text, pattern.String())
	}
}

func (p *parser) parseDescription() (string, Position) {
	kwPos, _ := p.expectKeyword("description")
	strTok, ok := p.expect(tokString, "string literal after 'description'")
	if !ok {
		return "", kwPos
	}
	return strTok.Text, kwPos
}

func (p *parser) parseType() *Type {
	kwPos, _ := p.expectKeyword("type")
	t := &Type{Pos: kwPos}

	nameTok, ok := p.expectIdentAny("type name")
	if !ok {
		p.skipUntilRBraceAtDepth0()
		return nil
	}
	validateName(p, nameTok, typeNamePattern, "type name")
	t.Name = nameTok.Text

	if p.peek().Kind == tokIdent && p.peek().Text == "deprecated" {
		p.advanceTok()
		t.Deprecated = true
	}

	if _, ok := p.expect(tokLBrace, "'{' opening type body"); !ok {
		p.skipUntilRBraceAtDepth0()
		return t
	}

	for {
		leading := p.collectLeading()
		if p.peek().Kind == tokRBrace {
			t.DanglingComments = leading
			p.advanceTok()
			return t
		}
		if p.peek().Kind == tokEOF {
			p.errorf(p.peek().Pos, "unexpected end of file; expected '}' closing type %q", t.Name)
			return t
		}
		if p.peek().Kind == tokIdent && p.peek().Text == "description" {
			// A comment preceding this description has nowhere dedicated
			// to attach either; fold it into the type's own leading
			// comments rather than drop it (see the analogous file-level
			// case in parseFile).
			t.LeadingComments = append(t.LeadingComments, leading...)
			if len(t.Blocks) > 0 {
				p.errorf(p.peek().Pos, "type %q's description must appear before its op blocks", t.Name)
			} else if t.Description != "" {
				p.errorf(p.peek().Pos, "type %q already has a description", t.Name)
			}
			t.Description, _ = p.parseDescription()
			t.DescriptionTrailingComment = p.trailingCommentSameLine()
			continue
		}
		if p.peek().Kind == tokIdent && p.peek().Text == "op" {
			ob := p.parseOpBlock()
			if ob != nil {
				ob.LeadingComments = append(leading, ob.LeadingComments...)
				t.Blocks = append(t.Blocks, ob)
			}
			continue
		}
		p.errorf(p.peek().Pos, "unexpected %s inside type %q; expected 'description' or 'op'", describeToken(p.peek()), t.Name)
		p.skipUntilRBraceAtDepth0()
		return t
	}
}

func (p *parser) parseOpBlock() *OpBlock {
	kwPos, _ := p.expectKeyword("op")
	ob := &OpBlock{Pos: kwPos}

	for {
		opTok, ok := p.expectIdentAny("op type name")
		if !ok {
			p.skipUntilRBraceAtDepth0()
			return nil
		}
		validateName(p, opTok, opTypeNamePattern, "op type name")

		verTok, ok := p.expect(tokNumber, "op version number")
		if !ok {
			p.skipUntilRBraceAtDepth0()
			return nil
		}
		ver, err := parsePositiveInt(verTok.Text)
		if err != nil {
			p.errorf(verTok.Pos, "invalid op version %q: %v", verTok.Text, err)
		}
		ob.Ops = append(ob.Ops, OpDecl{OpType: opTok.Text, OpVersion: ver, Pos: opTok.Pos})

		if p.peek().Kind == tokComma {
			p.advanceTok()
			continue
		}
		break
	}

	if _, ok := p.expect(tokLBrace, "'{' opening op block body"); !ok {
		p.skipUntilRBraceAtDepth0()
		return ob
	}

	for {
		leading := p.collectLeading()
		if p.peek().Kind == tokRBrace {
			ob.DanglingComments = leading
			p.advanceTok()
			return ob
		}
		if p.peek().Kind == tokEOF {
			p.errorf(p.peek().Pos, "unexpected end of file; expected '}' closing op block")
			return ob
		}
		if p.peek().Kind == tokIdent && p.peek().Text == "description" {
			// See the analogous file- and type-level cases: a comment
			// preceding this description has nowhere dedicated to attach,
			// so it joins the op block's own leading comments instead of
			// being dropped.
			ob.LeadingComments = append(ob.LeadingComments, leading...)
			if len(ob.Fields) > 0 {
				p.errorf(p.peek().Pos, "op block's description must appear before its fields")
			} else if ob.Description != "" {
				p.errorf(p.peek().Pos, "op block already has a description")
			}
			ob.Description, _ = p.parseDescription()
			ob.DescriptionTrailingComment = p.trailingCommentSameLine()
			continue
		}
		field := p.parseField()
		if field == nil {
			p.skipUntilRBraceAtDepth0()
			return ob
		}
		field.LeadingComments = leading
		ob.Fields = append(ob.Fields, field)
	}
}

func (p *parser) parseField() *Field {
	nameTok, ok := p.expectIdentAny("field name")
	if !ok {
		return nil
	}
	validateName(p, nameTok, fieldNamePattern, "field name")
	f := &Field{Name: nameTok.Text, Pos: nameTok.Pos}

	vt, vtPos, ok := p.parseValueTypeExpr()
	if !ok {
		return nil
	}
	f.ValueType = vt

	strategy, lattice, stratPos, ok := p.parseStrategyExpr()
	if !ok {
		return nil
	}
	f.Strategy = strategy
	f.Lattice = lattice

	// [T] is required with a collection strategy and forbidden otherwise
	// (spec/schema-source.md: exactly one spelling per rule, so parse and
	// render stay inverse by construction). untyped fields carry no name
	// to bracket either way, so they are exempt.
	isCollectionStrategy := strategy == "set-union" || strategy == "set-observed-remove"
	if vt.Kind != ValueTypeNone {
		if isCollectionStrategy && vt.Kind != ValueTypeCollection {
			p.errorf(vtPos, "strategy %q needs a collection element type '[%s]', not a bare value type", strategy, vt.Name)
		}
		if !isCollectionStrategy && vt.Kind == ValueTypeCollection {
			p.errorf(stratPos, "'[%s]' is only valid with set-union or set-observed-remove, not strategy %q", vt.Name, strategy)
		}
	}

	// Each modifier has exactly one spelling per field (spec/schema-source.md
	// §3.3), which is what keeps parsing and rendering inverse operations:
	// a repeat is rejected here rather than silently kept (last write) or
	// silently merged (key's columns), either of which would let one field
	// carry two different spellings for the same rule.
	seenModifier := make(map[string]bool)
modifiers:
	for p.peek().Kind == tokIdent {
		text := p.peek().Text
		switch text {
		case "key", "target", "deprecated":
			if seenModifier[text] {
				p.errorf(p.peek().Pos, "modifier %q is already declared on this field", text)
			}
			seenModifier[text] = true
			switch text {
			case "key":
				p.parseKeyModifier(f)
			case "target":
				p.parseTargetModifier(f)
			case "deprecated":
				p.advanceTok()
				f.Deprecated = true
			}
		default:
			break modifiers
		}
	}

	// key(...) iff strategy == keyed-lww (spec/schema-source.md: exactly
	// one spelling per rule).
	if strategy == "keyed-lww" && len(f.Key) == 0 {
		p.errorf(f.Pos, "strategy keyed-lww requires a key(...) modifier")
	}
	if strategy != "keyed-lww" && len(f.Key) > 0 {
		p.errorf(f.Pos, "key(...) is only valid with strategy keyed-lww, not %q", strategy)
	}

	f.TrailingComment = p.trailingCommentSameLine()
	return f
}

func (p *parser) parseValueTypeExpr() (ValueType, Position, bool) {
	if p.peek().Kind == tokLBracket {
		bracketPos := p.peek().Pos
		p.advanceTok()
		nameTok, ok := p.expectIdentAny("element value type")
		if !ok {
			return ValueType{}, bracketPos, false
		}
		if _, ok := p.expect(tokRBracket, "']'"); !ok {
			return ValueType{}, bracketPos, false
		}
		if !spec.KnownValueTypes[nameTok.Text] {
			p.errorf(nameTok.Pos, "unknown value type %q; expected one of %s", nameTok.Text, knownValueTypeList())
		}
		return ValueType{Kind: ValueTypeCollection, Name: nameTok.Text}, bracketPos, true
	}

	nameTok, ok := p.expectIdentAny("value type")
	if !ok {
		return ValueType{}, Position{}, false
	}

	if nameTok.Text == "untyped" {
		return ValueType{Kind: ValueTypeNone}, nameTok.Pos, true
	}

	if nameTok.Text == "enum" {
		if _, ok := p.expect(tokLParen, "'(' after 'enum'"); !ok {
			return ValueType{}, nameTok.Pos, false
		}
		vals, ok := p.parseIdentList()
		if !ok {
			return ValueType{}, nameTok.Pos, false
		}
		if _, ok := p.expect(tokRParen, "')'"); !ok {
			return ValueType{}, nameTok.Pos, false
		}
		if len(vals) == 0 {
			p.errorf(nameTok.Pos, "enum(...) declares no values")
		}
		return ValueType{Kind: ValueTypeEnum, Enum: vals}, nameTok.Pos, true
	}

	if !spec.KnownValueTypes[nameTok.Text] {
		p.errorf(nameTok.Pos, "unknown value type %q; expected one of %s, enum(...), untyped, or [<value type>]", nameTok.Text, knownValueTypeList())
	}
	vt := ValueType{Kind: ValueTypeScalar, Name: nameTok.Text}

	if p.peek().Kind == tokLParen {
		if nameTok.Text != "string" && nameTok.Text != "text" {
			p.errorf(p.peek().Pos, "max_length parameter '(...)' is only valid on string/text, not %q", nameTok.Text)
		}
		p.advanceTok()
		numTok, ok := p.expect(tokNumber, "max_length")
		if !ok {
			return vt, nameTok.Pos, false
		}
		n, err := parsePositiveInt(numTok.Text)
		if err != nil {
			p.errorf(numTok.Pos, "invalid max_length %q: %v", numTok.Text, err)
		}
		vt.MaxLength = n
		if _, ok := p.expect(tokRParen, "')'"); !ok {
			return vt, nameTok.Pos, false
		}
	}
	return vt, nameTok.Pos, true
}

func (p *parser) parseStrategyExpr() (string, []string, Position, bool) {
	tok, ok := p.expectIdentAny("merge strategy")
	if !ok {
		return "", nil, Position{}, false
	}

	if tok.Text == "lattice" {
		if _, ok := p.expect(tokLParen, "'(' after 'lattice'"); !ok {
			return "lattice", nil, tok.Pos, false
		}
		vals, ok := p.parseIdentList()
		if !ok {
			return "lattice", nil, tok.Pos, false
		}
		if _, ok := p.expect(tokRParen, "')'"); !ok {
			return "lattice", vals, tok.Pos, false
		}
		if len(vals) == 0 {
			p.errorf(tok.Pos, "lattice(...) declares no elements")
		}
		return "lattice", vals, tok.Pos, true
	}

	if !spec.KnownCatalogueStrategies[tok.Text] {
		p.errorf(tok.Pos, "unknown merge strategy %q; expected one of %s", tok.Text, knownStrategyList())
	}
	return tok.Text, nil, tok.Pos, true
}

func (p *parser) parseKeyModifier(f *Field) {
	p.advanceTok() // 'key'
	if _, ok := p.expect(tokLParen, "'(' after 'key'"); !ok {
		return
	}
	if p.peek().Kind == tokRParen {
		p.errorf(p.peek().Pos, "key(...) declares no columns")
	} else {
		for {
			nameTok, ok := p.expectIdentAny("key column name")
			if !ok {
				return
			}
			typeTok, ok := p.expectIdentAny("key column value type")
			if !ok {
				return
			}
			if !spec.KnownValueTypes[typeTok.Text] {
				p.errorf(typeTok.Pos, "unknown value type %q for key column %q; expected one of %s", typeTok.Text, nameTok.Text, knownValueTypeList())
			}
			f.Key = append(f.Key, KeyCol{Name: nameTok.Text, ValueType: typeTok.Text})
			if p.peek().Kind == tokComma {
				p.advanceTok()
				continue
			}
			break
		}
	}
	p.expect(tokRParen, "')'")
}

func (p *parser) parseTargetModifier(f *Field) {
	p.advanceTok() // 'target'
	if _, ok := p.expect(tokLParen, "'(' after 'target'"); !ok {
		return
	}
	nameTok, ok := p.expectIdentAny("target name")
	if ok {
		validateName(p, nameTok, fieldNamePattern, "target name")
		f.Target = nameTok.Text
	}
	p.expect(tokRParen, "')'")
}

// parseIdentList parses a comma-separated list of bare identifiers, used
// by enum(...) and lattice(...). An empty list ("()") is syntactically
// legal here; callers reject it as semantically empty where the
// production requires at least one value.
func (p *parser) parseIdentList() ([]string, bool) {
	var out []string
	if p.peek().Kind == tokRParen {
		return out, true
	}
	for {
		tok, ok := p.expectIdentAny("identifier")
		if !ok {
			return out, false
		}
		out = append(out, tok.Text)
		if p.peek().Kind == tokComma {
			p.advanceTok()
			continue
		}
		break
	}
	return out, true
}

// parsePositiveInt parses s (all-digit, per the lexer) as a positive
// int64, rejecting zero and overflow.
func parsePositiveInt(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}
	var n int64
	for _, c := range s {
		d := int64(c - '0')
		if n > (1<<63-1-d)/10 {
			return 0, fmt.Errorf("number %q overflows", s)
		}
		n = n*10 + d
	}
	if n < 1 {
		return 0, fmt.Errorf("must be a positive integer, not %q", s)
	}
	return n, nil
}

func knownValueTypeList() string {
	return strings.Join(sortedKeys(spec.KnownValueTypes), ", ")
}

func knownStrategyList() string {
	return strings.Join(sortedKeys(spec.KnownCatalogueStrategies), ", ")
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
