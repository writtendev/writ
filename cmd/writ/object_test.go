package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/engine"
)

// ticketObjectTestSchema declares a type writ has never heard of, using the
// neutral example type AGENTS.md and spec/schema-source.md use, with one
// field per value type `object.go`'s -field parsing has to convert.
const ticketObjectTestSchema = `namespace acme
description "Ticket vocabulary"

type ticket {
  op create 1, update 1 {
    title     string    lww
    estimate  number    lww
    count     int       lww
    done      bool      lww
    meta      anchor    lww
    tags      [string]  set-union
  }
}
`

func applyTicketObjectSchema(t *testing.T, dir string) {
	t.Helper()
	writeSchemaFile(t, dir, ticketObjectTestSchema)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", dir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}
}

func unmarshalEnvelopeData(t *testing.T, body []byte, wantKind string, out any) {
	t.Helper()
	var env wire.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v (body: %s)", err, body)
	}
	if env.Kind != wantKind {
		t.Fatalf("kind = %q, want %q", env.Kind, wantKind)
	}
	if env.SchemaVersion != wire.CurrentSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", env.SchemaVersion, wire.CurrentSchemaVersion)
	}
	data, err := json.Marshal(env.Data)
	if err != nil {
		t.Fatalf("remarshal envelope data: %v", err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("unmarshal envelope data into %T: %v", out, err)
	}
}

// TestObjectCLI_EndToEnd_NeverHeardOfType is the acceptance walk from the
// ticket: create -> apply -> show -> list, all against a type no Go type in
// this repository is named after.
func TestObjectCLI_EndToEnd_NeverHeardOfType(t *testing.T) {
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
	if created.ObjectType != "ticket" {
		t.Errorf("object_type = %q, want ticket", created.ObjectType)
	}
	if len(created.ObjectID) != 32 {
		t.Fatalf("unexpected object id %q", created.ObjectID)
	}
	objectID := created.ObjectID

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{
		"object", "apply", "-C", env.repoDir, objectID, "update",
		"-field", "title=Renamed",
		"-field", "estimate=2.5",
		"-field", "count=3",
		"-field", "done=true",
		"-field", `meta={"version":1,"old":{"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","path":"a","blob":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`,
		"-field", "tags=urgent",
		"-field", "tags=backend",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object apply failed with %d; stderr: %s", code, stderr.String())
	}
	var applied wire.ObjectApplied
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectApply, &applied)
	if applied.ObjectID != objectID || applied.OpType != "update" {
		t.Errorf("unexpected ObjectApplied: %+v", applied)
	}

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"object", "show", "-C", env.repoDir, objectID, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object show failed with %d; stderr: %s", code, stderr.String())
	}
	var obj wire.Object
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectShow, &obj)
	if obj.ObjectID != objectID || obj.ObjectType != "ticket" {
		t.Fatalf("unexpected Object: %+v", obj)
	}
	if title, _ := obj.Fields["title"].(string); title != "Renamed" {
		t.Errorf("title = %v, want Renamed", obj.Fields["title"])
	}
	if est, _ := obj.Fields["estimate"].(float64); est != 2.5 {
		t.Errorf("estimate = %v, want 2.5", obj.Fields["estimate"])
	}
	if cnt, _ := obj.Fields["count"].(float64); cnt != 3 {
		t.Errorf("count = %v, want 3", obj.Fields["count"])
	}
	if done, _ := obj.Fields["done"].(bool); !done {
		t.Errorf("done = %v, want true", obj.Fields["done"])
	}
	meta, ok := obj.Fields["meta"].(map[string]any)
	if !ok || meta["version"] != float64(1) {
		t.Errorf("meta = %#v, want an anchor object with version 1", obj.Fields["meta"])
	}
	tags, ok := obj.Fields["tags"].([]any)
	if !ok || len(tags) != 2 {
		t.Fatalf("tags = %#v, want a 2-element array", obj.Fields["tags"])
	}
	if len(obj.UnknownOps) != 0 {
		t.Errorf("unexpected unknown ops: %+v", obj.UnknownOps)
	}

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"object", "list", "-C", env.repoDir, "ticket", "-text", "Renamed", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object list failed with %d; stderr: %s", code, stderr.String())
	}
	var results []wire.ObjectSummary
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectList, &results)
	if len(results) != 1 || results[0].ObjectID != objectID {
		t.Fatalf("object list = %#v, want exactly one result for %s", results, objectID)
	}
}

