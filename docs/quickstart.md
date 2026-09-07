# Quickstart

This guide walks you through setting up Writ in a git repository, declaring a
schema-backed object type, creating and updating an object of that type, and
syncing operations to a collaborator across a remote repository.

Writ's engine knows merge types and value types, not SDLC types (see
`AGENTS.md`) — no built-in idea of what a "review" or an "issue" *means*.
What you call your objects and what fields they carry is something *you*
declare, in a `writ.schema` file, the same way a git repository knows
nothing about GitHub's issues until GitHub tells it what an issue looks
like. This walkthrough is therefore a schema-authoring tutorial first, and
an object tutorial second.

(This repository still ships a few built-in types — `review`, `issue`, and
the rest that `writ schema show` lists alongside whatever you declare below
— left over from before the engine's per-type code was deleted. They are
on their way out; treat their continued presence as an implementation
detail, not a promise.)

## 1. Set Up Your Repository & SSH Signing Key

Writ stores all operations directly inside your git repository as signed
commits under `refs/writ/*`.

Initialize your git repository and ensure your SSH signing key and identity
are configured:

```bash
git init
git config user.name "Alice"
git config user.email "alice@example.com"
git config gpg.format ssh
git config user.signingKey ~/.ssh/id_ed25519.pub
```

Make your initial commit:

```bash
echo "# My Project" > README.md
git add README.md
git commit -m "Initial commit"
git branch -M main
```

## 2. Initialize Writ

Run `writ init` to mint a unique writer ID, configure Writ's remote fetch
refspecs, and write a starter `writ.schema`:

```bash
writ init
```

Output:
```
Writer ID: 0123456789abcdef (minted)
Repo ID: a1b2c3d4e5f60718293a4b5c6d7e8f90 (minted)
Person ID: email:alice@example.com (derived from user.email)
Signing key: ~/.ssh/id_ed25519.pub (ssh)
No git remotes configured; fetch refspec will be added when a remote is configured.
Wrote starter /path/to/repo/writ.schema
```

The starter file `writ init` wrote has a namespace and nothing else — it
declares no types of its own (see the note above on this repository's own
still-built-in types, which live in the engine rather than in any
`writ.schema` file):

```
namespace my-project
```

## 3. Declare a Type

Edit `writ.schema` to declare a `ticket` type — the name is yours to choose;
`ticket` is just this guide's example — with a `title` field set by its
`create` and `update` ops:

```
namespace my-project

type ticket {
  op create 1, update 1 {
    title  string  lww
  }
}
```

Apply it, which signs and appends the ops that bring the schema object in
the log in line with this file:

```bash
writ schema apply
```

Output:
```
Created schema object 426905eb8b0b65f913ddcfd05905d1d3 (namespace "my-project").
Appended 6 op(s).
```

At any point you can ask Writ what vocabulary is actually installed and
folding right now — this is the source of truth for op and field names, not
the working-tree file you just edited:

```bash
writ schema show ticket
```

Output:
```
type  ticket
Ops:
  create  v1
  update  v1
Fields:
  title  create v1  string  lww
  title  update v1  string  lww
```

## 4. Create and Update an Object

`writ object` is generic plumbing over any schema-declared type — it knows
no `-title` flag, no per-type shape, only `-field <k>=<v>` pairs the
installed vocabulary parses:

```bash
writ object create ticket create -field "title=Add main entry point"
```

Output:
```
0192a1b2c3d4e5f60718293a4b5c6d7e
```

That bare id is the new object's id. Apply a further op against it:

```bash
writ object apply 0192a1b2c3d4e5f60718293a4b5c6d7e update \
  -field "title=Add main entry point (ready for review)"
```

Output:
```
0192a1b2c3d4e5f60718293a4b5c6d7e: applied update
```

Show its folded state, keyed by field:

```bash
writ object show 0192a1b2c3d4e5f60718293a4b5c6d7e
```

Output:
```
object_id    0192a1b2c3d4e5f60718293a4b5c6d7e
object_type  ticket
title        Add main entry point (ready for review)
```

## 5. Sync Operations with Remote

Add a git remote and sync Writ operations:

```bash
git remote add origin git@github.com:example/repo.git
writ sync origin
```

Output:
```
origin: pushed 8 ops
```

## 6. Collaborator Clones and Lists Objects

On another machine or clone, your collaborator initializes Writ and syncs:

```bash
git clone git@github.com:example/repo.git collab
cd collab
writ init
writ sync origin
```

Output:
```
origin: fetched 8 ops, 2 objects updated
```

Your collaborator can now list and inspect tickets offline — filtering to
one schema-declared type at a time:

```bash
writ object list ticket
```

Output:
```
0192a1b2  ticket  Alice <alice@example.com>  2026-08-31 16:00:00
```

And show one object's full folded state:

```bash
writ object show 0192a1b2
```

Output:
```
object_id    0192a1b2c3d4e5f60718293a4b5c6d7e
object_type  ticket
title        Add main entry point (ready for review)
```

## Where to go next

`writ object` and `writ schema` are deliberately plumbing, not porcelain —
see `docs/content/docs/object.md`. Everything here works the same for any
type your `writ.schema` declares; `writ schema show <type>` is always the
authoritative answer for what ops and fields exist, whatever this guide's
example drifts from.
