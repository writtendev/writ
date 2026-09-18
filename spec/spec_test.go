package spec_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/writtendev/writ/internal/codec/canonicaljson"
	"github.com/writtendev/writ/spec"
)

const schemaPath = "schemas/op-envelope.schema.json"

type compiledSchema struct {
	sch *jsonschema.Schema
	raw []byte
	err error
}

// schemaOnce reads and compiles the committed envelope schema once for
// the whole test binary. The compiler validates the schema document
// against its declared meta-schema as part of compilation, so a schema
// that is itself invalid draft 2020-12 fails here.
var schemaOnce = sync.OnceValue(func() compiledSchema {
	raw, err := spec.FS.ReadFile(schemaPath)
	if err != nil {
		return compiledSchema{err: err}
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return compiledSchema{err: err}
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaPath, doc); err != nil {
		return compiledSchema{err: err}
	}
	sch, err := c.Compile(schemaPath)
	if err != nil {
		return compiledSchema{err: err}
	}
	return compiledSchema{sch: sch, raw: raw}
})

func envelopeSchema(t *testing.T) (*jsonschema.Schema, []byte) {
	t.Helper()
	c := schemaOnce()
	if c.err != nil {
		t.Fatalf("compiling %s: %v", schemaPath, c.err)
	}
	return c.sch, c.raw
}

func TestSchemaDeclaresDraft2020(t *testing.T) {
	_, raw := envelopeSchema(t) // compile = meta-schema validation
	var doc struct {
		Schema string `json:"$schema"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing %s: %v", schemaPath, err)
	}
	const want = "https://json-schema.org/draft/2020-12/schema"
	if doc.Schema != want {
		t.Errorf("$schema = %q, want %q", doc.Schema, want)
	}
}

// Every valid envelope instance must validate against the schema AND be
// byte-identical to the canonical encoding of its own content — the
// byte-equality rule from spec/op-envelope.md.
func TestValidEnvelopes(t *testing.T) {
	sch, _ := envelopeSchema(t)
	entries, err := spec.FS.ReadDir("testdata/envelopes/valid")
	if err != nil {
		t.Fatalf("listing valid envelopes: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no valid envelope instances")
	}
	for _, e := range entries {
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/envelopes/valid/" + e.Name())
			if err != nil {
				t.Fatalf("reading instance: %v", err)
			}
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("parsing instance: %v", err)
			}
			if err := sch.Validate(inst); err != nil {
				t.Errorf("schema validation failed: %v", err)
			}
			canon, err := canonicaljson.Marshal(raw)
			if err != nil {
				t.Fatalf("canonicalizing: %v", err)
			}
			if !bytes.Equal(canon, raw) {
				t.Errorf("instance is not canonical:\n file: %q\ncanon: %q", raw, canon)
			}
		})
	}
}

type invalidEntry struct {
	File     string `json:"file"`
	Rejects  string `json:"rejects"`
	Category string `json:"category"`
	Reason   string `json:"reason"`
}

// Every invalid envelope instance must be rejected by the check its
// index entry records: "schema" means schema validation fails on the
// parsed value; "canonicalization" means the file's bytes are not a
// valid canonical encoding, with the entry's category pinning how —
// "not-canonical" (Marshal succeeds but returns different bytes) or an
// error category ("duplicate-key", "lone-surrogate") that Marshal's
// rejection must actually match, so a lost rejection rule can't hide
// behind a mere byte difference. The index and the directory must also
// agree exactly, so an instance can't be added without recording why it
// is invalid.
func TestInvalidEnvelopes(t *testing.T) {
	sch, _ := envelopeSchema(t)

	rawIndex, err := spec.FS.ReadFile("testdata/envelopes/invalid/index.json")
	if err != nil {
		t.Fatalf("reading index: %v", err)
	}
	var index []invalidEntry
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatalf("parsing index: %v", err)
	}

	indexed := map[string]bool{}
	for _, entry := range index {
		if indexed[entry.File] {
			t.Errorf("index lists %s more than once", entry.File)
		}
		indexed[entry.File] = true
	}
	entries, err := spec.FS.ReadDir("testdata/envelopes/invalid")
	if err != nil {
		t.Fatalf("listing invalid envelopes: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "index.json" {
			continue
		}
		if !indexed[e.Name()] {
			t.Errorf("instance %s is not listed in index.json", e.Name())
		}
	}

	for _, entry := range index {
		t.Run(entry.File, func(t *testing.T) {
			if entry.Reason == "" {
				t.Error("index entry has no reason")
			}
			raw, err := spec.FS.ReadFile("testdata/envelopes/invalid/" + entry.File)
			if err != nil {
				t.Fatalf("reading instance: %v", err)
			}
			switch entry.Rejects {
			case "schema":
				inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
				if err != nil {
					t.Fatalf("parsing instance (a schema-rejected instance must be parseable JSON): %v", err)
				}
				if err := sch.Validate(inst); err == nil {
					t.Errorf("schema accepted the instance; expected rejection: %s", entry.Reason)
				}
			case "canonicalization":
				canon, err := canonicaljson.Marshal(raw)
				switch entry.Category {
				case "not-canonical":
					if err != nil {
						t.Fatalf("Marshal errored (%v); a not-canonical instance must canonicalize to different bytes", err)
					}
					if bytes.Equal(canon, raw) {
						t.Errorf("bytes are canonical; expected rejection: %s", entry.Reason)
					}
				case "duplicate-key", "lone-surrogate", "not-one-value":
					if err == nil {
						t.Fatalf("Marshal accepted the instance; expected a %s rejection: %s", entry.Category, entry.Reason)
					}
					want := map[string]string{
						"duplicate-key":  "duplicate object key",
						"lone-surrogate": "lone surrogate",
						"not-one-value":  "trailing data",
					}[entry.Category]
					if !strings.Contains(err.Error(), want) {
						t.Errorf("Marshal rejected with %q, want a %s rejection (containing %q)", err, entry.Category, want)
					}
				default:
					t.Errorf("canonicalization entry has unknown category %q", entry.Category)
				}
			default:
				t.Errorf("index entry has unknown rejects value %q", entry.Rejects)
			}
		})
	}
}

// The vector corpus loads and passes the shape validation the loader in
// vectors.go enforces (unique names, exactly one of canonical/error).
// The byte-level behavior of each vector is enforced in
// engine/codec/canonicaljson's tests, which read this same loader.
func TestCanonicalizationVectorsLoad(t *testing.T) {
	if _, err := spec.CanonicalizationVectors(); err != nil {
		t.Fatal(err)
	}
}
