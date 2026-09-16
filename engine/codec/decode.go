package codec

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	"github.com/writtendev/writ/engine/codec/canonicaljson"
)

// DecodePayload decodes canonical JSON bytes into an Envelope, applying the
// byte-equality rule and envelope schema validation. Original bytes are retained in Raw.
// Unknown top-level fields are preserved in Unknown; body is preserved as raw JSON.
func DecodePayload(raw []byte) (Envelope, error) {
	// Rule 2: Byte-equality rule (canonicalization check)
	canon, err := canonicaljson.Marshal(raw)
	if err != nil {
		reason := RejectNonCanonicalPayload
		if strings.Contains(err.Error(), "duplicate") {
			reason = RejectDuplicateKey
		} else if strings.Contains(err.Error(), "lone surrogate") {
			reason = RejectLoneSurrogate
		}
		return Envelope{}, &RejectError{Reason: reason, Err: err}
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

func hasOpJSONInEntries(entries []TreeEntry) bool {
	for _, e := range entries {
		if e.Name == "op.json" {
			return true
		}
		if hasOpJSONInEntries(e.Entries) {
			return true
		}
	}
	return false
}

// DecodeCommit decodes a Commit into an Op, applying reader-validation rules 1–4
// in the spec's defined order: tree shape (including the rule 1 size bound,
// MaxPayloadBytes), byte-equality, schema, committer/author identity.
func DecodeCommit(commit Commit) (Op, error) {
	// Rule 1: Tree validation
	var opJsonFound bool
	var opBlob []byte
	var invalidMode bool
	var opJsonInSubdir bool

	for _, entry := range commit.Tree {
		if entry.Name == "op.json" {
			opJsonFound = true
			opBlob = entry.Data
			if entry.Mode != "100644" && entry.Mode != "100644\n" && entry.Mode != "0100644" {
				invalidMode = true
			}
		}
		if hasOpJSONInEntries(entry.Entries) {
			opJsonInSubdir = true
		}
	}

	if !opJsonFound && opJsonInSubdir {
		return Op{}, &RejectError{Reason: RejectOpJSONSubdirectory, Err: errors.New("op.json in subdirectory")}
	}
	if !opJsonFound {
		return Op{}, &RejectError{Reason: RejectMissingOpJSON, Err: errors.New("missing op.json in tree")}
	}
	if len(commit.Tree) > 1 {
		return Op{}, &RejectError{Reason: RejectExtraTreeEntry, Err: errors.New("extra tree entries beside op.json")}
	}
	if invalidMode {
		return Op{}, &RejectError{Reason: RejectInvalidOpJSONMode, Err: errors.New("invalid op.json file mode, must be 100644")}
	}
	if len(opBlob) > MaxPayloadBytes {
		return Op{}, &RejectError{Reason: RejectPayloadTooLarge, Err: errors.New("op.json exceeds maximum payload size")}
	}

	// Rules 2 & 3: Payload byte-equality and schema validation
	env, err := DecodePayload(opBlob)
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
