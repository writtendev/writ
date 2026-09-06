package schemasrc_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/writtendev/writ/engine/schemasrc"
)

// FuzzParse asserts Parse never panics on any input, and that any *File it
// returns survives Compile without panicking either — run under
// `make fuzz` (Makefile's FUZZTIME-bounded target). Seeded from both
// corpora so the fuzzer starts from inputs that are already close to the
// grammar's edges.
func FuzzParse(f *testing.F) {
	for _, dir := range []string{"testdata/valid", "testdata/invalid"} {
		matches, err := filepath.Glob(filepath.Join(dir, "*.schema"))
		if err != nil {
			f.Fatalf("globbing %s: %v", dir, err)
		}
		for _, m := range matches {
			src, err := os.ReadFile(m)
			if err != nil {
				f.Fatalf("reading %s: %v", m, err)
			}
			f.Add(src)
		}
	}
	f.Add([]byte(""))
	f.Add([]byte("namespace"))
	f.Add([]byte("namespace \xff\xfe"))

	f.Fuzz(func(t *testing.T, src []byte) {
		file, err := schemasrc.Parse("fuzz.schema", src)
		if err != nil {
			return
		}
		// A *File Parse returns without error must survive Compile without
		// panicking. Compile is allowed to reject it (an untyped AST built
		// from adversarial input can still fail spec.ValidateFieldRule),
		// but never allowed to panic.
		_, _ = schemasrc.Compile(file, "sch-fuzz")
	})
}
