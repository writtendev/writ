package fold

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/value"
)

// Accumulator defines the interface for state reducers in the closed strategy catalogue.
type Accumulator interface {
	// Apply updates the accumulator with an operation in total order L according to the matched rule.
	Apply(rule Rule, op codec.Op, body map[string]any, rawBody map[string]json.RawMessage) error
	// HasValue returns true if at least one operation has contributed to this accumulator.
	HasValue() bool
	// Result returns the folded value for this field in serialized representation.
	Result() (any, error)
}

// StrategyFactory creates an Accumulator for a given rule.
type StrategyFactory func(rule Rule, reach ReachOracle) (Accumulator, error)

var strategyCatalogue = map[string]StrategyFactory{
	"lww":                 newLWWAccumulator,
	"create-once":         newCreateOnceAccumulator,
	"set-union":           newSetUnionAccumulator,
	"set-observed-remove": newSetObservedRemoveAccumulator,
	"append":              newAppendAccumulator,
	"tombstone":           newTombstoneAccumulator,
	"lattice":             newLatticeAccumulator,
	"keyed-lww":           newKeyedLWWAccumulator,
	"multi-value":         newMultiValueAccumulator,
}

// NewAccumulator instantiates an Accumulator for the specified rule and reachability oracle.
func NewAccumulator(rule Rule, reach ReachOracle) (Accumulator, error) {
	factory, ok := strategyCatalogue[rule.Strategy]
	if !ok {
		return nil, fmt.Errorf("fold: unknown strategy %q", rule.Strategy)
	}
	return factory(rule, reach)
}

// 1. LWW (Last-Writer-Wins)
type lwwAccumulator struct {
	field  string
	hasVal bool
	val    any
}

func newLWWAccumulator(rule Rule, _ ReachOracle) (Accumulator, error) {
	return &lwwAccumulator{field: rule.Field}, nil
}

func (a *lwwAccumulator) Apply(rule Rule, op codec.Op, body map[string]any, _ map[string]json.RawMessage) error {
	if val, ok := body[rule.Field]; ok && val != nil {
		// Empty scalar contract (spec/fold.md §5.1): empty strings (including
		// person identifiers that normalize to empty) are preserved in the
		// generic fold map as deliberate scalar writes.
		if s, ok := val.(string); ok && rule.NormalizesValue() {
			val = value.Normalize(rule.ValueType, s)
		}
		a.val = val
		a.hasVal = true
	}
	return nil
}

func (a *lwwAccumulator) HasValue() bool       { return a.hasVal }
func (a *lwwAccumulator) Result() (any, error) { return a.val, nil }

// 2. Create-Once
type createOnceAccumulator struct {
	field  string
	hasVal bool
	val    any
}

func newCreateOnceAccumulator(rule Rule, _ ReachOracle) (Accumulator, error) {
	return &createOnceAccumulator{field: rule.Field}, nil
}

func (a *createOnceAccumulator) Apply(rule Rule, _ codec.Op, body map[string]any, rawBody map[string]json.RawMessage) error {
	if !a.hasVal {
		if raw, ok := rawBody[rule.Field]; ok && len(raw) > 0 && string(raw) != "null" {
			a.val = raw
			a.hasVal = true
		} else if val, ok := body[rule.Field]; ok && val != nil {
			a.val = val
			a.hasVal = true
		}
	}
	return nil
}

func (a *createOnceAccumulator) HasValue() bool       { return a.hasVal }
func (a *createOnceAccumulator) Result() (any, error) { return a.val, nil }

// 3. Set-Union
type setUnionAccumulator struct {
	field  string
	hasSet bool
	set    map[string]bool
}

func newSetUnionAccumulator(rule Rule, _ ReachOracle) (Accumulator, error) {
	return &setUnionAccumulator{
		field: rule.Field,
		set:   make(map[string]bool),
	}, nil
}

