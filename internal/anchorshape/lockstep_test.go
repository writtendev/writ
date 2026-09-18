package anchorshape_test

import (
	"encoding/json"
	"testing"

	"github.com/writtendev/writ/internal/resolve"
	"github.com/writtendev/writ/internal/value"
	"github.com/writtendev/writ/spec"
)

// sideCase names one side (old or new) extracted from a fixture anchor, by
// the fixture's own name plus which key it was found under, and carries the
// side's raw, unmodified JSON bytes.
type sideCase struct {
	label string
	raw   json.RawMessage
}

// extractSides pulls the raw "old" and "new" sub-values out of anchor's raw
// bytes, if it decodes as a JSON object with either key present. It returns
// nothing for a non-object anchor or one with neither key present -- there
// is no side to extract, the same shape ResolveRaw itself treats as having
// no side to orphan.
func extractSides(label string, anchor json.RawMessage) []sideCase {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(anchor, &top); err != nil {
		return nil
	}
	var out []sideCase
	if raw, ok := top["old"]; ok {
		out = append(out, sideCase{label: label + "/old", raw: raw})
	}
	if raw, ok := top["new"]; ok {
		out = append(out, sideCase{label: label + "/new", raw: raw})
	}
	return out
}

// wrapAsNewSide places side's raw bytes as the sole "new" side of a
// synthetic version-1 anchor, so its shape is judged in isolation from
// anything about a sibling side or the anchor's own version field -- a
// different pre-check governs each of those, and this test is about the
// one question anchorshape.SideWellFormed answers for both the producer and
// the reader.
func wrapAsNewSide(side json.RawMessage) []byte {
	out := make([]byte, 0, len(side)+20)
	out = append(out, `{"version":1,"new":`...)
	out = append(out, side...)
	out = append(out, '}')
	return out
}

// producerAccepts reports whether writ's own producer -- value.Validate,
// the public entry point engine/codec.BuildCommit actually calls -- accepts
// side as a well-formed v1 side anchor.
func producerAccepts(t *testing.T, side json.RawMessage) bool {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(wrapAsNewSide(side), &decoded); err != nil {
		t.Fatalf("decoding synthetic wrapper for %s: %v", side, err)
	}
	return value.Validate("anchor", value.Params{}, decoded) == nil
}

// readerConsidersMalformed reports whether engine/resolve's read side --
// ResolveRaw, the total entry point engine/projection's materializeAnchors
// actually calls -- orphans side as "malformed" under its Structural
// Pre-Check.
func readerConsidersMalformed(side json.RawMessage, tree *resolve.Tree) bool {
	res := resolve.ResolveRaw(wrapAsNewSide(side), tree)
	return res.New != nil && res.New.Outcome == resolve.OutcomeOrphaned && res.New.Reason == resolve.ReasonMalformed
}

// knownAsymmetries lists the one side-case in the whole conformance corpus
// where producer and reader deliberately disagree, and why: value.go's
// validateAnchorSide additionally refuses an empty (but present)
// commit/path/blob, which spec/resolution.md's Structural Pre-Check does
// not -- it is decode-and-arithmetic only, per the WRIT-252 ruling, and an
// empty string is still a JSON string. Nothing else in the corpus carries
// an empty commit/path/blob (only empty *line content*, inside
// before/lines/after, which both sides accept -- a blank source line is
// ordinary content, not a shape problem).
var knownAsymmetries = map[string]bool{
	"anchors/invalid/empty-path.json/new": true,
}

// TestProducerReaderAgreeOnSideShape is the direct regression test for
// WRIT-252 round 2's finding that the producer (engine/internal/value) and
// the reader (engine/resolve) disagreed about whether a side anchor is
// well-formed -- in both directions. It runs every side present in every
// resolution vector (spec/testdata/resolution/cases) and every anchor
// conformance vector (spec/testdata/anchors/{valid,invalid}) through both
// producerAccepts and readerConsidersMalformed and asserts they agree:
// producer-accepted iff not malformed on read, except the one documented
// asymmetry above.
//
// It intentionally does not assert a *direction* for schema-only invalid
// vectors (OID format, path syntax, line length): both producer and reader
// deliberately leave those to whatever validates against
// anchor.schema.json (nothing does, today), per the ruling, so the two
// agreeing to accept such a vector's shape is the correct outcome, not a
// bug this test should flag.
//
// This test fails against the round-2 code this round's fixer inherited:
// the producer accepted a non-string context.lines entry and a context
// missing before/after that the reader orphaned malformed, and the reader
// let a null side, a null collar array, and an absent commit/path/blob
// through that the producer refused.
func TestProducerReaderAgreeOnSideShape(t *testing.T) {
	tree := resolve.NewTree(map[string][]byte{"main.go": []byte("package main\n")}, resolve.SHA1)

	var cases []sideCase

	resCases, err := spec.ResolutionVectors()
	if err != nil {
		t.Fatalf("loading resolution vectors: %v", err)
	}
	for _, c := range resCases {
		cases = append(cases, extractSides("resolution/"+c.Name, c.Anchor)...)
	}

	for _, dir := range []string{"valid", "invalid"} {
		entries, err := spec.FS.ReadDir("testdata/anchors/" + dir)
		if err != nil {
			t.Fatalf("reading testdata/anchors/%s: %v", dir, err)
		}
		for _, e := range entries {
			if e.IsDir() || e.Name() == "index.json" {
				continue
			}
			raw, err := spec.FS.ReadFile("testdata/anchors/" + dir + "/" + e.Name())
			if err != nil {
				t.Fatal(err)
			}
			cases = append(cases, extractSides("anchors/"+dir+"/"+e.Name(), raw)...)
		}
	}

	if len(cases) == 0 {
		t.Fatal("no sides extracted from any fixture; extractSides or the fixture directories are broken")
	}

	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			producerOK := producerAccepts(t, c.raw)
			readerMalformed := readerConsidersMalformed(c.raw, tree)

			if producerOK == readerMalformed {
				if knownAsymmetries[c.label] {
					return
				}
				t.Errorf("disagreement for %s: producer accepted=%v, reader malformed=%v, side=%s", c.label, producerOK, readerMalformed, c.raw)
			}
		})
	}
}
