package projection_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/projection"
	"github.com/writtendev/writ/engine/state"
)

func TestProjectionSettingsLifecycle(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	// 1. Initially returns defaults
	res, err := db.Settings()
	if err != nil {
		t.Fatalf("db.Settings failed: %v", err)
	}
	if res.Settings.Timezone != "UTC" {
		t.Errorf("expected default Timezone 'UTC', got %q", res.Settings.Timezone)
	}
	if res.Settings.EstimateScale != "fibonacci" {
		t.Errorf("expected default EstimateScale 'fibonacci', got %q", res.Settings.EstimateScale)
	}

	// 2. Append settings op
	env := codec.Envelope{
		ObjectID:   state.DefaultSettingsObjectID,
		ObjectType: "settings",
		OpType:     "set",
		OpVersion:  1,
		Body: json.RawMessage(`{
			"name": "Custom Workspace",
			"identifier": "CUST",
			"timezone": "Europe/London",
			"estimate_scale": "exponential",
			"allow_zero_estimates": true,
			"cycles_enabled": true,
			"cycle_duration_weeks": 4,
			"cycle_start_day": 2,
			"cycle_cooldown_weeks": 1,
			"triage_enabled": true,
			"custom_plugin_field": "hello"
		}`),
	}
	raw, _ := codec.EncodePayload(env)
	env.Raw = raw
	if _, err := store.Append(ctx, env, nil); err != nil {
		t.Fatalf("store.Append failed: %v", err)
	}

	// 3. Refresh projection
	if _, err := db.Refresh(store, projection.WithSchema(testRules())); err != nil {
		t.Fatalf("db.Refresh failed: %v", err)
	}

	res, err = db.Settings()
	if err != nil {
		t.Fatalf("db.Settings failed: %v", err)
	}
	if res.Settings.Name != "Custom Workspace" {
		t.Errorf("Name = %q, want 'Custom Workspace'", res.Settings.Name)
	}
	if res.Settings.Identifier != "CUST" {
		t.Errorf("Identifier = %q, want 'CUST'", res.Settings.Identifier)
	}
	if res.Settings.Timezone != "Europe/London" {
		t.Errorf("Timezone = %q, want 'Europe/London'", res.Settings.Timezone)
	}
	if res.Settings.EstimateScale != "exponential" {
		t.Errorf("EstimateScale = %q, want 'exponential'", res.Settings.EstimateScale)
	}
	if !res.Settings.AllowZeroEstimates {
		t.Errorf("AllowZeroEstimates = false, want true")
	}
	if !res.Settings.CyclesEnabled {
		t.Errorf("CyclesEnabled = false, want true")
	}
	if res.Settings.CycleDurationWeeks != 4 {
		t.Errorf("CycleDurationWeeks = %d, want 4", res.Settings.CycleDurationWeeks)
	}
	if res.Settings.CycleStartDay != 2 {
		t.Errorf("CycleStartDay = %d, want 2", res.Settings.CycleStartDay)
	}
	if res.Settings.CycleCooldownWeeks != 1 {
		t.Errorf("CycleCooldownWeeks = %d, want 1", res.Settings.CycleCooldownWeeks)
	}
	if !res.Settings.TriageEnabled {
		t.Errorf("TriageEnabled = false, want true")
	}
	if res.Settings.UnknownKeys["custom_plugin_field"] != "hello" {
		t.Errorf("UnknownKeys['custom_plugin_field'] = %v, want 'hello'", res.Settings.UnknownKeys["custom_plugin_field"])
	}
}

