package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/writtendev/writ/engine"
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
// escaped (strconv.Quote escapes every code point writ.EscapeForbidden
// does), so it costs nothing where there is nothing to escape.
//
// Escaping inside the engine instead would be the wrong place: that error
// text is the public API's, shared with callers that are not a terminal.
// This is writ's own rendering, which is where WRIT-226 puts the escape.
func renderErr(w io.Writer, err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintln(w, escapeErrReport(errLine(err), subprocessFailure(err)))
	if storeOpenFailure(err) {
		return 5
	}
	return 1
}

// storeOpenFailure reports whether err's chain is one of the sentinels
// writ.Open wraps every store-open failure in: path resolution, git
// storage, DAG store, projection cache directory or database, and sync
// client (writ.ErrStoreOpen), or "not inside a git repository" specifically
// (writ.ErrNotRepository, which ErrStoreOpen itself does not subsume --
// ResolveGitDir and Open both wrap it directly). It does not cover schema
// resolution/apply failures during Open -- those are log-content errors,
// not open failures, and stay at exit 1 like any other rejection.
//
// renderErr uses this so every verb -- not just sync, which classified this
// itself before this change -- returns the exit 5 docs/cli-json.md §2.5
// promises for "not a git repository / store cannot be opened". cmd/writ/
// sync.go's exitCodeFor calls this same function so the two definitions
// cannot drift apart.
func storeOpenFailure(err error) bool {
	return errors.Is(err, writ.ErrStoreOpen) || errors.Is(err, writ.ErrNotRepository)
}

// subprocessFailure reports whether err's chain carries the failure of a
// subprocess writ ran -- the one condition under which a U+000A in the
// assembled report is structure one of writ's own format strings wrote,
// rather than data a string interpolated into it happens to contain.
//
// engine/codec/sign.go's `fmt.Errorf("codec: ssh-keygen -Y sign: %w\n%s",
// err, strings.TrimSpace(string(out)))` is the only error format string in
// the tree that writes a U+000A as structure (grep every Errorf and
// errors.New: the one other hit is spec/fixtures' test harness, which never
// reaches a CLI), and its %w is whatever os/exec returned -- *exec.ExitError
// when ssh-keygen ran and exited non-zero, *exec.Error when it could not be
// started at all.
//
// A hostile peer cannot get either type into an error chain: log-sourced
// text reaches renderErr through fold, schema validation and codec, none of
// which run a subprocess. So this distinguishes writ's line structure from a
// peer's bytes without having to tell them apart inside the assembled
// string, which is the thing cmd/writ cannot do.
func subprocessFailure(err error) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return true
	}
	var execErr *exec.Error
	return errors.As(err, &execErr)
}

// escapeErrReport returns line -- errLine's assembled error report -- with
// every forbidden code point (writ.EscapeForbidden's table,
// spec/identifiers.md §Value character repertoire) escaped as \uXXXX.
// U+000A is escaped with the rest unless keepLineBreaks, which renderErr sets only for the
// reports subprocessFailure identifies: there, and only there, a U+000A in
// the report is a break writ's own format string wrote around a failed
// subprocess's diagnostic.
//
// Both halves of that are load-bearing, and each was a round of review.
//
// Escaping U+000A unconditionally -- as this did when it was a plain
// writ.EscapeForbidden over the assembled line, the C0 range it covers
// including U+000A -- flattens engine/codec/sign.go's ssh-keygen failure onto
// one line with a literal escape where the break belonged, losing the
// diagnostic that capturing the subprocess's combined output exists to
// surface. A missing, unreadable or passphrase-protected signing key is a
// first-run misconfiguration, not an exotic state, so that was a regression
// against main on a common, non-hostile path (round 4 review of PR #195).
//
// Sparing U+000A unconditionally -- the round-4 fix, shaped after
// escapeRenderedSchemaSource in schema.go -- leaves a log-sourced U+000A
// unneutralised, and that is not the small concession round 4 recorded it
// as. A fetched define-field's enum member is not gated by anything on the
// read path (spec.ValidateFieldRule gates the field, target and key columns
// against identifierGrammar and value_type/strategy against their closed
// catalogues, and never looks at the members), and
// engine/internal/value.Check formats the declared list into its membership
// error with a bare %v. A member with a U+000A at *both* ends of its payload
// therefore puts the format string's trailing text on a line of its own and
// leaves the peer with a whole stderr line of its choosing -- byte for byte,
// convincing "writ: " prefix and all, indistinguishable from writ's own
// diagnostics. That is the spoofing class WRIT-226 exists to close, not a
// weaker cousin of it (round 5 review of PR #195, correcting round 4's
// claim that such a line still carries the format's trailing bracket).
//
// Conditioning on the error rather than on the code point gets both:
// sign.go's break survives, every log-sourced break is escaped, and the two
// are told apart by something a peer cannot forge -- see subprocessFailure.
// U+0009 is escaped throughout: no error format string in the tree writes a
// tab as structure, so escaping tabs mangles nothing.
//
// Escaping inside the engine instead was never the alternative: that error
// text is the public API's, shared with callers that are not a terminal.
// This is writ's own rendering, which is where WRIT-226 puts the escape.
func escapeErrReport(line string, keepLineBreaks bool) string {
	if keepLineBreaks {
		return writ.EscapeForbiddenKeepingNewlines(line)
	}
	return writ.EscapeForbidden(line)
}

// errLine is the error report renderErr prints for err, unescaped. Its arms
// return one line each, but the message an arm wraps need not be one line:
// engine/codec/sign.go's ssh-keygen failure carries its diagnostic on a
// second, and a log-sourced string can carry U+000A of its own -- which is
// why renderErr escapes what comes back rather than trusting it (see
// escapeErrReport).
func errLine(err error) string {
	var cfgErr *writ.ConfigError
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
// error message when the caller passes something else.
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
	case "created_at_asc":
		return writ.OrderByCreatedAtAsc, nil
	case "created_at_desc":
		return writ.OrderByCreatedAtDesc, nil
	case "updated_at_asc":
		return writ.OrderByUpdatedAtAsc, nil
	case "updated_at_desc":
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
