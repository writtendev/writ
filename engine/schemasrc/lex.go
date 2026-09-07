package schemasrc

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// tokenKind enumerates the lexical token kinds writ.schema's grammar needs.
// There is no token for an operator, a dot, a colon, or any of the other
// punctuation an expression or annotation grammar would need — the fence
// (WRIT-184 decision 6) is structural, not a parser-time choice: the
// punctuation to write one was never lexed in the first place.
type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokNumber
	tokString
	tokLBrace
	tokRBrace
	tokLParen
	tokRParen
	tokLBracket
	tokRBracket
	tokComma
	tokComment
)

// token is one lexed unit. Text carries the decoded value: the literal
// spelling for tokIdent/tokNumber, the unescaped content for tokString, and
// the trimmed comment body (without the leading `#`) for tokComment.
type token struct {
	Kind tokenKind
	Text string
	Pos  Position
}

// keywords is writ.schema's closed, enumerated reserved-word set
// (spec/schema-source.md §2). Reservation is positional, not global: in
// the namespace, type-name, and op-type-name slots, all eight remain
// reserved outright — this is the set to check there. In a field name
// or a target(...) argument, every word except deprecated is only a
// contextual keyword — the parser's own lookahead (parseOpBlock's
// description branch, parseField's key/target modifier handling) tells
// it apart from a field name with no ambiguity, and validateName checks
// fieldReserved rather than this map for those two slots.
// TestKeywordsAreClosed fails by name the moment this list grows, so
// adding one is a deliberate, reviewed act.
var keywords = map[string]bool{
	"namespace":   true,
	"description": true,
	"type":        true,
	"op":          true,
	"deprecated":  true,
	"untyped":     true,
	"key":         true,
	"target":      true,
}

// isKeyword reports whether s is a reserved word (see keywords).
func isKeyword(s string) bool {
	return keywords[s]
}

// IsKeyword reports whether s is one of writ.schema's reserved words (see
// keywords): the exported mirror of isKeyword, so a caller outside this
// package — cmd/writ's `writ init`, deriving a namespace that must not
// collide with the grammar, is the one today — tests a name against the
// same closed table TestKeywordsAreClosed pins, rather than keeping its
// own copy that can silently drift from it.
func IsKeyword(s string) bool {
	return isKeyword(s)
}

// lexer scans writ.schema source into a flat token slice. Positions are
// 1-based line and 1-based column, counted in Unicode code points
// (spec/value-types.md §Length units).
type lexer struct {
	name string
	src  string
	pos  int // byte offset into src
	line int
	col  int // code point column of pos
}

// utf8BOM is the UTF-8 byte-order mark (U+FEFF encoded as EF BB BF). A
// leading one is stripped before lexing begins (spec/schema-source.md
// §1.1): it is a file-level marker some editors add on save, not source
// text, so a source file that opens with one lexes exactly as the same
// file without it. A BOM anywhere else in the file is just the character
// U+FEFF and is rejected like any other character with no token
// production (§2).
const utf8BOM = "\uFEFF"

func newLexer(name string, src []byte) *lexer {
	s := string(src)
	s = strings.TrimPrefix(s, utf8BOM)
	return &lexer{name: name, src: s, line: 1, col: 1}
}

// lexAll scans the entire source into tokens, or returns the first
// SyntaxError encountered. It never panics: every branch below either
// consumes at least one byte or returns an error, so a malformed or
// truncated multi-byte sequence cannot loop forever (exercised by
// FuzzParse).
func lexAll(name string, src []byte) ([]token, error) {
	l := newLexer(name, src)
	var toks []token
	for {
		tok, err := l.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, tok)
		if tok.Kind == tokEOF {
			return toks, nil
		}
	}
}

func (l *lexer) errf(pos Position, format string, args ...any) error {
	return &SyntaxError{File: l.name, Line: pos.Line, Col: pos.Col, Msg: fmt.Sprintf(format, args...)}
}

// peekRune returns the rune at the current position without consuming it,
// along with its byte width. It returns (utf8.RuneError, 0) at end of
// input, and (utf8.RuneError, 1) at a byte that is not valid UTF-8 (per
// utf8.DecodeRuneInString) — the size distinguishes the two, since a
// legitimately-encoded U+FFFD decodes with size 3. Every caller that
// walks runes one at a time — next, lexComment, lexString — checks
// invalidUTF8 before consuming or accumulating one, so a malformed byte
// is a lexical error everywhere, not silently repaired in some positions
// and rejected in others (spec/schema-source.md §1.1).
func (l *lexer) peekRune() (rune, int) {
	if l.pos >= len(l.src) {
		return utf8.RuneError, 0
	}
	r, size := utf8.DecodeRuneInString(l.src[l.pos:])
	return r, size
}

// invalidUTF8 reports whether r, size — as returned by peekRune — is the
// sentinel for a byte that is not valid UTF-8, as opposed to end of input
// (size 0) or a legitimately-encoded rune, including U+FFFD itself
// (size > 1).
func invalidUTF8(r rune, size int) bool {
	return r == utf8.RuneError && size == 1
}

// advance consumes one rune, updating line/col.
func (l *lexer) advance() (rune, int) {
	r, size := l.peekRune()
	if size == 0 {
		return r, 0
	}
	l.pos += size
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r, size
}

