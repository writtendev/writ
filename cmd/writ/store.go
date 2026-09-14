package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/internal/textsafe"
)

type notFoundError struct {
	kind string
	id   string
}

func (e notFoundError) Error() string {
	kind := e.kind
	if kind == "" {
		kind = "object"
	}
	return fmt.Sprintf("no %s with id %s", kind, e.id)
}

func (e notFoundError) Unwrap() error {
	return writ.ErrNotFound
}

func openStore(dir string, opts ...writ.Option) (*writ.Store, error) {
	if dir == "" {
		dir = "."
	}
	return writ.Open(dir, opts...)
}

// renderErr prints err as the CLI's human error report and returns the
// exit code that goes with it.
//
// The report is escaped by escapeErrReport on its way out -- once, here,
// rather than at each of errLine's arms or at each error construction
// upstream. An engine error's text can carry a log-sourced string that
// nothing on the read path gates: the reachable case is a fetched
// define-field's body `enum` members, which spec.ValidateFieldRule never
// constrains (it gates the field, target and key columns against
// identifierGrammar and value_type/strategy against their closed
// catalogues, and stops there) and which engine/internal/value.Check then
// formats into its membership error with a bare %v. renderErr is the one
// place an engine error reaches a human's stderr, so escaping here covers
// that whole class instead of one message of it, and cannot be forgotten
// by the next error message added anywhere below it. It is a no-op on the
// fixed strings errLine returns and on any span Go's %q has already
// escaped (strconv.Quote escapes every textsafe.Forbidden rune), so it
// costs nothing where there is nothing to escape.
//
// Escaping inside the engine instead would be the wrong place: that error
// text is the public API's, shared with callers that are not a terminal.
// This is writ's own rendering, which is where WRIT-226 puts the escape.
func renderErr(w io.Writer, err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintln(w, escapeErrReport(errLine(err)))
	return 1
}

// escapeErrReport returns line -- errLine's assembled error report -- with
// every textsafe.Forbidden code point escaped as \uXXXX, except U+000A.
// Same shape, and the same reason, as escapeRenderedSchemaSource in
// schema.go: the exclusion exists to preserve the line structure writ's own
// format strings write, not to spare any code point in the data.
//
// Exactly one error in the tree writes a U+000A as structure --
// engine/codec/sign.go's `fmt.Errorf("codec: ssh-keygen -Y sign: %w\n%s",
// err, ...)`, whose second line is ssh-keygen's own diagnostic and the
// entire point of capturing its combined output. A missing, unreadable or
// passphrase-protected signing key is a first-run misconfiguration, not an
// exotic state, so escaping that break -- as the whole-line escape did
// before this exclusion -- flattens a common, non-hostile diagnostic onto
// one line for no security gain. U+0009 is not excluded with it: no error
// format string in the tree writes a tab as structure, so escaping tabs
// mangles nothing, and the exclusion stays exactly as wide as the structure
// that justifies it (round 4 review of PR #195).
//
// The trade-off this reopens, stated plainly rather than left implicit: a
// U+000A in a log-sourced string reaching an error report is no longer
// neutralised, so a hostile peer can forge an extra line on stderr. That is
// reachable today by the same route the escape was added for, and was
// confirmed by execution rather than assumed: a fetched define-field whose
// enum member is "closed\nwrit: forged line" prints a second stderr line
// out of value.Check's membership error. The forged line still carries
// whatever the surrounding format string puts after the interpolation (the
// closing bracket of %v's slice, here), so it is not fully attacker-chosen
// -- but a break is a break.
//
// It is accepted here because the alternative is worse for its size.
// Escaping the interpolation rather than the line would have to happen
// where the message is built, and the reachable message is built inside
// engine/internal/value, whose error text is the public API's and shared
// with callers that are not a terminal; cmd/writ receives it already
// assembled and cannot tell the peer's bytes from the engine's. So
// neutralising the forgery would mean changing engine error text for every
// caller, which is a different change from this one. A forged stderr line
// is also a weaker primitive than the bidi override this escape is for: it
// can add a line, not silently reverse what a line already says.
func escapeErrReport(line string) string {
	hasForbidden := strings.ContainsFunc(line, func(r rune) bool {
		return r != '\n' && textsafe.Forbidden(r)
	})
	if !hasForbidden {
		return line
	}
	var b strings.Builder
	b.Grow(len(line))
	for _, r := range line {
		if r == '\n' || !textsafe.Forbidden(r) {
			b.WriteRune(r)
			continue
		}
		fmt.Fprintf(&b, `\u%04x`, r)
	}
	return b.String()
}

