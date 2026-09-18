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

With no `remote` argument, every remote `git remote` lists is configured. `git remote` can list a remote writ cannot use — a url-less `[remote "x"]` section (a global `remote.<name>.prune`, set once, creates one in every repository on the machine) or a `-`-leading name (`git remote add -- -x <url>` succeeds) — and neither is a name anyone passed to `writ init`. Discovering one of these no longer stops the run: `writ init` configures every remote it can and reports, on stderr, the ones it skipped and why. The exit code tells the two cases apart: a url-less section never had anything configured for it, so skipping it exits 0; a real, named remote (a url is set) whose name writ cannot use is a genuine partial result, exiting 1 (not the usage-error exit 2) to say "not everything got done" without implying a bad argument. The remedy differs too — `git remote remove <name>` works for the real remote but fails ("No such remote") on a url-less section, which comes out instead with `git config --remove-section remote.<name>`. Naming a remote explicitly on the command line keeps the original behavior — a bad name you typed stops the run right there, since there is nothing else for that invocation to do.

For public issue intake (a stranger without push access to `refs/writ/<writer-id>/*` filing a bug), and how an intake bot attributes external reporters truthfully, see ["What's hard, and what isn't solved"](../../why/#whats-hard-and-what-isnt-solved) in [Why Writ](../../why/).
