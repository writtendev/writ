package codec

import "fmt"

// RejectReason represents the machine-readable reason an op commit or payload is rejected.
// The reader rejection-reason set is closed and mirrors spec/op-envelope.md §Reader validation.
type RejectReason string

const (
	RejectTreeShape           RejectReason = "tree-shape"
	RejectPayloadTooLarge     RejectReason = "payload-too-large"
	RejectNonCanonicalPayload RejectReason = "non-canonical-payload"
	RejectSchemaViolation     RejectReason = "schema-violation"
	RejectCommitterMismatch   RejectReason = "committer-mismatch"
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
const MaxCommitBytes = 1 << 20

// MaxTreeBytes is the maximum size, in bytes, of an op commit's root tree
// object a conforming reader accepts, inclusive (spec/op-envelope.md §Reader
// validation rule 1). A legitimate op tree is 35 bytes; the bound is
// deliberately larger so tree-shape stays reachable.
const MaxTreeBytes = 1 << 12

// MaxCommitParents is the maximum number of causal parents a single op commit
// may carry (spec/op-envelope.md §Producer validation). When an object's
// deduplicated causal frontier exceeds this limit, a conforming producer chunks
// the parents across sequential merge link ops.
const MaxCommitParents = 20000

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