// TestObjectCLI_FieldValueTypeErrors pins the acceptance case: a value that
// fails its declared value type is refused at the CLI, naming the field and
// value type, and appends nothing. This includes NaN/Inf (round-2 finding):
// strconv.ParseFloat accepts them, so convertFieldValue must reject them
// itself rather than let them reach json.Marshal(op.Fields) and die there
// with a message naming neither the field nor its value type.
func TestObjectCLI_FieldValueTypeErrors(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	for _, bad := range []string{"abc", "NaN", "Inf", "+Inf", "-Inf", "infinity"} {
		t.Run(bad, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{
				"object", "create", "-C", env.repoDir, "ticket", "create",
				"-field", "title=Something",
				"-field", "estimate=" + bad,
			}, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("expected a non-zero exit for an invalid number value, got 0 (stdout: %s)", stdout.String())
			}
			if !strings.Contains(stderr.String(), "estimate") {
				t.Errorf("stderr does not name the field: %q", stderr.String())
			}
			if !strings.Contains(stderr.String(), "number") {
				t.Errorf("stderr does not name the declared value type: %q", stderr.String())
			}
		})
	}

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"object", "list", "-C", env.repoDir, "ticket", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("object list failed: %s", stderr.String())
	}
	var results []wire.ObjectSummary
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectList, &results)
	if len(results) != 0 {
		t.Fatalf("expected nothing appended after the refused creates, got %d objects", len(results))
	}
}

// TestObjectCLI_UndeclaredField pins the acceptance case for a field the op
// does not declare at all.
func TestObjectCLI_UndeclaredField(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-field", "nosuchfield=x",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected a non-zero exit for an undeclared field, got 0")
	}
	if !strings.Contains(stderr.String(), "nosuchfield") {
		t.Errorf("stderr does not name the undeclared field: %q", stderr.String())
	}
}

// TestObjectCLI_UnknownTypeAndOp pins the acceptance case: an unknown op
// type or object type each fail naming what the installed vocabulary does
// declare.
func TestObjectCLI_UnknownTypeAndOp(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"object", "create", "-C", env.repoDir, "ticket", "nosuchop"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected a non-zero exit for an unknown op type, got 0")
	}
	if !strings.Contains(stderr.String(), "nosuchop") || !strings.Contains(stderr.String(), "ticket") {
		t.Errorf("stderr does not name the unknown op / type: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "create") || !strings.Contains(stderr.String(), "update") {
		t.Errorf("stderr does not name what ticket does declare (create, update): %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"object", "create", "-C", env.repoDir, "nosuchtype", "create"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected a non-zero exit for an unknown object type, got 0")
	}
	if !strings.Contains(stderr.String(), "nosuchtype") {
		t.Errorf("stderr does not name the unknown type: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "ticket") {
		t.Errorf("stderr does not name what the vocabulary does declare (ticket): %q", stderr.String())
	}
}

// TestObjectCLI_List_UnknownType pins the round-2 finding: a typo'd or
// renamed <type> positional to `object list` must be refused by name, the
// same way `object create` and `schema show` already refuse it, rather than
// silently returning an empty result indistinguishable from "no objects".
func TestObjectCLI_List_UnknownType(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"object", "list", "-C", env.repoDir, "nosuchtype"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected a non-zero exit for an unknown object type, got 0 (stdout: %s)", stdout.String())
	}
	if !strings.Contains(stderr.String(), "nosuchtype") {
		t.Errorf("stderr does not name the unknown type: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "ticket") {
		t.Errorf("stderr does not name what the vocabulary does declare (ticket): %q", stderr.String())
	}
}

// TestObjectCLI_List_SchemaType pins the round-3 finding: "schema" is
// Store.Types' one deliberate omission (it is writ's hard-coded object
// type, not schema-declared), but `writ schema apply` leaves real "schema"
// rows in the projection, so `object list schema` must list them rather
// than refusing "schema" as undeclared the way TestObjectCLI_List_UnknownType
// expects for an actual typo.
func TestObjectCLI_List_SchemaType(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"object", "list", "-C", env.repoDir, "schema", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object list schema failed with %d; stderr: %s", code, stderr.String())
	}
	var summaries []wire.ObjectSummary
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectList, &summaries)
	if len(summaries) == 0 {
		t.Fatalf("expected at least one schema object, got none")
	}
	for _, s := range summaries {
		if s.ObjectType != "schema" {
			t.Errorf("object_type = %q, want schema", s.ObjectType)
		}
	}
}

