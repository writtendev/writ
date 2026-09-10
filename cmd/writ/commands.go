package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

type flagSpec struct {
	Name       string // "title", "status"
	Arg        string // "<t>", "" for bool flags
	Usage      string
	Values     []string // closed enum, for completion: approve|request-changes|none
	Repeatable bool
}

type command struct {
	Name, Short string
	UsageLine   string // first line, verbatim
	Long        string
	Examples    []string // at least one per verb (DoD)
	ExitCodes   []string // only sync populates this today
	Flags       []flagSpec
	Subs        []*command
}

var rootCommand = &command{
	Name:      "writ",
	Short:     "Collaborative SDLC layer in git",
	UsageLine: "Usage: writ [-C <dir>] <command> [arguments]",
	Long:      "",
	Flags: []flagSpec{
		{
			Name:  "C",
			Arg:   "<dir>",
			Usage: "Run as if writ was started in <dir>",
		},
	},
	Subs: []*command{
		initCmd,
		objectCmd,
		schemaCmd,
		syncCmd,
		versionCmd,
		completionCmd,
		helpCmd,
	},
}

var initCmd = &command{
	Name:      "init",
	Short:     "Initialize writ configuration (writer ID and remote fetch refspecs)",
	UsageLine: "Usage: writ init [-C <dir>] [--namespace <name>] [remote...]",
	Long: "Initialize writ repository configuration by resolving or minting a writer ID,\n" +
		"verifying SSH signing key configuration, and adding fetch refspecs for git remotes.\n" +
		"On a work tree with no writ.schema yet, also writes a starter one: --namespace names\n" +
		"it explicitly, an interactive terminal is prompted for it, and a non-interactive run\n" +
		"with neither refuses rather than choosing one for you — the namespace becomes part of\n" +
		"every wire type the schema declares, so it is never derived from a directory name.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "namespace"},
	},
	Examples: []string{
		"writ init",
		"writ init origin",
		"writ init --namespace acme",
	},
}

var objectCmd = &command{
	Name:      "object",
	Short:     "Generic create, apply, show, and list over any schema-declared object type",
	UsageLine: "Usage: writ object [-C <dir>] <subcommand> [arguments]",
	Long: "Create, apply ops to, show, and list collaborative objects of any type the installed\n" +
		"vocabulary declares (see `writ schema show`). This is plumbing, not porcelain: writ\n" +
		"knows no per-type verbs (no `title`, no `assignee`) because it hard-codes no object\n" +
		"type at all -- only the schema in the log declares one. Prefer --json here for scripts\n" +
		"and agents; a nicer per-type CLI is a job for whatever layer owns the schema.",
	Flags: []flagSpec{
		{
			Name:  "C",
			Arg:   "<dir>",
			Usage: "Run as if writ was started in <dir>",
		},
	},
	Subs: []*command{
		objectCreateCmd,
		objectApplyCmd,
		objectShowCmd,
		objectListCmd,
	},
}

var objectCreateCmd = &command{
	Name:      "create",
	Short:     "Create a new object of a schema-declared type",
	UsageLine: "Usage: writ object create [-C <dir>] <type> <op-type> [-field <k>=<v>]... [-field-json <k>=<v>]... [-op-version <n>] [--json]",
	Long: "Append the op that starts a new object of <type>, using <op-type>'s field rules\n" +
		"from the installed vocabulary (`writ schema show <type>`) to parse each -field value.\n" +
		"-field-json sets a field from raw JSON instead, for a field -field cannot express: one\n" +
		"that is object-shaped, or that declares no value type at all (such as an\n" +
		"{object_type, object_id} record naming another object).",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "field", Repeatable: true},
		{Name: "field-json", Repeatable: true},
		{Name: "op-version"},
		{Name: "json"},
	},
	Examples: []string{
		"writ object create acme.ticket create -field title=\"Fix the thing\"",
		"writ object create acme.ticket create -field title=\"Fix the thing\" --json",
		"writ object create acme.gadget create -field-json subject='{\"object_type\":\"acme.ticket\",\"object_id\":\"<id>\"}' -field text=\"needs a second look\"",
	},
}

var objectApplyCmd = &command{
	Name:      "apply",
	Short:     "Apply a further op to an existing object",
	UsageLine: "Usage: writ object apply [-C <dir>] <object-id> <op-type> [-field <k>=<v>]... [-field-json <k>=<v>]... [-op-version <n>] [--json]",
	Long: "Append a further op against an existing object, causally following its current frontier.\n" +
		"-field-json sets a field from raw JSON instead of -field's type-directed conversion --\n" +
		"see `writ object create -h`.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "field", Repeatable: true},
		{Name: "field-json", Repeatable: true},
		{Name: "op-version"},
		{Name: "json"},
	},
	Examples: []string{
		"writ object apply 01J8ABC update -field title=\"Renamed\"",
	},
}

var objectShowCmd = &command{
	Name:      "show",
	Short:     "Show an object's folded state",
	UsageLine: "Usage: writ object show [-C <dir>] <object-id> [--json]",
	Long:      "Fold an object's state directly from the log and print it, keyed by target field.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "json"},
	},
	Examples: []string{
		"writ object show 01J8ABC",
		"writ object show 01J8ABC --json",
	},
}

