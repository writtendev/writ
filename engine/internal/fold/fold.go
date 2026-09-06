package fold

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/writtendev/writ/engine/codec"
)

// Rule specifies the merge strategy and value type for an (op_type, op_version, field) tuple.
type Rule struct {
	OpType    string            `json:"op_type,omitempty"`
	OpVersion int64             `json:"op_version,omitempty"`
	Field     string            `json:"field"`
	Target    string            `json:"target,omitempty"`
	Strategy  string            `json:"strategy"`
	Key       []string          `json:"key,omitempty"`
	Lattice   []string          `json:"lattice,omitempty"`
	ValueType string            `json:"value_type,omitempty"`
	Enum      []string          `json:"enum,omitempty"`
	MaxLength int64             `json:"max_length,omitempty"`
	KeyTypes  map[string]string `json:"key_types,omitempty"`
	// ObjectType scopes the rule to one object type (spec/fold.md §5): empty
	// on either the rule or the op matches anything, mirroring OpVersion's
	// treatment in opMatchesRule. It is left empty on every hand-written Go
	// rule table (ReviewRules, IssueRules, etc.) rather than set to the
	// table's own type: those tables are already selected per object type by
	// their callers and the typed reducers, so an empty ObjectType changes
	// nothing for them, and WRIT-194 deletes the tables outright — setting it
	// on every literal there would be throwaway work. Only log-sourced rules
	// (RulesFromSchemas) and rules built from spec.FieldRules() carry it.
	ObjectType string `json:"object_type,omitempty"`
}

// TargetKey returns Target if non-empty, otherwise Field.
func (r Rule) TargetKey() string {
	if r.Target != "" {
		return r.Target
	}
	return r.Field
}

// NormalizesKey reports whether keyCol is declared as a person-ref key column
// (spec/value-types.md): normalization is intrinsic to the person-ref value
// type rather than a separate rule attribute.
func (r Rule) NormalizesKey(keyCol string) bool {
	return r.KeyTypes[keyCol] == "person-ref"
}

// NormalizesValue reports whether the rule's scalar (or keyed-register) value
// is a person-ref, and so is normalized per spec/identifiers.md.
func (r Rule) NormalizesValue() bool {
	return r.ValueType == "person-ref"
}

// NormalizesItems reports whether the rule's collection elements are
// person-ref values, and so are normalized per spec/identifiers.md.
func (r Rule) NormalizesItems() bool {
	return r.ValueType == "person-ref"
}

// OpRef identifies an operation in an object's total order sequence L
// along with its causality-monotone effective timestamp t*.
type OpRef struct {
	Commit string `json:"commit"`
	TStar  int64  `json:"t_star"`
}

// UnknownOp records an operation that was preserved in the DAG and participated
// in ordering and ancestry, but contributed no field writes: either its
// (op_type, op_version) had no declared rules (spec/fold.md §7), or a field
// carrying a declared rule held a value its strategy cannot consume, which
// makes the whole operation uninterpretable (spec/fold.md §7.1). Both are
// quarantined through this one channel, so a count of them leads to a commit
// and a commit to the raw op and its signer.
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

// opMatchesRule returns true if op matches the rule's op_type, op_version and
// object_type filters. object_type is read straight off op (spec/fold.md §5):
// no clock, no I/O, no ambient state, and — unlike determineObjectType below,
// which infers a whole op set's object type from create-op precedence for
// ObjectState.ObjectType — this never resolves anything beyond the single op
// in front of it.
func opMatchesRule(op codec.Op, r Rule) bool {
	if r.OpType != "" && r.OpType != op.OpType {
		return false
	}
	if r.OpVersion != 0 && op.OpVersion != 0 && r.OpVersion != op.OpVersion {
		return false
	}
	if r.ObjectType != "" && op.ObjectType != "" && r.ObjectType != op.ObjectType {
		return false
	}
	return true
}

// determineObjectType determines the object type from an ops slice, matching the
// precedence in engine/projection/materialize.go: prioritize create ops with non-empty
// ObjectType, then first non-empty ObjectType, then ops[0].ObjectType if non-empty, else "".
func determineObjectType(ops []codec.Op) string {
	for _, op := range ops {
		if op.OpType == "create" && op.ObjectType != "" {
			return op.ObjectType
		}
	}
	for _, op := range ops {
		if op.ObjectType != "" {
			return op.ObjectType
		}
	}
	if len(ops) > 0 {
		return ops[0].ObjectType
	}
	return ""
}

