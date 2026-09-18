package resolve_test

import (
	"bytes"
	"encoding/json"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/writtendev/writ/internal/resolve"
	"github.com/writtendev/writ/spec"
)

const (
	anchorSchemaID     = "https://writ.dev/spec/anchor.schema.json"
	resolutionSchemaID = "https://writ.dev/spec/resolution.schema.json"
)

var getCompiledSchemas = sync.OnceValues(func() (*jsonschema.Schema, *jsonschema.Schema) {
	rawAnchor, err := spec.FS.ReadFile("schemas/anchor.schema.json")
	if err != nil {
		panic(err)
	}
	anchorDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawAnchor))
	if err != nil {
		panic(err)
	}

	rawRes, err := spec.FS.ReadFile("schemas/resolution.schema.json")
	if err != nil {
		panic(err)
	}
	resDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawRes))
	if err != nil {
		panic(err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource(anchorSchemaID, anchorDoc); err != nil {
		panic(err)
	}
	if err := c.AddResource(resolutionSchemaID, resDoc); err != nil {
		panic(err)
	}
	anchorSch, err := c.Compile(anchorSchemaID)
	if err != nil {
		panic(err)
	}
	resSch, err := c.Compile(resolutionSchemaID)
	if err != nil {
		panic(err)
	}
	return anchorSch, resSch
})

func FuzzResolve(f *testing.F) {
	// Seed with resolution vectors
	if cases, err := spec.ResolutionVectors(); err == nil {
		for _, c := range cases {
			var filesMapJSON []byte
			if targetBytes, err := json.Marshal(c.Target.Files); err == nil {
				filesMapJSON = targetBytes
			}
			f.Add([]byte(c.Anchor), filesMapJSON)
		}
	}

	// Seed with anchor vectors
	if entries, err := spec.FS.ReadDir("testdata/anchors/valid"); err == nil {
		for _, e := range entries {
			if raw, err := spec.FS.ReadFile("testdata/anchors/valid/" + e.Name()); err == nil {
				f.Add(raw, []byte(`{"main.go":"package main\n"}`))
			}
		}
	}

	anchorSch, resSch := getCompiledSchemas()

	f.Fuzz(func(t *testing.T, anchorData []byte, targetData []byte) {
		var filesMap map[string]string
		if err := json.Unmarshal(targetData, &filesMap); err != nil {
			return
		}
		files := make(map[string][]byte, len(filesMap))
		for k, v := range filesMap {
			if len(k) == 0 || k[0] == '/' {
				continue
			}
			files[k] = []byte(v)
		}
		tree := resolve.NewTree(files, resolve.SHA1)

		// ResolveRaw is the total read-side entry point (WRIT-252): it must
		// never panic on any byte input, schema-valid anchor or not, since
		// this is what materializeAnchors calls against whatever a peer
		// pushed. Run it before the schema-validity early return below,
		// which would otherwise skip every hostile shape this exists to
		// catch.
		var rawOutcome resolve.Resolution
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("ResolveRaw panicked on %q: %v", anchorData, r)
				}
			}()
			rawOutcome = resolve.ResolveRaw(anchorData, tree)
		}()
		// resolution.schema.json requires at least one of old/new; ResolveRaw
		// deliberately produces neither for a non-object anchor or one with
		// no old/new key (spec/resolution.md §Structural Pre-Check), so only
		// validate when there is a side to validate.
		if rawOutcome.Old != nil || rawOutcome.New != nil {
			rawJSON, err := json.Marshal(rawOutcome)
			if err != nil {
				t.Fatalf("marshaling ResolveRaw outcome: %v", err)
			}
			rawInst, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawJSON))
			if err != nil {
				t.Fatalf("decoding ResolveRaw outcome for schema validation: %v", err)
			}
			if err := resSch.Validate(rawInst); err != nil {
				t.Fatalf("ResolveRaw outcome failed schema validation: %v\noutcome: %s", err, string(rawJSON))
			}
		}

		anchorInst, err := jsonschema.UnmarshalJSON(bytes.NewReader(anchorData))
		if err != nil {
			return
		}
		if err := anchorSch.Validate(anchorInst); err != nil {
			return
		}

		anchor, err := resolve.ParseAnchor(anchorData)
		if err != nil {
			return
		}

		outcome := resolve.Resolve(anchor, tree)

		// Assert outcome validates against resolution.schema.json
		outcomeJSON, err := json.Marshal(outcome)
		if err != nil {
			t.Fatalf("marshaling outcome: %v", err)
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(outcomeJSON))
		if err != nil {
			t.Fatalf("decoding outcome JSON for schema validation: %v", err)
		}
		if err := resSch.Validate(inst); err != nil {
			t.Fatalf("resolution outcome failed schema validation: %v\noutcome: %s", err, string(outcomeJSON))
		}
	})
}
