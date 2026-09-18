package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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

// TestDescribeSchemaConflict_EscapesForbiddenCodePoints is round 5's finding
// on PR #185: describeSchemaConflict, the *other* SchemaConflict.Reason
// render site (buildSchemaPlan's "refusing to apply (this would introduce a
// new schema conflict...)" message, printed by renderSchemaError for
// `schema apply`), still formatted Reason with a bare %s -- the identical
// hole TestSchemaPlanPorcelain_HostileConflictReasonRendersEscaped above
// pins at the other site. Round 5 judged this site unreachable in practice
// (conflictsIntroducedByApply only ever calls describeSchemaConflict on a
// conflict RulesFromSchemas classifies as newly introduced by the apply,
// and the one Reason shape that can carry ungated text is a property of a
// single schema object's own field rule -- always present in `before` and
// so never "introduced" by an append-only apply) and routed it to WRIT-226
// rather than fixing it. That argument is not exercised here: the point of
// fixing this site is to stop relying on it, so this test calls
// describeSchemaConflict directly rather than trying to drive a real
// conflictsIntroducedByApply conflict through `schema apply`.
func TestDescribeSchemaConflict_EscapesForbiddenCodePoints(t *testing.T) {
	hostile := "field rule (create, 1, ali" + string(rune(0x202E)) + "ce) is invalid and was not installed: field \"ali" + string(rune(0x202E)) + "ce\" must match ^[a-z][a-z0-9_]*$"

	for _, c := range []writ.SchemaConflict{
		{ObjectType: "acme.standup", Reason: hostile, ObjectIDs: []string{"obj1"}},
		{Namespace: "acme", Reason: hostile, ObjectIDs: []string{"obj1", "obj2"}},
		{Reason: hostile},
	} {
		got := describeSchemaConflict(c)
		if strings.ContainsRune(got, 0x202E) {
			t.Errorf("describeSchemaConflict(%+v) = %q, contains a raw U+202E byte sequence", c, got)
		}
		escapeSeq := fmt.Sprintf("\\u%04x", 0x202E)
		if !strings.Contains(got, escapeSeq) {
			t.Errorf("describeSchemaConflict(%+v) = %q, want it to contain the %s escape", c, got, escapeSeq)
		}
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

// TestSchemaShow_HostileTypeNameRendersEscaped was WRIT-226's reachability
// spike and acceptance test: a define-type op's body `type` used to be
// the one foreign-sourced string that survived every existing gate --
// codec.ValidateEnvelope's decode-path schema leaves `body` unconstrained,
// FoldSchema only checks `type != ""`, and typeIsQualifiedForNamespace
// checked only the namespace prefix and single-segment shape, never
// character grammar -- so a hostile type name installed into Schema.Types
// and printed raw through both of `schema show`'s porcelain arms (the
// bare list and the single-type view).
//
// WRIT-253 closed that gap in the resolver instead of at each render
// site: writ.RulesFromSchemas now drops any declared type failing the
// object_type grammar before it can ever reach Store.Types, so the
// hostile name below no longer installs at all -- it is simply absent
// from `schema show`, not present-and-escaped. What still needs a render
// test is the *conflict* RulesFromSchemas reports for it: Reason is built
// with a bare %q around the hostile string (engine/schema.go), which
// reaches a human exactly like any other SchemaConflict.Reason
// (TestSchemaPlanPorcelain_HostileConflictReasonRendersEscaped's already-
// covered shape) -- this test ties that render path to WRIT-253's own
// repro rather than a synthetic one.
//
// The hostile op is planted with writeForeignOp, not by constructing a
// state.Schema directly: that is what makes it fetched-equivalent (as if
// received from a hostile remote peer) rather than locally constructed,
// the same mechanism WRIT-137's accepted test
// (TestObjectShow_HostilePersonRefRendersEscaped) used. The object id is
// derived the same way `writ schema apply` derives it
// (deriveSchemaObjectID: "schema:" + namespace) so this foreign op lands
// on the very schema object `schema apply` below just created, on a
// different writer ref -- exactly what a non-conforming second writer
// contesting a legitimate schema object looks like.
func TestSchemaShow_HostileTypeNameRendersEscaped(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}

	hostile := "acme.a" + string(rune(0x202E)) + "b"

	writeForeignOp(t, env.repoDir, "fedcba9876543210", "schema", "schema:acme", "define-type", 1, map[string]any{
		"type": hostile,
	})

	escapeSeq := []byte(fmt.Sprintf("\\u%04x", 0x202E))
	assertNoRawOverride := func(label string, out []byte) {
		t.Helper()
		if bytes.ContainsRune(out, 0x202E) {
			t.Errorf("%s contains a raw U+202E byte sequence: %s", label, out)
		}
	}

	// `writ schema show` with no argument: the bare porcelain type-name
	// listing. The hostile type is gated by RulesFromSchemas before it
	// ever reaches Store.Types, so it must be entirely absent here -- no
	// raw form, and (unlike before WRIT-253) no escaped form either,
	// since there is nothing to escape.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"schema", "show", "-C", env.repoDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema show (list) failed with %d; stderr: %s", code, stderr.String())
	}
	assertNoRawOverride("schema show (list)", stdout.Bytes())
	if bytes.Contains(stdout.Bytes(), escapeSeq) {
		t.Errorf("schema show (list) = %s, want no trace of the gated hostile type at all, escaped or not", stdout.Bytes())
	}
	if bytes.Contains(stdout.Bytes(), []byte("acme.a")) {
		t.Errorf("schema show (list) = %s, want the gated hostile type absent from the listing", stdout.Bytes())
	}

	// `writ schema show <hostile-name>`: since the type never installed,
	// this must now fail as "not declared" -- there is no single-type
	// view left to render for it, hostile or otherwise.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"schema", "show", "-C", env.repoDir, hostile}, &stdout, &stderr); code == 0 {
		t.Fatalf("schema show <name> for a gated hostile type unexpectedly succeeded; stdout: %s", stdout.String())
	}

	// The conflict RulesFromSchemas reports for the drop is the render
	// path that still matters here: Reason carries the hostile string
	// verbatim (engine/schema.go's %q), and renderSchemaPlanPorcelain's
	// "conflict: " line is one of the two sites that render it
	// (TestSchemaPlanPorcelain_HostileConflictReasonRendersEscaped covers
	// the other, describeSchemaConflict). Driven from the real resolver
	// output over the actual foreign op above, not a synthetic Reason.
	store, err := openStore(env.repoDir)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer store.Close()
	schemas, err := store.Schema(context.Background())
	if err != nil {
		t.Fatalf("store.Schema: %v", err)
	}
	_, conflicts := writ.RulesFromSchemas(schemas)

	var foundConflict bool
	for _, c := range conflicts {
		if strings.Contains(c.Reason, "acme.a") {
			foundConflict = true
		}
	}
	if !foundConflict {
		t.Fatalf("expected a SchemaConflict naming the gated hostile type, got %+v", conflicts)
	}

	var buf bytes.Buffer
	renderSchemaPlanPorcelain(&buf, &schemaPlanResult{upToDate: true, conflicts: conflicts})
	assertNoRawOverride("schema plan conflicts", buf.Bytes())
	if !bytes.Contains(buf.Bytes(), escapeSeq) {
		t.Errorf("schema plan conflicts = %s, want the conflict line to contain the %s escape", buf.Bytes(), escapeSeq)
	}
}

