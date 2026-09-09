package main

import (
	"strings"
	"testing"

	"github.com/writtendev/writ/engine"
)

// TestParseOrderBy_Valid pins the accepted --sort keys, canonical and alias
// forms alike, against the writ.OrderBy constants they resolve to.
func TestParseOrderBy_Valid(t *testing.T) {
	cases := map[string]writ.OrderBy{
		"":                "",
		"created_at_asc":  writ.OrderByCreatedAtAsc,
		"created-asc":     writ.OrderByCreatedAtAsc,
		"created_asc":     writ.OrderByCreatedAtAsc,
		"created":         writ.OrderByCreatedAtAsc,
		"created_at_desc": writ.OrderByCreatedAtDesc,
		"created-desc":    writ.OrderByCreatedAtDesc,
		"created_desc":    writ.OrderByCreatedAtDesc,
		"updated_at_asc":  writ.OrderByUpdatedAtAsc,
		"updated-asc":     writ.OrderByUpdatedAtAsc,
		"updated_asc":     writ.OrderByUpdatedAtAsc,
		"updated_at_desc": writ.OrderByUpdatedAtDesc,
		"updated-desc":    writ.OrderByUpdatedAtDesc,
		"updated_desc":    writ.OrderByUpdatedAtDesc,
		"updated":         writ.OrderByUpdatedAtDesc,
	}
	for in, want := range cases {
		got, err := parseOrderBy(in)
		if err != nil {
			t.Errorf("parseOrderBy(%q) returned error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseOrderBy(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestParseOrderBy_Invalid pins the WRIT-208 regression: an unknown --sort
// key must still be refused (the silent-acceptance bug WRIT-195 fixed by
// deleting the title/priority/position/estimate keys), and the refusal must
// name the keys parseOrderBy does accept rather than a bare "invalid sort
// order" that names nothing.
func TestParseOrderBy_Invalid(t *testing.T) {
	for _, in := range []string{"title", "priority", "bogus"} {
		_, err := parseOrderBy(in)
		if err == nil {
			t.Fatalf("parseOrderBy(%q): expected an error, got nil", in)
		}
		if !strings.Contains(err.Error(), in) {
			t.Errorf("parseOrderBy(%q) error does not name the rejected key: %q", in, err.Error())
		}
		for _, key := range validSortOrders {
			if !strings.Contains(err.Error(), key) {
				t.Errorf("parseOrderBy(%q) error does not name accepted key %q: %q", in, key, err.Error())
			}
		}
	}
}
