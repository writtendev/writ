package spec

// NormalizePerson exposes reffold.go's unexported normalizePerson to the
// external spec_test package, which is where the test binding it to the
// engine's definition has to live: that test imports engine/state, and
// engine/state reaches spec through engine/codec, so an in-package test
// importing it would be an import cycle. The helper itself stays unexported —
// reffold.go's surface is unchanged.
var NormalizePerson = normalizePerson

// SplitPerson exposes reffold.go's unexported splitPerson on the same terms as
// NormalizePerson above: the person-identifier vectors in spec_test drive the
// reference implementation's own split rather than a second spelling of it, so
// a fixture that says "the scheme is email" is checked against the code an
// independent implementer would read.
var SplitPerson = splitPerson

// PersonUnicodeVersion exposes the Unicode version the reference fold's copy of
// the normalization rule is pinned to, so the test binding it to the tables
// x/text actually compiled in can live beside the drift test.
const PersonUnicodeVersion = personUnicodeVersion

// FieldRuleSentinels exposes fieldrules.go's unexported token-to-sentinel
// table to spec_test on the same terms as NormalizePerson above: the
// schema-ops conformance harness (schema_ops_test.go) needs to bind an
// "invariant"-kind vector's invariant_rule token to the exact sentinel
// ValidateFieldRule wraps its error with, and that binding has to reach
// into the one table fieldrules.go itself maintains rather than duplicate
// it as a second spelling of the same map.
var FieldRuleSentinels = fieldRuleSentinels