// TestDecodeGate_RefusesForbiddenCodePointInEnvelope pins the claim
// object.go's comments now make about object_type and op_type: both are
// constrained at decode by spec/schemas/op-envelope.schema.json's
// patterns (^[a-z][a-z0-9-]{0,63}(\.[a-z][a-z0-9-]{0,63})?$ and
// ^[a-z][a-z0-9-]*$ respectively), so a payload carrying a forbidden code
// point in either is refused by codec.ValidateEnvelope before it ever
// folds -- it never reaches the three sites in object.go this ticket
// escaped for consistency rather than as live holes. If
// op-envelope.schema.json's patterns are ever loosened, this test fails
// rather than letting those three sites silently become live holes with a
// comment that no longer matches reality.
func TestDecodeGate_RefusesForbiddenCodePointInEnvelope(t *testing.T) {
	hostile := "acme.a" + string(rune(0x202E)) + "b"

	base := func() map[string]any {
		return map[string]any{
			"object_id":   "obj-1",
			"object_type": "acme.standup",
			"op_type":     "create",
			"op_version":  1,
			"body":        map[string]any{},
		}
	}

	for _, tc := range []struct {
		name  string
		field string
	}{
		{name: "object_type", field: "object_type"},
		{name: "op_type", field: "op_type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := base()
			payload[tc.field] = hostile

			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}

			if err := codec.ValidateEnvelope(raw); err == nil {
				t.Fatalf("ValidateEnvelope(%s) = nil, want a schema-violation error refusing the forbidden code point in %s", raw, tc.field)
			}
		})
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

