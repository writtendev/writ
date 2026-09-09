package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/engine"
)

// writeSchemaFile overwrites the working-tree writ.schema in dir.
func writeSchemaFile(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "writ.schema"), []byte(content), 0o644); err != nil {
		t.Fatalf("writing writ.schema: %v", err)
	}
}

// initTestRepo runs `writ init` (which also writes the starter writ.schema)
// and returns the CLI environment, signed and ready for schema plan/apply.
func initTestRepo(t *testing.T) testCLIEnv {
	t.Helper()
	env := setupTestCLIEnv(t)
	setupSigningKey(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"init", "-C", env.repoDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("writ init failed with %d; stderr: %s", code, stderr.String())
	}
	return env
}

// writSchemaRef returns the current tip commit of the sole
// refs/writ/<writer>/schema ref in dir, or "" if it does not exist yet.
func writSchemaRef(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "for-each-ref", "--format=%(objectname)", "refs/writ/*/schema")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git for-each-ref: %v", err)
	}
	lines := strings.Fields(string(out))
	switch len(lines) {
	case 0:
		return ""
	case 1:
		return lines[0]
	default:
		t.Fatalf("expected at most one schema chain, got %d: %v", len(lines), lines)
		return ""
	}
}

const fullTestSchema = `namespace acme
description "Acme's vocabulary"

type standup {
  description "A daily standup update"

  op create 1, update 1 {
    title      string(200)       lww
    body       text              multi-value
    author     person-ref        create-once
    attendees  [person-ref]      set-observed-remove
    position   position          lww
    state      enum(open, done)  lattice(open, done)
  }

  op approval 1 {
    description "Approve or block a standup"
    verdict  enum(approve, block)  keyed-lww  key(subject person-ref)
    message  string                keyed-lww  key(subject person-ref)
  }
}
`

// reorderedTestSchema is fullTestSchema with its op blocks and field lines
// reordered, reindented, and commented — the regression net for comparing
// compiled declarations against folded state rather than source text
// (WRIT-191's "comments hazard").
const reorderedTestSchema = `# Acme's namespace
namespace acme
description "Acme's vocabulary"  # trailing comment

type standup {
        description "A daily standup update"
        # reindented, reordered op blocks, extra comments throughout

        op approval 1 {
                description "Approve or block a standup"
                message string keyed-lww key(subject person-ref)   # msg
                verdict enum(approve, block) keyed-lww key(subject person-ref)
        }

        op update 1, create 1 {
                state enum(open, done) lattice(open, done)
                position position lww
                attendees [person-ref] set-observed-remove
                author person-ref create-once
                body text multi-value
                title string(200) lww
        }
}
`

func TestSchemaCLI_PlanRefusesMissingFile(t *testing.T) {
	env := setupTestCLIEnv(t)
	setupSigningKey(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for missing writ.schema, got %d; stdout: %s stderr: %s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "writ init") {
		t.Errorf("expected stderr to point at `writ init`, got: %s", stderr.String())
	}
}

// TestSchemaCLI_Idempotence is the central test WRIT-191 names: apply on a
// fresh repo, then plan reports zero ops / up_to_date / exit 0; apply a
// second time appends nothing, which this asserts directly against the
// writer's own chain tip rather than trusting `apply`'s stdout.
func TestSchemaCLI_Idempotence(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer

	// plan on a fresh repo: this is a creation.
	code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("first plan failed with %d; stderr: %s", code, stderr.String())
	}
	var planEnv wire.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &planEnv); err != nil {
		t.Fatalf("unmarshal plan envelope: %v", err)
	}
	if planEnv.Kind != wire.KindSchemaPlan {
		t.Fatalf("kind = %q, want %q", planEnv.Kind, wire.KindSchemaPlan)
	}
	planData, _ := json.Marshal(planEnv.Data)
	var plan wire.SchemaPlan
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatalf("unmarshal SchemaPlan: %v", err)
	}
	if !plan.Created {
		t.Errorf("expected created=true on a fresh repo, got false")
	}
	if plan.UpToDate {
		t.Errorf("expected up_to_date=false on a fresh repo, got true")
	}
	if len(plan.Ops) == 0 {
		t.Errorf("expected a non-empty op sequence, got none")
	}
	// The schema object id is derived from the namespace
	// (spec/identifiers.md's schema carve-out), so a creation plan's id is
	// exact, not a fabricated preview: apply writes to this same id.
	if plan.ObjectID != deriveSchemaObjectID(plan.Namespace) {
		t.Errorf("expected object_id %q on a creation plan, got %q", deriveSchemaObjectID(plan.Namespace), plan.ObjectID)
	}

	// apply: the ref must not exist yet.
	if tip := writSchemaRef(t, env.repoDir); tip != "" {
		t.Fatalf("expected no schema chain before apply, found tip %s", tip)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("apply failed with %d; stderr: %s", code, stderr.String())
	}
	tipAfterFirstApply := writSchemaRef(t, env.repoDir)
	if tipAfterFirstApply == "" {
		t.Fatalf("expected a schema chain tip after apply, found none")
	}

	// plan again: up to date, zero ops, exit 0.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("second plan failed with %d; stderr: %s", code, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &planEnv); err != nil {
		t.Fatalf("unmarshal plan envelope: %v", err)
	}
	planData, _ = json.Marshal(planEnv.Data)
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatalf("unmarshal SchemaPlan: %v", err)
	}
	if !plan.UpToDate {
		t.Fatalf("expected up_to_date=true after apply, got false; ops: %+v", plan.Ops)
	}
	if len(plan.Ops) != 0 {
		t.Fatalf("expected zero ops after apply, got %d: %+v", len(plan.Ops), plan.Ops)
	}
	if plan.Created {
		t.Errorf("expected created=false once the object exists, got true")
	}
	// A reuse plan's target is real (already folded from the log).
	if plan.ObjectID == "" {
		t.Errorf("expected object_id to be present on a reuse plan, got empty")
	}

	// apply a second time: the writer's chain tip must not move.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("second apply failed with %d; stderr: %s", code, stderr.String())
	}
	tipAfterSecondApply := writSchemaRef(t, env.repoDir)
	if tipAfterSecondApply != tipAfterFirstApply {
		t.Fatalf("writer's schema chain tip moved on a no-op apply: %s -> %s", tipAfterFirstApply, tipAfterSecondApply)
	}
}

