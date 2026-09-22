package writ_test

import (
	"strings"
	"testing"

	writ "github.com/writtendev/writ/engine"
)

// TestValidateNamespace exercises writ.ValidateNamespace directly, the
// wrapper's only guard against the symbol going stale: it forwards
// verbatim to schemasrc.ValidateNamespace (whose own grammar coverage
// lives in internal/schemasrc), so this covers the wrapper's contract —
// accept, and reject each of the ways spec/schema-ops.md §2 lets a
// namespace fail — rather than re-deriving the grammar.
func TestValidateNamespace(t *testing.T) {
	for _, name := range []string{
		"acme",
		"a",
		"a1",
		"acme-widgets",
		strings.Repeat("a", 64),
	} {
		t.Run("accept/"+name, func(t *testing.T) {
			if err := writ.ValidateNamespace(name); err != nil {
				t.Errorf("ValidateNamespace(%q) = %v, want nil", name, err)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		reason string
	}{
		{"Api", "uppercase"},
		{"9x", "leading digit"},
		{"type", "reserved keyword"},
		{"namespace", "reserved keyword"},
		{"a b", "embedded space"},
		{"acme\ntype x {}", "embedded newline"},
		{strings.Repeat("a", 65), "over the length limit"},
		{"", "empty"},
	} {
		t.Run("reject/"+tc.reason, func(t *testing.T) {
			if err := writ.ValidateNamespace(tc.name); err == nil {
				t.Errorf("ValidateNamespace(%q) = nil, want an error (%s)", tc.name, tc.reason)
			}
		})
	}
}
