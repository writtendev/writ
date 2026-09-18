package fixtures_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/writtendev/writ/internal/codec"
	"github.com/writtendev/writ/internal/codec/canonicaljson"
	"github.com/writtendev/writ/spec"
	"github.com/writtendev/writ/spec/fixtures"
)

// TestForwardCompatFamily registers the forward-compat fixture family and runs
// all forward-compat-*.yaml descriptions through the golden test harness.
func TestForwardCompatFamily(t *testing.T) {
	fixtures.Run(t, fixtures.Family{
		Name:      "forward-compat",
		GoldenDir: "testdata/golden/forward-compat",
		Filter: func(desc *fixtures.Description) bool {
			return strings.HasPrefix(desc.Name, "forward-compat-")
		},
		Runner: runForwardCompatFixture,
	})
}

type ForwardCompatGolden struct {
	ReaderProfile         string                 `json:"reader_profile"`
	Ops                   []ForwardCompatOpState `json:"ops"`
	OpaqueRecords         []OpaqueRecord         `json:"opaque_records"`
	InterpretablePayloads []string               `json:"interpretable_payloads"`
}

type ForwardCompatOpState struct {
	Ref                 string   `json:"ref"`
	Commit              string   `json:"commit"`
	Parents             []string `json:"parents"`
	BlobSHA             string   `json:"blob_sha"`
	CanonicalPayload    string   `json:"canonical_payload"`
	DeclaredDisposition string   `json:"declared_disposition"`
	ObservedDisposition string   `json:"observed_disposition"`
}

type OpaqueRecord struct {
	OpID       string `json:"op_id"`
	ObjectType string `json:"object_type"`
	OpType     string `json:"op_type"`
	OpVersion  int64  `json:"op_version"`
}

func loadProfile(t *testing.T) (codec.Profile, error) {
	t.Helper()
	raw, err := spec.FS.ReadFile("testdata/forward-compat/reader-profile.json")
	if err != nil {
		return codec.Profile{}, fmt.Errorf("read reader profile: %w", err)
	}
	var p codec.Profile
	if err := json.Unmarshal(raw, &p); err != nil {
		return codec.Profile{}, fmt.Errorf("decode reader profile: %w", err)
	}
	return p, nil
}

