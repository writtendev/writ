package fixtures

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type spyReporter struct {
	failed  bool
	fatals  []string
	errors  []string
	logs    []string
	tempDir string
}

func (s *spyReporter) Helper() {}
func (s *spyReporter) Fatalf(format string, args ...any) {
	s.failed = true
	s.fatals = append(s.fatals, fmt.Sprintf(format, args...))
}
func (s *spyReporter) Errorf(format string, args ...any) {
	s.failed = true
	s.errors = append(s.errors, fmt.Sprintf(format, args...))
}
func (s *spyReporter) Logf(format string, args ...any) {
	s.logs = append(s.logs, fmt.Sprintf(format, args...))
}
func (s *spyReporter) TempDir() string {
	return s.tempDir
}

func TestHarness_RunFamilyManifest(t *testing.T) {
	// One-line registration for the manifest fixture family
	RunFamily(t, "manifest", func(t *testing.T, fix *Fixture) ([]byte, error) {
		return marshalManifest(t, fix.Manifest), nil
	})
}

func TestHarness_MismatchShowsDiff(t *testing.T) {
	descs, err := LoadCorpus()
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}

	tempGoldenDir := t.TempDir()
	goldenContent := []byte("{\n  \"dummy\": \"expected-golden-value\"\n}\n")
	if err := os.WriteFile(filepath.Join(tempGoldenDir, descs[0].Name+".json"), goldenContent, 0o644); err != nil {
		t.Fatalf("write temp golden: %v", err)
	}

	spy := &spyReporter{tempDir: t.TempDir()}
	runner := func(tr TestReporter, fix *Fixture) ([]byte, error) {
		return []byte("{\n  \"dummy\": \"actual-test-value\"\n}\n"), nil
	}

	runSingleFixture(spy, descs[0], tempGoldenDir, ".json", false, runner)

	if !spy.failed {
		t.Fatal("expected fixture check to fail on mismatch, but it succeeded")
	}
	if len(spy.errors) == 0 {
		t.Fatal("expected error message with diff, got none")
	}

	diffOutput := spy.errors[0]
	if !strings.Contains(diffOutput, "-  \"dummy\": \"expected-golden-value\"") ||
		!strings.Contains(diffOutput, "+  \"dummy\": \"actual-test-value\"") {
		t.Errorf("error output did not contain expected diff:\n%s", diffOutput)
	}
}

func TestHarness_MissingGoldenFailsWithHelpfulError(t *testing.T) {
	descs, err := LoadCorpus()
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}

	emptyGoldenDir := t.TempDir()
	spy := &spyReporter{tempDir: t.TempDir()}
	runner := func(tr TestReporter, fix *Fixture) ([]byte, error) {
		return []byte("test output\n"), nil
	}

	runSingleFixture(spy, descs[0], emptyGoldenDir, ".json", false, runner)

	if !spy.failed {
		t.Fatal("expected fixture check to fail when golden is missing")
	}
	if len(spy.fatals) == 0 || !strings.Contains(spy.fatals[0], "does not exist; run with -update-golden to create it") {
		t.Errorf("expected missing golden guidance message, got: %v", spy.fatals)
	}
}

func TestHarness_UpdateGoldenFlow(t *testing.T) {
	descs, err := LoadCorpus()
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}

	tempGoldenDir := t.TempDir()
	goldenPath := filepath.Join(tempGoldenDir, descs[0].Name+".json")

	// 1. Initial creation (New Golden)
	initialOutput := []byte("{\n  \"version\": 1\n}\n")
	spy1 := &spyReporter{tempDir: t.TempDir()}
	runSingleFixture(spy1, descs[0], tempGoldenDir, ".json", true, func(tr TestReporter, fix *Fixture) ([]byte, error) {
		return initialOutput, nil
	})

	if spy1.failed {
		t.Fatalf("unexpected failure during initial golden creation: %v", spy1.fatals)
	}
	createdBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read created golden: %v", err)
	}
	if !bytes.Equal(createdBytes, initialOutput) {
		t.Fatalf("created golden content mismatch:\ngot:\n%s\nwant:\n%s", createdBytes, initialOutput)
	}
	if len(spy1.logs) == 0 || !strings.Contains(spy1.logs[0], "[NEW GOLDEN]") {
		t.Errorf("expected [NEW GOLDEN] log, got: %v", spy1.logs)
	}

	// 2. Unchanged update
	spy2 := &spyReporter{tempDir: t.TempDir()}
	runSingleFixture(spy2, descs[0], tempGoldenDir, ".json", true, func(tr TestReporter, fix *Fixture) ([]byte, error) {
		return initialOutput, nil
	})
	if len(spy2.logs) == 0 || !strings.Contains(spy2.logs[0], "[UNCHANGED]") {
		t.Errorf("expected [UNCHANGED] log, got: %v", spy2.logs)
	}

	// 3. Deliberate modification update (Updated Golden with diff)
	updatedOutput := []byte("{\n  \"version\": 2\n}\n")
	spy3 := &spyReporter{tempDir: t.TempDir()}
	runSingleFixture(spy3, descs[0], tempGoldenDir, ".json", true, func(tr TestReporter, fix *Fixture) ([]byte, error) {
		return updatedOutput, nil
	})

	if spy3.failed {
		t.Fatalf("unexpected failure during golden update: %v", spy3.fatals)
	}
	updatedBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read updated golden: %v", err)
	}
	if !bytes.Equal(updatedBytes, updatedOutput) {
		t.Fatalf("updated golden content mismatch:\ngot:\n%s\nwant:\n%s", updatedBytes, updatedOutput)
	}
	if len(spy3.logs) == 0 || !strings.Contains(spy3.logs[0], "[UPDATED GOLDEN]") || !strings.Contains(spy3.logs[0], "-  \"version\": 1") || !strings.Contains(spy3.logs[0], "+  \"version\": 2") {
		t.Errorf("expected [UPDATED GOLDEN] log with diff, got: %v", spy3.logs)
	}
}

