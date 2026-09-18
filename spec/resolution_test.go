package spec_test

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/writtendev/writ/spec"
)

const resolutionSchemaID = "https://writ.dev/spec/resolution.schema.json"

func compileResolutionSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	rawAnchor, err := spec.FS.ReadFile("schemas/anchor.schema.json")
	if err != nil {
		t.Fatalf("reading anchor schema: %v", err)
	}
	anchorDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawAnchor))
	if err != nil {
		t.Fatalf("decoding anchor schema: %v", err)
	}

	rawRes, err := spec.FS.ReadFile("schemas/resolution.schema.json")
	if err != nil {
		t.Fatalf("reading resolution schema: %v", err)
	}
	resDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawRes))
	if err != nil {
		t.Fatalf("decoding resolution schema: %v", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource(anchorSchemaID, anchorDoc); err != nil {
		t.Fatalf("adding anchor schema: %v", err)
	}
	if err := c.AddResource(resolutionSchemaID, resDoc); err != nil {
		t.Fatalf("adding resolution schema: %v", err)
	}
	sch, err := c.Compile(resolutionSchemaID)
	if err != nil {
		t.Fatalf("compiling resolution schema: %v", err)
	}
	return sch
}

func gitBlobOID(content string) string {
	b := []byte(content)
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func TestResolutionSchemaCompiles(t *testing.T) {
	compileResolutionSchema(t)
}

func TestResolutionVectorsValidate(t *testing.T) {
	anchorSch := compileAnchorSchema(t)
	resSch := compileResolutionSchema(t)

	cases, err := spec.ResolutionVectors()
	if err != nil {
		t.Fatalf("loading resolution vectors: %v", err)
	}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			// Version is decoded as json.RawMessage, not int: a malformed
			// vector may carry a non-integer version (e.g. "1" as a JSON
			// string), and that must not fail this test outright — it is
			// exactly the shape the malformed structural pre-check exists
			// to handle downstream, not a test-harness bug.
			var anchorObj struct {
				Version json.RawMessage `json:"version"`
				Old     *struct {
					Blob string `json:"blob"`
				} `json:"old,omitempty"`
				New *struct {
					Blob string `json:"blob"`
				} `json:"new,omitempty"`
			}
			if err := json.Unmarshal(c.Anchor, &anchorObj); err != nil {
				t.Fatalf("unmarshaling anchor: %v", err)
			}

			// A "malformed" case is by construction either schema-invalid
			// (e.g. version encoded as a string) or invariant-invalid (e.g.
			// a short elided range) — validateVector's job here is to prove
			// non-malformed vectors are clean, not to re-litigate the very
			// shape a malformed case exists to be rejected for.
			var expectSides struct {
				Old *struct {
					Reason string `json:"reason,omitempty"`
				} `json:"old,omitempty"`
				New *struct {
					Reason string `json:"reason,omitempty"`
				} `json:"new,omitempty"`
			}
			_ = json.Unmarshal(c.Expect, &expectSides)
			isMalformedCase := (expectSides.Old != nil && expectSides.Old.Reason == "malformed") ||
				(expectSides.New != nil && expectSides.New.Reason == "malformed")

			// Validate anchor against anchor.schema.json only when version
			// decodes as the JSON number 1 (an integral float such as 1.0
			// included, per spec/resolution.md's Version Pre-Check and
			// spec/canonicalization.md's "no integer/float distinction") and the
			// case isn't malformed. A malformed-version vector's version does
			// not decode as a number at all, and validating either kind would
			// just restate that it is schema- or invariant-invalid by
			// construction.
			var version float64
			isVersion1 := len(anchorObj.Version) > 0 && json.Unmarshal(anchorObj.Version, &version) == nil && version == 1
			if isVersion1 && !isMalformedCase {
				if err := validateVector(t, anchorSch, c.Anchor); err != nil {
					t.Errorf("anchor schema validation failed for %s: %v", c.Name, err)
				}
			}

			// Validate expect against resolution.schema.json
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(c.Expect))
			if err != nil {
				t.Fatalf("decoding expect instance: %v", err)
			}
			if err := resSch.Validate(inst); err != nil {
				t.Errorf("expect schema validation failed for %s: %v", c.Name, err)
			}

			var expectObj struct {
				Old *struct {
					Match string `json:"match,omitempty"`
					Path  string `json:"path,omitempty"`
				} `json:"old,omitempty"`
				New *struct {
					Match string `json:"match,omitempty"`
					Path  string `json:"path,omitempty"`
				} `json:"new,omitempty"`
			}
			if err := json.Unmarshal(c.Expect, &expectObj); err != nil {
				t.Fatalf("unmarshaling expect: %v", err)
			}

			// Verify blob OID integrity for exact-blob matches
			for path, content := range c.Target.Files {
				computed := gitBlobOID(content)
				if expectObj.New != nil && anchorObj.New != nil {
					if expectObj.New.Match == "exact-path-blob" && expectObj.New.Path == path {
						if anchorObj.New.Blob != computed {
							t.Errorf("%s: anchor.new.blob %q does not match computed blob %q of %s", c.Name, anchorObj.New.Blob, computed, path)
						}
					}
					if expectObj.New.Match == "exact-blob-moved" && expectObj.New.Path == path {
						if anchorObj.New.Blob != computed {
							t.Errorf("%s: anchor.new.blob %q does not match computed blob %q of %s", c.Name, anchorObj.New.Blob, computed, path)
						}
					}
				}
				if expectObj.Old != nil && anchorObj.Old != nil {
					if expectObj.Old.Match == "exact-path-blob" && expectObj.Old.Path == path {
						if anchorObj.Old.Blob != computed {
							t.Errorf("%s: anchor.old.blob %q does not match computed blob %q of %s", c.Name, anchorObj.Old.Blob, computed, path)
						}
					}
					if expectObj.Old.Match == "exact-blob-moved" && expectObj.Old.Path == path {
						if anchorObj.Old.Blob != computed {
							t.Errorf("%s: anchor.old.blob %q does not match computed blob %q of %s", c.Name, anchorObj.Old.Blob, computed, path)
						}
					}
				}
			}
		})
	}
}

