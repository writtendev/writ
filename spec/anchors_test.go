package spec_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/writtendev/writ/spec"
)

// Context-capture constants from spec/anchors.md §Context capture: ranges
// spanning more than selectedFullMax lines store the first and last
// headTail lines with the middle counted in "omitted".
const (
	selectedFullMax = 64
	headTail        = 32
)

const anchorSchemaID = "https://writ.dev/spec/anchor.schema.json"

// compileAnchorSchema compiles the anchor schema; compilation also
// validates it against the draft 2020-12 meta-schema.
func compileAnchorSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := spec.FS.ReadFile("schemas/anchor.schema.json")
	if err != nil {
		t.Fatalf("reading schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoding schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(anchorSchemaID, doc); err != nil {
		t.Fatalf("adding schema resource: %v", err)
	}
	sch, err := c.Compile(anchorSchemaID)
	if err != nil {
		t.Fatalf("compiling schema: %v", err)
	}
	return sch
}

// anchorInvariants enforces the cross-field rules of spec/anchors.md that
// JSON Schema cannot express. It assumes the instance already passed the
// schema; on schema-invalid input its answers are meaningless.
func anchorInvariants(a map[string]any) error {
	var oidLens []int
	for _, key := range []string{"old", "new"} {
		s, ok := a[key].(map[string]any)
		if !ok {
			continue
		}
		if err := sideInvariants(s, &oidLens); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	for _, n := range oidLens {
		if n != oidLens[0] {
			return fmt.Errorf("mixed OID lengths %v: one repo has one object format", oidLens)
		}
	}
	return nil
}

func sideInvariants(s map[string]any, oidLens *[]int) error {
	for _, f := range []string{"commit", "blob"} {
		if oid, ok := s[f].(string); ok {
			*oidLens = append(*oidLens, len(oid))
		}
	}
	if path, ok := s["path"].(string); ok {
		for _, seg := range strings.Split(path, "/") {
			if seg == "" || seg == "." || seg == ".." {
				return fmt.Errorf("path %q: segment %q not allowed", path, seg)
			}
		}
	}
	rng, ok := s["range"].(map[string]any)
	if !ok {
		return nil
	}
	start, sok := jsonInt(rng["start"])
	end, eok := jsonInt(rng["end"])
	if !sok || !eok {
		return fmt.Errorf("range start/end must be integers")
	}
	if end < start {
		return fmt.Errorf("range end %d < start %d", end, start)
	}
	size := end - start + 1
	ctx, ok := s["context"].(map[string]any)
	if !ok {
		return nil
	}
	lines, _ := ctx["lines"].([]any)
	omitted, hasOmitted := jsonInt(ctx["omitted"])
	if !hasOmitted {
		if int64(len(lines)) != size {
			return fmt.Errorf("context.lines has %d entries for a %d-line range and no omitted count", len(lines), size)
		}
		return nil
	}
	if len(lines) != selectedFullMax {
		return fmt.Errorf("omitted present but context.lines has %d entries, want %d (first %d + last %d)", len(lines), selectedFullMax, headTail, headTail)
	}
	if want := size - selectedFullMax; omitted != want {
		return fmt.Errorf("omitted is %d, want %d for a %d-line range", omitted, want, size)
	}
	return nil
}

func jsonInt(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok || f != float64(int64(f)) {
		return 0, false
	}
	return int64(f), true
}

// validateVector runs one anchor instance through schema and invariants.
func validateVector(t *testing.T, sch *jsonschema.Schema, raw []byte) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoding vector: %v", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	var a map[string]any
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("re-decoding vector: %v", err)
	}
	if err := anchorInvariants(a); err != nil {
		return fmt.Errorf("invariant: %w", err)
	}
	return nil
}

func readDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := spec.FS.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestAnchorSchemaCompiles(t *testing.T) {
	compileAnchorSchema(t)
}

func TestValidAnchorVectors(t *testing.T) {
	sch := compileAnchorSchema(t)
	for _, name := range readDirNames(t, "testdata/anchors/valid") {
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/anchors/valid/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateVector(t, sch, raw); err != nil {
				t.Errorf("valid vector rejected: %v", err)
			}
		})
	}
}

func TestInvalidAnchorVectors(t *testing.T) {
	sch := compileAnchorSchema(t)

	rawIndex, err := spec.FS.ReadFile("testdata/anchors/invalid/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatalf("decoding index.json: %v", err)
	}

	names := readDirNames(t, "testdata/anchors/invalid")
	files := make(map[string]bool)
	for _, name := range names {
		if name != "index.json" {
			files[name] = true
		}
	}
	for name := range index {
		if !files[name] {
			t.Errorf("index.json lists %s but the file does not exist", name)
		}
	}

	for name := range files {
		entry, ok := index[name]
		if !ok {
			t.Errorf("%s has no index.json entry recording its expected rejection", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/anchors/invalid/" + name)
			if err != nil {
				t.Fatal(err)
			}
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("decoding vector: %v", err)
			}
			schemaErr := sch.Validate(inst)
			switch entry.Kind {
			case "schema":
				if schemaErr == nil {
					t.Errorf("schema accepted it; expected rejection: %s", entry.Reason)
				}
			case "invariant":
				// Invariant vectors prove the rule lives outside the
				// schema: the schema must accept them.
				if schemaErr != nil {
					t.Errorf("schema rejected an invariant-kind vector (%v); expected only the invariant to fail: %s", schemaErr, entry.Reason)
				}
				var a map[string]any
				if err := json.Unmarshal(raw, &a); err != nil {
					t.Fatal(err)
				}
				if err := anchorInvariants(a); err == nil {
					t.Errorf("invariants accepted it; expected rejection: %s", entry.Reason)
				}
			default:
				t.Errorf("index.json kind %q unknown (want schema or invariant)", entry.Kind)
			}
		})
	}
}

// TestGitHubConversionVectors checks the anchor halves of the informative
// {github, pr, anchor} conversion vectors (spec/anchors.md appendix A).
// The conversion itself is exercised by whatever consumer imports these
// comments, once one exists.
func TestGitHubConversionVectors(t *testing.T) {
	sch := compileAnchorSchema(t)
	for _, name := range readDirNames(t, "testdata/anchors/github") {
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/anchors/github/" + name)
			if err != nil {
				t.Fatal(err)
			}
			var vec struct {
				GitHub json.RawMessage `json:"github"`
				PR     json.RawMessage `json:"pr"`
				Anchor json.RawMessage `json:"anchor"`
			}
			if err := json.Unmarshal(raw, &vec); err != nil {
				t.Fatalf("decoding vector: %v", err)
			}
			for field, v := range map[string]json.RawMessage{"github": vec.GitHub, "pr": vec.PR, "anchor": vec.Anchor} {
				if len(v) == 0 {
					t.Fatalf("vector is missing its %q member", field)
				}
			}
			if err := validateVector(t, sch, vec.Anchor); err != nil {
				t.Errorf("anchor member rejected: %v", err)
			}
		})
	}
}