// TestSchemaApply_HostileFetchedNamespaceNotCountedOrRendered is round 1's
// finding 1 on PR #195, revisited after WRIT-254 round-1 finding 3 changed
// what actually reaches the render call this test pins: a schema object's
// `namespace` is folded from a `create` op's *body*
// (engine/state/schema.go, `case "create"`), the same ungated slot as
// define-type's body `type`, so a fetched peer's hostile namespace text
// carries no repertoire gate of its own at fold time.
//
// Before round-1 finding 3, schemaNamespaces counted every folded schema
// object's namespace unconditionally, so a hostile one reached `schema
// apply`'s mint summary and needed escaping at render
// (cmd/writ/schema.go's textsafe.EscapeForbidden call). Finding 3 gave
// schemaNamespaces the same derived-id gate resolveSchemaTypes and
// resolveSchemaTarget already have: a schema object whose id disagrees
// with "schema:" + its own namespace is dropped, not counted. The
// hostile object below is exactly that shape (namespace disagrees with
// its non-derived id "foreign-evil-schema"), so it is now excluded
// before the render call this test used to exercise ever sees it -- this
// pins the drop, not an escape.
//
// A hostile-Unicode namespace has no other way to reach here: the
// derived form for one ("schema:" + hostile) is not even a legal
// object_id -- op-envelope.schema.json's object_id pattern
// (^[\x21-\x7e]+$) admits no U+202E, and engine/codec/decode.go enforces
// that schema against every op unconditionally, rejecting a
// non-conforming one outright (RejectSchemaViolation) before it ever
// reaches FoldSchema. So a hostile namespace can only ever ride in on a
// non-derived id, which is exactly the shape schemaNamespaces' new gate
// (and resolveSchemaTypes' matching one) drops.
func TestSchemaApply_HostileFetchedNamespaceNotCountedOrRendered(t *testing.T) {
	env := initTestRepo(t)

	hostile := "ev" + string(rune(0x202E)) + "il"

	// A foreign writer's own schema object, on its own writer ref -- what
	// fetching a hostile peer's schema chain leaves behind. The object id
	// deliberately does NOT carry the "schema:" prefix: see the doc
	// comment above for why that is the only reachable shape.
	writeForeignOp(t, env.repoDir, "fedcba9876543210", "schema", "foreign-evil-schema", "create", 1, map[string]any{
		"namespace": hostile,
	})

	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}

	out := stdout.Bytes()
	// The dropped foreign object must not inflate the count: only the
	// local "acme" mint counts, so the singular "namespace:" form -- not
	// "namespaces:" -- is what a correct apply prints.
	if !bytes.Contains(out, []byte("1 namespace: acme")) {
		t.Errorf("schema apply output = %s, want exactly \"1 namespace: acme\" -- the dropped foreign object must not be counted", out)
	}
	if bytes.Contains(out, []byte("namespaces:")) {
		t.Errorf("schema apply output = %s, want the singular \"namespace:\" form; \"namespaces:\" would mean the dropped foreign object was still counted", out)
	}
	if bytes.ContainsRune(out, 0x202E) {
		t.Errorf("schema apply contains a raw U+202E byte sequence: %s", out)
	}
	if bytes.Contains(out, []byte(hostile)) {
		t.Errorf("schema apply output = %s, want no trace of the dropped foreign object's hostile namespace at all", out)
	}
}

