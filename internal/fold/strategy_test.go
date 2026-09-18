package fold_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/fold"
)

type dummyOracle struct{}

func (dummyOracle) IsAncestor(ancestor, descendant string) bool { return false }

func TestLWWNormalizationVocabularyBlind(t *testing.T) {
	tests := []struct {
		name      string
		rule      fold.Rule
		input     string
		wantValue any
	}{
		{
			name: "custom op and field with person value normalization",
			rule: fold.Rule{
				OpType:    "arbitrary_op",
				OpVersion: 1,
				Field:     "custom_actor",
				Strategy:  "lww",
				ValueType: "person-ref",
			},
			input:     "email:Alice@Example.COM",
			wantValue: "email:alice@example.com",
		},
		{
			name: "custom op and field without normalization preserves value",
			rule: fold.Rule{
				OpType:    "arbitrary_op",
				OpVersion: 1,
				Field:     "custom_actor",
				Strategy:  "lww",
			},
			input:     "email:Alice@Example.COM",
			wantValue: "email:Alice@Example.COM",
		},
		{
			name: "bare email lowercased when normalization requested",
			rule: fold.Rule{
				OpType:    "custom_assignee",
				OpVersion: 1,
				Field:     "assignee",
				Strategy:  "lww",
				ValueType: "person-ref",
			},
			input:     "Alice@Example.COM",
			wantValue: "alice@example.com",
		},
		{
			name: "bare email preserved verbatim when normalization absent",
			rule: fold.Rule{
				OpType:    "custom_assignee",
				OpVersion: 1,
				Field:     "assignee",
				Strategy:  "lww",
			},
			input:     "Alice@Example.COM",
			wantValue: "Alice@Example.COM",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			acc, err := fold.NewAccumulator(tc.rule, dummyOracle{})
			if err != nil {
				t.Fatalf("NewAccumulator failed: %v", err)
			}
			op := codec.Op{ID: "c1", Envelope: codec.Envelope{OpType: tc.rule.OpType, OpVersion: tc.rule.OpVersion}}
			body := map[string]any{tc.rule.Field: tc.input}
			if err := acc.Apply(tc.rule, op, body, nil); err != nil {
				t.Fatalf("Apply failed: %v", err)
			}
			res, err := acc.Result()
			if err != nil {
				t.Fatalf("Result failed: %v", err)
			}
			if res != tc.wantValue {
				t.Errorf("got %v, want %v", res, tc.wantValue)
			}
		})
	}
}

func TestSetObservedRemoveNormalizationVocabularyBlind(t *testing.T) {
	ruleWithNorm := fold.Rule{
		OpType:    "custom_team",
		OpVersion: 1,
		Field:     "members",
		Strategy:  "set-observed-remove",
		ValueType: "person-ref",
	}
	accWithNorm, err := fold.NewAccumulator(ruleWithNorm, dummyOracle{})
	if err != nil {
		t.Fatalf("NewAccumulator failed: %v", err)
	}
	op1 := codec.Op{ID: "c1", Envelope: codec.Envelope{OpType: "custom_team", OpVersion: 1}}
	body1 := map[string]any{
		"members": map[string]any{
			"add": []any{"email:Alice@Example.COM", "   ", "email:Bob@Example.COM"},
		},
	}
	if err := accWithNorm.Apply(ruleWithNorm, op1, body1, nil); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	resWithNorm, err := accWithNorm.Result()
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	wantWithNorm := []string{"email:alice@example.com", "email:bob@example.com"}
	if !reflect.DeepEqual(resWithNorm, wantWithNorm) {
		t.Errorf("got %v, want %v", resWithNorm, wantWithNorm)
	}

	// Without normalization: whitespace-only items are kept and emails are not lowercased
	ruleWithoutNorm := fold.Rule{
		OpType:    "custom_team",
		OpVersion: 1,
		Field:     "members",
		Strategy:  "set-observed-remove",
	}
	accWithoutNorm, err := fold.NewAccumulator(ruleWithoutNorm, dummyOracle{})
	if err != nil {
		t.Fatalf("NewAccumulator failed: %v", err)
	}
	if err := accWithoutNorm.Apply(ruleWithoutNorm, op1, body1, nil); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	resWithoutNorm, err := accWithoutNorm.Result()
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	wantWithoutNorm := []string{"   ", "email:Alice@Example.COM", "email:Bob@Example.COM"}
	if !reflect.DeepEqual(resWithoutNorm, wantWithoutNorm) {
		t.Errorf("got %v, want %v", resWithoutNorm, wantWithoutNorm)
	}
}

