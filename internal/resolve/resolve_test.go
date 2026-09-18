package resolve_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/writtendev/writ/internal/codec/canonicaljson"
	"github.com/writtendev/writ/internal/resolve"
	"github.com/writtendev/writ/spec"
)

func detectHashAlgo(anchorRaw []byte) resolve.HashAlgo {
	var a struct {
		Old *struct {
			Commit string `json:"commit"`
			Blob   string `json:"blob"`
		} `json:"old,omitempty"`
		New *struct {
			Commit string `json:"commit"`
			Blob   string `json:"blob"`
		} `json:"new,omitempty"`
	}
	if err := json.Unmarshal(anchorRaw, &a); err == nil {
		if a.New != nil {
			if len(a.New.Commit) == 64 || len(a.New.Blob) == 64 {
				return resolve.SHA256
			}
		}
		if a.Old != nil {
			if len(a.Old.Commit) == 64 || len(a.Old.Blob) == 64 {
				return resolve.SHA256
			}
		}
	}
	return resolve.SHA1
}

func TestConformanceVectors(t *testing.T) {
	cases, err := spec.ResolutionVectors()
	if err != nil {
		t.Fatalf("loading resolution vectors: %v", err)
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			files := make(map[string][]byte, len(c.Target.Files))
			for p, content := range c.Target.Files {
				files[p] = []byte(content)
			}

			algo := detectHashAlgo(c.Anchor)
			tree := resolve.NewTree(files, algo)

			// ResolveRaw, not ParseAnchor+Resolve: the conformance vectors
			// are exactly the raw bytes materializeAnchors hands the
			// resolver, and a malformed-* vector's anchor.version is not
			// always something ParseAnchor can even decode (WRIT-252).
			outcome := resolve.ResolveRaw(c.Anchor, tree)

			outcomeJSON, err := json.Marshal(outcome)
			if err != nil {
				t.Fatalf("marshaling outcome: %v", err)
			}

			actualCanon, err := canonicaljson.Marshal(outcomeJSON)
			if err != nil {
				t.Fatalf("canonicalizing actual outcome: %v", err)
			}

			expectCanon, err := canonicaljson.Marshal(c.Expect)
			if err != nil {
				t.Fatalf("canonicalizing expect outcome: %v", err)
			}

			if !bytes.Equal(actualCanon, expectCanon) {
				t.Errorf("%s mismatch:\nactual:\n%s\nexpect:\n%s", c.Name, string(actualCanon), string(expectCanon))
			}

			// Assert outcome's anchor canonicalizes equal to input anchor (orphaned but preserved)
			outcomeAnchorJSON, err := json.Marshal(outcome.Anchor)
			if err != nil {
				t.Fatalf("marshaling outcome anchor: %v", err)
			}
			outcomeAnchorCanon, err := canonicaljson.Marshal(outcomeAnchorJSON)
			if err != nil {
				t.Fatalf("canonicalizing outcome anchor: %v", err)
			}

			inputAnchorCanon, err := canonicaljson.Marshal(c.Anchor)
			if err != nil {
				t.Fatalf("canonicalizing input anchor: %v", err)
			}

			if !bytes.Equal(outcomeAnchorCanon, inputAnchorCanon) {
				t.Errorf("%s anchor preservation mismatch:\nactual anchor:\n%s\ninput anchor:\n%s", c.Name, string(outcomeAnchorCanon), string(inputAnchorCanon))
			}
		})
	}
}

// TestContextMarshalsAbsentCollarAsArray pins the domain-layer transform that
// keeps an edge-of-file anchor writable. anchor.schema.json requires before,
// lines and after as arrays; a nil Go slice marshals to null, and the fields
// carry no omitempty, so the zero value has to serialize as an empty array
// rather than null. A comment on the first or last line of a file is the
// ordinary way to reach it.
func TestContextMarshalsAbsentCollarAsArray(t *testing.T) {
	raw, err := json.Marshal(resolve.Context{Lines: []string{"only line"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"before":[],"lines":["only line"],"after":[]}`
	if string(raw) != want {
		t.Errorf("got %s, want %s", raw, want)
	}

	// Round-tripping keeps the same shape.
	var back resolve.Context
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(again) != want {
		t.Errorf("round trip got %s, want %s", again, want)
	}

	// A populated collar is untouched, omitted included.
	full, err := json.Marshal(resolve.Context{
		Before:  []string{"a"},
		Lines:   []string{"b"},
		Omitted: 2,
		After:   []string{"c"},
	})
	if err != nil {
		t.Fatalf("marshal populated: %v", err)
	}
	if wantFull := `{"before":["a"],"lines":["b"],"omitted":2,"after":["c"]}`; string(full) != wantFull {
		t.Errorf("got %s, want %s", full, wantFull)
	}
}
