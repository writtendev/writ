package state_test

import (
	"testing"

	s "github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec"
)

func TestReferenceVectorsValidCorpus(t *testing.T) {
	vectors, err := spec.ReferenceVectors()
	if err != nil {
		t.Fatalf("spec.ReferenceVectors failed: %v", err)
	}

	if len(vectors) == 0 {
		t.Fatal("expected non-empty reference vectors from spec corpus")
	}

	for _, vec := range vectors {
		t.Run(vec.Name, func(t *testing.T) {
			if _, _, err := s.ParseReference(vec.Reference); err != nil {
				t.Fatalf("ParseReference(%q) error: %v", vec.Reference, err)
			}
		})
	}
}

func TestInvalidReferenceGrammarCases(t *testing.T) {
	invalidCases := []struct {
		name string
		ref  string
	}{
		{"empty string", ""},
		{"whitespace only", "   "},
		{"leading whitespace", " 0123456789abcdef0123456789abcdef"},
		{"trailing whitespace", "0123456789abcdef0123456789abcdef "},
		{"embedded whitespace", "0123456789abcdef 0123456789abcdef"},
		{"newline in reference", "0123456789abcdef\n0123456789abcdef"},
		{"tab in reference", "0123456789abcdef\t0123456789abcdef"},
		{"two hashes", "a1b2c3d4e5f60718293a4b5c6d7e8f90#0123456789abcdef0123456789abcdef#extra"},
		{"empty designator", "#0123456789abcdef0123456789abcdef"},
		{"empty object id", "a1b2c3d4e5f60718293a4b5c6d7e8f90#"},
		{"short repo id", "a1b2c3d4e5f6#0123456789abcdef0123456789abcdef"},
		{"uppercase repo id", "A1B2C3D4E5F60718293A4B5C6D7E8F90#0123456789abcdef0123456789abcdef"},
		{"non-hex repo id", "g1h2i3j4k5l6m7n8o9p0q1r2s3t4u5v6#0123456789abcdef0123456789abcdef"},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := s.ParseReference(tc.ref)
			if err == nil {
				t.Errorf("ParseReference(%q) expected error, got nil", tc.ref)
			}
		})
	}
}

func TestParseReferenceBareAndQualified(t *testing.T) {
	localRepoID := "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	objID := "0123456789abcdef0123456789abcdef"

	// Bare reference: no designator.
	des, gotObjID, err := s.ParseReference(objID)
	if err != nil {
		t.Fatalf("ParseReference bare failed: %v", err)
	}
	if des != "" || gotObjID != objID {
		t.Errorf("bare parse mismatch: designator=%q, objectID=%q", des, gotObjID)
	}

	// Fully-qualified reference.
	qualRef := localRepoID + "#" + objID
	des, gotObjID, err = s.ParseReference(qualRef)
	if err != nil {
		t.Fatalf("ParseReference qualified failed: %v", err)
	}
	if des != localRepoID || gotObjID != objID {
		t.Errorf("qualified parse mismatch: designator=%q, objectID=%q", des, gotObjID)
	}
}
