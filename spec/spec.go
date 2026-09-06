// Package spec exposes the machine-readable half of the Writ
// specification — the JSON Schemas under schemas/ and the conformance
// test data under testdata/ — as one embedded filesystem, so the engine's
// tests and any other consumer read the single committed copy instead of
// carrying their own. (The fixture-repo corpus under fixtures/testdata is
// separate and read by the fixtures package directly.)
//
// The prose halves live alongside as op-envelope.md, ref-layout.md,
// canonicalization.md, forward-compatibility.md, anchors.md, identifiers.md,
// ordering.md, review-ops.md, comments.md, issue-ops.md, workflow-state-ops.md,
// label-ops.md, documents.md, project-cycle.md, settings-ops.md, and README.md in this directory; the fixture-repo generator and
// golden harness live in the fixtures subpackage.
package spec

import "embed"

// FS holds schemas/ and testdata/ with paths relative to the spec
// directory, e.g. "schemas/op-envelope.schema.json" and
// "testdata/canonicalization/vectors.json".
//
//go:embed schemas testdata
var FS embed.FS