var objectListCmd = &command{
	Name:      "list",
	Short:     "List objects across or within a type",
	UsageLine: "Usage: writ object list [-C <dir>] [<type>] [-author <a>]... [-text <q>] [-include-deleted] [-limit N] [-offset N] [-sort <order>] [--json]",
	Long:      "List collaborative objects, optionally filtered to one schema-declared type.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "author", Repeatable: true},
		{Name: "text"},
		{Name: "include-deleted"},
		{Name: "limit"},
		{Name: "offset"},
		{Name: "sort", Values: []string{
			"created_at_asc", "created_at_desc",
			"updated_at_asc", "updated_at_desc",
		}},
		{Name: "json"},
	},
	Examples: []string{
		"writ object list",
		"writ object list acme.ticket",
		"writ object list acme.ticket -text urgent --json",
	},
}

var schemaCmd = &command{
	Name:      "schema",
	Short:     "Plan, apply, and show the schema-declared vocabulary",
	UsageLine: "Usage: writ schema [-C <dir>] <subcommand> [arguments]",
	Long:      "Connect the writ.schema working-tree source file to the schema objects in the log (plan, apply), and report the vocabulary actually installed and folding right now (show).",
	Flags: []flagSpec{
		{
			Name:  "C",
			Arg:   "<dir>",
			Usage: "Run as if writ was started in <dir>",
		},
	},
	Subs: []*command{
		schemaPlanCmd,
		schemaApplyCmd,
		schemaShowCmd,
	},
}

var schemaPlanCmd = &command{
	Name:      "plan",
	Short:     "Show the ops writ.schema would append",
	UsageLine: "Usage: writ schema plan [-C <dir>] [--json]",
	Long:      "Parse writ.schema, fold the schema objects in the repository, and print the ops applying the file would append. Appends no ops.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "json"},
	},
	Examples: []string{
		"writ schema plan",
		"writ schema plan --json",
	},
}

var schemaApplyCmd = &command{
	Name:      "apply",
	Short:     "Sign and append the ops writ.schema declares",
	UsageLine: "Usage: writ schema apply [-C <dir>] [--json]",
	Long:      "Run the same computation as `writ schema plan`, then sign and append the resulting ops.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "json"},
	},
	Examples: []string{
		"writ schema apply",
		"writ schema apply --json",
	},
}

var schemaShowCmd = &command{
	Name:      "show",
	Short:     "Show the vocabulary actually installed and folding now",
	UsageLine: "Usage: writ schema show [-C <dir>] [<type>] [--json]",
	Long: "Report the vocabulary Store.Types resolves right now -- built-in types overlaid by\n" +
		"whatever the log declares -- which is not the same question `writ schema plan`/`apply`\n" +
		"answer (the working-tree writ.schema file's own view). With no <type>, print one bare\n" +
		"type name per line. With <type>, print that type's declared ops and fields.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "json"},
	},
	Examples: []string{
		"writ schema show",
		"writ schema show acme.ticket",
		"writ schema show acme.ticket --json",
	},
}

var syncCmd = &command{
	Name:      "sync",
	Short:     "Synchronize operations with git remotes",
	UsageLine: "Usage: writ sync [-C <dir>] [--status] [--json] [remote...]",
	Long: "Synchronize collaborative SDLC operations with one or more git remotes.\n\n" +
		"Fetch remote operations, push local operations, and refresh the local projection cache.\n" +
		"With no remote specified, defaults to 'origin' or the sole configured remote.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "status"},
		{Name: "json"},
	},
	ExitCodes: []string{
		"0  Success",
		"1  Transport or unclassified git failure",
		"2  Usage error (bad flag, no resolvable default remote)",
		"3  Unknown or unconfigured remote",
		"4  Rejected non-fast-forward update",
		"5  Not a git repository / store cannot be opened",
		"6  Authentication or credentials failure",
		"7  Network or remote unreachable",
	},
	Examples: []string{
		"writ sync",
		"writ sync origin",
		"writ sync --status",
		"writ sync --status --json",
	},
}

var versionCmd = &command{
	Name:      "version",
	Short:     "Print the writ version",
	UsageLine: "Usage: writ version",
	Long:      "Print the version of the writ binary.",
	Examples: []string{
		"writ version",
	},
}

var completionCmd = &command{
	Name:      "completion",
	Short:     "Generate shell completion scripts",
	UsageLine: "Usage: writ completion <shell>",
	Long:      "Generate shell completion scripts for bash, zsh, or fish.\n\nSupported shells: bash, zsh, fish.",
	Examples: []string{
		"writ completion bash > /etc/bash_completion.d/writ",
		`writ completion zsh > "${fpath[1]}/_writ"`,
		"writ completion fish > ~/.config/fish/completions/writ.fish",
	},
}

