---
title: "CLI Reference"
slug: "cli"
---

# CLI Reference

`writ` stores signed, append-only, mergeable state inside a git repository, under types your own `writ.schema` declares.

## Table of Contents

- [`writ init`](#writ-init)
- [`writ object create`](#writ-object-create)
- [`writ object apply`](#writ-object-apply)
- [`writ object show`](#writ-object-show)
- [`writ object list`](#writ-object-list)
- [`writ schema plan`](#writ-schema-plan)
- [`writ schema apply`](#writ-schema-apply)
- [`writ schema show`](#writ-schema-show)
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

### `writ object create`

Create a new object of a schema-declared type

#### Synopsis

```console
Usage: writ object create [-C <dir>] <type> <op-type> [-field <k>=<v>]... [-field-json <k>=<v>]... [-op-version <n>] [--json]
```

#### Description

Append the op that starts a new object of <type>, using <op-type>'s field rules
from the installed vocabulary (`writ schema show <type>`) to parse each -field value.
-field-json sets a field from raw JSON instead, for a field -field cannot express: one
that is object-shaped, or that declares no value type at all (such as an
{object_type, object_id} record naming another object).

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-field <k>=<v>`: Field <k>=<v> to set on the creating op (repeatable; repeat the same key for a set)
- `-field-json <k>=<v>`: Field <k>=<v> to set from raw JSON, skipping type-directed conversion (repeatable; the escape hatch for an object-shaped or untyped field, such as an {object_type, object_id} record naming another object)
- `-op-version version`: Explicit op version (default: resolved from the installed vocabulary)
- `-json`: Output result as JSON

#### Examples

```bash
writ object create ticket create -field title="Fix the thing"
writ object create ticket create -field title="Fix the thing" --json
writ object create gadget create -field-json subject='{"object_type":"ticket","object_id":"<id>"}' -field text="needs a second look"
```

### `writ object apply`

Apply a further op to an existing object

#### Synopsis

```console
Usage: writ object apply [-C <dir>] <object-id> <op-type> [-field <k>=<v>]... [-field-json <k>=<v>]... [-op-version <n>] [--json]
```

#### Description

Append a further op against an existing object, causally following its current frontier.
-field-json sets a field from raw JSON instead of -field's type-directed conversion --
see `writ object create -h`.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-field <k>=<v>`: Field <k>=<v> to set on the op (repeatable; repeat the same key for a set)
- `-field-json <k>=<v>`: Field <k>=<v> to set from raw JSON, skipping type-directed conversion (repeatable; the escape hatch for an object-shaped or untyped field, such as an {object_type, object_id} record naming another object)
- `-op-version version`: Explicit op version (default: resolved from the installed vocabulary)
- `-json`: Output result as JSON

#### Examples

```bash
writ object apply 01J8ABC update -field title="Renamed"
```

### `writ object show`

Show an object's folded state

#### Synopsis

```console
Usage: writ object show [-C <dir>] <object-id> [--json]
```

#### Description

Fold an object's state directly from the log and print it, keyed by target field.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-json`: Output result as JSON

#### Examples

```bash
writ object show 01J8ABC
writ object show 01J8ABC --json
```

### `writ object list`

List objects across or within a type

#### Synopsis

```console
Usage: writ object list [-C <dir>] [<type>] [-author <a>]... [-text <q>] [-include-deleted] [-limit N] [-offset N] [-sort <order>] [--json]
```

#### Description

List collaborative objects, optionally filtered to one schema-declared type.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-author <a>`: Filter by author <a> name or email (repeatable)
- `-text <q>`: Filter by text <q> match
- `-include-deleted`: Include deleted objects
- `-limit N`: Maximum number N of objects to return
- `-offset N`: Skip the first N matching objects
- `-sort <order>`: Sort order <order> (created_at_asc, created_at_desc, updated_at_asc, updated_at_desc)
- `-json`: Output result as JSON

#### Examples

```bash
writ object list
writ object list ticket
writ object list ticket -text urgent --json
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

### `writ schema show`

Show the vocabulary actually installed and folding now

#### Synopsis

```console
Usage: writ schema show [-C <dir>] [<type>] [--json]
```

#### Description

Report the vocabulary Store.Types resolves right now -- built-in types overlaid by
whatever the log declares -- which is not the same question `writ schema plan`/`apply`
answer (the working-tree writ.schema file's own view). With no <type>, print one bare
type name per line. With <type>, print that type's declared ops and fields.

#### Flags

- `-C <dir>`: Run as if writ was started in <dir>
- `-json`: Output result as JSON

#### Examples

```bash
writ schema show
writ schema show ticket
writ schema show ticket --json
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
writ help object
writ help object create
writ help schema
writ help schema show
```

