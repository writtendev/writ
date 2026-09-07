package main

import (
	"fmt"
	"io"
	"strings"
)

func runCompletion(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "writ completion: shell required (bash, zsh, fish)")
		renderUsage(stderr, []string{"completion"}, completionCmd)
		return 2
	}

	switch args[0] {
	case "-h", "-help", "--help":
		renderUsage(stdout, []string{"completion"}, completionCmd)
		return 0
	case "bash":
		emitBashCompletion(stdout)
		return 0
	case "zsh":
		emitZshCompletion(stdout)
		return 0
	case "fish":
		emitFishCompletion(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "writ completion: unsupported shell %q (supported: bash, zsh, fish)\n", args[0])
		return 2
	}
}

func escapeFishDesc(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return s
}

func flagEnumChoices(flagName string, cmdPath []string) string {
	switch flagName {
	case "sort":
		return "created_at_asc created_at_desc updated_at_asc updated_at_desc"
	default:
		return ""
	}
}

func emitBashCompletion(w io.Writer) {
	fmt.Fprintln(w, `# bash completion for writ                          -*- shell-script -*-

_writ() {
    local cur prev words cword
    _init_completion -n : 2>/dev/null || {
        cur="${COMP_WORDS[COMP_CWORD]}"
        prev="${COMP_WORDS[COMP_CWORD-1]}"
        words=("${COMP_WORDS[@]}")
        cword=$COMP_CWORD
    }

    local cmd=""
    local subcmd=""
    local cmd_idx=0
    local subcmd_idx=0
    local i=1

    while [ $i -lt $cword ]; do
        local word="${words[i]}"
        case "$word" in
            -C)
                i=$((i + 2))
                continue
                ;;
            -*)
                i=$((i + 1))
                continue
                ;;
            *)
                if [ -z "$cmd" ]; then
                    cmd="$word"
                    cmd_idx=$i
                elif [ -z "$subcmd" ]; then
                    subcmd="$word"
                    subcmd_idx=$i
                fi
                i=$((i + 1))
                ;;
        esac
    done

    # Top-level completion
    if [ -z "$cmd" ]; then
        if [[ "$cur" == -* ]]; then
            COMPREPLY=($(compgen -W "-C -h -help --help" -- "$cur"))
            return 0
        fi
        COMPREPLY=($(compgen -W "init object schema sync version completion help" -- "$cur"))
        return 0
    fi

    # Flag value completion based on prev
    case "$prev" in
        -C)
            COMPREPLY=($(compgen -d -- "$cur"))
            return 0
            ;;
    esac

    # Command-specific completion
    case "$cmd" in
        init)
            if [[ "$cur" == -* ]]; then
                COMPREPLY=($(compgen -W "-C -h -help --help" -- "$cur"))
                return 0
            fi
            ;;
        sync)
            if [[ "$cur" == -* ]]; then
                COMPREPLY=($(compgen -W "-C -status --status -json --json -h -help --help" -- "$cur"))
                return 0
            fi
            ;;
        completion)
            if [ -z "$subcmd" ]; then
                COMPREPLY=($(compgen -W "bash zsh fish" -- "$cur"))
                return 0
            fi
            ;;
        help)
            if [ -z "$subcmd" ]; then
                COMPREPLY=($(compgen -W "init object schema sync version completion help" -- "$cur"))
                return 0
            fi
            case "$subcmd" in
                object)
                    COMPREPLY=($(compgen -W "create apply show list" -- "$cur"))
                    return 0
                    ;;
                schema)
                    COMPREPLY=($(compgen -W "plan apply show" -- "$cur"))
                    return 0
                    ;;
            esac
            ;;
        object)
            if [ -z "$subcmd" ]; then
                if [[ "$cur" == -* ]]; then
                    COMPREPLY=($(compgen -W "-C -h -help --help" -- "$cur"))
                    return 0
                fi
                COMPREPLY=($(compgen -W "create apply show list" -- "$cur"))
                return 0
            fi
            # Count positional arguments given after the subcommand, so
            # <type> (create, list) can be completed from the installed
            # vocabulary without stepping on flag values.
            local obj_pos=0
            local j=$((subcmd_idx + 1))
            while [ $j -lt $cword ]; do
                case "${words[j]}" in
                    -C|-field|--field|-op-version|--op-version|-author|--author|-text|--text|-limit|--limit|-offset|--offset|-sort|--sort)
                        j=$((j + 2))
                        ;;
                    -*) j=$((j + 1)) ;;
                    *) obj_pos=$((obj_pos + 1)); j=$((j + 1)) ;;
                esac
            done
            case "$subcmd" in
                create)
                    if [ $obj_pos -eq 0 ] && [[ "$cur" != -* ]]; then
                        COMPREPLY=($(compgen -W "$(writ schema show 2>/dev/null)" -- "$cur"))
                        return 0
                    fi
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -field -op-version -json --json -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                apply)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -field -op-version -json --json -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                show)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -json --json -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                list)
                    case "$prev" in
                        -sort|--sort)
                            COMPREPLY=($(compgen -W "created_at_asc created_at_desc updated_at_asc updated_at_desc" -- "$cur"))
                            return 0
                            ;;
                    esac
                    if [ $obj_pos -eq 0 ] && [[ "$cur" != -* ]]; then
                        COMPREPLY=($(compgen -W "$(writ schema show 2>/dev/null)" -- "$cur"))
                        return 0
                    fi
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -author -text -include-deleted -limit -offset -sort -json --json -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
            esac
            ;;
        schema)
            if [ -z "$subcmd" ]; then
                if [[ "$cur" == -* ]]; then
                    COMPREPLY=($(compgen -W "-C -h -help --help" -- "$cur"))
                    return 0
                fi
                COMPREPLY=($(compgen -W "plan apply show" -- "$cur"))
                return 0
            fi
            case "$subcmd" in
                plan|apply)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -json --json -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                show)
                    if [[ "$cur" != -* ]]; then
                        COMPREPLY=($(compgen -W "$(writ schema show 2>/dev/null)" -- "$cur"))
                        return 0
                    fi
                    COMPREPLY=($(compgen -W "-C -json --json -h -help --help" -- "$cur"))
                    return 0
                    ;;
            esac
            ;;
    esac
}

complete -F _writ writ`)
}

