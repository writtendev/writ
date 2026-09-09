package scenario_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/writtendev/writ/engine/codec"
	"github.com/writtendev/writ/engine/scenario"
	writsync "github.com/writtendev/writ/engine/sync"
)

var (
	alice = scenario.Writer{
		Name:  "Alice",
		Email: "alice@example.test",
	}
	bob = scenario.Writer{
		Name:  "Bob",
		Email: "bob@example.test",
	}

	aliceLaptop = scenario.Device{
		Name:     "alice-laptop",
		Writer:   alice,
		WriterID: "0123456789abcdef",
	}
	aliceDesktop = scenario.Device{
		Name:     "alice-desktop",
		Writer:   alice,
		WriterID: "1122334455667788",
	}
	bobLaptop = scenario.Device{
		Name:     "bob-laptop",
		Writer:   bob,
		WriterID: "fedcba9876543210",
	}
)

// anchorCommit stands in for the code commit an anchor points at. A scenario
// literal is built before the runner makes any commit, so the real SHA is not
// available here, and anchor resolution never reads the field — it re-anchors
// by path, blob and captured context. It still has to be a well-formed OID:
// the `anchor` value type requires one, and the producer refuses to sign an
// op whose body violates a declared value type. The literal OIDs in the
// endorse bodies below stand in the same way, for `revision`.
const anchorCommit = "0000000000000000000000000000000000000000"

const initialCalcCode = `package calc

func Add(a, b int) int {
	return a + b
}
`

const rebasedCalcCode = `package calc

// Add returns the sum of a and b.
func Add(a, b int) int {
	return a + b
}
`

// anchoredWaypointBody is the body of the anchored waypoint every scenario
// below writes: it names the widget it hangs off and carries the anchor
// Converge's anchor check re-resolves against the code tree.
func anchoredWaypointBody(t *testing.T) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"subject": "w-canonical",
		"text":    "Consider adding multiplication support as well.",
		"anchor":  scenario.MakeAnchor(anchorCommit, "calc.go", initialCalcCode, 3, 5),
	})
	if err != nil {
		t.Fatalf("marshal waypoint body: %v", err)
	}
	return body
}

func TestCanonical(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	steps := []scenario.Step{
		// 1. Alice-laptop commits initial code and pushes to origin
		scenario.Commit{
			Device:  aliceLaptop,
			Files:   map[string]string{"calc.go": initialCalcCode},
			Message: "initial calc implementation",
			At:      baseTime,
		},
		scenario.PushBranch{
			Device: aliceLaptop,
			Branch: "main",
		},
	}

	// 2. Alice-laptop declares the vocabulary and shares it: no op of a
	// declared type is writable on a device until the schema object has
	// reached that device's clone.
	steps = append(steps, declareSchema(t, aliceLaptop, baseTime)...)
	steps = append(steps,
		scenario.Push{Device: aliceLaptop},
		scenario.Fetch{Device: bobLaptop},
		scenario.Fetch{Device: aliceDesktop},

		// 3. Alice-laptop opens the widget and adds an anchored waypoint
		scenario.AppendOp{
			Device: aliceLaptop,
			At:     baseTime.Add(1 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "w-canonical",
				ObjectType: "widget",
				OpType:     "create",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"title": "Add calculator functions",
					"description": "Initial draft of addition"
				}`),
			},
		},
		scenario.AppendOp{
			Device: aliceLaptop,
			At:     baseTime.Add(2 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "wp-anchor",
				ObjectType: "waypoint",
				OpType:     "create",
				OpVersion:  1,
				Body:       anchoredWaypointBody(t),
			},
		},
		scenario.Push{
			Device: aliceLaptop,
		},

		// 4. Bob-laptop fetches
		scenario.Fetch{
			Device: bobLaptop,
		},

		// 5. Bob-laptop replies to the waypoint and endorses the widget
		scenario.AppendOp{
			Device: bobLaptop,
			At:     baseTime.Add(3 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "wp-reply",
				ObjectType: "waypoint",
				OpType:     "create",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"subject": "w-canonical",
					"in_reply_to": "wp-anchor",
					"text": "Great idea, will follow up!"
				}`),
			},
		},
		scenario.AppendOp{
			Device: bobLaptop,
			At:     baseTime.Add(4 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "w-canonical",
				ObjectType: "widget",
				OpType:     "endorse",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"subject": "user:bob",
					"revision": "1111111111111111111111111111111111111111",
					"verdict": "yes",
					"message": "Looks solid"
				}`),
			},
		},
		scenario.Push{
			Device: bobLaptop,
		},

		// 6. Alice-desktop has been offline since the schema landed; edits
		// the same widget concurrently (multi-device race)
		scenario.AppendOp{
			Device: aliceDesktop,
			At:     baseTime.Add(5 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "w-canonical",
				ObjectType: "widget",
				OpType:     "update",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"title": "Add calculator functions (polished title)"
				}`),
			},
		},

		// 7. Bob force-pushes the rebased code branch after the anchored
		// waypoint exists
		scenario.Commit{
			Device:  bobLaptop,
			Files:   map[string]string{"calc.go": rebasedCalcCode},
			Message: "rebase and add doc comment",
			At:      baseTime.Add(6 * time.Minute),
		},
		scenario.PushBranch{
			Device: bobLaptop,
			Branch: "main",
			Force:  true,
		},

		// 8. Alice-desktop syncs last (pushes local offline edits)
		scenario.Push{
			Device: aliceDesktop,
		},

		// 9. Everyone fetches all updates
		scenario.Fetch{Device: aliceLaptop},
		scenario.Fetch{Device: bobLaptop},
		scenario.Fetch{Device: aliceDesktop},

		// 10. Converge: assert identical folded state across all 3 clones
		scenario.Converge{
			AnchorChecks: []scenario.AnchorCheck{
				{ObjectID: "wp-anchor", Branch: "main"},
			},
		},
	)

	scenario.Run(t, scenario.Scenario{
		Name:    "canonical",
		Devices: []scenario.Device{aliceLaptop, aliceDesktop, bobLaptop},
		Steps:   steps,
	})
}

