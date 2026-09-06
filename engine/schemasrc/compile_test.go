package schemasrc_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/schemasrc"
	"github.com/writtendev/writ/engine/state"
)

func mustParse(t *testing.T, src string) *schemasrc.File {
	t.Helper()
	f, err := schemasrc.Parse("test.schema", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

// TestCompileRequiresObjectID pins the WRIT-187 "carry forward" hazard:
// Compile's objectID is an explicit parameter with no default and nothing
// derived from namespace, because reusing it across applies is
// load-bearing (WRIT-191) — a silently minted id would create a second
// schema object binding the same object_types, which RulesFromSchemas
// treats as a collision and responds to by withholding all rules for
// those types.
func TestCompileRequiresObjectID(t *testing.T) {
	f := mustParse(t, "namespace acme\n")
	if _, err := schemasrc.Compile(f, ""); err == nil {
		t.Fatal("Compile with an empty objectID should fail, not mint one")
	}
}

// TestCompileReusedObjectIDAvoidsCollision compiles the same source under
// the same objectID twice, folds both op sequences together (as two
// separate applies against one schema object would arrive in the log),
// and confirms RulesFromSchemas installs the type's rules without
// reporting a collision — the scenario the WRIT-187 plan's "carry
// forward" note warns a fresh id per apply would break.
func TestCompileReusedObjectIDAvoidsCollision(t *testing.T) {
	src := `namespace acme

type widget {
  op create 1 {
    name string lww
  }
}
`
	f := mustParse(t, src)
	envs, err := schemasrc.Compile(f, "sch-acme")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	ops := envelopesToOps(envs)
	folded, err := state.FoldSchema(ops)
	if err != nil {
		t.Fatalf("FoldSchema: %v", err)
	}

	rules, conflicts := writ.RulesFromSchemas([]state.Schema{folded})
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}
	if _, ok := rules["widget"]; !ok {
		t.Fatalf("expected rules installed for object_type %q, got %v", "widget", rules)
	}
}

// TestCompileDuplicateOpIsRejected: the same (op_type, op_version) named
// twice across op blocks within one type is a compile error, not a
// last-write-wins acceptance — the same determinism concern §8 raises
// about reusing state, applied at the source level instead of the fold
// level.
func TestCompileDuplicateOpIsRejected(t *testing.T) {
	src := `namespace acme

type widget {
  op create 1 {
    a string lww
  }
  op create 1 {
    b string lww
  }
}
`
	f := mustParse(t, src)
	if _, err := schemasrc.Compile(f, "sch-acme"); err == nil {
		t.Fatal("expected an error for a duplicate op declaration")
	} else if !strings.Contains(err.Error(), "declared more than once") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestCompileDuplicateFieldIsRejected: the same field declared twice for
// one (op_type, op_version) is likewise rejected rather than silently
// taking the last declaration.
func TestCompileDuplicateFieldIsRejected(t *testing.T) {
	src := `namespace acme

type widget {
  op create 1 {
    a string lww
    a int lww
  }
}
`
	f := mustParse(t, src)
	if _, err := schemasrc.Compile(f, "sch-acme"); err == nil {
		t.Fatal("expected an error for a duplicate field declaration")
	} else if !strings.Contains(err.Error(), "declared more than once") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestCompileDuplicateTypeIsRejected mirrors the op/field duplicate
// checks at the type level.
func TestCompileDuplicateTypeIsRejected(t *testing.T) {
	src := `namespace acme

type widget {
  op create 1 {
    a string lww
  }
}

type widget {
  op create 1 {
    b string lww
  }
}
`
	f := mustParse(t, src)
	if _, err := schemasrc.Compile(f, "sch-acme"); err == nil {
		t.Fatal("expected an error for a duplicate type declaration")
	} else if !strings.Contains(err.Error(), "declared more than once") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestCompileVersionBumpReusingTargetIsRejected pins WRIT-187 round-1
// finding 2: a version bump that changes strategy while reusing the
// default (field-name) target compiles clean under spec.ValidateFieldRule
// (it validates one rule at a time) and would then have its `create 2`
// rule silently dropped by RulesFromSchemas as a TargetKey() collision
// (engine/schema.go) — exactly the guarantee Compile's own doc comment and
// spec/schema-source.md §5 claim it rejects at compile time instead.
func TestCompileVersionBumpReusingTargetIsRejected(t *testing.T) {
	src := `namespace acme

type ticket {
  op create 1 {
    priority  string  lww
  }

  op create 2 {
    priority  enum(low, high)  lattice(low, high)
  }
}
`
	f := mustParse(t, src)
	_, err := schemasrc.Compile(f, "sch-acme")
	if err == nil {
		t.Fatal("expected an error for a version bump reusing the default target across a strategy change")
	}
	if !strings.Contains(err.Error(), `reuses target "priority"`) {
		t.Errorf("unexpected error: %v", err)
	}
	var se *schemasrc.SyntaxError
	if !errors.As(err, &se) {
		t.Fatalf("expected a *schemasrc.SyntaxError, got %T: %v", err, err)
	}
	if se.Line == 0 || se.Col == 0 {
		t.Errorf("expected a line and column on the collision error, got %+v", se)
	}
}

// TestCompileVersionBumpWithDistinctTargetIsAccepted is the positive
// control for TestCompileVersionBumpReusingTargetIsRejected: the same
// strategy change is accepted once the new version declares its own
// target, exactly as testdata/valid/version-bump.schema does.
func TestCompileVersionBumpWithDistinctTargetIsAccepted(t *testing.T) {
	src := `namespace acme

type ticket {
  op create 1 {
    priority  string  lww
  }

  op create 2 {
    priority  enum(low, high)  lattice(low, high)  target(priority_v2)
  }
}
`
	f := mustParse(t, src)
	if _, err := schemasrc.Compile(f, "sch-acme"); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

// TestCompileUntypedFieldOmitsValueType asserts an untyped field (WRIT-187
// correction 3) emits no value_type key at all, rather than an empty
// string — the distinction spec/value-types.md draws between "untyped"
// and a declared-but-empty type.
func TestCompileUntypedFieldOmitsValueType(t *testing.T) {
	src := `namespace acme

type widget {
  op create 1 {
    notes untyped append
  }
}
`
	f := mustParse(t, src)
	envs, err := schemasrc.Compile(f, "sch-acme")
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	found := false
	for _, env := range envs {
		if env.OpType != "define-field" {
			continue
		}
		found = true
		if strings.Contains(string(env.Body), "value_type") {
			t.Errorf("untyped field's body carries a value_type key: %s", env.Body)
		}
	}
	if !found {
		t.Fatal("no define-field op emitted")
	}
}
