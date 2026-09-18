package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The module path api-compat passes in as -v mod=.
const testMod = "github.com/writtendev/writ"

// filter runs apicompat.awk over report the way `make api-compat` does.
func filter(t *testing.T, report string) string {
	t.Helper()
	if _, err := exec.LookPath("awk"); err != nil {
		t.Skip("awk is not installed")
	}
	cmd := exec.Command("awk", "-f", "apicompat.awk", "-v", "mod="+testMod)
	cmd.Stdin = strings.NewReader(report)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	var out, errs bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errs
	if err := cmd.Run(); err != nil {
		t.Fatalf("awk: %v\nstderr: %s", err, errs.String())
	}
	return out.String()
}

// apidiff names a symbol relative to the module root ("./engine/resolve.X")
// but names a whole package by its full import path, so a filter that knows
// only the first spelling drops the single most breaking change there is:
// a public subpackage of engine disappearing. Both directions are covered —
// a package added on this side, and one that existed at the base and is gone.
func TestFilterKeepsWholePackageChanges(t *testing.T) {
	tests := []struct {
		name   string
		report string
		want   string
	}{
		{
			name: "package added",
			report: "Compatible changes:\n" +
				"- package github.com/writtendev/writ/engine/labels: added\n",
			want: "Compatible changes:\n" +
				"- package github.com/writtendev/writ/engine/labels: added\n",
		},
		{
			name: "package removed",
			report: "Incompatible changes:\n" +
				"- package github.com/writtendev/writ/engine/labels: removed\n",
			want: "Incompatible changes:\n" +
				"- package github.com/writtendev/writ/engine/labels: removed\n",
		},
		{
			name: "engine itself removed",
			report: "Incompatible changes:\n" +
				"- package github.com/writtendev/writ/engine: removed\n",
			want: "Incompatible changes:\n" +
				"- package github.com/writtendev/writ/engine: removed\n",
		},
		{
			name: "packages outside engine are dropped",
			report: "Compatible changes:\n" +
				"- package github.com/writtendev/writ/cmd/writ: added\n" +
				"- package github.com/writtendev/writ/spec/fixtures: added\n" +
				"- package github.com/writtendev/writ/enginex: added\n",
			want: "",
		},
		{
			name: "symbols are still matched by their relative spelling",
			report: "Incompatible changes:\n" +
				"- ./engine/resolve.SHA256: value changed from 1 to 2\n" +
				"- ./cmd/writ.Run: removed\n",
			want: "Incompatible changes:\n" +
				"- ./engine/resolve.SHA256: value changed from 1 to 2\n",
		},
		{
			// A symbol elsewhere in the module whose *type* lives under
			// engine is not an engine API change: apidiff spells the type
			// with its full import path, which does not contain "./engine".
			name: "a non-engine symbol referring to an engine type is dropped",
			report: "Compatible changes:\n" +
				"- ./spec/fixtures.Trust: added, of type " +
				"github.com/writtendev/writ/internal/codec/sshsig.TrustStore\n",
			want: "",
		},
		{
			// apidiff formats interface implementation changes using the
			// module-relative path for the target interface. A non-engine
			// subject implementing or no longer implementing an engine
			// interface must not be classified as an engine change.
			name: "non-engine subject implementing or breaking an engine interface is dropped",
			report: "Incompatible changes:\n" +
				"- ./spec/fixtures.X: no longer implements ./engine/codec.Signer\n" +
				"Compatible changes:\n" +
				"- ./spec/fixtures.X: implements ./engine/codec.Signer\n",
			want: "",
		},
		{
			name: "engine subject no longer implementing an interface is kept",
			report: "Incompatible changes:\n" +
				"- ./engine/codec.MySigner: no longer implements io.Closer\n",
			want: "Incompatible changes:\n" +
				"- ./engine/codec.MySigner: no longer implements io.Closer\n",
		},
		{
			name:   "nothing to report",
			report: "",
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := filter(t, tt.report); got != tt.want {
				t.Errorf("filtered report\n--- got ---\n%s\n--- want ---\n%s", got, tt.want)
			}
		})
	}
}

// apidiff walks a Go map, so its line order varies between runs on the same
// input. The report is read in a pull request; it has to be the same text
// every time, with the breaking half first.
func TestFilterSortsEachSection(t *testing.T) {
	report := "Compatible changes:\n" +
		"- package github.com/writtendev/writ/engine/labels: added\n" +
		"- ./engine.NewIssue.Labels: added\n" +
		"- ./engine/codec.Zip: added\n" +
		"Incompatible changes:\n" +
		"- ./engine/resolve.SHA256: value changed from 1 to 2\n" +
		"- ./engine.NewIssue: old is comparable, new is not\n"

	want := "Incompatible changes:\n" +
		"- ./engine.NewIssue: old is comparable, new is not\n" +
		"- ./engine/resolve.SHA256: value changed from 1 to 2\n" +
		"Compatible changes:\n" +
		"- ./engine.NewIssue.Labels: added\n" +
		"- ./engine/codec.Zip: added\n" +
		"- package github.com/writtendev/writ/engine/labels: added\n"

	if got := filter(t, report); got != want {
		t.Errorf("filtered report\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
