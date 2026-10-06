package resolve

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
)

// File contains the precomputed blob OID and prepared lines for a file in a Tree.
type File struct {
	Path  string
	Blob  string
	Lines []string
}

// Tree represents a target git tree as a precomputed set of files.
type Tree struct {
	paths []string
	files map[string]*File
}

// NewTree creates a Tree from a map of repo-relative paths to blob contents,
// precomputing the git blob OID and prepared lines for each file.
func NewTree(files map[string][]byte) *Tree {
	t := &Tree{
		paths: make([]string, 0, len(files)),
		files: make(map[string]*File, len(files)),
	}

	for p, content := range files {
		t.paths = append(t.paths, p)
		oid := computeBlobOID(content)
		lines := SplitLines(content)
		t.files[p] = &File{
			Path:  p,
			Blob:  oid,
			Lines: lines,
		}
	}
	sort.Strings(t.paths)
	return t
}

// Paths returns the sorted list of repo-relative file paths in the Tree.
func (t *Tree) Paths() []string {
	out := make([]string, len(t.paths))
	copy(out, t.paths)
	return out
}

// File returns the precomputed File entry for the given path, or nil if absent.
func (t *Tree) File(path string) *File {
	return t.files[path]
}

// Blob returns the blob OID for the given path, or false if absent.
func (t *Tree) Blob(path string) (string, bool) {
	if f, ok := t.files[path]; ok {
		return f.Blob, true
	}
	return "", false
}

func computeBlobOID(content []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(content))
	h.Write(content)
	return hex.EncodeToString(h.Sum(nil))
}
