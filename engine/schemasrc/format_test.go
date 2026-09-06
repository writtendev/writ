package schemasrc_test

import (
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/schemasrc"
)

// TestFormatPreservesCommentsInEveryPosition exercises comment placements
// the golden corpus does not: between `namespace` and `description`,
// between a type's `{` and its own `description`, and between an op
// block's `{` and its own `description`. None of these has a dedicated
// AST slot (schemasrc.File/Type/OpBlock only carry one LeadingComments
// list each), so the parser folds a comment found there into the
// enclosing node's leading comments rather than dropping it or
// mis-parsing what follows — this test is the regression net for that.
func TestFormatPreservesCommentsInEveryPosition(t *testing.T) {
	src := `namespace acme
# between namespace and description
description "d"

# before a type
type widget {
  # between type-open and its description
  description "a widget"

  # before an op block
  op create 1 {
    # between op-open and its description
    description "creates one"
    name string lww  # trailing on a field
  }
}
`
	out, err := schemasrc.Format("test.schema", []byte(src))
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	for _, want := range []string{
		"between namespace and description",
		"before a type",
		"between type-open and its description",
		"before an op block",
		"between op-open and its description",
		"trailing on a field",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("Format dropped comment %q; got:\n%s", want, out)
		}
	}

	out2, err := schemasrc.Format("test.schema", out)
	if err != nil {
		t.Fatalf("Format(Format(src)): %v", err)
	}
	if string(out) != string(out2) {
		t.Errorf("Format is not idempotent on its own output:\n--- first ---\n%s\n--- second ---\n%s", out, out2)
	}
}

// TestFormatTrailingCommentStaysOnItsField asserts a same-line comment
// after a field is reprinted on that field's own line, not hoisted to
// the next declaration.
func TestFormatTrailingCommentStaysOnItsField(t *testing.T) {
	src := `namespace acme

type widget {
  op create 1 {
    a string lww  # comment for a
    b int    lww
  }
}
`
	out, err := schemasrc.Format("test.schema", []byte(src))
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	lines := strings.Split(string(out), "\n")
	found := false
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "a ") {
			if !strings.Contains(l, "# comment for a") {
				t.Errorf("field a's line lost its trailing comment: %q", l)
			}
			found = true
		}
		if strings.HasPrefix(strings.TrimSpace(l), "b ") && strings.Contains(l, "comment for a") {
			t.Errorf("field a's trailing comment leaked onto field b's line: %q", l)
		}
	}
	if !found {
		t.Fatalf("field a's line not found in output:\n%s", out)
	}
}
