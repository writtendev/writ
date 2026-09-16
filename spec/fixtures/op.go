package fixtures

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/writtendev/writ/engine/codec/canonicaljson"
)

// OpDesc defines the structured payload of a Writ operation within a
// fixture commit. The generator canonicalizes it into op.json at mode
// 100644 and derives the commit message per the producer rules.
type OpDesc struct {
	ObjectID   string         `yaml:"object_id"`
	ObjectType string         `yaml:"object_type"`
	OpType     string         `yaml:"op_type"`
	OpVersion  uint64         `yaml:"op_version"`
	Body       any            `yaml:"body"`
	Extra      map[string]any `yaml:",inline"`
}

// BuildOpPayload canonicalizes the op description into byte-stable JSON
// per spec/canonicalization.md and spec/op-envelope.md.
func BuildOpPayload(op *OpDesc) ([]byte, error) {
	if op == nil {
		return nil, fmt.Errorf("fixtures: op is nil")
	}

	payloadMap := make(map[string]any)
	for k, v := range op.Extra {
		payloadMap[k] = v
	}

	payloadMap["object_id"] = op.ObjectID
	payloadMap["object_type"] = op.ObjectType
	payloadMap["op_type"] = op.OpType
	payloadMap["op_version"] = op.OpVersion
	if op.Body != nil {
		payloadMap["body"] = op.Body
	} else {
		payloadMap["body"] = map[string]any{}
	}

	raw, err := json.Marshal(payloadMap)
	if err != nil {
		return nil, fmt.Errorf("fixtures: marshal op payload: %w", err)
	}

	canon, err := canonicaljson.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("fixtures: canonicalize op payload: %w", err)
	}

	return canon, nil
}

// DeriveMessage derives the commit message for an op commit per the
// producer rules in spec/op-envelope.md: `writ: <op_type> <object_type>/<object_id>\n`.
func DeriveMessage(op *OpDesc) string {
	if op == nil {
		return ""
	}
	return fmt.Sprintf("writ: %s %s/%s\n", op.OpType, op.ObjectType, op.ObjectID)
}

// PadOpJSON canonicalizes op with an added body field, "pad", sized so the
// resulting op.json is exactly size bytes — a fixture that pins an exact
// op.json byte length (e.g. the reader-validation rule 1 size bound) this
// way needs no literal megabytes of padding checked in: the tree-entry
// blob SHA already pins the exact bytes, so goldens can carry the byte
// count instead of the content. The pad character is "x", which canonical
// JSON encodes unescaped and without surrounding whitespace, so each
// character added to "pad" costs exactly one output byte, and the needed
// length is computed rather than searched for.
func PadOpJSON(op *OpDesc, size int) ([]byte, error) {
	if op == nil {
		return nil, fmt.Errorf("fixtures: op is nil")
	}

	body, ok := op.Body.(map[string]any)
	if !ok {
		if op.Body != nil {
			return nil, fmt.Errorf("fixtures: op_json_size requires an object body, got %T", op.Body)
		}
		body = map[string]any{}
	}
	if _, exists := body["pad"]; exists {
		return nil, fmt.Errorf("fixtures: op_json_size conflicts with an explicit body.pad field")
	}
	padded := make(map[string]any, len(body)+1)
	for k, v := range body {
		padded[k] = v
	}
	padded["pad"] = ""

	baseOp := *op
	baseOp.Body = padded
	base, err := BuildOpPayload(&baseOp)
	if err != nil {
		return nil, err
	}
	if len(base) > size {
		return nil, fmt.Errorf("fixtures: op_json_size %d is smaller than the unpadded payload (%d bytes)", size, len(base))
	}

	padded["pad"] = strings.Repeat("x", size-len(base))
	return BuildOpPayload(&baseOp)
}
