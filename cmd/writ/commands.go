package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/writtendev/writ/spec"
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
		commentCmd,
		docCmd,
		issueCmd,
		objectCmd,
		reviewCmd,
		stateCmd,
		labelCmd,
		settingsCmd,
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
	UsageLine: "Usage: writ init [-C <dir>] [remote...]",
	Long: "Initialize writ repository configuration by resolving or minting a writer ID,\n" +
		"verifying SSH signing key configuration, and adding fetch refspecs for git remotes.",
	Flags: []flagSpec{
		{Name: "C"},
	},
	Examples: []string{
		"writ init",
		"writ init origin",
	},
}

var commentCmd = &command{
	Name:      "comment",
	Short:     "Manage comments (edit, delete)",
	UsageLine: "Usage: writ comment [-C <dir>] <subcommand> [arguments]",
	Long:      "Manage comments on collaborative objects.",
	Flags: []flagSpec{
		{
			Name:  "C",
			Arg:   "<dir>",
			Usage: "Run as if writ was started in <dir>",
		},
	},
	Subs: []*command{
		commentEditCmd,
		commentDeleteCmd,
	},
}

var commentEditCmd = &command{
	Name:      "edit",
	Short:     "Edit an existing comment",
	UsageLine: "Usage: writ comment edit [-C <dir>] <id> -m <msg> [--json]",
	Long:      "Edit the text of an existing comment.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "m"},
		{Name: "json"},
	},
	Examples: []string{
		`writ comment edit 01J8ABC -m "Updated comment text"`,
		`writ comment edit 01J8ABC -m "Updated comment text" --json`,
	},
}

var commentDeleteCmd = &command{
	Name:      "delete",
	Short:     "Delete a comment (tombstone)",
	UsageLine: "Usage: writ comment delete [-C <dir>] <id> [--json]",
	Long:      "Delete a comment by creating a tombstone operation.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "json"},
	},
	Examples: []string{
		"writ comment delete 01J8ABC",
		"writ comment delete 01J8ABC --json",
	},
}

var issueCmd = &command{
	Name:      "issue",
	Short:     "Manage issues (create, update, status, comment, assign, list, link, label)",
	UsageLine: "Usage: writ issue [-C <dir>] <subcommand> [arguments]",
	Long:      "Manage issues.",
	Flags: []flagSpec{
		{
			Name:  "C",
			Arg:   "<dir>",
			Usage: "Run as if writ was started in <dir>",
		},
	},
	Subs: []*command{
		issueCreateCmd,
		issueUpdateCmd,
		issueStatusCmd,
		issueCommentCmd,
		issueAssignCmd,
		issueListCmd,
		issueLinkCmd,
		issueLabelCmd,
	},
}

var issueCreateCmd = &command{
	Name:      "create",
	Short:     "Create a new issue",
	UsageLine: "Usage: writ issue create [-C <dir>] -title <t> [-description <d>] [-state <s>] [-priority <p>] [-estimate <e>] [-position <pos>] [-fixes <ref>]... [-relates <ref>]...",
	Long:      "Create a new issue.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "title"},
		{Name: "description"},
		{Name: "state"},
		{Name: "priority", Values: spec.IssuePriorityNames()},
		{Name: "estimate"},
		{Name: "position"},
		{Name: "fixes", Repeatable: true},
		{Name: "relates", Repeatable: true},
	},
	Examples: []string{
		`writ issue create -title "Fix memory leak"`,
		`writ issue create -title "Bug in parser" -priority urgent -estimate 3 -fixes 01J8ABC`,
	},
}

var issueUpdateCmd = &command{
	Name:      "update",
	Short:     "Update an existing issue",
	UsageLine: "Usage: writ issue update [-C <dir>] <id> [-title <t>] [-description <d>] [-priority <p>] [-estimate <e>] [-position <pos>]",
	Long:      "Update an existing issue.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "title"},
		{Name: "description"},
		{Name: "priority", Values: spec.IssuePriorityNames()},
		{Name: "estimate"},
		{Name: "position"},
	},
	Examples: []string{
		`writ issue update 01J8ABC -title "Updated title"`,
		`writ issue update 01J8ABC -priority urgent -estimate 5`,
	},
}

var issueStatusCmd = &command{
	Name:      "status",
	Short:     "View or update issue status",
	UsageLine: "Usage: writ issue status [-C <dir>] <id> [<state>] [-reason <r>] [-position <pos>] [--json]",
	Long:      "View or update issue status.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "reason"},
		{Name: "position"},
		{Name: "json"},
	},
	Examples: []string{
		"writ issue status 01J8ABC",
		`writ issue status 01J8ABC closed -reason "resolved in #42"`,
		"writ issue status 01J8ABC --json",
	},
}

