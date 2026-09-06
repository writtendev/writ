package spec

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// KnownCatalogueStrategies is the closed set of per-field merge strategies
// defined in spec/fold.md.
var KnownCatalogueStrategies = map[string]bool{
	"lww":                 true,
	"create-once":         true,
	"set-union":           true,
	"set-observed-remove": true,
	"append":              true,
	"tombstone":           true,
	"lattice":             true,
	"keyed-lww":           true,
	"multi-value":         true,
}

// OrderOp represents a single abstract operation in an ordering vector.
type OrderOp struct {
	ID       string   `json:"id"`
	Parents  []string `json:"parents"`
	Time     int64    `json:"time"`
	ObjectID string   `json:"object_id"`
}

// OrderVector represents one ordering test case under testdata/fold/order/.
type OrderVector struct {
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	ObjectID      string    `json:"object_id"`
	Ops           []OrderOp `json:"ops"`
	ExpectedOrder []string  `json:"expected_order"`
}

// StrategyConfig specifies the merge strategy and optional parameters (e.g. lattice elements)
// for a field in a merge vector.
//
// Field, OpType, OpVersion, and Target are optional and exist for the sole
// case the map key cannot express: two rules for the *same* op body field
// scoped to different (op_type, op_version), as spec/fold.md §5's target
// remedy requires (WRIT-186 §Evolution). A vector's `fields` map is keyed by
// an arbitrary label in that case rather than by the field name itself, and
// Field supplies the real body field every such label shares. Every existing
// vector omits all four, and the map key remains the field name for them,
// unchanged.
type StrategyConfig struct {
	Strategy  string            `json:"strategy"`
	Lattice   []string          `json:"lattice,omitempty"`
	Key       []string          `json:"key,omitempty"`
	ValueType string            `json:"value_type,omitempty"`
	Enum      []string          `json:"enum,omitempty"`
	MaxLength int64             `json:"max_length,omitempty"`
	KeyTypes  map[string]string `json:"key_types,omitempty"`
	// Field overrides the op body field name this rule reads; defaults to the
	// vector's `fields` map key when empty.
	Field string `json:"field,omitempty"`
	// OpType and OpVersion scope the rule to one operation version, as a
	// production Rule does; empty/zero match any op, as before.
	OpType    string `json:"op_type,omitempty"`
	OpVersion int64  `json:"op_version,omitempty"`
	// Target names the state key this rule's value lands under, defaulting to
	// the resolved Field when empty (spec/fold.md §5's TargetKey).
	Target string `json:"target,omitempty"`
	// ObjectType scopes the rule to one object type (spec/fold.md §5), as a
	// production FieldRule/Rule does; empty matches any op's object type, as
	// before. It exists so a merge vector can pin the object-type scoping
	// clause in opMatchesRule itself: two StrategyConfig entries sharing a
	// Target and Field but declaring different ObjectType are exactly the
	// cross-vocabulary target collision spec.FieldRules() produces today
	// (the "labels" target, document vs review/issue) and must not merge into
	// one accumulator.
	ObjectType string `json:"object_type,omitempty"`
}

// MergeAuthor represents the author identity on a commit carrier for an operation.
type MergeAuthor struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

// MergeOp represents a single synthetic operation in a merge vector.
type MergeOp struct {
	ID         string         `json:"id"`
	Parents    []string       `json:"parents"`
	Time       int64          `json:"time"`
	ObjectID   string         `json:"object_id"`
	ObjectType string         `json:"object_type,omitempty"`
	OpType     string         `json:"op_type,omitempty"`
	OpVersion  int64          `json:"op_version,omitempty"`
	Author     MergeAuthor    `json:"author,omitempty"`
	Body       map[string]any `json:"body"`
}

