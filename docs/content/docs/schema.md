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

`writ init` writes a starter `writ.schema` — a namespace line and nothing else — for any repository with a working tree that doesn't already have one.

## `plan`

Parses `writ.schema`, folds the schema objects already in the repository, and prints the ops applying the file would append. `plan` appends no ops of its own; it exits `1` on an invalid file or a refused plan, `2` on a usage error, and `0` otherwise.

The comparison is always between compiled declarations and folded log state, never source text: comments, spacing, and the order types and op blocks appear in the file have no bearing on the result. Reordering, reindenting, and commenting an already-applied file produces a zero-op plan.

### What `plan` refuses

Nothing is ever removed from the log, so `plan` refuses any edit that would silently drop a declaration instead of compiling it into a no-op:

- An invalid file: unknown value type, unknown merge strategy, a strategy/value-type mismatch, `keyed-lww` without a `key(...)`.
- A removed type, op, or field. Mark it `deprecated` instead.
- A removed `description`. No merge strategy in this vocabulary can clear one once written.
- An un-deprecation. No op clears `deprecated` once it is set.
- A namespace change, when it can be told apart from declaring an unrelated, brand-new namespace — see "Which schema object `apply` writes to" below. `create`'s `namespace` field folds `create-once`: the first value ever written is permanent.

## `apply`

Runs the same computation as `plan` — it never trusts a previous run, since there is no plan artifact and no recorded target id — then signs and appends the resulting ops.

## Which schema object `apply` writes to

Computed fresh on every run by folding the schema objects present in the repository and matching the file's `namespace`:

1. **Exactly one match** → that object is the target; its id is reused. If the file also declares an `object_type` a *different* schema object already binds, this is refused too, exactly as case 3 below — reuse is not exempt from the contested-type guard.
2. **No match, and no other schema object binds any `object_type` the file declares** → a fresh object id is minted (128 bits of CSPRNG randomness, 32 lowercase hex, per `spec/identifiers.md`), and `apply` reports `created`.
3. **No match, but another schema object already binds a type the file declares** → refused, naming the object id(s) and the contested type(s). Applying would bind the same `object_type` to two schema objects, and `RulesFromSchemas` responds to that collision by withholding **every** rule for the contested type — permanently, since nothing is ever removed and `deprecate-type` does not unbind a type. When every contested type traces back to one single existing object, the message reads as what it is: a namespace change (that object's own namespace, set once by its first `create` op, would differ from the file's) rather than a generic collision between two independent objects.
4. **More than one namespace match** → refused, naming the object ids. The repository already has a namespace collision to resolve before `apply` can pick between them.

A namespace change that *also* renames or drops every type the old object declared leaves no signal behind to catch: resolution is computed fresh from the file and the folded log, with no stored association between them, so a file that no longer names any of its old object's types is indistinguishable from a legitimate, brand-new, independent namespace. Keep at least one type name across a rename if you want the mistake caught.

There is no `--object-id` flag. Every case it would serve is a repository already in the state this resolution exists to prevent.

### Known residual risk

Two writers who each create the repository's *first* schema object while offline still end up with two schema objects, unrecoverably: nothing here can tell "no schema object exists yet" from "one exists but I haven't fetched it," and a well-known canonical object id (the way `settings` has one) was considered and rejected — `spec/schema-ops.md` explicitly denies `schema` one in normative text, and `spec/identifiers.md` makes random minting a producer MUST. Closing this needs a normative spec change, tracked separately (WRIT-199); fetch and sync before running `writ schema apply` for the first time in a repository, and coordinate the first schema object out of band if more than one person might create it concurrently.

## `show`

Reports the vocabulary actually installed and folding right now (`Store.Types`): whatever the log declares. This answers a different question than `plan`/`apply` do — theirs is the working-tree `writ.schema` file's own view; `show`'s is what the log has actually folded to. With no `<type>`, prints one bare type name per line, deliberately bare so shell completion can be a plain `$(writ schema show)` call with nothing to parse. With `<type>`, prints that type's declared ops and fields, including the constraints `writ object create`/`apply` leave to the producer validator rather than re-checking at the CLI (`enum`, `max_length`, and the rest — see `object.md` and `docs/cli-json.md`'s `schema.show` field table). Each field row also names the target key it folds into when that target differs from the field's own name — the one attribute a caller needs to reconcile `-field <name>` on `object create`/`apply` with the target-keyed `fields` map `object show` reports back.

`schema show schema` is a special case: `schema` never appears in the bare-name listing and is not one of `Store.Types`' entries — it is writ's one hard-coded object type (`spec/schema-ops.md`), not schema-declared, so it has no `Fields`/`Ops` this command's usual machinery resolves. Naming it explicitly still succeeds rather than failing "not declared" (that reads exactly as wrong as the same failure would for `writ object list schema`, which lists real `schema` rows once `writ schema apply` has run); it just prints the bare type name with no ops or fields, since there is nothing else here to report.

## JSON output

All three verbs support `--json` (`schema.plan`, `schema.apply`, and `schema.show` in `docs/cli-json.md`). `plan` and `apply` report the target object id, its namespace, whether it was minted fresh, and the ops appended (or that would be) as their normative wire bodies — the same shape a conforming implementation of `spec/schema-ops.md` reads and writes. `writ schema plan --json` omits `object_id` on a creation plan (`created: true`): `plan` mints no id of its own, so there is no id yet that a later `apply` is bound to reuse. `writ schema apply --json` always reports the real id, since apply resolves and mints its own target and then writes to exactly that id. `writ schema show --json` reports the resolved vocabulary itself: an array of types with no `<type>` argument, or one type's full declaration with it.