// TestSchemaCLI_CommentsHazard is WRIT-191's regression net for comparing
// compiled declarations against folded state rather than source text:
// reordering types and op blocks, reindenting, and adding comments to an
// already-applied file must report zero ops.
func TestSchemaCLI_CommentsHazard(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("apply failed with %d; stderr: %s", code, stderr.String())
	}

	writeSchemaFile(t, env.repoDir, reorderedTestSchema)

	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("plan after reorder/reindent/comments failed with %d; stderr: %s", code, stderr.String())
	}
	var envW wire.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envW); err != nil {
		t.Fatalf("unmarshal plan envelope: %v", err)
	}
	planData, _ := json.Marshal(envW.Data)
	var plan wire.SchemaPlan
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatalf("unmarshal SchemaPlan: %v", err)
	}
	if !plan.UpToDate || len(plan.Ops) != 0 {
		t.Fatalf("reordering/reindenting/commenting an applied file should report zero ops; got up_to_date=%v ops=%+v", plan.UpToDate, plan.Ops)
	}
}

func TestSchemaCLI_Refusals(t *testing.T) {
	base := `namespace acme
description "Acme's vocabulary"

type standup {
  description "A daily standup update"

  op create 1 {
    title  string(200)  lww
  }
}
`
	tests := []struct {
		name    string
		edited  string
		wantMsg string
	}{
		{
			name: "removed type",
			edited: `namespace acme
description "Acme's vocabulary"
`,
			wantMsg: "was removed",
		},
		{
			name: "removed field",
			edited: `namespace acme
description "Acme's vocabulary"

type standup {
  description "A daily standup update"

  op create 1 {
  }
}
`,
			wantMsg: "was removed",
		},
		{
			name: "removed description",
			edited: `namespace acme

type standup {
  description "A daily standup update"

  op create 1 {
    title  string(200)  lww
  }
}
`,
			wantMsg: "description",
		},
		{
			name: "un-deprecated type",
			edited: `namespace acme
description "Acme's vocabulary"

type standup {
  description "A daily standup update"

  op create 1 {
    title  string(200)  lww
  }
}
`,
			wantMsg: "un-deprecated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := initTestRepo(t)
			writeSchemaFile(t, env.repoDir, base)

			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
				t.Fatalf("initial apply failed with %d; stderr: %s", code, stderr.String())
			}

			if tt.name == "un-deprecated type" {
				// Deprecate first, apply, then remove the keyword: only
				// then is un-deprecation the thing being refused.
				deprecated := strings.Replace(base, "type standup {", "type standup deprecated {", 1)
				writeSchemaFile(t, env.repoDir, deprecated)
				stdout.Reset()
				stderr.Reset()
				if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
					t.Fatalf("deprecate apply failed with %d; stderr: %s", code, stderr.String())
				}
			}

			tipBefore := writSchemaRef(t, env.repoDir)

			writeSchemaFile(t, env.repoDir, tt.edited)

			stdout.Reset()
			stderr.Reset()
			code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan"}, &stdout, &stderr)
			if code != 1 {
				t.Fatalf("expected exit 1, got %d; stdout: %s stderr: %s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantMsg) {
				t.Errorf("expected refusal message to mention %q, got: %s", tt.wantMsg, stderr.String())
			}

			// A refused plan must never append anything, and apply runs
			// the same computation, so it must refuse identically.
			stdout.Reset()
			stderr.Reset()
			code = run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr)
			if code != 1 {
				t.Fatalf("expected apply to also refuse with exit 1, got %d", code)
			}
			if tip := writSchemaRef(t, env.repoDir); tip != tipBefore {
				t.Fatalf("a refused apply appended ops: chain tip moved %s -> %s", tipBefore, tip)
			}
		})
	}
}