// MergeVector represents one merge strategy test case under testdata/fold/merge/.
type MergeVector struct {
	Name          string                    `json:"name"`
	Description   string                    `json:"description,omitempty"`
	ObjectID      string                    `json:"object_id"`
	Fields        map[string]StrategyConfig `json:"fields"`
	Ops           []MergeOp                 `json:"ops"`
	ExpectedState map[string]any            `json:"expected_state"`
	// ExpectedUnknownOps lists, in total order, the ids of ops the fold must
	// quarantine rather than reduce (spec/fold.md §7, §7.1). Omitted where no
	// op in the vector is quarantined.
	ExpectedUnknownOps []string `json:"expected_unknown_ops,omitempty"`
}

// OrderVectors loads all ordering test vectors from testdata/fold/order/
// and validates their structure.
func OrderVectors() ([]OrderVector, error) {
	const dir = "testdata/fold/order"
	entries, err := FS.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("spec: reading order vector dir: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("spec: no order vectors found in %s", dir)
	}

	var vectors []OrderVector
	seen := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		filePath := path.Join(dir, entry.Name())
		raw, err := FS.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("spec: reading %s: %w", filePath, err)
		}
		var vec OrderVector
		if err := json.Unmarshal(raw, &vec); err != nil {
			return nil, fmt.Errorf("spec: parsing %s: %w", filePath, err)
		}
		if vec.Name == "" {
			return nil, fmt.Errorf("spec: %s has empty name", filePath)
		}
		if seen[vec.Name] {
			return nil, fmt.Errorf("spec: duplicate order vector name %q in %s", vec.Name, filePath)
		}
		seen[vec.Name] = true

		if vec.ObjectID == "" {
			return nil, fmt.Errorf("spec: order vector %q has empty object_id", vec.Name)
		}
		if len(vec.Ops) == 0 {
			return nil, fmt.Errorf("spec: order vector %q has no ops", vec.Name)
		}

		opMap := make(map[string]OrderOp, len(vec.Ops))
		matchingIDs := make(map[string]bool)
		for _, op := range vec.Ops {
			if op.ID == "" {
				return nil, fmt.Errorf("spec: order vector %q has op with empty id", vec.Name)
			}
			if _, exists := opMap[op.ID]; exists {
				return nil, fmt.Errorf("spec: order vector %q has duplicate op id %q", vec.Name, op.ID)
			}
			opMap[op.ID] = op
			if op.ObjectID == vec.ObjectID {
				matchingIDs[op.ID] = true
			}
		}

		if len(vec.ExpectedOrder) != len(matchingIDs) {
			return nil, fmt.Errorf("spec: order vector %q expected_order len %d != matching ops count %d",
				vec.Name, len(vec.ExpectedOrder), len(matchingIDs))
		}
		expectedSeen := make(map[string]bool, len(vec.ExpectedOrder))
		for _, id := range vec.ExpectedOrder {
			if !matchingIDs[id] {
				return nil, fmt.Errorf("spec: order vector %q expected_order contains unknown or mismatched op id %q", vec.Name, id)
			}
			if expectedSeen[id] {
				return nil, fmt.Errorf("spec: order vector %q expected_order contains duplicate op id %q", vec.Name, id)
			}
			expectedSeen[id] = true
		}

		// Acyclicity check in restricted DAG
		if err := validateAcyclicOrder(vec.Ops, vec.ObjectID); err != nil {
			return nil, fmt.Errorf("spec: order vector %q restricted DAG cycle: %w", vec.Name, err)
		}

		vectors = append(vectors, vec)
	}

	return vectors, nil
}

