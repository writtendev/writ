package schemasrc_test

import (
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/schemasrc"
)

// TestValidateNamespace pins ValidateNamespace against the exact grammar
// a `namespace <name>` declaration enforces at parse time (parse.go's
// validateName, in the namespace slot): namespacePattern, maxNameLength,
// and the full keywords table. It exists as an exported entry point for a
// caller that has a namespace string before there is any writ.schema
// source to parse it from — `writ init`, resolving a namespace a human
// supplies for the starter file it is about to write, is the one today
// (WRIT-220) — and this test is the guarantee that caller gets the same
// verdict Parse would.
func TestValidateNamespace(t *testing.T) {
	for _, name := range []string{
		"acme",
		"a",
		"a1",
		"acme-widgets",
		strings.Repeat("a", 64),
	} {
		t.Run("accept/"+name, func(t *testing.T) {
			if err := schemasrc.ValidateNamespace(name); err != nil {
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
			if err := schemasrc.ValidateNamespace(tc.name); err == nil {
				t.Errorf("ValidateNamespace(%q) = nil, want an error (%s)", tc.name, tc.reason)
			}
		})
	}
}
