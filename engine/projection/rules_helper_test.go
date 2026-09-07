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
