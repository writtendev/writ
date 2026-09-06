package schemasrc_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/schemasrc"
	"github.com/writtendev/writ/engine/state"
)

var corpusOpVersionPattern = regexp.MustCompile(`^[1-9][0-9]*$`)

// assertCanonicalOpVersion asserts every op_version body field this
// envelope carries (define-op, define-field, deprecate-field) is the
// canonical decimal string spec/schema-ops.md §3.1 requires
// (^[1-9][0-9]*$) — the WRIT-186 round-1 bug's regression net, applied
// here to every op the corpus actually produces rather than only to a
// synthetic sweep (see TestEmittedOpVersionIsCanonicalDecimal).
func assertCanonicalOpVersion(t *testing.T, env codec.Envelope) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(env.Body, &body); err != nil {
		t.Fatalf("unmarshaling %s body: %v", env.OpType, err)
	}
	v, ok := body["op_version"]
	if !ok {
		return
	}
	s, ok := v.(string)
	if !ok {
		t.Errorf("%s body's op_version is %T, not a string", env.OpType, v)
		return
	}
	if !corpusOpVersionPattern.MatchString(s) {
		t.Errorf("%s body's op_version %q does not match %s", env.OpType, s, corpusOpVersionPattern.String())
	}
}

func validCorpusFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("testdata/valid/*.schema")
	if err != nil {
		t.Fatalf("globbing testdata/valid: %v", err)
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		t.Fatal("no valid corpus files found under testdata/valid")
	}
	return matches
}

// TestValidCorpus is the corpus described in WRIT-187's plan: each
// valid/*.schema is paired with a golden op sequence and a golden
// canonical rendering, asserted byte-for-byte, and every one of the
// round-trip contract's promises is checked per case.
func TestValidCorpus(t *testing.T) {
	sch := schemaOpsSchema(t)

	for _, path := range validCorpusFiles(t) {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			src := readGolden(t, path)

			f1, err := schemasrc.Parse(name, src)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}

			envs1, err := schemasrc.Compile(f1, testObjectID)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}

			// Promise 1: deterministic, canonically ordered, canonical
			// op_version, and every define-field passes ValidateFieldRule
			// (Compile itself enforces the last of these; re-running
			// Compile on the same AST must reproduce the same bytes).
			envs1b, err := schemasrc.Compile(f1, testObjectID)
			if err != nil {
				t.Fatalf("Compile (second run): %v", err)
			}
			if !reflect.DeepEqual(envs1, envs1b) {
				t.Errorf("Compile is not deterministic across repeated calls on the same AST")
			}

			for _, env := range envs1 {
				validateAgainstWireSchema(t, sch, env)
				assertCanonicalOpVersion(t, env)
			}

			gotOps := marshalEnvelopes(t, envs1)
			compareOrUpdateGolden(t, path+".ops.json", gotOps)

			// Fold the compiled sequence and render it back.
			ops := envelopesToOps(envs1)
			folded, err := state.FoldSchema(ops)
			if err != nil {
				t.Fatalf("FoldSchema: %v", err)
			}
			if len(folded.UnknownOps) != 0 {
				t.Fatalf("FoldSchema reported unknown ops: %+v", folded.UnknownOps)
			}

			rendered, err := schemasrc.Render(folded)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			compareOrUpdateGolden(t, path+".rendered", rendered)

			// Promise 2: C(P(R(F(C(P(src)))))) == C(P(src)).
			f2, err := schemasrc.Parse(name+".render", rendered)
			if err != nil {
				t.Fatalf("re-Parse rendered source: %v\n--- rendered ---\n%s", err, rendered)
			}
			envs2, err := schemasrc.Compile(f2, testObjectID)
			if err != nil {
				t.Fatalf("re-Compile rendered source: %v", err)
			}
			if !reflect.DeepEqual(envs1, envs2) {
				t.Errorf("semantic round-trip failed: Compile(Parse(Render(Fold(Compile(Parse(src)))))) != Compile(Parse(src))\nfirst:  %s\nsecond: %s",
					marshalEnvelopes(t, envs1), marshalEnvelopes(t, envs2))
			}

			// Promise 3: R(F(C(P(R(s))))) == R(s) — render is idempotent.
			ops2 := envelopesToOps(envs2)
			folded2, err := state.FoldSchema(ops2)
			if err != nil {
				t.Fatalf("re-FoldSchema: %v", err)
			}
			rendered2, err := schemasrc.Render(folded2)
			if err != nil {
				t.Fatalf("re-Render: %v", err)
			}
			if string(rendered) != string(rendered2) {
				t.Errorf("Render is not idempotent:\n--- first ---\n%s\n--- second ---\n%s", rendered, rendered2)
			}

			// Promise 4: Format(Format(src)) == Format(src), comments preserved.
			formatted1, err := schemasrc.Format(name, src)
			if err != nil {
				t.Fatalf("Format: %v", err)
			}
			formatted2, err := schemasrc.Format(name, formatted1)
			if err != nil {
				t.Fatalf("Format(Format(src)): %v", err)
			}
			if string(formatted1) != string(formatted2) {
				t.Errorf("Format is not idempotent:\n--- first ---\n%s\n--- second ---\n%s", formatted1, formatted2)
			}
			for _, c := range collectComments(t, src) {
				if !strings.Contains(string(formatted1), c) {
					t.Errorf("Format dropped comment %q", c)
				}
			}
		})
	}
}

func marshalEnvelopes(t *testing.T, envs []codec.Envelope) []byte {
	t.Helper()
	b, err := json.MarshalIndent(envs, "", "  ")
	if err != nil {
		t.Fatalf("marshaling envelopes: %v", err)
	}
	return append(b, '\n')
}

// collectComments extracts every comment body (text after '#', trimmed)
// from src, using the package's own lexer indirectly via Parse +
// re-Format is circular, so this scans the raw source directly: a line
// containing '#' contributes everything after the first '#' on that
// line, trimmed. This is intentionally cruder than the real lexer (it
// does not understand string literals containing '#'), which is fine for
// this corpus: no fixture below quotes a literal '#'.
func collectComments(t *testing.T, src []byte) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(string(src), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			c := strings.TrimSpace(line[i+1:])
			if c != "" {
				out = append(out, c)
			}
		}
	}
	return out
}
