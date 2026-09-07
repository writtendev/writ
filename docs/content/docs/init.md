---
title: "writ init"
weight: 20
---

Initialize writ configuration in a git repository: resolve or mint a writer ID, verify SSH signing key configuration, and add fetch refspecs for git remotes.

## Synopsis

```console
writ init [remote...]
```

## What it does

`writ init` prepares a repository for writ operations. It writes fetch refspecs into `.git/config` so that `git fetch` carries writ data into remote-tracking refs.

For public issue intake (a stranger without push access to `refs/writ/<writer-id>/*` filing a bug), and how an intake bot attributes external reporters truthfully, see ["What's hard, and what isn't solved"](../../why/#whats-hard-and-what-isnt-solved) in [Why Writ](../../why/).