func TestSetUnionNormalizationVocabularyBlind(t *testing.T) {
	ruleWithNorm := fold.Rule{
		OpType:    "custom_tags",
		OpVersion: 1,
		Field:     "authors",
		Strategy:  "set-union",
		ValueType: "person-ref",
	}
	accWithNorm, err := fold.NewAccumulator(ruleWithNorm, dummyOracle{})
	if err != nil {
		t.Fatalf("NewAccumulator failed: %v", err)
	}
	op1 := codec.Op{ID: "c1", Envelope: codec.Envelope{OpType: "custom_tags", OpVersion: 1}}
	body1 := map[string]any{
		"authors": []any{"email:Alice@Example.COM", "   ", "email:Bob@Example.COM"},
	}
	if err := accWithNorm.Apply(ruleWithNorm, op1, body1, nil); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	resWithNorm, err := accWithNorm.Result()
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	wantWithNorm := []string{"email:alice@example.com", "email:bob@example.com"}
	if !reflect.DeepEqual(resWithNorm, wantWithNorm) {
		t.Errorf("got %v, want %v", resWithNorm, wantWithNorm)
	}

	ruleWithoutNorm := fold.Rule{
		OpType:    "custom_tags",
		OpVersion: 1,
		Field:     "authors",
		Strategy:  "set-union",
	}
	accWithoutNorm, err := fold.NewAccumulator(ruleWithoutNorm, dummyOracle{})
	if err != nil {
		t.Fatalf("NewAccumulator failed: %v", err)
	}
	if err := accWithoutNorm.Apply(ruleWithoutNorm, op1, body1, nil); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	resWithoutNorm, err := accWithoutNorm.Result()
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	wantWithoutNorm := []string{"   ", "email:Alice@Example.COM", "email:Bob@Example.COM"}
	if !reflect.DeepEqual(resWithoutNorm, wantWithoutNorm) {
		t.Errorf("got %v, want %v", resWithoutNorm, wantWithoutNorm)
	}
}

func TestKeyedLWWNormalizationVocabularyBlind(t *testing.T) {
	ruleWithNorm := fold.Rule{
		OpType:    "custom_vote",
		OpVersion: 1,
		Field:     "voter",
		Strategy:  "keyed-lww",
		Key:       []string{"voter", "topic"},
		ValueType: "person-ref",
		KeyTypes: map[string]string{
			"voter": "person-ref",
			"topic": "string",
		},
	}
	accWithNorm, err := fold.NewAccumulator(ruleWithNorm, dummyOracle{})
	if err != nil {
		t.Fatalf("NewAccumulator failed: %v", err)
	}
	op1 := codec.Op{ID: "c1", Envelope: codec.Envelope{OpType: "custom_vote", OpVersion: 1}}
	body1 := map[string]any{
		"voter": "email:Alice@Example.COM",
		"topic": "Topic-One",
	}
	if err := accWithNorm.Apply(ruleWithNorm, op1, body1, nil); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	resWithNorm, err := accWithNorm.Result()
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	wantWithNorm := []any{
		map[string]any{
			"key":   []string{"email:alice@example.com", "Topic-One"},
			"value": "email:alice@example.com",
		},
	}
	if !reflect.DeepEqual(resWithNorm, wantWithNorm) {
		t.Errorf("got %v, want %v", resWithNorm, wantWithNorm)
	}

	ruleWithoutNorm := fold.Rule{
		OpType:    "custom_vote",
		OpVersion: 1,
		Field:     "voter",
		Strategy:  "keyed-lww",
		Key:       []string{"voter", "topic"},
	}
	accWithoutNorm, err := fold.NewAccumulator(ruleWithoutNorm, dummyOracle{})
	if err != nil {
		t.Fatalf("NewAccumulator failed: %v", err)
	}
	if err := accWithoutNorm.Apply(ruleWithoutNorm, op1, body1, nil); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	resWithoutNorm, err := accWithoutNorm.Result()
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	wantWithoutNorm := []any{
		map[string]any{
			"key":   []string{"email:Alice@Example.COM", "Topic-One"},
			"value": "email:Alice@Example.COM",
		},
	}
	if !reflect.DeepEqual(resWithoutNorm, wantWithoutNorm) {
		t.Errorf("got %v, want %v", resWithoutNorm, wantWithoutNorm)
	}
}