// TestSchemaPlan_HostileFetchedOpTypeRendersEscaped is round 1's finding 2
// on PR #195: `schema plan`'s removal-refusal messages formatted a folded
// define-op's / define-field's body `op_type` with %s, right beside a
// %q-escaped type or field name (Go's %q escapes Cf; %s does not). The
// value comes from the same ungated body slot as everything else this
// ticket covers.
//
// Reachability is the default consequence of fetching any hostile
// define-op, not an exotic arrangement: the local writ.schema never
// declares that op, so the delta reads as a removal and `schema plan`
// refuses with the attacker-chosen name in the message a human is reading
// to decide what to do. The resolver's own grammar gate (validOpTypeGrammar
// in resolveSchemaTypes) does not cover this path -- the plan delta
// compares against raw folded state.Schema, upstream of the resolver.
func TestSchemaPlan_HostileFetchedOpTypeRendersEscaped(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}

	hostile := "a" + string(rune(0x202E)) + "b"

	// Both removal-refusal arms that interpolate an op type: the define-op
	// one ("op %s version %d on type %q was removed") and the define-field
	// one ("field %q on op %s version %d of type %q was removed").
	writeForeignOp(t, env.repoDir, "fedcba9876543210", "schema", "schema:acme", "define-op", 1, map[string]any{
		"type":        "acme.standup",
		"op_type":     hostile,
		"op_version":  "1",
		"description": "hostile op",
	})
	writeForeignOp(t, env.repoDir, "fedcba9876543211", "schema", "schema:acme", "define-field", 1, map[string]any{
		"type":       "acme.standup",
		"op_type":    hostile,
		"op_version": "1",
		"field":      "note",
		"value_type": "string",
		"strategy":   "lww",
	})

	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan"}, &stdout, &stderr); code != 1 {
		t.Fatalf("schema plan exited %d, want 1 (refusing to plan); stdout: %s stderr: %s", code, stdout.String(), stderr.String())
	}

	out := stderr.Bytes()
	if !bytes.Contains(out, []byte("refusing to plan")) {
		t.Fatalf("schema plan stderr = %s, want the removal-refusal message", out)
	}
	if bytes.ContainsRune(out, 0x202E) {
		t.Errorf("schema plan refusal contains a raw U+202E byte sequence: %s", out)
	}
	escapeSeq := []byte(fmt.Sprintf("\\u%04x", 0x202E))
	if !bytes.Contains(out, escapeSeq) {
		t.Errorf("schema plan refusal = %s, want it to contain the %s escape", out, escapeSeq)
	}
}

// TestObjectUnknownType_HostileDeclaredTypeListRendersEscaped covers the
// third caller of a folded define-type body `type`: declaredTypeNames
// (object.go), whose sorted list is joined into the "not declared by the
// installed vocabulary (declares: ...)" message emitted by both
// `writ object list <type>` and `writ object create`/`apply` (through
// resolveOpVersion). It used to be the same value
// TestSchemaShow_HostileTypeNameRendersEscaped plants, reached by a
// lower-friction route -- a plain typo in the type argument prints the
// attacker-chosen string beside a %q-quoted (and so already escaped)
// copy of the user's own input.
//
// WRIT-253's resolver gate means declaredTypeNames can no longer be
// handed the hostile type at all: RulesFromSchemas drops it before
// Store.Types ever returns it, so this list has nothing of the hostile
// name left to escape. What this test now pins is that absence -- the
// declares: list carries no trace of it, raw or escaped -- while the
// legitimate declared types (fullTestSchema's own) still populate it
// normally, so the list itself is not simply empty by accident.
func TestObjectUnknownType_HostileDeclaredTypeListRendersEscaped(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}

	hostile := "acme.a" + string(rune(0x202E)) + "b"

	writeForeignOp(t, env.repoDir, "fedcba9876543210", "schema", "schema:acme", "define-type", 1, map[string]any{
		"type": hostile,
	})

	escapeSeq := []byte(fmt.Sprintf("\\u%04x", 0x202E))
	assertNoTraceOfHostileType := func(label string, out []byte) {
		t.Helper()
		if bytes.ContainsRune(out, 0x202E) {
			t.Errorf("%s contains a raw U+202E byte sequence: %s", label, out)
		}
		if bytes.Contains(out, escapeSeq) {
			t.Errorf("%s = %s, want no trace of the gated hostile type at all, escaped or not", label, out)
		}
		if !bytes.Contains(out, []byte("declares:")) {
			t.Errorf("%s = %s, want a non-empty declares: list of the legitimately declared types", label, out)
		}
	}

	// `writ object list <undeclared>`: the declares: list goes straight to
	// stderr, no renderErr in between.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"object", "list", "-C", env.repoDir, "acme.nosuch"}, &stdout, &stderr); code == 0 {
		t.Fatalf("object list with an undeclared type unexpectedly succeeded; stdout: %s", stdout.String())
	}
	assertNoTraceOfHostileType("object list (undeclared type)", stderr.Bytes())

	// `writ object create <undeclared> <op>`: the same list, from
	// resolveOpVersion's error, printed straight to stderr as well.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"object", "create", "-C", env.repoDir, "acme.nosuch", "create"}, &stdout, &stderr); code == 0 {
		t.Fatalf("object create with an undeclared type unexpectedly succeeded; stdout: %s", stdout.String())
	}
	assertNoTraceOfHostileType("object create (undeclared type)", stderr.Bytes())
}

