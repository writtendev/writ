package writ

import (
	"strings"

	"github.com/writtendev/writ/internal/textsafe"
)

// EscapeForbidden returns s with every textsafe.Forbidden code point --
// bidi and zero-width code points a hostile peer could use to make
// displayed text differ from stored text (spec/identifiers.md §Rendering a
// person identifier) -- replaced by its JSON \uXXXX escape. It is the one
// call cmd/writ's porcelain and --json rendering (porcelainf/porcelainln,
// emitJSON, fieldDisplay, authorDisplay, declaredTypeNames, and
// describeSchemaConflict) share, so every human- and machine-readable
// surface applies the identical table -- see internal/textsafe's package
// doc for why that agreement matters.
func EscapeForbidden(s string) string {
	return textsafe.EscapeForbidden(s)
}

// EscapeForbiddenKeepingNewlines is EscapeForbidden except that U+000A
// passes through unescaped. It exists for the two renderers whose own
// output uses U+000A as line structure rather than as data:
// cmd/writ's escapeErrReport, only when the error it is escaping carries a
// failed subprocess's diagnostic (so ssh-keygen's own line breaks survive),
// and cmd/writ's escapeRenderedSchemaSource, which always spares it so
// schemasrc.Render's line-per-declaration output keeps its shape in the
// human-readable `writ schema plan` diff.
func EscapeForbiddenKeepingNewlines(s string) string {
	escapes := func(r rune) bool {
		return r != '\n' && textsafe.Forbidden(r)
	}
	if !strings.ContainsFunc(s, escapes) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !escapes(r) {
			b.WriteRune(r)
			continue
		}
		textsafe.EscapeRune(&b, r)
	}
	return b.String()
}