// Fold executes deterministic fold reduction on an input set of operations
// against declared field merge rules, returning the resulting ObjectState.
func Fold(ops []codec.Op, rules []Rule) (ObjectState, error) {
	if len(ops) == 0 {
		return ObjectState{State: make(map[string]any)}, nil
	}

	objectID := ops[0].ObjectID
	objectType := determineObjectType(ops)

	orderedOps, err := OrderWithTStar(ops)
	if err != nil {
		return ObjectState{}, err
	}

	reach := BuildReachability(orderedOps)

	bodyMap := make(map[string]map[string]any, len(orderedOps))
	rawBodyMap := make(map[string]map[string]json.RawMessage, len(orderedOps))
	for _, o := range orderedOps {
		var bm map[string]any
		var rbm map[string]json.RawMessage
		if len(o.Op.Body) > 0 {
			if err := json.Unmarshal(o.Op.Body, &bm); err != nil {
				return ObjectState{}, fmt.Errorf("fold: unmarshaling op %s body: %w", o.Op.ID, err)
			}
			if err := json.Unmarshal(o.Op.Body, &rbm); err != nil {
				return ObjectState{}, fmt.Errorf("fold: unmarshaling op %s raw body: %w", o.Op.ID, err)
			}
		}
		if bm == nil {
			bm = make(map[string]any)
		}
		if rbm == nil {
			rbm = make(map[string]json.RawMessage)
		}
		bodyMap[o.Op.ID] = bm
		rawBodyMap[o.Op.ID] = rbm
	}

	// Quarantine, in total order, the ops that contribute no field writes: ops
	// matching no declared rule (spec/fold.md §7) and ops whose body a declared
	// rule cannot consume (spec/fold.md §7.1). Both stay full members of the
	// restricted DAG — they are in the total order and in every ancestry
	// calculation — and both are reported through unknown_ops rather than as a
	// fold error. One bad op costs that op, never the object.
	var unknownOps []UnknownOp
	rejected := make(map[string]bool)
	for _, o := range orderedOps {
		known := false
		for _, r := range rules {
			if opMatchesRule(o.Op, r) {
				known = true
				break
			}
		}
		if known && Uninterpretable(o.Op, bodyMap[o.Op.ID], rules) {
			rejected[o.Op.ID] = true
			known = false
		}
		if !known {
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     o.Op.ID,
				ObjectType: o.Op.ObjectType,
				OpType:     o.Op.OpType,
				OpVersion:  o.Op.OpVersion,
			})
		}
	}

	// Group matching rules by target key for ops actually present in input set
	matchedRulesByField := make(map[string][]Rule)
	for _, r := range rules {
		for _, o := range orderedOps {
			if rejected[o.Op.ID] {
				continue
			}
			if opMatchesRule(o.Op, r) {
				bm := bodyMap[o.Op.ID]
				hasWrite := false
				if _, present := bm[r.Field]; present || o.Op.OpType == "delete" || o.Op.OpType == "undelete" {
					hasWrite = true
				} else if r.Strategy == "set-observed-remove" {
					if r.Field == "add" && bm["remove"] != nil {
						hasWrite = true
					} else if r.Field == "remove" && bm["add"] != nil {
						hasWrite = true
					}
				} else if r.Field == "subject" && o.Op.OpType == "approval" && o.Op.Author.Email != "" {
					hasWrite = true
				}
				if hasWrite {
					targetKey := r.TargetKey()
					matchedRulesByField[targetKey] = append(matchedRulesByField[targetKey], r)
					break
				}
			}
		}
	}

	// Instantiate strategy accumulators for each target key
	accumulators := make(map[string]Accumulator, len(matchedRulesByField))
	for targetKey, fieldRules := range matchedRulesByField {
		primaryRule := fieldRules[0]
		acc, err := NewAccumulator(primaryRule, reach)
		if err != nil {
			return ObjectState{}, fmt.Errorf("fold: target %q: %w", targetKey, err)
		}
		accumulators[targetKey] = acc
	}

	// Walk total order L once, dispatching to matching field accumulators
	for _, o := range orderedOps {
		if rejected[o.Op.ID] {
			continue
		}
		bm := bodyMap[o.Op.ID]
		rbm := rawBodyMap[o.Op.ID]
		for targetKey, fieldRules := range matchedRulesByField {
			for _, r := range fieldRules {
				if opMatchesRule(o.Op, r) {
					acc := accumulators[targetKey]
					if err := acc.Apply(r, o.Op, bm, rbm); err != nil {
						return ObjectState{}, fmt.Errorf("fold: applying op %s to target %q: %w", o.Op.ID, targetKey, err)
					}
					break
				}
			}
		}
	}

	// Collect folded state in deterministic sorted field order
	state := make(map[string]any)
	var fieldNames []string
	for f := range matchedRulesByField {
		fieldNames = append(fieldNames, f)
	}
	sort.Strings(fieldNames)

	for _, f := range fieldNames {
		acc := accumulators[f]
		if acc.HasValue() {
			val, err := acc.Result()
			if err != nil {
				return ObjectState{}, fmt.Errorf("fold: collecting field %q: %w", f, err)
			}
			state[f] = val
		}
	}

	totalOrder := make([]OpRef, len(orderedOps))
	for i, o := range orderedOps {
		totalOrder[i] = OpRef{
			Commit: o.Op.ID,
			TStar:  o.TStar,
		}
	}

	return ObjectState{
		ObjectID:   objectID,
		ObjectType: objectType,
		TotalOrder: totalOrder,
		State:      state,
		UnknownOps: unknownOps,
	}, nil
}
