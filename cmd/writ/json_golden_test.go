package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/writtendev/writ/cmd/writ/internal/wire"
	"github.com/writtendev/writ/engine"
	"github.com/writtendev/writ/spec/fixtures"
)

func maskGoldenTimestamps(data []byte) []byte {
	re := regexp.MustCompile(`"(created_at|updated_at)":"[^"]+"`)
	return re.ReplaceAll(data, []byte(`"$1":"2026-01-01T00:00:00Z"`))
}

// maskGoldenObjectID replaces every occurrence of a freshly minted (32-hex,
// crypto/rand) object id with a fixed placeholder, so a golden file pinning
// `object.*` output stays byte-for-byte stable across runs.
func maskGoldenObjectID(data []byte, id string) []byte {
	return bytes.ReplaceAll(data, []byte(id), []byte("0123456789abcdef0123456789abcdef"))
}

func compareOrUpdateGolden(t *testing.T, goldenName string, got []byte) {
	t.Helper()
	goldenPath := filepath.Join("testdata", "golden", goldenName)

	if fixtures.UpdateGolden() {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden %s failed: %v", goldenPath, err)
		}
		t.Logf("[UPDATED GOLDEN] %s (%d bytes)", goldenPath, len(got))
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("golden file %s does not exist; run with -update-golden to generate it", goldenPath)
		}
		t.Fatalf("read golden %s failed: %v", goldenPath, err)
	}

	if !bytes.Equal(got, want) {
		diff := fixtures.Diff(goldenPath+" (golden)", want, "got (actual output)", got)
		t.Errorf("output does not match golden file %s\n\n%s", goldenPath, diff)
	}
}

func loadFixtureRepo(t *testing.T, descName string) string {
	t.Helper()
	corpus, err := fixtures.LoadCorpus()
	if err != nil {
		t.Fatalf("load corpus failed: %v", err)
	}
	var desc *fixtures.Description
	for _, d := range corpus {
		if d.Name == descName {
			desc = d
			break
		}
	}
	if desc == nil {
		t.Fatalf("fixture description %q not found in corpus", descName)
	}

	repoDir := filepath.Join(t.TempDir(), "repo_"+descName)
	if _, err := fixtures.Generate(desc, repoDir); err != nil {
		t.Fatalf("generate fixture %s failed: %v", descName, err)
	}

	return repoDir
}