func (a *setUnionAccumulator) Apply(rule Rule, _ codec.Op, body map[string]any, _ map[string]json.RawMessage) error {
	raw, ok := body[rule.Field]
	if !ok || raw == nil {
		return nil
	}
	a.hasSet = true
	normalizeItem := func(it string) string {
		if rule.NormalizesItems() {
			return value.Normalize(rule.ValueType, it)
		}
		return it
	}
	// Elements that are the empty string are dropped (spec/fold.md §5.3).
	add := func(item string) {
		if norm := normalizeItem(item); norm != "" {
			a.set[norm] = true
		}
	}
	// Every item is a string: an op carrying anything else at this field is
	// uninterpretable and never reaches an accumulator (spec/fold.md §7.1).
	switch val := raw.(type) {
	case []any:
		for _, item := range val {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	case []string:
		for _, item := range val {
			add(item)
		}
	case string:
		add(val)
	}
	return nil
}

func (a *setUnionAccumulator) HasValue() bool { return a.hasSet }
func (a *setUnionAccumulator) Result() (any, error) {
	res := make([]string, 0, len(a.set))
	for k := range a.set {
		res = append(res, k)
	}
	sort.Strings(res)
	return res, nil
}

// 4. Set-Observed-Remove (Add-Wins OR-Set)
type orSetAddRecord struct {
	opID string
	item string
}

type orSetRemoveRecord struct {
	opID string
	item string
}

type setObservedRemoveAccumulator struct {
	field   string
	reach   ReachOracle
	hasOps  bool
	adds    []orSetAddRecord
	removes []orSetRemoveRecord
}

func newSetObservedRemoveAccumulator(rule Rule, reach ReachOracle) (Accumulator, error) {
	return &setObservedRemoveAccumulator{
		field: rule.Field,
		reach: reach,
	}, nil
}

func (a *setObservedRemoveAccumulator) Apply(rule Rule, op codec.Op, body map[string]any, _ map[string]json.RawMessage) error {
	var adds, removes []string
	if rule.Field == "add" || rule.Field == "remove" {
		if m, ok := body["add"].(map[string]any); ok {
			adds = append(adds, orSetItems(m["add"])...)
			removes = append(removes, orSetItems(m["remove"])...)
		} else {
			adds = append(adds, orSetItems(body["add"])...)
		}
		if m, ok := body["remove"].(map[string]any); ok {
			adds = append(adds, orSetItems(m["add"])...)
			removes = append(removes, orSetItems(m["remove"])...)
		} else {
			removes = append(removes, orSetItems(body["remove"])...)
		}
	} else if bodyMap, ok := body[rule.Field].(map[string]any); ok {
		adds = append(adds, orSetItems(bodyMap["add"])...)
		removes = append(removes, orSetItems(bodyMap["remove"])...)
	} else if raw, ok := body[rule.Field]; ok && raw != nil {
		if (len(op.OpType) >= 4 && op.OpType[:4] == "add-") || op.OpType == "add" {
			adds = append(adds, orSetItems(raw)...)
		} else if (len(op.OpType) >= 7 && op.OpType[:7] == "remove-") || op.OpType == "remove" {
			removes = append(removes, orSetItems(raw)...)
		}
	}

	if len(adds) == 0 && len(removes) == 0 {
		return nil
	}
	a.hasOps = true

	// Person-valued fields normalize per spec/identifiers.md; every other item
	// is taken verbatim. Items that are empty after normalization are dropped
	// from both sides of the OR-set, whatever the op type (spec/fold.md §5.4).
	normalizeItem := func(it string) string {
		if rule.NormalizesItems() {
			return value.Normalize(rule.ValueType, it)
		}
		return it
	}

	// Every item is a string; see setUnionAccumulator.Apply. A side holds one
	// item or an array of them, and orSetItems consumes exactly what
	// orSetAccepts admitted.
	for _, it := range adds {
		if item := normalizeItem(it); item != "" {
			a.adds = append(a.adds, orSetAddRecord{opID: op.ID, item: item})
		}
	}
	for _, it := range removes {
		if item := normalizeItem(it); item != "" {
			a.removes = append(a.removes, orSetRemoveRecord{opID: op.ID, item: item})
		}
	}
	return nil
}

func (a *setObservedRemoveAccumulator) HasValue() bool { return a.hasOps }
func (a *setObservedRemoveAccumulator) Result() (any, error) {
	presentSet := make(map[string]bool)
	for _, add := range a.adds {
		removed := false
		for _, rem := range a.removes {
			if rem.item == add.item && a.reach.IsAncestor(add.opID, rem.opID) {
				removed = true
				break
			}
		}
		if !removed {
			presentSet[add.item] = true
		}
	}
	res := make([]string, 0, len(presentSet))
	for k := range presentSet {
		res = append(res, k)
	}
	sort.Strings(res)
	return res, nil
}

// 5. Append
type appendAccumulator struct {
	field     string
	hasAppend bool
	list      []any
}

func newAppendAccumulator(rule Rule, _ ReachOracle) (Accumulator, error) {
	return &appendAccumulator{field: rule.Field}, nil
}

func (a *appendAccumulator) Apply(rule Rule, _ codec.Op, body map[string]any, _ map[string]json.RawMessage) error {
	raw, ok := body[rule.Field]
	if !ok || raw == nil {
		return nil
	}
	a.hasAppend = true
	if slice, ok := raw.([]any); ok {
		a.list = append(a.list, slice...)
	} else {
		a.list = append(a.list, raw)
	}
	return nil
}

func (a *appendAccumulator) HasValue() bool { return a.hasAppend }

// Result returns the appended entries. The initial state of an append field is
// the empty list, not null (spec/fold.md §5.5), so an op that writes the field
// with an empty array folds to [] — a written-but-empty list, which is what it
// says — rather than to a JSON null that no other implementation would produce
// from the same bytes.
func (a *appendAccumulator) Result() (any, error) {
	if a.list == nil {
		return []any{}, nil
	}
	return a.list, nil
}

// 6. Tombstone
type tombstoneAccumulator struct {
	field        string
	reach        ReachOracle
	hasTombstone bool
	deletes      []string
	undeletes    []string
}

func newTombstoneAccumulator(rule Rule, reach ReachOracle) (Accumulator, error) {
	return &tombstoneAccumulator{
		field: rule.Field,
		reach: reach,
	}, nil
}

func (a *tombstoneAccumulator) Apply(rule Rule, op codec.Op, body map[string]any, _ map[string]json.RawMessage) error {
	val, hasField := body[rule.Field]
	if op.OpType == "delete" || (hasField && val == true) {
		a.deletes = append(a.deletes, op.ID)
		a.hasTombstone = true
	} else if op.OpType == "undelete" || (hasField && val == false) {
		a.undeletes = append(a.undeletes, op.ID)
		a.hasTombstone = true
	}
	return nil
}

func (a *tombstoneAccumulator) HasValue() bool { return a.hasTombstone }
func (a *tombstoneAccumulator) Result() (any, error) {
	isDeleted := false
	for _, d := range a.deletes {
		cleared := false
		for _, u := range a.undeletes {
			if a.reach.IsAncestor(d, u) {
				cleared = true
				break
			}
		}
		if !cleared {
			isDeleted = true
			break
		}
	}
	return isDeleted, nil
}

// 7. Lattice
type latticeAccumulator struct {
	field       string
	rankMap     map[string]int
	currentRank int
	currentVal  string
	hasLattice  bool
}

func newLatticeAccumulator(rule Rule, _ ReachOracle) (Accumulator, error) {
	if len(rule.Lattice) == 0 {
		return nil, fmt.Errorf("fold: lattice strategy for field %q requires non-empty lattice elements", rule.Field)
	}
	rankMap := make(map[string]int, len(rule.Lattice))
	for i, elem := range rule.Lattice {
		rankMap[elem] = i
	}
	return &latticeAccumulator{
		field:       rule.Field,
		rankMap:     rankMap,
		currentRank: -1,
	}, nil
}

func (a *latticeAccumulator) Apply(rule Rule, _ codec.Op, body map[string]any, _ map[string]json.RawMessage) error {
	raw, ok := body[rule.Field]
	if !ok || raw == nil {
		return nil
	}
	// The value is a string; see setUnionAccumulator.Apply. A string outside
	// the declared lattice is ignored rather than rejected: that is a value
	// from a future vocabulary, which preserve-and-ignore covers.
	valStr, ok := raw.(string)
	if !ok {
		return nil
	}
	if rk, ok := a.rankMap[valStr]; ok {
		if rk > a.currentRank {
			a.currentRank = rk
			a.currentVal = valStr
			a.hasLattice = true
		}
	}
	return nil
}

func (a *latticeAccumulator) HasValue() bool       { return a.hasLattice }
func (a *latticeAccumulator) Result() (any, error) { return a.currentVal, nil }

// 8. Keyed-LWW
type keyedEntry struct {
	key   []string
	value any
}

type keyedLWWAccumulator struct {
	field    string
	hasKeyed bool
	latest   map[string]*keyedEntry
}

func newKeyedLWWAccumulator(rule Rule, _ ReachOracle) (Accumulator, error) {
	if len(rule.Key) == 0 {
		return nil, fmt.Errorf("fold: keyed-lww strategy for field %q requires non-empty key", rule.Field)
	}
	return &keyedLWWAccumulator{
		field:  rule.Field,
		latest: make(map[string]*keyedEntry),
	}, nil
}

func (a *keyedLWWAccumulator) Apply(rule Rule, op codec.Op, body map[string]any, _ map[string]json.RawMessage) error {
	normVal := rule.NormalizesValue()

	val, ok := body[rule.Field]
	if !ok {
		if rule.Field == "subject" && op.OpType == "approval" && op.Author.Email != "" {
			val = value.Normalize("person-ref", "email:"+op.Author.Email)
		} else {
			return nil
		}
	} else if normVal {
		if s, isStr := val.(string); isStr {
			norm := value.Normalize(rule.ValueType, s)
			if norm == "" && rule.Field == "subject" && op.OpType == "approval" && op.Author.Email != "" {
				norm = value.Normalize("person-ref", "email:"+op.Author.Email)
			}
			val = norm
		}
	}
	a.hasKeyed = true
	// The stored value is normalized on the same terms as the key component it
	// mirrors: a person identifier reads back normalized per spec/identifiers.md.
	key := make([]string, 0, len(rule.Key))
	for _, kf := range rule.Key {
		// Every present key component is a string; see setUnionAccumulator.Apply.
		// An absent one contributes the empty component, except for approval
		// subject which falls back to the commit author's email.
		vStr, _ := body[kf].(string)
		if rule.NormalizesKey(kf) {
			vStr = value.Normalize(rule.KeyTypes[kf], vStr)
		}
		if vStr == "" && kf == "subject" && op.OpType == "approval" && op.Author.Email != "" {
			vStr = value.Normalize("person-ref", "email:"+op.Author.Email)
		}
		key = append(key, vStr)
	}
	// The map key groups writes addressing the same register and is never
	// serialized. It is a JSON array so the encoding is injective over the key
	// tuple without depending on any one language's rendering of a slice.
	keyStr, err := json.Marshal(key)
	if err != nil {
		return fmt.Errorf("fold: encoding keyed-lww key for field %q: %w", rule.TargetKey(), err)
	}
	a.latest[string(keyStr)] = &keyedEntry{key: key, value: val}
	return nil
}

func (a *keyedLWWAccumulator) HasValue() bool { return a.hasKeyed }
func (a *keyedLWWAccumulator) Result() (any, error) {
	entries := make([]*keyedEntry, 0, len(a.latest))
	for _, e := range a.latest {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		aKey, bKey := entries[i].key, entries[j].key
		for x := range aKey {
			if aKey[x] != bKey[x] {
				return aKey[x] < bKey[x]
			}
		}
		return false
	})
	keyed := make([]any, 0, len(entries))
	for _, e := range entries {
		keyed = append(keyed, map[string]any{
			"key":   e.key,
			"value": e.value,
		})
	}
	return keyed, nil
}

