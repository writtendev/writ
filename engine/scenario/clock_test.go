package scenario_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/scenario"
)

// Steps may omit At — the runner defaults the clock rather than stamping ops
// with the zero time, which git rejects outright as an author date.
func TestStepsWithoutExplicitTime(t *testing.T) {
	body, err := json.Marshal(map[string]any{"title": "no explicit time"})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	steps := []scenario.Step{
		scenario.Commit{
			Device:  aliceLaptop,
			Files:   map[string]string{"calc.go": initialCalcCode},
			Message: "initial calc implementation",
		},
	}
	// The declaration comes first: an op of a type the log does not declare
	// is refused by the producer, so there would be nothing to stamp.
	steps = append(steps, declareSchema(t, aliceLaptop, time.Time{})...)
	steps = append(steps, scenario.AppendOp{
		Device: aliceLaptop,
		Envelope: codec.Envelope{
			ObjectID:   "w-no-time",
			ObjectType: "acme.widget",
			OpType:     "create",
			OpVersion:  1,
			Body:       body,
		},
	})

	scenario.Run(t, scenario.Scenario{
		Name:    "no-explicit-time",
		Devices: []scenario.Device{aliceLaptop},
		Steps:   steps,
	})
}