// TestObjectCLI_ExplicitOpVersion_Undeclared pins the minor finding from
// round 1: an explicit -op-version the vocabulary doesn't declare must be
// refused by resolveOpVersion itself, naming the versions ticket create
// does declare -- not surface later as parseFieldFlags blaming a field
// that is, in fact, declared for the version the caller meant.
func TestObjectCLI_ExplicitOpVersion_Undeclared(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-op-version", "7", "-field", "title=x",
	}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected a non-zero exit for an undeclared op version, got 0")
	}
	if strings.Contains(stderr.String(), "declares no fields") {
		t.Errorf("stderr blames the field instead of the version: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "7") || !strings.Contains(stderr.String(), "1") {
		t.Errorf("stderr does not name the requested version and the declared version: %q", stderr.String())
	}
}

// TestObjectCLI_ResolveObjectID_Prefix exercises resolveObjectID's prefix
// path (cmd/writ/store.go): a short, unambiguous prefix resolves the same
// object a full id does.
func TestObjectCLI_ResolveObjectID_Prefix(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-field", "title=Only one",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object create failed: %s", stderr.String())
	}
	var created wire.ObjectCreated
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectCreate, &created)

	prefix := created.ObjectID[:8]
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"object", "show", "-C", env.repoDir, prefix, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object show by prefix failed: %s", stderr.String())
	}
	var obj wire.Object
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectShow, &obj)
	if obj.ObjectID != created.ObjectID {
		t.Fatalf("resolved id = %s, want %s", obj.ObjectID, created.ObjectID)
	}
}

// TestObjectCLI_Show_ByteIdenticalAfterRebuild pins the acceptance case:
// `object show --json` reads from the DAG, never the projection, so a
// dropped-and-rebuilt cache must not change its output.
func TestObjectCLI_Show_ByteIdenticalAfterRebuild(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-field", "title=Stable",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object create failed: %s", stderr.String())
	}
	var created wire.ObjectCreated
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectCreate, &created)

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"object", "show", "-C", env.repoDir, created.ObjectID, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("object show (before) failed: %s", stderr.String())
	}
	before := append([]byte(nil), stdout.Bytes()...)

	store, err := writ.Open(env.repoDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := store.Rebuild(context.Background()); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	store.Close()

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"object", "show", "-C", env.repoDir, created.ObjectID, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("object show (after) failed: %s", stderr.String())
	}

	if !bytes.Equal(before, stdout.Bytes()) {
		t.Fatalf("object show output differs after rebuild:\nbefore: %s\nafter:  %s", before, stdout.Bytes())
	}
}

