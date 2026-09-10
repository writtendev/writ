package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
)

// TestEmitJSON_EscapesForbiddenCodePoints is the unit-level half of WRIT-137's
// half 2 acceptance: emitJSON, one of the two rendering chokepoints
// spec/identifiers.md §Rendering a person identifier names, must neutralise
// bidi and zero-width code points wherever they occur in the payload it
// encodes -- json.go's own doc comment explains why that means the whole
// encoded document, not only fields it can prove are person-ref.
func TestEmitJSON_EscapesForbiddenCodePoints(t *testing.T) {
	hostile := "email:alice" + string(rune(0x202E)) + "@evil.com"

	var buf bytes.Buffer
	if err := emitJSON(&buf, "test.kind", map[string]string{"subject": hostile}); err != nil {
		t.Fatalf("emitJSON: %v", err)
	}
	out := buf.String()

	for _, r := range out {
		if r == 0x202E {
			t.Fatalf("emitJSON output contains a raw U+202E byte sequence: %q", out)
		}
	}
	if !strings.Contains(out, "\\u202e") {
		t.Errorf("emitJSON output = %q, want it to contain the \\u202e escape", out)
	}

	// Lossless: a JSON decoder reads the escape back to the exact hostile
	// string emitJSON was given.
	var env wire.Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("decoding emitJSON's own output: %v", err)
	}
	data, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("envelope data = %T, want map[string]any", env.Data)
	}
	if data["subject"] != hostile {
		t.Errorf("decoded subject = %q, want the original hostile value %q (lossless round trip)", data["subject"], hostile)
	}
}

// TestFieldDisplay_EscapesForbiddenCodePoints covers fieldDisplay's other
// arm too -- the compact-JSON fallback for a non-string field -- since both
// are separate chokepoints per spec/identifiers.md §Rendering a person
// identifier.
func TestFieldDisplay_EscapesForbiddenCodePoints(t *testing.T) {
	hostile := "email:alice" + string(rune(0x202E)) + "@evil.com"

	if got := fieldDisplay(hostile); strings.ContainsRune(got, 0x202E) {
		t.Errorf("fieldDisplay(string) = %q, still contains the raw override", got)
	} else if !strings.Contains(got, "\\u202e") {
		t.Errorf("fieldDisplay(string) = %q, want the \\u202e escape", got)
	}

	if got := fieldDisplay([]any{hostile}); strings.ContainsRune(got, 0x202E) {
		t.Errorf("fieldDisplay(slice) = %q, still contains the raw override", got)
	} else if !strings.Contains(got, "\\u202e") {
		t.Errorf("fieldDisplay(slice) = %q, want the \\u202e escape", got)
	}
}

// TestAuthorDisplay_EscapesForbiddenCodePoints pins that authorDisplay
// escapes both name and email, for consistency with the same command's
// --json half (json.go's authorDisplay doc comment).
func TestAuthorDisplay_EscapesForbiddenCodePoints(t *testing.T) {
	hostileName := "Alice" + string(rune(0x202E))
	got := authorDisplay(hostileName, "alice@example.com")
	if strings.ContainsRune(got, 0x202E) {
		t.Errorf("authorDisplay(...) = %q, still contains the raw override", got)
	}
	if !strings.Contains(got, "\\u202e") {
		t.Errorf("authorDisplay(...) = %q, want the \\u202e escape", got)
	}
}

