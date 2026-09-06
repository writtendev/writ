// Package textdiff renders a unified line diff between two texts.
//
// It is a straight move of spec/fixtures/diff.go (WRIT-191): package
// fixtures imports "testing" and "flag" (harness.go), so nothing that ships
// in a binary — cmd/writ's `schema plan`, concretely — can link it.
// spec/fixtures.Diff and spec/fixtures.DiffText keep their old signatures
// as thin wrappers delegating here, so this is one implementation moved,
// not a second one added.
package textdiff

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Diff compares oldData against newData and returns a formatted unified diff.
// If both slices are byte-identical, Diff returns an empty string.
// If either slice is not valid UTF-8, Diff returns a binary diff summary.
func Diff(oldName string, oldData []byte, newName string, newData []byte) string {
	if bytes.Equal(oldData, newData) {
		return ""
	}

	if !utf8.Valid(oldData) || !utf8.Valid(newData) {
		return fmt.Sprintf("Binary files %s and %s differ (old: %d bytes, new: %d bytes)\n",
			oldName, newName, len(oldData), len(newData))
	}

	return DiffText(oldName, string(oldData), newName, string(newData))
}

// DiffText compares oldText against newText and returns a formatted unified diff
// with 3 lines of context. If both strings are identical, DiffText returns "".
func DiffText(oldName, oldText, newName, newText string) string {
	if oldText == newText {
		return ""
	}

	oldLines := splitLines(oldText)
	newLines := splitLines(newText)

	ops := computeDiff(oldLines, newLines)
	hunks := createHunks(ops, 3)
	if len(hunks) == 0 {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("--- %s\n", oldName))
		sb.WriteString(fmt.Sprintf("+++ %s\n", newName))
		if strings.HasSuffix(oldText, "\n") && !strings.HasSuffix(newText, "\n") {
			sb.WriteString("@@ -1 +1 @@\n")
			sb.WriteString(" " + strings.TrimRight(oldText, "\r\n") + "\n")
			sb.WriteString("\\ No newline at end of file\n")
		} else if !strings.HasSuffix(oldText, "\n") && strings.HasSuffix(newText, "\n") {
			sb.WriteString("@@ -1 +1 @@\n")
			sb.WriteString(" " + strings.TrimRight(newText, "\r\n") + "\n")
			sb.WriteString("\\ No newline at end of old file\n")
		} else {
			sb.WriteString(fmt.Sprintf("@@ files differ in trailing whitespace or line endings (old: %d bytes, new: %d bytes) @@\n", len(oldText), len(newText)))
		}
		return sb.String()
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- %s\n", oldName))
	sb.WriteString(fmt.Sprintf("+++ %s\n", newName))

	for _, h := range hunks {
		writeHunk(&sb, h)
	}

	return sb.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

type diffOp int

const (
	opEqual diffOp = iota
	opDelete
	opInsert
)

type diffEntry struct {
	op      diffOp
	text    string
	oldLine int
	newLine int
}

func computeDiff(a, b []string) []diffEntry {
	n, m := len(a), len(b)

	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}

	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	var entries []diffEntry
	i, j := n, m
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && a[i-1] == b[j-1] {
			entries = append(entries, diffEntry{op: opEqual, text: a[i-1], oldLine: i, newLine: j})
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			entries = append(entries, diffEntry{op: opInsert, text: b[j-1], oldLine: i, newLine: j})
			j--
		} else if i > 0 && (j == 0 || dp[i][j-1] < dp[i-1][j]) {
			entries = append(entries, diffEntry{op: opDelete, text: a[i-1], oldLine: i, newLine: j})
			i--
		}
	}

	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}

	return entries
}

type hunk struct {
	oldStart int
	oldCount int
	newStart int
	newCount int
	entries  []diffEntry
}

func createHunks(entries []diffEntry, context int) []hunk {
	if len(entries) == 0 {
		return nil
	}

	var changeIndices []int
	for i, e := range entries {
		if e.op != opEqual {
			changeIndices = append(changeIndices, i)
		}
	}
	if len(changeIndices) == 0 {
		return nil
	}

	type indexRange struct {
		start int
		end   int
	}

	var ranges []indexRange
	current := indexRange{
		start: max(0, changeIndices[0]-context),
		end:   min(len(entries), changeIndices[0]+context+1),
	}

	for _, idx := range changeIndices[1:] {
		nextStart := max(0, idx-context)
		nextEnd := min(len(entries), idx+context+1)

		if nextStart <= current.end {
			current.end = nextEnd
		} else {
			ranges = append(ranges, current)
			current = indexRange{start: nextStart, end: nextEnd}
		}
	}
	ranges = append(ranges, current)

	var hunks []hunk
	for _, r := range ranges {
		hunkEntries := entries[r.start:r.end]
		h := hunk{entries: hunkEntries}

		oldCount := 0
		newCount := 0
		firstOld := 0
		firstNew := 0

		for _, e := range hunkEntries {
			switch e.op {
			case opEqual:
				oldCount++
				newCount++
				if firstOld == 0 && e.oldLine > 0 {
					firstOld = e.oldLine
				}
				if firstNew == 0 && e.newLine > 0 {
					firstNew = e.newLine
				}
			case opDelete:
				oldCount++
				if firstOld == 0 && e.oldLine > 0 {
					firstOld = e.oldLine
				}
			case opInsert:
				newCount++
				if firstNew == 0 && e.newLine > 0 {
					firstNew = e.newLine
				}
			}
		}

		if firstOld == 0 {
			if r.start > 0 {
				firstOld = entries[r.start-1].oldLine + 1
			} else {
				firstOld = 1
			}
		}
		if firstNew == 0 {
			if r.start > 0 {
				firstNew = entries[r.start-1].newLine + 1
			} else {
				firstNew = 1
			}
		}

		h.oldStart = firstOld
		h.oldCount = oldCount
		h.newStart = firstNew
		h.newCount = newCount

		hunks = append(hunks, h)
	}

	return hunks
}

func writeHunk(sb *strings.Builder, h hunk) {
	sb.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", h.oldStart, h.oldCount, h.newStart, h.newCount))
	for _, e := range h.entries {
		switch e.op {
		case opEqual:
			sb.WriteString(" ")
			sb.WriteString(e.text)
			sb.WriteString("\n")
		case opDelete:
			sb.WriteString("-")
			sb.WriteString(e.text)
			sb.WriteString("\n")
		case opInsert:
			sb.WriteString("+")
			sb.WriteString(e.text)
			sb.WriteString("\n")
		}
	}
}
