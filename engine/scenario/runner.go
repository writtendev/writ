package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/engine/dag"
	"github.com/writtendev/writ/engine/identity"
	"github.com/writtendev/writ/engine/internal/fold"
	"github.com/writtendev/writ/engine/resolve"
	"github.com/writtendev/writ/engine/state"
	writsync "github.com/writtendev/writ/engine/sync"
	"github.com/writtendev/writ/spec/fixtures"
)

type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mutableClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.UTC()
}

func (c *mutableClock) get() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Steps may omit At. Without a default, ops would be stamped with the zero
	// time and `git commit` would reject GIT_AUTHOR_DATE outright.
	if c.now.IsZero() {
		return time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	}
	return c.now
}

type deviceRuntime struct {
	device            Device
	dir               string
	storer            storage.Storer
	syncClient        *writsync.Client
	store             *dag.Store
	clock             *mutableClock
	appendedOps       []string
	appendedOpsByType map[string][]string
}

// TestReporter is the interface subset of *testing.T used by the scenario runner.
type TestReporter interface {
	Helper()
	Fatalf(format string, args ...any)
	Logf(format string, args ...any)
	TempDir() string
	Skip(args ...any)
}

// Run executes a scenario end-to-end against a throwaway bare git remote.
func Run(t TestReporter, s Scenario) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}

	tempDir := t.TempDir()
	remoteDir := filepath.Join(tempDir, "remote.git")

	cmdInit := exec.Command("git", "init", "--bare", remoteDir)
	if out, err := cmdInit.CombinedOutput(); err != nil {
		t.Fatalf("git init bare failed: %v (%s)", err, string(out))
	}

	runtimes := make(map[string]*deviceRuntime)
	var deviceList []*deviceRuntime

	for _, dev := range s.Devices {
		devDir := filepath.Join(tempDir, dev.Name)
		cmdClone := exec.Command("git", "clone", remoteDir, devDir)
		if out, err := cmdClone.CombinedOutput(); err != nil {
			t.Fatalf("git clone %s failed: %v (%s)", dev.Name, err, string(out))
		}

		for k, v := range map[string]string{
			"user.name":     dev.Writer.Name,
			"user.email":    dev.Writer.Email,
			"writ.writerId": string(dev.WriterID),
		} {
			cmdCfg := exec.Command("git", "-C", devDir, "config", k, v)
			if out, err := cmdCfg.CombinedOutput(); err != nil {
				t.Fatalf("git config %s failed: %v (%s)", k, err, string(out))
			}
		}

		ident := identity.Identity{
			WriterID: dev.WriterID,
			Author: identity.Author{
				Name:  dev.Writer.Name,
				Email: dev.Writer.Email,
			},
		}

		syncClient, err := writsync.Open(devDir, ident)
		if err != nil {
			t.Fatalf("writsync.Open %s failed: %v", dev.Name, err)
		}

		if _, err := syncClient.Ensure(context.Background(), "origin"); err != nil {
			t.Fatalf("syncClient.Ensure %s failed: %v", dev.Name, err)
		}

		clock := &mutableClock{}
		store, err := dag.Open(devDir, ident, dag.WithNow(clock.get))
		if err != nil {
			t.Fatalf("dag.Open %s failed: %v", dev.Name, err)
		}

		rt := &deviceRuntime{
			device:            dev,
			dir:               devDir,
			storer:            store.Storer(),
			syncClient:        syncClient,
			store:             store,
			clock:             clock,
			appendedOpsByType: make(map[string][]string),
		}
		runtimes[dev.Name] = rt
		deviceList = append(deviceList, rt)
	}

	for stepIdx, rawStep := range s.Steps {
		switch step := rawStep.(type) {
		case AppendOp:
			rt := runtimes[step.Device.Name]
			if rt == nil {
				t.Fatalf("step %d: unknown device %q in AppendOp", stepIdx, step.Device.Name)
				return
			}
			if !step.At.IsZero() {
				rt.clock.set(step.At)
			}
			op, err := rt.store.Append(context.Background(), step.Envelope, step.CausalParents)
			if err != nil {
				t.Fatalf("step %d: AppendOp on %s failed: %v", stepIdx, step.Device.Name, err)
				return
			}
			rt.appendedOps = append(rt.appendedOps, op.ID)
			rt.appendedOpsByType[step.Envelope.ObjectType] = append(rt.appendedOpsByType[step.Envelope.ObjectType], op.ID)

		case Commit:
			rt := runtimes[step.Device.Name]
			if rt == nil {
				t.Fatalf("step %d: unknown device %q in Commit", stepIdx, step.Device.Name)
				return
			}
			branch := step.Branch
			if branch == "" {
				branch = "main"
			}
			cmdBranch := exec.Command("git", "-C", rt.dir, "checkout", "-B", branch)
			if out, err := cmdBranch.CombinedOutput(); err != nil {
				t.Fatalf("step %d: checkout -B %s failed: %v (%s)", stepIdx, branch, err, string(out))
				return
			}

			for path, content := range step.Files {
				fullPath := filepath.Join(rt.dir, path)
				if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
					t.Fatalf("step %d: mkdir for %s failed: %v", stepIdx, fullPath, err)
					return
				}
				if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
					t.Fatalf("step %d: write %s failed: %v", stepIdx, fullPath, err)
					return
				}
			}

			cmdAdd := exec.Command("git", "-C", rt.dir, "add", "-A")
			if out, err := cmdAdd.CombinedOutput(); err != nil {
				t.Fatalf("step %d: git add on %s failed: %v (%s)", stepIdx, step.Device.Name, err, string(out))
				return
			}

			commitTime := step.At
			if commitTime.IsZero() {
				commitTime = rt.clock.get()
			}
			cmdCommit := exec.Command("git", "-C", rt.dir, "commit", "-m", step.Message)
			cmdCommit.Env = append(os.Environ(),
				fmt.Sprintf("GIT_AUTHOR_NAME=%s", step.Device.Writer.Name),
				fmt.Sprintf("GIT_AUTHOR_EMAIL=%s", step.Device.Writer.Email),
				fmt.Sprintf("GIT_COMMITTER_NAME=%s", step.Device.Writer.Name),
				fmt.Sprintf("GIT_COMMITTER_EMAIL=%s", step.Device.Writer.Email),
				fmt.Sprintf("GIT_AUTHOR_DATE=%d +0000", commitTime.Unix()),
				fmt.Sprintf("GIT_COMMITTER_DATE=%d +0000", commitTime.Unix()),
			)
			if out, err := cmdCommit.CombinedOutput(); err != nil {
				t.Fatalf("step %d: git commit on %s failed: %v (%s)", stepIdx, step.Device.Name, err, string(out))
				return
			}

		case Push:
			rt := runtimes[step.Device.Name]
			if rt == nil {
				t.Fatalf("step %d: unknown device %q in Push", stepIdx, step.Device.Name)
				return
			}
			remote := step.Remote
			if remote == "" {
				remote = "origin"
			}
			_, err := rt.syncClient.Push(context.Background(), remote)
			if step.ExpectedError != nil {
				if !errors.Is(err, step.ExpectedError) {
					t.Fatalf("step %d: Push on %s expected error %v, got %v", stepIdx, step.Device.Name, step.ExpectedError, err)
					return
				}
			} else if err != nil {
				t.Fatalf("step %d: Push on %s failed: %v", stepIdx, step.Device.Name, err)
				return
			}

		case PushBranch:
			rt := runtimes[step.Device.Name]
			if rt == nil {
				t.Fatalf("step %d: unknown device %q in PushBranch", stepIdx, step.Device.Name)
				return
			}
			remote := step.Remote
			if remote == "" {
				remote = "origin"
			}
			branch := step.Branch
			if branch == "" {
				branch = "main"
			}
			args := []string{"-C", rt.dir, "push"}
			if step.Force {
				args = append(args, "--force")
			}
			args = append(args, remote, branch)
			cmdPush := exec.Command("git", args...)
			out, err := cmdPush.CombinedOutput()
			if step.ExpectedError != nil {
				if err == nil {
					t.Fatalf("step %d: PushBranch on %s expected error %v, got success (%s)", stepIdx, step.Device.Name, step.ExpectedError, string(out))
					return
				}
			} else if err != nil {
				t.Fatalf("step %d: PushBranch on %s failed: %v (%s)", stepIdx, step.Device.Name, err, string(out))
				return
			}

		case Fetch:
			rt := runtimes[step.Device.Name]
			if rt == nil {
				t.Fatalf("step %d: unknown device %q in Fetch", stepIdx, step.Device.Name)
				return
			}
			remote := step.Remote
			if remote == "" {
				remote = "origin"
			}
			_, err := rt.syncClient.Fetch(context.Background(), remote)
			if step.ExpectedError != nil {
				if !errors.Is(err, step.ExpectedError) {
					t.Fatalf("step %d: Fetch on %s expected error %v, got %v", stepIdx, step.Device.Name, step.ExpectedError, err)
					return
				}
			} else if err != nil {
				t.Fatalf("step %d: Fetch on %s failed: %v", stepIdx, step.Device.Name, err)
				return
			}

		case ForcePushChain:
			rt := runtimes[step.Device.Name]
			if rt == nil {
				t.Fatalf("step %d: unknown device %q in ForcePushChain", stepIdx, step.Device.Name)
				return
			}
			targetSHA := step.TargetOpSHA
			if targetSHA == "" {
				ops := rt.appendedOpsByType[step.ObjectType]
				if step.TargetOpIndex < 0 || step.TargetOpIndex >= len(ops) {
					t.Fatalf("step %d: TargetOpIndex %d out of range (have %d ops for %s)", stepIdx, step.TargetOpIndex, len(ops), step.ObjectType)
					return
				}
				targetSHA = ops[step.TargetOpIndex]
			}
			remote := step.Remote
			if remote == "" {
				remote = "origin"
			}
			refName := fmt.Sprintf("refs/writ/%s/%s", rt.device.WriterID, step.ObjectType)
			cmdUpd := exec.Command("git", "-C", rt.dir, "update-ref", refName, targetSHA)
			if out, err := cmdUpd.CombinedOutput(); err != nil {
				t.Fatalf("step %d: update-ref %s failed: %v (%s)", stepIdx, refName, err, string(out))
				return
			}
			cmdForce := exec.Command("git", "-C", rt.dir, "push", "--force", remote, fmt.Sprintf("%s:%s", refName, refName))
			out, err := cmdForce.CombinedOutput()
			if step.ExpectedError != nil {
				if err == nil {
					t.Fatalf("step %d: ForcePushChain on %s expected error %v, got success (%s)", stepIdx, step.Device.Name, step.ExpectedError, string(out))
					return
				}
			} else if err != nil {
				t.Fatalf("step %d: ForcePushChain on %s failed: %v (%s)", stepIdx, step.Device.Name, err, string(out))
				return
			}

		case ResetLocalChain:
			rt := runtimes[step.Device.Name]
			if rt == nil {
				t.Fatalf("step %d: unknown device %q in ResetLocalChain", stepIdx, step.Device.Name)
				return
			}
			targetSHA := step.TargetOpSHA
			if targetSHA == "" {
				ops := rt.appendedOpsByType[step.ObjectType]
				if step.TargetOpIndex < 0 || step.TargetOpIndex >= len(ops) {
					t.Fatalf("step %d: TargetOpIndex %d out of range (have %d ops for %s)", stepIdx, step.TargetOpIndex, len(ops), step.ObjectType)
					return
				}
				targetSHA = ops[step.TargetOpIndex]
			}
			refName := fmt.Sprintf("refs/writ/%s/%s", rt.device.WriterID, step.ObjectType)
			cmdUpd := exec.Command("git", "-C", rt.dir, "update-ref", refName, targetSHA)
			if out, err := cmdUpd.CombinedOutput(); err != nil {
				t.Fatalf("step %d: update-ref %s failed: %v (%s)", stepIdx, refName, err, string(out))
				return
			}

		case Converge:
			var dev0Bytes []byte
			for i, rt := range deviceList {
				snapshot, err := buildSnapshot(t, rt, step.AnchorChecks)
				if err != nil {
					t.Fatalf("step %d: build snapshot for device %s failed: %v", stepIdx, rt.device.Name, err)
					return
				}
				raw, err := json.MarshalIndent(snapshot, "", "  ")
				if err != nil {
					t.Fatalf("step %d: marshal snapshot for device %s failed: %v", stepIdx, rt.device.Name, err)
					return
				}
				raw = append(raw, '\n')
				if i == 0 {
					dev0Bytes = raw
				} else {
					if !bytes.Equal(raw, dev0Bytes) {
						diff := fixtures.Diff(
							fmt.Sprintf("device %s (%s)", deviceList[0].device.Name, deviceList[0].device.WriterID),
							dev0Bytes,
							fmt.Sprintf("device %s (%s)", rt.device.Name, rt.device.WriterID),
							raw,
						)
						t.Fatalf("step %d: convergence mismatch between %s and %s:\n%s",
							stepIdx, deviceList[0].device.Name, rt.device.Name, diff)
						return
					}
				}
			}

			goldenName := s.Name
			if step.GoldenName != "" {
				goldenName = step.GoldenName
			}
			goldenPath := filepath.Join("testdata", "golden", goldenName+".json")

			if fixtures.UpdateGolden() && !step.SkipGoldenUpdate {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatalf("step %d: mkdir for golden %s failed: %v", stepIdx, goldenPath, err)
					return
				}
				if err := os.WriteFile(goldenPath, dev0Bytes, 0o644); err != nil {
					t.Fatalf("step %d: write golden %s failed: %v", stepIdx, goldenPath, err)
					return
				}
				t.Logf("[GOLDEN UPDATED] %s (%d bytes)", goldenPath, len(dev0Bytes))
			} else {
				want, err := os.ReadFile(goldenPath)
				if err != nil {
					t.Fatalf("step %d: read golden %s failed (run with -update-golden to generate): %v", stepIdx, goldenPath, err)
					return
				}
				if !bytes.Equal(dev0Bytes, want) {
					diff := fixtures.Diff(goldenPath+" (golden)", want, "actual (converged)", dev0Bytes)
					t.Fatalf("step %d: converged snapshot differs from golden %s:\n%s", stepIdx, goldenPath, diff)
					return
				}
			}

		default:
			t.Fatalf("step %d: unsupported step type %T", stepIdx, rawStep)
		}
	}
}

