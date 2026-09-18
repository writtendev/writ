package codec_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/spec"
)

func TestValidEnvelopesDecode(t *testing.T) {
	entries, err := spec.FS.ReadDir("testdata/envelopes/valid")
	if err != nil {
		t.Fatalf("reading valid envelopes dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("no valid envelope instances found")
	}

	for _, e := range entries {
		t.Run(e.Name(), func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/envelopes/valid/" + e.Name())
			if err != nil {
				t.Fatalf("reading file: %v", err)
			}

			env, err := codec.DecodePayload(raw)
			if err != nil {
				t.Fatalf("DecodePayload failed for valid instance: %v", err)
			}

			if env.ObjectID == "" || env.ObjectType == "" || env.OpType == "" || env.OpVersion < 1 {
				t.Errorf("decoded envelope missing expected fields: %+v", env)
			}

			if !bytes.Equal(env.Raw, raw) {
				t.Errorf("env.Raw mismatch:\n got: %q\nwant: %q", env.Raw, raw)
			}

			encoded, err := codec.EncodePayload(env)
			if err != nil {
				t.Fatalf("EncodePayload failed on decoded envelope: %v", err)
			}

			if !bytes.Equal(encoded, raw) {
				t.Errorf("round-trip mismatch:\n encoded: %q\noriginal: %q", encoded, raw)
			}
		})
	}
}

type invalidIndexEntry struct {
	File     string `json:"file"`
	Rejects  string `json:"rejects"`
	Category string `json:"category,omitempty"`
	Reason   string `json:"reason"`
}

func TestInvalidEnvelopesReject(t *testing.T) {
	rawIndex, err := spec.FS.ReadFile("testdata/envelopes/invalid/index.json")
	if err != nil {
		t.Fatalf("reading invalid envelopes index: %v", err)
	}

	var index []invalidIndexEntry
	if err := json.Unmarshal(rawIndex, &index); err != nil {
		t.Fatalf("unmarshaling invalid envelopes index: %v", err)
	}

	for _, entry := range index {
		t.Run(entry.File, func(t *testing.T) {
			raw, err := spec.FS.ReadFile("testdata/envelopes/invalid/" + entry.File)
			if err != nil {
				t.Fatalf("reading invalid file %s: %v", entry.File, err)
			}

			_, decErr := codec.DecodePayload(raw)
			if decErr == nil {
				t.Fatalf("DecodePayload accepted invalid envelope %s; expected rejection: %s", entry.File, entry.Reason)
			}

			var rej *codec.RejectError
			if !errors.As(decErr, &rej) {
				t.Fatalf("DecodePayload returned non-RejectError: %T (%v)", decErr, decErr)
			}

			var wantReason codec.RejectReason
			switch entry.Rejects {
			case "schema":
				wantReason = codec.RejectSchemaViolation
			case "canonicalization":
				switch entry.Category {
				case "duplicate-key":
					wantReason = codec.RejectDuplicateKey
				case "lone-surrogate":
					wantReason = codec.RejectLoneSurrogate
				case "not-one-value", "not-canonical":
					wantReason = codec.RejectNonCanonicalPayload
				default:
					t.Fatalf("unrecognized canonicalization category: %s", entry.Category)
				}
			default:
				t.Fatalf("unrecognized rejects type: %s", entry.Rejects)
			}

			if rej.Reason != wantReason {
				t.Errorf("wrong reject reason for %s: got %q, want %q (reason: %s)", entry.File, rej.Reason, wantReason, entry.Reason)
			}
		})
	}
}