var issueCommentCmd = &command{
	Name:      "comment",
	Short:     "Add a comment to an issue or resolve a thread",
	UsageLine: "Usage: writ issue comment [-C <dir>] <id> [-m <text>] [-reply-to <comment-id>] [-resolve] [-unresolve]",
	Long:      "Add a comment to an issue or resolve/unresolve a comment thread.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "m"},
		{Name: "reply-to"},
		{Name: "resolve"},
		{Name: "unresolve"},
	},
	Examples: []string{
		`writ issue comment 01J8ABC -m "Investigating this now"`,
		`writ issue comment 01J8ABC -m "Fixed in main" -reply-to 01J8DEF`,
		`writ issue comment 01J8ABC -reply-to 01J8DEF -resolve`,
		`writ issue comment 01J8ABC -reply-to 01J8DEF -m "Resolved after testing" -resolve`,
	},
}

var issueAssignCmd = &command{
	Name:      "assign",
	Short:     "Add or remove issue assignees",
	UsageLine: "Usage: writ issue assign [-C <dir>] <id> [-add <a>]... [-remove <a>]...",
	Long:      "Add or remove issue assignees.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "add", Repeatable: true},
		{Name: "remove", Repeatable: true},
	},
	Examples: []string{
		"writ issue assign 01J8ABC -add email:alice@example.com",
		"writ issue assign 01J8ABC -remove user:bob",
	},
}

var issueListCmd = &command{
	Name:      "list",
	Short:     "List issues",
	UsageLine: "Usage: writ issue list [-C <dir>] [-state <s>]... [-assignee <a>]... [-label <l>]... [-author <a>]... [-priority <p>]... [-text <q>] [-limit N] [-sort <order>] [--json]",
	Long:      "List issues.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "state", Repeatable: true},
		{Name: "assignee", Repeatable: true},
		{Name: "label", Repeatable: true},
		{Name: "author", Repeatable: true},
		{Name: "priority", Values: spec.IssuePriorityNames(), Repeatable: true},
		{Name: "text"},
		{Name: "limit"},
		{Name: "sort", Values: []string{
			"created_at_asc", "created_at_desc",
			"updated_at_asc", "updated_at_desc",
			"title_asc", "title_desc",
			"priority_asc", "priority_desc",
			"position_asc", "position_desc",
			"estimate_asc", "estimate_desc",
		}},
		{Name: "json"},
	},
	Examples: []string{
		"writ issue list",
		"writ issue list -state open",
		"writ issue list -assignee email:alice@example.com --json",
	},
}

var issueLinkCmd = &command{
	Name:      "link",
	Short:     "Manage issue cross-reference links",
	UsageLine: "Usage: writ issue link [-C <dir>] <id> -target <ref> -relation fixes|relates|none [-target-type <t>]",
	Long:      "Manage issue cross-reference links.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "target"},
		{Name: "relation", Values: spec.LinkRelations()},
		{Name: "target-type"},
	},
	Examples: []string{
		"writ issue link 01J8ABC -target 01J8DEF -relation fixes",
		"writ issue link 01J8ABC -target other-repo#01J8DEF -relation relates",
	},
}

var issueLabelCmd = &command{
	Name:      "label",
	Short:     "Add or remove issue labels",
	UsageLine: "Usage: writ issue label [-C <dir>] <id> [-add <l>]... [-remove <l>]... [--json]",
	Long:      "Add or remove issue labels.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "add", Repeatable: true},
		{Name: "remove", Repeatable: true},
		{Name: "json"},
	},
	Examples: []string{
		"writ issue label 01J8ABC",
		"writ issue label 01J8ABC -add bug",
		"writ issue label 01J8ABC -remove duplicate",
		"writ issue label 01J8ABC --json",
	},
}

var objectCmd = &command{
	Name:      "object",
	Short:     "Generic create, apply, show, and list over any schema-declared object type",
	UsageLine: "Usage: writ object [-C <dir>] <subcommand> [arguments]",
	Long: "Create, apply ops to, show, and list collaborative objects of any type the installed\n" +
		"vocabulary declares (see `writ schema show`). This is plumbing, not porcelain: writ\n" +
		"knows no per-type verbs (no `title`, no `assignee`) because it does not know what an\n" +
		"issue or a review is -- only the schema in the log does. Prefer --json here for scripts\n" +
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
	UsageLine: "Usage: writ object create [-C <dir>] <type> <op-type> [-field <k>=<v>]... [-op-version <n>] [--json]",
	Long: "Append the op that starts a new object of <type>, using <op-type>'s field rules\n" +
		"from the installed vocabulary (`writ schema show <type>`) to parse each -field value.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "field", Repeatable: true},
		{Name: "op-version"},
		{Name: "json"},
	},
	Examples: []string{
		"writ object create ticket create -field title=\"Fix the thing\"",
		"writ object create ticket create -field title=\"Fix the thing\" --json",
	},
}