func emitZshCompletion(w io.Writer) {
	fmt.Fprintln(w, `#compdef writ

_writ() {
    local -a commands
    local curcontext="$curcontext" state line
    typeset -A opt_args

    _arguments -C \
        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
        '(-h -help --help)'{-h,-help,--help}'[Show help information]' \
        '1: :->command' \
        '*:: :->args'

    case $state in
        command)
            commands=(
                'init:Initialize writ configuration'
                'object:Generic create, apply, show, and list over any schema-declared object type'
                'schema:Plan, apply, and show the schema-declared vocabulary'
                'sync:Synchronize collaborative SDLC operations'
                'version:Print the writ version'
                'completion:Generate shell completion scripts'
                'help:Show help for writ commands'
            )
            _describe -t commands 'writ command' commands
            ;;
        args)
            case $line[1] in
                init)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '*:remote:_git_remotes'
                    ;;
                sync)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '--status[Report unpushed ops count without network transport]' \
                        '-status[Report unpushed ops count without network transport]' \
                        '--json[Output result as JSON]' \
                        '-json[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '*:remote:_git_remotes'
                    ;;
                completion)
                    _arguments -s -S \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:shell:(bash zsh fish)'
                    ;;
                help)
                    _arguments -s -S \
                        '1:command:(init object schema sync version completion help)' \
                        '2:subcommand:->help_subcommand'
                    case $line[1] in
                        object) _values 'object subcommand' create apply show list ;;
                        schema) _values 'schema subcommand' plan apply show ;;
                    esac
                    ;;
                object)
                    _writ_object
                    ;;
                schema)
                    _writ_schema
                    ;;
            esac
            ;;
    esac
}

_writ_schema_types() {
    local -a types
    types=(${(f)"$(writ schema show 2>/dev/null)"})
    _describe 'type' types
}

_writ_object() {
    local curcontext="$curcontext" state line
    typeset -A opt_args

    _arguments -C \
        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
        '1: :->subcommand' \
        '*:: :->args'

    case $state in
        subcommand)
            local -a subcommands
            subcommands=(
                'create:Create a new object of a schema-declared type'
                'apply:Apply a further op to an existing object'
                "show:Show an object's folded state"
                'list:List objects across or within a type'
            )
            _describe -t subcommands 'object subcommand' subcommands
            ;;
        args)
            case $line[1] in
                create)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '*-field[Field k=v to set on the creating op]:field:' \
                        '-op-version[Explicit op version]:version:' \
                        '(--json -json)'{--json,-json}'[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:type:_writ_schema_types' \
                        '2:op-type:'
                    ;;
                apply)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '*-field[Field k=v to set on the op]:field:' \
                        '-op-version[Explicit op version]:version:' \
                        '(--json -json)'{--json,-json}'[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:object ID:' \
                        '2:op-type:'
                    ;;
                show)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '(--json -json)'{--json,-json}'[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:object ID:'
                    ;;
                list)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '*-author[Filter by author]:author:' \
                        '-text[Filter by text query]:text:' \
                        '-include-deleted[Include deleted objects]' \
                        '-limit[Maximum objects to return]:limit:' \
                        '-offset[Skip the first N matching objects]:offset:' \
                        '-sort[Sort order]:sort:(created_at_asc created_at_desc updated_at_asc updated_at_desc)' \
                        '(--json -json)'{--json,-json}'[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:type:_writ_schema_types'
                    ;;
            esac
            ;;
    esac
}

_writ_schema() {
    local curcontext="$curcontext" state line
    typeset -A opt_args

    _arguments -C \
        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
        '1: :->subcommand' \
        '*:: :->args'

    case $state in
        subcommand)
            local -a subcommands
            subcommands=(
                'plan:Show the ops writ.schema would append'
                'apply:Sign and append the ops writ.schema declares'
                'show:Show the vocabulary actually installed and folding now'
            )
            _describe -t subcommands 'schema subcommand' subcommands
            ;;
        args)
            case $line[1] in
                plan|apply)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '(--json -json)'{--json,-json}'[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]'
                    ;;
                show)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '(--json -json)'{--json,-json}'[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:type:_writ_schema_types'
                    ;;
            esac
            ;;
    esac
}

_writ "$@"`)
}

