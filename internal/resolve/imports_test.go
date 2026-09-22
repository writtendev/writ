package resolve_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var allowedImports = map[string]bool{
	`"bytes"`:         true,
	`"crypto/sha1"`:   true,
	`"crypto/sha256"`: true,
	`"encoding/hex"`:  true,
	`"encoding/json"`: true,
	`"errors"`:        true,
	`"fmt"`:           true,
	`"sort"`:          true,
	`"strings"`:       true,
	`"unicode/utf8"`:  true,
	`"github.com/writtendev/writ/internal/anchorshape"`: true,
}

func TestImportsAllowlist(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("ParseFile %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			pathVal := imp.Path.Value
			if !allowedImports[pathVal] {
				t.Errorf("%s imports forbidden package %s (resolver must remain pure and free of I/O)", filepath.Base(name), pathVal)
			}
		}
	}
}