// TestSchemaCLI_RefusesAttributeNarrowing is the round-3 regression net for
// the major finding on PR #159: state.FoldSchema's define-field case
// overwrites a register (value_type, enum, max_length, lattice, key,
// key_types, target) only when a later op's body actually carries that
// key, so a define-field op that narrows one of these — the file stops
// declaring it, but the log already holds it for that field — does not
// clear it; it leaves the log holding both the new value and the stale
// old one, forever, with no representable way to fix it in place
// (spec/schema-ops.md §8.1, ARCHITECTURE.md §Schema layer, WRIT-200:
// there deliberately is no op that clears a single attribute — narrowing
// takes a new op_version instead, a distinct target only for the
// attributes schemaFieldTargetSensitive names — see its comment for why
// those four and not the ones the fold merely reads per op).
// This is `schemaRemovals`'s attribute check treating that exactly like any
// other removal: refuse, name the field and the attribute, append nothing.
//
// Both cases here are the two round-3 reproductions verbatim: narrowing an
// enum field to a bare string (which also happens to leave behind a stale
// `enum` alongside a `value_type` spec.ValidateFieldRule would reject —
// the major finding's own silent-corruption path before this fix), and
// narrowing a bounded string to an unbounded one (the medium finding's
// non-convergent loop — max_length is a valid attribute to drop on its own
// terms, so only the attribute-narrowing refusal catches it at all).
func TestSchemaCLI_RefusesAttributeNarrowing(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		edited  string
		wantMsg string
	}{
		{
			name: "enum narrowed to string",
			base: `namespace acme

type standup {
  op create 1 {
    state  enum(open, done)  lww
  }
}
`,
			edited: `namespace acme

type standup {
  op create 1 {
    state  string  lww
  }
}
`,
			wantMsg: `attribute "enum" was removed`,
		},
		{
			name: "bounded string narrowed to unbounded",
			base: `namespace acme

type standup {
  op create 1 {
    title  string(200)  lww
  }
}
`,
			edited: `namespace acme

type standup {
  op create 1 {
    title  string  lww
  }
}
`,
			wantMsg: `attribute "max_length" was removed`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := initTestRepo(t)
			writeSchemaFile(t, env.repoDir, tt.base)

			var stdout, stderr bytes.Buffer
			if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
				t.Fatalf("initial apply failed with %d; stderr: %s", code, stderr.String())
			}
			tipBefore := writSchemaRef(t, env.repoDir)

			writeSchemaFile(t, env.repoDir, tt.edited)

			stdout.Reset()
			stderr.Reset()
			code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan"}, &stdout, &stderr)
			if code != 1 {
				t.Fatalf("expected plan to refuse with exit 1, got %d; stdout: %s stderr: %s", code, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantMsg) {
				t.Errorf("expected refusal message to mention %q, got: %s", tt.wantMsg, stderr.String())
			}
			if tip := writSchemaRef(t, env.repoDir); tip != tipBefore {
				t.Fatalf("a refused plan must never append anything, but the chain tip moved %s -> %s", tipBefore, tip)
			}

			// apply runs the same computation; it must refuse identically,
			// and the writer's chain tip must not move — the medium finding
			// was exactly an apply that printed success while silently
			// re-appending the same op, so this checks the tip, never the
			// exit code or stdout text alone.
			stdout.Reset()
			stderr.Reset()
			code = run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr)
			if code != 1 {
				t.Fatalf("expected apply to also refuse with exit 1, got %d; stdout: %s stderr: %s", code, stdout.String(), stderr.String())
			}
			if tip := writSchemaRef(t, env.repoDir); tip != tipBefore {
				t.Fatalf("a refused apply appended ops: chain tip moved %s -> %s", tipBefore, tip)
			}
		})
	}
}

