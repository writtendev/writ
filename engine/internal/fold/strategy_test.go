package fold_test

import (
	"reflect"
	"testing"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
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