func TestResolutionIndexCoverage(t *testing.T) {
	rawIndex, err := spec.FS.ReadFile("testdata/resolution/index.json")
	if err != nil {
		t.Fatal(err)
	}

	var index map[string]struct {
		Description string `json:"description"`
		Outcome     string `json:"outcome"`
		Match       string `json:"match"`
		Reason      string `json:"reason"`
	}
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatalf("decoding index.json: %v", err)
	}

	caseFiles := readDirNames(t, "testdata/resolution/cases")
	filesMap := make(map[string]bool)
	for _, f := range caseFiles {
		filesMap[f] = true
	}

	for f := range index {
		if !filesMap[f] {
			t.Errorf("index.json lists %s but file does not exist in cases/", f)
		}
	}

	for _, f := range caseFiles {
		entry, ok := index[f]
		if !ok {
			t.Errorf("case file %s is missing from index.json", f)
			continue
		}
		if entry.Description == "" {
			t.Errorf("case file %s in index.json has empty description", f)
		}
		if entry.Outcome == "" {
			t.Errorf("case file %s in index.json has empty outcome", f)
		}
	}

	// Assert every ladder rung and every orphan reason has coverage
	rungsCovered := make(map[string]bool)
	reasonsCovered := make(map[string]bool)

	for _, entry := range index {
		if entry.Match != "" {
			rungsCovered[entry.Match] = true
		}
		if entry.Reason != "" {
			reasonsCovered[entry.Reason] = true
		}
	}

	allRungs := []string{"exact-path-blob", "exact-blob-moved", "context-exact", "context-fuzzy"}
	for _, r := range allRungs {
		if !rungsCovered[r] {
			t.Errorf("ladder rung %q has no coverage in index.json", r)
		}
	}

	allReasons := []string{"path-absent", "no-candidate", "below-threshold", "ambiguous", "unsupported-version", "malformed"}
	for _, r := range allReasons {
		if !reasonsCovered[r] {
			t.Errorf("orphan reason %q has no coverage in index.json", r)
		}
	}
}
