package resolve_test

import (
	"fmt"
	"testing"

	"github.com/writtendev/writ/internal/resolve"
)

func TestLadderCrossFileScanWhenPathAbsent(t *testing.T) {
	anchor := resolve.Anchor{
		Version: 1,
		New: &resolve.SideAnchor{
			Commit: "1111111111111111111111111111111111111111",
			Path:   "missing.go",
			Blob:   "0000000000000000000000000000000000000000",
			Range:  &resolve.Range{Start: 2, End: 3},
			Context: &resolve.Context{
				Before: []string{"func Main() {"},
				Lines:  []string{"\tdoA()", "\tdoB()"},
				After:  []string{"}"},
			},
		},
	}

	files := map[string][]byte{
		"other/renamed.go": []byte("package other\nfunc Main() {\n\tdoA()\n\tdoB()\n}\n"),
	}
	tree := resolve.NewTree(files, resolve.SHA1)

	res := resolve.Resolve(anchor, tree)
	if res.New == nil || res.New.Outcome != "resolved" {
		t.Fatalf("expected resolved, got %+v", res.New)
	}
	if res.New.Match != "context-exact" || res.New.Path != "other/renamed.go" {
		t.Errorf("unexpected match/path: match=%q path=%q", res.New.Match, res.New.Path)
	}
	if res.New.Range == nil || res.New.Range.Start != 3 || res.New.Range.End != 4 {
		t.Errorf("unexpected range: %+v", res.New.Range)
	}
}

func TestLadderTiebreakCollarScore(t *testing.T) {
	// Two matching windows: line 2 has 1 collar match, line 7 has 2 collar matches
	anchor := resolve.Anchor{
		Version: 1,
		New: &resolve.SideAnchor{
			Commit: "1111111111111111111111111111111111111111",
			Path:   "file.go",
			Blob:   "0000000000000000000000000000000000000000",
			Range:  &resolve.Range{Start: 1, End: 1},
			Context: &resolve.Context{
				Before: []string{"before1", "before2"},
				Lines:  []string{"target_line"},
				After:  []string{"after1", "after2"},
			},
		},
	}

	content := "wrong_before\ntarget_line\nafter1\nother\nbefore1\nbefore2\ntarget_line\nafter1\nafter2\n"
	tree := resolve.NewTree(map[string][]byte{"file.go": []byte(content)}, resolve.SHA1)

	res := resolve.Resolve(anchor, tree)
	if res.New == nil || res.New.Outcome != "resolved" {
		t.Fatalf("expected resolved, got %+v", res.New)
	}
	if res.New.Match != "context-exact" || res.New.Range.Start != 7 {
		t.Errorf("expected line 7 (higher collar score), got start=%d match=%s", res.New.Range.Start, res.New.Match)
	}
}

func TestLadderTiebreakDistance(t *testing.T) {
	// Two matching windows with same collar score: line 10 and line 30. Original start is 12.
	// Line 10 (dist=2) should beat line 30 (dist=18).
	anchor := resolve.Anchor{
		Version: 1,
		New: &resolve.SideAnchor{
			Commit: "1111111111111111111111111111111111111111",
			Path:   "file.go",
			Blob:   "0000000000000000000000000000000000000000",
			Range:  &resolve.Range{Start: 12, End: 12},
			Context: &resolve.Context{
				Before: []string{"header"},
				Lines:  []string{"target"},
				After:  []string{"footer"},
			},
		},
	}

	var lines []string
	for i := 1; i <= 40; i++ {
		switch i {
		case 9, 29:
			lines = append(lines, "header")
		case 10, 30:
			lines = append(lines, "target")
		case 11, 31:
			lines = append(lines, "footer")
		default:
			lines = append(lines, fmt.Sprintf("line_%d", i))
		}
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}

	tree := resolve.NewTree(map[string][]byte{"file.go": []byte(content)}, resolve.SHA1)
	res := resolve.Resolve(anchor, tree)
	if res.New == nil || res.New.Outcome != "resolved" {
		t.Fatalf("expected resolved, got %+v", res.New)
	}
	if res.New.Range.Start != 10 {
		t.Errorf("expected line 10 (closer distance), got start=%d", res.New.Range.Start)
	}
}

func TestLadderTiebreakEarliestStart(t *testing.T) {
	// Original start is 20. Windows at line 15 (dist=5) and line 25 (dist=5) with same collar score.
	// Earliest start (15 < 25) wins.
	anchor := resolve.Anchor{
		Version: 1,
		New: &resolve.SideAnchor{
			Commit: "1111111111111111111111111111111111111111",
			Path:   "file.go",
			Blob:   "0000000000000000000000000000000000000000",
			Range:  &resolve.Range{Start: 20, End: 20},
			Context: &resolve.Context{
				Before: []string{"ctx"},
				Lines:  []string{"match"},
				After:  []string{"ctx"},
			},
		},
	}

	var lines []string
	for i := 1; i <= 40; i++ {
		switch i {
		case 14, 24:
			lines = append(lines, "ctx")
		case 15, 25:
			lines = append(lines, "match")
		case 16, 26:
			lines = append(lines, "ctx")
		default:
			lines = append(lines, fmt.Sprintf("filler_%d", i))
		}
	}
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}

	tree := resolve.NewTree(map[string][]byte{"file.go": []byte(content)}, resolve.SHA1)
	res := resolve.Resolve(anchor, tree)
	if res.New == nil || res.New.Outcome != "resolved" {
		t.Fatalf("expected resolved, got %+v", res.New)
	}
	if res.New.Range.Start != 15 {
		t.Errorf("expected line 15 (earlier start line), got start=%d", res.New.Range.Start)
	}
}

