package schemasrc_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/spec"
)

// testObjectID is the fixed objectID every corpus case compiles under, so
// golden op sequences are stable across runs. Compile's objectID
// parameter has no default (WRIT-187): the corpus supplies one exactly as
// a real caller must.
const testObjectID = "sch-acme"

var updateGolden = flag.Bool("update-golden", false, "update engine/schemasrc golden op sequences and renderings")

// envelopesToOps wraps a compiled op sequence into codec.Ops suitable for
// state.FoldSchema: a single linear chain with strictly increasing
// author timestamps, so OrderWithTStar's total order matches emission
// order exactly — the property every corpus case's round-trip checks
// build on.
func envelopesToOps(envs []codec.Envelope) []codec.Op {
	base := time.Unix(1700000000, 0).UTC()
	ops := make([]codec.Op, len(envs))
	var parent string
	for i, env := range envs {
		id := fmt.Sprintf("op-%03d", i)
		var parents []string
		if parent != "" {
			parents = []string{parent}
		}
		ops[i] = codec.Op{
			Envelope: env,
			ID:       id,
			Parents:  parents,
			Author:   codec.Identity{When: base.Add(time.Duration(i) * time.Minute)},
		}
		parent = id
	}
	return ops
}

// schemaOpsSchema compiles spec/schemas/schema-ops.schema.json (which
// allOf-refs op-envelope.schema.json), the same way spec/schema_ops_test.go
// does, so this package's own tests can validate every op this package
// emits against the normative wire schema without duplicating it.
func schemaOpsSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	const (
		envelopeSchemaID  = "https://writ.dev/spec/op-envelope.schema.json"
		schemaOpsSchemaID = "https://writ.dev/spec/schema-ops.schema.json"
	)

	envRaw, err := spec.FS.ReadFile("schemas/op-envelope.schema.json")
	if err != nil {
		t.Fatalf("reading envelope schema: %v", err)
	}
	envDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(envRaw))
	if err != nil {
		t.Fatalf("decoding envelope schema: %v", err)
	}

	schRaw, err := spec.FS.ReadFile("schemas/schema-ops.schema.json")
	if err != nil {
		t.Fatalf("reading schema-ops schema: %v", err)
	}
	schDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schRaw))
	if err != nil {
		t.Fatalf("decoding schema-ops schema: %v", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource(envelopeSchemaID, envDoc); err != nil {
		t.Fatalf("adding envelope schema resource: %v", err)
	}
	if err := c.AddResource(schemaOpsSchemaID, schDoc); err != nil {
		t.Fatalf("adding schema-ops schema resource: %v", err)
	}
	sch, err := c.Compile(schemaOpsSchemaID)
	if err != nil {
		t.Fatalf("compiling schema-ops schema: %v", err)
	}
	return sch
}

// validateAgainstWireSchema asserts env's on-wire JSON shape validates
// against spec/schemas/schema-ops.schema.json.
func validateAgainstWireSchema(t *testing.T, sch *jsonschema.Schema, env codec.Envelope) {
	t.Helper()
	raw, err := jsonMarshalEnvelope(env)
	if err != nil {
		t.Fatalf("marshaling envelope: %v", err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoding envelope as schema instance: %v", err)
	}
	if err := sch.Validate(instance); err != nil {
		t.Errorf("envelope %s/%d fails schema-ops.schema.json: %v", env.OpType, env.OpVersion, err)
	}
}

func jsonMarshalEnvelope(env codec.Envelope) ([]byte, error) {
	return json.Marshal(env)
}

func readGolden(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("golden file %s does not exist; run with -update-golden to create it", path)
		}
		t.Fatalf("reading golden file %s: %v", path, err)
	}
	return b
}

func writeGoldenIfUpdating(t *testing.T, path string, content []byte) {
	t.Helper()
	if !*updateGolden {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating golden dir: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writing golden file %s: %v", path, err)
	}
}

func compareOrUpdateGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	writeGoldenIfUpdating(t, path, got)
	want := readGolden(t, path)
	if !bytes.Equal(want, got) {
		t.Errorf("golden mismatch for %s:\n--- want ---\n%s\n--- got ---\n%s", path, want, got)
	}
}