// errLine is the error report renderErr prints for err, unescaped. It is one
// line for every error but engine/codec/sign.go's ssh-keygen failure, which
// carries its diagnostic on a second (see escapeErrReport).
func errLine(err error) string {
	var cfgErr *identity.ConfigError
	if errors.As(err, &cfgErr) {
		return fmt.Sprintf("writ: %v", cfgErr)
	}

	if errors.Is(err, writ.ErrNoIdentity) {
		return "writ: no writer identity configured (run 'writ init' to configure)"
	}

	if errors.Is(err, writ.ErrNoSigningKey) {
		return "writ: no signing key configured (run 'writ init' to configure)"
	}

	if errors.Is(err, writ.ErrNotFound) {
		var nf notFoundError
		if errors.As(err, &nf) {
			return "writ: " + nf.Error()
		}
	}

	msg := err.Error()
	if !strings.HasPrefix(msg, "writ: ") {
		msg = "writ: " + msg
	}
	return msg
}

// validSortOrders lists parseOrderBy's canonical --sort keys, for use in its
// error message when the caller passes something else. Each also accepts a
// handful of shorthand aliases (e.g. "created-asc", "created"); the message
// names only the canonical form so it stays readable.
var validSortOrders = []string{
	"created_at_asc",
	"created_at_desc",
	"updated_at_asc",
	"updated_at_desc",
}

func parseOrderBy(sortOrder string) (writ.OrderBy, error) {
	if sortOrder == "" {
		return "", nil
	}
	switch sortOrder {
	case "created_at_asc", "created-asc", "created_asc", "created":
		return writ.OrderByCreatedAtAsc, nil
	case "created_at_desc", "created-desc", "created_desc":
		return writ.OrderByCreatedAtDesc, nil
	case "updated_at_asc", "updated-asc", "updated_asc":
		return writ.OrderByUpdatedAtAsc, nil
	case "updated_at_desc", "updated-desc", "updated_desc", "updated":
		return writ.OrderByUpdatedAtDesc, nil
	default:
		return "", fmt.Errorf("invalid sort order %q (accepted: %s)", sortOrder, strings.Join(validSortOrders, ", "))
	}
}

// resolveObjectID resolves an object ID or unambiguous prefix to a full
// object ID, for the generic `writ object`/`writ schema` commands that work
// across every schema-declared type rather than one typed collection.
//
// A full 32-hex-character id passes straight through, unresolved: it goes
// directly to Objects.Get, which folds from the DAG and needs the exact id,
// not a prefix. Anything shorter is resolved through
// Query.Objects{IncludeDeleted: true} instead: prefix matching needs an
// index to search, and the projection is that index. This is now the only
// id-resolving helper in this file.
func resolveObjectID(ctx context.Context, store *writ.Store, prefix string) (string, error) {
	if prefix == "" {
		return "", fmt.Errorf("object ID required")
	}

	if len(prefix) == 32 {
		if _, err := hex.DecodeString(prefix); err == nil {
			return prefix, nil
		}
	}

	objects, err := store.Query.Objects(writ.ObjectFilter{IncludeDeleted: true})
	if err != nil {
		return "", err
	}

	var matches []string
	for _, o := range objects {
		if strings.HasPrefix(o.ObjectID, prefix) {
			matches = append(matches, o.ObjectID)
		}
	}

	if len(matches) == 0 {
		return "", notFoundError{kind: "object", id: prefix}
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("ambiguous object ID prefix %q matches %d objects (%s)", prefix, len(matches), strings.Join(matches, ", "))
	}

	return matches[0], nil
}