// TestSchemaCLI_ApplyConvergesAfterEdit is the round-3 regression net for
// the medium finding's general shape, not only its specific (now-refused)
// reproduction: after any successful apply — including one that appends a
// real delta on top of history the object already has — plan must report
// up_to_date and a second apply must append nothing. The medium finding
// was precisely a apply that printed "Appended 1 op(s)" at exit 0 on every
// run while the log never actually converged, so this asserts the
// writer's chain tip across two applies rather than trusting the printed
// op count or exit code.
func TestSchemaCLI_ApplyConvergesAfterEdit(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, `namespace acme

type standup {
  op create 1 {
    title  string(200)  lww
  }
}
`)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("initial apply failed with %d; stderr: %s", code, stderr.String())
	}

	// A legitimate edit that adds a field: a real delta, not a narrowing,
	// so this must succeed and append ops.
	edited := `namespace acme

type standup {
  op create 1 {
    title  string(200)  lww
    body   text         multi-value
  }
}
`
	writeSchemaFile(t, env.repoDir, edited)

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("second apply (adding a field) failed with %d; stderr: %s", code, stderr.String())
	}
	tipAfterEdit := writSchemaRef(t, env.repoDir)

	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("plan after the edit landed failed with %d; stderr: %s", code, stderr.String())
	}
	var envW wire.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envW); err != nil {
		t.Fatalf("unmarshal plan envelope: %v", err)
	}
	planData, _ := json.Marshal(envW.Data)
	var plan wire.SchemaPlan
	if err := json.Unmarshal(planData, &plan); err != nil {
		t.Fatalf("unmarshal SchemaPlan: %v", err)
	}
	if !plan.UpToDate || len(plan.Ops) != 0 {
		t.Fatalf("expected up_to_date with zero ops once the edit has landed; got up_to_date=%v ops=%+v", plan.UpToDate, plan.Ops)
	}

	// Re-apply the same (unedited) file: must be a no-op, verified against
	// the chain tip, not the printed op count.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("re-apply of the unedited file failed with %d; stderr: %s", code, stderr.String())
	}
	tipAfterReapply := writSchemaRef(t, env.repoDir)
	if tipAfterReapply != tipAfterEdit {
		t.Fatalf("apply did not converge: chain tip moved on a no-op re-apply %s -> %s", tipAfterEdit, tipAfterReapply)
	}
}

// TestSchemaCLI_NarrowingRefusalHoldsAcrossRepeatedApplies is the medium
// finding's own reproduction, verified exactly the way round 3 verified it:
// running `writ schema apply` three times in a row on a file that narrows
// title's max_length and checking the writer's chain tip after each,
// instead of trusting any single run's exit code or printed text. Before
// this fix, each of the three runs printed "Appended 1 op(s)" at exit 0
// and moved the chain tip every time — a define-field op that never
// actually narrows the log's folded max_length, so `plan` never reached
// up_to_date and the loop never converged. After this fix, the narrowing
// is refused at exit 1 on every run and the tip never moves at all —
// converged, just not by landing anything.
func TestSchemaCLI_NarrowingRefusalHoldsAcrossRepeatedApplies(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, `namespace acme

type standup {
  op create 1 {
    title  string(200)  lww
  }
}
`)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("initial apply failed with %d; stderr: %s", code, stderr.String())
	}
	tipBefore := writSchemaRef(t, env.repoDir)

	writeSchemaFile(t, env.repoDir, `namespace acme

type standup {
  op create 1 {
    title  string  lww
  }
}
`)

	for i := 1; i <= 3; i++ {
		stdout.Reset()
		stderr.Reset()
		code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr)
		if code != 1 {
			t.Fatalf("apply #%d: expected exit 1 (a narrowing max_length can never converge, so it must be refused, not applied), got %d; stdout: %s stderr: %s", i, code, stdout.String(), stderr.String())
		}
		if tip := writSchemaRef(t, env.repoDir); tip != tipBefore {
			t.Fatalf("apply #%d moved the chain tip on a refusal: %s -> %s", i, tipBefore, tip)
		}
	}
}

// TestSchemaCLI_NamespaceChangeIsRefused exercises the namespace-change
// refusal itself (resolveSchemaTarget's case-0, single-contested-owner
// branch), distinctly from the cross-object collision case
// TestSchemaCLI_ObjectIdentity covers: the changed file re-declares the
// same type ("standup") as before, so no *other* schema object in this
// repository binds it — declared types don't overlap with any third
// object, so the generic "object_type already bound" guard has nothing to
// catch here. The only thing that changed is the namespace, which is what
// this test means to prove is refused, and refused for that reason.
func TestSchemaCLI_NamespaceChangeIsRefused(t *testing.T) {
	env := initTestRepo(t)
	base := `namespace acme
description "Acme's vocabulary"

type standup {
  op create 1 {
    title  string(200)  lww
  }
}
`
	writeSchemaFile(t, env.repoDir, base)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("initial apply failed with %d; stderr: %s", code, stderr.String())
	}
	objectID := applyObjectID(t, stdout.Bytes())

	changed := strings.Replace(base, "namespace acme", "namespace acme2", 1)
	writeSchemaFile(t, env.repoDir, changed)

	tipBefore := writSchemaRef(t, env.repoDir)

	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for a namespace change, got %d; stdout: %s", code, stdout.String())
	}
	msg := stderr.String()
	if !strings.Contains(msg, objectID) {
		t.Errorf("expected the refusal to name the existing object id %s, got: %s", objectID, msg)
	}
	if !strings.Contains(msg, `"acme"`) || !strings.Contains(msg, `"acme2"`) {
		t.Errorf("expected the refusal to name both the old and new namespace, got: %s", msg)
	}
	if !strings.Contains(msg, "namespace is set once") || !strings.Contains(msg, "never changes") {
		t.Errorf("expected the refusal to read as a namespace change, not a generic object_type collision, got: %s", msg)
	}

	// A refused plan appends nothing, and apply runs the same computation,
	// so it must refuse identically and leave the chain tip untouched.
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected apply to also refuse with exit 1, got %d", code)
	}
	if tip := writSchemaRef(t, env.repoDir); tip != tipBefore {
		t.Fatalf("a refused apply appended ops: chain tip moved %s -> %s", tipBefore, tip)
	}
}

