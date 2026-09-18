package projection

import (
	"github.com/writtendev/writ/internal/dag"
)

// WithEnumOverrideForTest overrides the enumeration result for testing.
func WithEnumOverrideForTest(res *dag.EnumerateResult) Option {
	return func(c *refreshConfig) {
		c.enumOverride = res
	}
}