func buildSnapshot(t TestReporter, rt *deviceRuntime, checks []AnchorCheck) (Snapshot, error) {
	enumRes, err := rt.store.Enumerate()
	if err != nil {
		return Snapshot{}, fmt.Errorf("enumerate: %w", err)
	}

	rules, err := state.BuiltinRules()
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve builtin rules: %w", err)
	}

	var objectIDs []string
	for objID := range enumRes.Ops {
		objectIDs = append(objectIDs, objID)
	}
	sort.Strings(objectIDs)

	var snapshot Snapshot
	for _, id := range objectIDs {
		ops := enumRes.Ops[id]
		if len(ops) == 0 {
			continue
		}
		objectType := fold.DetermineObjectType(ops)
		st, err := writ.Fold(ops, rules[objectType])
		if err != nil {
			return Snapshot{}, fmt.Errorf("fold %s %s: %w", objectType, id, err)
		}
		snapshot.Objects = append(snapshot.Objects, ObjectRecord{
			ObjectID:    id,
			ObjectType:  objectType,
			ObjectState: st,
		})
	}

	// Anchor checks
	for _, chk := range checks {
		rec := findObjectRecord(snapshot.Objects, chk.CommentID)
		if rec == nil {
			return Snapshot{}, fmt.Errorf("anchor check: comment %q not found in snapshot objects", chk.CommentID)
		}
		anchor, ok := decodeAnchor(rec.ObjectState.State["anchor"])
		if !ok {
			return Snapshot{}, fmt.Errorf("anchor check: comment %q has no anchor", chk.CommentID)
		}
		branch := chk.Branch
		if branch == "" {
			branch = "main"
		}
		branchRef, err := storer.ResolveReference(rt.storer, plumbing.ReferenceName("refs/remotes/origin/"+branch))
		if err != nil {
			branchRef, err = storer.ResolveReference(rt.storer, plumbing.ReferenceName("refs/heads/"+branch))
		}
		if err != nil {
			return Snapshot{}, fmt.Errorf("resolve branch %s: %w", branch, err)
		}
		files, err := materializeTree(rt.storer, branchRef.Hash().String())
		if err != nil {
			return Snapshot{}, fmt.Errorf("materialize tree for %s: %w", branchRef.Hash().String(), err)
		}
		tree := resolve.NewTree(files, resolve.SHA1)
		res := resolve.Resolve(anchor, tree)
		status := deriveStatus(res)
		snapshot.Resolutions = append(snapshot.Resolutions, ResolutionRecord{
			CommentID:  chk.CommentID,
			Anchor:     anchor,
			Resolution: res,
			Status:     status,
		})
	}

	sort.Slice(snapshot.Resolutions, func(i, j int) bool {
		return snapshot.Resolutions[i].CommentID < snapshot.Resolutions[j].CommentID
	})

	return snapshot, nil
}

func materializeTree(s storage.Storer, commitHash string) (map[string][]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("nil storer")
	}
	commit, err := object.GetCommit(s, plumbing.NewHash(commitHash))
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
		return nil, fmt.Errorf("read tree files: %w", err)
	}
	return files, nil
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

func findObjectRecord(objects []ObjectRecord, id string) *ObjectRecord {
	for i := range objects {
		if objects[i].ObjectID == id {
			return &objects[i]
		}
	}
	return nil
}

// decodeAnchor recovers a resolve.Anchor from a generically-folded "anchor"
// field: create-once carries the value verbatim as data (spec/fold.md §6),
// so it comes back from writ.Fold as a map[string]any matching the same
// JSON shape resolve.Anchor itself marshals to, not a typed struct.
func decodeAnchor(raw any) (resolve.Anchor, bool) {
	if raw == nil {
		return resolve.Anchor{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return resolve.Anchor{}, false
	}
	var anchor resolve.Anchor
	if err := json.Unmarshal(b, &anchor); err != nil {
		return resolve.Anchor{}, false
	}
	return anchor, true
}
