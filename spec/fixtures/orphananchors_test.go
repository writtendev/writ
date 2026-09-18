package fixtures_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/writtendev/writ/internal/resolve"
	"github.com/writtendev/writ/spec"
	"github.com/writtendev/writ/spec/fixtures"
)

// TestOrphanAnchorsFamily registers the orphan-anchors fixture family and runs
// all orphan-anchors-*.yaml descriptions through the golden test harness.
func TestOrphanAnchorsFamily(t *testing.T) {
	fixtures.Run(t, fixtures.Family{
		Name:      "orphan-anchors",
		GoldenDir: "testdata/golden/orphan-anchors",
		Filter: func(desc *fixtures.Description) bool {
			return strings.HasPrefix(desc.Name, "orphan-anchors-")
		},
		Runner: runOrphanAnchorsFixture,
	})
}

type OrphanAnchorsGolden struct {
	Cases []OrphanAnchorCaseGolden `json:"cases"`
}

type OrphanAnchorCaseGolden struct {
	Name       string             `json:"name"`
	Source     AnchorSource       `json:"source"`
	Target     AnchorTarget       `json:"target"`
	Anchor     resolve.Anchor     `json:"anchor"`
	Resolution resolve.Resolution `json:"resolution"`
	Status     string             `json:"status"`
}

type AnchorSource struct {
	Commit string         `json:"commit"`
	Path   string         `json:"path"`
	Range  *resolve.Range `json:"range,omitempty"`
}

type AnchorTarget struct {
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
}

func compileAnchorSchemas(t *testing.T) (*jsonschema.Schema, *jsonschema.Schema) {
	t.Helper()

	anchorRaw, err := spec.FS.ReadFile("schemas/anchor.schema.json")
	if err != nil {
		t.Fatalf("read anchor schema: %v", err)
	}
	anchorDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(anchorRaw))
	if err != nil {
		t.Fatalf("unmarshal anchor schema: %v", err)
	}

	resolutionRaw, err := spec.FS.ReadFile("schemas/resolution.schema.json")
	if err != nil {
		t.Fatalf("read resolution schema: %v", err)
	}
	resolutionDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(resolutionRaw))
	if err != nil {
		t.Fatalf("unmarshal resolution schema: %v", err)
	}

	c := jsonschema.NewCompiler()
	if err := c.AddResource("https://writ.dev/spec/anchor.schema.json", anchorDoc); err != nil {
		t.Fatalf("add anchor schema resource: %v", err)
	}
	if err := c.AddResource("https://writ.dev/spec/resolution.schema.json", resolutionDoc); err != nil {
		t.Fatalf("add resolution schema resource: %v", err)
	}

	anchorSch, err := c.Compile("https://writ.dev/spec/anchor.schema.json")
	if err != nil {
		t.Fatalf("compile anchor schema: %v", err)
	}
	resSch, err := c.Compile("https://writ.dev/spec/resolution.schema.json")
	if err != nil {
		t.Fatalf("compile resolution schema: %v", err)
	}

	return anchorSch, resSch
}

