package state_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	s "github.com/writtendev/writ/engine/state"
	"github.com/writtendev/writ/spec"
)

func TestLabelRulesDriftGuard(t *testing.T) {
	allRules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules failed: %v", err)
	}

	var expectedRules []s.Rule
	for _, r := range allRules {
		if r.Vocabulary == "label" {
			expectedRules = append(expectedRules, s.Rule{
				OpType:    r.OpType,
				OpVersion: r.OpVersion,
				Field:     r.Field,
				Target:    r.Target,
				Strategy:  r.Strategy,
				Key:       r.Key,
				Lattice:   r.Lattice,
				ValueType: r.ValueType,
				Enum:      r.Enum,
				MaxLength: r.MaxLength,
				KeyTypes:  r.KeyTypes,
			})
		}
	}

	builtIn := s.LabelRules()
	if !reflect.DeepEqual(builtIn, expectedRules) {
		t.Fatalf("LabelRules() drifted from published label field-rules.json:\n got:  %+v\n want: %+v", builtIn, expectedRules)
	}
}

func TestFoldLabelEmpty(t *testing.T) {
	state, err := s.FoldLabel(nil)
	if err != nil {
		t.Fatalf("FoldLabel(nil) returned error: %v", err)
	}
	if !reflect.DeepEqual(state, s.Label{}) {
		t.Fatalf("expected empty Label, got %+v", state)
	}
}

func TestFoldLabelLifecycle(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	createOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "lbl-1",
			ObjectType: "label",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"name":"bug","color":"#d73a4a","description":"Something is broken"}`),
		},
		ID: "c1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	updateOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "lbl-1",
			ObjectType: "label",
			OpType:     "update",
			OpVersion:  1,
			Body:       json.RawMessage(`{"name":"defect","color":"#e2b93c"}`),
		},
		ID:      "u1",
		Parents: []string{"c1"},
		Author: codec.Identity{
			Email: "bob@example.com",
			When:  now.Add(time.Minute),
		},
	}

	lbl, err := s.FoldLabel([]codec.Op{createOp, updateOp})
	if err != nil {
		t.Fatalf("FoldLabel failed: %v", err)
	}

	if lbl.Name != "defect" {
		t.Errorf("got name %q, want %q", lbl.Name, "defect")
	}
	if lbl.Color != "#e2b93c" {
		t.Errorf("got color %q, want %q", lbl.Color, "#e2b93c")
	}
	if lbl.Description != "Something is broken" {
		t.Errorf("got description %q, want %q", lbl.Description, "Something is broken")
	}
	if len(lbl.UnknownOps) != 0 {
		t.Errorf("expected 0 unknown ops, got %d", len(lbl.UnknownOps))
	}
}

func TestFoldLabelConcurrentLWW(t *testing.T) {
	baseTime := time.Unix(100, 0).UTC()

	createOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "lbl-conc",
			ObjectType: "label",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"name":"Initial","color":"#111111"}`),
		},
		ID: "c1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  baseTime,
		},
	}

	// Concurrent op 1: Alice updates color at T+10
	opAlice := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "lbl-conc",
			ObjectType: "label",
			OpType:     "update",
			OpVersion:  1,
			Body:       json.RawMessage(`{"color":"#aaaaaa"}`),
		},
		ID:      "a1",
		Parents: []string{"c1"},
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  baseTime.Add(10 * time.Second),
		},
	}

	// Concurrent op 2: Bob updates color and name at T+20 (Bob wins color, updates name)
	opBob := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "lbl-conc",
			ObjectType: "label",
			OpType:     "update",
			OpVersion:  1,
			Body:       json.RawMessage(`{"name":"BobName","color":"#bbbbbb"}`),
		},
		ID:      "b1",
		Parents: []string{"c1"},
		Author: codec.Identity{
			Email: "bob@example.com",
			When:  baseTime.Add(20 * time.Second),
		},
	}

	// Order: create, then Alice, then Bob
	lbl1, err := s.FoldLabel([]codec.Op{createOp, opAlice, opBob})
	if err != nil {
		t.Fatalf("FoldLabel 1 failed: %v", err)
	}

	// Permutation: create, then Bob, then Alice
	lbl2, err := s.FoldLabel([]codec.Op{createOp, opBob, opAlice})
	if err != nil {
		t.Fatalf("FoldLabel 2 failed: %v", err)
	}

	if !reflect.DeepEqual(lbl1, lbl2) {
		t.Fatalf("commutativity violation:\n lbl1: %+v\n lbl2: %+v", lbl1, lbl2)
	}
	if lbl1.Color != "#bbbbbb" {
		t.Errorf("got color %q, want #bbbbbb (Bob had higher t*)", lbl1.Color)
	}
	if lbl1.Name != "BobName" {
		t.Errorf("got name %q, want BobName", lbl1.Name)
	}
}

func TestFoldLabelUninterpretable(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	validOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "lbl-unrec",
			ObjectType: "label",
			OpType:     "create",
			OpVersion:  1,
			Body:       json.RawMessage(`{"name":"test"}`),
		},
		ID: "c1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	unknownOp := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   "lbl-unrec",
			ObjectType: "label",
			OpType:     "archive",
			OpVersion:  1,
			Body:       json.RawMessage(`{"archived":true}`),
		},
		ID:      "u1",
		Parents: []string{"c1"},
		Author: codec.Identity{
			Email: "bob@example.com",
			When:  now.Add(time.Minute),
		},
	}

	lbl, err := s.FoldLabel([]codec.Op{validOp, unknownOp})
	if err != nil {
		t.Fatalf("FoldLabel failed: %v", err)
	}

	if lbl.Name != "test" {
		t.Errorf("got name %q, want test", lbl.Name)
	}
	if len(lbl.UnknownOps) != 1 {
		t.Fatalf("expected 1 unknown op, got %d", len(lbl.UnknownOps))
	}
	if lbl.UnknownOps[0].OpType != "archive" {
		t.Errorf("got unknown op type %q, want archive", lbl.UnknownOps[0].OpType)
	}
}
