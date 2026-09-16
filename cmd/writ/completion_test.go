package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestCompletion_Bash(t *testing.T) {
	var buf bytes.Buffer
	emitBashCompletion(&buf)
	script := buf.String()

	if script == "" {
		t.Fatal("emitBashCompletion produced empty output")
	}

	if !strings.Contains(script, "_writ()") || !strings.Contains(script, "complete -F _writ writ") {
		t.Errorf("bash completion missing entry points")
	}

	// Verify all subcommands mentioned
	expectedWords := []string{
		"init", "object", "schema", "sync", "completion", "help",
		"create", "apply", "show", "list", "plan",
	}
	for _, word := range expectedWords {
		if !strings.Contains(script, word) {
			t.Errorf("bash completion missing expected word %q", word)
		}
	}

	// Verify double-dash enum flag support for -sort
	if !strings.Contains(script, "-sort|--sort") {
		t.Errorf("bash completion missing double-dash flag matching for -sort")
	}

	if _, err := exec.LookPath("bash"); err == nil {
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("bash syntax check failed: %v\nOutput: %s", err, string(out))
		}
	}
}

func TestCompletion_Zsh(t *testing.T) {
	var buf bytes.Buffer
	emitZshCompletion(&buf)
	script := buf.String()

	if script == "" {
		t.Fatal("emitZshCompletion produced empty output")
	}

	if !strings.HasPrefix(script, "#compdef writ") {
		t.Errorf("zsh completion missing #compdef header")
	}

	expectedWords := []string{
		"init", "object", "schema", "sync", "completion", "help",
		"create", "apply", "show", "list", "plan",
	}
	for _, word := range expectedWords {
		if !strings.Contains(script, word) {
			t.Errorf("zsh completion missing expected word %q", word)
		}
	}

	if !strings.Contains(script, "case $line[1] in") {
		t.Errorf("zsh completion for help must inspect line[1]")
	}

	if _, err := exec.LookPath("zsh"); err == nil {
		cmd := exec.Command("zsh", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("zsh syntax check failed: %v\nOutput: %s", err, string(out))
		}
	}
}

func TestCompletion_Fish(t *testing.T) {
	var buf bytes.Buffer
	emitFishCompletion(&buf)
	script := buf.String()

	if script == "" {
		t.Fatal("emitFishCompletion produced empty output")
	}

	if !strings.Contains(script, "complete -c writ") {
		t.Errorf("fish completion missing complete -c writ")
	}

	expectedWords := []string{
		"init", "object", "schema", "sync", "completion", "help",
		"create", "apply", "show", "list", "plan",
	}
	for _, word := range expectedWords {
		if !strings.Contains(script, word) {
			t.Errorf("fish completion missing expected word %q", word)
		}
	}

	// Verify Round 2 Finding 1 & 2: __fish_writ_needs_subcommand and __fish_writ_args exist
	if !strings.Contains(script, "__fish_writ_needs_subcommand object") || !strings.Contains(script, "__fish_writ_needs_subcommand schema") || !strings.Contains(script, "__fish_writ_args") {
		t.Errorf("fish completion missing __fish_writ_needs_subcommand or __fish_writ_args")
	}

	// Verify Round 3 Finding 1: newline separation in __fish_writ_args
	if !strings.Contains(script, "for arg in $args\n        echo $arg\n    end") {
		t.Errorf("fish completion missing per-line echo in __fish_writ_args")
	}

	// Verify Finding 5: single char options use -s
	if strings.Contains(script, "-l C ") {
		t.Errorf("fish completion uses -l for single-letter options (-C)")
	}

	if _, err := exec.LookPath("fish"); err == nil {
		cmd := exec.Command("fish", "--no-execute")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("fish syntax check failed: %v\nOutput: %s", err, string(out))
		}
	}
}

func TestCompletion_CLI(t *testing.T) {
	t.Run("missing_shell", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"completion"}, &stdout, &stderr)
		if code != 2 {
			t.Errorf("completion no args code = %d, want 2", code)
		}
		if !strings.Contains(stderr.String(), "shell required") {
			t.Errorf("stderr missing 'shell required': %s", stderr.String())
		}
	})

	t.Run("help_flag", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"completion", "-h"}, &stdout, &stderr)
		if code != 0 {
			t.Errorf("completion -h code = %d, want 0; stderr: %s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "Usage: writ completion") {
			t.Errorf("stdout missing usage: %s", stdout.String())
		}
	})

	t.Run("unsupported_shell", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), []string{"completion", "powershell"}, &stdout, &stderr)
		if code != 2 {
			t.Errorf("completion unsupported shell code = %d, want 2", code)
		}
		if !strings.Contains(stderr.String(), "unsupported shell") {
			t.Errorf("stderr missing 'unsupported shell': %s", stderr.String())
		}
	})

	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run("valid_"+shell, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(context.Background(), []string{"completion", shell}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("completion %s failed with %d: %s", shell, code, stderr.String())
			}
			if stdout.Len() == 0 {
				t.Errorf("completion %s stdout is empty", shell)
			}
		})
	}
}