// 9. Multi-Value Register
type multiValueWrite struct {
	opID string
	val  string
}

type multiValueAccumulator struct {
	field  string
	reach  ReachOracle
	writes []multiValueWrite
}

func newMultiValueAccumulator(rule Rule, reach ReachOracle) (Accumulator, error) {
	return &multiValueAccumulator{
		field: rule.Field,
		reach: reach,
	}, nil
}

func (a *multiValueAccumulator) Apply(rule Rule, op codec.Op, body map[string]any, _ map[string]json.RawMessage) error {
	raw, ok := body[rule.Field]
	if !ok || raw == nil {
		return nil
	}
	if s, ok := raw.(string); ok {
		a.writes = append(a.writes, multiValueWrite{opID: op.ID, val: s})
	}
	return nil
}

func (a *multiValueAccumulator) HasValue() bool {
	return len(a.writes) > 0
}

func (a *multiValueAccumulator) Result() (any, error) {
	if len(a.writes) == 0 {
		return "", nil
	}
	var maximal []multiValueWrite
	for i, w1 := range a.writes {
		superseded := false
		for j, w2 := range a.writes {
			if i != j && a.reach.IsAncestor(w1.opID, w2.opID) {
				superseded = true
				break
			}
		}
		if !superseded {
			maximal = append(maximal, w1)
		}
	}
	seen := make(map[string]bool)
	var vals []string
	for _, w := range maximal {
		if !seen[w.val] {
			seen[w.val] = true
			vals = append(vals, w.val)
		}
	}
	sort.Strings(vals)
	if len(vals) == 1 {
		return vals[0], nil
	}
	res := make([]any, len(vals))
	for i, v := range vals {
		res[i] = v
	}
	return res, nil
}
