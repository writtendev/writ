package spec_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/writtendev/writ/spec"
)

const refVectorsPath = "testdata/ref-names/vectors.json"

type refVectorsDoc struct {
	Refspecs struct {
		Fetch string `json:"fetch"`
		Push  string `json:"push"`
	} `json:"refspecs"`
	Valid []struct {
		Ref        string `json:"ref"`
		WriterID   string `json:"writer_id"`
		ObjectType string `json:"object_type"`
	} `json:"valid"`
	Invalid []struct {
		Ref    string `json:"ref"`
		Reason string `json:"reason"`
	} `json:"invalid"`
}

func loadRefVectors(t *testing.T) refVectorsDoc {
	t.Helper()
	raw, err := spec.FS.ReadFile(refVectorsPath)
	if err != nil {
		t.Fatalf("reading %s: %v", refVectorsPath, err)
	}
	var doc refVectorsDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decoding %s: %v", refVectorsPath, err)
	}
	return doc
}

var (
	writerIDRegexp   = regexp.MustCompile(`^[0-9a-f]{16}$`)
	objectTypeRegexp = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}(\.[a-z][a-z0-9-]{0,63})?$`)
)

const objectTypeMaxLength = 129

// objectTypeEndsInLock mirrors engine/dag/refs.go's ref-safety exclusion:
// git rejects any slash-separated ref path component ending in ".lock"
// outright, so an object type whose final ("."-delimited) segment is
// exactly "lock" can never be written to a chain (spec/ref-layout.md §4).
func objectTypeEndsInLock(objType string) bool {
	if i := strings.LastIndexByte(objType, '.'); i >= 0 {
		return objType[i+1:] == "lock"
	}
	return objType == "lock"
}

// parseRefName parses and validates a ref name per spec/ref-layout.md.
func parseRefName(ref string) (writerID, objectType string, err error) {
	const prefix = "refs/writ/"
	if !strings.HasPrefix(ref, prefix) {
		return "", "", fmt.Errorf("ref %q must start with %q", ref, prefix)
	}
	rem := strings.TrimPrefix(ref, prefix)
	parts := strings.Split(rem, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("ref %q must have exactly 2 segments after %q, got %d", ref, prefix, len(parts))
	}
	wID, objType := parts[0], parts[1]
	if !writerIDRegexp.MatchString(wID) {
		return "", "", fmt.Errorf("invalid writer-id %q: must match ^[0-9a-f]{16}$", wID)
	}
	if len(objType) == 0 || len(objType) > objectTypeMaxLength || !objectTypeRegexp.MatchString(objType) || objectTypeEndsInLock(objType) {
		return "", "", fmt.Errorf("invalid object-type %q: must match %s with length 1..%d and not end in \".lock\"", objType, objectTypeRegexp.String(), objectTypeMaxLength)
	}
	return wID, objType, nil
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH; skipping git check-ref-format verification")
	}
}

func TestRefLayoutVectorsLoad(t *testing.T) {
	doc := loadRefVectors(t)
	if len(doc.Valid) == 0 {
		t.Fatal("no valid ref vectors loaded")
	}
	if len(doc.Invalid) == 0 {
		t.Fatal("no invalid ref vectors loaded")
	}
}

func TestRefspecsPinned(t *testing.T) {
	doc := loadRefVectors(t)
	const wantFetch = "refs/writ/*:refs/remotes/<remote>/writ/*"
	const wantPush = "refs/writ/<writer-id>/*:refs/writ/<writer-id>/*"
	if doc.Refspecs.Fetch != wantFetch {
		t.Errorf("fetch refspec = %q, want %q", doc.Refspecs.Fetch, wantFetch)
	}
	if doc.Refspecs.Push != wantPush {
		t.Errorf("push refspec = %q, want %q", doc.Refspecs.Push, wantPush)
	}
}

func TestValidRefNames(t *testing.T) {
	doc := loadRefVectors(t)

	for _, v := range doc.Valid {
		v := v
		t.Run(v.Ref, func(t *testing.T) {
			gotWriterID, gotObjectType, err := parseRefName(v.Ref)
			if err != nil {
				t.Fatalf("parseRefName(%q) failed: %v", v.Ref, err)
			}
			if gotWriterID != v.WriterID {
				t.Errorf("writer-id = %q, want %q", gotWriterID, v.WriterID)
			}
			if gotObjectType != v.ObjectType {
				t.Errorf("object-type = %q, want %q", gotObjectType, v.ObjectType)
			}
		})
	}
}

func TestValidRefNamesGitCheckRefFormat(t *testing.T) {
	requireGit(t)
	doc := loadRefVectors(t)

	for _, v := range doc.Valid {
		v := v
		t.Run(v.Ref, func(t *testing.T) {
			cmd := exec.Command("git", "check-ref-format", "--normalize", v.Ref)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("git check-ref-format --normalize %q failed: %v (output: %q)", v.Ref, err, string(out))
			}
			normalized := strings.TrimSpace(string(out))
			if normalized != v.Ref {
				t.Errorf("normalized ref = %q, want %q", normalized, v.Ref)
			}
		})
	}
}

func TestInvalidRefNames(t *testing.T) {
	doc := loadRefVectors(t)

	for _, v := range doc.Invalid {
		v := v
		t.Run(v.Ref, func(t *testing.T) {
			if v.Reason == "" {
				t.Error("invalid ref vector has empty reason")
			}
			writerID, objType, err := parseRefName(v.Ref)
			if err == nil {
				t.Errorf("parseRefName(%q) accepted invalid ref (got writerID=%q, objType=%q); expected rejection: %s",
					v.Ref, writerID, objType, v.Reason)
			}
		})
	}
}
