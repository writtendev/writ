package state

import (
	"encoding/json"
	"fmt"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/internal/fold"
)

// DefaultSettingsObjectID is the well-known canonical 32-character hexadecimal
// object ID for repository settings ("00000000000000000000000073657474").
const DefaultSettingsObjectID = "00000000000000000000000073657474"

// Settings represents the materialized state of repository settings (v1),
// produced by FoldSettings.
type Settings struct {
	ObjectID           string         `json:"object_id"`
	Name               string         `json:"name"`
	Identifier         string         `json:"identifier"`
	Timezone           string         `json:"timezone"`
	EstimateScale      string         `json:"estimate_scale"`
	AllowZeroEstimates bool           `json:"allow_zero_estimates"`
	CyclesEnabled      bool           `json:"cycles_enabled"`
	CycleDurationWeeks int            `json:"cycle_duration_weeks"`
	CycleStartDay      int            `json:"cycle_start_day"`
	CycleCooldownWeeks int            `json:"cycle_cooldown_weeks"`
	TriageEnabled      bool           `json:"triage_enabled"`
	UnknownKeys        map[string]any `json:"unknown_keys,omitempty"`
	UnknownOps         []UnknownOp    `json:"unknown_ops,omitempty"`
}

// DefaultSettings returns the default configuration for a repository that has
// never written a settings operation.
func DefaultSettings() Settings {
	return Settings{
		ObjectID:           DefaultSettingsObjectID,
		Name:               "",
		Identifier:         "",
		Timezone:           "UTC",
		EstimateScale:      "fibonacci",
		AllowZeroEstimates: false,
		CyclesEnabled:      false,
		CycleDurationWeeks: 2,
		CycleStartDay:      1, // Monday
		CycleCooldownWeeks: 0,
		TriageEnabled:      false,
		UnknownKeys:        make(map[string]any),
	}
}

// FoldSettings executes deterministic fold reduction on an input set of operations
// for a settings collaborative object, returning the materialized Settings state.
func FoldSettings(ops []codec.Op) (Settings, error) {
	if len(ops) == 0 {
		return DefaultSettings(), nil
	}

	orderedOps, err := fold.OrderWithTStar(ops)
	if err != nil {
		return Settings{}, err
	}

	state := DefaultSettings()
	if len(ops) > 0 && ops[0].ObjectID != "" {
		state.ObjectID = ops[0].ObjectID
	}

	var unknownOps []UnknownOp
	rules := internalRules(SettingsRules())

	for _, o := range orderedOps {
		op := o.Op
		if op.ObjectType != "settings" || op.OpVersion != 1 {
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     op.ID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
			})
			continue
		}

		var body map[string]any
		if len(op.Body) > 0 {
			if err := json.Unmarshal(op.Body, &body); err != nil {
				return Settings{}, fmt.Errorf("fold settings: unmarshaling op %s body: %w", op.ID, err)
			}
		}
		if body == nil {
			body = make(map[string]any)
		}

		if fold.Uninterpretable(op, body, rules) {
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     op.ID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
			})
			continue
		}

		switch op.OpType {
		case "set":
			if v, ok := body["name"].(string); ok {
				state.Name = v
			}
			if v, ok := body["identifier"].(string); ok {
				state.Identifier = v
			}
			if v, ok := body["timezone"].(string); ok {
				state.Timezone = v
			}
			if v, ok := body["estimate_scale"].(string); ok {
				state.EstimateScale = v
			}
			if v, ok := body["allow_zero_estimates"].(bool); ok {
				state.AllowZeroEstimates = v
			}
			if v, ok := body["cycles_enabled"].(bool); ok {
				state.CyclesEnabled = v
			}
			if v, ok := body["cycle_duration_weeks"].(float64); ok {
				state.CycleDurationWeeks = int(v)
			} else if v, ok := body["cycle_duration_weeks"].(int); ok {
				state.CycleDurationWeeks = v
			}
			if v, ok := body["cycle_start_day"].(float64); ok {
				state.CycleStartDay = int(v)
			} else if v, ok := body["cycle_start_day"].(int); ok {
				state.CycleStartDay = v
			}
			if v, ok := body["cycle_cooldown_weeks"].(float64); ok {
				state.CycleCooldownWeeks = int(v)
			} else if v, ok := body["cycle_cooldown_weeks"].(int); ok {
				state.CycleCooldownWeeks = v
			}
			if v, ok := body["triage_enabled"].(bool); ok {
				state.TriageEnabled = v
			}

			// Unknown settings keys preservation
			for k, v := range body {
				switch k {
				case "name", "identifier", "timezone", "estimate_scale",
					"allow_zero_estimates", "cycles_enabled",
					"cycle_duration_weeks", "cycle_start_day",
					"cycle_cooldown_weeks", "triage_enabled":
					// known field
				default:
					if state.UnknownKeys == nil {
						state.UnknownKeys = make(map[string]any)
					}
					state.UnknownKeys[k] = v
				}
			}
		default:
			unknownOps = append(unknownOps, UnknownOp{
				Commit:     op.ID,
				ObjectType: op.ObjectType,
				OpType:     op.OpType,
				OpVersion:  op.OpVersion,
			})
		}
	}

	state.UnknownOps = unknownOps
	return state, nil
}

// SettingsRules returns the built-in field merge rules for the settings vocabulary (v1).
func SettingsRules() []Rule {
	return []Rule{
		{OpType: "set", OpVersion: 1, Field: "name", Strategy: "lww", ValueType: "string"},
		{OpType: "set", OpVersion: 1, Field: "identifier", Strategy: "lww", ValueType: "string"},
		{OpType: "set", OpVersion: 1, Field: "timezone", Strategy: "lww", ValueType: "string"},
		{OpType: "set", OpVersion: 1, Field: "estimate_scale", Strategy: "lww", ValueType: "enum", Enum: []string{"none", "fibonacci", "exponential", "linear", "t-shirt"}},
		{OpType: "set", OpVersion: 1, Field: "allow_zero_estimates", Strategy: "lww", ValueType: "bool"},
		{OpType: "set", OpVersion: 1, Field: "cycles_enabled", Strategy: "lww", ValueType: "bool"},
		{OpType: "set", OpVersion: 1, Field: "cycle_duration_weeks", Strategy: "lww", ValueType: "int"},
		{OpType: "set", OpVersion: 1, Field: "cycle_start_day", Strategy: "lww", ValueType: "int"},
		{OpType: "set", OpVersion: 1, Field: "cycle_cooldown_weeks", Strategy: "lww", ValueType: "int"},
		{OpType: "set", OpVersion: 1, Field: "triage_enabled", Strategy: "lww", ValueType: "bool"},
	}
}
