package scenario

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/resolve"
)

// Writer represents a human author across one or more devices.
type Writer struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// Device represents an individual machine / clone for a Writer.
// Per spec/ref-layout.md §Writer ID, devices of the same human get distinct
// writer IDs by design.
type Device struct {
	Name     string            `json:"name"`
	Writer   Writer            `json:"writer"`
	WriterID identity.WriterID `json:"writer_id"`
}

// Step is a declarative action executed during a scenario.
type Step interface {
	isStep()
}

// AppendOp commits an operation onto the device's local writer chain for Envelope.ObjectType.
type AppendOp struct {
	Device        Device
	At            time.Time
	Envelope      codec.Envelope
	CausalParents []string
}

func (AppendOp) isStep() {}

// Commit creates a real git commit with the specified files on the device's working tree.
type Commit struct {
	Device  Device
	Files   map[string]string
	Message string
	Branch  string
	At      time.Time
}

func (Commit) isStep() {}

// Push executes system-git push via sync.Client.Push, pushing refs/writ/<writer-id>/* to remote.
type Push struct {
	Device        Device
	Remote        string
	ExpectedError error
}

func (Push) isStep() {}

// PushBranch pushes a code branch (e.g. "main") to remote via git push.
type PushBranch struct {
	Device        Device
	Branch        string
	Remote        string
	Force         bool
	ExpectedError error
}

func (PushBranch) isStep() {}

// Fetch executes system-git fetch via sync.Client.Fetch.
type Fetch struct {
	Device        Device
	Remote        string
	ExpectedError error
}

func (Fetch) isStep() {}

// ForcePushChain rewinds the device's local writ ref for ObjectType to TargetOpIndex / TargetOpSHA
// and force-pushes to remote.
type ForcePushChain struct {
	Device        Device
	ObjectType    string
	TargetOpIndex int
	TargetOpSHA   string
	Remote        string
	ExpectedError error
}

func (ForcePushChain) isStep() {}

// ResetLocalChain resets the device's local writ ref for ObjectType to TargetOpIndex / TargetOpSHA without pushing.
type ResetLocalChain struct {
	Device        Device
	ObjectType    string
	TargetOpIndex int
	TargetOpSHA   string
}

func (ResetLocalChain) isStep() {}

// AnchorCheck specifies an anchor resolution check to perform during Converge.
type AnchorCheck struct {
	CommentID string
	Branch    string
}

// Converge executes DAG enumeration on all clones, folds each collaborative object,
// performs anchor checks against the target code tree, asserts byte-identical
// converged snapshots across all devices, and verifies against the golden file.
type Converge struct {
	AnchorChecks     []AnchorCheck
	GoldenName       string
	SkipGoldenUpdate bool
}

func (Converge) isStep() {}

// Scenario defines a multi-writer test scenario.
type Scenario struct {
	Name    string
	Devices []Device
	Steps   []Step
}

// ObjectRecord is a folded collaborative object of any type in a converged
// snapshot — the schema-shaped replacement for the per-type
// ReviewRecord/IssueRecord/ProjectRecord/CycleRecord this ticket deletes.
type ObjectRecord struct {
	ObjectID    string           `json:"object_id"`
	ObjectType  string           `json:"object_type"`
	ObjectState writ.ObjectState `json:"object_state"`
}

// ResolutionRecord records the deterministic resolution of an anchored comment.
type ResolutionRecord struct {
	CommentID  string             `json:"comment_id"`
	Anchor     resolve.Anchor     `json:"anchor"`
	Resolution resolve.Resolution `json:"resolution"`
	Status     string             `json:"status"`
}

// Snapshot represents the materialized multi-object state of a converged clone.
type Snapshot struct {
	Objects     []ObjectRecord     `json:"objects,omitempty"`
	Resolutions []ResolutionRecord `json:"resolutions,omitempty"`
}

// MakeAnchor builds a resolve.Anchor for a given file content and line range.
func MakeAnchor(commitSHA, path, content string, startLine, endLine int) resolve.Anchor {
	lines := resolve.SplitLines([]byte(content))
	blob := computeBlobOID([]byte(content))

	beforeStart := startLine - 3
	if beforeStart < 1 {
		beforeStart = 1
	}
	// before and after start empty rather than nil: spec/schemas/anchor.schema.json
	// requires both as arrays, and a nil slice marshals to null.
	before := []string{}
	for i := beforeStart; i < startLine && i <= len(lines); i++ {
		before = append(before, lines[i-1])
	}

	afterEnd := endLine + 3
	if afterEnd > len(lines) {
		afterEnd = len(lines)
	}
	after := []string{}
	for i := endLine + 1; i <= afterEnd; i++ {
		after = append(after, lines[i-1])
	}

	rangeLen := endLine - startLine + 1
	ctxLines := make([]string, 0, rangeLen)
	for i := startLine; i <= endLine && i <= len(lines); i++ {
		ctxLines = append(ctxLines, lines[i-1])
	}

	return resolve.Anchor{
		Version: 1,
		New: &resolve.SideAnchor{
			Commit: commitSHA,
			Path:   path,
			Blob:   blob,
			Range:  &resolve.Range{Start: startLine, End: endLine},
			Context: &resolve.Context{
				Before: before,
				Lines:  ctxLines,
				After:  after,
			},
		},
	}
}

func computeBlobOID(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}
