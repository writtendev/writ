package schemasrc_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/schemasrc"
)

// commentPositions is the closed, explicit list of syntactic positions a
// comment can occupy in a writ.schema source file. It exists so a new
// syntactic position (a new declaration kind, a new modifier, a new
// description-bearing block) shows up here as a missing marker rather
// than silently going untested — the failure mode a test merely named
// "in every position" but covering a handful of them invites (round-1
// review of WRIT-187 PR #157, finding 1). Each entry names one position;
// srcCommentTag below turns the name into the unique marker text embedded
// in testFormatSrc, and every entry must appear in Format's output for
// the test to pass.
var commentPositions = []string{
	"leading-on-namespace",              // File.LeadingComments
	"trailing-on-namespace",             // File.NamespaceTrailingComment
	"between-namespace-and-description", // folded into File.LeadingComments
	"trailing-on-file-description",      // File.DescriptionTrailingComment
	"leading-on-type",                   // Type.LeadingComments
	"trailing-on-type-header",           // folded into Type.LeadingComments
	"leading-on-type-description",       // folded into Type.LeadingComments
	"trailing-on-type-description",      // Type.DescriptionTrailingComment
	"leading-on-op-block",               // OpBlock.LeadingComments
	"trailing-on-op-block-header",       // folded into OpBlock.LeadingComments
	"leading-on-op-block-description",   // folded into OpBlock.LeadingComments
	"trailing-on-op-block-description",  // OpBlock.DescriptionTrailingComment
	"leading-on-field",                  // Field.LeadingComments
	"trailing-on-field",                 // Field.TrailingComment
	"dangling-end-of-op-block",          // OpBlock.DanglingComments
	"dangling-end-of-type-body",         // Type.DanglingComments
	"dangling-end-of-file",              // File.TrailingComments
}

// srcCommentTag turns a commentPositions entry into the exact marker text
// testFormatSrc embeds and this test looks for in Format's output — one
// function so the two never drift apart.
func srcCommentTag(position string) string {
	return "marker: " + position
}

// bareCommentTag is the value-axis counterpart to srcCommentTag: every
// comment body is empty, i.e. every comment in the source is a bare `#`.
// commentPositions enumerates *where* a comment can appear; pairing it
// with this body generator instead of srcCommentTag's exercises *what* it
// can contain, at every position at once, structurally — a bare `#` at
// any of the 17 positions is a value-level case the position-only guard
// cannot see (round-2 review of WRIT-187 PR #157, finding 1).
func bareCommentTag(position string) string {
	return ""
}