// writeForeignOp appends a raw op commit to refs/writ/<writerID>/<objectType>
// in dir's repository, bypassing every producer check (engine/codec.BuildCommit
// is never called) -- exactly what a foreign or non-conforming client would
// do. This is deliberate: engine/internal/person.Check now refuses a
// hostile person-ref value at the producer, so the only way to get one into
// the log at all, to prove writ's rendering paths neutralise it regardless,
// is to write the commit directly the way spec/fixtures' fold-corpus
// generator does (it also canonicalizes payloads directly and never runs
// BuildCommit).
func writeForeignOp(t *testing.T, dir, writerID, objectType, objectID, opType string, opVersion int64, body map[string]any) {
	t.Helper()

	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("opening repo at %s: %v", dir, err)
	}

	raw, err := json.Marshal(map[string]any{
		"object_id":   objectID,
		"object_type": objectType,
		"op_type":     opType,
		"op_version":  opVersion,
		"body":        body,
	})
	if err != nil {
		t.Fatalf("marshal op payload: %v", err)
	}
	canon, err := canonicaljson.Marshal(raw)
	if err != nil {
		t.Fatalf("canonicalize op payload: %v", err)
	}

	when := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	who := codec.Identity{Name: "Foreign Client", Email: "foreign@example.com", When: when}
	commit := &codec.Commit{
		Author:    who,
		Committer: who,
		Message:   fmt.Sprintf("writ: %s %s/%s\n", opType, objectType, objectID),
		Tree:      []codec.TreeEntry{{Name: "op.json", Mode: "100644", Data: canon}},
	}

	hash, err := codec.WriteCommit(context.Background(), repo.Storer, commit, nil)
	if err != nil {
		t.Fatalf("writing foreign commit: %v", err)
	}

	refName := plumbing.ReferenceName(fmt.Sprintf("refs/writ/%s/%s", writerID, objectType))
	if err := repo.Storer.SetReference(plumbing.NewHashReference(refName, hash)); err != nil {
		t.Fatalf("setting ref %s: %v", refName, err)
	}
}

// TestObjectShow_HostilePersonRefRendersEscaped is WRIT-137's end-to-end
// acceptance test: a foreign client's op carrying a bidi override in a
// person-ref field folds (spec/fold.md §7.1 makes fold total; a producer
// rule is hygiene, not the security boundary) and is displayed safely
// through both `object show` and `object show --json` -- no raw U+202E
// anywhere in either output.
func TestObjectShow_HostilePersonRefRendersEscaped(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}

	const objectID = "hostile-standup-1"
	hostile := "email:mallory" + string(rune(0x202E)) + "@evil.com"

	// A foreign writer, not the local one initTestRepo configured -- the
	// point is that this op never went through this repository's own
	// producer at all.
	writeForeignOp(t, env.repoDir, "fedcba9876543210", "acme.standup", objectID, "create", 1, map[string]any{
		"title":  "Hostile Repertoire Rendering Test",
		"author": hostile,
	})

	escapeSeq := []byte(fmt.Sprintf("\\u%04x", 0x202E))
	assertNoRawOverride := func(label string, out []byte) {
		t.Helper()
		if bytes.ContainsRune(out, 0x202E) {
			t.Errorf("%s contains a raw U+202E byte sequence: %s", label, out)
		}
		if !bytes.Contains(out, escapeSeq) {
			t.Errorf("%s = %s, want it to contain the %s escape", label, out, escapeSeq)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"object", "show", "-C", env.repoDir, objectID}, &stdout, &stderr); code != 0 {
		t.Fatalf("object show failed with %d; stderr: %s", code, stderr.String())
	}
	assertNoRawOverride("object show", stdout.Bytes())

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"object", "show", "-C", env.repoDir, objectID, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("object show --json failed with %d; stderr: %s", code, stderr.String())
	}
	assertNoRawOverride("object show --json", stdout.Bytes())

	// The escape is lossless: decoding --json's output must recover the
	// exact hostile string that was folded, not something stripped or
	// otherwise mangled.
	var obj wire.Object
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectShow, &obj)
	if got, _ := obj.Fields["author"].(string); got != hostile {
		t.Errorf("decoded author field = %q, want the original hostile value %q", got, hostile)
	}
}

// hostileDescriptionSchema returns a writ.schema source declaring one type
// whose type-level and op-level descriptions both carry hostile. The raw
// code point is written into the string via rune concatenation, never a Go
// string literal in source, and the schema grammar itself imposes no
// repertoire gate on description text (only on names) -- schemasrc's string
// literal lexer (lexString) accepts any rune but a bare newline, unescaped
// quote, or invalid UTF-8, so hostile reaches writ.schema, and from there
// `writ schema apply`, completely unmodified.
func hostileDescriptionSchema(hostile string) string {
	return "namespace acme\n" +
		"description \"Acme's vocabulary\"\n\n" +
		"type standup {\n" +
		"  description \"" + hostile + "\"\n\n" +
		"  op create 1 {\n" +
		"    description \"" + hostile + "\"\n" +
		"    title string(200) lww\n" +
		"  }\n" +
		"}\n"
}

