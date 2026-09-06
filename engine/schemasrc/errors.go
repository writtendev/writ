package schemasrc

import (
	"strconv"
	"strings"
)

// SyntaxError is one parse failure, carrying the line and column a human
// editing writ.schema by hand needs to find it (1-based, counted in
// Unicode code points, per spec/value-types.md §Length units).
type SyntaxError struct {
	File string
	Line int
	Col  int
	Msg  string
}

func (e *SyntaxError) Error() string {
	return e.File + ":" + strconv.Itoa(e.Line) + ":" + strconv.Itoa(e.Col) + ": " + e.Msg
}

// ErrorList collects every SyntaxError a parse produced, so a hand-edited
// file reports more than its first mistake.
type ErrorList []*SyntaxError

func (errs ErrorList) Error() string {
	var b strings.Builder
	for i, e := range errs {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(e.Error())
	}
	return b.String()
}