func runForwardCompatFixture(t *testing.T, fix *fixtures.Fixture) ([]byte, error) {
	t.Helper()

	profile, err := loadProfile(t)
	if err != nil {
		return nil, err
	}

	golden := ForwardCompatGolden{
		ReaderProfile:         profile.Name,
		Ops:                   []ForwardCompatOpState{},
		OpaqueRecords:         []OpaqueRecord{},
		InterpretablePayloads: []string{},
	}

	commitIdx := 0
	for _, ref := range fix.Description.Refs {
		for _, gen := range ref.History {
			gs := fix.Manifest.Generations[commitIdx]
			commitIdx++

			for ci, cd := range gen.Commits {
				cState := gs.Commits[ci]
				commitHash := plumbing.NewHash(cState.SHA)
				commit, err := fix.Repo.CommitObject(commitHash)
				if err != nil {
					return nil, fmt.Errorf("lookup commit %s: %w", cState.SHA, err)
				}

				pureCommit, err := codec.FromGitCommit(fix.Repo.Storer, commit)
				if err != nil {
					return nil, fmt.Errorf("from git commit %s: %w", cState.SHA, err)
				}

				// Fault isolation (FC-12/FC-15): every op in the DAG decodes cleanly
				_, decErr := codec.DecodeCommit(pureCommit)
				if decErr != nil {
					t.Fatalf("fault isolation violation: DecodeCommit failed for commit %s: %v", cState.SHA, decErr)
				}

				var opBlobSHA string
				var opBlobData []byte
				for _, entry := range pureCommit.Tree {
					if entry.Name == "op.json" {
						opBlobSHA = entry.Hash
						opBlobData = entry.Data
						break
					}
				}
				if len(opBlobData) == 0 {
					return nil, fmt.Errorf("commit %s has no op.json blob data", cState.SHA)
				}

				env, err := codec.DecodePayload(opBlobData)
				if err != nil {
					t.Fatalf("DecodePayload failed on commit %s payload: %v", cState.SHA, err)
				}

				canonBytes, err := canonicaljson.Marshal(opBlobData)
				if err != nil {
					return nil, fmt.Errorf("canonicalize op payload %s: %w", cState.SHA, err)
				}
				canonicalPayload := string(canonBytes)

				// Codec leg (FC-11): re-encode must match raw payload bytes byte-identically
				reencoded, err := codec.EncodePayload(env)
				if err != nil {
					t.Fatalf("FC-11 violation: EncodePayload failed for commit %s: %v", cState.SHA, err)
				}
				if !bytes.Equal(reencoded, opBlobData) {
					t.Fatalf("FC-11 violation: re-encoded payload differs from raw for commit %s in fixture %s:\n got:  %s\n want: %s",
						cState.SHA, fix.Name, string(reencoded), string(opBlobData))
				}

				// Declared vs observed disposition check
				observedDisp := profile.Classify(env)
				declaredDisp := cd.Disposition
				if string(observedDisp) != declaredDisp {
					t.Fatalf("disposition mismatch for fixture %s commit %s: declared %q, observed %q",
						fix.Name, cState.SHA, declaredDisp, observedDisp)
				}

				opState := ForwardCompatOpState{
					Ref:                 ref.Name,
					Commit:              commit.Hash.String(),
					Parents:             pureCommit.Parents,
					BlobSHA:             opBlobSHA,
					CanonicalPayload:    canonicalPayload,
					DeclaredDisposition: declaredDisp,
					ObservedDisposition: string(observedDisp),
				}
				golden.Ops = append(golden.Ops, opState)

				if observedDisp == codec.DispositionOpaque {
					golden.OpaqueRecords = append(golden.OpaqueRecords, OpaqueRecord{
						OpID:       commit.Hash.String(),
						ObjectType: env.ObjectType,
						OpType:     env.OpType,
						OpVersion:  env.OpVersion,
					})
				} else if observedDisp == codec.DispositionInterpretable {
					golden.InterpretablePayloads = append(golden.InterpretablePayloads, canonicalPayload)
				}
			}
		}
	}

	sort.Strings(golden.InterpretablePayloads)

	// Field isolation assertion for forward-compat-mixed-dag (FC-4)
	if fix.Name == "forward-compat-mixed-dag" {
		var mixedPayloads, controlPayloads []string
		for _, opState := range golden.Ops {
			if opState.ObservedDisposition == string(codec.DispositionInterpretable) {
				if strings.HasSuffix(opState.Ref, "-control") {
					controlPayloads = append(controlPayloads, opState.CanonicalPayload)
				} else {
					mixedPayloads = append(mixedPayloads, opState.CanonicalPayload)
				}
			}
		}
		sort.Strings(mixedPayloads)
		sort.Strings(controlPayloads)
		if !reflect.DeepEqual(mixedPayloads, controlPayloads) {
			t.Fatalf("FC-4 field isolation violation in %s: interpretable payloads on mixed refs do not match control refs:\nmixed:   %v\ncontrol: %v",
				fix.Name, mixedPayloads, controlPayloads)
		}
	}

	// Sync leg assertion (FC-14): git clone --mirror round-trip
	cloneDir := filepath.Join(t.TempDir(), "mirror-clone")
	cmd := exec.Command("git", "clone", "--mirror", fix.RepoDir, cloneDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("FC-14 sync leg clone --mirror failed for %s: %w\noutput: %s", fix.Name, err, string(out))
	}

	clonedRepo, err := git.PlainOpen(cloneDir)
	if err != nil {
		return nil, fmt.Errorf("open cloned mirror repo %s: %w", cloneDir, err)
	}

	// Assert every ref in the manifest matches in the clone
	for _, refState := range fix.Manifest.Refs {
		refName := plumbing.ReferenceName(refState.Name)
		clonedRef, err := clonedRepo.Reference(refName, true)
		if err != nil {
			t.Fatalf("FC-14 sync leg ref missing in mirror clone: %s: %v", refState.Name, err)
		}
		if clonedRef.Hash().String() != refState.Commit {
			t.Fatalf("FC-14 sync leg ref commit mismatch for %s: got %s, want %s",
				refState.Name, clonedRef.Hash().String(), refState.Commit)
		}
	}

	// Assert every op.json blob SHA in the cloned repo matches
	for _, opState := range golden.Ops {
		clonedCommit, err := clonedRepo.CommitObject(plumbing.NewHash(opState.Commit))
		if err != nil {
			t.Fatalf("FC-14 sync leg lookup commit %s in mirror clone: %v", opState.Commit, err)
		}
		clonedTree, err := clonedCommit.Tree()
		if err != nil {
			t.Fatalf("FC-14 sync leg commit tree %s in mirror clone: %v", opState.Commit, err)
		}
		entry, err := clonedTree.FindEntry("op.json")
		if err != nil {
			t.Fatalf("FC-14 sync leg missing op.json in commit %s in mirror clone: %v", opState.Commit, err)
		}
		if entry.Hash.String() != opState.BlobSHA {
			t.Fatalf("FC-14 sync leg op.json blob SHA mismatch for commit %s in mirror clone: got %s, want %s",
				opState.Commit, entry.Hash.String(), opState.BlobSHA)
		}
	}

	b, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal forward-compat golden: %w", err)
	}
	return append(b, '\n'), nil
}