// TestCreateOnceNormalizationVocabularyBlind pins WRIT-190's reducer fix:
// create-once is a scalar position by spec/value-types.md §Normalization's
// own definition ("NormalizesValue() ≡ scalar or keyed-register position"),
// so a person-ref value normalizes here exactly as it does under lww. Before
// this fix, an unnormalized "Alice@Example.com" permanently beat
// "alice@example.com" under first-write-wins — the exact failure person
// normalization exists to prevent.
func TestCreateOnceNormalizationVocabularyBlind(t *testing.T) {
	tests := []struct {
		name      string
		rule      fold.Rule
		input     string
		wantValue any
	}{
		{
			name: "custom op and field with person value normalization",
			rule: fold.Rule{
				OpType:    "arbitrary_op",
				OpVersion: 1,
				Field:     "custom_actor",
				Strategy:  "create-once",
				ValueType: "person-ref",
			},
			input:     "email:Alice@Example.COM",
			wantValue: "email:alice@example.com",
		},
		{
			name: "custom op and field without normalization preserves value",
			rule: fold.Rule{
				OpType:    "arbitrary_op",
				OpVersion: 1,
				Field:     "custom_actor",
				Strategy:  "create-once",
			},
			input:     "email:Alice@Example.COM",
			wantValue: "email:Alice@Example.COM",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			acc, err := fold.NewAccumulator(tc.rule, dummyOracle{})
			if err != nil {
				t.Fatalf("NewAccumulator failed: %v", err)
			}
			op := codec.Op{ID: "c1", Envelope: codec.Envelope{OpType: tc.rule.OpType, OpVersion: tc.rule.OpVersion}}
			body := map[string]any{tc.rule.Field: tc.input}
			// rawBody nil exercises the body-only fallback path.
			if err := acc.Apply(tc.rule, op, body, nil); err != nil {
				t.Fatalf("Apply failed: %v", err)
			}
			res, err := acc.Result()
			if err != nil {
				t.Fatalf("Result failed: %v", err)
			}
			if res != tc.wantValue {
				t.Errorf("got %v, want %v", res, tc.wantValue)
			}
		})
	}

	// The rawBody path (byte-exact preservation for every non-normalizing
	// value) also normalizes a person-ref write.
	rule := fold.Rule{
		OpType:    "arbitrary_op",
		OpVersion: 1,
		Field:     "custom_actor",
		Strategy:  "create-once",
		ValueType: "person-ref",
	}
	acc, err := fold.NewAccumulator(rule, dummyOracle{})
	if err != nil {
		t.Fatalf("NewAccumulator failed: %v", err)
	}
	op := codec.Op{ID: "c1", Envelope: codec.Envelope{OpType: rule.OpType, OpVersion: rule.OpVersion}}
	body := map[string]any{"custom_actor": "email:Alice@Example.COM"}
	rawBody := map[string]json.RawMessage{"custom_actor": json.RawMessage(`"email:Alice@Example.COM"`)}
	if err := acc.Apply(rule, op, body, rawBody); err != nil {
		t.Fatalf("Apply failed: %v", err)
	}
	res, err := acc.Result()
	if err != nil {
		t.Fatalf("Result failed: %v", err)
	}
	raw, ok := res.(json.RawMessage)
	if !ok {
		t.Fatalf("expected json.RawMessage result, got %T: %v", res, res)
	}
	if string(raw) != `"email:alice@example.com"` {
		t.Errorf("got %s, want %q", raw, `"email:alice@example.com"`)
	}
}

// TestMultiValueNormalizationVocabularyBlind pins WRIT-190's reducer fix:
// multi-value is a scalar position by spec/value-types.md §Normalization's
// own definition, so a person-ref value normalizes here exactly as it does
// under lww and create-once.
func TestMultiValueNormalizationVocabularyBlind(t *testing.T) {
	tests := []struct {
		name      string
		rule      fold.Rule
		input     string
		wantValue any
	}{
		{
			name: "custom op and field with person value normalization",
			rule: fold.Rule{
				OpType:    "arbitrary_op",
				OpVersion: 1,
				Field:     "custom_actor",
				Strategy:  "multi-value",
				ValueType: "person-ref",
			},
			input:     "email:Alice@Example.COM",
			wantValue: "email:alice@example.com",
		},
		{
			name: "custom op and field without normalization preserves value",
			rule: fold.Rule{
				OpType:    "arbitrary_op",
				OpVersion: 1,
				Field:     "custom_actor",
				Strategy:  "multi-value",
			},
			input:     "email:Alice@Example.COM",
			wantValue: "email:Alice@Example.COM",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			acc, err := fold.NewAccumulator(tc.rule, dummyOracle{})
			if err != nil {
				t.Fatalf("NewAccumulator failed: %v", err)
			}
			op := codec.Op{ID: "c1", Envelope: codec.Envelope{OpType: tc.rule.OpType, OpVersion: tc.rule.OpVersion}}
			body := map[string]any{tc.rule.Field: tc.input}
			if err := acc.Apply(tc.rule, op, body, nil); err != nil {
				t.Fatalf("Apply failed: %v", err)
			}
			res, err := acc.Result()
			if err != nil {
				t.Fatalf("Result failed: %v", err)
			}
			if res != tc.wantValue {
				t.Errorf("got %v, want %v", res, tc.wantValue)
			}
		})
	}
}