// MergeVectors loads all merge test vectors from testdata/fold/merge/
// and validates their structure.
func MergeVectors() ([]MergeVector, error) {
	const dir = "testdata/fold/merge"
	entries, err := FS.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("spec: reading merge vector dir: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("spec: no merge vectors found in %s", dir)
	}

	var vectors []MergeVector
	seen := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		filePath := path.Join(dir, entry.Name())
		raw, err := FS.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("spec: reading %s: %w", filePath, err)
		}
		var vec MergeVector
		if err := json.Unmarshal(raw, &vec); err != nil {
			return nil, fmt.Errorf("spec: parsing %s: %w", filePath, err)
		}
		if vec.Name == "" {
			return nil, fmt.Errorf("spec: %s has empty name", filePath)
		}
		if seen[vec.Name] {
			return nil, fmt.Errorf("spec: duplicate merge vector name %q in %s", vec.Name, filePath)
		}
		seen[vec.Name] = true

		if vec.ObjectID == "" {
			return nil, fmt.Errorf("spec: merge vector %q has empty object_id", vec.Name)
		}
		if len(vec.Fields) == 0 {
			return nil, fmt.Errorf("spec: merge vector %q has no declared fields", vec.Name)
		}
		for fieldName, cfg := range vec.Fields {
			if !KnownCatalogueStrategies[cfg.Strategy] {
				return nil, fmt.Errorf("spec: merge vector %q field %q has unknown strategy %q", vec.Name, fieldName, cfg.Strategy)
			}
			if cfg.Strategy == "lattice" && len(cfg.Lattice) == 0 {
				return nil, fmt.Errorf("spec: merge vector %q field %q uses lattice strategy but defines no lattice elements", vec.Name, fieldName)
			}
			if cfg.Strategy == "keyed-lww" && len(cfg.Key) == 0 {
				return nil, fmt.Errorf("spec: merge vector %q field %q uses keyed-lww strategy but declares no key", vec.Name, fieldName)
			}
		}
		if len(vec.Ops) == 0 {
			return nil, fmt.Errorf("spec: merge vector %q has no ops", vec.Name)
		}

		opMap := make(map[string]bool, len(vec.Ops))
		var orderOps []OrderOp
		for _, op := range vec.Ops {
			if op.ID == "" {
				return nil, fmt.Errorf("spec: merge vector %q has op with empty id", vec.Name)
			}
			if opMap[op.ID] {
				return nil, fmt.Errorf("spec: merge vector %q has duplicate op id %q", vec.Name, op.ID)
			}
			opMap[op.ID] = true
			orderOps = append(orderOps, OrderOp{
				ID:       op.ID,
				Parents:  op.Parents,
				Time:     op.Time,
				ObjectID: op.ObjectID,
			})
		}

		for _, id := range vec.ExpectedUnknownOps {
			if !opMap[id] {
				return nil, fmt.Errorf("spec: merge vector %q expected_unknown_ops names unknown op id %q", vec.Name, id)
			}
		}

		// Acyclicity check in restricted DAG
		if err := validateAcyclicOrder(orderOps, vec.ObjectID); err != nil {
			return nil, fmt.Errorf("spec: merge vector %q restricted DAG cycle: %w", vec.Name, err)
		}

		vectors = append(vectors, vec)
	}

	return vectors, nil
}

func validateAcyclicOrder(ops []OrderOp, objectID string) error {
	inSet := make(map[string]bool)
	for _, op := range ops {
		if op.ObjectID == objectID {
			inSet[op.ID] = true
		}
	}

	// DFS cycle detection: 0 = unvisited, 1 = visiting, 2 = visited
	state := make(map[string]int)
	parentsMap := make(map[string][]string)
	for _, op := range ops {
		if inSet[op.ID] {
			var pList []string
			for _, p := range op.Parents {
				if inSet[p] {
					pList = append(pList, p)
				}
			}
			parentsMap[op.ID] = pList
		}
	}

	var visit func(node string) error
	visit = func(node string) error {
		if state[node] == 1 {
			return fmt.Errorf("cycle detected involving op %q", node)
		}
		if state[node] == 2 {
			return nil
		}
		state[node] = 1
		for _, p := range parentsMap[node] {
			if err := visit(p); err != nil {
				return err
			}
		}
		state[node] = 2
		return nil
	}

	for id := range inSet {
		if state[id] == 0 {
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	return nil
}
