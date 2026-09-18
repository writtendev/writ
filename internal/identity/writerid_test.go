package identity_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/writtendev/writ/internal/identity"
	"github.com/writtendev/writ/spec"
)

const refVectorsPath = "testdata/ref-names/vectors.json"

type refVectorsDoc struct {
	Valid []struct {
		Ref        string `json:"ref"`
		WriterID   string `json:"writer_id"`
		ObjectType string `json:"object_type"`
	} `json:"valid"`
	Invalid []struct {
		Ref    string `json:"ref"`
		Reason string `json:"reason"`
	} `json:"invalid"`
}

func loadRefVectors(t *testing.T) refVectorsDoc {
	t.Helper()
	raw, err := spec.FS.ReadFile(refVectorsPath)
	if err != nil {
		t.Fatalf("reading %s: %v", refVectorsPath, err)
	}
	var doc refVectorsDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding %s: %v", refVectorsPath, err)
	}
	return doc
}

func TestParseWriterID_ValidVectors(t *testing.T) {
	doc := loadRefVectors(t)
	if len(doc.Valid) == 0 {
		t.Fatal("no valid ref vectors loaded")
	}

	seen := make(map[string]bool)
	for _, v := range doc.Valid {
		if seen[v.WriterID] {
			continue
		}
		seen[v.WriterID] = true

		t.Run(v.WriterID, func(t *testing.T) {
			got, err := identity.ParseWriterID(v.WriterID)
			if err != nil {
				t.Fatalf("ParseWriterID(%q) unexpected error: %v", v.WriterID, err)
			}
			if string(got) != v.WriterID {
				t.Errorf("ParseWriterID(%q) = %q, want %q", v.WriterID, got, v.WriterID)
			}
		})
	}
}

func TestParseWriterID_InvalidVectors(t *testing.T) {
	doc := loadRefVectors(t)

	var testedCount int
	for _, inv := range doc.Invalid {
		if !strings.Contains(strings.ToLower(inv.Reason), "writer-id") {
			continue
		}
		// Extract writer-id segment from refs/writ/<segment>/...
		trimmed := strings.TrimPrefix(inv.Ref, "refs/writ/")
		parts := strings.Split(trimmed, "/")
		candidate := parts[0]

		testedCount++
		t.Run(inv.Reason, func(t *testing.T) {
			got, err := identity.ParseWriterID(candidate)
			if err == nil {
				t.Fatalf("ParseWriterID(%q) succeeded (got %q), expected rejection: %s", candidate, got, inv.Reason)
			}
			if !errors.Is(err, identity.ErrInvalid) {
				t.Errorf("ParseWriterID(%q) error = %v, want errors.Is ErrInvalid", candidate, err)
			}
			var cfgErr *identity.ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("ParseWriterID(%q) error is %T, want *identity.ConfigError", candidate, err)
			}
			if cfgErr.Key != "writ.writerId" {
				t.Errorf("cfgErr.Key = %q, want %q", cfgErr.Key, "writ.writerId")
			}
			if cfgErr.Value != candidate {
				t.Errorf("cfgErr.Value = %q, want %q", cfgErr.Value, candidate)
			}
		})
	}

	if testedCount == 0 {
		t.Fatal("no invalid writer-id vectors were matched and tested")
	}
}

func TestParseWriterID_LocalRejectionTable(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		reason string
	}{
		{"empty", "", "empty string"},
		{"too_short_15", "0123456789abcde", "15 hex characters"},
		{"too_short_1", "0", "1 character"},
		{"too_long_17", "0123456789abcdef0", "17 hex characters"},
		{"too_long_32", "0123456789abcdef0123456789abcdef", "32 hex characters"},
		{"uppercase_all", "0123456789ABCDEF", "all uppercase hex"},
		{"uppercase_mixed", "0123456789Abcdef", "mixed case hex"},
		{"non_hex_g", "0123456789abcdeg", "non-hex character g"},
		{"non_hex_z", "0123456789abcdefz", "non-hex character z"},
		{"hyphen", "0123456789abc-ef", "hyphen in middle"},
		{"underscore", "0123456789abc_ef", "underscore in middle"},
		{"human_name", "alice-workstation", "human name"},
		{"leading_space", " 0123456789abcdef", "leading space"},
		{"trailing_space", "0123456789abcdef ", "trailing space"},
		{"trailing_newline", "0123456789abcdef\n", "trailing newline"},
		{"embedded_slash", "01234567/9abcdef", "embedded slash"},
		{"utf8_chars", "0123456789abcdeé", "non-ASCII UTF-8"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := identity.ParseWriterID(tc.input)
			if err == nil {
				t.Fatalf("ParseWriterID(%q) succeeded (got %q), want ErrInvalid (%s)", tc.input, got, tc.reason)
			}
			if !errors.Is(err, identity.ErrInvalid) {
				t.Errorf("ParseWriterID(%q) error = %v, want errors.Is ErrInvalid", tc.input, err)
			}
			var cfgErr *identity.ConfigError
			if !errors.As(err, &cfgErr) {
				t.Fatalf("ParseWriterID(%q) error is %T, want *identity.ConfigError", tc.input, err)
			}
			if cfgErr.Key != "writ.writerId" {
				t.Errorf("cfgErr.Key = %q, want %q", cfgErr.Key, "writ.writerId")
			}
			if cfgErr.Value != tc.input {
				t.Errorf("cfgErr.Value = %q, want %q", cfgErr.Value, tc.input)
			}
		})
	}
}
