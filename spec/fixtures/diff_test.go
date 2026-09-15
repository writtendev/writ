package fixtures

import (
	"strings"
	"testing"
)

func TestDiff_Identical(t *testing.T) {
	data := []byte("line1\nline2\nline3\n")
	if diff := Diff("a", data, "b", data); diff != "" {
		t.Errorf("expected empty diff for identical inputs, got:\n%s", diff)
	}
}

func TestDiff_SingleLineChange(t *testing.T) {
	oldText := "line1\nline2\nline3\n"
	newText := "line1\nline2-modified\nline3\n"

	diff := DiffText("want.json", oldText, "got.json", newText)
	if diff == "" {
		t.Fatal("expected diff, got empty string")
	}

	expectedParts := []string{
		"--- want.json",
		"+++ got.json",
		"@@ -1,3 +1,3 @@",
		" line1",
		"-line2",
		"+line2-modified",
		" line3",
	}
	for _, part := range expectedParts {
		if !strings.Contains(diff, part) {
			t.Errorf("diff missing expected part %q\nFull diff:\n%s", part, diff)
		}
	}
}

func TestDiff_AdditionAndDeletion(t *testing.T) {
	oldText := "alpha\nbeta\ngamma\n"
	newText := "alpha\nbeta-new\ndelta\nepsilon\n"

	diff := DiffText("old", oldText, "new", newText)
	if diff == "" {
		t.Fatal("expected diff, got empty string")
	}
	if !strings.Contains(diff, "-beta") || !strings.Contains(diff, "+beta-new") || !strings.Contains(diff, "+delta") {
		t.Errorf("unexpected diff content:\n%s", diff)
	}
}

func TestDiff_BinaryFiles(t *testing.T) {
	oldData := []byte{0x00, 0x01, 0x02, 0xff}
	newData := []byte{0x00, 0x01, 0x03, 0xff}

	diff := Diff("old.bin", oldData, "new.bin", newData)
	if !strings.Contains(diff, "Binary files old.bin and new.bin differ") {
		t.Errorf("expected binary diff message, got:\n%s", diff)
	}
}

func TestDiff_EmptyInputs(t *testing.T) {
	if diff := DiffText("a", "", "b", ""); diff != "" {
		t.Errorf("expected empty diff for both empty strings, got:\n%s", diff)
	}

	diff := DiffText("a", "", "b", "first line\n")
	if diff == "" {
		t.Fatal("expected non-empty diff")
	}
	if !strings.Contains(diff, "+first line") {
		t.Errorf("expected addition of first line, got:\n%s", diff)
	}
}

func TestDiff_TrailingNewlineDifference(t *testing.T) {
	oldData := []byte("line1\nline2\n")
	newData := []byte("line1\nline2")

	diff := Diff("old", oldData, "new", newData)
	if diff == "" {
		t.Fatal("expected non-empty diff for missing trailing newline, got empty string")
	}
	if !strings.Contains(diff, "No newline at end of file") {
		t.Errorf("expected newline notice in diff, got:\n%s", diff)
	}

	diffRev := Diff("old", newData, "new", oldData)
	if diffRev == "" {
		t.Fatal("expected non-empty diff for added trailing newline, got empty string")
	}
	if !strings.Contains(diffRev, "No newline at end of old file") {
		t.Errorf("expected old newline notice in diff, got:\n%s", diffRev)
	}
}
