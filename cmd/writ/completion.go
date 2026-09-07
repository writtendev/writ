package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/writtendev/writ/spec"
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
	p := strings.Join(cmdPath, " ")
	switch flagName {
	case "status":
		if p == "review list" {
			return strings.Join(spec.ReviewStatuses(), " ")
		}
		return ""
	case "verdict":
		return strings.Join(spec.ApprovalVerdicts(), " ")
	case "relation":
		return strings.Join(spec.LinkRelations(), " ")
	case "sort":
		return "created_at_asc created_at_desc updated_at_asc updated_at_desc title_asc title_desc"
	case "type":
		if p == "state create" || p == "state update" {
			return strings.Join(spec.WorkflowStateTypes(), " ")
		}
		return ""
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
        COMPREPLY=($(compgen -W "init comment issue object review schema sync version completion help" -- "$cur"))
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
        comment)
            if [ -z "$subcmd" ]; then
                if [[ "$cur" == -* ]]; then
                    COMPREPLY=($(compgen -W "-C -h -help --help" -- "$cur"))
                    return 0
                fi
                COMPREPLY=($(compgen -W "edit delete" -- "$cur"))
                return 0
            fi
            case "$subcmd" in
                edit)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -m -json --json -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                delete)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -json --json -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
            esac
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
                COMPREPLY=($(compgen -W "init comment issue object review schema sync version completion help" -- "$cur"))
                return 0
            fi
            case "$subcmd" in
                comment)
                    COMPREPLY=($(compgen -W "edit delete" -- "$cur"))
                    return 0
                    ;;
                issue)
                    COMPREPLY=($(compgen -W "create status comment assign list link label" -- "$cur"))
                    return 0
                    ;;
                object)
                    COMPREPLY=($(compgen -W "create apply show list" -- "$cur"))
                    return 0
                    ;;
                review)
                    COMPREPLY=($(compgen -W "open comment approve assign label link status list" -- "$cur"))
                    return 0
                    ;;
                schema)
                    COMPREPLY=($(compgen -W "plan apply show" -- "$cur"))
                    return 0
                    ;;
            esac
            ;;
        issue)
            if [ -z "$subcmd" ]; then
                if [[ "$cur" == -* ]]; then
                    COMPREPLY=($(compgen -W "-C -h -help --help" -- "$cur"))
                    return 0
                fi
                COMPREPLY=($(compgen -W "create status comment assign list link label" -- "$cur"))
                return 0
            fi
            case "$subcmd" in
                create)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -title -description -state -fixes -relates -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                status)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -reason -json --json -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                comment)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -m -reply-to -resolve -unresolve -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                assign)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -add -remove -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                list)
                    case "$prev" in
                        -sort|--sort)
                            COMPREPLY=($(compgen -W "created_at_asc created_at_desc updated_at_asc updated_at_desc title_asc title_desc" -- "$cur"))
                            return 0
                            ;;
                    esac
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -state -assignee -label -author -text -limit -sort -json --json -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                link)
                    case "$prev" in
                        -relation|--relation)
                            COMPREPLY=($(compgen -W "fixes relates none" -- "$cur"))
                            return 0
                            ;;
                    esac
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -target -relation -target-type -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                label)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -add -remove -json --json -h -help --help" -- "$cur"))
                        return 0
                    fi
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
        review)
            if [ -z "$subcmd" ]; then
                if [[ "$cur" == -* ]]; then
                    COMPREPLY=($(compgen -W "-C -h -help --help" -- "$cur"))
                    return 0
                fi
                COMPREPLY=($(compgen -W "open comment approve assign label link status list" -- "$cur"))
                return 0
            fi
            case "$subcmd" in
                open)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -title -description -base -head -draft -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                comment)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -m -reply-to -resolve -unresolve -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                approve)
                    case "$prev" in
                        -verdict|--verdict)
                            COMPREPLY=($(compgen -W "approve request-changes none" -- "$cur"))
                            return 0
                            ;;
                    esac
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -verdict -revision -m -subject -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                assign)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -add -remove -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                label)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -add -remove -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                link)
                    case "$prev" in
                        -relation|--relation)
                            COMPREPLY=($(compgen -W "fixes relates none" -- "$cur"))
                            return 0
                            ;;
                    esac
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -target -relation -target-type -h -help --help" -- "$cur"))
                        return 0
                    fi
                    ;;
                status)
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -reason -merge-commit -json --json -h -help --help" -- "$cur"))
                        return 0
                    fi
                    # Check positional count after subcommand
                    local pos_count=0
                    local j=$((subcmd_idx + 1))
                    while [ $j -lt $cword ]; do
                        case "${words[j]}" in
                            -C|-reason|--reason|-merge-commit|--merge-commit) j=$((j + 2)) ;;
                            -*) j=$((j + 1)) ;;
                            *) pos_count=$((pos_count + 1)); j=$((j + 1)) ;;
                        esac
                    done
                    if [ $pos_count -eq 1 ]; then
                        COMPREPLY=($(compgen -W "draft open closed merged" -- "$cur"))
                        return 0
                    fi
                    ;;
                list)
                    case "$prev" in
                        -status|--status)
                            COMPREPLY=($(compgen -W "draft open closed merged" -- "$cur"))
                            return 0
                            ;;
                        -sort|--sort)
                            COMPREPLY=($(compgen -W "created_at_asc created_at_desc updated_at_asc updated_at_desc title_asc title_desc" -- "$cur"))
                            return 0
                            ;;
                    esac
                    if [[ "$cur" == -* ]]; then
                        COMPREPLY=($(compgen -W "-C -status -assignee -label -author -text -limit -sort -json --json -h -help --help" -- "$cur"))
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
                'comment:Manage comments'
                'issue:Manage issues'
                'object:Generic create, apply, show, and list over any schema-declared object type'
                'review:Manage code reviews'
                'schema:Plan and apply the writ.schema working-tree file'
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
                comment)
                    _writ_comment
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
                        '1:command:(init comment issue object review schema sync version completion help)' \
                        '2:subcommand:->help_subcommand'
                    case $line[1] in
                        comment) _values 'comment subcommand' edit delete ;;
                        issue) _values 'issue subcommand' create status comment assign list link label ;;
                        object) _values 'object subcommand' create apply show list ;;
                        review) _values 'review subcommand' open comment approve assign label link status list ;;
                        schema) _values 'schema subcommand' plan apply show ;;
                    esac
                    ;;
                issue)
                    _writ_issue
                    ;;
                object)
                    _writ_object
                    ;;
                review)
                    _writ_review
                    ;;
                schema)
                    _writ_schema
                    ;;
            esac
            ;;
    esac
}