func TestGolden_ReviewList_Empty(t *testing.T) {
	env := setupTestCLIEnv(t)
	setupSigningKey(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init failed: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"review", "list", "-C", env.repoDir, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("review list --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "review_list_empty.json", stdout.Bytes())
}

func TestGolden_ReviewList_Single(t *testing.T) {
	repoDir := loadFixtureRepo(t, "fold-concurrent-review-edits")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"review", "list", "-C", repoDir, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("review list --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "review_list_single.json", stdout.Bytes())
}

func TestGolden_ReviewList_Multi(t *testing.T) {
	repoDir := loadFixtureRepo(t, "multi-writer-chains")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"review", "list", "-C", repoDir, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("review list --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "review_list_multi.json", stdout.Bytes())
}

func TestGolden_ReviewStatus_Detail(t *testing.T) {
	repoDir := loadFixtureRepo(t, "review-mixed-signals")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"review", "status", "-C", repoDir, "r-mixed", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("review status --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "review_status_detail.json", stdout.Bytes())
}

func TestGolden_ReviewStatus_UnknownOps(t *testing.T) {
	repoDir := loadFixtureRepo(t, "forward-compat-unknown-ops")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"review", "status", "-C", repoDir, "review-01", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("review status --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "review_status_unknown_ops.json", stdout.Bytes())
}

func TestGolden_IssueList_Empty(t *testing.T) {
	env := setupTestCLIEnv(t)
	setupSigningKey(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", env.repoDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init failed: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"issue", "list", "-C", env.repoDir, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("issue list --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "issue_list_empty.json", stdout.Bytes())
}

func TestGolden_IssueList_Single(t *testing.T) {
	repoDir := loadFixtureRepo(t, "issue-lifecycle")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"issue", "list", "-C", repoDir, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("issue list --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "issue_list_single.json", stdout.Bytes())
}

func TestGolden_IssueStatus_Detail(t *testing.T) {
	repoDir := loadFixtureRepo(t, "issue-concurrent-triage")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"issue", "status", "-C", repoDir, "i-concurrent", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("issue status --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "issue_status_detail.json", stdout.Bytes())
}

func TestGolden_IssueStatus_LinksAndUnknownOps(t *testing.T) {
	repoDir := loadFixtureRepo(t, "issue-cross-repo-links")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"issue", "status", "-C", repoDir, "i-links", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("issue status --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "issue_status_links.json", stdout.Bytes())
}

func TestGolden_IssueLabel(t *testing.T) {
	repoDir := loadFixtureRepo(t, "issue-concurrent-triage")

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"issue", "label", "-C", repoDir, "i-concurrent", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("issue label --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "issue_labels.json", stdout.Bytes())
}

func TestGolden_CommentEdit(t *testing.T) {
	repoDir := loadFixtureRepo(t, "fold-comment-threads")
	setupSigningKey(t, repoDir)

	var initOut, initErr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", repoDir}, &initOut, &initErr)
	if code != 0 {
		t.Fatalf("init failed: %s", initErr.String())
	}

	var stdout, stderr bytes.Buffer
	code = run(context.Background(), []string{"comment", "edit", "-C", repoDir, "c-root", "-m", "Updated root comment for golden", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("comment edit --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "comment_edit.json", maskGoldenTimestamps(stdout.Bytes()))
}

func TestGolden_CommentDelete(t *testing.T) {
	repoDir := loadFixtureRepo(t, "fold-comment-threads")
	setupSigningKey(t, repoDir)

	var initOut, initErr bytes.Buffer
	code := run(context.Background(), []string{"init", "-C", repoDir}, &initOut, &initErr)
	if code != 0 {
		t.Fatalf("init failed: %s", initErr.String())
	}

	var stdout, stderr bytes.Buffer
	code = run(context.Background(), []string{"comment", "delete", "-C", repoDir, "c-root", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("comment delete --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "comment_delete.json", maskGoldenTimestamps(stdout.Bytes()))
}

func TestGolden_SyncStatus(t *testing.T) {
	_, aliceDir, _ := setupSyncTestHarness(t)
	ctx := context.Background()

	// Alice creates a review so there is 1 unsynced op
	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Alice failed: %v", err)
	}
	_, err = sA.Reviews.Create(ctx, writ.NewReview{
		Title: "Unsynced Op Review",
	})
	if err != nil {
		sA.Close()
		t.Fatalf("Alice create review: %v", err)
	}
	sA.Close()

	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"-C", aliceDir, "sync", "--status", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("sync --status --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "sync_status.json", stdout.Bytes())
}

func TestGolden_SyncResult(t *testing.T) {
	_, aliceDir, _ := setupSyncTestHarness(t)
	ctx := context.Background()

	// Alice creates a review and then syncs
	sA, err := writ.Open(aliceDir, writ.WithSigner(dummySigner()))
	if err != nil {
		t.Fatalf("Open Alice failed: %v", err)
	}
	_, err = sA.Reviews.Create(ctx, writ.NewReview{
		Title: "Sync Result Review",
	})
	if err != nil {
		sA.Close()
		t.Fatalf("Alice create review: %v", err)
	}
	sA.Close()

	var stdout, stderr bytes.Buffer
	code := run(ctx, []string{"-C", aliceDir, "sync", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("sync --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "sync_result.json", stdout.Bytes())
}

// TestGolden_SchemaShow pins `writ schema show <type> --json`. Store.Types
// sorts by name and the payload carries no ids or timestamps, so it needs
// no masking.
func TestGolden_SchemaShow(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"schema", "show", "-C", env.repoDir, "ticket", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("schema show --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "schema_show.json", stdout.Bytes())
}

// TestGolden_ObjectCreate pins `writ object create --json`. The object id
// is a fresh crypto/rand value every run, so it is masked to a fixed
// placeholder before comparison.
func TestGolden_ObjectCreate(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-field", "title=Fix the thing",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object create --json failed with %d; stderr: %s", code, stderr.String())
	}
	var created wire.ObjectCreated
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectCreate, &created)

	compareOrUpdateGolden(t, "object_create.json", maskGoldenObjectID(stdout.Bytes(), created.ObjectID))
}

// TestGolden_ObjectApply pins `writ object apply --json`, masking the
// existing object's id the same way TestGolden_ObjectCreate does.
func TestGolden_ObjectApply(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-field", "title=Fix the thing",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object create failed with %d; stderr: %s", code, stderr.String())
	}
	var created wire.ObjectCreated
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectCreate, &created)
	objectID := created.ObjectID

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{
		"object", "apply", "-C", env.repoDir, objectID, "update",
		"-field", "title=Renamed",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object apply --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "object_apply.json", maskGoldenObjectID(stdout.Bytes(), objectID))
}

// TestGolden_ObjectShow pins `writ object show --json`, folded straight
// from the DAG. The object id is masked the same way as create/apply.
func TestGolden_ObjectShow(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-field", "title=Fix the thing",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object create failed with %d; stderr: %s", code, stderr.String())
	}
	var created wire.ObjectCreated
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectCreate, &created)
	objectID := created.ObjectID

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{
		"object", "apply", "-C", env.repoDir, objectID, "update",
		"-field", "title=Renamed",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object apply failed with %d; stderr: %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"object", "show", "-C", env.repoDir, objectID, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object show --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "object_show.json", maskGoldenObjectID(stdout.Bytes(), objectID))
}

// TestGolden_ObjectList pins `writ object list --json`. Both the object id
// and the created_at/updated_at timestamps vary run to run, so both are
// masked before comparison.
func TestGolden_ObjectList(t *testing.T) {
	env := initTestRepo(t)
	applyTicketObjectSchema(t, env.repoDir)

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"object", "create", "-C", env.repoDir, "ticket", "create",
		"-field", "title=Fix the thing",
		"--json",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object create failed with %d; stderr: %s", code, stderr.String())
	}
	var created wire.ObjectCreated
	unmarshalEnvelopeData(t, stdout.Bytes(), wire.KindObjectCreate, &created)
	objectID := created.ObjectID

	stdout.Reset()
	stderr.Reset()
	code = run(context.Background(), []string{"object", "list", "-C", env.repoDir, "ticket", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("object list --json failed with %d; stderr: %s", code, stderr.String())
	}

	compareOrUpdateGolden(t, "object_list.json", maskGoldenObjectID(maskGoldenTimestamps(stdout.Bytes()), objectID))
}

func TestDeterminism_AllReadVerbs(t *testing.T) {
	repoDir := loadFixtureRepo(t, "review-mixed-signals")
	ctx := context.Background()

	issueRepoDir := loadFixtureRepo(t, "issue-concurrent-triage")

	readCommands := [][]string{
		{"review", "list", "-C", repoDir, "--json"},
		{"review", "status", "-C", repoDir, "r-mixed", "--json"},
		{"issue", "list", "-C", issueRepoDir, "--json"},
		{"issue", "status", "-C", issueRepoDir, "i-concurrent", "--json"},
		{"issue", "label", "-C", issueRepoDir, "i-concurrent", "--json"},
	}

	for _, cmd := range readCommands {
		var out1, err1 bytes.Buffer
		code1 := run(ctx, cmd, &out1, &err1)
		if code1 != 0 {
			t.Fatalf("run 1 for %v failed: %s", cmd, err1.String())
		}

		var out2, err2 bytes.Buffer
		code2 := run(ctx, cmd, &out2, &err2)
		if code2 != 0 {
			t.Fatalf("run 2 for %v failed: %s", cmd, err2.String())
		}

		if !bytes.Equal(out1.Bytes(), out2.Bytes()) {
			t.Fatalf("determinism violation for command %v:\nrun 1: %s\nrun 2: %s", cmd, out1.String(), out2.String())
		}
	}
}

func TestEveryReadVerbHasJSON(t *testing.T) {
	readVerbs := []struct {
		name string
		args []string
	}{
		{name: "review list", args: []string{"review", "list", "-h"}},
		{name: "review status", args: []string{"review", "status", "-h"}},
		{name: "issue list", args: []string{"issue", "list", "-h"}},
		{name: "issue status", args: []string{"issue", "status", "-h"}},
		{name: "issue label", args: []string{"issue", "label", "-h"}},
		{name: "sync", args: []string{"sync", "-h"}},
	}

	for _, tc := range readVerbs {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), tc.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("help for %s exited with %d; stderr: %s", tc.name, code, stderr.String())
			}

			combined := stdout.String() + stderr.String()
			if !strings.Contains(combined, "--json") && !strings.Contains(combined, "-json") {
				t.Errorf("read verb %q help output does not advertise --json flag:\n%s", tc.name, combined)
			}
		})
	}
}
