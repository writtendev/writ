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

Writ's operations are commits under `refs/writ/*`; folding an object correctly needs each op commit's full ancestry to be present locally. A shallow clone (`--depth=...`) or a partial clone (`--filter=...`) leaves some op commits absent, and fetching into an already-shallow repository keeps it shallow.

Check out at full depth: `fetch-depth: 0` for `actions/checkout`, or a plain `git clone` without `--depth`/`--filter`. Repair an existing shallow clone with `git fetch --unshallow`, an existing partial clone with `git fetch --refetch`.

A nonzero `rejected` count in `writ sync`'s output is the signal that op commits are missing locally, not that a peer wrote bad ops. In the Go API, `Store.Refresh` and `Store.Rebuild` return this as `RefreshStats.Rejections`, with each entry carrying reason `object-unavailable` (`dag.RejectObjectUnavailable`) for this case.
