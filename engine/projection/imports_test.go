package projection_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/writtendev/writ/engine/projection"
)

var allowedImports = map[string]bool{
	`"crypto/rand"`:                                true,
	`"crypto/sha256"`:                              true,
	`"database/sql"`:                               true,
	`"encoding/hex"`:                               true,
	`"encoding/json"`:                              true,
	`"errors"`:                                     true,
	`"fmt"`:                                        true,
	`"path/filepath"`:                              true,
	`"regexp"`:                                     true,
	`"sort"`:                                       true,
	`"strconv"`:                                    true,
	`"strings"`:                                    true,
	`"sync"`:                                       true,
	`"time"`:                                       true,
	`"modernc.org/sqlite"`:                         true,
	`"github.com/go-git/go-git/v5"`:                 true,
	`"github.com/go-git/go-git/v5/plumbing"`:        true,
	`"github.com/go-git/go-git/v5/plumbing/object"`: true,
	`"github.com/go-git/go-git/v5/plumbing/storer"`: true,
	`"github.com/go-git/go-git/v5/storage"`:         true,
	`"github.com/writtendev/writ/engine/codec"`:     true,
	`"github.com/writtendev/writ/engine/dag"`:      true,
	`"github.com/writtendev/writ/engine/resolve"`:  true,
	`"github.com/writtendev/writ/engine/state"`:    true,
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
					t.Errorf("%s imports forbidden package %s", filepath.Base(filename), pathVal)
				}
			}
		}
	}
}

// TestDomainShapedResultTypes performs a reflection walk across all exported query result types
// to ensure no git SHAs, refnames, or refspecs leak to callers beyond the allowed domain content fields.
func TestDomainShapedResultTypes(t *testing.T) {
	allowedFields := map[string]bool{
		"LastOpID": true, // object summary op id
	}

	disallowedSubstrings := []string{
		"sha", "refspec", "refname", "gitref", "branch",
	}

	var checkType func(typ reflect.Type, visited map[reflect.Type]bool, path string)
	checkType = func(typ reflect.Type, visited map[reflect.Type]bool, path string) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		if visited[typ] {
			return
		}
		visited[typ] = true

		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}

			fieldPath := path + "." + field.Name

			if !allowedFields[field.Name] {
				nameLower := strings.ToLower(field.Name)
				for _, sub := range disallowedSubstrings {
					if strings.Contains(nameLower, sub) {
						t.Errorf("field %s leaks internal git concept (%q)", fieldPath, sub)
					}
				}
			}

			checkType(field.Type, visited, fieldPath)
		}
	}

	typesToCheck := []any{
		projection.ObjectResult{},
		projection.Author{},
	}

	visited := make(map[reflect.Type]bool)
	for _, inst := range typesToCheck {
		typ := reflect.TypeOf(inst)
		checkType(typ, visited, typ.Name())
	}
}
