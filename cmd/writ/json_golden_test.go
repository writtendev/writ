package main

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/internal/textdiff"
)

// updateGoldenFlag mirrors spec/fixtures' -update-golden convention
// (engine/schemasrc/helpers_test.go does the same) for this package's own
// golden files, now that cmd/writ imports neither spec nor spec/fixtures.
var updateGoldenFlag = flag.Bool("update-golden", false, "update golden files instead of checking against them")

// updateGolden reports whether golden file updates have been requested via
// -update-golden or the WRIT_UPDATE_GOLDEN / UPDATE_GOLDEN environment
// variables.
func updateGolden() bool {
	if updateGoldenFlag != nil && *updateGoldenFlag {
		return true
	}
	env := os.Getenv("WRIT_UPDATE_GOLDEN")
	if env == "" {
		env = os.Getenv("UPDATE_GOLDEN")
	}
	return env == "1" || strings.EqualFold(env, "true")
}

func maskGoldenTimestamps(data []byte) []byte {
	re := regexp.MustCompile(`"(created_at|updated_at)":"[^"]+"`)
	return re.ReplaceAll(data, []byte(`"$1":"2026-01-01T00:00:00Z"`))
}

// maskGoldenObjectID replaces every occurrence of a freshly minted (32-hex,
// crypto/rand) object id with a fixed placeholder, so a golden file pinning
// `object.*` output stays byte-for-byte stable across runs.
func maskGoldenObjectID(data []byte, id string) []byte {
	return bytes.ReplaceAll(data, []byte(id), []byte("0123456789abcdef0123456789abcdef"))
}

func compareOrUpdateGolden(t *testing.T, goldenName string, got []byte) {
	t.Helper()
	goldenPath := filepath.Join("testdata", "golden", goldenName)

	if updateGolden() {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden %s failed: %v", goldenPath, err)
		}
		t.Logf("[UPDATED GOLDEN] %s (%d bytes)", goldenPath, len(got))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("golden file %s does not exist; run with -update-golden to generate it", goldenPath)
		}
		t.Fatalf("read golden %s failed: %v", goldenPath, err)
	}

	if !bytes.Equal(got, want) {
		diff := textdiff.Diff(goldenPath+" (golden)", want, "got (actual output)", got)
		t.Errorf("output does not match golden file %s\n\n%s", goldenPath, diff)
	}
}

func TestGolden_SyncStatus(t *testing.T) {
	_, aliceDir, _ := setupSyncTestHarness(t)
	ctx := context.Background()

	// Alice declares and syncs the ticket type first, so it is not itself
	// the unsynced op this golden captures.
	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Alice failed: %v", err)
	}
	applyTicketSchemaViaStore(t, ctx, sA, aliceDir)
	sA.Close()
	var stdoutSchema, stderrSchema bytes.Buffer
	if code := run(ctx, []string{"-C", aliceDir, "sync"}, &stdoutSchema, &stderrSchema); code != 0 {
		t.Fatalf("Alice schema sync exited with %d; stderr: %s", code, stderrSchema.String())
	}

	// Alice creates an object so there is 1 unsynced op.
	sA, err = writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Reopen Alice failed: %v", err)
	}
	_, err = sA.Objects.Create(ctx, "ticket", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Unsynced Op Ticket"},
	})
	if err != nil {
		sA.Close()
		t.Fatalf("Alice create object: %v", err)
	}
	sA.Close()

	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"-C", aliceDir, "sync", "--status", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("sync --status --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "sync_status.json", stdout.Bytes())
}

func TestGolden_SyncResult(t *testing.T) {
	_, aliceDir, _ := setupSyncTestHarness(t)
	ctx := context.Background()

	// Alice declares and syncs the ticket type first, so it is not itself
	// the op this golden captures being pushed.
	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Alice failed: %v", err)
	}
	applyTicketSchemaViaStore(t, ctx, sA, aliceDir)
	sA.Close()
	var stdoutSchema, stderrSchema bytes.Buffer
	if code := run(ctx, []string{"-C", aliceDir, "sync"}, &stdoutSchema, &stderrSchema); code != 0 {
		t.Fatalf("Alice schema sync exited with %d; stderr: %s", code, stderrSchema.String())
	}

	// Alice creates an object and then syncs.
	sA, err = writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Reopen Alice failed: %v", err)
	}
	_, err = sA.Objects.Create(ctx, "ticket", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Sync Result Ticket"},
	})
	if err != nil {
		sA.Close()
		t.Fatalf("Alice create object: %v", err)
	}
	sA.Close()

	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"-C", aliceDir, "sync", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("sync --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "sync_result.json", stdout.Bytes())
}