func materializeTree(repo *git.Repository, commitHash string) (map[string][]byte, error) {
	commit, err := repo.CommitObject(plumbing.NewHash(commitHash))
	if err != nil {
		return nil, fmt.Errorf("lookup commit %s: %w", commitHash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("lookup tree %s: %w", commitHash, err)
	}

	files := make(map[string][]byte)
	err = tree.Files().ForEach(func(f *object.File) error {
		contents, err := f.Contents()
		if err != nil {
			return err
		}
		files[f.Name] = []byte(contents)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read tree files %s: %w", commitHash, err)
	}
	return files, nil
}

func captureSide(tree *resolve.Tree, commitSHA, path string, rng *resolve.Range) (*resolve.SideAnchor, error) {
	file := tree.File(path)
	if file == nil {
		return nil, fmt.Errorf("file %q not found in tree", path)
	}

	side := &resolve.SideAnchor{
		Commit: commitSHA,
		Path:   path,
		Blob:   file.Blob,
	}

	if rng == nil {
		return side, nil
	}

	if rng.Start < 1 || rng.End < rng.Start || rng.End > len(file.Lines) {
		return nil, fmt.Errorf("invalid range [%d, %d] for file %q (total lines %d)", rng.Start, rng.End, path, len(file.Lines))
	}

	side.Range = &resolve.Range{Start: rng.Start, End: rng.End}

	beforeStart := rng.Start - 3
	if beforeStart < 1 {
		beforeStart = 1
	}
	before := make([]string, 0, rng.Start-beforeStart)
	for i := beforeStart; i < rng.Start; i++ {
		before = append(before, file.Lines[i-1])
	}

	afterEnd := rng.End + 3
	if afterEnd > len(file.Lines) {
		afterEnd = len(file.Lines)
	}
	after := make([]string, 0, afterEnd-rng.End)
	for i := rng.End + 1; i <= afterEnd; i++ {
		after = append(after, file.Lines[i-1])
	}

	rangeLen := rng.End - rng.Start + 1
	var ctxLines []string
	var omitted int

	if rangeLen <= 64 {
		ctxLines = make([]string, rangeLen)
		copy(ctxLines, file.Lines[rng.Start-1:rng.End])
	} else {
		ctxLines = make([]string, 64)
		copy(ctxLines[:32], file.Lines[rng.Start-1:rng.Start-1+32])
		copy(ctxLines[32:], file.Lines[rng.End-32:rng.End])
		omitted = rangeLen - 64
	}

	side.Context = &resolve.Context{
		Before:  before,
		Lines:   ctxLines,
		Omitted: omitted,
		After:   after,
	}

	return side, nil
}

func deriveStatus(res resolve.Resolution) string {
	hasResolved := false
	hasOrphaned := false

	if res.Old != nil {
		if res.Old.Outcome == resolve.OutcomeResolved {
			hasResolved = true
		} else if res.Old.Outcome == resolve.OutcomeOrphaned {
			hasOrphaned = true
		}
	}
	if res.New != nil {
		if res.New.Outcome == resolve.OutcomeResolved {
			hasResolved = true
		} else if res.New.Outcome == resolve.OutcomeOrphaned {
			hasOrphaned = true
		}
	}

	if hasResolved && hasOrphaned {
		return "partially-resolved"
	}
	if hasResolved {
		return "resolved"
	}
	return "orphaned"
}

func runOrphanAnchorsFixture(t *testing.T, fix *fixtures.Fixture) ([]byte, error) {
	t.Helper()

	anchorSch, resSch := compileAnchorSchemas(t)

	golden := OrphanAnchorsGolden{
		Cases: make([]OrphanAnchorCaseGolden, 0, len(fix.Description.Resolutions)),
	}

	for _, caseDesc := range fix.Description.Resolutions {
		targetSHA, ok := fix.CommitSHA(caseDesc.Target)
		if !ok {
			return nil, fmt.Errorf("case %q: unknown target label %q", caseDesc.Name, caseDesc.Target)
		}
		targetRef := fix.TargetRef(targetSHA)

		targetFiles, err := materializeTree(fix.Repo, targetSHA)
		if err != nil {
			return nil, fmt.Errorf("case %q: materialize target tree: %w", caseDesc.Name, err)
		}
		targetTree := resolve.NewTree(targetFiles, resolve.SHA1)

		var anchor resolve.Anchor
		anchor.Version = 1

		var srcSummary AnchorSource

		if caseDesc.Anchor.Old != nil || caseDesc.Anchor.New != nil {
			if caseDesc.Anchor.Old != nil {
				oldSHA, ok := fix.CommitSHA(caseDesc.Anchor.Old.At)
				if !ok {
					return nil, fmt.Errorf("case %q: unknown old at %q", caseDesc.Name, caseDesc.Anchor.Old.At)
				}
				oldFiles, err := materializeTree(fix.Repo, oldSHA)
				if err != nil {
					return nil, fmt.Errorf("case %q: materialize old tree: %w", caseDesc.Name, err)
				}
				oldTree := resolve.NewTree(oldFiles, resolve.SHA1)

				var oldRng *resolve.Range
				if len(caseDesc.Anchor.Old.Range) == 2 {
					oldRng = &resolve.Range{Start: caseDesc.Anchor.Old.Range[0], End: caseDesc.Anchor.Old.Range[1]}
				}
				oldSide, err := captureSide(oldTree, oldSHA, caseDesc.Anchor.Old.Path, oldRng)
				if err != nil {
					return nil, fmt.Errorf("case %q: capture old side: %w", caseDesc.Name, err)
				}
				anchor.Old = oldSide
				srcSummary = AnchorSource{Commit: oldSHA, Path: caseDesc.Anchor.Old.Path, Range: oldRng}
			}
			if caseDesc.Anchor.New != nil {
				newSHA, ok := fix.CommitSHA(caseDesc.Anchor.New.At)
				if !ok {
					return nil, fmt.Errorf("case %q: unknown new at %q", caseDesc.Name, caseDesc.Anchor.New.At)
				}
				newFiles, err := materializeTree(fix.Repo, newSHA)
				if err != nil {
					return nil, fmt.Errorf("case %q: materialize new tree: %w", caseDesc.Name, err)
				}
				newTree := resolve.NewTree(newFiles, resolve.SHA1)

				var newRng *resolve.Range
				if len(caseDesc.Anchor.New.Range) == 2 {
					newRng = &resolve.Range{Start: caseDesc.Anchor.New.Range[0], End: caseDesc.Anchor.New.Range[1]}
				}
				newSide, err := captureSide(newTree, newSHA, caseDesc.Anchor.New.Path, newRng)
				if err != nil {
					return nil, fmt.Errorf("case %q: capture new side: %w", caseDesc.Name, err)
				}
				anchor.New = newSide
				srcSummary = AnchorSource{Commit: newSHA, Path: caseDesc.Anchor.New.Path, Range: newRng}
			}
		} else {
			srcSHA, ok := fix.CommitSHA(caseDesc.Anchor.At)
			if !ok {
				return nil, fmt.Errorf("case %q: unknown anchor at %q", caseDesc.Name, caseDesc.Anchor.At)
			}
			srcFiles, err := materializeTree(fix.Repo, srcSHA)
			if err != nil {
				return nil, fmt.Errorf("case %q: materialize src tree: %w", caseDesc.Name, err)
			}
			srcTree := resolve.NewTree(srcFiles, resolve.SHA1)

			var rng *resolve.Range
			if len(caseDesc.Anchor.Range) == 2 {
				rng = &resolve.Range{Start: caseDesc.Anchor.Range[0], End: caseDesc.Anchor.Range[1]}
			}

			sideName := caseDesc.Anchor.Side
			if sideName == "" {
				sideName = "new"
			}

			if sideName == "old" || sideName == "both" {
				oldSide, err := captureSide(srcTree, srcSHA, caseDesc.Anchor.Path, rng)
				if err != nil {
					return nil, fmt.Errorf("case %q: capture old side: %w", caseDesc.Name, err)
				}
				anchor.Old = oldSide
			}
			if sideName == "new" || sideName == "both" {
				newSide, err := captureSide(srcTree, srcSHA, caseDesc.Anchor.Path, rng)
				if err != nil {
					return nil, fmt.Errorf("case %q: capture new side: %w", caseDesc.Name, err)
				}
				anchor.New = newSide
			}
			srcSummary = AnchorSource{Commit: srcSHA, Path: caseDesc.Anchor.Path, Range: rng}
		}

		// 1. Validate anchor against anchor.schema.json
		anchorBytes, err := json.Marshal(anchor)
		if err != nil {
			return nil, fmt.Errorf("case %q: marshal anchor: %w", caseDesc.Name, err)
		}
		anchorDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(anchorBytes))
		if err != nil {
			t.Fatalf("case %q: unmarshal anchor JSON for schema validation: %v", caseDesc.Name, err)
		}
		if err := anchorSch.Validate(anchorDoc); err != nil {
			t.Fatalf("case %q: emitted anchor failed schema validation: %v", caseDesc.Name, err)
		}

		// 2. Resolve anchor against target tree
		res := resolve.Resolve(anchor, targetTree)

		// 3. Validate resolution against resolution.schema.json
		resBytes, err := json.Marshal(res)
		if err != nil {
			return nil, fmt.Errorf("case %q: marshal resolution: %w", caseDesc.Name, err)
		}
		resDoc, err := jsonschema.UnmarshalJSON(bytes.NewReader(resBytes))
		if err != nil {
			t.Fatalf("case %q: unmarshal resolution JSON for schema validation: %v", caseDesc.Name, err)
		}
		if err := resSch.Validate(resDoc); err != nil {
			t.Fatalf("case %q: resolution output failed schema validation: %v", caseDesc.Name, err)
		}

		status := deriveStatus(res)

		// 4. Assert declared vs observed outcome/match/reason
		if caseDesc.Expect.Status != "" && status != caseDesc.Expect.Status {
			t.Fatalf("case %q: status mismatch: got %q, want %q", caseDesc.Name, status, caseDesc.Expect.Status)
		}
		if caseDesc.Expect.Old != nil {
			if res.Old == nil {
				t.Fatalf("case %q: expected old resolution, got nil", caseDesc.Name)
			}
			if res.Old.Outcome != caseDesc.Expect.Old.Outcome {
				t.Fatalf("case %q: old outcome mismatch: got %q, want %q", caseDesc.Name, res.Old.Outcome, caseDesc.Expect.Old.Outcome)
			}
			if caseDesc.Expect.Old.Match != "" && res.Old.Match != caseDesc.Expect.Old.Match {
				t.Fatalf("case %q: old match mismatch: got %q, want %q", caseDesc.Name, res.Old.Match, caseDesc.Expect.Old.Match)
			}
			if caseDesc.Expect.Old.Reason != "" && res.Old.Reason != caseDesc.Expect.Old.Reason {
				t.Fatalf("case %q: old reason mismatch: got %q, want %q", caseDesc.Name, res.Old.Reason, caseDesc.Expect.Old.Reason)
			}
		}
		if caseDesc.Expect.New != nil {
			if res.New == nil {
				t.Fatalf("case %q: expected new resolution, got nil", caseDesc.Name)
			}
			if res.New.Outcome != caseDesc.Expect.New.Outcome {
				t.Fatalf("case %q: new outcome mismatch: got %q, want %q", caseDesc.Name, res.New.Outcome, caseDesc.Expect.New.Outcome)
			}
			if caseDesc.Expect.New.Match != "" && res.New.Match != caseDesc.Expect.New.Match {
				t.Fatalf("case %q: new match mismatch: got %q, want %q", caseDesc.Name, res.New.Match, caseDesc.Expect.New.Match)
			}
			if caseDesc.Expect.New.Reason != "" && res.New.Reason != caseDesc.Expect.New.Reason {
				t.Fatalf("case %q: new reason mismatch: got %q, want %q", caseDesc.Name, res.New.Reason, caseDesc.Expect.New.Reason)
			}
		}
		if caseDesc.Expect.Outcome != "" {
			var sideRes *resolve.SideResult
			if caseDesc.Anchor.Side == "old" {
				sideRes = res.Old
			} else {
				sideRes = res.New
			}
			if sideRes == nil {
				t.Fatalf("case %q: expected side resolution, got nil", caseDesc.Name)
			}
			if sideRes.Outcome != caseDesc.Expect.Outcome {
				t.Fatalf("case %q: outcome mismatch: got %q, want %q", caseDesc.Name, sideRes.Outcome, caseDesc.Expect.Outcome)
			}
			if caseDesc.Expect.Match != "" && sideRes.Match != caseDesc.Expect.Match {
				t.Fatalf("case %q: match mismatch: got %q, want %q", caseDesc.Name, sideRes.Match, caseDesc.Expect.Match)
			}
			if caseDesc.Expect.Reason != "" && sideRes.Reason != caseDesc.Expect.Reason {
				t.Fatalf("case %q: reason mismatch: got %q, want %q", caseDesc.Name, sideRes.Reason, caseDesc.Expect.Reason)
			}
		}

		// 5. Assert orphan preservation: input anchor bytes must match resolution.anchor bytes
		hasOrphanedSide := (res.Old != nil && res.Old.Outcome == resolve.OutcomeOrphaned) ||
			(res.New != nil && res.New.Outcome == resolve.OutcomeOrphaned)
		if hasOrphanedSide {
			resAnchorBytes, err := json.Marshal(res.Anchor)
			if err != nil {
				t.Fatalf("case %q: marshal res.Anchor: %v", caseDesc.Name, err)
			}
			if !bytes.Equal(anchorBytes, resAnchorBytes) {
				t.Fatalf("case %q: orphan preservation failure: res.Anchor differs from captured anchor:\n got:  %s\n want: %s",
					caseDesc.Name, string(resAnchorBytes), string(anchorBytes))
			}
		}

		golden.Cases = append(golden.Cases, OrphanAnchorCaseGolden{
			Name:       caseDesc.Name,
			Source:     srcSummary,
			Target:     AnchorTarget{Ref: targetRef, Commit: targetSHA},
			Anchor:     anchor,
			Resolution: res,
			Status:     status,
		})
	}

	b, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal orphan anchors golden: %w", err)
	}
	return append(b, '\n'), nil
}
