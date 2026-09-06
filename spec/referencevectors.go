package spec

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// ReferenceCase represents one reference test case from testdata/references/valid/*.json.
type ReferenceCase struct {
	Name      string `json:"name,omitempty"`
	Reference string `json:"reference"`
}

// ReferenceVectors loads all valid reference test cases from testdata/references/valid/
// in sorted order and validates that every case has a non-empty reference.
func ReferenceVectors() ([]ReferenceCase, error) {
	const dir = "testdata/references/valid"
	entries, err := FS.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("spec: reading reference cases directory: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("spec: no reference cases found in %s", dir)
	}

	var filenames []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			filenames = append(filenames, entry.Name())
		}
	}
	sort.Strings(filenames)

	if len(filenames) == 0 {
		return nil, fmt.Errorf("spec: no json files found in %s", dir)
	}

	cases := make([]ReferenceCase, 0, len(filenames))
	for _, filename := range filenames {
		filePath := path.Join(dir, filename)
		raw, err := FS.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("spec: reading %s: %w", filePath, err)
		}

		var c ReferenceCase
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("spec: parsing %s: %w", filePath, err)
		}

		c.Name = strings.TrimSuffix(filename, ".json")
		if c.Reference == "" {
			return nil, fmt.Errorf("spec: %s has empty reference", filePath)
		}

		cases = append(cases, c)
	}

	return cases, nil
}
