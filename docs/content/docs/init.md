---
title: "writ init"
weight: 20
---

Initialize writ configuration in a git repository: resolve or mint a writer ID, verify SSH signing key configuration, and add fetch refspecs for git remotes.

## Synopsis

```console
writ init [--namespace <name>] [remote...]
```

## What it does

`writ init` prepares a repository for writ operations. It writes fetch refspecs into `.git/config` so that `git fetch` carries writ data into remote-tracking refs.

On a work tree with no `writ.schema` yet, it also writes a starter one — a single `namespace <name>` line, no types. That namespace becomes the public name of every type the schema goes on to declare (it is baked into the schema object's id and into every wire type), so `writ init` never derives it: pass `--namespace <name>` explicitly, or answer the interactive prompt when stdin and stderr are both a terminal. A non-interactive run with neither refuses rather than choosing one for you. A bare repository, or one whose `writ.schema` already exists, needs no namespace and keeps succeeding non-interactively either way.

For public issue intake (a stranger without push access to `refs/writ/<writer-id>/*` filing a bug), and how an intake bot attributes external reporters truthfully, see ["What's hard, and what isn't solved"](../../why/#whats-hard-and-what-isnt-solved) in [Why Writ](../../why/).