// TestSchemaCLI_ObjectIdentity exercises the three cases resolveSchemaTarget
// implements: a fresh mint reporting `created`, reuse of an existing
// namespace match without minting, and a refusal naming both object ids
// when a second schema object would bind an already-bound object_type.
func TestSchemaCLI_ObjectIdentity(t *testing.T) {
	env := initTestRepo(t)

	first := `namespace acme
description "Acme's vocabulary"

type standup {
  op create 1 {
    title  string(200)  lww
  }
}
`
	writeSchemaFile(t, env.repoDir, first)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("apply failed with %d; stderr: %s", code, stderr.String())
	}
	firstObjectID := applyObjectID(t, stdout.Bytes())
	if want := deriveSchemaObjectID("acme"); firstObjectID != want {
		t.Fatalf("expected the derived object id %s, got %s", want, firstObjectID)
	}

	// Reuse: same namespace, a changed description (an ordinary update, not
	// a removal) must resolve to the same object id and never mint again.
	updated := `namespace acme
description "Updated"

type standup {
  op create 1 {
    title  string(200)  lww
  }
}
`
	writeSchemaFile(t, env.repoDir, updated)

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"-C", env.repoDir, "schema", "plan", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("reuse plan failed with %d; stderr: %s", code, stderr.String())
	}
	if reuseID := planObjectID(t, stdout.Bytes()); reuseID != firstObjectID {
		t.Fatalf("expected the same object id to be reused, got %s want %s", reuseID, firstObjectID)
	}

	// A second, independent namespace declaring the same type collides.
	second := `namespace other
description "A different vocabulary"

type standup {
  op create 1 {
    title  string(200)  lww
  }
}
`
	writeSchemaFile(t, env.repoDir, second)

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"-C", env.repoDir, "schema", "plan"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for a colliding object_type, got %d; stdout: %s", code, stdout.String())
	}
	if !strings.Contains(stderr.String(), firstObjectID) || !strings.Contains(stderr.String(), "standup") {
		t.Errorf("expected the refusal to name the existing object id %s and the contested type, got: %s", firstObjectID, stderr.String())
	}
}

// TestSchemaCLI_ReuseRefusesContestedType is finding 1's regression net:
// the contested-object_type guard must run on the reuse branch
// (resolveSchemaTarget's "exactly one namespace match" case), not only on
// the "no match" creation branch. A file whose namespace matches an
// existing schema object, but which adds a type a *different* schema
// object already binds, must be refused exactly like the creation
// branch's own collision — otherwise apply would double-bind the type and
// RulesFromSchemas would withhold every rule for it, permanently.
func TestSchemaCLI_ReuseRefusesContestedType(t *testing.T) {
	env := initTestRepo(t)

	// Object A: namespace acme, type standup.
	writeSchemaFile(t, env.repoDir, `namespace acme
description "Acme's vocabulary"

type standup {
  op create 1 {
    title  string(200)  lww
  }
}
`)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("first apply failed with %d; stderr: %s", code, stderr.String())
	}
	objectA := applyObjectID(t, stdout.Bytes())

	// Object B: a distinct namespace, a distinct type — a legitimate
	// second schema object, no collision.
	writeSchemaFile(t, env.repoDir, `namespace other
description "A different vocabulary"

type sprint {
  op create 1 {
    title  string(200)  lww
  }
}
`)
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("second apply failed with %d; stderr: %s", code, stderr.String())
	}
	objectB := applyObjectID(t, stdout.Bytes())
	if objectB == objectA {
		t.Fatalf("expected a distinct object id for the second namespace, got the same id %s twice", objectA)
	}

	// Back to namespace acme (an exact match: object A, the reuse branch)
	// — but now also declaring "sprint", which object B already binds.
	tipBefore := writSchemaRef(t, env.repoDir)
	writeSchemaFile(t, env.repoDir, `namespace acme
description "Acme's vocabulary"

type standup {
  op create 1 {
    title  string(200)  lww
  }
}

type sprint {
  op create 1 {
    title  string(200)  lww
  }
}
`)

	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for a reuse that contests object B's type, got %d; stdout: %s", code, stdout.String())
	}
	msg := stderr.String()
	if !strings.Contains(msg, objectA) || !strings.Contains(msg, objectB) {
		t.Errorf("expected the refusal to name both object ids (%s reusing, %s already bound), got: %s", objectA, objectB, msg)
	}
	if !strings.Contains(msg, "sprint") {
		t.Errorf("expected the refusal to name the contested type, got: %s", msg)
	}

	// A refused plan appends nothing, and apply runs the same computation.
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected apply to also refuse with exit 1, got %d", code)
	}
	if tip := writSchemaRef(t, env.repoDir); tip != tipBefore {
		t.Fatalf("a refused apply appended ops: chain tip moved %s -> %s", tipBefore, tip)
	}
}