_writ_comment() {
    local curcontext="$curcontext" state line
    typeset -A opt_args

    _arguments -C \
        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
        '(-h -help --help)'{-h,-help,--help}'[Show help information]' \
        '1: :->subcommand' \
        '*:: :->args'

    case $state in
        subcommand)
            local -a subcommands
            subcommands=(
                'edit:Edit an existing comment'
                'delete:Delete a comment (tombstone)'
            )
            _describe -t subcommands 'comment subcommand' subcommands
            ;;
        args)
            case $line[1] in
                edit)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '-m[Comment message]:message:' \
                        '--json[Output result as JSON]' \
                        '-json[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:comment ID:'
                    ;;
                delete)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '--json[Output result as JSON]' \
                        '-json[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:comment ID:'
                    ;;
            esac
            ;;
    esac
}

_writ_issue() {
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
                'create:Create a new issue'
                'status:View or update issue status'
                'comment:Add a comment to an issue or resolve a thread'
                'assign:Add or remove issue assignees'
                'list:List issues'
                'link:Manage issue cross-reference links'
                'label:Add or remove issue labels'
            )
            _describe -t subcommands 'issue subcommand' subcommands
            ;;
        args)
            case $line[1] in
                create)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '-title[Issue title]:title:' \
                        '-description[Issue description]:description:' \
                        '-state[Initial issue state]:state:' \
                        '*-fixes[Add fixes cross-reference link]:ref:' \
                        '*-relates[Add relates cross-reference link]:ref:' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]'
                    ;;
                status)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '-reason[Reason for status change]:reason:' \
                        '(--json -json)'{--json,-json}'[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:issue ID:' \
                        '2:state:'
                    ;;
                comment)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '-m[Comment message text]:message:' \
                        '-reply-to[Comment ID to reply to]:comment ID:' \
                        '-resolve[Mark comment thread as resolved]' \
                        '-unresolve[Mark comment thread as unresolved]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:issue ID:'
                    ;;
                assign)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '*-add[Add assignee, a scheme:value person identifier]:assignee:' \
                        '*-remove[Remove assignee, a scheme:value person identifier]:assignee:' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:issue ID:'
                    ;;
                list)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '*-state[Filter by issue state]:state:' \
                        '*-assignee[Filter by assignee]:assignee:' \
                        '*-label[Filter by label]:label:' \
                        '*-author[Filter by author]:author:' \
                        '-text[Filter by text query]:text:' \
                        '-limit[Maximum issues to return]:limit:' \
                        '-sort[Sort order]:sort:(created_at_asc created_at_desc updated_at_asc updated_at_desc title_asc title_desc)' \
                        '(--json -json)'{--json,-json}'[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]'
                    ;;
                link)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '-target[Target reference]:ref:' \
                        '-relation[Link relation]:relation:(fixes relates none)' \
                        '-target-type[Target object type]:type:' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:issue ID:'
                    ;;
                label)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '*-add[Add label]:label:' \
                        '*-remove[Remove label]:label:' \
                        '(--json -json)'{--json,-json}'[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:issue ID:'
                    ;;
            esac
            ;;
    esac
}