// TestObjectCreate_HostileFetchedEnumRendersEscaped covers the one
// foreign-sourced string that reaches a human view through an *engine
// error* rather than through a value writ formats itself: a fetched
// define-field's body `enum` members.
//
// Nothing on the read path gates them. spec.ValidateFieldRule constrains
// the field, target and key columns against identifierGrammar and the
// value_type/strategy names against their closed catalogues, but it never
// looks at the members of an enum -- so a peer's entirely valid,
// namespace-qualified vocabulary installs cleanly with whatever it chose
// there. `writ schema plan` reports no conflict, and the first a local
// user hears of it is engine/internal/value.Check's membership error,
// which formats the declared list with a bare %v and reaches stderr
// through renderErr.
//
// Friction is one typo on a peer-published type, in the error a human is
// reading to work out what they mistyped -- beside a %q-quoted, and so
// already escaped, copy of their own input.
func TestObjectCreate_HostileFetchedEnumRendersEscaped(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}

	hostile := "clo" + string(rune(0x202E)) + "sed"

	// A peer publishing its own namespace-qualified vocabulary, one op per
	// foreign writer (writeForeignOp plants a single-op chain per writer).
	peer := func(writerID, opType string, body map[string]any) {
		t.Helper()
		writeForeignOp(t, env.repoDir, writerID, "schema", "schema:peer", opType, 1, body)
	}
	peer("eeeeeeeeeeeeeee0", "create", map[string]any{"namespace": "peer"})
	peer("eeeeeeeeeeeeeee1", "define-type", map[string]any{"type": "peer.thing"})
	peer("eeeeeeeeeeeeeee2", "define-op", map[string]any{
		"type": "peer.thing", "op_type": "create", "op_version": "1",
	})
	peer("eeeeeeeeeeeeeee3", "define-field", map[string]any{
		"type": "peer.thing", "op_type": "create", "op_version": "1",
		"field": "status", "value_type": "enum", "strategy": "lww",
		"enum": []any{"open", hostile},
	})

	// The peer's schema is valid: nothing warns the local user about it
	// before the typo below.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "plan"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema plan exited %d, want 0 (the peer's vocabulary is valid); stdout: %s stderr: %s", code, stdout.String(), stderr.String())
	}

	// One typo in an enum value on the peer-published type.
	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), []string{"object", "create", "-C", env.repoDir, "peer.thing", "create", "-field", "status=oepn"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("object create with a non-member enum value unexpectedly succeeded; stdout: %s", stdout.String())
	}

	out := stderr.Bytes()
	if !bytes.Contains(out, []byte("is not a member of the declared enum")) {
		t.Fatalf("object create stderr = %s, want the enum membership rejection", out)
	}
	if bytes.ContainsRune(out, 0x202E) {
		t.Errorf("object create (non-member enum value) contains a raw U+202E byte sequence: %s", out)
	}
	escapeSeq := []byte(fmt.Sprintf("\\u%04x", 0x202E))
	if !bytes.Contains(out, escapeSeq) {
		t.Errorf("object create (non-member enum value) = %s, want it to contain the %s escape", out, escapeSeq)
	}
}