var objectApplyCmd = &command{
	Name:      "apply",
	Short:     "Apply a further op to an existing object",
	UsageLine: "Usage: writ object apply [-C <dir>] <object-id> <op-type> [-field <k>=<v>]... [-op-version <n>] [--json]",
	Long:      "Append a further op against an existing object, causally following its current frontier.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "field", Repeatable: true},
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
		"writ object list ticket",
		"writ object list ticket -text urgent --json",
	},
}

var reviewCmd = &command{
	Name:      "review",
	Short:     "Manage code reviews (open, comment, approve, assign, label, link, status, list)",
	UsageLine: "Usage: writ review [-C <dir>] <subcommand> [arguments]",
	Long:      "Manage code reviews.",
	Flags: []flagSpec{
		{
			Name:  "C",
			Arg:   "<dir>",
			Usage: "Run as if writ was started in <dir>",
		},
	},
	Subs: []*command{
		reviewOpenCmd,
		reviewCommentCmd,
		reviewApproveCmd,
		reviewAssignCmd,
		reviewLabelCmd,
		reviewLinkCmd,
		reviewStatusCmd,
		reviewListCmd,
	},
}

var reviewOpenCmd = &command{
	Name:      "open",
	Short:     "Create a new code review",
	UsageLine: "Usage: writ review open [-C <dir>] -title <t> [-description <d>] [-base <ref> -head <ref>] [-draft]",
	Long:      "Create a new code review.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "title"},
		{Name: "description"},
		{Name: "base"},
		{Name: "head"},
		{Name: "draft"},
	},
	Examples: []string{
		`writ review open -title "Add feature X"`,
		`writ review open -title "Add feature X" -base main -head feature-x`,
		`writ review open -title "WIP: feature" -draft`,
	},
}

var reviewCommentCmd = &command{
	Name:      "comment",
	Short:     "Add a comment to a review or resolve a thread",
	UsageLine: "Usage: writ review comment [-C <dir>] <id> [-m <text>] [-reply-to <comment-id>] [-resolve] [-unresolve]",
	Long:      "Add a comment to a review or resolve/unresolve a comment thread.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "m"},
		{Name: "reply-to"},
		{Name: "resolve"},
		{Name: "unresolve"},
	},
	Examples: []string{
		`writ review comment 01J8ABC -m "Looks good to me"`,
		`writ review comment 01J8ABC -m "Addressed feedback" -reply-to 01J8DEF`,
		`writ review comment 01J8ABC -reply-to 01J8DEF -resolve`,
		`writ review comment 01J8ABC -reply-to 01J8DEF -m "Fixed in latest push" -resolve`,
	},
}

var reviewApproveCmd = &command{
	Name:      "approve",
	Short:     "Record a review verdict",
	UsageLine: "Usage: writ review approve [-C <dir>] <id> [-verdict approve|request-changes|none] [-revision <ref>] [-m <msg>] [-subject <s>]",
	Long:      "Record a review verdict.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "verdict", Values: spec.ApprovalVerdicts()},
		{Name: "revision"},
		{Name: "m"},
		{Name: "subject"},
	},
	Examples: []string{
		"writ review approve 01J8ABC",
		`writ review approve 01J8ABC -verdict request-changes -m "Please fix tests"`,
		"writ review approve 01J8ABC -subject user:alice",
	},
}

var reviewAssignCmd = &command{
	Name:      "assign",
	Short:     "Add or remove review assignees (requested reviewers)",
	UsageLine: "Usage: writ review assign [-C <dir>] <id> [-add <a>]... [-remove <a>]...",
	Long:      "Add or remove review assignees (requested reviewers).",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "add", Repeatable: true},
		{Name: "remove", Repeatable: true},
	},
	Examples: []string{
		"writ review assign 01J8ABC -add email:alice@example.com",
		"writ review assign 01J8ABC -remove user:bob",
	},
}

var reviewLabelCmd = &command{
	Name:      "label",
	Short:     "Add or remove review labels",
	UsageLine: "Usage: writ review label [-C <dir>] <id> [-add <l>]... [-remove <l>]...",
	Long:      "Add or remove review labels.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "add", Repeatable: true},
		{Name: "remove", Repeatable: true},
	},
	Examples: []string{
		"writ review label 01J8ABC -add area/engine",
		"writ review label 01J8ABC -remove wip",
	},
}

