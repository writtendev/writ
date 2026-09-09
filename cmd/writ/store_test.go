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

// TestValidSortOrders_AcceptedByParseOrderBy pins the direction the two
// tests above don't: every key validSortOrders advertises in the refusal
// message must itself round-trip through parseOrderBy without error, and
// resolve to a distinct, non-empty writ.OrderBy. Without this, a key added
// to validSortOrders alone — a typo, or a key named before its switch case
// lands — would ship a refusal advertising a --sort spelling parseOrderBy
// then rejects, with TestParseOrderBy_Valid and TestParseOrderBy_Invalid
// both still green.
func TestValidSortOrders_AcceptedByParseOrderBy(t *testing.T) {
	seen := make(map[writ.OrderBy]string, len(validSortOrders))
	for _, key := range validSortOrders {
		got, err := parseOrderBy(key)
		if err != nil {
			t.Errorf("validSortOrders names %q, which parseOrderBy rejects: %v", key, err)
			continue
		}
		if got == "" {
			t.Errorf("parseOrderBy(%q) returned an empty writ.OrderBy", key)
			continue
		}
		if other, ok := seen[got]; ok {
			t.Errorf("parseOrderBy(%q) and parseOrderBy(%q) both resolve to %q", key, other, got)
		}
		seen[got] = key
	}

	// Two further hand-maintained copies of the same four keys: the
	// objectListCmd flagSpec's Values (shown in generated help/completion)
	// and the -sort flag's usage string. Both are unexported package
	// symbols already reachable from this test with no plumbing, so pin
	// them here too rather than let a fourth copy drift unnoticed.
	var listValues []string
	for _, spec := range objectListCmd.Flags {
		if spec.Name == "sort" {
			listValues = spec.Values
		}
	}
	if strings.Join(listValues, ",") != strings.Join(validSortOrders, ",") {
		t.Errorf("objectListCmd's sort flag Values = %v, want validSortOrders %v", listValues, validSortOrders)
	}

	fs, _ := newObjectListFlagSet("")
	usage := fs.Lookup("sort").Usage
	for _, key := range validSortOrders {
		if !strings.Contains(usage, key) {
			t.Errorf("-sort usage string does not name accepted key %q: %q", key, usage)
		}
	}
}
