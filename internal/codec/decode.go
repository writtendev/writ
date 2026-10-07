package codec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/writtendev/writ/internal/codec/canonicaljson"
)

// DecodePayload decodes canonical JSON bytes into an Envelope, applying the
// byte-equality rule and envelope schema validation. Original bytes are retained in Raw.
// Unknown top-level fields are preserved in Unknown; body is preserved as raw JSON.
func DecodePayload(raw []byte) (Envelope, error) {
	// Rule 2: Byte-equality rule (canonicalization check)
	canon, err := canonicaljson.Marshal(raw)
	if err != nil {
		return Envelope{}, &RejectError{Reason: RejectNonCanonicalPayload, Err: err}
	}
	if !bytes.Equal(canon, raw) {
		return Envelope{}, &RejectError{
			Reason: RejectNonCanonicalPayload,
			Err:    errors.New("payload bytes are not canonical JSON"),
		}
	}

	// Rule 3: Schema validation
	if err := ValidateEnvelope(raw); err != nil {
		return Envelope{}, err
	}

	// Parse payload into Envelope structure, preserving unknown fields
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(raw, &topLevel); err != nil {
		return Envelope{}, &RejectError{Reason: RejectSchemaViolation, Err: err}
	}

	var env Envelope
	env.Raw = raw

	if v, ok := topLevel["object_id"]; ok {
		if err := json.Unmarshal(v, &env.ObjectID); err != nil {
			return Envelope{}, &RejectError{Reason: RejectSchemaViolation, Err: err}
		}
		delete(topLevel, "object_id")
	}
	if v, ok := topLevel["object_type"]; ok {
		if err := json.Unmarshal(v, &env.ObjectType); err != nil {
			return Envelope{}, &RejectError{Reason: RejectSchemaViolation, Err: err}
		}
		delete(topLevel, "object_type")
	}
	if v, ok := topLevel["op_type"]; ok {
		if err := json.Unmarshal(v, &env.OpType); err != nil {
			return Envelope{}, &RejectError{Reason: RejectSchemaViolation, Err: err}
		}
		delete(topLevel, "op_type")
	}
	if v, ok := topLevel["op_version"]; ok {
		if err := json.Unmarshal(v, &env.OpVersion); err != nil {
			return Envelope{}, &RejectError{Reason: RejectSchemaViolation, Err: err}
		}
		delete(topLevel, "op_version")
	}
	if v, ok := topLevel["body"]; ok {
		env.Body = v
		delete(topLevel, "body")
	}
	if len(topLevel) > 0 {
		env.Unknown = topLevel
	}

	return env, nil
}

// DecodeCommit decodes a Commit into an Op, applying reader-validation rules 1–4
// in the spec's defined order: the root tree object's size bound (MaxTreeBytes),
// tree shape, the op.json size bound (MaxPayloadBytes), byte-equality, schema,
// committer/author identity. The commit object's own size bound
// (MaxCommitBytes) is checked earlier still, by GetCommit, before there is a
// Commit to decode.
func DecodeCommit(commit Commit) (Op, error) {
	// Rule 1: Tree validation
	if commit.TreeSize > MaxTreeBytes {
		return Op{}, &RejectError{Reason: RejectTreeTooLarge, Err: fmt.Errorf("root tree object is %d bytes, exceeds %d", commit.TreeSize, MaxTreeBytes)}
	}
	if len(commit.Tree) == 0 {
		return Op{}, &RejectError{Reason: RejectTreeShape, Err: errors.New("missing op.json in tree")}
	}
	if len(commit.Tree) > 1 {
		return Op{}, &RejectError{Reason: RejectTreeShape, Err: errors.New("extra tree entries beside op.json")}
	}
	entry := commit.Tree[0]
	if entry.Name != "op.json" {
		return Op{}, &RejectError{Reason: RejectTreeShape, Err: fmt.Errorf("missing op.json in tree (found %q)", entry.Name)}
	}
	if entry.Mode != "100644" && entry.Mode != "0100644" {
		return Op{}, &RejectError{Reason: RejectTreeShape, Err: fmt.Errorf("invalid op.json file mode %q, must be 100644", entry.Mode)}
	}
	if len(entry.Data) > MaxPayloadBytes {
		return Op{}, &RejectError{Reason: RejectPayloadTooLarge, Err: errors.New("op.json exceeds maximum payload size")}
	}

	// Rules 2 & 3: Payload byte-equality and schema validation
	env, err := DecodePayload(entry.Data)
	if err != nil {
		return Op{}, err
	}

	// Rule 4: Committer / Author match
	if commit.Committer.Name != commit.Author.Name ||
		commit.Committer.Email != commit.Author.Email ||
		!commit.Committer.When.Equal(commit.Author.When) {
		return Op{}, &RejectError{Reason: RejectCommitterMismatch, Err: errors.New("committer does not match author")}
	}

	return Op{
		Envelope:  env,
		ID:        commit.ID,
		Parents:   commit.Parents,
		Author:    commit.Author,
		Committer: commit.Committer,
		Message:   commit.Message,
		Signature: commit.Signature,
	}, nil
}
