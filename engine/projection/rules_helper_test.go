package projection_test

import (
	"sync"

	"github.com/writtendev/writ/engine/state"
)

// testRules is the one definition of "a test schema that declares the
// current [built-in] types" every projection test shares — a thin, memoised
// wrapper over state.BuiltinRules(), the single shared construction (see
// its doc comment for why schema-ops is excluded).
var testRulesOnce = sync.OnceValue(func() map[string][]state.Rule {
	rules, err := state.BuiltinRules()
	if err != nil {
		panic(err)
	}
	return rules
})

func testRules() map[string][]state.Rule {
	return testRulesOnce()
}

// neutralTestRules declares one schema-shaped type, "ticket" — the neutral
// example type AGENTS.md and spec/schema-source.md use, never "review" or
// "issue" — for tests exercising the generic Objects/Object query path that
// would otherwise have no reason to depend on testRules()'s still-builtin
// (pre-WRIT-194) vocabulary. Hand-built rather than routed through
// schemasrc/RulesFromSchemas: ApplySchema/Refresh take a plain
// map[string][]state.Rule regardless of where it came from, and a literal
// table costs the same to read as a compiled one for a fixed, small schema.
func neutralTestRules() map[string][]state.Rule {
	return map[string][]state.Rule{
		"ticket": {
			{OpType: "create", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
			{OpType: "create", OpVersion: 1, Field: "status", Strategy: "lww", ValueType: "string"},
			{OpType: "update", OpVersion: 1, Field: "title", Strategy: "lww", ValueType: "string"},
			{OpType: "update", OpVersion: 1, Field: "status", Strategy: "lww", ValueType: "string"},
			{OpType: "assign", OpVersion: 1, Field: "add", Target: "assignees", Strategy: "set-observed-remove", ValueType: "string"},
			{OpType: "assign", OpVersion: 1, Field: "remove", Target: "assignees", Strategy: "set-observed-remove", ValueType: "string"},
		},
	}
}