func TestCompletion_BashDoesNotExpandTypeNames(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found in PATH")
	}

	dir := t.TempDir()

	// Names outside the object_type grammar (spec/op-envelope.md,
	// engine/dag/refs.go objectTypeRegexp) that a hostile schema could
	// still get into `writ schema show`'s output. Bash inserts a
	// completed candidate into the command line unquoted, so any of
	// these landing in COMPREPLY would run on Enter; they must never
	// reach it.
	hostile := []string{
		"acme.$(touch " + dir + "/marker1)",
		"acme.`touch " + dir + "/marker2`",
		"acme.${IFS}x;touch " + dir + "/marker3",
		"acme.*",
	}
	// Grammar-legal names that must still be offered.
	benign := []string{
		"acme.issue",
		"acme-widget",
		"bigco.gadget",
	}
	allNames := append(append([]string{}, hostile...), benign...)
	fixturePath := filepath.Join(dir, "fixture.txt")
	if err := os.WriteFile(fixturePath, []byte(strings.Join(allNames, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	stubPath := filepath.Join(dir, "writ")
	stub := "#!/bin/sh\ncat '" + fixturePath + "'\n"
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub writ: %v", err)
	}

	var buf bytes.Buffer
	emitBashCompletion(&buf)
	scriptPath := filepath.Join(dir, "completion.bash")
	if err := os.WriteFile(scriptPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write completion script: %v", err)
	}

	// grammarRE mirrors engine/dag/refs.go objectTypeRegexp. Every
	// COMPREPLY entry the helper offers must satisfy it: an independent
	// check (not the regexp the bash helper itself uses) that a
	// candidate which could inject never reaches COMPREPLY, standing in
	// for driving a real TTY to press Enter.
	grammarRE := regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}(\.[a-z][a-z0-9-]{0,63})?$`)

	driver := `
set -u
source "` + scriptPath + `"

complete_for() {
    local words=("$@")
    local n=${#words[@]}
    COMP_WORDS=("${words[@]}")
    COMP_CWORD=$((n - 1))
    COMPREPLY=()
    _writ
    printf '%s\n' "${COMPREPLY[@]}"
}

echo '--list--'
complete_for writ object list 'acme.'
echo '--create--'
complete_for writ object create 'acme.'
echo '--show--'
complete_for writ schema show 'acme.'
echo '--empty--'
complete_for writ object list ''
echo '--prefix--'
complete_for writ object list 'acme.i'
`

	cmd := exec.Command("bash", "--norc", "--noprofile", "-c", driver)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("driver script failed: %v\noutput:\n%s", err, out)
	}
	output := string(out)

	for _, marker := range []string{"marker1", "marker2", "marker3"} {
		if _, statErr := os.Stat(filepath.Join(dir, marker)); statErr == nil {
			t.Errorf("marker file %q was created: completion executed an injected command\noutput:\n%s", marker, output)
		}
	}

	for _, name := range hostile {
		if strings.Contains(output, name) {
			t.Errorf("output contains hostile candidate %q: it must be dropped, not offered\noutput:\n%s", name, output)
		}
	}

	for _, name := range benign {
		if !strings.Contains(output, name) {
			t.Errorf("output missing benign candidate %q\noutput:\n%s", name, output)
		}
	}

	// acme.* must not have glob-expanded against the temp directory's own files.
	if strings.Contains(output, "completion.bash") || strings.Contains(output, "fixture.txt") {
		t.Errorf("acme.* appears to have glob-expanded against directory contents\noutput:\n%s", output)
	}

	// Every offered candidate, in every section, must satisfy the
	// object_type grammar. This is the cheap Enter-time check: it
	// would catch a hostile name that slipped past the helper's own
	// filter without needing to actually drive a bash TTY to press
	// Enter.
	for _, line := range strings.Split(output, "\n") {
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		if !grammarRE.MatchString(line) {
			t.Errorf("COMPREPLY entry %q does not satisfy the object_type grammar\noutput:\n%s", line, output)
		}
	}

	prefixSection := output[strings.Index(output, "--prefix--"):]
	if !strings.Contains(prefixSection, "acme.issue") {
		t.Errorf("prefix match on %q did not return acme.issue\noutput:\n%s", "acme.i", output)
	}
	if strings.Contains(prefixSection, "acme.*") || strings.Contains(prefixSection, "marker") {
		t.Errorf("prefix match on %q returned unexpected candidates\noutput:\n%s", "acme.i", output)
	}
}

// TestCompletion_BashLoadsInPosixMode is the Round 3 finding 1 guard:
// the "< <(...)" process substitution the helper used to read
// `writ schema show`'s output is only legal in POSIX mode from bash
// 5.1 on. Before that, POSIXLY_CORRECT=1 or `set -o posix` makes
// sourcing the generated script a syntax error, so `_writ` is never
// defined and writ loses bash completion entirely — a different,
// non-injection failure than the one the other tests in this file
// guard against, so it needs its own case. It drives the actual
// helper under both ways bash enters POSIX mode and checks that the
// script still loads (`_writ` gets registered), a benign candidate is
// still offered, and a hostile one is still dropped.
func TestCompletion_BashLoadsInPosixMode(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found in PATH")
	}

	dir := t.TempDir()

	names := []string{"acme.issue", "acme.$(touch " + dir + "/marker)"}
	fixturePath := filepath.Join(dir, "fixture.txt")
	if err := os.WriteFile(fixturePath, []byte(strings.Join(names, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	stubPath := filepath.Join(dir, "writ")
	stub := "#!/bin/sh\ncat '" + fixturePath + "'\n"
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub writ: %v", err)
	}

	var buf bytes.Buffer
	emitBashCompletion(&buf)
	scriptPath := filepath.Join(dir, "completion.bash")
	if err := os.WriteFile(scriptPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write completion script: %v", err)
	}

	driver := `
source "` + scriptPath + `"
complete -p writ
COMP_WORDS=(writ object list "acme.")
COMP_CWORD=3
COMPREPLY=()
_writ
printf '%s\n' "${COMPREPLY[@]}"
`

	cases := []struct {
		name string
		args []string
		env  []string
	}{
		{name: "posix_flag", args: []string{"--posix", "--norc", "--noprofile", "-c", driver}},
		{name: "posixly_correct_env", args: []string{"--norc", "--noprofile", "-c", driver}, env: []string{"POSIXLY_CORRECT=1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", tc.args...)
			cmd.Env = append(append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH")), tc.env...)
			out, err := cmd.CombinedOutput()
			output := string(out)
			if err != nil {
				t.Fatalf("driver script failed: %v\noutput:\n%s", err, output)
			}

			if !strings.Contains(output, "complete -F _writ writ") {
				t.Errorf("_writ was not registered under POSIX mode; sourcing the script failed\noutput:\n%s", output)
			}
			if !strings.Contains(output, "acme.issue") {
				t.Errorf("benign candidate missing under POSIX mode\noutput:\n%s", output)
			}
			if strings.Contains(output, "$(touch") || strings.Contains(output, "marker)") {
				t.Errorf("hostile candidate leaked through under POSIX mode\noutput:\n%s", output)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "marker")); statErr == nil {
				t.Errorf("marker file was created: completion executed an injected command under POSIX mode")
			}
		})
	}
}

// bashTypeRegexRangePattern matches a hyphen joining two alphanumerics
// inside what would be a POSIX bracket expression, e.g. "a-z" or
// "0-9" — the form whose collation regcomp expands according to the
// active LC_COLLATE/LC_ALL instead of literal ASCII (see
// TestCompletion_BashDoesNotExpandTypeNamesUnderBrokenLocale). Go's
// regexp package (RE2) is not subject to that: this check runs
// locale-independently on every platform, including CI's
// ubuntu-latest, which doesn't ship the kk_KZ.PT154 locale the
// behavioural test above needs and skips without.
var bashTypeRegexRangePattern = regexp.MustCompile(`[0-9A-Za-z]-[0-9A-Za-z]`)

// extractBashTypeRe pulls the `type_re='...'` value out of the
// generated bash completion script.
func extractBashTypeRe(script string) (string, bool) {
	const marker = "local type_re='"
	i := strings.Index(script, marker)
	if i < 0 {
		return "", false
	}
	rest := script[i+len(marker):]
	j := strings.Index(rest, "'")
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// TestCompletion_BashTypeRegexHasNoLocaleDependentRanges is the Round
// 3 finding 2 guard: it fails a revert of the round-2 fix back to
// [a-z]/[a-z0-9-] even on ubuntu-latest, where
// TestCompletion_BashDoesNotExpandTypeNamesUnderBrokenLocale skips for
// lack of the kk_KZ.PT154 locale and TestCompletion_BashDoesNotExpandTypeNames
// passes because it only runs in the C locale.
func TestCompletion_BashTypeRegexHasNoLocaleDependentRanges(t *testing.T) {
	var buf bytes.Buffer
	emitBashCompletion(&buf)
	script := buf.String()

	typeRe, ok := extractBashTypeRe(script)
	if !ok {
		t.Fatal("could not find type_re in generated bash completion script")
	}

	if bashTypeRegexRangePattern.MatchString(typeRe) {
		t.Errorf("type_re contains a locale-dependent bracket range: %q", typeRe)
	}

	// Prove the check has teeth: it must flag the pre-round-2 ranged
	// form the finding was filed against, or a silent revert would
	// pass this test the same way it would pass the C-locale test.
	reverted := `^[a-z][a-z0-9-]{0,63}(\.[a-z][a-z0-9-]{0,63})?$`
	if !bashTypeRegexRangePattern.MatchString(reverted) {
		t.Errorf("range-detection pattern did not flag the pre-round-2 ranged form %q", reverted)
	}
}

// TestCompletion_BashDoesNotExpandTypeNamesUnderBrokenLocale is the
// locale-dependent half of the finding above. [a-z] and [a-z0-9-] are
// POSIX bracket *ranges*, and range matching collates according to
// LC_COLLATE/LC_ALL. Under many single-byte locales — kk_KZ.PT154 is
// one — "[a-z]" collates across nearly all printable ASCII, including
// shell metacharacters ($ ( ) ; ` | & * ~), space, and TAB, so a
// candidate that the C-locale-tested grammar regexp would reject
// instead passes the bash helper's filter, gets offered, and runs on
// Enter. This drives the actual completion helper (not a copy of its
// regexp) under such a locale and checks the same two things the
// locale-independent test above checks: no injected command ran, and
// no hostile candidate was offered.
func TestCompletion_BashDoesNotExpandTypeNamesUnderBrokenLocale(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found in PATH")
	}

	const brokenLocale = "kk_KZ.PT154"
	if out, err := exec.Command("locale", "-a").CombinedOutput(); err != nil || !strings.Contains(string(out), brokenLocale) {
		t.Skipf("locale %s not installed (locale -a doesn't list it); skipping the broken-range subcase", brokenLocale)
	}

	dir := t.TempDir()

	// Each of these is outside the object_type grammar but, per the
	// probe above, is accepted by [a-z]/[a-z0-9-] under LC_ALL=kk_KZ.PT154
	// specifically because that locale's collation folds the ranges
	// open: two command-substitution forms and a space (word-splits into
	// a second, executable word). Touch targets are short relative
	// names, not t.TempDir()'s (often 60+ char) absolute path: the
	// grammar's own {0,63} segment-length cap would otherwise reject a
	// long candidate anyway, masking the character-class bug this test
	// exists to catch. cmd.Dir below puts bash's cwd at dir so a
	// hypothetical execution still lands the marker there.
	hostile := []string{
		"acme.zz$(touch m1)",
		"acme.zz`touch m2`",
		"acme.zz touchm3",
	}
	benign := []string{"acme.issue"}
	allNames := append(append([]string{}, hostile...), benign...)
	fixturePath := filepath.Join(dir, "fixture.txt")
	if err := os.WriteFile(fixturePath, []byte(strings.Join(allNames, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	stubPath := filepath.Join(dir, "writ")
	stub := "#!/bin/sh\ncat '" + fixturePath + "'\n"
	if err := os.WriteFile(stubPath, []byte(stub), 0o755); err != nil {
		t.Fatalf("write stub writ: %v", err)
	}

	var buf bytes.Buffer
	emitBashCompletion(&buf)
	scriptPath := filepath.Join(dir, "completion.bash")
	if err := os.WriteFile(scriptPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write completion script: %v", err)
	}

	driver := `
set -u
source "` + scriptPath + `"

complete_for() {
    local words=("$@")
    local n=${#words[@]}
    COMP_WORDS=("${words[@]}")
    COMP_CWORD=$((n - 1))
    COMPREPLY=()
    _writ
    printf '%s\n' "${COMPREPLY[@]}"
}

complete_for writ object list 'acme.'
`

	cmd := exec.Command("bash", "--norc", "--noprofile", "-c", driver)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "LC_ALL="+brokenLocale)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("driver script failed under LC_ALL=%s: %v\noutput:\n%s", brokenLocale, err, out)
	}
	output := string(out)

	for _, marker := range []string{"m1", "m2", "m3"} {
		if _, statErr := os.Stat(filepath.Join(dir, marker)); statErr == nil {
			t.Errorf("marker file %q was created under LC_ALL=%s: completion executed an injected command\noutput:\n%s", marker, brokenLocale, output)
		}
	}

	for _, name := range hostile {
		if strings.Contains(output, name) {
			t.Errorf("output contains hostile candidate %q under LC_ALL=%s: it must be dropped, not offered\noutput:\n%s", name, brokenLocale, output)
		}
	}

	if !strings.Contains(output, "acme.issue") {
		t.Errorf("output missing benign candidate %q under LC_ALL=%s\noutput:\n%s", "acme.issue", brokenLocale, output)
	}
}