func TestLadderTiebreakEarliestPath(t *testing.T) {
	// Recorded path absent. Files "b.go" and "a.go" both have identical matching windows.
	anchor := resolve.Anchor{
		Version: 1,
		New: &resolve.SideAnchor{
			Commit: "1111111111111111111111111111111111111111",
			Path:   "missing.go",
			Blob:   "0000000000000000000000000000000000000000",
			Range:  &resolve.Range{Start: 1, End: 1},
			Context: &resolve.Context{
				Before: []string{"head"},
				Lines:  []string{"line"},
				After:  []string{"tail"},
			},
		},
	}

	content := "head\nline\ntail\n"
	files := map[string][]byte{
		"z_file.go": []byte(content),
		"a_file.go": []byte(content),
		"m_file.go": []byte(content),
	}

	tree := resolve.NewTree(files, resolve.SHA1)
	res := resolve.Resolve(anchor, tree)
	if res.New == nil || res.New.Outcome != "resolved" {
		t.Fatalf("expected resolved, got %+v", res.New)
	}
	if res.New.Path != "a_file.go" {
		t.Errorf("expected lexicographically earliest path 'a_file.go', got %q", res.New.Path)
	}
}

func TestLadderElidedRangeExactMatch(t *testing.T) {
	// Range spanning 100 lines (32 head + 36 omitted + 32 tail)
	var headLines []string
	for i := 1; i <= 32; i++ {
		headLines = append(headLines, fmt.Sprintf("head_%d", i))
	}
	var tailLines []string
	for i := 1; i <= 32; i++ {
		tailLines = append(tailLines, fmt.Sprintf("tail_%d", i))
	}

	allCtxLines := append(append([]string{}, headLines...), tailLines...)

	anchor := resolve.Anchor{
		Version: 1,
		New: &resolve.SideAnchor{
			Commit: "1111111111111111111111111111111111111111",
			Path:   "long.go",
			Blob:   "0000000000000000000000000000000000000000",
			Range:  &resolve.Range{Start: 5, End: 104},
			Context: &resolve.Context{
				Before:  []string{"before"},
				Lines:   allCtxLines,
				Omitted: 36,
				After:   []string{"after"},
			},
		},
	}

	// Build target file shifted by 10 lines (start at line 15) with different middle lines
	var fileLines []string
	for i := 1; i <= 14; i++ {
		fileLines = append(fileLines, fmt.Sprintf("prefix_%d", i))
	}
	fileLines = append(fileLines, headLines...)
	for i := 1; i <= 36; i++ {
		fileLines = append(fileLines, fmt.Sprintf("edited_middle_%d", i))
	}
	fileLines = append(fileLines, tailLines...)
	fileLines = append(fileLines, "suffix")

	content := ""
	for _, l := range fileLines {
		content += l + "\n"
	}

	tree := resolve.NewTree(map[string][]byte{"long.go": []byte(content)}, resolve.SHA1)
	res := resolve.Resolve(anchor, tree)
	if res.New == nil || res.New.Outcome != "resolved" {
		t.Fatalf("expected resolved, got %+v", res.New)
	}
	if res.New.Match != "context-exact" || res.New.Range.Start != 15 || res.New.Range.End != 114 {
		t.Errorf("unexpected resolved range: start=%d end=%d match=%s", res.New.Range.Start, res.New.Range.End, res.New.Match)
	}
}

func TestLadderWholeFileOrphanReasons(t *testing.T) {
	anchor := resolve.Anchor{
		Version: 1,
		New: &resolve.SideAnchor{
			Commit: "1111111111111111111111111111111111111111",
			Path:   "file.txt",
			Blob:   "1111111111111111111111111111111111111111",
		},
	}

	// 1. Path exists with changed blob -> no-candidate
	treeWithFile := resolve.NewTree(map[string][]byte{"file.txt": []byte("new content\n")}, resolve.SHA1)
	res1 := resolve.Resolve(anchor, treeWithFile)
	if res1.New == nil || res1.New.Outcome != "orphaned" || res1.New.Reason != "no-candidate" {
		t.Errorf("expected orphaned/no-candidate, got %+v", res1.New)
	}

	// 2. Path absent and blob absent -> path-absent
	treeEmpty := resolve.NewTree(map[string][]byte{"other.txt": []byte("other content\n")}, resolve.SHA1)
	res2 := resolve.Resolve(anchor, treeEmpty)
	if res2.New == nil || res2.New.Outcome != "orphaned" || res2.New.Reason != "path-absent" {
		t.Errorf("expected orphaned/path-absent, got %+v", res2.New)
	}
}
