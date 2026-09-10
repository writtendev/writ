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
	"github.com/writtendev/writ/engine"
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

// TestSchemaPlanPorcelain_HostileConflictReasonRendersEscaped is round 4's
// finding 1 on PR #185: SchemaConflict.Reason can carry a value straight
// out of a foreign client's op body with no repertoire gate and no %q
// quoting. engine/schema.go's RulesFromSchemas formats a define-field's
// field into Reason with %s once spec.ValidateFieldRule has already
// rejected it -- the field name is exactly what failed that check, not
// something this rejection path re-validates -- so a hostile field name
// reaches Reason unescaped. renderSchemaPlanPorcelain's "conflict: %s\n"
// is a rendering chokepoint like any other and must escape it.
//
// The foreign define-field op is appended directly (writeForeignOp),
// bypassing engine/codec.BuildCommit entirely, to the same schema object
// `schema apply` below just created: RulesFromSchemas folds every schema
// object's ops together regardless of which writer ref they came in on,
// so this is exactly what a non-conforming second writer contesting a
// legitimate schema object looks like.
func TestSchemaPlanPorcelain_HostileConflictReasonRendersEscaped(t *testing.T) {
	// hostile stands in for what engine/schema.go's RulesFromSchemas
	// actually builds at the site round 4 traced: a define-field's field
	// name, formatted into Reason with %s once spec.ValidateFieldRule has
	// already rejected it for containing exactly this code point --
	// RulesFromSchemas itself does no further quoting or gating of its
	// own, so this is the literal shape a real conflict's Reason takes.
	hostile := "field rule (create, 1, ali" + string(rune(0x202E)) + "ce) is invalid and was not installed: field \"ali" + string(rune(0x202E)) + "ce\" must match ^[a-z][a-z0-9_]*$"

	var buf bytes.Buffer
	renderSchemaPlanPorcelain(&buf, &schemaPlanResult{
		upToDate: true,
		conflicts: []writ.SchemaConflict{{
			ObjectType: "acme.standup",
			Reason:     hostile,
		}},
	})
	out := buf.Bytes()

	if !bytes.Contains(out, []byte("conflict: ")) {
		t.Fatalf("renderSchemaPlanPorcelain output = %s, want a conflict: line", out)
	}
	if bytes.ContainsRune(out, 0x202E) {
		t.Errorf("renderSchemaPlanPorcelain output contains a raw U+202E byte sequence: %s", out)
	}
	escapeSeq := []byte(fmt.Sprintf("\\u%04x", 0x202E))
	if !bytes.Contains(out, escapeSeq) {
		t.Errorf("renderSchemaPlanPorcelain output = %s, want the conflict line to contain the %s escape", out, escapeSeq)
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

// schemaWithTypeDescription returns a writ.schema source declaring one
// type whose type-level description is desc and whose op-level
// description stays fixed, so a test can change exactly the type
// description between two renders and attribute any diff to that field
// alone.
func schemaWithTypeDescription(desc string) string {
	return "namespace acme\n" +
		"description \"Acme's vocabulary\"\n\n" +
		"type standup {\n" +
		"  description \"" + schemaFileEscape(desc) + "\"\n\n" +
		"  op create 1 {\n" +
		"    description \"Create a standup\"\n" +
		"    title string(200) lww\n" +
		"  }\n" +
		"}\n"
}

// schemaFileEscape returns desc ready to embed inside a writ.schema string
// literal. schemasrc's lexer (lexString) recognizes exactly four escape
// sequences -- \", \\, \n, \t -- and rejects any other backslash sequence
// outright ("unknown escape sequence"), so a data backslash, quote,
// newline or tab must be doubled here to survive as data rather than fail
// to parse. This is the write side of exactly what schemasrc's own
// quoteString does on the read side.
func schemaFileEscape(desc string) string {
	var b strings.Builder
	for _, r := range desc {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeLiteralText returns the six ASCII characters a \uXXXX escape of
// cp would use, as literal text -- backslash, 'u', four lowercase hex
// digits -- built one byte at a time from cp's own value (mirroring
// textsafe.EscapeForbidden's own construction), never typed as a \uXXXX
// sequence in this file's source. That discipline matters here
// specifically: this file's source must never itself spell out the
// escape text for a Forbidden code point, because a test that builds
// "the literal escape text" by typing it directly is indistinguishable,
// on disk, from a test that accidentally embedded the real code point --
// exactly the hazard this whole package exists to catch.
func escapeLiteralText(cp rune) string {
	const hex = "0123456789abcdef"
	return string([]byte{
		'\\', 'u',
		hex[(cp>>12)&0xF], hex[(cp>>8)&0xF], hex[(cp>>4)&0xF], hex[cp&0xF],
	})
}

// TestSchemaPlanPorcelain_DiffIsInjectiveAcrossLiteralEscapeText is round
// 4's finding 2 on PR #185: an earlier version of this fix ran
// textsafe.EscapeForbidden on a description before schemasrc.Render
// quoted it, so quoteString then backslash-escaped the very backslash
// the earlier pass had just introduced. Two different descriptions could
// collapse onto the same rendered text under that composition, so `schema
// plan` could report a real change with an empty diff.
//
// literalEscapeText's description already spells out, as ordinary text,
// the six ASCII characters a \uXXXX escape of U+202E would use (no
// Forbidden rune among them -- see escapeLiteralText above for why this
// is built at runtime rather than typed literally). realCodePointText's
// description differs only by containing the actual U+202E code point at
// the same position (built by rune concatenation, never a literal
// character in this file). A real change from one to the other must
// produce a non-empty diff whose two description lines are visibly
// different from each other, and the printed op count must match:
// exactly one define-type op.
func TestSchemaPlanPorcelain_DiffIsInjectiveAcrossLiteralEscapeText(t *testing.T) {
	env := initTestRepo(t)

	literalEscapeText := "before" + escapeLiteralText(0x202E) + "after"
	realCodePointText := "before" + string(rune(0x202E)) + "after"

	writeSchemaFile(t, env.repoDir, schemaWithTypeDescription(literalEscapeText))
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply (literal escape text) failed with %d; stderr: %s", code, stderr.String())
	}

	writeSchemaFile(t, env.repoDir, schemaWithTypeDescription(realCodePointText))
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema plan failed with %d; stderr: %s", code, stderr.String())
	}
	out := stdout.String()

	// quoteString backslash-escapes literalEscapeText's own data
	// backslash, so its rendered line carries two backslashes before
	// u202e; escapeRenderedSchemaSource turns realCodePointText's raw
	// rune into a single-backslash escape. The two must stay visibly
	// distinct.
	wantOldLine := "-  description \"before\\" + escapeLiteralText(0x202E) + "after\""
	wantNewLine := "+  description \"before" + escapeLiteralText(0x202E) + "after\""
	lines := make(map[string]bool)
	for _, l := range strings.Split(out, "\n") {
		lines[l] = true
	}
	if !lines[wantOldLine] {
		t.Errorf("schema plan diff missing removed-description line %q; full output:\n%s", wantOldLine, out)
	}
	if !lines[wantNewLine] {
		t.Errorf("schema plan diff missing added-description line %q; full output:\n%s", wantNewLine, out)
	}
	if wantOldLine == wantNewLine {
		t.Fatalf("test is broken: the two expected lines are identical, so it cannot distinguish the bug from the fix")
	}

	wantSummary := "1 op(s) to append: 1 define-type\n"
	if !strings.Contains(out, wantSummary) {
		t.Errorf("schema plan diff = %q, want it to contain the summary line %q (a non-empty diff and a matching op count)", out, wantSummary)
	}
}

// TestSchemaPlanJSON_SourceFieldsStayRawAcrossHostileDescription pins
// docs/cli-json.md's promise for current_source and planned_source: each
// is "writ.schema source text rendered from the log's own folded state",
// i.e. schemasrc.Render's raw output, not a display-escaped copy -- the
// human-readable diff's own escaping (renderSchemaPlanPorcelain) must
// never reach these fields. Round 4's finding 2 traced a version of the
// fix that broke this: escaping a description before Render saw it
// changed the schema state Render rendered, so --json's current_source no
// longer matched real source schemasrc.Parse would accept back.
//
// hostile is built by rune concatenation, never a literal character in
// this file's source, per the same discipline as the bidi vectors
// elsewhere in this file.
func TestSchemaPlanJSON_SourceFieldsStayRawAcrossHostileDescription(t *testing.T) {
	env := initTestRepo(t)

	hostile := "Owned by email:alice" + string(rune(0x202E)) + "@good.com"
	writeSchemaFile(t, env.repoDir, hostileDescriptionSchema(hostile))

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema plan --json failed with %d; stderr: %s", code, stderr.String())
	}

	var plan wire.SchemaPlan
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindSchemaPlan, &plan)

	if !plan.UpToDate {
		t.Fatalf("plan.UpToDate = false, want true -- writ.schema was not changed since the apply above")
	}
	if !strings.ContainsRune(plan.CurrentSource, 0x202E) {
		t.Errorf("current_source (decoded) = %q, want it to contain the actual U+202E code point -- Render's raw output, faithfully round-tripped through emitJSON's own lossless escape", plan.CurrentSource)
	}
	if !strings.ContainsRune(plan.PlannedSource, 0x202E) {
		t.Errorf("planned_source (decoded) = %q, want it to contain the actual U+202E code point", plan.PlannedSource)
	}
	// The round 4 regression pre-escaped the description before Render, so
	// the decoded field would have held the literal, double-escaped text
	// below instead of the raw code point -- assert that shape is absent,
	// not only that the raw rune is present.
	if strings.Contains(plan.CurrentSource, `\\u202e`) {
		t.Errorf("current_source (decoded) = %q, contains double-escaped literal text -- want Render's raw code point, not a pre-escaped copy", plan.CurrentSource)
	}
}