var reviewLinkCmd = &command{
	Name:      "link",
	Short:     "Manage review cross-reference links",
	UsageLine: "Usage: writ review link [-C <dir>] <id> -target <ref> -relation fixes|relates|none [-target-type <t>]",
	Long:      "Manage review cross-reference links.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "target"},
		{Name: "relation", Values: spec.LinkRelations()},
		{Name: "target-type"},
	},
	Examples: []string{
		"writ review link 01J8ABC -target 01J8DEF -relation fixes",
		"writ review link 01J8ABC -target other-repo#01J8DEF -relation relates",
	},
}

var reviewStatusCmd = &command{
	Name:      "status",
	Short:     "View or update review status",
	UsageLine: "Usage: writ review status [-C <dir>] <id> [<state>] [-reason <r>] [-merge-commit <ref>] [--json]",
	Long:      "View or update review status.\n\nStates:\n  draft, open, closed, merged",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "reason"},
		{Name: "merge-commit"},
		{Name: "json"},
	},
	Examples: []string{
		"writ review status 01J8ABC",
		`writ review status 01J8ABC closed -reason "superseded"`,
		"writ review status 01J8ABC merged -merge-commit main",
		"writ review status 01J8ABC --json",
	},
}

var reviewListCmd = &command{
	Name:      "list",
	Short:     "List code reviews",
	UsageLine: "Usage: writ review list [-C <dir>] [-status <s>]... [-assignee <a>]... [-label <l>]... [-author <a>]... [-text <q>] [-limit N] [-sort <order>] [--json]",
	Long:      "List code reviews.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "status", Values: spec.ReviewStatuses(), Repeatable: true},
		{Name: "assignee", Repeatable: true},
		{Name: "label", Repeatable: true},
		{Name: "author", Repeatable: true},
		{Name: "text"},
		{Name: "limit"},
		{Name: "sort", Values: []string{"created_at_asc", "created_at_desc", "updated_at_asc", "updated_at_desc", "title_asc", "title_desc"}},
		{Name: "json"},
	},
	Examples: []string{
		"writ review list",
		"writ review list -status open",
		"writ review list -assignee email:alice@example.com",
		"writ review list -label area/engine",
		"writ review list -status open -status draft --json",
	},
}

var stateCmd = &command{
	Name:      "state",
	Short:     "Manage workflow states (list, create, update)",
	UsageLine: "Usage: writ state [-C <dir>] <subcommand> [arguments]",
	Long:      "Manage workflow states.",
	Flags: []flagSpec{
		{
			Name:  "C",
			Arg:   "<dir>",
			Usage: "Run as if writ was started in <dir>",
		},
	},
	Subs: []*command{
		stateListCmd,
		stateCreateCmd,
		stateUpdateCmd,
	},
}

var stateListCmd = &command{
	Name:      "list",
	Short:     "List workflow states",
	UsageLine: "Usage: writ state list [-C <dir>] [--json]",
	Long:      "List workflow states ordered by board position.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "json"},
	},
	Examples: []string{
		"writ state list",
		"writ state list --json",
	},
}

var stateCreateCmd = &command{
	Name:      "create",
	Short:     "Create a workflow state",
	UsageLine: "Usage: writ state create [-C <dir>] -name <name> -type <type> [-color <c>] [-position <pos>] [-description <d>]",
	Long:      "Create a new workflow state.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "name"},
		{Name: "type", Values: spec.WorkflowStateTypes()},
		{Name: "color"},
		{Name: "position"},
		{Name: "description"},
	},
	Examples: []string{
		`writ state create -name "In Review" -type started`,
		`writ state create -name QA -type started -color "#f2c94c" -position f`,
	},
}

var stateUpdateCmd = &command{
	Name:      "update",
	Short:     "Update a workflow state",
	UsageLine: "Usage: writ state update [-C <dir>] <id> [-name <name>] [-type <type>] [-color <c>] [-position <pos>] [-description <d>]",
	Long:      "Update an existing workflow state.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "name"},
		{Name: "type", Values: spec.WorkflowStateTypes()},
		{Name: "color"},
		{Name: "position"},
		{Name: "description"},
	},
	Examples: []string{
		`writ state update 01J8ABC -name "Code Review"`,
		`writ state update 01J8ABC -position f -color "#e2b93c"`,
	},
}

var labelCmd = &command{
	Name:      "label",
	Short:     "Manage labels (list, create, edit)",
	UsageLine: "Usage: writ label [-C <dir>] <subcommand> [arguments]",
	Long:      "Manage labels.",
	Flags: []flagSpec{
		{
			Name:  "C",
			Arg:   "<dir>",
			Usage: "Run as if writ was started in <dir>",
		},
	},
	Subs: []*command{
		labelListCmd,
		labelCreateCmd,
		labelEditCmd,
	},
}

