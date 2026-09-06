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
// (spec/schema-source.md §The fence). It is a structural fence, not a
// stylistic one: these words cannot be used as a field, type, or op name,
// which is what keeps a field line's grammar unambiguous without an
// expression grammar to disambiguate it. TestKeywordsAreClosed fails by
// name the moment this list grows, so adding one is a deliberate,
// reviewed act.
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

func newLexer(name string, src []byte) *lexer {
	return &lexer{name: name, src: string(src), line: 1, col: 1}
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
// along with its byte width. It returns (utf8.RuneError, 0) at end of input.
func (l *lexer) peekRune() (rune, int) {
	if l.pos >= len(l.src) {
		return utf8.RuneError, 0
	}
	r, size := utf8.DecodeRuneInString(l.src[l.pos:])
	return r, size
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
