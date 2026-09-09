package codec_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/spec"
)

func FuzzPayloadRoundTrip(f *testing.F) {
	// Seed with valid envelopes
	if entries, err := spec.FS.ReadDir("testdata/envelopes/valid"); err == nil {
		for _, e := range entries {
			if raw, err := spec.FS.ReadFile("testdata/envelopes/valid/" + e.Name()); err == nil {
				f.Add(raw)
			}
		}
	}

	// Seed with forward-compat ops
	if entries, err := spec.FS.ReadDir("testdata/forward-compat/ops"); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				if raw, err := spec.FS.ReadFile("testdata/forward-compat/ops/" + e.Name()); err == nil {
					f.Add(raw)
				}
			}
		}
	}

	// Seed with valid schema ops, the one vocabulary writ ships
	if entries, err := spec.FS.ReadDir("testdata/schema-ops/valid"); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".json") {
				if raw, err := spec.FS.ReadFile("testdata/schema-ops/valid/" + e.Name()); err == nil {
					f.Add(raw)
				}
			}
		}
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		env, err := codec.DecodePayload(data)
		if err != nil {
			// Expected for non-canonical or invalid inputs
			return
		}

		// 1. Re-encoding decoded payload must reproduce the exact bytes (FC-11)
		encoded, err := codec.EncodePayload(env)
		if err != nil {
			t.Fatalf("EncodePayload failed on decoded envelope: %v", err)
		}

		if !bytes.Equal(encoded, data) {
			t.Fatalf("round-trip mismatch:\n  got:  %q\n  want: %q", encoded, data)
		}

		// 2. Fixed point property
		env2, err := codec.DecodePayload(encoded)
		if err != nil {
			t.Fatalf("DecodePayload failed on encoded payload: %v", err)
		}

		encoded2, err := codec.EncodePayload(env2)
		if err != nil {
			t.Fatalf("EncodePayload failed on second iteration: %v", err)
		}

		if !bytes.Equal(encoded2, encoded) {
			t.Fatalf("fixed point violation:\n  first:  %q\n  second: %q", encoded, encoded2)
		}
	})
}
