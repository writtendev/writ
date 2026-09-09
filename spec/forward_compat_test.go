package spec_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	writ "github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/codec/canonicaljson"
	"github.com/writtendev/writ/spec"
)

type readerProfile struct {
	Profile  string            `json:"profile"`
	KnownOps []knownOpCapacity `json:"known_ops"`
}

type knownOpCapacity struct {
	ObjectType string  `json:"object_type"`
	OpType     string  `json:"op_type"`
	Versions   []int64 `json:"versions"`
	// Rules are the merge rules this synthetic reader implements for the
	// op type. They are stated inline rather than pointing at a
	// field-rules.json directory because writ ships no vocabulary to point
	// at: `schema` aside, every object type is declared by a schema object
	// in a repository's own log, and this profile describes a reader whose
	// log declared `widget` and `gadget`. The triple the reader implements
	// and the rules that govern that triple are separate facts, and this is
	// where the second one is stated.
	Rules []spec.FieldRule `json:"rules"`
}

type forwardCompatEntry struct {
	Disposition string   `json:"disposition"`
	Rules       []string `json:"rules"`
	Reason      string   `json:"reason"`
	// EnvelopeDisposition is what `FC-1`'s type leg alone decides. It is
	// present only where the two legs disagree — an op whose envelope is
	// interpretable and whose body is not — and it exists because
	// `codec.Profile.Classify` classifies envelopes and structurally cannot
	// decide the body leg: deciding it needs the vocabulary's merge rules and a
	// fold, which the codec layer neither has nor should have.
	// engine/codec/forwardcompat_test.go measures Classify against this field;
	// this file measures the full disposition, and binds the two together in
	// TestForwardCompatConformances.
	EnvelopeDisposition string `json:"envelope_disposition,omitempty"`
}

func loadReaderProfile(t *testing.T) readerProfile {
	t.Helper()
	raw, err := spec.FS.ReadFile("testdata/forward-compat/reader-profile.json")
	if err != nil {
		t.Fatalf("reading reader profile: %v", err)
	}
	var p readerProfile
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decoding reader profile: %v", err)
	}
	if len(p.KnownOps) == 0 {
		t.Fatal("reader profile declares no known ops")
	}
	// Every rule the profile states must be a legal one, so a typo in the
	// synthetic table shows up as a broken profile rather than as a
	// silently unmatched rule deriving the wrong disposition.
	for _, k := range p.KnownOps {
		for _, r := range k.Rules {
			if err := spec.ValidateFieldRule(r); err != nil {
				t.Fatalf("reader profile rule for %s/%s is invalid: %v", k.ObjectType, k.OpType, err)
			}
		}
	}
	return p
}

// implementsTriple reports whether the reader profile implements an op's
// (object_type, op_type, op_version). This is `FC-1`'s type leg, and the only
// leg `codec.Profile.Classify` decides.
func implementsTriple(profile readerProfile, objectType, opType string, opVersion int64) bool {
	for _, k := range profile.KnownOps {
		if k.ObjectType != objectType || k.OpType != opType {
			continue
		}
		for _, v := range k.Versions {
			if v == opVersion {
				return true
			}
		}
		return false
	}
	return false
}

// governingRules returns the merge rules that govern one op: the rules the
// profile states for its (object_type, op_type), narrowed to its op_version.
//
// Narrowing here is a filter over the stated table, not a second opinion
// about the op. Which rules govern an op is what a rule table means; whether
// the op's body survives them is decided in deriveDisposition by running the
// reference fold and reading what it quarantined.
func governingRules(profile readerProfile, objectType, opType string, opVersion int64) []spec.FieldRule {
	for _, k := range profile.KnownOps {
		if k.ObjectType != objectType || k.OpType != opType {
			continue
		}
		var out []spec.FieldRule
		for _, r := range k.Rules {
			if r.OpType == opType && r.OpVersion == opVersion {
				out = append(out, r)
			}
		}
		return out
	}
	return nil
}

func loadForwardCompatIndex(t *testing.T) map[string]forwardCompatEntry {
	t.Helper()
	raw, err := spec.FS.ReadFile("testdata/forward-compat/index.json")
	if err != nil {
		t.Fatalf("reading forward-compat index.json: %v", err)
	}
	var index map[string]forwardCompatEntry
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatalf("decoding forward-compat index.json: %v", err)
	}
	if len(index) == 0 {
		t.Fatal("forward-compat index is empty")
	}
	return index
}