// TestSchemaShow_HostileDescriptionRendersEscaped is round 2's finding on
// PR #185: unlike a person-ref value, a schema type's or op's `description`
// reaches the log through the ordinary, conforming `writ schema apply`
// path -- no foreign client needed, because nothing gates description
// content (spec/schema-ops.md never restricts it, only identifiers). It
// must still come out escaped on both of `schema show`'s rendering paths,
// exactly as a person-ref value does on `object show`'s.
func TestSchemaShow_HostileDescriptionRendersEscaped(t *testing.T) {
	env := initTestRepo(t)

	hostile := "Owned by email:alice" + string(rune(0x202E)) + "@good.com"
	writeSchemaFile(t, env.repoDir, hostileDescriptionSchema(hostile))

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}

	escapeSeq := []byte(fmt.Sprintf("\\u%04x", 0x202E))
	assertNoRawOverride := func(label string, out []byte) {
		t.Helper()
		if bytes.ContainsRune(out, 0x202E) {
			t.Errorf("%s contains a raw U+202E byte sequence: %s", label, out)
		}
		if !bytes.Contains(out, escapeSeq) {
			t.Errorf("%s = %s, want it to contain the %s escape", label, out, escapeSeq)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"schema", "show", "-C", env.repoDir, "acme.standup"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema show failed with %d; stderr: %s", code, stderr.String())
	}
	assertNoRawOverride("schema show", stdout.Bytes())

	// schema show --json already goes through emitJSON (json.go), which
	// WRIT-137's half 2 already covers -- confirmed clean here too, plus a
	// lossless round trip, so a regression in emitJSON's own pass would
	// still be caught by this test.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"schema", "show", "-C", env.repoDir, "acme.standup", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema show --json failed with %d; stderr: %s", code, stderr.String())
	}
	if bytes.ContainsRune(stdout.Bytes(), 0x202E) {
		t.Errorf("schema show --json contains a raw U+202E byte sequence: %s", stdout.Bytes())
	}

	var typeInfo wire.SchemaTypeInfo
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindSchemaShow, &typeInfo)
	if typeInfo.Description != hostile {
		t.Errorf("decoded type description = %q, want the original hostile value %q", typeInfo.Description, hostile)
	}
	if len(typeInfo.Ops) != 1 || typeInfo.Ops[0].Description != hostile {
		t.Errorf("decoded op description = %+v, want exactly one op with description %q", typeInfo.Ops, hostile)
	}
}

// TestSchemaPlanPorcelain_HostileDescriptionRendersEscaped covers the third
// rendering chokepoint round 2 named alongside schema show: `schema plan`'s
// human-readable unified diff, which renders schemasrc.Render output built
// straight from a schema object's own (also ungated) description text.
func TestSchemaPlanPorcelain_HostileDescriptionRendersEscaped(t *testing.T) {
	env := initTestRepo(t)

	hostile := "Owned by email:alice" + string(rune(0x202E)) + "@good.com"
	writeSchemaFile(t, env.repoDir, hostileDescriptionSchema(hostile))

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema plan failed with %d; stderr: %s", code, stderr.String())
	}

	if bytes.ContainsRune(stdout.Bytes(), 0x202E) {
		t.Errorf("schema plan diff contains a raw U+202E byte sequence: %s", stdout.Bytes())
	}
	escapeSeq := []byte(fmt.Sprintf("\\u%04x", 0x202E))
	if !bytes.Contains(stdout.Bytes(), escapeSeq) {
		t.Errorf("schema plan diff = %s, want it to contain the %s escape", stdout.Bytes(), escapeSeq)
	}
}