func TestChainRollback(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	steps := declareSchema(t, aliceLaptop, baseTime)
	steps = append(steps,
		scenario.Push{Device: aliceLaptop},
		scenario.Fetch{Device: bobLaptop},

		// 1. Alice-laptop creates widget w-rollback (op1)
		scenario.AppendOp{
			Device: aliceLaptop,
			At:     baseTime,
			Envelope: codec.Envelope{
				ObjectID:   "w-rollback",
				ObjectType: "widget",
				OpType:     "create",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"title": "Op1 Title",
					"description": "Initial creation"
				}`),
			},
		},
		// 2. Alice-laptop updates the widget (op2)
		scenario.AppendOp{
			Device: aliceLaptop,
			At:     baseTime.Add(1 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "w-rollback",
				ObjectType: "widget",
				OpType:     "update",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"title": "Op2 Title (Rewound Later)"
				}`),
			},
		},
		// 3. Alice pushes to origin (origin ref now at op2)
		scenario.Push{
			Device: aliceLaptop,
		},
		// 4. Bob fetches from origin (Bob tracking ref at op2)
		scenario.Fetch{
			Device: bobLaptop,
		},
		// 5. Alice rewinds local chain to op1 and force-pushes to origin
		scenario.ForcePushChain{
			Device:        aliceLaptop,
			ObjectType:    "widget",
			TargetOpIndex: 0,
		},
		// 6. Bob fetches: must reject non-fast-forward update (spec/ref-layout.md §168)
		scenario.Fetch{
			Device:        bobLaptop,
			ExpectedError: writsync.ErrNonFastForward,
		},
		// 7. Alice recovers by restoring local ref to op2 and appending op3
		scenario.ResetLocalChain{
			Device:        aliceLaptop,
			ObjectType:    "widget",
			TargetOpIndex: 1,
		},
		scenario.AppendOp{
			Device: aliceLaptop,
			At:     baseTime.Add(2 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "w-rollback",
				ObjectType: "widget",
				OpType:     "update",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"title": "Op3 Title (Fast-Forward Recovered)"
				}`),
			},
		},
		scenario.Push{
			Device: aliceLaptop,
		},
		// 8. Bob fetches fast-forwarded update
		scenario.Fetch{
			Device: bobLaptop,
		},
		// 9. Converge: both clones converge and no peer lost ops
		scenario.Converge{},
	)

	scenario.Run(t, scenario.Scenario{
		Name:    "chain-rollback",
		Devices: []scenario.Device{aliceLaptop, bobLaptop},
		Steps:   steps,
	})
}

func TestSyncOrderPermutation(t *testing.T) {
	baseTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// Permuted sync order: same appends, reordered push/fetch
	steps := []scenario.Step{
		// 1. Alice-laptop commits initial code
		scenario.Commit{
			Device:  aliceLaptop,
			Files:   map[string]string{"calc.go": initialCalcCode},
			Message: "initial calc implementation",
			At:      baseTime,
		},
		scenario.PushBranch{
			Device: aliceLaptop,
			Branch: "main",
		},
	}

	// 2. The same schema prologue the canonical scenario runs: a device
	// cannot write a declared type before the declaration reaches it, so
	// this much of the order is not permutable.
	steps = append(steps, declareSchema(t, aliceLaptop, baseTime)...)
	steps = append(steps,
		scenario.Push{Device: aliceLaptop},
		scenario.Fetch{Device: bobLaptop},
		scenario.Fetch{Device: aliceDesktop},

		// 3. Alice-laptop appends the widget create and the anchored waypoint
		scenario.AppendOp{
			Device: aliceLaptop,
			At:     baseTime.Add(1 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "w-canonical",
				ObjectType: "widget",
				OpType:     "create",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"title": "Add calculator functions",
					"description": "Initial draft of addition"
				}`),
			},
		},
		scenario.AppendOp{
			Device: aliceLaptop,
			At:     baseTime.Add(2 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "wp-anchor",
				ObjectType: "waypoint",
				OpType:     "create",
				OpVersion:  1,
				Body:       anchoredWaypointBody(t),
			},
		},

		// 4. Alice-desktop appends its update offline FIRST, before anyone
		// pushed the widget ops
		scenario.AppendOp{
			Device: aliceDesktop,
			At:     baseTime.Add(5 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "w-canonical",
				ObjectType: "widget",
				OpType:     "update",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"title": "Add calculator functions (polished title)"
				}`),
			},
		},

		// 5. Alice-laptop pushes
		scenario.Push{
			Device: aliceLaptop,
		},

		// 6. Bob fetches and appends the reply and the endorsement
		scenario.Fetch{
			Device: bobLaptop,
		},
		scenario.AppendOp{
			Device: bobLaptop,
			At:     baseTime.Add(3 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "wp-reply",
				ObjectType: "waypoint",
				OpType:     "create",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"subject": "w-canonical",
					"in_reply_to": "wp-anchor",
					"text": "Great idea, will follow up!"
				}`),
			},
		},
		scenario.AppendOp{
			Device: bobLaptop,
			At:     baseTime.Add(4 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "w-canonical",
				ObjectType: "widget",
				OpType:     "endorse",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"subject": "user:bob",
					"revision": "1111111111111111111111111111111111111111",
					"verdict": "yes",
					"message": "Looks solid"
				}`),
			},
		},

		// 7. Bob force-pushes the code branch and pushes writ ops
		scenario.Commit{
			Device:  bobLaptop,
			Files:   map[string]string{"calc.go": rebasedCalcCode},
			Message: "rebase and add doc comment",
			At:      baseTime.Add(6 * time.Minute),
		},
		scenario.PushBranch{
			Device: bobLaptop,
			Branch: "main",
			Force:  true,
		},
		scenario.Push{
			Device: bobLaptop,
		},

		// 8. Alice-laptop fetches Bob's ops before Alice-desktop pushes
		scenario.Fetch{Device: aliceLaptop},

		// 9. Alice-desktop pushes
		scenario.Push{Device: aliceDesktop},

		// 10. Remaining fetches in permuted order
		scenario.Fetch{Device: bobLaptop},
		scenario.Fetch{Device: aliceLaptop},
		scenario.Fetch{Device: aliceDesktop},

		// 11. Converge: must equal canonical golden snapshot
		scenario.Converge{
			GoldenName: "canonical",
			AnchorChecks: []scenario.AnchorCheck{
				{ObjectID: "wp-anchor", Branch: "main"},
			},
		},
	)

	scenario.Run(t, scenario.Scenario{
		Name:    "sync-order-permutation",
		Devices: []scenario.Device{aliceLaptop, aliceDesktop, bobLaptop},
		Steps:   steps,
	})
}

type spyReporter struct {
	fatals  []string
	logs    []string
	tempDir string
}

func (s *spyReporter) Helper() {}
func (s *spyReporter) Fatalf(format string, args ...any) {
	s.fatals = append(s.fatals, format)
}
func (s *spyReporter) Logf(format string, args ...any) {
	s.logs = append(s.logs, format)
}
func (s *spyReporter) TempDir() string {
	return s.tempDir
}
func (s *spyReporter) Skip(args ...any) {}

// canonicalStepsMinus is the canonical scenario with exactly one step
// dropped, which is what the two negative controls below are: each proves
// the convergence and golden assertions are load-bearing by removing one
// thing and requiring a failure. skip is called with each step in order and
// returns true for the single step to omit; dropping any other number is
// itself a failure, so a control cannot silently stop removing anything.
func canonicalStepsMinus(t *testing.T, skip func(scenario.Step) bool) []scenario.Step {
	t.Helper()

	baseTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	steps := []scenario.Step{
		scenario.Commit{
			Device:  aliceLaptop,
			Files:   map[string]string{"calc.go": initialCalcCode},
			Message: "initial calc implementation",
			At:      baseTime,
		},
		scenario.PushBranch{Device: aliceLaptop, Branch: "main"},
	}
	steps = append(steps, declareSchema(t, aliceLaptop, baseTime)...)
	steps = append(steps,
		scenario.Push{Device: aliceLaptop},
		scenario.Fetch{Device: bobLaptop},
		scenario.Fetch{Device: aliceDesktop},
		scenario.AppendOp{
			Device: aliceLaptop,
			At:     baseTime.Add(1 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "w-canonical",
				ObjectType: "widget",
				OpType:     "create",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"title": "Add calculator functions",
					"description": "Initial draft of addition"
				}`),
			},
		},
		scenario.AppendOp{
			Device: aliceLaptop,
			At:     baseTime.Add(2 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "wp-anchor",
				ObjectType: "waypoint",
				OpType:     "create",
				OpVersion:  1,
				Body:       anchoredWaypointBody(t),
			},
		},
		scenario.Push{Device: aliceLaptop},
		scenario.Fetch{Device: bobLaptop},
		scenario.AppendOp{
			Device: bobLaptop,
			At:     baseTime.Add(3 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "wp-reply",
				ObjectType: "waypoint",
				OpType:     "create",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"subject": "w-canonical",
					"in_reply_to": "wp-anchor",
					"text": "Great idea, will follow up!"
				}`),
			},
		},
		scenario.AppendOp{
			Device: bobLaptop,
			At:     baseTime.Add(4 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "w-canonical",
				ObjectType: "widget",
				OpType:     "endorse",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"subject": "user:bob",
					"revision": "1111111111111111111111111111111111111111",
					"verdict": "yes",
					"message": "Looks solid"
				}`),
			},
		},
		scenario.Push{Device: bobLaptop},
		scenario.AppendOp{
			Device: aliceDesktop,
			At:     baseTime.Add(5 * time.Minute),
			Envelope: codec.Envelope{
				ObjectID:   "w-canonical",
				ObjectType: "widget",
				OpType:     "update",
				OpVersion:  1,
				Body: json.RawMessage(`{
					"title": "Add calculator functions (polished title)"
				}`),
			},
		},
		scenario.Commit{
			Device:  bobLaptop,
			Files:   map[string]string{"calc.go": rebasedCalcCode},
			Message: "rebase and add doc comment",
			At:      baseTime.Add(6 * time.Minute),
		},
		scenario.PushBranch{Device: bobLaptop, Branch: "main", Force: true},
		scenario.Push{Device: aliceDesktop},
		scenario.Fetch{Device: aliceLaptop},
		scenario.Fetch{Device: bobLaptop},
		scenario.Fetch{Device: aliceDesktop},
		scenario.Converge{
			GoldenName:       "canonical",
			SkipGoldenUpdate: true,
			AnchorChecks: []scenario.AnchorCheck{
				{ObjectID: "wp-anchor", Branch: "main"},
			},
		},
	)

	kept := make([]scenario.Step, 0, len(steps))
	dropped := 0
	for _, step := range steps {
		if skip(step) {
			dropped++
			continue
		}
		kept = append(kept, step)
	}
	if dropped != 1 {
		t.Fatalf("negative control dropped %d steps, want exactly 1", dropped)
	}
	return kept
}