// TestSchemaShowCLI pins schema show's two forms: a bare type-name list,
// and one type's full detail under --json.
func TestSchemaShowCLI(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"schema", "show", "-C", env.repoDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("schema show failed with %d; stderr: %s", code, stderr.String())
	}
	names := strings.Fields(stdout.String())
	var foundTicket, foundBuiltin bool
	for _, n := range names {
		if n == "ticket" {
			foundTicket = true
		}
		if n == "review" {
			foundBuiltin = true
		}
	}
	if !foundTicket {
		t.Errorf("schema show did not list the log-declared type ticket:\n%s", stdout.String())
	}
	if !foundBuiltin {
		t.Errorf("schema show did not list the built-in type review:\n%s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"schema", "show", "-C", env.repoDir, "ticket", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("schema show ticket --json failed with %d; stderr: %s", code, stderr.String())
	}
	var typeInfo wire.SchemaTypeInfo
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindSchemaShow, &typeInfo)
	if typeInfo.Name != "ticket" {
		t.Errorf("type = %q, want ticket", typeInfo.Name)
	}
	if len(typeInfo.Fields) == 0 {
		t.Errorf("expected fields for ticket, got none")
	}
}

// widgetTargetTestSchema declares one field twice under two different ops:
// once under the default target (the field name) and once renamed via
// `target(...)` -- the round-2 finding's `label`/`label_v2` example.
const widgetTargetTestSchema = `namespace acme
description "Widget vocabulary"

type widget {
  op create 1 {
    label  string  lww
  }
  op rename 1 {
    label  string  lww  target(label_v2)
  }
}
`

// TestSchemaShowCLI_FieldTable_Target pins the round-2 finding: the human
// `schema show <type>` field table must print `target` when a field's
// storage target differs from its declared name, the one attribute needed
// to reconcile `-field <name>` on `object create` with `object show`'s
// target-keyed output. A field whose target is undeclared (equal to its own
// name) prints no target, matching wire.SchemaTypeField's own `omitempty`.
func TestSchemaShowCLI_FieldTable_Target(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, widgetTargetTestSchema)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), []string{"schema", "show", "-C", env.repoDir, "widget"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("schema show widget failed with %d; stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "create") && strings.Contains(line, "label") {
			if strings.Contains(line, "label_v2") {
				t.Errorf("create's label declares no target override, want no target in the row: %q", line)
			}
		}
		if strings.Contains(line, "rename") && strings.Contains(line, "label") {
			if !strings.Contains(line, "label_v2") {
				t.Errorf("rename's label targets label_v2, want it named in the row: %q", line)
			}
		}
	}
}

// TestSchemaShowCLI_SchemaType pins the round-3 finding's second surface:
// `schema show schema` must not fail as "not declared" either, even though
// Store.Types never returns a "schema" entry (its vocabulary is
// hard-coded, not schema-declared) -- it reports the bare type name with
// no fields/ops, rather than inventing data this API has no source for.
func TestSchemaShowCLI_SchemaType(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"schema", "show", "-C", env.repoDir, "schema", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("schema show schema failed with %d; stderr: %s", code, stderr.String())
	}
	var typeInfo wire.SchemaTypeInfo
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindSchemaShow, &typeInfo)
	if typeInfo.Name != "schema" {
		t.Errorf("type = %q, want schema", typeInfo.Name)
	}
	if len(typeInfo.Fields) != 0 || len(typeInfo.Ops) != 0 {
		t.Errorf("expected no fields/ops for schema, got fields=%v ops=%v", typeInfo.Fields, typeInfo.Ops)
	}

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"schema", "show", "-C", env.repoDir, "schema"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("schema show schema (human) failed with %d; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "schema") {
		t.Errorf("stdout does not name the type: %q", stdout.String())
	}
}

// TestSchemaShowCLI_UnknownType pins the not-declared-type error path.
func TestSchemaShowCLI_UnknownType(t *testing.T) {
	env := initTestRepo(t)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"schema", "show", "-C", env.repoDir, "nosuchtype"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected a non-zero exit for an unknown type, got 0")
	}
	if !strings.Contains(stderr.String(), "nosuchtype") {
		t.Errorf("stderr does not name the unknown type: %q", stderr.String())
	}
}