// TestProjectionSettingsPartialWritePreservesFoldDefaults reproduces WRIT-189
// round 1's MAJOR-1 finding: a settings object whose only op writes "name"
// left every other register's column NULL, and DB.Settings() used to read a
// NULL column back as SQL's own zero value ("" / 0) via COALESCE rather than
// state.DefaultSettings()' non-zero defaults — diverging from
// state.FoldSettings, which starts from DefaultSettings() and only
// overwrites a field an op's body actually names, so a field no op has ever
// set keeps its default forever, not just until the object's first write.
func TestProjectionSettingsPartialWritePreservesFoldDefaults(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	env := codec.Envelope{
		ObjectID:   state.DefaultSettingsObjectID,
		ObjectType: "settings",
		OpType:     "set",
		OpVersion:  1,
		Body:       json.RawMessage(`{"name": "Only Name Set"}`),
	}
	raw, _ := codec.EncodePayload(env)
	env.Raw = raw
	if _, err := store.Append(ctx, env, nil); err != nil {
		t.Fatalf("store.Append failed: %v", err)
	}

	if _, err := db.Refresh(store, projection.WithSchema(testRules())); err != nil {
		t.Fatalf("db.Refresh failed: %v", err)
	}

	res, err := db.Settings()
	if err != nil {
		t.Fatalf("db.Settings failed: %v", err)
	}

	want := state.DefaultSettings()
	if res.Settings.Name != "Only Name Set" {
		t.Errorf("Name = %q, want %q", res.Settings.Name, "Only Name Set")
	}
	if res.Settings.Timezone != want.Timezone {
		t.Errorf("Timezone = %q, want default %q", res.Settings.Timezone, want.Timezone)
	}
	if res.Settings.EstimateScale != want.EstimateScale {
		t.Errorf("EstimateScale = %q, want default %q", res.Settings.EstimateScale, want.EstimateScale)
	}
	if res.Settings.CycleDurationWeeks != want.CycleDurationWeeks {
		t.Errorf("CycleDurationWeeks = %d, want default %d", res.Settings.CycleDurationWeeks, want.CycleDurationWeeks)
	}
	if res.Settings.CycleStartDay != want.CycleStartDay {
		t.Errorf("CycleStartDay = %d, want default %d", res.Settings.CycleStartDay, want.CycleStartDay)
	}
	if res.Settings.CycleCooldownWeeks != want.CycleCooldownWeeks {
		t.Errorf("CycleCooldownWeeks = %d, want default %d", res.Settings.CycleCooldownWeeks, want.CycleCooldownWeeks)
	}
	if res.Settings.AllowZeroEstimates != want.AllowZeroEstimates {
		t.Errorf("AllowZeroEstimates = %v, want default %v", res.Settings.AllowZeroEstimates, want.AllowZeroEstimates)
	}
	if res.Settings.CyclesEnabled != want.CyclesEnabled {
		t.Errorf("CyclesEnabled = %v, want default %v", res.Settings.CyclesEnabled, want.CyclesEnabled)
	}
	if res.Settings.TriageEnabled != want.TriageEnabled {
		t.Errorf("TriageEnabled = %v, want default %v", res.Settings.TriageEnabled, want.TriageEnabled)
	}
}

func TestProjectionSettingsDeterministicSelection(t *testing.T) {
	ctx := context.Background()
	_, store := createTestStore(t, "0123456789abcdef")

	db, err := projection.Open(":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:) failed: %v", err)
	}
	defer db.Close()

	// Append a non-canonical settings op that sorts lexicographically before the default settings object ID.
	// DefaultSettingsObjectID is "00000000000000000000000073657474"
	// "00000000000000000000000000000001" sorts before DefaultSettingsObjectID.
	nonCanonicalID := "00000000000000000000000000000001"
	envForeign := codec.Envelope{
		ObjectID:   nonCanonicalID,
		ObjectType: "settings",
		OpType:     "set",
		OpVersion:  1,
		Body: json.RawMessage(`{
			"name": "Foreign Settings"
		}`),
	}
	rawForeign, _ := codec.EncodePayload(envForeign)
	envForeign.Raw = rawForeign
	if _, err := store.Append(ctx, envForeign, nil); err != nil {
		t.Fatalf("store.Append foreign failed: %v", err)
	}

	// Append canonical settings op
	envCanonical := codec.Envelope{
		ObjectID:   state.DefaultSettingsObjectID,
		ObjectType: "settings",
		OpType:     "set",
		OpVersion:  1,
		Body: json.RawMessage(`{
			"name": "Canonical Workspace Settings"
		}`),
	}
	rawCanonical, _ := codec.EncodePayload(envCanonical)
	envCanonical.Raw = rawCanonical
	if _, err := store.Append(ctx, envCanonical, nil); err != nil {
		t.Fatalf("store.Append canonical failed: %v", err)
	}

	if _, err := db.Refresh(store, projection.WithSchema(testRules())); err != nil {
		t.Fatalf("db.Refresh failed: %v", err)
	}

	res, err := db.Settings()
	if err != nil {
		t.Fatalf("db.Settings failed: %v", err)
	}
	if res.ObjectID != state.DefaultSettingsObjectID {
		t.Fatalf("res.ObjectID = %q, want canonical %q", res.ObjectID, state.DefaultSettingsObjectID)
	}
	if res.Settings.Name != "Canonical Workspace Settings" {
		t.Fatalf("res.Settings.Name = %q, want 'Canonical Workspace Settings'", res.Settings.Name)
	}
}

