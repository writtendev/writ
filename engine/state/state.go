package state

import (
	"github.com/writtendev/writ/engine/internal/person"
	"github.com/writtendev/writ/engine/resolve"
)

// NormalizePerson normalizes a person identifier string per spec/identifiers.md
// (trimmed leading/trailing whitespace, lowercase). The rule itself lives in
// engine/internal/person; this is the name the state package, its callers, and
// the projection use.
func NormalizePerson(s string) string {
	return person.NormalizePerson(s)
}

// PersonValueIsStreamSafe reports whether a person identifier's value
// conforms to spec/identifiers.md §Value shape: Stream-Safe Text. The rule
// itself lives in engine/internal/person; it is exposed here on the same
// terms as NormalizePerson above, so that spec/reffold_test.go — which
// cannot reach engine/internal/person directly — can bind the reference
// copy of this rule (spec.PersonValueIsStreamSafe) to the engine's one
// definition of it, the way it already does for normalization.
func PersonValueIsStreamSafe(s string) bool {
	return person.IsStreamSafe(s)
}

// Anchor is a content-based position in code (v1), a value type any
// schema-declared object can carry (spec/anchors.md).
// Fold carries anchors verbatim as data per spec/fold.md §6.
type Anchor = resolve.Anchor

// OpRef identifies an operation in an object's total order sequence L
// along with its causality-monotone effective timestamp t*.
type OpRef struct {
	Commit string `json:"commit"`
	TStar  int64  `json:"t_star"`
}

// UnknownOp records an operation that was preserved in the DAG and participated
// in ordering and ancestry, but whose (op_type, op_version) had no declared rules
// per spec/fold.md §7 or whose body a declared rule found uninterpretable per §7.1.
type UnknownOp struct {
	Commit     string `json:"commit"`
	ObjectType string `json:"object_type"`
	OpType     string `json:"op_type"`
	OpVersion  int64  `json:"op_version"`
}

// ObjectState is the folded state produced by the fold driver for a collaborative object.
type ObjectState struct {
	ObjectID   string         `json:"object_id"`
	ObjectType string         `json:"object_type,omitempty"`
	TotalOrder []OpRef        `json:"total_order"`
	State      map[string]any `json:"state"`
	UnknownOps []UnknownOp    `json:"unknown_ops,omitempty"`
}
