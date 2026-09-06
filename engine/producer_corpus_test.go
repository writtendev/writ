package writ_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	writ "github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/spec"
)

// TestProducerCorpus is the engine-level driver for
// spec/testdata/producer/: WRIT-188's regression net that producer
// validation and reader tolerance move in lockstep, not just individually.
// It lives here rather than in package spec because package spec cannot
// import engine/codec (spec sits below engine in the dependency graph, and
// a producer-verdict driver needs codec.BuildCommit); the "index.json
// covers every case file" completeness check lives in
// spec/producer_test.go, the pattern spec/testdata/*/invalid/index.json
// already uses.
//
// Each case declares zero or more schema objects (folded exactly as the
// log would fold them, via writ.SchemaFromEnvelopes) and one op envelope.
// index.json names, per case, the producer verdict codec.BuildCommit must
// return and the reader disposition the exact same op must have when
// folded with the rules those same schemas resolve to
// (writ.RulesFromSchemas) — proving every rejected case is nonetheless
// tolerated by a reader, and every accepted case's schema resolution is
// the one the reader itself would use.
func TestProducerCorpus(t *testing.T) {
	rawIndex, err := spec.FS.ReadFile("testdata/producer/index.json")
	if err != nil {
		t.Fatalf("reading producer index: %v", err)
	}
	var index map[string]struct {
		ProducerVerdict   string `json:"producer_verdict"`
		ReasonCode        string `json:"reason_code,omitempty"`
		ReaderDisposition string `json:"reader_disposition"`
		Reason            string `json:"reason"`
	}
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatalf("decoding producer index: %v", err)
	}
	if len(index) == 0 {
		t.Fatal("producer index lists no cases")
	}

	type schemaOpCase struct {
		OpType string         `json:"op_type"`
		Body   map[string]any `json:"body"`
	}
	type schemaObjCase struct {
		ObjectID string         `json:"object_id"`
		Ops      []schemaOpCase `json:"ops"`
	}
	type envelopeCase struct {
		ObjectID   string         `json:"object_id"`
		ObjectType string         `json:"object_type"`
		OpType     string         `json:"op_type"`
		OpVersion  int64          `json:"op_version"`
		Body       map[string]any `json:"body"`
	}
	type producerCase struct {
		Schemas  []schemaObjCase `json:"schemas"`
		Envelope envelopeCase    `json:"envelope"`
	}

	author := codec.Identity{Name: "Producer Corpus", Email: "corpus@example.com", When: time.Unix(0, 0).UTC()}

	for file, entry := range index {
		t.Run(file, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/producer/cases/" + file)
			if err != nil {
				t.Fatalf("reading case %s: %v", file, err)
			}
			var c producerCase
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatalf("decoding case %s: %v", file, err)
			}

			var schemas []writ.Schema
			for _, so := range c.Schemas {
				envs := make([]codec.Envelope, len(so.Ops))
				for i, op := range so.Ops {
					bodyRaw, err := json.Marshal(op.Body)
					if err != nil {
						t.Fatalf("marshal schema op body: %v", err)
					}
					envs[i] = codec.Envelope{
						ObjectID:   so.ObjectID,
						ObjectType: "schema",
						OpType:     op.OpType,
						OpVersion:  1,
						Body:       bodyRaw,
					}
				}
				sch, err := writ.SchemaFromEnvelopes(envs)
				if err != nil {
					t.Fatalf("SchemaFromEnvelopes(%s): %v", so.ObjectID, err)
				}
				schemas = append(schemas, sch)
			}

			vocabularies, _ := writ.VocabulariesFromSchemas(schemas)
			rules, _ := writ.RulesFromSchemas(schemas)

			envBody, err := json.Marshal(c.Envelope.Body)
			if err != nil {
				t.Fatalf("marshal envelope body: %v", err)
			}
			env := codec.Envelope{
				ObjectID:   c.Envelope.ObjectID,
				ObjectType: c.Envelope.ObjectType,
				OpType:     c.Envelope.OpType,
				OpVersion:  c.Envelope.OpVersion,
				Body:       envBody,
			}

			// --- Producer direction ---
			_, buildErr := codec.BuildCommit(env, author, nil, vocabularies)
			gotVerdict := "accept"
			if buildErr != nil {
				gotVerdict = "reject"
			}
			if gotVerdict != entry.ProducerVerdict {
				t.Fatalf("producer verdict = %s, want %s (BuildCommit error: %v)", gotVerdict, entry.ProducerVerdict, buildErr)
			}
			if entry.ReasonCode != "" {
				var rej *codec.RejectError
				if !errors.As(buildErr, &rej) {
					t.Fatalf("expected a *codec.RejectError carrying reason %q, got: %v", entry.ReasonCode, buildErr)
				}
				if string(rej.Reason) != entry.ReasonCode {
					t.Errorf("reason code = %q, want %q", rej.Reason, entry.ReasonCode)
				}
			}

			// --- Reader direction: the same op, arrived from elsewhere,
			// folded with the rules the same schemas resolve to. ---
			dataOp := codec.Op{
				ID:       "data-op-1",
				Envelope: env,
				Author:   codec.Identity{When: time.Unix(1, 0).UTC()},
			}
			objState, err := writ.Fold([]codec.Op{dataOp}, rules[c.Envelope.ObjectType])
			if err != nil {
				t.Fatalf("reader Fold errored (a reader must never error on unrecognized input): %v", err)
			}
			gotDisposition := "interpretable"
			if len(objState.UnknownOps) > 0 {
				gotDisposition = "unknown-op"
			}
			if gotDisposition != entry.ReaderDisposition {
				t.Errorf("reader disposition = %s, want %s (UnknownOps: %+v)", gotDisposition, entry.ReaderDisposition, objState.UnknownOps)
			}
		})
	}
}
