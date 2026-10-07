package writ

import (
	"github.com/writtendev/writ/internal/state"
)

// UnknownOp records an operation that was preserved in the DAG and participated
// in ordering and ancestry, but whose (op_type, op_version) had no declared rules
// per spec/fold.md §7 or whose body a declared rule found uninterpretable per §7.1.
type UnknownOp struct {
	Commit       string `json:"commit"`
	ObjectType   string `json:"object_type"`
	OpType       string `json:"op_type"`
	OpVersion    int64  `json:"op_version"`
	Verification string `json:"verification"`
}

// NormalizePerson normalizes a person identifier string per spec/identifiers.md
// (scheme lowercased; value trimmed and case-folded).
func NormalizePerson(s string) string {
	return state.NormalizePerson(s)
}
