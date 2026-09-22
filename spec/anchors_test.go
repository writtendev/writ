package spec_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/writtendev/writ/spec"
)

// Context-capture constants from spec/anchors.md §Context capture: ranges
// spanning more than selectedFullMax lines store the first and last
// headTail lines with the middle counted in "omitted".
const (
	selectedFullMax = 64
	headTail        = 32
)

const anchorSchemaID = "https://writ.dev/spec/anchor.schema.json"

// compileAnchorSchema compiles the anchor schema; compilation also
// validates it against the draft 2020-12 meta-schema.
func compileAnchorSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	raw, err := spec.FS.ReadFile("schemas/anchor.schema.json")
	if err != nil {
		t.Fatalf("reading schema: %v", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoding schema: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(anchorSchemaID, doc); err != nil {
		t.Fatalf("adding schema resource: %v", err)
	}
	sch, err := c.Compile(anchorSchemaID)
	if err != nil {
		t.Fatalf("compiling schema: %v", err)
	}
	return sch
}

// anchorInvariantRule is one cross-field rule of spec/anchors.md that JSON
// Schema cannot express, checked against the whole anchor value. It assumes
// the instance already passed the schema; on schema-invalid input its
// answer is meaningless. A rule returns a descriptive error when it rejects
// the anchor, nil when it accepts.
type anchorInvariantRule func(a map[string]any) error

// anchorInvariantTable enforces the cross-field rules of spec/anchors.md,
// keyed by the token spec/testdata/anchors/invalid/index.json's
// invariant_rule field names each one with (spec/anchors.md's opening
// paragraph names the same six tokens and where each rule is stated).
//
// Three rules are deliberately guarded on another rule's precondition, so
// that a vector tripping more than one raw condition still names exactly
// one rule: context-length, elided-lines-count and omitted-arithmetic all
// presuppose a well-ordered range (end >= start, rangeOrderOK's job), and
// omitted-arithmetic additionally presupposes the elided shape
// (elidedLinesCountOK's job, exactly 64 stored lines) before its own
// arithmetic means anything. Without these guards, end-before-start.json
// (start 5, end 3, 1 stored line) would trip both range-order and
// context-length, and omitted-with-short-lines.json (1..100, 3 lines,
// omitted 97) would trip both elided-lines-count and omitted-arithmetic —
// each losing the one-rule binding this table exists to provide. The
// guards encode a real dependency (the context rules presuppose a
// well-formed range; the arithmetic rule presupposes the elided shape), not
// a fixture-fitting shortcut.
var anchorInvariantTable = map[string]anchorInvariantRule{
	"path-segments":        pathSegmentsOK,
	"oid-length-agreement": oidLengthAgreementOK,
	"range-order":          rangeOrderOK,
	"context-length":       contextLengthOK,
	"elided-lines-count":   elidedLinesCountOK,
	"omitted-arithmetic":   omittedArithmeticOK,
}

// anchorInvariantTokens is anchorInvariantTable's key set in sorted order,
// the composition order anchorInvariants below runs the table in.
var anchorInvariantTokens = sortedKeys(anchorInvariantTable)

func sortedKeys(m map[string]anchorInvariantRule) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// anchorInvariants runs every rule in anchorInvariantTable, in sorted-token
// order, and returns the first rejection. Used by validateVector (the
// valid/ and github/ corpora), which only asks whether the anchor is
// accepted as a whole; TestInvalidAnchorVectors below calls the table's
// rules directly instead, so it can hold each vector to exactly one.
func anchorInvariants(a map[string]any) error {
	for _, token := range anchorInvariantTokens {
		if err := anchorInvariantTable[token](a); err != nil {
			return err
		}
	}
	return nil
}

// eachSide calls f for old and new, in that order, for whichever of the two
// sides the anchor actually has.
func eachSide(a map[string]any, f func(key string, s map[string]any)) {
	for _, key := range []string{"old", "new"} {
		s, ok := a[key].(map[string]any)
		if !ok {
			continue
		}
		f(key, s)
	}
}

// sideRangeBounds reads a side's range.start/range.end as integers. ok is
// false when the side has no range, or start/end are not both integers (the
// latter schema-invalid for an instance that reached this far, so never hit
// on schema-valid input; the guard exists so the range-dependent rules below
// degrade to "not applicable" rather than panic on a malformed side).
func sideRangeBounds(s map[string]any) (start, end int64, ok bool) {
	rng, isMap := s["range"].(map[string]any)
	if !isMap {
		return 0, 0, false
	}
	start, sok := jsonInt(rng["start"])
	end, eok := jsonInt(rng["end"])
	if !sok || !eok {
		return 0, 0, false
	}
	return start, end, true
}

// pathSegmentsOK is the path-segments rule: no side's path may contain an
// empty, ".", or ".." segment.
func pathSegmentsOK(a map[string]any) error {
	var err error
	eachSide(a, func(key string, s map[string]any) {
		if err != nil {
			return
		}
		path, ok := s["path"].(string)
		if !ok {
			return
		}
		for _, seg := range strings.Split(path, "/") {
			if seg == "" || seg == "." || seg == ".." {
				err = fmt.Errorf("%s: path %q: segment %q not allowed", key, path, seg)
				return
			}
		}
	})
	return err
}

// oidLengthAgreementOK is the oid-length-agreement rule: every OID across
// both sides of one anchor must share a length, because the containing repo
// has one object format.
func oidLengthAgreementOK(a map[string]any) error {
	var oidLens []int
	eachSide(a, func(_ string, s map[string]any) {
		for _, f := range []string{"commit", "blob"} {
			if oid, ok := s[f].(string); ok {
				oidLens = append(oidLens, len(oid))
			}
		}
	})
	for _, n := range oidLens {
		if n != oidLens[0] {
			return fmt.Errorf("mixed OID lengths %v: one repo has one object format", oidLens)
		}
	}
	return nil
}

// rangeOrderOK is the range-order rule: range.end must be >= range.start.
func rangeOrderOK(a map[string]any) error {
	var err error
	eachSide(a, func(key string, s map[string]any) {
		if err != nil {
			return
		}
		start, end, ok := sideRangeBounds(s)
		if !ok {
			return
		}
		if end < start {
			err = fmt.Errorf("%s: range end %d < start %d", key, end, start)
		}
	})
	return err
}

// contextLengthOK is the context-length rule: when omitted is absent,
// len(context.lines) must equal the range size. Guarded on a well-ordered
// range (rangeOrderOK's precondition) and on omitted's absence
// (elidedLinesCountOK and omittedArithmeticOK's precondition) — see
// anchorInvariantTable's doc.
func contextLengthOK(a map[string]any) error {
	var err error
	eachSide(a, func(key string, s map[string]any) {
		if err != nil {
			return
		}
		start, end, ok := sideRangeBounds(s)
		if !ok || end < start {
			return
		}
		size := end - start + 1
		ctx, ok := s["context"].(map[string]any)
		if !ok {
			return
		}
		lines, _ := ctx["lines"].([]any)
		if _, hasOmitted := jsonInt(ctx["omitted"]); hasOmitted {
			return
		}
		if int64(len(lines)) != size {
			err = fmt.Errorf("%s: context.lines has %d entries for a %d-line range and no omitted count", key, len(lines), size)
		}
	})
	return err
}

// elidedLinesCountOK is the elided-lines-count rule: when omitted is
// present, context.lines must hold exactly selectedFullMax entries (the
// first headTail + last headTail). Guarded on a well-ordered range
// (rangeOrderOK's precondition) — see anchorInvariantTable's doc.
func elidedLinesCountOK(a map[string]any) error {
	var err error
	eachSide(a, func(key string, s map[string]any) {
		if err != nil {
			return
		}
		start, end, ok := sideRangeBounds(s)
		if !ok || end < start {
			return
		}
		ctx, ok := s["context"].(map[string]any)
		if !ok {
			return
		}
		lines, _ := ctx["lines"].([]any)
		if _, hasOmitted := jsonInt(ctx["omitted"]); !hasOmitted {
			return
		}
		if len(lines) != selectedFullMax {
			err = fmt.Errorf("%s: omitted present but context.lines has %d entries, want %d (first %d + last %d)", key, len(lines), selectedFullMax, headTail, headTail)
		}
	})
	return err
}

// omittedArithmeticOK is the omitted-arithmetic rule: omitted must equal
// (end - start + 1) - selectedFullMax. Guarded on a well-ordered range
// (rangeOrderOK's precondition) and on the elided shape already holding
// (elidedLinesCountOK's precondition, exactly selectedFullMax stored lines)
// — see anchorInvariantTable's doc.
func omittedArithmeticOK(a map[string]any) error {
	var err error
	eachSide(a, func(key string, s map[string]any) {
		if err != nil {
			return
		}
		start, end, ok := sideRangeBounds(s)
		if !ok || end < start {
			return
		}
		size := end - start + 1
		ctx, ok := s["context"].(map[string]any)
		if !ok {
			return
		}
		lines, _ := ctx["lines"].([]any)
		omitted, hasOmitted := jsonInt(ctx["omitted"])
		if !hasOmitted || len(lines) != selectedFullMax {
			return
		}
		if want := size - selectedFullMax; omitted != want {
			err = fmt.Errorf("%s: omitted is %d, want %d for a %d-line range", key, omitted, want, size)
		}
	})
	return err
}

func jsonInt(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok || f != float64(int64(f)) {
		return 0, false
	}
	return int64(f), true
}

// validateVector runs one anchor instance through schema and invariants.
func validateVector(t *testing.T, sch *jsonschema.Schema, raw []byte) error {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decoding vector: %v", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	var a map[string]any
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("re-decoding vector: %v", err)
	}
	if err := anchorInvariants(a); err != nil {
		return fmt.Errorf("invariant: %w", err)
	}
	return nil
}

func readDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := spec.FS.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestAnchorSchemaCompiles(t *testing.T) {
	compileAnchorSchema(t)
}

func TestValidAnchorVectors(t *testing.T) {
	sch := compileAnchorSchema(t)
	for _, name := range readDirNames(t, "testdata/anchors/valid") {
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/anchors/valid/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateVector(t, sch, raw); err != nil {
				t.Errorf("valid vector rejected: %v", err)
			}
		})
	}
}

func TestInvalidAnchorVectors(t *testing.T) {
	sch := compileAnchorSchema(t)

	rawIndex, err := spec.FS.ReadFile("testdata/anchors/invalid/index.json")
	if err != nil {
		t.Fatal(err)
	}
	var index map[string]struct {
		Kind          string `json:"kind"`
		InvariantRule string `json:"invariant_rule,omitempty"`
		Reason        string `json:"reason"`
	}
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatalf("decoding index.json: %v", err)
	}

	names := readDirNames(t, "testdata/anchors/invalid")
	files := make(map[string]bool)
	for _, name := range names {
		if name != "index.json" {
			files[name] = true
		}
	}
	for name := range index {
		if !files[name] {
			t.Errorf("index.json lists %s but the file does not exist", name)
		}
	}

	ruleNamed := make(map[string]bool)
	for name := range files {
		entry, ok := index[name]
		if !ok {
			t.Errorf("%s has no index.json entry recording its expected rejection", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/anchors/invalid/" + name)
			if err != nil {
				t.Fatal(err)
			}
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("decoding vector: %v", err)
			}
			schemaErr := sch.Validate(inst)
			switch entry.Kind {
			case "schema":
				if entry.InvariantRule != "" {
					t.Fatalf("%s: kind %q does not take invariant_rule, but index.json sets it to %q", name, entry.Kind, entry.InvariantRule)
				}
				if schemaErr == nil {
					t.Errorf("schema accepted it; expected rejection: %s", entry.Reason)
				}
			case "invariant":
				// Invariant vectors prove the rule lives outside the
				// schema: the schema must accept them.
				if schemaErr != nil {
					t.Errorf("schema rejected an invariant-kind vector (%v); expected only the invariant to fail: %s", schemaErr, entry.Reason)
				}
				rule, ok := anchorInvariantTable[entry.InvariantRule]
				if !ok {
					t.Fatalf("%s: index.json names unknown or missing invariant_rule %q", name, entry.InvariantRule)
				}
				ruleNamed[entry.InvariantRule] = true
				var a map[string]any
				if err := json.Unmarshal(raw, &a); err != nil {
					t.Fatal(err)
				}
				// The named rule must reject the vector -- that is what
				// binds it to the rule its "reason" names, rather than to
				// whichever rule in the table happens to reject it.
				if err := rule(a); err == nil {
					t.Errorf("invariant_rule %q accepted it; expected rejection: %s", entry.InvariantRule, entry.Reason)
				}
				// Every other rule must accept it. Without this, deleting a
				// rule this vector does not name could still redden it, and
				// the vector would not actually be pinned to the rule it
				// claims.
				for otherToken, other := range anchorInvariantTable {
					if otherToken == entry.InvariantRule {
						continue
					}
					if err := other(a); err != nil {
						t.Errorf("invariant_rule %q also rejected it (%v), but the vector is pinned to %q; expected every other invariant rule to accept: %s", otherToken, err, entry.InvariantRule, entry.Reason)
					}
				}
			default:
				t.Errorf("index.json kind %q unknown (want schema or invariant)", entry.Kind)
			}
		})
	}

	for token := range anchorInvariantTable {
		if !ruleNamed[token] {
			t.Errorf("no testdata/anchors/invalid vector names invariant_rule %q; it is untested", token)
		}
	}
}

// TestGitHubConversionVectors checks the anchor halves of the informative
// {github, pr, anchor} conversion vectors (spec/anchors.md appendix A).
// The conversion itself is exercised by whatever consumer imports these
// comments, once one exists.
func TestGitHubConversionVectors(t *testing.T) {
	sch := compileAnchorSchema(t)
	for _, name := range readDirNames(t, "testdata/anchors/github") {
		t.Run(name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/anchors/github/" + name)
			if err != nil {
				t.Fatal(err)
			}
			var vec struct {
				GitHub json.RawMessage `json:"github"`
				PR     json.RawMessage `json:"pr"`
				Anchor json.RawMessage `json:"anchor"`
			}
			if err := json.Unmarshal(raw, &vec); err != nil {
				t.Fatalf("decoding vector: %v", err)
			}
			for field, v := range map[string]json.RawMessage{"github": vec.GitHub, "pr": vec.PR, "anchor": vec.Anchor} {
				if len(v) == 0 {
					t.Fatalf("vector is missing its %q member", field)
				}
			}
			if err := validateVector(t, sch, vec.Anchor); err != nil {
				t.Errorf("anchor member rejected: %v", err)
			}
		})
	}
}
