package codec

import "fmt"

// RejectReason represents the machine-readable reason an op commit or payload is rejected.
type RejectReason string

const (
	RejectMissingOpJSON       RejectReason = "missing-op-json"
	RejectExtraTreeEntry      RejectReason = "extra-tree-entry"
	RejectInvalidOpJSONMode   RejectReason = "invalid-op-json-mode"
	RejectCommitterMismatch   RejectReason = "committer-mismatch"
	RejectNonCanonicalPayload RejectReason = "non-canonical-payload"
	RejectDuplicateKey        RejectReason = "duplicate-key"
	RejectLoneSurrogate       RejectReason = "lone-surrogate"
	RejectSchemaViolation     RejectReason = "schema-violation"
	RejectPayloadTooLarge     RejectReason = "payload-too-large"
	RejectCommitTooLarge      RejectReason = "commit-too-large"
	RejectTreeTooLarge        RejectReason = "tree-too-large"
)

// MaxPayloadBytes is the maximum length, in bytes, of an op.json blob a
// conforming reader accepts and a conforming producer writes
// (spec/op-envelope.md §Reader validation rule 1, §Producer validation).
const MaxPayloadBytes = 1 << 20

// MaxCommitBytes is the maximum size, in bytes, of an op commit object a
// conforming reader accepts and a conforming producer writes, inclusive
// (spec/op-envelope.md §Reader validation rule 1, §Producer validation).
const MaxCommitBytes = 1 << 16

// MaxTreeBytes is the maximum size, in bytes, of an op commit's root tree
// object a conforming reader accepts, inclusive (spec/op-envelope.md §Reader
// validation rule 1). A legitimate op tree is 35 bytes; the bound is
// deliberately larger so the tree-shape reasons stay reachable.
const MaxTreeBytes = 1 << 12

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