var labelListCmd = &command{
	Name:      "list",
	Short:     "List labels",
	UsageLine: "Usage: writ label list [-C <dir>] [--json]",
	Long:      "List labels.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "json"},
	},
	Examples: []string{
		"writ label list",
		"writ label list --json",
	},
}

var labelCreateCmd = &command{
	Name:      "create",
	Short:     "Create a label",
	UsageLine: "Usage: writ label create [-C <dir>] -name <name> [-color <c>] [-description <d>]",
	Long:      "Create a new label.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "name"},
		{Name: "color"},
		{Name: "description"},
	},
	Examples: []string{
		`writ label create -name bug`,
		`writ label create -name bug -color "#d73a4a" -description "Something isn't working"`,
	},
}

var labelEditCmd = &command{
	Name:      "edit",
	Short:     "Edit a label",
	UsageLine: "Usage: writ label edit [-C <dir>] <id> [-name <name>] [-color <c>] [-description <d>]",
	Long:      "Edit an existing label.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "name"},
		{Name: "color"},
		{Name: "description"},
	},
	Examples: []string{
		`writ label edit 01J8ABC -color "#e2b93c"`,
		`writ label edit bug -name defect`,
	},
}

var settingsCmd = &command{
	Name:      "settings",
	Short:     "Manage repository configuration settings",
	UsageLine: "Usage: writ settings [-C <dir>] <subcommand> [arguments]",
	Long:      "View and update repository configuration settings (estimate scale, timezone, cycle cadence).",
	Flags: []flagSpec{
		{
			Name:  "C",
			Arg:   "<dir>",
			Usage: "Run as if writ was started in <dir>",
		},
	},
	Subs: []*command{
		settingsGetCmd,
		settingsSetCmd,
	},
}

var settingsGetCmd = &command{
	Name:      "get",
	Short:     "View repository settings",
	UsageLine: "Usage: writ settings get [-C <dir>] [--json]",
	Long:      "Display the current repository settings in tabular format or as machine-readable JSON.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "json"},
	},
	Examples: []string{
		"writ settings get",
		"writ settings get --json",
	},
}

var settingsSetCmd = &command{
	Name:      "set",
	Short:     "Update repository settings",
	UsageLine: "Usage: writ settings set [-C <dir>] [-name <name>] [-identifier <id>] [-timezone <tz>] [-estimate-scale <scale>] [-allow-zero-estimates <bool>] [-cycles-enabled <bool>] [-cycle-duration <weeks>] [-cycle-start-day <day>] [-cycle-cooldown <weeks>] [-triage-enabled <bool>] [--json]",
	Long:      "Update one or more repository configuration settings. Untouched settings and unknown keys are preserved.",
	Flags: []flagSpec{
		{Name: "C"},
		{Name: "json"},
		{
			Name:  "name",
			Arg:   "<name>",
			Usage: "Repository display name",
		},
		{
			Name:  "identifier",
			Arg:   "<id>",
			Usage: "Issue identifier prefix",
		},
		{
			Name:  "timezone",
			Arg:   "<tz>",
			Usage: "IANA timezone identifier",
		},
		{
			Name:   "estimate-scale",
			Arg:    "<scale>",
			Usage:  "Estimate scale (none, fibonacci, exponential, linear, t-shirt)",
			Values: []string{"none", "fibonacci", "exponential", "linear", "t-shirt"},
		},
		{
			Name:  "allow-zero-estimates",
			Arg:   "<bool>",
			Usage: "Allow zero as estimate (true|false)",
		},
		{
			Name:  "cycles-enabled",
			Arg:   "<bool>",
			Usage: "Enable cycles (true|false)",
		},
		{
			Name:  "cycle-duration",
			Arg:   "<weeks>",
			Usage: "Cycle duration in weeks",
		},
		{
			Name:  "cycle-start-day",
			Arg:   "<day>",
			Usage: "Cycle start day (1=Monday, 7=Sunday)",
		},
		{
			Name:  "cycle-cooldown",
			Arg:   "<weeks>",
			Usage: "Cycle cooldown in weeks",
		},
		{
			Name:  "triage-enabled",
			Arg:   "<bool>",
			Usage: "Enable triage mode (true|false)",
		},
	},
	Examples: []string{
		`writ settings set --name "Writ" --identifier WRIT`,
		`writ settings set --estimate-scale t-shirt --timezone America/New_York`,
		`writ settings set --cycles-enabled=true --cycle-duration 3`,
	},
}

