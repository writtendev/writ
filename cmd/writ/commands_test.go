package main

import (
	"flag"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var synopsisFlagRe = regexp.MustCompile(`(?:^|[\s\[])--?([a-zA-Z0-9]+(?:-[a-zA-Z0-9]+)*)`)

func extractSynopsisFlags(usageLine string) []string {
	matches := synopsisFlagRe.FindAllStringSubmatch(usageLine, -1)
	seen := make(map[string]bool)
	var flags []string
	for _, m := range matches {
		if len(m) > 1 && !seen[m[1]] {
			seen[m[1]] = true
			flags = append(flags, m[1])
		}
	}
	sort.Strings(flags)
	return flags
}

func TestCommands_Drift(t *testing.T) {
	var collectLeaves func(path []string, cmd *command) []struct {
		path string
		cmd  *command
	}

	collectLeaves = func(path []string, cmd *command) []struct {
		path string
		cmd  *command
	} {
		var res []struct {
			path string
			cmd  *command
		}
		if len(cmd.Subs) > 0 {
			for _, sub := range cmd.Subs {
				res = append(res, collectLeaves(append(path, sub.Name), sub)...)
			}
		} else {
			res = append(res, struct {
				path string
				cmd  *command
			}{
				path: strings.Join(path, " "),
				cmd:  cmd,
			})
		}
		return res
	}

	leaves := collectLeaves(nil, rootCommand)

	for _, leaf := range leaves {
		cmdPath := leaf.path
		cmd := leaf.cmd

		// completion, help, and version don't have flag sets
		if cmdPath == "completion" || cmdPath == "help" || cmdPath == "version" {
			continue
		}

		ctor, ok := flagSetConstructors[cmdPath]
		if !ok {
			t.Errorf("missing FlagSet constructor for command %q", cmdPath)
			continue
		}

		fs := ctor()
		var fsFlags []string
		fs.VisitAll(func(f *flag.Flag) {
			fsFlags = append(fsFlags, f.Name)
			arg, usage := flag.UnquoteUsage(f)
			if usage == "" {
				t.Errorf("command %q flag %q has empty usage string", cmdPath, f.Name)
			}
			_ = arg
		})
		sort.Strings(fsFlags)

		var tableFlags []string
		for _, f := range cmd.Flags {
			tableFlags = append(tableFlags, f.Name)
		}
		sort.Strings(tableFlags)

		fsJoined := strings.Join(fsFlags, ",")
		tableJoined := strings.Join(tableFlags, ",")

		if fsJoined != tableJoined {
			t.Errorf("command %q flag drift: FlagSet has [%s] but table has [%s]", cmdPath, fsJoined, tableJoined)
		}

		// Gate the synopsis line: extracted flag tokens must match the command's FlagSet
		synopsisFlags := extractSynopsisFlags(cmd.UsageLine)
		synopsisJoined := strings.Join(synopsisFlags, ",")
		if fsJoined != synopsisJoined {
			t.Errorf("command %q synopsis flag drift: FlagSet has [%s] but UsageLine has [%s]", cmdPath, fsJoined, synopsisJoined)
		}
	}
}

func TestCommands_ExamplesAndMetadata(t *testing.T) {
	var walk func(path []string, cmd *command)
	walk = func(path []string, cmd *command) {
		cmdPath := strings.Join(path, " ")
		if len(cmd.Subs) > 0 {
			for _, sub := range cmd.Subs {
				walk(append(path, sub.Name), sub)
			}
		} else {
			if len(cmd.Examples) == 0 {
				t.Errorf("leaf command %q has no examples", cmdPath)
			}
			for _, ex := range cmd.Examples {
				if !strings.HasPrefix(ex, "writ ") {
					t.Errorf("command %q example %q does not start with 'writ '", cmdPath, ex)
				}
			}
			if cmd.Short == "" {
				t.Errorf("leaf command %q has empty Short description", cmdPath)
			}
			if !strings.HasPrefix(cmd.UsageLine, "Usage: writ ") {
				t.Errorf("leaf command %q UsageLine %q does not start with 'Usage: writ '", cmdPath, cmd.UsageLine)
			}
		}
	}

	for _, sub := range rootCommand.Subs {
		walk([]string{sub.Name}, sub)
	}
}

func TestExtractSynopsisFlags(t *testing.T) {
	tests := []struct {
		usageLine string
		want      []string
	}{
		{
			usageLine: "Usage: writ init [-C <dir>] [remote...]",
			want:      []string{"C"},
		},
		{
			usageLine: "Usage: writ object create [-C <dir>] <type> <op-type> [-field <k>=<v>]... [-op-version <n>] [--json]",
			want:      []string{"C", "field", "json", "op-version"},
		},
		{
			usageLine: "Usage: writ sync [-C <dir>] [--status] [--json] [remote...]",
			want:      []string{"C", "json", "status"},
		},
		{
			usageLine: "Usage: writ object apply [-C <dir>] <object-id> <op-type> [-field <k>=<v>]... [-op-version <n>] [--json]",
			want:      []string{"C", "field", "json", "op-version"},
		},
		{
			usageLine: "Usage: writ version",
			want:      nil,
		},
		{
			usageLine: "Usage: writ completion <shell>",
			want:      nil,
		},
		{
			usageLine: "Usage: writ help [<command> [<subcommand>]]",
			want:      nil,
		},
	}

	for _, tt := range tests {
		got := extractSynopsisFlags(tt.usageLine)
		gotJoined := strings.Join(got, ",")
		wantJoined := strings.Join(tt.want, ",")
		if gotJoined != wantJoined {
			t.Errorf("extractSynopsisFlags(%q) = [%s], want [%s]", tt.usageLine, gotJoined, wantJoined)
		}
	}
}
