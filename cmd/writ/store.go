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

func renderErr(w io.Writer, err error) int {
	if err == nil {
		return 0
	}

	var cfgErr *identity.ConfigError
	if errors.As(err, &cfgErr) {
		fmt.Fprintf(w, "writ: %v\n", cfgErr)
		return 1
	}

	if errors.Is(err, writ.ErrNoIdentity) {
		fmt.Fprintln(w, "writ: no writer identity configured (run 'writ init' to configure)")
		return 1
	}

	if errors.Is(err, writ.ErrNoSigningKey) {
		fmt.Fprintln(w, "writ: no signing key configured (run 'writ init' to configure)")
		return 1
	}

	if errors.Is(err, writ.ErrNotFound) {
		var nf notFoundError
		if errors.As(err, &nf) {
			fmt.Fprintf(w, "writ: %s\n", nf.Error())
			return 1
		}
	}

	msg := err.Error()
	if !strings.HasPrefix(msg, "writ: ") {
		msg = "writ: " + msg
	}
	fmt.Fprintln(w, msg)
	return 1
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
		return "", fmt.Errorf("invalid sort order %q", sortOrder)
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
