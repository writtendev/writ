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

## Public intake and bot attribution

Writ operations require push access to `refs/writ/<writer-id>/*`. Unauthenticated
public contributors cannot write ops directly into the repository.

For open-source projects or public intake of any kind, teams run an **intake
bot**: a designated writer with push credentials that bridges incoming
webhooks, web forms, email, or an external tracker into writ operations.

To attribute external reporters truthfully without synthesizing fake email
addresses, intake bots use the `user:` person identifier scheme
(`user:<service>-<id>`, such as `user:github-octocat`). This accurately records
the reporter's origin identity while the bot signs the operation commit.
