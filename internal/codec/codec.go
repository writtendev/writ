// Package codec implements canonical op envelope encoding, decoding, and commit validation for Writ operations.
package codec

import (
	"encoding/json"
	"time"
)

// Identity represents the author or committer identity and timestamp on an op commit.
type Identity struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	When  time.Time `json:"when"`
}

// TreeEntry represents a file or subtree entry in an op commit's tree.
type TreeEntry struct {
	Name    string      `json:"name"`
	Mode    string      `json:"mode"`
	Hash    string      `json:"hash,omitempty"`
	Data    []byte      `json:"data,omitempty"`
	Entries []TreeEntry `json:"entries,omitempty"`
}

// Commit is the pure, repository-independent representation of an op commit.
type Commit struct {
	ID        string      `json:"id,omitempty"`
	Parents   []string    `json:"parents,omitempty"`
	Author    Identity    `json:"author"`
	Committer Identity    `json:"committer"`
	Message   string      `json:"message,omitempty"`
	Signature string      `json:"signature,omitempty"`
	Payload   []byte      `json:"-"`
	Tree      []TreeEntry `json:"tree"`
}

// Envelope represents the logical payload of a Writ operation (op.json).
type Envelope struct {
	ObjectID   string                     `json:"object_id"`
	ObjectType string                     `json:"object_type"`
	OpType     string                     `json:"op_type"`
	OpVersion  int64                      `json:"op_version"`
	Body       json.RawMessage            `json:"body"`
	Unknown    map[string]json.RawMessage `json:"-"`
	Raw        []byte                     `json:"-"`
}

// Op represents a fully decoded and validated Writ operation, combining the
// payload envelope with its commit carrier metadata.
type Op struct {
	Envelope
	ID        string   `json:"id"`
	Parents   []string `json:"parents"`
	Author    Identity `json:"author"`
	Committer Identity `json:"committer"`
	Message   string   `json:"message"`
	Signature string   `json:"signature,omitempty"`
	// Verification carries the outcome of verifying this op's signature
	// against the reader's trust store. DecodeCommit leaves it zero; only
	// the DAG ingest boundary (dag.Store.EnumerateSince) sets it
	// (spec/signing.md "Ingest-time verification"). It is data only: fold
	// never reads it and every op folds regardless of its outcome
	// (AGENTS.md "Fold is pure and deterministic").
	Verification Verification `json:"verification"`
}
