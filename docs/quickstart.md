# Quickstart

This guide walks you through setting up Writ in a git repository, declaring a
schema-backed object type, creating and updating an object of that type, and
syncing operations to a collaborator across a remote repository.

Writ's engine knows merge types and value types, not SDLC types (see
`AGENTS.md`) — no built-in idea of what any of your object types *mean*.
What you call your objects and what fields they carry is something *you*
declare, in a `writ.schema` file, the same way a git repository knows
nothing about GitHub's issues until GitHub tells it what an issue looks
like. This walkthrough is therefore a schema-authoring tutorial first, and
an object tutorial second.

## 1. Set Up Your Repository & SSH Signing Key

Writ stores all operations directly inside your git repository as signed
commits under `refs/writ/*`.

Create a project directory and initialize your git repository, then ensure
your SSH signing key and identity are configured. Step 2 chooses a
namespace explicitly with `--namespace`; this walkthrough uses
`my-project`, but the namespace is independent of the directory name — it
is the public name of every type your schema goes on to declare, so it is
never derived from anything.

```bash
mkdir my-project
cd my-project
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
refspecs, and write a starter `writ.schema`. Writing a starter file needs a
namespace, and `writ init` never derives one — it becomes the public name
of every type your schema declares, so pass it explicitly with
`--namespace` (an interactive terminal is prompted for it instead, but a
script or agent running this non-interactively must pass the flag):

```bash
writ init --namespace my-project
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
Created schema object schema:my-project (namespace "my-project").
This repository now declares 1 namespace: my-project.
Appended 6 op(s).
```

At any point you can ask Writ what vocabulary is actually installed and
folding right now — this is the source of truth for op and field names, not
the working-tree file you just edited:

```bash
writ schema show my-project.ticket
```

Output:
```
type       my-project.ticket
namespace  my-project
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
writ object create my-project.ticket create -field "title=Add main entry point"
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
object_id     0192a1b2c3d4e5f60718293a4b5c6d7e
object_type   my-project.ticket
verification  wrong-key
title         Add main entry point (ready for review)
```

`verification` reports `wrong-key`, not `valid`, because this guide never configured a
trust store: step 1 set `gpg.format` and `user.signingKey` so ops could be *signed*, but
signed and *trusted* are different questions, and nothing here named which keys are
authorized to sign as which people. `writ` prints a one-line hint to stderr saying so.
Folding never depends on the answer — `title` above is correct either way — but a caller
that wants to treat this state as authentic checks `verification` itself; configuring a
`gpg.ssh.allowedSignersFile` mapping principals to authorized keys is what turns this
`wrong-key` into `valid` (`spec/signing.md` §Trust Store), and is a walkthrough of its own.

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

On another machine or clone, your collaborator initializes Writ and syncs.
This walkthrough never `git add`s `writ.schema` (step 1 commits only
`README.md`), so the clone has no `writ.schema` either, and their
`writ init` needs a namespace too; it names the starter file `writ init`
would write for *this* work tree, not anything that gets synced, so it
need not even match yours. (If you do commit `writ.schema` — it is a
working-tree source file meant to be shared, the same way a `package.json`
is — a collaborator's clone already has one, and their `writ init` needs
no `--namespace` at all: it reports the file already exists and leaves it
unchanged.)

```bash
git clone git@github.com:example/repo.git collab
cd collab
writ init --namespace my-project
writ sync origin
```

Output:
```
origin: fetched 8 ops, 2 objects updated
```

### Clone with full history

Writ's operations are commits under `refs/writ/*`, and folding an object
correctly needs each op commit's full ancestry — including its blobs —
to be present locally. `writ sync` fetches those refs without `--depth`,
so an ordinary **shallow** clone (`--depth=...`) of the repository does
not truncate them: `writ sync` still fetches the complete op chains even
though `.git/shallow` stays in place. A **partial** clone
(`--filter=...`) is the one that bites: it leaves some op commits'
objects missing from the local store, and an ordinary `writ sync` does
not backfill them.

* GitHub Actions: `actions/checkout`'s `filter:` input is independent of
  its `fetch-depth:` input — leave `filter:` unset (the default) rather
  than passing `blob:none` or similar. Its `sparse-checkout:` input
  applies `blob:none` on its own whenever `filter:` is unset, and
  setting `filter: ''` does not override that
  ([actions/checkout#1949](https://github.com/actions/checkout/issues/1949)):
  skip `sparse-checkout:` if you need every op's objects, or repair
  afterward as below.
* Plain git: clone without `--filter`. Repair an existing partial clone
  with `git fetch --refetch --no-filter`; plain `--refetch` re-applies
  the clone's configured filter and leaves the same objects missing.

What a partial clone's missing objects do to `writ object show` depends
on how much of an object's history got filtered out. Missing *every* op
for an object fails outright — `writ: object not found`, exit 1. Missing
only *some* of them does not: the object folds from whatever ops
survived the filter and exits 0, which can be a stale but
plausible-looking answer — for example a size-limit filter (`git clone
--filter=blob:limit=...`) that lets small op blobs through but drops one
large one silently returns the value from before that op, with no error
and nothing in the output pointing at what is missing.

`writ sync` reports ops it could not apply — `N ops not applied` in
porcelain, `"rejected": N` under `--json` — and that count is the only
signal that anything was dropped. It mixes two different causes it does
not distinguish on its own: a malformed op rejected on reader
validation, or an op commit naming an object this clone does not have (a
partial clone, above); see `RefreshStats.Rejections` in the Go API,
where each entry's reason does distinguish them. The count is also
one-shot: it reflects only the sync call that observed the rejection, so
a later `writ sync` that finds nothing new to fetch reports `up to date`
with no `rejected`/`ops not applied` field at all — even though the
object folded above is still stale.

Your collaborator can now list and inspect tickets offline — filtering to
one schema-declared type at a time:

```bash
writ object list my-project.ticket
```

Output:
```
0192a1b2  my-project.ticket  Alice <alice@example.com>  2026-08-31 16:00:00  [verification: wrong-key]
```

And show one object's full folded state:

```bash
writ object show 0192a1b2
```

Output:
```
object_id     0192a1b2c3d4e5f60718293a4b5c6d7e
object_type   my-project.ticket
verification  wrong-key
title         Add main entry point (ready for review)
```

Still `wrong-key`, for the same reason as step 4: no trust store was ever configured in
either repository, so there is no key list to check either party's ops against.

## Where to go next

`writ object` and `writ schema` are deliberately plumbing, not porcelain —
see the [object reference](content/docs/object.md). Everything here works the same for any
type your `writ.schema` declares; `writ schema show <type>` is always the
authoritative answer for what ops and fields exist, whatever this guide's
example drifts from.