func TestNegativeControl_MissingFetchFails(t *testing.T) {
	// Alice-desktop's final fetch is omitted, so it never sees Bob's ops.
	// Its first fetch is the schema prologue's and has to stay: without it
	// alice-desktop could not write its own update at all, and the control
	// would fail for a reason that has nothing to do with convergence.
	seen := 0
	steps := canonicalStepsMinus(t, func(step scenario.Step) bool {
		fetch, ok := step.(scenario.Fetch)
		if !ok || fetch.Device.Name != aliceDesktop.Name {
			return false
		}
		seen++
		return seen == 2
	})

	spy := &spyReporter{tempDir: t.TempDir()}
	scenario.Run(spy, scenario.Scenario{
		Name:    "canonical",
		Devices: []scenario.Device{aliceLaptop, aliceDesktop, bobLaptop},
		Steps:   steps,
	})

	if len(spy.fatals) == 0 {
		t.Fatalf("expected negative control (missing fetch) to fail with convergence mismatch, but it passed")
	}
}

func TestNegativeControl_MissingOpFails(t *testing.T) {
	// Bob's endorsement is omitted -> golden mismatch.
	steps := canonicalStepsMinus(t, func(step scenario.Step) bool {
		op, ok := step.(scenario.AppendOp)
		return ok && op.Envelope.OpType == "endorse"
	})

	spy := &spyReporter{tempDir: t.TempDir()}
	scenario.Run(spy, scenario.Scenario{
		Name:    "canonical",
		Devices: []scenario.Device{aliceLaptop, aliceDesktop, bobLaptop},
		Steps:   steps,
	})

	if len(spy.fatals) == 0 {
		t.Fatalf("expected negative control (missing op) to fail with golden diff mismatch, but it passed")
	}
}