_writ_review() {
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
                'open:Create a new code review'
                'comment:Add a comment to a review'
                'approve:Record a review verdict'
                'assign:Add or remove review assignees'
                'label:Add or remove review labels'
                'link:Manage review cross-reference links'
                'status:View or update review status'
                'list:List code reviews'
            )
            _describe -t subcommands 'review subcommand' subcommands
            ;;
        args)
            case $line[1] in
                open)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '-title[Review title]:title:' \
                        '-description[Review description]:description:' \
                        '-base[Base revision commit or ref]:ref:_git_revisions' \
                        '-head[Head revision commit or ref]:ref:_git_revisions' \
                        '-draft[Create review in draft state]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]'
                    ;;
                comment)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '-m[Comment message text]:message:' \
                        '-reply-to[Comment ID to reply to]:comment ID:' \
                        '-resolve[Mark comment thread as resolved]' \
                        '-unresolve[Mark comment thread as unresolved]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:review ID:'
                    ;;
                approve)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '-verdict[Review verdict]:verdict:(approve request-changes none)' \
                        '-revision[Revision commit ref or SHA]:revision:_git_revisions' \
                        '-m[Review verdict message]:message:' \
                        '-subject[Subject identity]:subject:' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:review ID:'
                    ;;
                assign)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '*-add[Add assignee, a scheme:value person identifier]:assignee:' \
                        '*-remove[Remove assignee, a scheme:value person identifier]:assignee:' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:review ID:'
                    ;;
                label)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '*-add[Add label]:label:' \
                        '*-remove[Remove label]:label:' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:review ID:'
                    ;;
                link)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '-target[Target reference]:ref:' \
                        '-relation[Link relation]:relation:(fixes relates none)' \
                        '-target-type[Target object type]:type:' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:review ID:'
                    ;;
                status)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '-reason[Reason for status change]:reason:' \
                        '-merge-commit[Merge commit ref or SHA]:commit:_git_revisions' \
                        '(--json -json)'{--json,-json}'[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]' \
                        '1:review ID:' \
                        '2:status:(draft open closed merged)'
                    ;;
                list)
                    _arguments -s -S \
                        '(-C)-C[Run as if writ was started in <dir>]:directory:_files -/' \
                        '*-status[Filter by review status]:status:(draft open closed merged)' \
                        '*-assignee[Filter by assignee]:assignee:' \
                        '*-label[Filter by label]:label:' \
                        '*-author[Filter by author]:author:' \
                        '-text[Filter by text query]:text:' \
                        '-limit[Maximum reviews to return]:limit:' \
                        '-sort[Sort order]:sort:(created_at_asc created_at_desc updated_at_asc updated_at_desc title_asc title_desc)' \
                        '(--json -json)'{--json,-json}'[Output result as JSON]' \
                        '(-h -help --help)'{-h,-help,--help}'[Show help]'
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
# Subcommands for comment`)
	for _, sub := range commentCmd.Subs {
		fmt.Fprintf(w, "complete -c writ -n '__fish_writ_needs_subcommand comment' -f -a '%s' -d '%s'\n",
			sub.Name, escapeFishDesc(sub.Short))
	}

	fmt.Fprintln(w, `
# Subcommands for issue`)
	for _, sub := range issueCmd.Subs {
		fmt.Fprintf(w, "complete -c writ -n '__fish_writ_needs_subcommand issue' -f -a '%s' -d '%s'\n",
			sub.Name, escapeFishDesc(sub.Short))
	}

	fmt.Fprintln(w, `
# Subcommands for object`)
	for _, sub := range objectCmd.Subs {
		fmt.Fprintf(w, "complete -c writ -n '__fish_writ_needs_subcommand object' -f -a '%s' -d '%s'\n",
			sub.Name, escapeFishDesc(sub.Short))
	}

	fmt.Fprintln(w, `
# Subcommands for review`)
	for _, sub := range reviewCmd.Subs {
		fmt.Fprintf(w, "complete -c writ -n '__fish_writ_needs_subcommand review' -f -a '%s' -d '%s'\n",
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
complete -c writ -n '__fish_writ_needs_subcommand help' -f -a 'init comment issue object review schema sync version completion help'
complete -c writ -n '__fish_writ_needs_subcommand help comment' -f -a 'edit delete'
complete -c writ -n '__fish_writ_needs_subcommand help issue' -f -a 'create status comment assign list link label'
complete -c writ -n '__fish_writ_needs_subcommand help object' -f -a 'create apply show list'
complete -c writ -n '__fish_writ_needs_subcommand help review' -f -a 'open comment approve assign label link status list'
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
			} else if f.Name == "draft" || f.Name == "json" || f.Name == "resolve" || f.Name == "unresolve" || f.Name == "status" && cmd.Name == "sync" {
				fmt.Fprintf(w, "complete -c writ -n '%s' %s -d '%s'\n",
					cond, optFlag, desc)
			} else {
				fmt.Fprintf(w, "complete -c writ -n '%s' %s -d '%s' -r\n",
					cond, optFlag, desc)
			}
		}

		// Positional enums
		if cmd.Name == "status" && len(path) == 2 && path[0] == "review" {
			fmt.Fprintf(w, "complete -c writ -n '%s' -f -a 'draft open closed merged'\n", cond)
		}
	}

	for _, sub := range rootCommand.Subs {
		walkFlags([]string{sub.Name}, sub)
	}
}