var helpCmd = &command{
	Name:      "help",
	Short:     "Show help for writ or a subcommand",
	UsageLine: "Usage: writ help [command...]",
	Long:      "Show help for writ or a subcommand.",
	Examples: []string{
		"writ help",
		"writ help object",
		"writ help object create",
		"writ help schema",
		"writ help schema show",
	},
}

var flagSetConstructors map[string]func() *flag.FlagSet

func init() {
	flagSetConstructors = map[string]func() *flag.FlagSet{
		"init":          func() *flag.FlagSet { fs, _ := newInitFlagSet(""); return fs },
		"schema plan":   func() *flag.FlagSet { fs, _ := newSchemaPlanFlagSet(""); return fs },
		"schema apply":  func() *flag.FlagSet { fs, _ := newSchemaApplyFlagSet(""); return fs },
		"schema show":   func() *flag.FlagSet { fs, _ := newSchemaShowFlagSet(""); return fs },
		"object create": func() *flag.FlagSet { fs, _ := newObjectCreateFlagSet(""); return fs },
		"object apply":  func() *flag.FlagSet { fs, _ := newObjectApplyFlagSet(""); return fs },
		"object show":   func() *flag.FlagSet { fs, _ := newObjectShowFlagSet(""); return fs },
		"object list":   func() *flag.FlagSet { fs, _ := newObjectListFlagSet(""); return fs },
		"sync":          func() *flag.FlagSet { fs, _ := newSyncFlagSet(""); return fs },
	}
}

func commandFlags(path []string, c *command) []flagSpec {
	cmdPath := strings.Join(path, " ")
	ctor, ok := flagSetConstructors[cmdPath]
	if !ok {
		return c.Flags
	}
	fs := ctor()
	flags := make([]flagSpec, len(c.Flags))
	for i, f := range c.Flags {
		flagObj := fs.Lookup(f.Name)
		arg, usage := "", ""
		if flagObj != nil {
			arg, usage = flag.UnquoteUsage(flagObj)
		}
		flags[i] = flagSpec{
			Name:       f.Name,
			Arg:        arg,
			Usage:      usage,
			Values:     f.Values,
			Repeatable: f.Repeatable,
		}
	}
	return flags
}

func findCommandByPath(path []string) (*command, []string, error) {
	if len(path) == 0 {
		return rootCommand, nil, nil
	}

	curr := rootCommand
	var matchedPath []string

	for i, segment := range path {
		var found *command
		for _, sub := range curr.Subs {
			if sub.Name == segment {
				found = sub
				break
			}
		}
		if found == nil {
			if i == 0 {
				return nil, nil, fmt.Errorf("unknown command %q", segment)
			}
			return nil, nil, fmt.Errorf("unknown subcommand %q for \"writ %s\"", segment, strings.Join(matchedPath, " "))
		}
		curr = found
		matchedPath = append(matchedPath, segment)
	}

	return curr, matchedPath, nil
}

func renderUsage(w io.Writer, path []string, c *command) {
	fmt.Fprintln(w, c.UsageLine)
	fmt.Fprintln(w)
	if c.Long != "" {
		fmt.Fprintln(w, c.Long)
		fmt.Fprintln(w)
	}

	if len(c.Subs) > 0 {
		sectionName := "Commands:"
		if len(path) > 0 {
			sectionName = "Subcommands:"
		}
		fmt.Fprintln(w, sectionName)
		maxLen := 0
		for _, sub := range c.Subs {
			if len(sub.Name) > maxLen {
				maxLen = len(sub.Name)
			}
		}
		for _, sub := range c.Subs {
			padding := strings.Repeat(" ", maxLen-len(sub.Name)+2)
			fmt.Fprintf(w, "  %s%s%s\n", sub.Name, padding, sub.Short)
		}
		fmt.Fprintln(w)

		if len(path) == 0 {
			fmt.Fprintln(w, "Plumbing:")
			fmt.Fprintln(w, "  Every read verb supports --json for machine-readable output.")
			fmt.Fprintln(w)
			fmt.Fprintln(w, "Run 'writ <command> -h' for more information on a command.")
		} else {
			cmdPath := strings.Join(path, " ")
			fmt.Fprintf(w, "Run 'writ %s <subcommand> -h' for more information on a subcommand.\n", cmdPath)
		}
		return
	}

	flags := commandFlags(path, c)
	if len(flags) > 0 {
		fmt.Fprintln(w, "Flags:")
		maxFlagLen := 0
		type flagFormat struct {
			display string
			usage   string
		}
		var formatted []flagFormat
		for _, f := range flags {
			disp := "-" + f.Name
			if f.Arg != "" {
				disp += " " + f.Arg
			}
			if len(disp) > maxFlagLen {
				maxFlagLen = len(disp)
			}
			formatted = append(formatted, flagFormat{display: disp, usage: f.Usage})
		}
		for _, ff := range formatted {
			padding := strings.Repeat(" ", maxFlagLen-len(ff.display)+3)
			fmt.Fprintf(w, "  %s%s%s\n", ff.display, padding, ff.usage)
		}
	}

	if len(c.ExitCodes) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "Exit codes:")
		for _, ec := range c.ExitCodes {
			fmt.Fprintf(w, "  %s\n", ec)
		}
	}
}
