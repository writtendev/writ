---
title: "CLI Reference"
slug: "cli"
---

# CLI Reference

`writ` is an open SDLC layer that stores code review and issues inside git.

## Table of Contents

- [`writ init`](#writ-init)
- [`writ comment edit`](#writ-comment-edit)
- [`writ comment delete`](#writ-comment-delete)
- [`writ doc create`](#writ-doc-create)
- [`writ doc list`](#writ-doc-list)
- [`writ doc show`](#writ-doc-show)
- [`writ doc edit`](#writ-doc-edit)
- [`writ doc link`](#writ-doc-link)
- [`writ doc section`](#writ-doc-section)
- [`writ issue create`](#writ-issue-create)
- [`writ issue update`](#writ-issue-update)
- [`writ issue status`](#writ-issue-status)
- [`writ issue comment`](#writ-issue-comment)
- [`writ issue assign`](#writ-issue-assign)
- [`writ issue list`](#writ-issue-list)
- [`writ issue link`](#writ-issue-link)
- [`writ issue label`](#writ-issue-label)
- [`writ review open`](#writ-review-open)
- [`writ review comment`](#writ-review-comment)
- [`writ review approve`](#writ-review-approve)
- [`writ review assign`](#writ-review-assign)
- [`writ review label`](#writ-review-label)
- [`writ review link`](#writ-review-link)
- [`writ review status`](#writ-review-status)
- [`writ review list`](#writ-review-list)
- [`writ state list`](#writ-state-list)
- [`writ state create`](#writ-state-create)
- [`writ state update`](#writ-state-update)
- [`writ label list`](#writ-label-list)
- [`writ label create`](#writ-label-create)
- [`writ label edit`](#writ-label-edit)
- [`writ settings get`](#writ-settings-get)
- [`writ settings set`](#writ-settings-set)
- [`writ schema plan`](#writ-schema-plan)
- [`writ schema apply`](#writ-schema-apply)
- [`writ sync`](#writ-sync)
- [`writ version`](#writ-version)
- [`writ completion`](#writ-completion)
- [`writ help`](#writ-help)

## Commands

### `writ init`

Initialize writ configuration (writer ID and remote fetch refspecs)

#### Synopsis

```console
Usage: writ init [-C <dir>] [remote...]
```

#### Description

Initialize writ repository configuration by resolving or minting a writer ID,
verifying SSH signing key configuration, and adding fetch refspecs for git remotes.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>

#### Examples

```bash
writ init
writ init origin
```

### `writ comment edit`

Edit an existing comment

#### Synopsis

```console
Usage: writ comment edit [-C <dir>] <id> -m <msg> [--json]
```

#### Description

Edit the text of an existing comment.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-m <msg>`: Comment message <msg>
- `-json`: Output result as JSON

#### Examples

```bash
writ comment edit 01J8ABC -m "Updated comment text"
writ comment edit 01J8ABC -m "Updated comment text" --json
```

### `writ comment delete`

Delete a comment (tombstone)

#### Synopsis

```console
Usage: writ comment delete [-C <dir>] <id> [--json]
```

#### Description

Delete a comment by creating a tombstone operation.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-json`: Output result as JSON

#### Examples

```bash
writ comment delete 01J8ABC
writ comment delete 01J8ABC --json
```

### `writ doc create`

Create a document

#### Synopsis

```console
Usage: writ doc create [-C <dir>] [-t <title>] [--link <target:relation>] [--label <l>] [--json]
```

#### Description

Create a new collaborative document object.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-t string`: Document title
- `-link value`: Link in target:relation[:type] format (repeatable)
- `-label value`: Label to attach (repeatable)
- `-json`: Output machine-readable JSON

#### Examples

```bash
writ doc create -t "RFC: Collaborative SDLC"
writ doc create -t "Design Doc" --link issue-42:plan --label architecture --json
```

### `writ doc list`

List documents

#### Synopsis

```console
Usage: writ doc list [-C <dir>] [--label <l>] [--json]
```

#### Description

List documents.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-label value`: Filter by label (repeatable)
- `-json`: Output machine-readable JSON

#### Examples

```bash
writ doc list
writ doc list --label rfc --json
```

### `writ doc show`

Show document details and sections

#### Synopsis

```console
Usage: writ doc show [-C <dir>] <id> [--json]
```

#### Description

Display document metadata and ordered sections, including visual markers for any conflicted section bodies.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-json`: Output machine-readable JSON

#### Examples

```bash
writ doc show 01J8ABC
writ doc show 01J8ABC --json
```

### `writ doc edit`

Edit document metadata

#### Synopsis

```console
Usage: writ doc edit [-C <dir>] <id> [-t <title>] [--label <l>] [--remove-label <l>] [--json]
```

#### Description

Update document title or labels.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-t string`: New title
- `-label value`: Add label (repeatable)
- `-remove-label value`: Remove label (repeatable)
- `-json`: Output machine-readable JSON

#### Examples

```bash
writ doc edit 01J8ABC -t "RFC: Architecture (Updated)"
writ doc edit 01J8ABC --label approved --remove-label draft
```

### `writ doc link`

Attach a link to a document

#### Synopsis

```console
Usage: writ doc link [-C <dir>] <id> --target <target> --relation <relation> [--target-type <type>] [--json]
```

#### Description

Attach or update a cross-reference link on a document.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-target string`: Target entity identifier
- `-relation string`: Relationship predicate
- `-target-type string`: Optional target type
- `-json`: Output machine-readable JSON

#### Examples

```bash
writ doc link 01J8ABC --target issue-105 --relation implementation-plan
```

### `writ doc section`

Manage document sections (add, edit, move, delete)

#### Synopsis

```console
Usage: writ doc section [-C <dir>] <subcommand> [arguments]
```

#### Description

Manage sections within collaborative documents.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>

### `writ issue create`

Create a new issue

#### Synopsis

```console
Usage: writ issue create [-C <dir>] -title <t> [-description <d>] [-state <s>] [-priority <p>] [-estimate <e>] [-position <pos>] [-fixes <ref>]... [-relates <ref>]...
```

#### Description

Create a new issue.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-title <t>`: Issue title <t> (required)
- `-description <d>`: Issue description <d>
- `-state <state>`: Initial issue state <state> (workflow-state name or ID)
- `-priority <p>`: Issue priority <p> (urgent|high|medium|low|none or 0..4)
- `-estimate <e>`: Issue estimate <e> (non-negative number)
- `-position <pos>`: Issue position <pos> (fractional index)
- `-fixes <ref>`: Add a 'fixes' cross-reference link <ref> (repeatable)
- `-relates <ref>`: Add a 'relates' cross-reference link <ref> (repeatable)

#### Examples

```bash
writ issue create -title "Fix memory leak"
writ issue create -title "Bug in parser" -priority urgent -estimate 3 -fixes 01J8ABC
```

### `writ issue update`

Update an existing issue

#### Synopsis

```console
Usage: writ issue update [-C <dir>] <id> [-title <t>] [-description <d>] [-priority <p>] [-estimate <e>] [-position <pos>]
```

#### Description

Update an existing issue.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-title <t>`: Updated title <t>
- `-description <d>`: Updated description <d>
- `-priority <p>`: Updated priority <p> (urgent|high|medium|low|none or 0..4)
- `-estimate <e>`: Updated estimate <e> (non-negative number)
- `-position <pos>`: Updated position <pos> (fractional index)

#### Examples

```bash
writ issue update 01J8ABC -title "Updated title"
writ issue update 01J8ABC -priority urgent -estimate 5
```

### `writ issue status`

View or update issue status

#### Synopsis

```console
Usage: writ issue status [-C <dir>] <id> [<state>] [-reason <r>] [-position <pos>] [--json]
```

#### Description

View or update issue status.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-reason <r>`: Reason <r> for status change
- `-position <pos>`: Updated position <pos> (fractional index)
- `-json`: Output result as JSON (view mode only)

#### Examples

```bash
writ issue status 01J8ABC
writ issue status 01J8ABC closed -reason "resolved in #42"
writ issue status 01J8ABC --json
```

### `writ issue comment`

Add a comment to an issue or resolve a thread

#### Synopsis

```console
Usage: writ issue comment [-C <dir>] <id> [-m <text>] [-reply-to <comment-id>] [-resolve] [-unresolve]
```

#### Description

Add a comment to an issue or resolve/unresolve a comment thread.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-m <text>`: Comment text <text>
- `-reply-to <comment-id>`: Comment ID <comment-id> to reply to
- `-resolve`: Mark comment thread as resolved, attributed to writ.personId, else email:<user.email>
- `-unresolve`: Mark comment thread as unresolved, preserving the recorded resolver

#### Examples

```bash
writ issue comment 01J8ABC -m "Investigating this now"
writ issue comment 01J8ABC -m "Fixed in main" -reply-to 01J8DEF
writ issue comment 01J8ABC -reply-to 01J8DEF -resolve
writ issue comment 01J8ABC -reply-to 01J8DEF -m "Resolved after testing" -resolve
```

### `writ issue assign`

Add or remove issue assignees

#### Synopsis

```console
Usage: writ issue assign [-C <dir>] <id> [-add <a>]... [-remove <a>]...
```

#### Description

Add or remove issue assignees.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-add <a>`: Add assignee <a>, a scheme:value person identifier (repeatable)
- `-remove <a>`: Remove assignee <a>, a scheme:value person identifier (repeatable)

#### Examples

```bash
writ issue assign 01J8ABC -add email:alice@example.com
writ issue assign 01J8ABC -remove user:bob
```

### `writ issue list`

List issues

#### Synopsis

```console
Usage: writ issue list [-C <dir>] [-state <s>]... [-assignee <a>]... [-label <l>]... [-author <a>]... [-priority <p>]... [-text <q>] [-limit N] [-sort <order>] [--json]
```

#### Description

List issues.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-state <s>`: Filter by issue state <s> (repeatable)
- `-assignee <a>`: Filter by assignee <a>, a scheme:value person identifier (repeatable)
- `-label <l>`: Filter by label <l> (repeatable)
- `-author <a>`: Filter by author <a> name or email (repeatable)
- `-priority <p>`: Filter by priority <p> (urgent|high|medium|low|none or 0..4) (repeatable)
- `-text <q>`: Filter by text <q> match in title or description
- `-limit N`: Maximum number N of issues to return
- `-sort <order>`: Sort order <order> (created_at_asc, created_at_desc, updated_at_asc, updated_at_desc, title_asc, title_desc, priority_asc, priority_desc, position_asc, position_desc, estimate_asc, estimate_desc)
- `-json`: Output result as JSON

#### Examples

```bash
writ issue list
writ issue list -state open
writ issue list -assignee email:alice@example.com --json
```

### `writ issue link`

Manage issue cross-reference links

#### Synopsis

```console
Usage: writ issue link [-C <dir>] <id> -target <ref> -relation fixes|relates|none [-target-type <t>]
```

#### Description

Manage issue cross-reference links.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-target <ref>`: Target reference <ref> (required, e.g. <repo-id>#<object-id> or <object-id>)
- `-relation <rel>`: Link relation <rel>: fixes, relates, or none (required)
- `-target-type <t>`: Target object type <t>

#### Examples

```bash
writ issue link 01J8ABC -target 01J8DEF -relation fixes
writ issue link 01J8ABC -target other-repo#01J8DEF -relation relates
```

### `writ issue label`

Add or remove issue labels

#### Synopsis

```console
Usage: writ issue label [-C <dir>] <id> [-add <l>]... [-remove <l>]... [--json]
```

#### Description

Add or remove issue labels.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-add <l>`: Add label <l> (repeatable)
- `-remove <l>`: Remove label <l> (repeatable)
- `-json`: Output result as JSON

#### Examples

```bash
writ issue label 01J8ABC
writ issue label 01J8ABC -add bug
writ issue label 01J8ABC -remove duplicate
writ issue label 01J8ABC --json
```

### `writ review open`

Create a new code review

#### Synopsis

```console
Usage: writ review open [-C <dir>] -title <t> [-description <d>] [-base <ref> -head <ref>] [-draft]
```

#### Description

Create a new code review.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-title <t>`: Review title <t> (required)
- `-description <d>`: Review description <d>
- `-base <ref>`: Base revision <ref> commit or ref
- `-head <ref>`: Head revision <ref> commit or ref
- `-draft`: Create review in draft state

#### Examples

```bash
writ review open -title "Add feature X"
writ review open -title "Add feature X" -base main -head feature-x
writ review open -title "WIP: feature" -draft
```

### `writ review comment`

Add a comment to a review or resolve a thread

#### Synopsis

```console
Usage: writ review comment [-C <dir>] <id> [-m <text>] [-reply-to <comment-id>] [-resolve] [-unresolve]
```

#### Description

Add a comment to a review or resolve/unresolve a comment thread.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-m <text>`: Comment message text <text>
- `-reply-to <comment-id>`: Comment ID <comment-id> to reply to
- `-resolve`: Mark comment thread as resolved, attributed to writ.personId, else email:<user.email>
- `-unresolve`: Mark comment thread as unresolved, preserving the recorded resolver

#### Examples

```bash
writ review comment 01J8ABC -m "Looks good to me"
writ review comment 01J8ABC -m "Addressed feedback" -reply-to 01J8DEF
writ review comment 01J8ABC -reply-to 01J8DEF -resolve
writ review comment 01J8ABC -reply-to 01J8DEF -m "Fixed in latest push" -resolve
```

### `writ review approve`

Record a review verdict

#### Synopsis

```console
Usage: writ review approve [-C <dir>] <id> [-verdict approve|request-changes|none] [-revision <ref>] [-m <msg>] [-subject <s>]
```

#### Description

Record a review verdict.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-verdict approve|request-changes|none`: Verdict approve|request-changes|none (default: approve)
- `-revision <ref>`: Revision commit ref or SHA <ref> (defaults to latest head)
- `-m <msg>`: Verdict message <msg>
- `-subject <s>`: Subject person identifier <s>, scheme:value (defaults to writ.personId, else email:<user.email>)

#### Examples

```bash
writ review approve 01J8ABC
writ review approve 01J8ABC -verdict request-changes -m "Please fix tests"
writ review approve 01J8ABC -subject user:alice
```

### `writ review assign`

Add or remove review assignees (requested reviewers)

#### Synopsis

```console
Usage: writ review assign [-C <dir>] <id> [-add <a>]... [-remove <a>]...
```

#### Description

Add or remove review assignees (requested reviewers).

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-add <a>`: Add assignee <a>, a scheme:value person identifier (repeatable)
- `-remove <a>`: Remove assignee <a>, a scheme:value person identifier (repeatable)

#### Examples

```bash
writ review assign 01J8ABC -add email:alice@example.com
writ review assign 01J8ABC -remove user:bob
```

### `writ review label`

Add or remove review labels

#### Synopsis

```console
Usage: writ review label [-C <dir>] <id> [-add <l>]... [-remove <l>]...
```

#### Description

Add or remove review labels.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-add <l>`: Add label <l> (repeatable)
- `-remove <l>`: Remove label <l> (repeatable)

#### Examples

```bash
writ review label 01J8ABC -add area/engine
writ review label 01J8ABC -remove wip
```

### `writ review link`

Manage review cross-reference links

#### Synopsis

```console
Usage: writ review link [-C <dir>] <id> -target <ref> -relation fixes|relates|none [-target-type <t>]
```

#### Description

Manage review cross-reference links.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-target <ref>`: Target reference <ref> (required, e.g. <repo-id>#<object-id> or <object-id>)
- `-relation <rel>`: Link relation <rel>: fixes, relates, or none (required)
- `-target-type <t>`: Target object type <t>

#### Examples

```bash
writ review link 01J8ABC -target 01J8DEF -relation fixes
writ review link 01J8ABC -target other-repo#01J8DEF -relation relates
```

### `writ review status`

View or update review status

#### Synopsis

```console
Usage: writ review status [-C <dir>] <id> [<state>] [-reason <r>] [-merge-commit <ref>] [--json]
```

#### Description

View or update review status.

States:
  draft, open, closed, merged

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-reason <r>`: Reason <r> for status change
- `-merge-commit <ref>`: Merge commit ref or SHA <ref> (valid when setting status to merged)
- `-json`: Output result as JSON (view mode only)

#### Examples

```bash
writ review status 01J8ABC
writ review status 01J8ABC closed -reason "superseded"
writ review status 01J8ABC merged -merge-commit main
writ review status 01J8ABC --json
```

### `writ review list`

List code reviews

#### Synopsis

```console
Usage: writ review list [-C <dir>] [-status <s>]... [-assignee <a>]... [-label <l>]... [-author <a>]... [-text <q>] [-limit N] [-sort <order>] [--json]
```

#### Description

List code reviews.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-status <s>`: Filter by review status <s> (repeatable)
- `-assignee <a>`: Filter by assignee <a>, a scheme:value person identifier (repeatable)
- `-label <l>`: Filter by label <l> (repeatable)
- `-author <a>`: Filter by author <a> name or email (repeatable)
- `-text <q>`: Filter by text <q> match in title or description
- `-limit N`: Maximum number N of reviews to return
- `-sort <order>`: Sort order <order> (created_at_asc, created_at_desc, updated_at_asc, updated_at_desc, title_asc, title_desc)
- `-json`: Output result as JSON

#### Examples

```bash
writ review list
writ review list -status open
writ review list -assignee email:alice@example.com
writ review list -label area/engine
writ review list -status open -status draft --json
```

### `writ state list`

List workflow states

#### Synopsis

```console
Usage: writ state list [-C <dir>] [--json]
```

#### Description

List workflow states ordered by board position.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-json`: Output machine-readable JSON

#### Examples

```bash
writ state list
writ state list --json
```

### `writ state create`

Create a workflow state

#### Synopsis

```console
Usage: writ state create [-C <dir>] -name <name> -type <type> [-color <c>] [-position <pos>] [-description <d>]
```

#### Description

Create a new workflow state.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-name string`: State display name
- `-type string`: State type (backlog, unstarted, started, completed, canceled)
- `-color string`: Hex color client hint
- `-position string`: Fractional order position
- `-description string`: State description

#### Examples

```bash
writ state create -name "In Review" -type started
writ state create -name QA -type started -color "#f2c94c" -position f
```

### `writ state update`

Update a workflow state

#### Synopsis

```console
Usage: writ state update [-C <dir>] <id> [-name <name>] [-type <type>] [-color <c>] [-position <pos>] [-description <d>]
```

#### Description

Update an existing workflow state.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-name value`: State display name
- `-type string`: State type (backlog, unstarted, started, completed, canceled)
- `-color string`: Hex color client hint
- `-position string`: Fractional order position
- `-description string`: State description

#### Examples

```bash
writ state update 01J8ABC -name "Code Review"
writ state update 01J8ABC -position f -color "#e2b93c"
```

### `writ label list`

List labels

#### Synopsis

```console
Usage: writ label list [-C <dir>] [--json]
```

#### Description

List labels.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-json`: Output machine-readable JSON

#### Examples

```bash
writ label list
writ label list --json
```

### `writ label create`

Create a label

#### Synopsis

```console
Usage: writ label create [-C <dir>] -name <name> [-color <c>] [-description <d>]
```

#### Description

Create a new label.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-name string`: Label display name
- `-color string`: Hex color client hint
- `-description string`: Label description

#### Examples

```bash
writ label create -name bug
writ label create -name bug -color "#d73a4a" -description "Something isn't working"
```

### `writ label edit`

Edit a label

#### Synopsis

```console
Usage: writ label edit [-C <dir>] <id> [-name <name>] [-color <c>] [-description <d>]
```

#### Description

Edit an existing label.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-name value`: Label display name
- `-color string`: Hex color client hint
- `-description string`: Label description

#### Examples

```bash
writ label edit 01J8ABC -color "#e2b93c"
writ label edit bug -name defect
```

### `writ settings get`

View repository settings

#### Synopsis

```console
Usage: writ settings get [-C <dir>] [--json]
```

#### Description

Display the current repository settings in tabular format or as machine-readable JSON.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-json`: Output machine-readable JSON

#### Examples

```bash
writ settings get
writ settings get --json
```

### `writ settings set`

Update repository settings

#### Synopsis

```console
Usage: writ settings set [-C <dir>] [-name <name>] [-identifier <id>] [-timezone <tz>] [-estimate-scale <scale>] [-allow-zero-estimates <bool>] [-cycles-enabled <bool>] [-cycle-duration <weeks>] [-cycle-start-day <day>] [-cycle-cooldown <weeks>] [-triage-enabled <bool>] [--json]
```

#### Description

Update one or more repository configuration settings. Untouched settings and unknown keys are preserved.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-json`: Output machine-readable JSON
- `-name string`: Repository display name
- `-identifier string`: Issue identifier prefix
- `-timezone string`: IANA timezone identifier
- `-estimate-scale string`: Estimate scale (none, fibonacci, exponential, linear, t-shirt)
- `-allow-zero-estimates string`: Allow zero as estimate (true|false)
- `-cycles-enabled string`: Enable cycles (true|false)
- `-cycle-duration int`: Cycle duration in weeks
- `-cycle-start-day int`: Cycle start day (1=Monday, 7=Sunday)
- `-cycle-cooldown int`: Cycle cooldown in weeks
- `-triage-enabled string`: Enable triage mode (true|false)

#### Examples

```bash
writ settings set --name "Writ" --identifier WRIT
writ settings set --estimate-scale t-shirt --timezone America/New_York
writ settings set --cycles-enabled=true --cycle-duration 3
```

### `writ schema plan`

Show the ops writ.schema would append

#### Synopsis

```console
Usage: writ schema plan [-C <dir>] [--json]
```

#### Description

Parse writ.schema, fold the schema objects in the repository, and print the ops applying the file would append. Appends no ops.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-json`: Output machine-readable JSON

#### Examples

```bash
writ schema plan
writ schema plan --json
```

### `writ schema apply`

Sign and append the ops writ.schema declares

#### Synopsis

```console
Usage: writ schema apply [-C <dir>] [--json]
```

#### Description

Run the same computation as `writ schema plan`, then sign and append the resulting ops.

#### Flags

- `-C string`: Run as if writ was started in <dir>
- `-json`: Output machine-readable JSON

#### Examples

```bash
writ schema apply
writ schema apply --json
```

### `writ sync`

Synchronize operations with git remotes

#### Synopsis

```console
Usage: writ sync [-C <dir>] [--status] [--json] [remote...]
```

#### Description

Synchronize collaborative SDLC operations with one or more git remotes.

Fetch remote operations, push local operations, and refresh the local projection cache.
With no remote specified, defaults to 'origin' or the sole configured remote.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-status`: Report unpushed ops count without network transport
- `-json`: Output result as JSON

#### Exit Codes

- `0`: Success
- `1`: Transport or unclassified git failure
- `2`: Usage error (bad flag, no resolvable default remote)
- `3`: Unknown or unconfigured remote
- `4`: Rejected non-fast-forward update
- `5`: Not a git repository / store cannot be opened
- `6`: Authentication or credentials failure
- `7`: Network or remote unreachable

#### Examples

```bash
writ sync
writ sync origin
writ sync --status
writ sync --status --json
```

### `writ version`

Print the writ version

#### Synopsis

```console
Usage: writ version
```

#### Description

Print the version of the writ binary.

#### Examples

```bash
writ version
```

### `writ completion`

Generate shell completion scripts

#### Synopsis

```console
Usage: writ completion <shell>
```

#### Description

Generate shell completion scripts for bash, zsh, or fish.

Supported shells: bash, zsh, fish.

#### Examples

```bash
writ completion bash > /etc/bash_completion.d/writ
writ completion zsh > "${fpath[1]}/_writ"
writ completion fish > ~/.config/fish/completions/writ.fish
```

### `writ help`

Show help for writ or a subcommand

#### Synopsis

```console
Usage: writ help [command...]
```

#### Description

Show help for writ or a subcommand.

#### Examples

```bash
writ help
writ help issue
writ help issue create
writ help review
writ help review open
```

