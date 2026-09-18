package resolve_test

import (
	"go/parser"
	"go/token"
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
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}

	for _, pkg := range pkgs {
		for filename, file := range pkg.Files {
			if strings.HasSuffix(filename, "_test.go") {
				continue
			}
			for _, imp := range file.Imports {
				pathVal := imp.Path.Value
				if !allowedImports[pathVal] {
					t.Errorf("%s imports forbidden package %s (resolver must remain pure and free of I/O)", filepath.Base(filename), pathVal)
				}
			}
		}
	}
}
