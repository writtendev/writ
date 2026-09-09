package scenario_test

import (
	"testing"
	"time"

	"github.com/writtendev/writ/engine/scenario"
	"github.com/writtendev/writ/engine/schemasrc"
)

// schemaObjectID is the object every declaration op below lands on: one
// schema object, authored once, fetched by the other devices.
const schemaObjectID = "sch-acme"

// acmeSchema is the vocabulary these scenarios write against. Writ
// hard-codes one object type, `schema`, so a scenario declares its own types
// in its own log the way any consumer does — and the same declaration is
// what the producer validates each op against and what the converged
// snapshot is folded by.
const acmeSchema = `namespace acme

type widget {
  op create 1, update 1 {
    title        string  lww
    description  text    lww
  }

  op endorse 1 {
    subject   person-ref     lww
    revision  git-oid        lww
    verdict   enum(yes, no)  lww
    message   string         lww
  }
}

type waypoint {
  op create 1 {
    subject      object-ref  create-once
    in_reply_to  object-ref  lww
    text         text        lww
    anchor       anchor      lww
  }
}
`

// declareSchema is the prologue every scenario below starts with: the ops
// that write acmeSchema into dev's log, one AppendOp apiece, stamped at at.
// Until they land — and until the other devices have fetched them — an op of
// a declared type is refused by the producer, which is the real behaviour
// rather than a test artefact, and is why each scenario pushes and fetches
// before appending anything else.
func declareSchema(t *testing.T, dev scenario.Device, at time.Time) []scenario.Step {
	t.Helper()

	file, err := schemasrc.Parse("acme.schema", []byte(acmeSchema))
	if err != nil {
		t.Fatalf("parse schema source: %v", err)
	}
	envelopes, err := schemasrc.Compile(file, schemaObjectID)
	if err != nil {
		t.Fatalf("compile schema source: %v", err)
	}

	steps := make([]scenario.Step, 0, len(envelopes))
	for _, env := range envelopes {
		steps = append(steps, scenario.AppendOp{Device: dev, At: at, Envelope: env})
	}
	return steps
}