// TestSchemaCLI_ReuseRefusesFutureBootstrapCollision is round 2's finding:
// `plan` (and `apply`, which runs the whole of `plan`) validated only the
// *current* log, never the state applying would produce. A writ.schema
// declaring `type schema` — the engine's one hard-coded bootstrap type —
// sails through resolveSchemaTarget: no schema *object* binds "schema"
// today (the engine does, without ever appearing in the schemas list), so
// the cross-object collision guard has nothing to compare it against.
// RulesFromSchemas, though, refuses to let any object claim it and
// withholds every rule for it, forever — nothing removes a type once
// written and `deprecate-type` does not unbind it. Reusing an existing
// schema object exercises resolveSchemaTarget's "exactly one match"
// branch; TestSchemaCLI_CreateRefusesFutureBootstrapCollision below
// exercises the same conflict through the "no match, mint fresh" branch,
// so together they prove the general "conflicts the apply would
// introduce" mechanism rather than one hard-coded `type schema` check.
func TestSchemaCLI_ReuseRefusesFutureBootstrapCollision(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, `namespace acme
description "Acme's vocabulary"

type widget {
  op create 1 {
    title  string(200)  lww
  }
}
`)
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("initial apply failed with %d; stderr: %s", code, stderr.String())
	}

	tipBefore := writSchemaRef(t, env.repoDir)

	writeSchemaFile(t, env.repoDir, `namespace acme
description "Acme's vocabulary"

type widget {
  op create 1 {
    title  string(200)  lww
  }
}

type schema {
  op create 1 {
    title  string(200)  lww
  }
}
`)

	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for a writ.schema declaring `type schema`, got %d; stdout: %s", code, stdout.String())
	}
	msg := stderr.String()
	if !strings.Contains(msg, `"schema"`) || !strings.Contains(msg, "bootstrap") {
		t.Errorf("expected the refusal to name the schema bootstrap conflict, got: %s", msg)
	}

	// A refused plan appends nothing, and apply runs the same computation,
	// so it must refuse identically and leave the chain tip untouched.
	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected apply to also refuse with exit 1, got %d", code)
	}
	if tip := writSchemaRef(t, env.repoDir); tip != tipBefore {
		t.Fatalf("a refused apply appended ops: chain tip moved %s -> %s", tipBefore, tip)
	}
}

// TestSchemaCLI_CreateRefusesFutureBootstrapCollision is
// TestSchemaCLI_ReuseRefusesFutureBootstrapCollision's counterpart through
// resolveSchemaTarget's other route to an append: a brand-new object
// (case 0, no namespace match, mint fresh) whose file declares `type
// schema` from the very first apply. No schema object exists yet at all,
// so there is nothing for the cross-object guard to compare against
// either way; only re-running RulesFromSchemas over the state this apply
// would produce catches it.
func TestSchemaCLI_CreateRefusesFutureBootstrapCollision(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, `namespace acme
description "Acme's vocabulary"

type schema {
  op create 1 {
    title  string(200)  lww
  }
}
`)

	tipBefore := writSchemaRef(t, env.repoDir)
	if tipBefore != "" {
		t.Fatalf("expected no schema chain before the first apply, got tip %s", tipBefore)
	}

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected exit 1 for a fresh writ.schema declaring `type schema`, got %d; stdout: %s", code, stdout.String())
	}
	msg := stderr.String()
	if !strings.Contains(msg, `"schema"`) || !strings.Contains(msg, "bootstrap") {
		t.Errorf("expected the refusal to name the schema bootstrap conflict, got: %s", msg)
	}

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected apply to also refuse with exit 1, got %d", code)
	}
	if tip := writSchemaRef(t, env.repoDir); tip != tipBefore {
		t.Fatalf("a refused apply appended ops: chain tip moved %s -> %s", tipBefore, tip)
	}
}

