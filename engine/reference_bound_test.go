package writ_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	writ "github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
)

// referenceBoundSchemaSrc declares a "ticket" type with one
// object-ref-valued field, "targets" — the same value type
// spec/identifiers.md's reference bound governs for any schema-declared
// field that uses it. Nothing here is special-cased for a particular op
// body; there is no such thing as a per-type op body any more.
// ("target" is reserved in the modifier position in writ.schema, so the
// field is named "targets".)
const referenceBoundSchemaSrc = `namespace acme
description "Reference bound test vocabulary"

type ticket {
  op create 1 {
    title  string  lww
  }

  op link 1 {
    targets      [object-ref]  set-union
    target_type  string        lww
    relation     string        lww
  }
}
`

// TestLinkTargetLengthBoundIsEnforcedOnTheProducerPath answers the question
// WRIT-136 asked about the reference bound: does writ's own producer stop
// itself writing an op the schema would reject?
//
// It does, and not because anything here guards it. codec.BuildCommit — the
// sole commit constructor — validates every op body against its vocabulary
// schema (WRIT-129, PR #100), and engine/internal/value's object-ref case
// enforces spec/identifiers.md's reference bound (maxLength 289) generically
// for any schema-declared field of that value type — not a rule restated in
// Go for one per-type op body. So the bound reaches the write path through
// the schema the fixtures already pin, with no second copy of the number in
// Go. That is the outcome to prefer: a reference-length constant in engine/
// would be a rule stated twice, free to drift from the one the corpus
// enforces.
//
// requireCommitOID next door is the case that does earn a domain-side guard,
// and the difference is instructive. "main" is a value a caller types on
// purpose, and the fix — resolve the ref first — is only obvious if the error
// names the field. A 290-code-point reference is not something anyone types;
// it is a program handing over a string it never bounded, and a schema
// violation is a truthful answer to it.
//
// The test is written against behaviour rather than against that reasoning:
// it goes through the public API, checks both sides of the bound, and checks
// that the refusal left nothing behind.
func TestLinkTargetLengthBoundIsEnforcedOnTheProducerPath(t *testing.T) {
	repoDir, _ := setupConfiguredRepo(t)
	s, err := writ.Open(repoDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	envs := compileTestSchema(t, "sch-refbound", referenceBoundSchemaSrc)
	if err := s.ApplySchema(ctx, envs); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	ticketID, err := s.Objects.Create(ctx, "ticket", writ.NewOp{
		Type:   "create",
		Fields: map[string]any{"title": "Reference bound"},
	})
	if err != nil {
		t.Fatalf("Create ticket failed: %v", err)
	}

	// 289 code points and 578 bytes. A producer counting octets would refuse
	// this one, so accepting it is what pins the unit on the write path the
	// way spec/testdata/references/valid/at-limit-multibyte.json pins it in
	// the corpus.
	atLimit := strings.Repeat("é", 289)
	if got := len([]rune(atLimit)); got != 289 || len(atLimit) != 578 {
		t.Fatalf("test setup: atLimit is %d code points / %d bytes, want 289 / 578", got, len(atLimit))
	}
	if err := s.Objects.Apply(ctx, ticketID, writ.NewOp{
		Type: "link",
		Fields: map[string]any{
			"targets":     atLimit,
			"target_type": "gadget",
			"relation":    "fixes",
		},
	}); err != nil {
		t.Fatalf("a 289-code-point target is inside the bound and must be accepted: %v", err)
	}

	// 290 code points, one over.
	overLimit := strings.Repeat("a", 290)
	err = s.Objects.Apply(ctx, ticketID, writ.NewOp{
		Type: "link",
		Fields: map[string]any{
			"targets":     overLimit,
			"target_type": "gadget",
			"relation":    "fixes",
		},
	})
	if err == nil {
		t.Fatal("a 290-code-point target is over the bound and must be refused")
	}
	var reject *codec.RejectError
	if !errors.As(err, &reject) {
		t.Errorf("refusal should be a codec reject, got %T: %v", err, err)
	} else if reject.Reason != codec.RejectSchemaViolation {
		t.Errorf("reject reason = %q, want %q", reject.Reason, codec.RejectSchemaViolation)
	}

	// The refusal must be total. An op log that recorded the over-long link
	// and then reported an error would have written the very thing the bound
	// exists to keep out of the repository.
	obj, err := s.Objects.Get(ctx, ticketID)
	if err != nil {
		t.Fatalf("Objects.Get failed: %v", err)
	}
	targets, ok := obj.Fields["targets"].([]string)
	if !ok || len(targets) != 1 {
		t.Fatalf("expected the one accepted link target, got %#v", obj.Fields["targets"])
	}
	if targets[0] != atLimit {
		t.Errorf("surviving link target is not the accepted one: %d code points",
			len([]rune(targets[0])))
	}
}