// TestFoldSetObservedRemoveIndexedRemoves pins add-wins presence through
// the item-indexed Result rewrite (WRIT-268 edit 3), end to end through
// fold.Fold rather than against the accumulator directly, so ancestry
// comes from the real ordering and lazy reachability oracle rather than a
// dummyOracle stub. Many adds of distinct items exercise the common case
// the index makes linear: most items have no removes at all. One item
// gets a remove that causally lands (the remove's op is a descendant of
// its add), and it alone is dropped from presence.
func TestFoldSetObservedRemoveIndexedRemoves(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rules := []fold.Rule{
		{OpType: "add-member", OpVersion: 1, Field: "members", Strategy: "set-observed-remove"},
		{OpType: "remove-member", OpVersion: 1, Field: "members", Strategy: "set-observed-remove"},
	}

	const n = 20
	ops := make([]codec.Op, 0, n+1)
	for i := 0; i < n; i++ {
		item := fmt.Sprintf("item-%02d", i)
		ops = append(ops, codec.Op{
			ID: fmt.Sprintf("add-%02d", i),
			Envelope: codec.Envelope{
				ObjectID:   "obj-1",
				ObjectType: "acme.roster",
				OpType:     "add-member",
				OpVersion:  1,
				Body:       []byte(fmt.Sprintf(`{"members":%q}`, item)),
			},
			Author: codec.Identity{When: baseTime.Add(time.Duration(i) * time.Minute)},
		})
	}
	// item-00's remove causally follows its add: a genuine descendant, so
	// it lands and item-00 drops out of presence.
	ops = append(ops, codec.Op{
		ID: "remove-00",
		Envelope: codec.Envelope{
			ObjectID:   "obj-1",
			ObjectType: "acme.roster",
			OpType:     "remove-member",
			OpVersion:  1,
			Body:       []byte(`{"members":"item-00"}`),
		},
		Parents: []string{"add-00"},
		Author:  codec.Identity{When: baseTime.Add(time.Duration(n) * time.Minute)},
	})

	state, err := fold.Fold(ops, rules)
	if err != nil {
		t.Fatalf("Fold failed: %v", err)
	}

	got, ok := state.State["members"].([]string)
	if !ok {
		t.Fatalf("members field is %T, want []string", state.State["members"])
	}

	want := make([]string, 0, n-1)
	for i := 1; i < n; i++ {
		want = append(want, fmt.Sprintf("item-%02d", i))
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (item-00 should be removed, the rest present)", got, want)
	}
}

// TestFoldSetObservedRemoveConcurrentAddWins pins add-wins semantics for a
// remove that is concurrent with its add — neither causally precedes the
// other — through the same item-indexed Result path: the item must stay
// present, since spec/fold.md §5.4 requires ∀r. removes(r,x) → a ⊀ r, and
// a concurrent remove is not ≺ (ancestor of) anything.
func TestFoldSetObservedRemoveConcurrentAddWins(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rules := []fold.Rule{
		{OpType: "add-member", OpVersion: 1, Field: "members", Strategy: "set-observed-remove"},
		{OpType: "remove-member", OpVersion: 1, Field: "members", Strategy: "set-observed-remove"},
	}

	ops := []codec.Op{
		{
			ID: "add-alice",
			Envelope: codec.Envelope{
				ObjectID:   "obj-1",
				ObjectType: "acme.roster",
				OpType:     "add-member",
				OpVersion:  1,
				Body:       []byte(`{"members":"alice"}`),
			},
			Author: codec.Identity{When: baseTime},
		},
		{
			// No Parents: concurrent with add-alice, not a descendant of it,
			// so it must not remove alice under add-wins.
			ID: "remove-alice-concurrent",
			Envelope: codec.Envelope{
				ObjectID:   "obj-1",
				ObjectType: "acme.roster",
				OpType:     "remove-member",
				OpVersion:  1,
				Body:       []byte(`{"members":"alice"}`),
			},
			Author: codec.Identity{When: baseTime.Add(time.Minute)},
		},
	}

	state, err := fold.Fold(ops, rules)
	if err != nil {
		t.Fatalf("Fold failed: %v", err)
	}

	got, ok := state.State["members"].([]string)
	if !ok {
		t.Fatalf("members field is %T, want []string", state.State["members"])
	}
	want := []string{"alice"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v (concurrent remove must not beat the add)", got, want)
	}
}
