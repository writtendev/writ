package resolve

import (
	"bytes"
)

// SplitLines splits blob contents into lines following spec/anchors.md §Lines and encoding:
//  1. Splitting: byte-level split at LF (0x0A). Trailing LF produces no empty final line.
//     CR (0x0D) is preserved verbatim. Empty content produces nil.
//  2. Decoding: decoded as UTF-8, replacing each invalid byte with U+FFFD.
//  3. Truncation: lines longer than 1000 Unicode code points are truncated to the first 1000.
func SplitLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	raw := bytes.Split(content, []byte{'\n'})
	if len(raw) > 0 && len(raw[len(raw)-1]) == 0 {
		raw = raw[:len(raw)-1]
	}
	lines := make([]string, len(raw))
	for i, b := range raw {
		lines[i] = decodeAndTruncateLine(b)
	}
	return lines
}

func decodeAndTruncateLine(b []byte) string {
	runes := []rune(string(b))
	if len(runes) > 1000 {
		runes = runes[:1000]
	}
	return string(runes)
}
