package fixtures

import "github.com/writtendev/writ/internal/textdiff"

// Diff compares oldData against newData and returns a formatted unified diff.
// If both slices are byte-identical, Diff returns an empty string.
// If either slice is not valid UTF-8, Diff returns a binary diff summary.
//
// The implementation lives in internal/textdiff (WRIT-191): this package
// imports "testing" and "flag" (harness.go), so nothing that ships in a
// binary can link it. This wrapper keeps the old signature for this
// package's own callers.
func Diff(oldName string, oldData []byte, newName string, newData []byte) string {
	return textdiff.Diff(oldName, oldData, newName, newData)
}

// DiffText compares oldText against newText and returns a formatted unified diff
// with 3 lines of context. If both strings are identical, DiffText returns "".
func DiffText(oldName, oldText, newName, newText string) string {
	return textdiff.DiffText(oldName, oldText, newName, newText)
}