// testFormatSrc is one source file placing a comment — tagged by body(pos)
// — in every position commentPositions names, exercising each of the AST
// slots noted above: a same-line trailing comment on `namespace`, on the
// file-level `description`, on a type's own `description`, and on an op
// block's own `description` (the five `Format` used to silently drop for
// a non-empty body before the round-1 fix, and for an empty body before
// the round-2 fix — see writeTrailingComment), plus a dangling comment
// before each of the three `}`-delimited block closings (op block, type
// body, end of file).
func testFormatSrc(body func(position string) string) string {
	var b strings.Builder
	tag := body
	fmt.Fprintf(&b, "# %s\n", tag("leading-on-namespace"))
	fmt.Fprintf(&b, "namespace acme  # %s\n", tag("trailing-on-namespace"))
	fmt.Fprintf(&b, "# %s\n", tag("between-namespace-and-description"))
	fmt.Fprintf(&b, "description \"d\"  # %s\n", tag("trailing-on-file-description"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "# %s\n", tag("leading-on-type"))
	fmt.Fprintf(&b, "type widget {  # %s\n", tag("trailing-on-type-header"))
	fmt.Fprintf(&b, "  # %s\n", tag("leading-on-type-description"))
	fmt.Fprintf(&b, "  description \"a widget\"  # %s\n", tag("trailing-on-type-description"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "  # %s\n", tag("leading-on-op-block"))
	fmt.Fprintf(&b, "  op create 1 {  # %s\n", tag("trailing-on-op-block-header"))
	fmt.Fprintf(&b, "    # %s\n", tag("leading-on-op-block-description"))
	fmt.Fprintf(&b, "    description \"creates one\"  # %s\n", tag("trailing-on-op-block-description"))
	fmt.Fprintf(&b, "    # %s\n", tag("leading-on-field"))
	fmt.Fprintf(&b, "    name string lww  # %s\n", tag("trailing-on-field"))
	fmt.Fprintf(&b, "    # %s\n", tag("dangling-end-of-op-block"))
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  # %s\n", tag("dangling-end-of-type-body"))
	b.WriteString("}\n")
	fmt.Fprintf(&b, "# %s\n", tag("dangling-end-of-file"))
	return b.String()
}

// TestFormatPreservesCommentsInEveryPosition is the regression net for
// round-1 finding 1 on WRIT-187 PR #157: Format silently dropped a
// same-line trailing comment on `namespace`, on the file-level
// `description`, on a type's own `description`, and on an op block's own
// `description`, plus any comment dangling before the closing `}` of an
// op block, a type body, or the file itself. Every position
// commentPositions names is asserted individually — by name, not by a
// single combined substring check — so a regression on any one slot fails
// with the position's own name rather than a generic diff.
func TestFormatPreservesCommentsInEveryPosition(t *testing.T) {
	src := testFormatSrc(srcCommentTag)

	out, err := schemasrc.Format("test.schema", []byte(src))
	if err != nil {
		t.Fatalf("Format: %v", err)
	}

	for _, position := range commentPositions {
		if !strings.Contains(string(out), srcCommentTag(position)) {
			t.Errorf("Format dropped the comment at position %q; got:\n%s", position, out)
		}
	}

	out2, err := schemasrc.Format("test.schema", out)
	if err != nil {
		t.Fatalf("Format(Format(src)): %v", err)
	}
	if string(out) != string(out2) {
		t.Errorf("Format is not idempotent on its own output:\n--- first ---\n%s\n--- second ---\n%s", out, out2)
	}
	for _, position := range commentPositions {
		if !strings.Contains(string(out2), srcCommentTag(position)) {
			t.Errorf("Format(Format(src)) dropped the comment at position %q; got:\n%s", position, out2)
		}
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

// TestFormatPreservesBareCommentsInEveryPosition is the value-axis
// regression net for round-2 finding 1b: writeTrailingComment used to
// read an empty comment body as "no comment" and drop the line's `#`
// entirely, at every one of the five trailing-comment slots (namespace,
// the three description levels, and a field). commentPositions' own
// tagged-marker test (above) cannot see this, because a bare `#` carries
// no marker text to look for — so this test checks the one thing that
// does distinguish "preserved" from "deleted" for an empty body: the `#`
// itself must still be there, once per position, both after one Format
// pass and after a second (idempotence).
func TestFormatPreservesBareCommentsInEveryPosition(t *testing.T) {
	src := testFormatSrc(bareCommentTag)
	if got := strings.Count(src, "#"); got != len(commentPositions) {
		t.Fatalf("test fixture itself has %d '#' markers, want %d (one per commentPositions entry) — fixture and list have drifted", got, len(commentPositions))
	}

	out, err := schemasrc.Format("test.schema", []byte(src))
	if err != nil {
		t.Fatalf("Format: %v", err)
	}
	if got := strings.Count(string(out), "#"); got != len(commentPositions) {
		t.Errorf("Format(bare-comment src) has %d '#' markers, want %d — a bare trailing comment was dropped", got, len(commentPositions))
	}

	out2, err := schemasrc.Format("test.schema", out)
	if err != nil {
		t.Fatalf("Format(Format(src)): %v", err)
	}
	if string(out) != string(out2) {
		t.Errorf("Format is not idempotent on bare-comment output:\n--- first ---\n%s\n--- second ---\n%s", out, out2)
	}
	if got := strings.Count(string(out2), "#"); got != len(commentPositions) {
		t.Errorf("Format(Format(bare-comment src)) has %d '#' markers, want %d", got, len(commentPositions))
	}
}

// TestFormatPreservesBareTrailingCommentAtEachSlot pins the same bug
// (finding 1b) with exact line content at each of the five slots that
// route through writeTrailingComment, rather than only an aggregate
// count — so a regression at one specific slot is named, not just
// counted.
func TestFormatPreservesBareTrailingCommentAtEachSlot(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantLine string
	}{
		{
			name:     "namespace",
			src:      "namespace acme  #\n",
			wantLine: "namespace acme  #",
		},
		{
			name:     "file description",
			src:      "namespace acme\ndescription \"d\"  #\n",
			wantLine: `description "d"  #`,
		},
		{
			name: "type description",
			src: `namespace acme

type widget {
  description "d"  #
}
`,
			wantLine: `  description "d"  #`,
		},
		{
			name: "op block description",
			src: `namespace acme

type widget {
  op create 1 {
    description "d"  #
    a string lww
  }
}
`,
			wantLine: `    description "d"  #`,
		},
		{
			name: "field",
			src: `namespace acme

type widget {
  op create 1 {
    a string lww  #
  }
}
`,
			wantLine: `    a string lww  #`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := schemasrc.Format("test.schema", []byte(tc.src))
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			found := false
			for _, l := range strings.Split(string(out), "\n") {
				if l == tc.wantLine {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Format dropped the bare trailing comment; want a line %q, got:\n%s", tc.wantLine, out)
			}

			out2, err := schemasrc.Format("test.schema", out)
			if err != nil {
				t.Fatalf("Format(Format(src)): %v", err)
			}
			if string(out) != string(out2) {
				t.Errorf("Format is not idempotent:\n--- first ---\n%s\n--- second ---\n%s", out, out2)
			}
		})
	}
}

// TestParseRejectsEmptyDescription pins round-2 finding 1a: an explicit
// `description ""` carries no data — it compiles to the same omitted wire
// field as no description line at all (spec/schema-source.md §5, §6) —
// so letting it through gave "no description" two spellings, and a
// same-line trailing comment on that line was silently discarded as a
// side effect of Format treating Description == "" as "line absent".
// Rejecting it at parse time closes the gap structurally: Description
// can no longer be "" for anything Parse accepted, so every
// Format/Compile/Render site that already tests it against "" for
// absence is correct by construction, at all three levels an empty
// description can appear.
func TestParseRejectsEmptyDescription(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{
			name: "file level",
			src:  "namespace acme\ndescription \"\"\n",
		},
		{
			name: "type level",
			src: `namespace acme

type widget {
  description ""
}
`,
		},
		{
			name: "op block level",
			src: `namespace acme

type widget {
  op create 1 {
    description ""
    a string lww
  }
}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := schemasrc.Parse("test.schema", []byte(tc.src))
			if err == nil {
				t.Fatalf("Parse: expected an error rejecting the empty description, got none")
			}
			if !strings.Contains(err.Error(), "description must not be empty") {
				t.Errorf("Parse error = %q, want it to contain %q", err.Error(), "description must not be empty")
			}
		})
	}
}
