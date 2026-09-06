package schemasrc_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/writtendev/writ/engine/schemasrc"
)

// invalidCase is one testdata/invalid/index.json entry: a fixture and the
// first rejection it must produce, pinned by line, column, and message —
// whichever stage catches it, Parse (a syntax error) or Compile (a
// cross-field invariant spec.ValidateFieldRule alone can see, such as a
// lattice element outside its enum). Both surface a *schemasrc.SyntaxError,
// which is what makes pinning both kinds through one shape possible.
type invalidCase struct {
	File    string `json:"file"`
	Line    int    `json:"line"`
	Col     int    `json:"col"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
}

func loadInvalidIndex(t *testing.T) []invalidCase {
	t.Helper()
	raw := readGolden(t, "testdata/invalid/index.json")
	var cases []invalidCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("decoding testdata/invalid/index.json: %v", err)
	}
	return cases
}

// firstSyntaxError returns the first *schemasrc.SyntaxError produced by
// parsing (and, if parsing succeeds, compiling) name/src, or nil if
// neither step reports one.
func firstSyntaxError(name string, src []byte) *schemasrc.SyntaxError {
	f, err := schemasrc.Parse(name, src)
	if err != nil {
		var list schemasrc.ErrorList
		if errors.As(err, &list) && len(list) > 0 {
			return list[0]
		}
		var se *schemasrc.SyntaxError
		if errors.As(err, &se) {
			return se
		}
		return nil
	}
	_, err = schemasrc.Compile(f, testObjectID)
	if err == nil {
		return nil
	}
	var se *schemasrc.SyntaxError
	if errors.As(err, &se) {
		return se
	}
	return nil
}

func TestInvalidCorpus(t *testing.T) {
	cases := loadInvalidIndex(t)
	seen := make(map[string]bool)
	for _, tc := range cases {
		tc := tc
		seen[tc.File] = true
		t.Run(tc.File, func(t *testing.T) {
			path := filepath.Join("testdata/invalid", tc.File)
			src := readGolden(t, path)
			got := firstSyntaxError(tc.File, src)
			if got == nil {
				t.Fatalf("%s: expected a rejection, got none", tc.File)
			}
			if got.Line != tc.Line || got.Col != tc.Col || got.Msg != tc.Message {
				t.Errorf("%s: got %d:%d: %s\nwant %d:%d: %s", tc.File, got.Line, got.Col, got.Msg, tc.Line, tc.Col, tc.Message)
			}
		})
	}

	// Every *.schema file under testdata/invalid must be named in
	// index.json — an un-indexed fixture is a corpus entry nobody is
	// actually checking.
	matches, err := filepath.Glob("testdata/invalid/*.schema")
	if err != nil {
		t.Fatalf("globbing testdata/invalid: %v", err)
	}
	sort.Strings(matches)
	for _, m := range matches {
		base := filepath.Base(m)
		if !seen[base] {
			t.Errorf("testdata/invalid/%s is not indexed in index.json", base)
		}
	}
}

// TestGenerateInvalidIndex regenerates testdata/invalid/index.json's
// line/col/message columns from the fixtures' actual first rejection,
// run only with -update-golden (the harness.go idiom this repository
// otherwise uses throughout, e.g. spec/fixtures).
func TestGenerateInvalidIndex(t *testing.T) {
	if !*updateGolden {
		t.Skip("run with -update-golden to regenerate testdata/invalid/index.json")
	}
	cases := loadInvalidIndex(t)
	for i := range cases {
		path := filepath.Join("testdata/invalid", cases[i].File)
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		got := firstSyntaxError(cases[i].File, src)
		if got == nil {
			t.Fatalf("%s: expected a rejection, got none", cases[i].File)
		}
		cases[i].Line = got.Line
		cases[i].Col = got.Col
		cases[i].Message = got.Msg
	}
	raw, err := json.MarshalIndent(cases, "", "  ")
	if err != nil {
		t.Fatalf("marshaling index: %v", err)
	}
	if err := os.WriteFile("testdata/invalid/index.json", append(raw, '\n'), 0o644); err != nil {
		t.Fatalf("writing index.json: %v", err)
	}
}
