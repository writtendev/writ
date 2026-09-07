package state

import (
	"fmt"

	"github.com/writtendev/writ/spec"
)

// BuiltinRules returns the built-in vocabulary's fold rules, grouped by
// object type: spec.FieldRules() converted to Rule and bucketed by the
// ObjectType each rule already carries. "schema" is excluded — schema
// objects are excluded from the rule index by construction
// (spec/schema-ops.md §1.2) and always land in unknown_ops, and
// RulesFromSchemas refuses to let a log schema redefine "schema" itself.
//
// This is the one shared construction: package writ's own builtinRulesOnce
// (engine/schema.go) delegates to it directly, and so does every test
// package that previously reimplemented "a rule index declaring the
// current built-in types" on its own — three test copies plus a fourth,
// separately drifting one in production, before WRIT-189 round 2 MINOR-4
// consolidated all four here. Keeping this in engine/state rather than
// spec/fixtures (its round-1 home) puts the one real definition under
// api/engine.txt's guard: a change to it is visible in the public API
// diff, the way a production dependency's only definition should be.
func BuiltinRules() (map[string][]Rule, error) {
	fr, err := spec.FieldRules()
	if err != nil {
		return nil, fmt.Errorf("state: field rules: %w", err)
	}
	out := make(map[string][]Rule)
	for _, r := range fr {
		if r.ObjectType == "schema" {
			continue
		}
		out[r.ObjectType] = append(out[r.ObjectType], Rule{
			OpType:     r.OpType,
			OpVersion:  r.OpVersion,
			Field:      r.Field,
			Target:     r.Target,
			Strategy:   r.Strategy,
			Key:        r.Key,
			Lattice:    r.Lattice,
			ValueType:  r.ValueType,
			Enum:       r.Enum,
			MaxLength:  r.MaxLength,
			KeyTypes:   r.KeyTypes,
			ObjectType: r.ObjectType,
		})
	}
	return out, nil
}