var schemaCmd = &command{
	Name:      "schema",
	Short:     "Plan and apply the writ.schema working-tree file",
	UsageLine: "Usage: writ schema [-C <dir>] <subcommand> [arguments]",
	Long:      "Connect the writ.schema working-tree source file to the schema objects in the log.",
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
		"writ schema show ticket",
		"writ schema show ticket --json",
	},
}

var docCmd = &command{
	Name:      "doc",
	Short:     "Manage collaborative documents and sections",
	UsageLine: "Usage: writ doc [-C <dir>] <subcommand> [arguments]",
	Long:      "Manage collaborative documents, sections, ordering, and cross-references.",
	Flags: []flagSpec{
		{
			Name:  "C",
			Arg:   "<dir>",
			Usage: "Run as if writ was started in <dir>",
		},
	},
	Subs: []*command{
		docCreateCmd,
		docListCmd,
		docShowCmd,
		docEditCmd,
		docLinkCmd,
		docSectionCmd,
	},
}

var docCreateCmd = &command{
	Name:      "create",
	Short:     "Create a document",
	UsageLine: "Usage: writ doc create [-C <dir>] [-t <title>] [--link <target:relation>] [--label <l>] [--json]",
	Long:      "Create a new collaborative document object.",
	Flags: []flagSpec{
		{Name: "C", Arg: "<dir>", Usage: "Run as if writ was started in <dir>"},
		{Name: "t", Arg: "<title>", Usage: "Document title"},
		{Name: "link", Arg: "<target:rel>", Usage: "Link in target:relation[:type] format", Repeatable: true},
		{Name: "label", Arg: "<label>", Usage: "Label to attach", Repeatable: true},
		{Name: "json", Usage: "Output machine-readable JSON"},
	},
	Examples: []string{
		`writ doc create -t "RFC: Collaborative SDLC"`,
		`writ doc create -t "Design Doc" --link issue-42:plan --label architecture --json`,
	},
}

var docListCmd = &command{
	Name:      "list",
	Short:     "List documents",
	UsageLine: "Usage: writ doc list [-C <dir>] [--label <l>] [--json]",
	Long:      "List documents.",
	Flags: []flagSpec{
		{Name: "C", Arg: "<dir>", Usage: "Run as if writ was started in <dir>"},
		{Name: "label", Arg: "<label>", Usage: "Filter by label", Repeatable: true},
		{Name: "json", Usage: "Output machine-readable JSON"},
	},
	Examples: []string{
		"writ doc list",
		"writ doc list --label rfc --json",
	},
}

var docShowCmd = &command{
	Name:      "show",
	Short:     "Show document details and sections",
	UsageLine: "Usage: writ doc show [-C <dir>] <id> [--json]",
	Long:      "Display document metadata and ordered sections, including visual markers for any conflicted section bodies.",
	Flags: []flagSpec{
		{Name: "C", Arg: "<dir>", Usage: "Run as if writ was started in <dir>"},
		{Name: "json", Usage: "Output machine-readable JSON"},
	},
	Examples: []string{
		"writ doc show 01J8ABC",
		"writ doc show 01J8ABC --json",
	},
}

var docEditCmd = &command{
	Name:      "edit",
	Short:     "Edit document metadata",
	UsageLine: "Usage: writ doc edit [-C <dir>] <id> [-t <title>] [--label <l>] [--remove-label <l>] [--json]",
	Long:      "Update document title or labels.",
	Flags: []flagSpec{
		{Name: "C", Arg: "<dir>", Usage: "Run as if writ was started in <dir>"},
		{Name: "t", Arg: "<title>", Usage: "New title"},
		{Name: "label", Arg: "<label>", Usage: "Add label", Repeatable: true},
		{Name: "remove-label", Arg: "<label>", Usage: "Remove label", Repeatable: true},
		{Name: "json", Usage: "Output machine-readable JSON"},
	},
	Examples: []string{
		`writ doc edit 01J8ABC -t "RFC: Architecture (Updated)"`,
		`writ doc edit 01J8ABC --label approved --remove-label draft`,
	},
}

var docLinkCmd = &command{
	Name:      "link",
	Short:     "Attach a link to a document",
	UsageLine: "Usage: writ doc link [-C <dir>] <id> --target <target> --relation <relation> [--target-type <type>] [--json]",
	Long:      "Attach or update a cross-reference link on a document.",
	Flags: []flagSpec{
		{Name: "C", Arg: "<dir>", Usage: "Run as if writ was started in <dir>"},
		{Name: "target", Arg: "<target>", Usage: "Target entity identifier"},
		{Name: "relation", Arg: "<relation>", Usage: "Relationship predicate"},
		{Name: "target-type", Arg: "<type>", Usage: "Optional target type"},
		{Name: "json", Usage: "Output machine-readable JSON"},
	},
	Examples: []string{
		"writ doc link 01J8ABC --target issue-105 --relation implementation-plan",
	},
}

