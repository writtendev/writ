package codec

import "fmt"

// RejectReason represents the machine-readable reason an op commit or payload is rejected.
type RejectReason string

const (
	RejectMissingOpJSON       RejectReason = "missing-op-json"
	RejectExtraTreeEntry      RejectReason = "extra-tree-entry"
	RejectOpJSONSubdirectory  RejectReason = "op-json-subdirectory"
	RejectInvalidOpJSONMode   RejectReason = "invalid-op-json-mode"
	RejectCommitterMismatch   RejectReason = "committer-mismatch"
	RejectNonCanonicalPayload RejectReason = "non-canonical-payload"
	RejectDuplicateKey        RejectReason = "duplicate-key"
	RejectLoneSurrogate       RejectReason = "lone-surrogate"
	RejectSchemaViolation     RejectReason = "schema-violation"
	RejectPayloadTooLarge     RejectReason = "payload-too-large"
)

// MaxPayloadBytes is the maximum length, in bytes, of an op.json blob a
// conforming reader accepts and a conforming producer writes
// (spec/op-envelope.md §Reader validation rule 1, §Producer validation).
const MaxPayloadBytes = 1 << 20

// RejectError is returned when an op commit or payload fails validation —
// reader validation of an op that arrived, or producer validation of one about
// to be signed (spec/op-envelope.md).
type RejectError struct {
	Reason RejectReason
	Err    error
}

func (e *RejectError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("codec: reject %s: %v", e.Reason, e.Err)
	}
	return fmt.Sprintf("codec: reject %s", e.Reason)
}

func (e *RejectError) Unwrap() error {
	return e.Err
}
