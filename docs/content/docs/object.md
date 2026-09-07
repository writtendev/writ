---
title: "writ object"
weight: 26
---

Generic create, apply, show, and list over any schema-declared collaborative object type.

## Synopsis

```console
writ object create <type> <op-type> [-field <k>=<v>]... [-op-version <n>] [--json]
writ object apply  <object-id> <op-type> [-field <k>=<v>]... [-op-version <n>] [--json]
writ object show   <object-id> [--json]
writ object list   [<type>] [-author <a>]... [-text <q>] [-include-deleted] [-limit <n>] [-offset <n>] [-sort <order>] [--json]
```

## What this is, and is not

Writ knows merge types and value types, not SDLC types: the vocabulary of `review`, `issue`, `comment`, and the rest is a schema declared as data (`writ.schema`, `writ schema plan`/`apply`), the same way git knows nothing about GitHub. `writ object` is the CLI surface for any type that vocabulary declares, including a type writ has never heard of.

This is **plumbing**, not porcelain. `writ object create ticket create -field title=...` is worse to type than a hand-written `writ ticket create -title ...` would be — that is expected and correct, not a rough edge to smooth over. Writ no longer knows what a `ticket` is, so it cannot offer a good per-type verb for one. Nice per-type porcelain is a job for whatever layer owns the schema, built on `--json`. Nothing here generates subcommands, flags, or help text from the schema dynamically: that would make all of it schema-dependent, for a CLI whose main consumer is agents reading `--json` anyway.

## `create`

Appends the op that starts a new object of `<type>`, using `<op-type>` — a bare op name the vocabulary declares, not necessarily `create`; a log-declared type may name its creating op anything (`spec/op-envelope.md` §`op_type`).

Each `-field <k>=<v>` is looked up by `(type, op-type, op-version, field)` against `writ schema show <type>`'s field rules; an undeclared field is refused, naming the fields the op does declare. The raw string is then converted by the field's declared value type:

- `string`, `text`, `enum`, `person-ref`, `object-ref`, `git-oid`, `position`, `timestamp`, or no declared value type at all — the raw string, unchanged.
- `int`, `number`, `bool` — parsed accordingly; a bad value is refused at the CLI, naming the field and its declared value type.
- `anchor` — parsed as a JSON object.
- The same `-field k=` given more than once builds a JSON array of the parsed elements, in the order given, instead of a scalar — the input shape `set-union`/`set-observed-remove` strategies expect.

This is type-directed **parsing** only, never re-validation: enum membership, `max_length`, and pattern checks (a `position` must be a canonical base-62 fractional index, for example) are left to the producer validator that already runs inside `Objects.Create`, whose rejection already names the field and the reason.

`-op-version` defaults to `0`, resolved from the installed vocabulary: unambiguous only when `<type>` declares exactly one version of `<op-type>`. A type declaring several versions of the same op type requires `-op-version` explicitly.

```console
$ writ object create ticket create -field title="Fix the thing"
0123456789abcdef0123456789abcdef
```

## `apply`

Appends a further op against an existing object, causally following its current frontier. The object's type comes from the object itself — resolved from `<object-id>` — never from a flag; `-field` values are looked up against that type's rules exactly as `create` does.

```console
$ writ object apply 0123456789abcdef0123456789abcdef update -field title="Renamed"
0123456789abcdef0123456789abcdef: applied update
```

## `show`

Folds `<object-id>`'s state directly from the log — never the projection cache, so it never requires the cache to exist or be current — and prints it as a table of target key to value, plus any unknown ops (ops no installed rule interprets, preserved for forward compatibility rather than dropped).

```console
$ writ object show 0123456789abcdef0123456789abcdef
object_id    0123456789abcdef0123456789abcdef
object_type  ticket
title        Fix the thing
```

## `list`

Lists objects across every schema-declared type, or within one when `<type>` is given, served from the projection cache. `-author`, `-text`, `-include-deleted`, `-limit`, `-offset`, and `-sort` filter and page the results the same way the per-type `list` commands do.

```console
$ writ object list ticket -text urgent
01234567 ticket  Alice <alice@example.com>  2026-01-01 00:00:00
```

## JSON output

All four verbs support `--json`: `object.create`, `object.apply`, `object.show`, and `object.list` in `docs/cli-json.md`. `object.show`'s `fields` is an **open map keyed by target key** — a rule's declared `target` when it has one, otherwise its field name — not a fixed field set: the keys present, and each value's shape, are whatever the installed vocabulary for that object's type declares. This is the one place this CLI's usual "additive-only, never retyped" JSON promise carries a schema-shaped carve-out, because the object type it describes is data, not a Go struct writ ships.