// deriveDisposition computes whether an op payload is interpretable or opaque
// under a given reader profile.
//
// `FC-1` puts two populations in the opaque category, so this has two legs and
// only the first is derivable from the envelope:
//
//  1. **The type.** Whether the reader implements this
//     (object_type, op_type, op_version) triple. The profile is the only source
//     for that — it is a *synthetic* reader, deliberately not the shipped one,
//     which is the whole point of testing a reader against ops it does not know.
//  2. **The body.** Whether the fold can consume it (`spec/fold.md` §7.1).
//     This is not restated here. The op is run through the reference fold
//     against the rules that govern it, and the answer is whether the fold put
//     it in `UnknownOps` — the same quarantine channel §7.1 rule 2 specifies,
//     read rather than predicted.
//
// Leg 2 used to be missing entirely: this harness classified an op from the
// envelope alone and never opened `body`. When `FC-1` was widened to cover
// uninterpretable bodies, that made the harness stale in the way that matters
// most — it could not express a fixture for the newly-covered half, so `FC-1`
// went on claiming coverage it did not have. `uninterpretable-body.json` is
// that fixture, and it fails against a re-derived disposition.
func deriveDisposition(profile readerProfile, name string, rawOp []byte) (string, error) {
	var env struct {
		ObjectID   string         `json:"object_id"`
		ObjectType string         `json:"object_type"`
		OpType     string         `json:"op_type"`
		OpVersion  int64          `json:"op_version"`
		Body       map[string]any `json:"body"`
	}
	if err := json.Unmarshal(rawOp, &env); err != nil {
		return "", fmt.Errorf("decoding envelope: %w", err)
	}

	// Leg 1: the type.
	if !implementsTriple(profile, env.ObjectType, env.OpType, env.OpVersion) {
		return "opaque", nil
	}

	// Leg 2: the body. §7.1 reaches only fields with a declared merge rule, so
	// a triple the profile states no rule for has nothing for it to reject —
	// which is the case for the profile's `gadget/post`.
	rules := governingRules(profile, env.ObjectType, env.OpType, env.OpVersion)
	if len(rules) == 0 {
		return "interpretable", nil
	}
	folded, err := spec.Fold([]spec.MergeOp{{
		ID:         name,
		ObjectID:   env.ObjectID,
		ObjectType: env.ObjectType,
		OpType:     env.OpType,
		OpVersion:  env.OpVersion,
		Body:       env.Body,
	}}, rules)
	if err != nil {
		return "", fmt.Errorf("folding instance: %w", err)
	}
	// Every rule passed governs this op, so the only way into the quarantine
	// channel here is §7.1.
	if len(folded.UnknownOps) > 0 {
		return "opaque", nil
	}
	return "interpretable", nil
}

// TestForwardCompatConformances tests every instance in the forward-compat
// testdata corpus:
// 1. Every instance validates against op-envelope.schema.json (unknown is not invalid).
// 2. Every instance is byte-canonical and survives canonicaljson round-trip unmodified.
// 3. Derived disposition (from reader profile + payload) matches index.json.
// 4. Directory and index agree 1-to-1.
func TestForwardCompatConformances(t *testing.T) {
	sch, _ := envelopeSchema(t)
	profile := loadReaderProfile(t)
	index := loadForwardCompatIndex(t)

	entries, err := spec.FS.ReadDir("testdata/forward-compat/ops")
	if err != nil {
		t.Fatalf("reading testdata/forward-compat/ops directory: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no forward-compat op instances found")
	}

	filesInDir := make(map[string]bool)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			filesInDir[e.Name()] = true
		}
	}

	// 1-to-1 agreement between directory and index
	for name := range index {
		if !filesInDir[name] {
			t.Errorf("index.json lists %s but file does not exist in ops/", name)
		}
	}
	for name := range filesInDir {
		if _, ok := index[name]; !ok {
			t.Errorf("ops/%s exists but is not listed in index.json", name)
		}
	}

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			entry, ok := index[name]
			if !ok {
				t.Fatalf("instance %s missing from index.json", name)
			}
			if entry.Reason == "" {
				t.Error("index entry has empty reason")
			}
			if len(entry.Rules) == 0 {
				t.Error("index entry cites no rule IDs")
			}
			if entry.Disposition != "interpretable" && entry.Disposition != "opaque" {
				t.Errorf("invalid disposition %q (want interpretable or opaque)", entry.Disposition)
			}
			if entry.EnvelopeDisposition != "" {
				if entry.EnvelopeDisposition != "interpretable" && entry.EnvelopeDisposition != "opaque" {
					t.Errorf("invalid envelope_disposition %q (want interpretable or opaque)", entry.EnvelopeDisposition)
				}
				if entry.EnvelopeDisposition == entry.Disposition {
					t.Errorf("envelope_disposition duplicates disposition (%q); state it only where the two "+
						"legs of FC-1 disagree, or engine/codec's harness is asserting nothing extra",
						entry.Disposition)
				}
			}

			path := "testdata/forward-compat/ops/" + name
			raw, err := spec.FS.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}

			// Schema validation (unknown is NOT invalid)
			inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Fatalf("parsing JSON instance: %v", err)
			}
			if err := sch.Validate(inst); err != nil {
				t.Errorf("schema validation failed (unknown op must be valid envelope): %v", err)
			}

			// Codec leg: canonical JSON round-trip
			canon, err := canonicaljson.Marshal(raw)
			if err != nil {
				t.Fatalf("canonicalizing instance: %v", err)
			}
			if !bytes.Equal(canon, raw) {
				t.Errorf("instance bytes are not canonical:\n  file: %q\n canon: %q", raw, canon)
			}

			// Disposition derivation check
			derived, err := deriveDisposition(profile, name, raw)
			if err != nil {
				t.Fatalf("deriving disposition: %v", err)
			}
			if derived != entry.Disposition {
				t.Errorf("disposition mismatch for %s: derived %q, index declares %q (reason: %s)", name, derived, entry.Disposition, entry.Reason)
			}

			// And where the index declares an envelope_disposition, FC-1's
			// type leg alone must produce it. Without this the field would be
			// an unchecked assertion in a data file that only the codec
			// harness reads, and the two harnesses could drift apart on which
			// leg they think they are measuring.
			if entry.EnvelopeDisposition != "" {
				var env struct {
					ObjectType string `json:"object_type"`
					OpType     string `json:"op_type"`
					OpVersion  int64  `json:"op_version"`
				}
				if err := json.Unmarshal(raw, &env); err != nil {
					t.Fatalf("decoding envelope: %v", err)
				}
				wantEnvelope := "opaque"
				if implementsTriple(profile, env.ObjectType, env.OpType, env.OpVersion) {
					wantEnvelope = "interpretable"
				}
				if entry.EnvelopeDisposition != wantEnvelope {
					t.Errorf("envelope_disposition is %q but FC-1's type leg derives %q for %s",
						entry.EnvelopeDisposition, wantEnvelope, name)
				}
			}
		})
	}
}

