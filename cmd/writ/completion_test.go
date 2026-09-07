package main

import (
	"bytes"
	"context"
	"os/exec"
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
