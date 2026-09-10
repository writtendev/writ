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
	"github.com/writtendev/writ/engine/schemasrc"
	"github.com/writtendev/writ/engine/state"
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
//
// This is also the regression pin for round 3's finding 1: an earlier
// version of the escape (WRIT-137 round 2, PR #185 commit 0f2ba7e) ran
// textsafe.EscapeForbidden over the whole assembled diff document rather
// than over each description value before rendering, which escapes
// U+000A right along with the bidi override -- collapsing this entire
// multi-line diff onto one line. The two assertions this test had before
// (no raw override present, the escape sequence appears somewhere) both
// still pass against that collapsed single line, which is exactly how the
// regression shipped unnoticed; the exact-line assertions below are what
// actually distinguishes "escaped bidi override, normal line structure"
// from "escaped bidi override, everything on one line".
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

	// The benign structure of this diff -- a namespace line, a blank line,
	// a type block with its own blank line, an op block, a field line, two
	// closing braces -- must survive escaping intact: one changed line per
	// source line, exactly as a human reads any other diff they are about
	// to sign into the log. Assert a representative sample of that
	// structure as exact, standalone lines (not substrings of one another
	// or of a collapsed blob) rather than merely checking they occur
	// somewhere in the output.
	lines := make(map[string]bool)
	for _, l := range strings.Split(stdout.String(), "\n") {
		lines[l] = true
	}
	wantLines := []string{
		"--- schema in the log",
		"+++ writ.schema",
		"+namespace acme",
		"+",
		"+type standup {",
		"+  op create 1 {",
		"+    title string(200) lww",
		"+  }",
		"+}",
	}
	for _, w := range wantLines {
		if !lines[w] {
			t.Errorf("schema plan diff missing expected standalone line %q -- line structure did not survive escaping; full output:\n%s", w, stdout.String())
		}
	}
}

// TestSchemaPlanPorcelain_HostileNewlineInDescriptionCannotForgeDiffLine is
// round 3's finding 1's other required test: the reason the "just stop
// escaping U+000A" fix is wrong. A description carries no repertoire gate
// at all (spec/schema-ops.md never restricts description content), so a
// raw newline inside one reaches state.Schema the same way a bidi override
// does -- schemasrc.Parse's own lexer rejects a bare newline inside a
// quoted string literal, so this can never come from a real writ.schema
// file, only from data already folded into the log (a foreign or
// non-conforming client's op, exactly like TestObjectShow_
// HostilePersonRefRendersEscaped's foreign write). Exercised directly
// against escapeSchemaDescriptions and schemasrc.Render -- the same two
// calls buildSchemaPlan makes -- rather than through a full schema-apply
// round trip, because there is no conforming path that plants the raw
// newline in the first place.
func TestSchemaPlanPorcelain_HostileNewlineInDescriptionCannotForgeDiffLine(t *testing.T) {
	sentinel := "forged addition"
	// hostile is built from rune concatenation, not a literal newline in
	// this file's source, per the same discipline as the bidi vectors
	// above.
	hostile := "line one" + string(rune(0x0A)) + sentinel

	current := state.Schema{
		Namespace: "acme",
		Types: []state.SchemaType{{
			Name:        "acme.standup",
			Description: hostile,
			Ops:         []state.SchemaOp{{OpType: "create", OpVersion: 1, Description: "Create a standup"}},
			Fields:      []state.SchemaField{{Name: "title", OpType: "create", OpVersion: 1, ValueType: "string", MaxLength: 200, Strategy: "lww"}},
		}},
	}
	planned := current
	planned.Types = []state.SchemaType{{
		Name:        "acme.standup",
		Description: "line one",
		Ops:         current.Types[0].Ops,
		Fields:      current.Types[0].Fields,
	}}

	currentSource, err := schemasrc.Render(escapeSchemaDescriptions(current))
	if err != nil {
		t.Fatalf("render current: %v", err)
	}
	plannedSource, err := schemasrc.Render(escapeSchemaDescriptions(planned))
	if err != nil {
		t.Fatalf("render planned: %v", err)
	}

	var buf bytes.Buffer
	renderSchemaPlanPorcelain(&buf, &schemaPlanResult{
		currentSource: currentSource,
		plannedSource: plannedSource,
	})
	out := buf.String()

	if !strings.Contains(out, sentinel) {
		t.Fatalf("schema plan diff = %q, want it to still contain %q somewhere (lossless)", out, sentinel)
	}

	var sawSentinelWithLineOne bool
	for _, l := range strings.Split(out, "\n") {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(l, "+"), "-")
		if trimmed == sentinel || strings.TrimSpace(trimmed) == sentinel {
			t.Errorf("schema plan diff = %q, contains %q as its own diff line -- the embedded raw newline forged an extra line", out, l)
		}
		if strings.Contains(l, "line one") && strings.Contains(l, sentinel) {
			sawSentinelWithLineOne = true
		}
	}
	if !sawSentinelWithLineOne {
		t.Errorf("schema plan diff = %q, want %q and %q on the same rendered line (the newline between them escaped, not real)", out, "line one", sentinel)
	}
}