// runSchemaSyncOrFatal runs `writ sync` against dir (push any local ops,
// fetch any new remote ones) and fails the test on a non-zero exit.
func runSchemaSyncOrFatal(t *testing.T, dir string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", dir, "sync"}, &stdout, &stderr); code != 0 {
		t.Fatalf("sync in %s failed with %d; stdout: %s stderr: %s", dir, code, stdout.String(), stderr.String())
	}
}

// TestSchemaCLI_IndependentOfflineBootstrapConverges is WRIT-199's
// acceptance test, run end to end against a real system-git transport
// (setupSyncTestHarness): two writers who each bootstrap the same
// namespace offline — neither has fetched anything, so neither can know
// whether a schema object already exists — must converge on one schema
// object, not two.
//
// Before this change, each bootstrap minted an independent 128-bit random
// object id; both pushes would land two schema objects binding the same
// object_type, and RulesFromSchemas withholds every rule for a type bound
// twice, permanently (WRIT-186) — this test fails against that behavior.
// Deriving the id from the namespace (spec/identifiers.md's schema
// carve-out) makes the collision unreachable: both writers compute
// schema:<namespace> and append to the same object, and their disjoint
// declarations merge through the fold the same way any other concurrent
// edit would.
func TestSchemaCLI_IndependentOfflineBootstrapConverges(t *testing.T) {
	_, aliceDir, bobDir := setupSyncTestHarness(t)
	ctx := context.Background()

	const schemaSrc = `namespace offline-demo
description "Offline bootstrap demo"

type widget {
  op create 1 {
    title  string(200)  lww
  }
}
`

	// Alice bootstraps the namespace entirely offline: no sync has run, so
	// she has no way to know whether anyone else has already created this
	// schema object.
	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("open Alice: %v", err)
	}
	writeSchemaFile(t, aliceDir, schemaSrc)
	planA, err := buildSchemaPlan(ctx, sA, aliceDir)
	if err != nil {
		t.Fatalf("Alice build schema plan: %v", err)
	}
	if !planA.created {
		t.Fatalf("expected Alice's plan to create a fresh schema object")
	}
	if err := sA.ApplySchema(ctx, planA.ops); err != nil {
		t.Fatalf("Alice apply schema: %v", err)
	}
	sA.Close()

	// Bob independently bootstraps the same namespace, also offline, with
	// no chance to fetch Alice's ops first — the exact hazard WRIT-199
	// closes.
	sB, err := writ.Open(bobDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("open Bob: %v", err)
	}
	writeSchemaFile(t, bobDir, schemaSrc)
	planB, err := buildSchemaPlan(ctx, sB, bobDir)
	if err != nil {
		t.Fatalf("Bob build schema plan: %v", err)
	}
	if !planB.created {
		t.Fatalf("expected Bob's plan to create a fresh schema object")
	}
	if planB.objectID != planA.objectID {
		t.Fatalf("expected Alice and Bob to derive the same object id, got %s and %s", planA.objectID, planB.objectID)
	}
	if err := sB.ApplySchema(ctx, planB.ops); err != nil {
		t.Fatalf("Bob apply schema: %v", err)
	}
	sB.Close()

	// Both push, then both fetch, over real system git.
	runSchemaSyncOrFatal(t, aliceDir)
	runSchemaSyncOrFatal(t, bobDir)
	runSchemaSyncOrFatal(t, aliceDir)

	sA, err = writ.Open(aliceDir)
	if err != nil {
		t.Fatalf("reopen Alice: %v", err)
	}
	defer sA.Close()

	schemas, err := sA.Schema(ctx)
	if err != nil {
		t.Fatalf("Alice Schema: %v", err)
	}
	if len(schemas) != 1 {
		t.Fatalf("expected exactly one schema object once both writers push and fetch, got %d: %+v", len(schemas), schemas)
	}
	if want := deriveSchemaObjectID("offline-demo"); schemas[0].ObjectID != want {
		t.Fatalf("expected the schema object id to be %s, got %s", want, schemas[0].ObjectID)
	}

	rules, conflicts := writ.RulesFromSchemas(schemas)
	if len(conflicts) != 0 {
		t.Fatalf("expected no schema conflicts once both writers converge on one object, got %+v", conflicts)
	}
	if len(rules["widget"]) == 0 {
		t.Fatalf("expected rules for the declared type %q, got none; rules: %+v", "widget", rules)
	}
}

