---
title: "writ schema"
weight: 25
---

Connect the `writ.schema` working-tree source file to the schema objects in the log: preview and append the ops a schema declaration compiles to.

## Synopsis

```console
writ schema plan [--json]
writ schema apply [--json]
writ schema show [<type>] [--json]
```

## What `writ.schema` is

Writ hard-codes exactly one collaborative object type: `schema`. Every other object type — `review`, `comment`, `issue`, and anything a consumer declares — is data a `schema` object writes into the log. `writ.schema` is a human-editable, working-tree source form for that data, Prisma-style: the log stays the source of truth, and the file is a view onto it, not a second store of it. See the schema layer section of `ARCHITECTURE.md` and `spec/schema-source.md` for the full grammar.

`writ init` writes a starter `writ.schema` — a namespace line and nothing else — for any repository with a working tree that doesn't already have one, using the namespace `--namespace <name>` supplies (or an interactive prompt answers); see [`writ init`](../init/).

## `plan`

Parses `writ.schema`, folds the schema objects already in the repository, and prints the ops applying the file would append. `plan` appends no ops of its own; it exits `1` on an invalid file or a refused plan, `2` on a usage error, and `0` otherwise.

The comparison is always between compiled declarations and folded log state, never source text: comments, spacing, and the order types and op blocks appear in the file have no bearing on the result. Reordering, reindenting, and commenting an already-applied file produces a zero-op plan.

### What `plan` refuses

Nothing is ever removed from the log, so `plan` refuses any edit that would silently drop a declaration instead of compiling it into a no-op:

- An invalid file: unknown value type, unknown merge strategy, a strategy/value-type mismatch, `keyed-lww` without a `key(...)`.
- A removed type, op, or field. Mark it `deprecated` instead.
- A removed `description`. No merge strategy in this vocabulary can clear one once written.
- An un-deprecation. No op clears `deprecated` once it is set.
- A namespace change. A schema object's identity is its namespace, and `create`'s `namespace` field folds `create-once`: the first value ever written is permanent.

## `apply`

Runs the same computation as `plan` — it never trusts a previous run, since there is no plan artifact and no recorded target id — then signs and appends the resulting ops.

## Which schema object `apply` writes to

Computed fresh on every run by folding the schema objects present in the repository and matching the file's `namespace`:

1. **Existing schema object matches namespace** → that object is the target; its id is reused.
2. **No match** → the target id is derived from the namespace, `schema:<namespace>` (`spec/identifiers.md`'s schema carve-out), and `apply` reports `created`. Two writers bootstrapping the same namespace offline derive the same id, so they converge on one schema object instead of minting two that bind the same types.

There is no `--object-id` flag. Every case it would serve is a repository already in the state this resolution exists to prevent.

## Type names containing `--`

A type name may contain `--`, but it is a foot-gun worth avoiding. The query surface maps `-` to `_` in a type's table name, so type `a--b` generates the table `o_<ns>.a__b` — the same table the type `a` generates for a collection target named `b` in the same namespace. Two types cannot own one table, so the lexically later type (here `a--b`) is withheld from the query surface: it gets no generated tables, so its ops are kept as unknown ops rather than materialized, and queries that read a type's fields (such as a text search) do not see its objects. Its ops stay in the log, and `Objects.Get` still folds them. Only the author of the schema can trigger this, and only against their own namespace; avoid `--` in type names, or avoid a collection target whose name would spell one.

## `show`

Reports the vocabulary actually installed and folding right now (`Store.Types`): whatever the log declares. This answers a different question than `plan`/`apply` do — theirs is the working-tree `writ.schema` file's own view; `show`'s is what the log has actually folded to. With no `<type>`, prints one bare type name per line, deliberately bare: one candidate per line, for completion scripts to read line by line. With `<type>`, prints that type's declared ops and fields, including the constraints `writ object create`/`apply` leave to the producer validator rather than re-checking at the CLI (`enum`, `max_length`, and the rest — see `object.md` and `docs/cli-json.md`'s `schema.show` field table). Each field row also names the target key it folds into when that target differs from the field's own name — the one attribute a caller needs to reconcile `-field <name>` on `object create`/`apply` with the target-keyed `fields` map `object show` reports back.

`schema show schema` is a special case: `schema` never appears in the bare-name listing and is not one of `Store.Types`' entries — it is writ's one hard-coded object type (`spec/schema-ops.md`), not schema-declared, so it has no `Fields`/`Ops` this command's usual machinery resolves. Naming it explicitly still succeeds rather than failing "not declared" (that reads exactly as wrong as the same failure would for `writ object list schema`, which lists real `schema` rows once `writ schema apply` has run); it just prints the bare type name with no ops or fields, since there is nothing else here to report.

## JSON output

All three verbs support `--json` (`schema.plan`, `schema.apply`, and `schema.show` in `docs/cli-json.md`). `plan` and `apply` report the target object id, its namespace, whether it would be (or was) created fresh, and the ops appended (or that would be) as their normative wire bodies — the same shape a conforming implementation of `spec/schema-ops.md` reads and writes. `object_id` is always present on both: since the id is derived from the namespace rather than minted, a creation plan's id is the exact id `apply` would write to, not a preview. `writ schema show --json` reports the resolved vocabulary itself: an array of types with no `<type>` argument, or one type's full declaration with it.