// TestForwardCompatRuleCoverage parses spec/forward-compatibility.md to find all
// numbered rules (FC-1, FC-2, ...) and ensures every single rule ID is cited in
// index.json.
func TestForwardCompatRuleCoverage(t *testing.T) {
	index := loadForwardCompatIndex(t)

	// Read spec/forward-compatibility.md
	docPath := "forward-compatibility.md"
	rawDoc, err := os.ReadFile(docPath)
	if err != nil {
		// Try from repo root if running from parent directory
		docPath = filepath.Join("spec", "forward-compatibility.md")
		rawDoc, err = os.ReadFile(docPath)
		if err != nil {
			t.Fatalf("reading spec/forward-compatibility.md: %v", err)
		}
	}

	re := regexp.MustCompile(`\bFC-\d+\b`)
	matches := re.FindAllString(string(rawDoc), -1)
	if len(matches) == 0 {
		t.Fatal("no FC-* rule IDs found in spec/forward-compatibility.md")
	}

	definedRules := make(map[string]bool)
	for _, m := range matches {
		definedRules[m] = true
	}

	citedRules := make(map[string]bool)
	for _, entry := range index {
		for _, r := range entry.Rules {
			citedRules[r] = true
			if !definedRules[r] {
				t.Errorf("index.json cites rule %s which is not defined in spec/forward-compatibility.md", r)
			}
		}
	}

	var uncited []string
	for r := range definedRules {
		if !citedRules[r] {
			uncited = append(uncited, r)
		}
	}
	if len(uncited) > 0 {
		sort.Strings(uncited)
		t.Errorf("the following normative rule IDs are defined in spec/forward-compatibility.md but never cited in testdata/forward-compat/index.json: %v", uncited)
	}
}

// TestNegativeDispositionDerivation verifies that the derivation check is
// active and fails if an expected disposition is inverted — once per leg of
// FC-1, because a harness that reads only the envelope answers the first
// correctly while being blind to the second.
func TestNegativeDispositionDerivation(t *testing.T) {
	profile := loadReaderProfile(t)

	cases := []struct {
		name string
		file string
		leg  string
	}{
		{
			name: "unimplemented op_version",
			file: "future-op-version.json",
			leg:  "type",
		},
		{
			// widget/create v1 is in the reader profile and its envelope is
			// beyond reproach. Only `body` makes it opaque: `title` carries a
			// declared `lww` rule and holds null, which §7.1 rejects.
			name: "body a strategy cannot consume",
			file: "uninterpretable-body.json",
			leg:  "body",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/forward-compat/ops/" + tc.file)
			if err != nil {
				t.Fatal(err)
			}
			derived, err := deriveDisposition(profile, tc.file, raw)
			if err != nil {
				t.Fatal(err)
			}
			if derived != "opaque" {
				t.Errorf("%s derives as %q, want opaque; the %s leg of FC-1 is not being decided",
					tc.file, derived, tc.leg)
			}
		})
	}
}