// TestSchemaCLI_IndependentOfflineBootstrapFieldDisagreement is the second
// half of WRIT-199's acceptance criteria: when two writers bootstrapping
// the same namespace offline don't just declare disjoint types but
// concurrently disagree about one field's declaration, the disagreement
// resolves through the object's ordinary keyed-lww register — the field
// is never withheld — and a later write corrects it, exactly the
// correction flow that already works for any other keyed-lww disagreement.
func TestSchemaCLI_IndependentOfflineBootstrapFieldDisagreement(t *testing.T) {
	_, aliceDir, bobDir := setupSyncTestHarness(t)
	ctx := context.Background()

	schemaSrc := func(maxLength int) string {
		return fmt.Sprintf(`namespace conflict-demo
description "Conflict demo"

type widget {
  op create 1 {
    priority  string(%d)  lww
  }
}
`, maxLength)
	}

	applyOffline := func(dir string, maxLength int) {
		t.Helper()
		s, err := writ.Open(dir, writ.WithSigner(dummySigner()))
		if err != nil {
			t.Fatalf("open %s: %v", dir, err)
		}
		defer s.Close()
		writeSchemaFile(t, dir, schemaSrc(maxLength))
		planRes, err := buildSchemaPlan(ctx, s, dir)
		if err != nil {
			t.Fatalf("build schema plan in %s: %v", dir, err)
		}
		if err := s.ApplySchema(ctx, planRes.ops); err != nil {
			t.Fatalf("apply schema in %s: %v", dir, err)
		}
	}

	// Alice and Bob each bootstrap the same namespace offline, declaring
	// the same field with a different max_length — a genuine concurrent
	// disagreement, not a removal.
	applyOffline(aliceDir, 50)
	applyOffline(bobDir, 300)

	runSchemaSyncOrFatal(t, aliceDir)
	runSchemaSyncOrFatal(t, bobDir)
	runSchemaSyncOrFatal(t, aliceDir)

	readFolded := func(dir string) (maxLength int64, conflicts []writ.SchemaConflict) {
		t.Helper()
		s, err := writ.Open(dir)
		if err != nil {
			t.Fatalf("open %s: %v", dir, err)
		}
		defer s.Close()
		schemas, err := s.Schema(ctx)
		if err != nil {
			t.Fatalf("Schema in %s: %v", dir, err)
		}
		if len(schemas) != 1 {
			t.Fatalf("expected exactly one schema object, got %d: %+v", len(schemas), schemas)
		}
		_, conflicts = writ.RulesFromSchemas(schemas)
		for _, ty := range schemas[0].Types {
			if ty.Name != "widget" {
				continue
			}
			for _, f := range ty.Fields {
				if f.Name == "priority" {
					return f.MaxLength, conflicts
				}
			}
		}
		t.Fatalf("field widget.priority not found in folded schema: %+v", schemas[0])
		return 0, nil
	}

	maxLength, conflicts := readFolded(aliceDir)
	if len(conflicts) != 0 {
		t.Fatalf("expected the field disagreement to resolve via keyed-lww with no withheld rules, got conflicts: %+v", conflicts)
	}
	if maxLength != 50 && maxLength != 300 {
		t.Fatalf("expected the folded max_length to be one writer's value (50 or 300), got %d", maxLength)
	}

	// Correction: a later write, applied and synced after both concurrent
	// declarations have landed, resolves the disagreement to whatever it
	// says — the same correction flow that already works for any other
	// keyed-lww field.
	time.Sleep(1100 * time.Millisecond)
	applyOffline(aliceDir, 999)
	runSchemaSyncOrFatal(t, aliceDir)
	runSchemaSyncOrFatal(t, bobDir)

	maxLength, conflicts = readFolded(bobDir)
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts after the corrective write, got: %+v", conflicts)
	}
	if maxLength != 999 {
		t.Fatalf("expected the corrective write (max_length 999) to win, got %d", maxLength)
	}
}

func planObjectID(t *testing.T, jsonData []byte) string {
	t.Helper()
	var envW wire.Envelope
	if err := json.Unmarshal(jsonData, &envW); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	data, _ := json.Marshal(envW.Data)
	var plan wire.SchemaPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("unmarshal SchemaPlan: %v", err)
	}
	return plan.ObjectID
}

func applyObjectID(t *testing.T, jsonData []byte) string {
	t.Helper()
	var envW wire.Envelope
	if err := json.Unmarshal(jsonData, &envW); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	data, _ := json.Marshal(envW.Data)
	var apply wire.SchemaApply
	if err := json.Unmarshal(data, &apply); err != nil {
		t.Fatalf("unmarshal SchemaApply: %v", err)
	}
	return apply.ObjectID
}

func TestGolden_SchemaPlan_Fresh(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("plan --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "schema_plan.json", stdout.Bytes())
}

func TestGolden_SchemaPlan_UpToDate(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("apply failed with %d; stderr: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("plan --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "schema_plan_up_to_date.json", stdout.Bytes())
}

func TestGolden_SchemaApply(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("apply --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "schema_apply.json", stdout.Bytes())
}