func emitFishCompletion(w io.Writer) {
	fmt.Fprintln(w, `# fish completion for writ

function __fish_writ_args
    set -l raw (commandline -opc)
    set -l args
    set -l skip 0
    for arg in $raw
        if test $skip -gt 0
            set skip (math $skip - 1)
            continue
        end
        if test "$arg" = "-C"
            set skip 1
            continue
        end
        set -a args "$arg"
    end
    for arg in $args
        echo $arg
    end
end

function __fish_writ_needs_command
    set -l cmd (__fish_writ_args)
    if test (count $cmd) -eq 1
        return 0
    end
    return 1
end

function __fish_writ_needs_subcommand
    set -l cmd (__fish_writ_args)
    set -l n (count $argv)
    if test (count $cmd) -ne (math $n + 1)
        return 1
    end
    for i in (seq 1 $n)
        set -l pos (math $i + 1)
        if test "$cmd[$pos]" != "$argv[$i]"
            return 1
        end
    end
    return 0
end

function __fish_writ_using_command
    set -l cmd (__fish_writ_args)
    set -l n (count $argv)
    if test (count $cmd) -le $n
        return 1
    end
    for i in (seq 1 $n)
        set -l pos (math $i + 1)
        if test "$cmd[$pos]" != "$argv[$i]"
            return 1
        end
    end
    return 0
end

# Disable file completions by default
complete -c writ -f

# Global options
complete -c writ -s C -d 'Run as if writ was started in <dir>' -r -a '(__fish_complete_directories)'
complete -c writ -l help -s h -d 'Show help information'

# Top-level commands`)

	for _, sub := range rootCommand.Subs {
		fmt.Fprintf(w, "complete -c writ -n '__fish_writ_needs_command' -f -a '%s' -d '%s'\n",
			sub.Name, escapeFishDesc(sub.Short))
	}

	fmt.Fprintln(w, `
# Subcommands for object`)
	for _, sub := range objectCmd.Subs {
		fmt.Fprintf(w, "complete -c writ -n '__fish_writ_needs_subcommand object' -f -a '%s' -d '%s'\n",
			sub.Name, escapeFishDesc(sub.Short))
	}

	fmt.Fprintln(w, `
# Subcommands for schema`)
	for _, sub := range schemaCmd.Subs {
		fmt.Fprintf(w, "complete -c writ -n '__fish_writ_needs_subcommand schema' -f -a '%s' -d '%s'\n",
			sub.Name, escapeFishDesc(sub.Short))
	}

	fmt.Fprintln(w, `
# Subcommands for completion
complete -c writ -n '__fish_writ_using_command completion' -f -a 'bash zsh fish'

# Subcommands for help
complete -c writ -n '__fish_writ_needs_subcommand help' -f -a 'init object schema sync version completion help'
complete -c writ -n '__fish_writ_needs_subcommand help object' -f -a 'create apply show list'
complete -c writ -n '__fish_writ_needs_subcommand help schema' -f -a 'plan apply show'

# <type> completion for object create/list and schema show, from the vocabulary the installed schema declares.
complete -c writ -n '__fish_writ_using_command object create' -f -a '(writ schema show 2>/dev/null)'
complete -c writ -n '__fish_writ_using_command object list' -f -a '(writ schema show 2>/dev/null)'
complete -c writ -n '__fish_writ_using_command schema show' -f -a '(writ schema show 2>/dev/null)'

# Flags for commands`)

	var walkFlags func(path []string, cmd *command)
	walkFlags = func(path []string, cmd *command) {
		if len(cmd.Subs) > 0 {
			for _, sub := range cmd.Subs {
				walkFlags(append(path, sub.Name), sub)
			}
			return
		}

		cond := fmt.Sprintf("__fish_writ_using_command %s", strings.Join(path, " "))
		for _, f := range commandFlags(path, cmd) {
			optFlag := "-l " + f.Name
			if len(f.Name) == 1 {
				optFlag = "-s " + f.Name
			}
			desc := escapeFishDesc(f.Usage)
			enumChoices := strings.Join(f.Values, " ")
			if enumChoices == "" {
				enumChoices = flagEnumChoices(f.Name, path)
			}

			if enumChoices != "" {
				fmt.Fprintf(w, "complete -c writ -n '%s' %s -d '%s' -r -f -a '%s'\n",
					cond, optFlag, desc, enumChoices)
			} else if f.Name == "C" {
				fmt.Fprintf(w, "complete -c writ -n '%s' %s -d '%s' -r -a '(__fish_complete_directories)'\n",
					cond, optFlag, desc)
			} else if f.Name == "json" || f.Name == "status" && cmd.Name == "sync" {
				fmt.Fprintf(w, "complete -c writ -n '%s' %s -d '%s'\n",
					cond, optFlag, desc)
			} else {
				fmt.Fprintf(w, "complete -c writ -n '%s' %s -d '%s' -r\n",
					cond, optFlag, desc)
			}
		}
	}

	for _, sub := range rootCommand.Subs {
		walkFlags([]string{sub.Name}, sub)
	}
}
