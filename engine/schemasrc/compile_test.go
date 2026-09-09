package schemasrc_test

import (
	"errors"
	"fmt"
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
	if _, ok := rules["acme.widget"]; !ok {
		t.Fatalf("expected rules installed for object_type %q, got %v", "acme.widget", rules)
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

// TestCompileCrossOpTypeTargetReuseWithDifferentValueTypeIsRejected pins
// WRIT-198's widened collision check: two fields sharing a target (here the
// default, the field name) across different op_types must agree on
// value_type, not just strategy. Before the widening, "owner" on create
// (person-ref) and "owner" on assign (object-ref), both lww, compiled
// silently — exactly the shape of WRIT-198's five colliding review/issue
// targets, which all agreed on strategy and disagreed only on value_type.
func TestCompileCrossOpTypeTargetReuseWithDifferentValueTypeIsRejected(t *testing.T) {
	src := `namespace acme

type widget {
  op create 1 {
    owner  person-ref  lww
  }

  op assign 1 {
    owner  object-ref  lww
  }
}
`
	f := mustParse(t, src)
	_, err := schemasrc.Compile(f, "sch-acme")
	if err == nil {
		t.Fatal("expected an error for two op_types sharing a target but disagreeing on value_type")
	}
	if !strings.Contains(err.Error(), `reuses target "owner"`) {
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

// TestCompileThreeRuleTargetSharingIsRejected pins WRIT-211's fix at the
// compiler level with the ticket's own three-rule vector: "configure"'s two
// versions are a version-bump class of one another (free to disagree on
// value_type alone), but "reset" declares an explicit target(mode) that
// reuses their shared target while belonging to neither's class. Once a
// target is bound by more than one class the version-bump carve-out is
// void for every rule on it (spec.CheckTargetAgreement), so Compile must
// reject the whole type — not accept it because "configure"'s two versions
// look pairwise fine in isolation, and not merely reject "reset" alone.
func TestCompileThreeRuleTargetSharingIsRejected(t *testing.T) {
	src := `namespace acme

type widget {
  op configure 1 {
    mode  string  lww
  }

  op configure 2 {
    mode  int  lww
  }

  op reset 1 {
    value  string  lww  target(mode)
  }
}
`
	f := mustParse(t, src)
	_, err := schemasrc.Compile(f, "sch-acme")
	if err == nil {
		t.Fatal("expected an error: a version-bump class (configure) and an unrelated class (reset) share target \"mode\"")
	}
	if !strings.Contains(err.Error(), `reuses target "mode"`) {
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

// TestCompileVersionBumpValueTypeOnlyIsAccepted is the positive control for
// TestCompileCrossOpTypeTargetReuseWithDifferentValueTypeIsRejected: an
// op_version bump of the same (op_type, field) may freely change value_type
// under the shared default target (spec/schema-ops.md §8, spec/fold.md §5) —
// the carve-out the cross-op_type case above does not extend to.
func TestCompileVersionBumpValueTypeOnlyIsAccepted(t *testing.T) {
	src := `namespace acme

type ticket {
  op create 1 {
    priority  string  lww
  }

  op create 2 {
    priority  int  lww
  }
}
`
	f := mustParse(t, src)
	if _, err := schemasrc.Compile(f, "sch-acme"); err != nil {
		t.Fatalf("Compile: %v", err)
	}
}

// TestCompileTargetCollisionIsOrderIndependent pins spec.CheckTargetAgreement
// as a set-level rule (spec/fold.md §5), not one that only compares a
// candidate against the most recently bound rule for its target. Both
// schemas below declare the same three fields sharing the default target
// "f" — a version bump from string to int on one op_type (permitted, no
// strategy change), plus an unrelated op_type also declaring "f" as int,
// which disagrees with the *first* version (string) even though it agrees
// with the *second* (int). Renaming the first op_type from "alpha" to
// "zeta" changes nothing about what the schema declares — only where
// fieldKeyLess (which sorts fields by op_type name) places the version
// bump relative to the unrelated op_type — and must not change whether the
// schema is accepted. Checking a candidate only against the last-bound
// rule for a target lets the version-bump carve-out rebind "f" to int
// before "beta" is checked, so "beta" agreeing with the rebound value
// alone let this compile; comparing against every bound rule for the
// target catches the disagreement with "alpha"/"zeta" 1 regardless of
// which op_type sorts where.
func TestCompileTargetCollisionIsOrderIndependent(t *testing.T) {
	const template = `namespace acme

type widget {
  op %s 1 {
    f  string  lww
  }

  op %s 2 {
    f  int  lww
  }

  op beta 1 {
    f  int  lww
  }
}
`
	for _, opType := range []string{"alpha", "zeta"} {
		t.Run(opType, func(t *testing.T) {
			src := fmt.Sprintf(template, opType, opType)
			f := mustParse(t, src)
			_, err := schemasrc.Compile(f, "sch-acme")
			if err == nil {
				t.Fatalf("expected an error: %q's version 1 (string) disagrees with beta's (int) even though %s's version 2 (also int) does not", opType, opType)
			}
			if !strings.Contains(err.Error(), `reuses target "f"`) {
				t.Errorf("unexpected error: %v", err)
			}
			var se *schemasrc.SyntaxError
			if !errors.As(err, &se) {
				t.Fatalf("expected a *schemasrc.SyntaxError, got %T: %v", err, err)
			}
		})
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