func isIdentStart(r rune) bool {
	return r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z')
}

func isIdentCont(r rune) bool {
	return isIdentStart(r) || r == '-' || ('0' <= r && r <= '9')
}

func isDigit(r rune) bool {
	return '0' <= r && r <= '9'
}

// next scans and returns the next token, skipping whitespace.
func (l *lexer) next() (token, error) {
	for {
		r, size := l.peekRune()
		if size == 0 {
			return token{Kind: tokEOF, Pos: Position{Line: l.line, Col: l.col}}, nil
		}
		if invalidUTF8(r, size) {
			return token{}, l.errf(Position{Line: l.line, Col: l.col}, "invalid UTF-8 encoding")
		}
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			l.advance()
			continue
		}
		break
	}

	start := Position{Line: l.line, Col: l.col}
	r, _ := l.peekRune()

	switch {
	case r == '#':
		return l.lexComment(start)
	case r == '"':
		return l.lexString(start)
	case isDigit(r):
		return l.lexNumber(start)
	case isIdentStart(r):
		return l.lexIdent(start)
	case r == '{':
		l.advance()
		return token{Kind: tokLBrace, Text: "{", Pos: start}, nil
	case r == '}':
		l.advance()
		return token{Kind: tokRBrace, Text: "}", Pos: start}, nil
	case r == '(':
		l.advance()
		return token{Kind: tokLParen, Text: "(", Pos: start}, nil
	case r == ')':
		l.advance()
		return token{Kind: tokRParen, Text: ")", Pos: start}, nil
	case r == '[':
		l.advance()
		return token{Kind: tokLBracket, Text: "[", Pos: start}, nil
	case r == ']':
		l.advance()
		return token{Kind: tokRBracket, Text: "]", Pos: start}, nil
	case r == ',':
		l.advance()
		return token{Kind: tokComma, Text: ",", Pos: start}, nil
	default:
		l.advance()
		return token{}, l.errf(start, "unexpected character %q", r)
	}
}

func (l *lexer) lexComment(start Position) (token, error) {
	l.advance() // '#'
	var b strings.Builder
	for {
		r, size := l.peekRune()
		if size == 0 || r == '\n' {
			break
		}
		// A comment body is still source text, not free-form bytes: an
		// invalid byte here is a lexical error like anywhere else, never
		// a silent U+FFFD substitution (round-3 review of WRIT-187 PR
		// #157, finding 1) — Format rewrites the file in place, so a
		// repaired byte would be unrecoverable.
		if invalidUTF8(r, size) {
			return token{}, l.errf(Position{Line: l.line, Col: l.col}, "invalid UTF-8 encoding")
		}
		l.advance()
		b.WriteRune(r)
	}
	text := strings.TrimSpace(b.String())
	return token{Kind: tokComment, Text: text, Pos: start}, nil
}

func (l *lexer) lexString(start Position) (token, error) {
	l.advance() // opening quote
	var b strings.Builder
	for {
		r, size := l.peekRune()
		if size == 0 {
			return token{}, l.errf(start, "unterminated string literal")
		}
		// See lexComment: a string literal's bytes get the same lexical
		// error, not a silent replacement-character rewrite.
		if invalidUTF8(r, size) {
			return token{}, l.errf(Position{Line: l.line, Col: l.col}, "invalid UTF-8 encoding")
		}
		if r == '\n' {
			return token{}, l.errf(start, "unterminated string literal (newline before closing quote)")
		}
		if r == '"' {
			l.advance()
			return token{Kind: tokString, Text: b.String(), Pos: start}, nil
		}
		if r == '\\' {
			l.advance()
			esc, esize := l.peekRune()
			if esize == 0 {
				return token{}, l.errf(start, "unterminated string literal")
			}
			if invalidUTF8(esc, esize) {
				return token{}, l.errf(Position{Line: l.line, Col: l.col}, "invalid UTF-8 encoding")
			}
			l.advance()
			switch esc {
			case '"':
				b.WriteRune('"')
			case '\\':
				b.WriteRune('\\')
			case 'n':
				b.WriteRune('\n')
			case 't':
				b.WriteRune('\t')
			default:
				return token{}, l.errf(Position{Line: l.line, Col: l.col}, "unknown escape sequence \\%c", esc)
			}
			continue
		}
		l.advance()
		b.WriteRune(r)
	}
}

func (l *lexer) lexNumber(start Position) (token, error) {
	var b strings.Builder
	for {
		r, size := l.peekRune()
		if size == 0 || !isDigit(r) {
			break
		}
		l.advance()
		b.WriteRune(r)
	}
	// A number immediately followed by an identifier character (e.g. "1a")
	// is not a separate token pair; it is a malformed literal, rejected
	// here rather than silently lexing "1" then "a".
	if r, _ := l.peekRune(); isIdentStart(r) {
		return token{}, l.errf(start, "malformed number literal")
	}
	return token{Kind: tokNumber, Text: b.String(), Pos: start}, nil
}

func (l *lexer) lexIdent(start Position) (token, error) {
	var b strings.Builder
	for {
		r, size := l.peekRune()
		if size == 0 || !isIdentCont(r) {
			break
		}
		l.advance()
		b.WriteRune(r)
	}
	return token{Kind: tokIdent, Text: b.String(), Pos: start}, nil
}
