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

func TestSettingsRulesDriftGuard(t *testing.T) {
	allRules, err := spec.FieldRules()
	if err != nil {
		t.Fatalf("spec.FieldRules failed: %v", err)
	}

	var expectedRules []s.Rule
	for _, r := range allRules {
		if r.Vocabulary == "settings" {
			expectedRules = append(expectedRules, s.Rule{
				OpType:    r.OpType,
				OpVersion: r.OpVersion,
				Field:     r.Field,
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

	builtIn := s.SettingsRules()
	if !reflect.DeepEqual(builtIn, expectedRules) {
		t.Fatalf("SettingsRules() drifted from published settings field-rules.json:\n got:  %+v\n want: %+v", builtIn, expectedRules)
	}
}

func TestFoldSettingsEmpty(t *testing.T) {
	state, err := s.FoldSettings(nil)
	if err != nil {
		t.Fatalf("FoldSettings(nil) returned error: %v", err)
	}
	want := s.DefaultSettings()
	if !reflect.DeepEqual(state, want) {
		t.Fatalf("expected DefaultSettings, got %+v", state)
	}
}

func TestFoldSettingsLifecycle(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	set1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   s.DefaultSettingsObjectID,
			ObjectType: "settings",
			OpType:     "set",
			OpVersion:  1,
			Body: json.RawMessage(`{
				"name": "Acme Workspace",
				"identifier": "ACME",
				"timezone": "America/New_York",
				"estimate_scale": "t-shirt",
				"allow_zero_estimates": true
			}`),
		},
		ID: "c1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	set2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   s.DefaultSettingsObjectID,
			ObjectType: "settings",
			OpType:     "set",
			OpVersion:  1,
			Body: json.RawMessage(`{
				"cycles_enabled": true,
				"cycle_duration_weeks": 3,
				"cycle_start_day": 3,
				"cycle_cooldown_weeks": 1,
				"triage_enabled": true,
				"name": "Acme Global"
			}`),
		},
		ID:      "c2",
		Parents: []string{"c1"},
		Author: codec.Identity{
			Email: "bob@example.com",
			When:  now.Add(time.Minute),
		},
	}

	state, err := s.FoldSettings([]codec.Op{set1, set2})
	if err != nil {
		t.Fatalf("FoldSettings returned error: %v", err)
	}

	if state.Name != "Acme Global" {
		t.Errorf("state.Name = %q, want 'Acme Global'", state.Name)
	}
	if state.Identifier != "ACME" {
		t.Errorf("state.Identifier = %q, want 'ACME'", state.Identifier)
	}
	if state.Timezone != "America/New_York" {
		t.Errorf("state.Timezone = %q, want 'America/New_York'", state.Timezone)
	}
	if state.EstimateScale != "t-shirt" {
		t.Errorf("state.EstimateScale = %q, want 't-shirt'", state.EstimateScale)
	}
	if !state.AllowZeroEstimates {
		t.Errorf("state.AllowZeroEstimates = false, want true")
	}
	if !state.CyclesEnabled {
		t.Errorf("state.CyclesEnabled = false, want true")
	}
	if state.CycleDurationWeeks != 3 {
		t.Errorf("state.CycleDurationWeeks = %d, want 3", state.CycleDurationWeeks)
	}
	if state.CycleStartDay != 3 {
		t.Errorf("state.CycleStartDay = %d, want 3", state.CycleStartDay)
	}
	if state.CycleCooldownWeeks != 1 {
		t.Errorf("state.CycleCooldownWeeks = %d, want 1", state.CycleCooldownWeeks)
	}
	if !state.TriageEnabled {
		t.Errorf("state.TriageEnabled = false, want true")
	}
	if len(state.UnknownOps) != 0 {
		t.Errorf("state.UnknownOps length = %d, want 0", len(state.UnknownOps))
	}
}

func TestFoldSettingsUnknownKeys(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	// Newer client writes unknown setting keys
	op1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   s.DefaultSettingsObjectID,
			ObjectType: "settings",
			OpType:     "set",
			OpVersion:  1,
			Body: json.RawMessage(`{
				"name": "Writ Core",
				"theme": "dark",
				"auto_archive_days": 30
			}`),
		},
		ID: "c1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	// Older client writes only known field 'identifier'
	op2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   s.DefaultSettingsObjectID,
			ObjectType: "settings",
			OpType:     "set",
			OpVersion:  1,
			Body: json.RawMessage(`{
				"identifier": "WRIT"
			}`),
		},
		ID:      "c2",
		Parents: []string{"c1"},
		Author: codec.Identity{
			Email: "bob@example.com",
			When:  now.Add(time.Minute),
		},
	}

	state, err := s.FoldSettings([]codec.Op{op1, op2})
	if err != nil {
		t.Fatalf("FoldSettings returned error: %v", err)
	}

	if state.Name != "Writ Core" {
		t.Errorf("state.Name = %q, want 'Writ Core'", state.Name)
	}
	if state.Identifier != "WRIT" {
		t.Errorf("state.Identifier = %q, want 'WRIT'", state.Identifier)
	}
	if state.UnknownKeys["theme"] != "dark" {
		t.Errorf("state.UnknownKeys['theme'] = %v, want 'dark'", state.UnknownKeys["theme"])
	}
	if state.UnknownKeys["auto_archive_days"] != float64(30) {
		t.Errorf("state.UnknownKeys['auto_archive_days'] = %v, want 30", state.UnknownKeys["auto_archive_days"])
	}
}

func TestFoldSettingsUnknownOps(t *testing.T) {
	now := time.Unix(100, 0).UTC()

	// Unknown op type
	op1 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   s.DefaultSettingsObjectID,
			ObjectType: "settings",
			OpType:     "reset-defaults",
			OpVersion:  1,
			Body:       json.RawMessage(`{"all":true}`),
		},
		ID: "c1",
		Author: codec.Identity{
			Email: "alice@example.com",
			When:  now,
		},
	}

	// Future op version
	op2 := codec.Op{
		Envelope: codec.Envelope{
			ObjectID:   s.DefaultSettingsObjectID,
			ObjectType: "settings",
			OpType:     "set",
			OpVersion:  2,
			Body:       json.RawMessage(`{"v2_setting":123}`),
		},
		ID: "c2",
		Author: codec.Identity{
			Email: "bob@example.com",
			When:  now.Add(time.Minute),
		},
	}

	state, err := s.FoldSettings([]codec.Op{op1, op2})
	if err != nil {
		t.Fatalf("FoldSettings returned error: %v", err)
	}

	if len(state.UnknownOps) != 2 {
		t.Fatalf("expected 2 unknown ops, got %d", len(state.UnknownOps))
	}
	if state.UnknownOps[0].OpType != "reset-defaults" {
		t.Errorf("expected reset-defaults op type, got %s", state.UnknownOps[0].OpType)
	}
	if state.UnknownOps[1].OpVersion != 2 {
		t.Errorf("expected version 2 op, got %d", state.UnknownOps[1].OpVersion)
	}
}