// TestForwardCompatFC5 verifies normative rule FC-5: folded state MUST surface
// each uninterpretable op as an opaque record containing op_id (spelled commit),
// object_type, op_type, and op_version.
func TestForwardCompatFC5(t *testing.T) {
	profile := loadReaderProfile(t)

	cases := []struct {
		name string
		file string
	}{
		{name: "unknown object type", file: "unknown-object-type.json"},
		{name: "unknown op type", file: "unknown-op-type.json"},
		{name: "future op version", file: "future-op-version.json"},
		{name: "uninterpretable body", file: "uninterpretable-body.json"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/forward-compat/ops/" + tc.file)
			if err != nil {
				t.Fatalf("reading %s: %v", tc.file, err)
			}
			var env struct {
				ObjectID   string         `json:"object_id"`
				ObjectType string         `json:"object_type"`
				OpType     string         `json:"op_type"`
				OpVersion  int64          `json:"op_version"`
				Body       map[string]any `json:"body"`
			}
			if err := json.Unmarshal(raw, &env); err != nil {
				t.Fatalf("decoding envelope: %v", err)
			}

			rules := governingRules(profile, env.ObjectType, env.OpType, env.OpVersion)

			// 1. Reference fold assertion
			folded, err := spec.Fold([]spec.MergeOp{{
				ID:         tc.file,
				ObjectID:   env.ObjectID,
				ObjectType: env.ObjectType,
				OpType:     env.OpType,
				OpVersion:  env.OpVersion,
				Body:       env.Body,
			}}, rules)
			if err != nil {
				t.Fatalf("spec.Fold failed: %v", err)
			}
			if len(folded.UnknownOps) != 1 {
				t.Fatalf("spec.Fold expected 1 UnknownOp, got %d", len(folded.UnknownOps))
			}
			refU := folded.UnknownOps[0]
			if refU.Commit != tc.file {
				t.Errorf("ref.Commit mismatch: got %q, want %q", refU.Commit, tc.file)
			}
			if refU.ObjectType != env.ObjectType {
				t.Errorf("ref.ObjectType mismatch: got %q, want %q", refU.ObjectType, env.ObjectType)
			}
			if refU.OpType != env.OpType {
				t.Errorf("ref.OpType mismatch: got %q, want %q", refU.OpType, env.OpType)
			}
			if refU.OpVersion != env.OpVersion {
				t.Errorf("ref.OpVersion mismatch: got %d, want %d", refU.OpVersion, env.OpVersion)
			}

			// 2. Engine fold assertion
			var writRules []writ.Rule
			for _, r := range rules {
				writRules = append(writRules, writ.Rule{
					OpType:    r.OpType,
					OpVersion: r.OpVersion,
					Field:     r.Field,
					Strategy:  r.Strategy,
					Key:       r.Key,
					Lattice:   r.Lattice,
					ValueType: r.ValueType,
					Enum:      r.Enum,
					MaxLength: r.MaxLength,
					KeyTypes:  r.KeyTypes,
				})
			}
			bodyJSON, err := json.Marshal(env.Body)
			if err != nil {
				t.Fatalf("marshaling body: %v", err)
			}
			engineRes, err := writ.Fold([]codec.Op{{
				ID: tc.file,
				Envelope: codec.Envelope{
					ObjectID:   env.ObjectID,
					ObjectType: env.ObjectType,
					OpType:     env.OpType,
					OpVersion:  env.OpVersion,
					Body:       bodyJSON,
				},
			}}, writRules)
			if err != nil {
				t.Fatalf("writ.Fold failed: %v", err)
			}
			if len(engineRes.UnknownOps) != 1 {
				t.Fatalf("writ.Fold expected 1 UnknownOp, got %d", len(engineRes.UnknownOps))
			}
			engU := engineRes.UnknownOps[0]
			if engU.Commit != tc.file {
				t.Errorf("engine.Commit mismatch: got %q, want %q", engU.Commit, tc.file)
			}
			if engU.ObjectType != env.ObjectType {
				t.Errorf("engine.ObjectType mismatch: got %q, want %q", engU.ObjectType, env.ObjectType)
			}
			if engU.OpType != env.OpType {
				t.Errorf("engine.OpType mismatch: got %q, want %q", engU.OpType, env.OpType)
			}
			if engU.OpVersion != env.OpVersion {
				t.Errorf("engine.OpVersion mismatch: got %d, want %d", engU.OpVersion, env.OpVersion)
			}
		})
	}
}