// TestObjectCreate_HostileFetchedEnumCannotForgeAnErrLine pins the other
// half of the same reachable path: a fetched define-field's enum member
// must not be able to put a line of its own choosing on writ's stderr.
//
// A member with a U+000A at *both* ends of its payload leaves the
// surrounding format string's trailing text on a line of its own, so the
// forged line carries nothing of writ's -- a byte-for-byte attacker-chosen
// line, "writ: " prefix and all, indistinguishable from writ's own
// diagnostics (round 5 review of PR #195).
func TestObjectCreate_HostileFetchedEnumCannotForgeAnErrLine(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}

	forged := "writ: error: your signing key is compromised"
	hostile := "closed\n" + forged + "\n"

	peer := func(writerID, opType string, body map[string]any) {
		t.Helper()
		writeForeignOp(t, env.repoDir, writerID, "schema", "schema:peer", opType, 1, body)
	}
	peer("eeeeeeeeeeeeeee0", "create", map[string]any{"namespace": "peer"})
	peer("eeeeeeeeeeeeeee1", "define-type", map[string]any{"type": "peer.thing"})
	peer("eeeeeeeeeeeeeee2", "define-op", map[string]any{
		"type": "peer.thing", "op_type": "create", "op_version": "1",
	})
	peer("eeeeeeeeeeeeeee3", "define-field", map[string]any{
		"type": "peer.thing", "op_type": "create", "op_version": "1",
		"field": "status", "value_type": "enum", "strategy": "lww",
		"enum": []any{"open", hostile},
	})

	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), []string{"object", "create", "-C", env.repoDir, "peer.thing", "create", "-field", "status=oepn"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("object create with a non-member enum value unexpectedly succeeded; stdout: %s", stdout.String())
	}

	out := stderr.String()
	if !strings.Contains(out, "is not a member of the declared enum") {
		t.Fatalf("object create stderr = %q, want the enum membership rejection", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for i, line := range lines {
		if line == forged {
			t.Errorf("object create stderr line %d is the peer's forged line verbatim: %q\nfull report: %q", i, line, out)
		}
	}
	if len(lines) != 1 {
		t.Errorf("object create stderr rendered on %d lines, want the whole report on one: %q", len(lines), out)
	}
}

// TestRenderErr_SigningFailureKeepsItsSecondLine is the counterweight to the
// test above: renderErr's escape must not flatten the one error in the tree
// whose text carries a U+000A as structure rather than as data.
//
// engine/codec/sign.go builds it -- `fmt.Errorf("codec: ssh-keygen -Y sign:
// %w\n%s", err, ...)` -- so a missing, unreadable or passphrase-protected
// signing key reports ssh-keygen's own diagnostic on a second line, which is
// the entire point of capturing its combined output. A first-run
// misconfiguration, not an exotic state.
//
// Escaping the whole assembled line without sparing this U+000A collapsed
// the two lines into one, with the escape text sitting where the break
// belonged (round 4 review of PR #195). It is spared by conditioning on the
// error rather than on the code point -- subprocessFailure -- so that a
// log-sourced U+000A is still escaped; the test above pins that half.
func TestRenderErr_SigningFailureKeepsItsSecondLine(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}

	// The commonest way to reach sign.go's two-line error: a signing key
	// that is configured but not there.
	setGitConfig(t, env.repoDir, "user.signingKey", filepath.Join(t.TempDir(), "absent_ed25519"))

	stdout.Reset()
	stderr.Reset()
	code := run(context.Background(), []string{"object", "create", "-C", env.repoDir, "acme.standup", "create", "-field", "title=hi"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("object create with a missing signing key unexpectedly succeeded; stdout: %s", stdout.String())
	}

	out := stderr.String()
	if !strings.Contains(out, "ssh-keygen -Y sign") {
		t.Fatalf("object create stderr = %q, want the ssh-keygen signing failure", out)
	}
	newlineEscape := fmt.Sprintf("\\u%04x", 0x000A)
	if strings.Contains(out, newlineEscape) {
		t.Errorf("object create stderr carries a literal %s escape, so ssh-keygen's diagnostic was flattened onto one line: %q", newlineEscape, out)
	}
	if got := len(strings.Split(strings.TrimRight(out, "\n"), "\n")); got < 2 {
		t.Errorf("object create stderr rendered on %d line(s), want ssh-keygen's diagnostic on a line of its own: %q", got, out)
	}
}

// TestForeignOp_KeyArityCollision_ReadStaysClean is WRIT-234's acceptance
// test: reproduction 2 from that ticket's investigation, replayed against
// this ticket's fix. Alice authors and applies an entirely benign schema
// (fullTestSchema's "approval" v1, keyed-lww key(subject) — one column,
// nothing mismatched) and writes one v1 op on her own object. A foreign
// writer then plants, with writeForeignOp — bypassing dag.Append,
// codec.BuildCommit and validateProducerOp exactly as a fetched peer ref
// would — a schema extension binding the *same* field ("verdict") under
// the *same* op_type ("approval") to a new op_version 2 whose key tuple
// has different arity ((subject, revision) instead of (subject) alone),
// plus one v2 data op on Alice's own object.
//
// On `main`, before this ticket, key and key_types were on
// spec/schema-ops.md §8's "MAY freely change" list, so both versions'
// rules resolved and installed under the shared "verdict" target, and
// engine/internal/fold.keyedLWWAccumulator.Result's sort comparator —
// handed two entries whose key tuples are of different lengths — read
// past the end of the shorter one. Because Result ranges a Go map to build
// the slice it sorts, the panic was intermittent: reachable roughly one
// read in eight, not on every invocation. This ticket closes the carve-out
// (spec/fieldrules.go's FindTargetDisagreement), so spec.CheckTargetAgreement
// now rejects the pair as an ordinary "key" disagreement and
// engine/schema.go's resolveSchemaTypes withholds the whole "verdict"
// target before RulesFromSchemas ever emits its rules — no keyed-lww
// accumulator is ever constructed for it, so the comparator is never
// reached at all.
//
// The loop of 50 iterations is what the ticket's own reproduction used to
// defeat the map-order intermittency (its measured natural rate was
// roughly one panic in eight reads): 50 identical reads make a surviving
// panic overwhelmingly likely to be caught, while a single invocation
// could pass by chance even against the unfixed comparator.
func TestForeignOp_KeyArityCollision_ReadStaysClean(t *testing.T) {
	env := initTestRepo(t)
	writeSchemaFile(t, env.repoDir, fullTestSchema)

	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-C", env.repoDir, "schema", "apply"}, &stdout, &stderr); code != 0 {
		t.Fatalf("schema apply failed with %d; stderr: %s", code, stderr.String())
	}

	// Alice's own benign v1 op: fullTestSchema's "approval" op 1 keys
	// "verdict" on key(subject person-ref) alone.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "acme.standup", "approval",
		"-field", "verdict=approve",
		"-field", "subject=email:alice@example.com",
		"--json",
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("object create (Alice's v1 approval) failed with %d; stderr: %s", code, stderr.String())
	}
	var created wire.ObjectCreated
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectCreate, &created)
	objectID := created.ObjectID

	// A foreign writer's schema extension on the same schema object
	// ("schema:acme", the id apply derived from the namespace): a new
	// "approval" op_version 2 for the same field "verdict", key arity 2
	// instead of 1. Two distinct writer ids, exactly as the existing
	// foreign-schema tests above use, since define-op and define-field are
	// ordinarily authored by whichever local client ran `schema apply`
	// last — here, deliberately, someone else's.
	writeForeignOp(t, env.repoDir, "fedcba9876543210", "schema", "schema:acme", "define-op", 1, map[string]any{
		"type": "acme.standup", "op_type": "approval", "op_version": "2",
		"description": "Approve or block a standup (v2)",
	})
	writeForeignOp(t, env.repoDir, "fedcba9876543211", "schema", "schema:acme", "define-field", 1, map[string]any{
		"type": "acme.standup", "op_type": "approval", "op_version": "2",
		"field": "verdict", "value_type": "enum", "enum": []any{"approve", "block"},
		"strategy": "keyed-lww",
		"key":      []any{"subject", "revision"},
		"key_types": map[string]any{
			"subject":  "person-ref",
			"revision": "string",
		},
	})

	// The same foreign writer's v2 data op, on Alice's own object —
	// reproduction 2's step 3's second half: a remote writer's op landing
	// on a victim's object, not just on their own.
	writeForeignOp(t, env.repoDir, "fedcba9876543212", "acme.standup", objectID, "approval", 2, map[string]any{
		"subject":  "email:alice@example.com",
		"revision": "deadbeef",
		"verdict":  "block",
	})

	var first string
	for i := 0; i < 50; i++ {
		stdout.Reset()
		stderr.Reset()
		code := run(context.Background(), []string{"object", "show", "-C", env.repoDir, objectID}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("iteration %d: object show exited %d (want a clean read, no panic); stderr: %s", i, code, stderr.String())
		}
		out := stdout.String()
		if i == 0 {
			first = out
			continue
		}
		if out != first {
			t.Fatalf("iteration %d: object show output differs from iteration 0's:\n got:  %q\nwant: %q", i, out, first)
		}
	}
}