// TestGolden_SchemaShow pins `writ schema show <type> --json`. Store.Types
// sorts by name and the payload carries no ids or timestamps, so it needs
// no masking.
func TestGolden_SchemaShow(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"schema", "show", "-C", env.repoDir, "ticket", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("schema show --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "schema_show.json", stdout.Bytes())
}

// TestGolden_ObjectCreate pins `writ object create --json`. The object id
// is a fresh crypto/rand value every run, so it is masked to a fixed
// placeholder before comparison.
func TestGolden_ObjectCreate(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-field", "title=Fix the thing",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object create --json failed with %d; stderr: %s", code, stderr.String())
	}
	var created wire.ObjectCreated
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectCreate, &created)

	compareOrUpdateGolden(t, "object_create.json", maskGoldenObjectID(stdout.Bytes(), created.ObjectID))
}

// TestGolden_ObjectApply pins `writ object apply --json`, masking the
// existing object's id the same way TestGolden_ObjectCreate does.
func TestGolden_ObjectApply(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-field", "title=Fix the thing",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object create failed with %d; stderr: %s", code, stderr.String())
	}
	var created wire.ObjectCreated
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectCreate, &created)
	objectID := created.ObjectID

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{
		"object", "apply", "-C", env.repoDir, objectID, "update",
		"-field", "title=Renamed",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object apply --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "object_apply.json", maskGoldenObjectID(stdout.Bytes(), objectID))
}

// TestGolden_ObjectShow pins `writ object show --json`, folded straight
// from the DAG. The object id is masked the same way as create/apply.
func TestGolden_ObjectShow(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-field", "title=Fix the thing",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object create failed with %d; stderr: %s", code, stderr.String())
	}
	var created wire.ObjectCreated
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectCreate, &created)
	objectID := created.ObjectID

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{
		"object", "apply", "-C", env.repoDir, objectID, "update",
		"-field", "title=Renamed",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object apply failed with %d; stderr: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"object", "show", "-C", env.repoDir, objectID, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object show --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "object_show.json", maskGoldenObjectID(stdout.Bytes(), objectID))
}

// TestGolden_ObjectList pins `writ object list --json`. Both the object id
// and the created_at/updated_at timestamps vary run to run, so both are
// masked before comparison.
func TestGolden_ObjectList(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-field", "title=Fix the thing",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object create failed with %d; stderr: %s", code, stderr.String())
	}
	var created wire.ObjectCreated
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectCreate, &created)
	objectID := created.ObjectID

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"object", "list", "-C", env.repoDir, "ticket", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object list --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "object_list.json", maskGoldenObjectID(maskGoldenTimestamps(stdout.Bytes()), objectID))
}

func TestDeterminism_AllReadVerbs(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var created bytes.Buffer
	var createErr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-field", "title=Fix the thing",
		"--json",
	}, &created, &createErr)
	if code != 0 {
		t.Fatalf("object create failed with %d; stderr: %s", code, createErr.String())
	}
	var createdObj wire.ObjectCreated
	unmarshalEnvelopeData(t, created.Bytes(), wire.KindObjectCreate, &createdObj)

	readCommands := [][]string{
		{"object", "list", "-C", env.repoDir, "ticket", "--json"},
		{"object", "show", "-C", env.repoDir, createdObj.ObjectID, "--json"},
		{"schema", "show", "-C", env.repoDir, "ticket", "--json"},
	}

	for _, cmd := range readCommands {
		var out1, err1 bytes.Buffer
		code1 := run(context.Background(), cmd, &out1, &err1)
		if code1 != 0 {
			t.Fatalf("run 1 for %v failed: %s", cmd, err1.String())
		}

		var out2, err2 bytes.Buffer
		code2 := run(context.Background(), cmd, &out2, &err2)
		if code2 != 0 {
			t.Fatalf("run 2 for %v failed: %s", cmd, err2.String())
		}

		if !bytes.Equal(out1.Bytes(), out2.Bytes()) {
			t.Fatalf("determinism violation for command %v:\nrun 1: %s\nrun 2: %s", cmd, out1.String(), out2.String())
		}
	}
}

func TestEveryReadVerbHasJSON(t *testing.T) {
	readVerbs := []struct {
		name string
		args []string
	}{
		{name: "object list", args: []string{"object", "list", "-h"}},
		{name: "object show", args: []string{"object", "show", "-h"}},
		{name: "schema show", args: []string{"schema", "show", "-h"}},
		{name: "sync", args: []string{"sync", "-h"}},
	}

	for _, tc := range readVerbs {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tc.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("help for %s exited with %d; stderr: %s", tc.name, code, stderr.String())
			}

			combined := stdout.String() + stderr.String()
			if !strings.Contains(combined, "--json") && !strings.Contains(combined, "-json") {
				t.Errorf("read verb %q help output does not advertise --json flag:\n%s", tc.name, combined)
			}
		})
	}
}