var docSectionCmd = &command{
	Name:      "section",
	Short:     "Manage document sections (add, edit, move, delete)",
	UsageLine: "Usage: writ doc section [-C <dir>] <subcommand> [arguments]",
	Long:      "Manage sections within collaborative documents.",
	Flags: []flagSpec{
		{
			Name:  "C",
			Arg:   "<dir>",
			Usage: "Run as if writ was started in <dir>",
		},
	},
	Subs: []*command{
		docSectionAddCmd,
		docSectionEditCmd,
		docSectionMoveCmd,
		docSectionDeleteCmd,
	},
}

var docSectionAddCmd = &command{
	Name:      "add",
	Short:     "Add a section to a document",
	UsageLine: "Usage: writ doc section add [-C <dir>] <doc-id> [-t <title>] [-m <body> | -F <file>] [--after <id>] [--before <id>] [--json]",
	Long:      "Create and append or position a new section in a document.",
	Flags: []flagSpec{
		{Name: "C", Arg: "<dir>", Usage: "Run as if writ was started in <dir>"},
		{Name: "t", Arg: "<title>", Usage: "Section title"},
		{Name: "m", Arg: "<body>", Usage: "Section body content"},
		{Name: "F", Arg: "<file>", Usage: "Read body from file ('-' for stdin)"},
		{Name: "after", Arg: "<id>", Usage: "Insert after section ID"},
		{Name: "before", Arg: "<id>", Usage: "Insert before section ID"},
		{Name: "json", Usage: "Output machine-readable JSON"},
	},
	Examples: []string{
		`writ doc section add 01J8ABC -t "Overview" -m "This document describes..."`,
		`writ doc section add 01J8ABC -t "Specification" -F spec.md --after 01J8SEC`,
	},
}

var docSectionEditCmd = &command{
	Name:      "edit",
	Short:     "Edit a section title or body",
	UsageLine: "Usage: writ doc section edit [-C <dir>] <section-id> [-t <title>] [-m <body> | -F <file>] [--json]",
	Long:      "Update a section's title or body, resolving any existing edit conflicts.",
	Flags: []flagSpec{
		{Name: "C", Arg: "<dir>", Usage: "Run as if writ was started in <dir>"},
		{Name: "t", Arg: "<title>", Usage: "Section title"},
		{Name: "m", Arg: "<body>", Usage: "New section body"},
		{Name: "F", Arg: "<file>", Usage: "Read new body from file ('-' for stdin)"},
		{Name: "json", Usage: "Output machine-readable JSON"},
	},
	Examples: []string{
		`writ doc section edit 01J8SEC -t "New Title"`,
		`writ doc section edit 01J8SEC -m "Updated section text"`,
		`writ doc section edit 01J8SEC -F draft.md`,
	},
}

var docSectionMoveCmd = &command{
	Name:      "move",
	Short:     "Move a section relative to siblings",
	UsageLine: "Usage: writ doc section move [-C <dir>] <section-id> [--after <id>] [--before <id>] [--json]",
	Long:      "Reorder a section by establishing a new fractional position between siblings.",
	Flags: []flagSpec{
		{Name: "C", Arg: "<dir>", Usage: "Run as if writ was started in <dir>"},
		{Name: "after", Arg: "<id>", Usage: "Move after section ID"},
		{Name: "before", Arg: "<id>", Usage: "Move before section ID"},
		{Name: "json", Usage: "Output machine-readable JSON"},
	},
	Examples: []string{
		"writ doc section move 01J8SEC --after 01J8FIRST",
		"writ doc section move 01J8SEC --before 01J8LAST",
	},
}

var docSectionDeleteCmd = &command{
	Name:      "delete",
	Short:     "Delete a section",
	UsageLine: "Usage: writ doc section delete [-C <dir>] <section-id> [--json]",
	Long:      "Soft-delete (tombstone) a section from its parent document.",
	Flags: []flagSpec{
		{Name: "C", Arg: "<dir>", Usage: "Run as if writ was started in <dir>"},
		{Name: "json", Usage: "Output machine-readable JSON"},
	},
	Examples: []string{
		"writ doc section delete 01J8SEC",
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
		"writ help issue",
		"writ help issue create",
		"writ help review",
		"writ help review open",
	},
}

var flagSetConstructors map[string]func() *flag.FlagSet