func TestHarness_CustomCorpusAndFilter(t *testing.T) {
	corpus, err := LoadCorpus()
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}

	ranCount := 0
	family := Family{
		Name:      "custom",
		GoldenDir: "testdata/golden",
		Corpus:    corpus,
		Filter: func(desc *Description) bool {
			return desc.Name == "multi-writer-refs"
		},
		Runner: func(t *testing.T, fix *Fixture) ([]byte, error) {
			ranCount++
			if fix.Name != "multi-writer-refs" {
				t.Errorf("unexpected fixture ran: %s", fix.Name)
			}
			return marshalManifest(t, fix.Manifest), nil
		},
	}

	Run(t, family)

	if ranCount != 1 {
		t.Errorf("expected exactly 1 fixture to run with filter, got %d", ranCount)
	}
}

func TestHarness_CustomCorpusDir(t *testing.T) {
	family := Family{
		Name:      "from-dir",
		GoldenDir: "testdata/golden",
		CorpusDir: "testdata/descriptions",
		Filter: func(desc *Description) bool {
			return desc.Name == "linear-history"
		},
		Runner: func(t *testing.T, fix *Fixture) ([]byte, error) {
			return marshalManifest(t, fix.Manifest), nil
		},
	}

	Run(t, family)
}

func TestUpdateGoldenFlag(t *testing.T) {
	os.Setenv("WRIT_UPDATE_GOLDEN", "1")
	if !UpdateGolden() {
		t.Error("expected UpdateGolden() to be true when WRIT_UPDATE_GOLDEN=1")
	}
	os.Unsetenv("WRIT_UPDATE_GOLDEN")

	os.Setenv("UPDATE_GOLDEN", "true")
	if !UpdateGolden() {
		t.Error("expected UpdateGolden() to be true when UPDATE_GOLDEN=true")
	}
	os.Unsetenv("UPDATE_GOLDEN")
}

func TestHarness_ResolveGoldenDir(t *testing.T) {
	// Manifest family defaults to testdata/golden
	if got := resolveGoldenDir("", "manifest"); got != filepath.Join("testdata", "golden") {
		t.Errorf("expected manifest to resolve to testdata/golden, got: %s", got)
	}

	// Empty family name defaults to testdata/golden
	if got := resolveGoldenDir("", ""); got != filepath.Join("testdata", "golden") {
		t.Errorf("expected empty name to resolve to testdata/golden, got: %s", got)
	}

	// New family name (non-manifest) resolves to testdata/golden/<name> even if directory does not exist
	if got := resolveGoldenDir("", "fold"); got != filepath.Join("testdata", "golden", "fold") {
		t.Errorf("expected fold to resolve to testdata/golden/fold, got: %s", got)
	}

	// Configured directory overrides family name
	if got := resolveGoldenDir("custom/golden/path", "fold"); got != "custom/golden/path" {
		t.Errorf("expected configured dir override, got: %s", got)
	}
}

type mockTestRunner struct {
	spyReporter
	subtestCount int
}

func (m *mockTestRunner) Run(name string, f func(t *testing.T)) bool {
	m.subtestCount++
	return true
}

func (m *mockTestRunner) Skip(args ...any) {}

func TestHarness_FilterMatchesZeroFails(t *testing.T) {
	family := Family{
		Name: "test-zero-match",
		Filter: func(desc *Description) bool {
			return desc.Name == "non-existent-fixture-typo"
		},
		Runner: func(t *testing.T, fix *Fixture) ([]byte, error) {
			return []byte("output"), nil
		},
	}

	mock := &mockTestRunner{spyReporter: spyReporter{tempDir: t.TempDir()}}
	runFamily(mock, family)

	if !mock.failed {
		t.Fatal("expected runFamily to fail when 0 fixtures match the filter, but it succeeded")
	}
	if len(mock.fatals) == 0 || !strings.Contains(mock.fatals[0], "no fixtures matched the specified filter") {
		t.Errorf("expected fatal error about 0 matching fixtures, got: %v", mock.fatals)
	}
	if mock.subtestCount != 0 {
		t.Errorf("expected 0 subtests to run, got %d", mock.subtestCount)
	}
}

func TestHarness_CommitSHALookup(t *testing.T) {
	family := Family{
		Name:      "test-commit-sha",
		GoldenDir: "testdata/golden",
		Filter: func(desc *Description) bool {
			return desc.Name == "envelope-valid-ops"
		},
		Runner: func(t *testing.T, fix *Fixture) ([]byte, error) {
			sha, ok := fix.CommitSHA("alice-root")
			if !ok || sha == "" {
				t.Fatalf("expected to find SHA for alice-root, got sha=%q, ok=%v", sha, ok)
			}
			if _, ok := fix.CommitSHA("nonexistent-label"); ok {
				t.Fatalf("expected false for nonexistent label")
			}
			ref := fix.TargetRef(sha)
			if ref != "refs/writ/alice/widget" {
				t.Fatalf("expected target ref refs/writ/alice/widget, got %q", ref)
			}
			return marshalManifest(t, fix.Manifest), nil
		},
	}

	Run(t, family)
}

