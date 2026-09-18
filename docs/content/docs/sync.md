---
title: "writ sync"
weight: 50
---

Synchronize operations with git remotes: fetch remote operations, push local operations, and refresh the local projection cache.

## Synopsis

```console
writ sync [--status] [--json] [remote...]
```

## What it does

`writ sync` ensures refspecs are configured, fetches remote writ refs, pushes local operations, and refreshes the SQLite projection cache. With `--status`, it reports the count of unpushed operations without performing network transport.

## Clone depth

Writ's operations are commits under `refs/writ/*`; folding an object correctly needs each op commit's full ancestry, including its blobs, to be present locally. `writ sync` fetches those refs without `--depth`, so an ordinary shallow clone (`--depth=...`) of the repository does not leave any op commits absent — `writ sync` still fetches the complete chains, even though `.git/shallow` stays in place. A partial clone (`--filter=...`) is the one that leaves objects missing: it excludes blobs those op commits need, and an ordinary `writ sync` does not backfill them.

Clone without `--filter`; in CI, leave `actions/checkout`'s `filter:` input unset (it is independent of its `fetch-depth:` input, so `fetch-depth: 0` alone does not help). Repair an existing partial clone with `git fetch --refetch --no-filter` — plain `--refetch` re-applies the clone's configured filter and leaves the same objects missing. On a partial clone with objects still missing, `writ object show` fails outright (`writ: object not found`, exit 1), not with a silently-wrong result.

A nonzero `rejected` count in `writ sync`'s output does not by itself mean a peer wrote bad ops: it also counts op commits naming an object this clone does not have (a partial clone, above). In the Go API, `Store.Refresh` and `Store.Rebuild` return this as `RefreshStats.Rejections`, where each entry's reason distinguishes a reader-validation rejection from `object-unavailable` (`dag.RejectObjectUnavailable`) for this case.