func init() {
	flagSetConstructors = map[string]func() *flag.FlagSet{
		"init":               func() *flag.FlagSet { fs, _ := newInitFlagSet(""); return fs },
		"comment edit":       func() *flag.FlagSet { fs, _ := newCommentEditFlagSet(""); return fs },
		"comment delete":     func() *flag.FlagSet { fs, _ := newCommentDeleteFlagSet(""); return fs },
		"issue create":       func() *flag.FlagSet { fs, _ := newIssueCreateFlagSet(""); return fs },
		"issue update":       func() *flag.FlagSet { fs, _ := newIssueUpdateFlagSet(""); return fs },
		"issue status":       func() *flag.FlagSet { fs, _ := newIssueStatusFlagSet(""); return fs },
		"issue comment":      func() *flag.FlagSet { fs, _ := newIssueCommentFlagSet(""); return fs },
		"issue assign":       func() *flag.FlagSet { fs, _ := newIssueAssignFlagSet(""); return fs },
		"issue list":         func() *flag.FlagSet { fs, _ := newIssueListFlagSet(""); return fs },
		"issue link":         func() *flag.FlagSet { fs, _ := newIssueLinkFlagSet(""); return fs },
		"issue label":        func() *flag.FlagSet { fs, _ := newIssueLabelFlagSet(""); return fs },
		"review open":        func() *flag.FlagSet { fs, _ := newReviewOpenFlagSet(""); return fs },
		"review comment":     func() *flag.FlagSet { fs, _ := newReviewCommentFlagSet(""); return fs },
		"review approve":     func() *flag.FlagSet { fs, _ := newReviewApproveFlagSet(""); return fs },
		"review assign":      func() *flag.FlagSet { fs, _ := newReviewAssignFlagSet(""); return fs },
		"review label":       func() *flag.FlagSet { fs, _ := newReviewLabelFlagSet(""); return fs },
		"review link":        func() *flag.FlagSet { fs, _ := newReviewLinkFlagSet(""); return fs },
		"review status":      func() *flag.FlagSet { fs, _ := newReviewStatusFlagSet(""); return fs },
		"review list":        func() *flag.FlagSet { fs, _ := newReviewListFlagSet(""); return fs },
		"state list":         func() *flag.FlagSet { fs, _ := newStateListFlagSet(""); return fs },
		"state create":       func() *flag.FlagSet { fs, _ := newStateCreateFlagSet(""); return fs },
		"state update":       func() *flag.FlagSet { fs, _ := newStateUpdateFlagSet(""); return fs },
		"label list":         func() *flag.FlagSet { fs, _ := newLabelListFlagSet(""); return fs },
		"label create":       func() *flag.FlagSet { fs, _ := newLabelCreateFlagSet(""); return fs },
		"label edit":         func() *flag.FlagSet { fs, _ := newLabelEditFlagSet(""); return fs },
		"doc create":         func() *flag.FlagSet { fs, _ := newDocCreateFlagSet(""); return fs },
		"doc list":           func() *flag.FlagSet { fs, _ := newDocListFlagSet(""); return fs },
		"doc show":           func() *flag.FlagSet { fs, _ := newDocShowFlagSet(""); return fs },
		"doc edit":           func() *flag.FlagSet { fs, _ := newDocEditFlagSet(""); return fs },
		"doc link":           func() *flag.FlagSet { fs, _ := newDocLinkFlagSet(""); return fs },
		"doc section add":    func() *flag.FlagSet { fs, _ := newDocSectionAddFlagSet(""); return fs },
		"doc section edit":   func() *flag.FlagSet { fs, _ := newDocSectionEditFlagSet(""); return fs },
		"doc section move":   func() *flag.FlagSet { fs, _ := newDocSectionMoveFlagSet(""); return fs },
		"doc section delete": func() *flag.FlagSet { fs, _ := newDocSectionDeleteFlagSet(""); return fs },
		"settings get":       func() *flag.FlagSet { fs, _ := newSettingsGetFlagSet(""); return fs },
		"settings set":       func() *flag.FlagSet { fs, _ := newSettingsSetFlagSet(""); return fs },
		"schema plan":        func() *flag.FlagSet { fs, _ := newSchemaPlanFlagSet(""); return fs },
		"schema apply":       func() *flag.FlagSet { fs, _ := newSchemaApplyFlagSet(""); return fs },
		"schema show":        func() *flag.FlagSet { fs, _ := newSchemaShowFlagSet(""); return fs },
		"object create":      func() *flag.FlagSet { fs, _ := newObjectCreateFlagSet(""); return fs },
		"object apply":       func() *flag.FlagSet { fs, _ := newObjectApplyFlagSet(""); return fs },
		"object show":        func() *flag.FlagSet { fs, _ := newObjectShowFlagSet(""); return fs },
		"object list":        func() *flag.FlagSet { fs, _ := newObjectListFlagSet(""); return fs },
		"sync":               func() *flag.FlagSet { fs, _ := newSyncFlagSet(""); return fs },
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
