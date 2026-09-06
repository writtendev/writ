# Changelog

All notable changes to writ are documented in this file. The format is based
on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Each version's section is what GitHub publishes as that release's body.

## [Unreleased]

### Added
- Document collaborative object type (`document` and `section`), multi-value fold register strategy, fractional section ordering, and CLI porcelain (`writ doc`).
- Carried `object_type` in `writ.UnknownOp`, `state.UnknownOp`, CLI JSON output, and SQLite projection cache to satisfy forward-compatibility rule `FC-5`.

### Removed
- Workspace routing (WRIT-180): `writ.Workspace`, `writ.WorkspaceInfo`, `WithWorkspace`, `writ.ResolveReference`, `writ.ResolvedReference`, `ErrWorkspaceRemoteURLNotSupported`, and `ErrWorkspaceUnconfigured` are gone from the engine's public API, along with the `writ.workspace` git config key and `writ init`'s workspace auto-registration. Every collaborative object now homes in the repo writ was opened on; there is no configured elsewhere and no routing branch. Qualified references (`<repo-id>#<object-id>`) still parse via `writ.ParseReference`, but resolving one against a repository other than the one writ is standing in is a job for a higher layer now.
- The `repo` collaborative object type (WRIT-182): `writ.RepoEntry`, `writ.FoldRepo`, `writ.RepoRules`, `state.ResolveReference`, `state.ResolvedReference`, `state.RepoEntry`, `state.FoldRepo`, `state.RepoRules`, and `projection.DB.Repo`/`DB.Repos` are gone, along with the `repos`/`repo_remotes` projection tables (`schemaVersion` bumped 13 → 14, so an existing `.writ` cache rebuilds automatically). The repository registry it modeled — mapping a repo-id to a slug, remote URLs, and workspace membership — was resolution machinery that WRIT-180 already moved out of writ's scope; `<repo-id>#<object-id>` stays representable as an opaque reference via `writ.ParseReference`.
