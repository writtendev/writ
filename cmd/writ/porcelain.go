package main

import (
	"fmt"
	"io"

	"github.com/writtendev/writ/internal/textsafe"
)

// porcelainf formats according to a format specifier and writes to w. Any
// string arguments are funneled through textsafe.EscapeForbidden so that
// unescaped foreign-sourced strings cannot reach human-readable CLI views.
func porcelainf(w io.Writer, format string, args ...any) {
	escaped := make([]any, len(args))
	for i, arg := range args {
		if s, ok := arg.(string); ok {
			escaped[i] = textsafe.EscapeForbidden(s)
		} else {
			escaped[i] = arg
		}
	}
	_, _ = fmt.Fprintf(w, format, escaped...)
}

// porcelainln formats using the default formats for its operands and writes to
// w. Any string arguments are funneled through textsafe.EscapeForbidden so that
// unescaped foreign-sourced strings cannot reach human-readable CLI views.
func porcelainln(w io.Writer, args ...any) {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(w)
		return
	}
	escaped := make([]any, len(args))
	for i, arg := range args {
		if s, ok := arg.(string); ok {
			escaped[i] = textsafe.EscapeForbidden(s)
		} else {
			escaped[i] = arg
		}
	}
	_, _ = fmt.Fprintln(w, escaped...)
}
