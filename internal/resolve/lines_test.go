package resolve_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/writtendev/writ/internal/resolve"
)

func TestSplitLines(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected []string
	}{
		{
			name:     "empty blob",
			input:    []byte(""),
			expected: nil,
		},
		{
			name:     "single line with trailing LF",
			input:    []byte("hello world\n"),
			expected: []string{"hello world"},
		},
		{
			name:     "single line without trailing LF",
			input:    []byte("hello world"),
			expected: []string{"hello world"},
		},
		{
			name:     "multiple lines with trailing LF",
			input:    []byte("line 1\nline 2\nline 3\n"),
			expected: []string{"line 1", "line 2", "line 3"},
		},
		{
			name:     "multiple lines without trailing LF",
			input:    []byte("line 1\nline 2\nline 3"),
			expected: []string{"line 1", "line 2", "line 3"},
		},
		{
			name:     "CRLF preserved verbatim",
			input:    []byte("line 1\r\nline 2\r\n"),
			expected: []string{"line 1\r", "line 2\r"},
		},
		{
			name:     "mixed LF and CRLF",
			input:    []byte("line 1\r\nline 2\nline 3\r\n"),
			expected: []string{"line 1\r", "line 2", "line 3\r"},
		},
		{
			name:     "empty lines within content",
			input:    []byte("line 1\n\nline 3\n"),
			expected: []string{"line 1", "", "line 3"},
		},
		{
			name:     "invalid UTF-8 replacement per byte",
			input:    []byte{0xff, 'a', 0xfe, 'b', 0xc3, 0x28, '\n'},
			expected: []string{"\uFFFDa\uFFFDb\uFFFD("},
		},
		{
			name:     "1000 rune truncation on multibyte boundary",
			input:    []byte(strings.Repeat("世", 1005) + "\n"),
			expected: []string{strings.Repeat("世", 1000)},
		},
		{
			name:     "1000 rune truncation ASCII",
			input:    []byte(strings.Repeat("a", 1005)),
			expected: []string{strings.Repeat("a", 1000)},
		},
		{
			name:     "1000 rune truncation supplementary characters",
			input:    []byte(strings.Repeat("𠀀", 1005) + "\n"),
			expected: []string{strings.Repeat("𠀀", 1000)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := resolve.SplitLines(tt.input)
			if !reflect.DeepEqual(actual, tt.expected) {
				t.Errorf("SplitLines(%q):\nactual:   %+v\nexpected: %+v", tt.input, actual, tt.expected)
			}
		})
	}
}
