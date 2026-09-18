# Ref-namespace host compatibility (WRIT-1)

The host-agnostic thesis (VISION.md §Risks #1) assumes major git hosts accept
pushes to a custom ref namespace (now `refs/writ/*`), let it be fetched by
exact refspec, and don't GC or hide it destructively. This spike tests that,
empirically, against as many of the six named targets as this run had
credentials or infrastructure for.

> **Historical note.** These probes were run before the project was renamed
> from Gloss to Writ, so the recorded commands and results below use the
> pre-rename namespace `refs/gloss/*` against the pre-transfer repo
> `mattwalters/gloss` (now `writtendev/writ`). They are deliberately kept
> as-run. The findings are namespace-agnostic — every restriction observed is
> scoped to a host's *own* reserved namespaces, never to arbitrary
> third-party ones — so they carry over to `refs/writ/*` unchanged. Future
> probes (WRIT-64) should use `refs/writ/*`.

## Method

Same probe against every target: create one commit object out-of-band (not on
any branch), then

1. `git push <remote> <sha>:refs/gloss/_spike-test/probe`
2. `git ls-remote <remote> 'refs/gloss/*'` — is the ref visible/enumerable
3. `git fetch <remote> '+refs/gloss/_spike-test/probe:refs/remotes/x'` — fetch
   by exact refspec (this is what a fetch/push refspec written into
   `.git/config` by `writ init` relies on)
4. Force a GC on the remote side (`git gc --prune=now --aggressive`) and
   re-check the ref/object survive
5. `git push <remote> :refs/gloss/_spike-test/probe` — delete, confirm honored
6. For comparison, attempt the same push under a namespace each host is known
   to special-case (`refs/pull/*` on GitHub, `refs/merge-requests/*` on
   GitLab-shaped servers) to confirm the probe methodology actually detects a
   real restriction when one exists

## Results

| Host | Push | Fetch by refspec | Survives forced GC | How verified |
|---|---|---|---|---|
| **GitHub** | ✅ accepted | ✅ | Not observed directly (see below) | **Empirical**, live against `mattwalters/gloss` |
| **Gitea** (stand-in for **Forgejo**) | ✅ accepted | ✅ | ✅ ref and object intact after `git gc --prune=now --aggressive` | **Empirical**, self-hosted `gitea/gitea:latest` in Docker |
| **Codeberg** | — | — | — | **Not verified directly.** Codeberg runs Forgejo, so the Gitea result above is weak, software-only evidence — no push was ever made to Codeberg itself, and Codeberg could layer its own server-side hooks/policy on top of stock Forgejo. |
| **bare git-over-SSH** (no forge software) | ✅ accepted | ✅ | ✅ | **Empirical**, `linuxserver/openssh-server` + `git init --bare` in Docker, real `ssh://` push/fetch |
| **GitLab** | — | — | — | **Not verified.** No GitLab.com account and no credentials were available to this run, and self-hosting GitLab CE was judged too heavy to bring up reliably in one session (multi-GB image, multi-minute first-boot reconfigure). Documentation-only: GitLab denies pushes into its own internal namespaces (`refs/merge-requests/*`, `refs/pipelines/*`, `refs/environments/*` — "deny updating a hidden ref"), the same mechanism GitHub uses for `refs/pull/*`. Nothing in GitLab's docs or issue tracker suggests it extends that denial to arbitrary third-party namespaces like `refs/gloss/*`, but that is a documentation read, not a push we made. |
| **Bitbucket** (Cloud) | — | — | — | **Not verified.** No Bitbucket account. Bitbucket Server/Data Center — which would have been self-hostable — was discontinued for new deployments, so there is no viable free self-host path either. Documentation-only: Bitbucket Cloud reserves `refs/pull-requests/*` for its own PR refs; no documented general restriction on arbitrary namespaces. |

### GitHub detail

Pushed, listed, and fetched `refs/gloss/_spike-test/probe` against this
project's own GitHub repo, then deleted it — all as expected. As a control,
the same commit pushed to `refs/pull/999999/probe` was rejected server-side
with `deny updating a hidden ref`, confirming the probe would have caught a
`refs/gloss/*`-specific restriction had one existed. GC survival specifically
was **not** verified empirically: GitHub's object/ref retention runs on its
own schedule server-side and isn't something this run could trigger or
observe within a single session. This is a real gap, not a formality — it's
the one place "documentation, not observation" is standing in for GitHub too.
GitHub's git backend does not document special retention handling for
non-standard `refs/*` namespaces, and unreachable-object grace-period
policies are unrelated to *reachable* refs like these, but that's inference
from how GitHub describes its git backend, not a push-then-wait-then-check.

### Gitea / Forgejo / Codeberg detail

Gitea and Forgejo are the same codebase lineage (Forgejo is a 2022 hard fork
of Gitea); Codeberg is a public Forgejo instance. Testing Gitea directly is
strong evidence for Forgejo's ref handling and weaker, software-only evidence
for Codeberg specifically — Codeberg could apply its own server-side hooks or
policy on top of stock Forgejo that this test wouldn't see. Push, fetch, and
an explicit `git gc --prune=now --aggressive` on the bare repo all left the
ref and its commit object intact and re-fetchable afterward. As a control,
pushing to `refs/pull/999999/head` (Gitea's own PR-ref convention) was
rejected with `hook declined to update refs/pull/999999/head`.

### Bare SSH detail

This is the trivial case and the empirical result confirms why: with no forge
software in front of `git-receive-pack`/`git-upload-pack`, there is nothing
to special-case any namespace. Push, refspec fetch, forced GC, and delete all
behaved exactly like local git. This result should generalize to any bare
repo reached over SSH regardless of which machine hosts it.

## Go/no-go

**Conditional go.** Every target this run could actually reach —
GitHub, Gitea (and by strong inference Forgejo), and bare SSH — accepts and
fetches an arbitrary `refs/gloss/*` namespace with no special handling, and
Gitea and bare SSH also confirmed GC-survival directly (GitHub's GC-survival
was not observed — see the GitHub detail section above). The one host-specific restriction pattern that
shows up everywhere it's checkable (GitHub's `refs/pull/*`, GitLab's
`refs/merge-requests/*`, Gitea's `refs/pull/*`, Bitbucket's
`refs/pull-requests/*`) is always scoped to the host's *own* generated refs,
never to arbitrary third-party namespaces — which is consistent with
`refs/gloss/*` being safe on the remaining unverified hosts too, but
"consistent with" is not "verified," and GitLab and Bitbucket are both named
explicitly in VISION.md's risk list.

Do not close this ticket's DoD as met — three of the six named targets
(**GitLab**, **Bitbucket**, **Codeberg**) have zero direct empirical
evidence, and GitHub's GC-survival specifically is also unverified. Recommend:
proceed with spec and engine work that depends on the ref-namespace approach
(it is not blocked by anything found here), but keep this ticket, or a
follow-up, open until someone with GitLab.com/Bitbucket/Codeberg credentials
— or willing to wait out GitHub's own GC cycle — can run the same six-step
probe against the remaining gaps.

## Fallback sketch (branch-namespace encoding)

If a host is later found to reject or hide `refs/writ/*`, the fallback is to
encode the same information inside an *allowed* namespace instead of a new
top-level one — since every host tested (and, per docs, GitLab and Bitbucket
too — Codeberg untested either way) treats `refs/heads/*` as unrestricted:

- Prefix-encode: `refs/heads/writ/<writer-id>/<object-type>`
  instead of `refs/writ/<writer-id>/<object-type>`. Same tree
  shape, same per-writer-namespace property: no other writer has cause to
  push into your namespace, so your push never races a colleague's — while
  your own push can still be rejected non-fast-forward against your own
  prior push after a restore or a rebase of shared history, and that
  rejection is never caused by a second writer (ARCHITECTURE.md) — not an
  access-control guarantee against another writer with push access
  writing into this namespace; see `spec/ref-layout.md` §Per-writer append
  chains. Costs: these op-commits now show up in branch-listing
  UI unless clients filter the `writ/` prefix out (a client concern, not a
  protocol one) — and, more seriously, `refs/heads/*` is exactly the
  namespace most hosts hang CI triggers (`on: push`) and branch-protection
  rules on, including wildcard patterns that could match `writ/**`. Landing
  in `refs/heads/*` to dodge one host's restriction on `refs/writ/*` risks
  walking into a *different* restriction, or firing unwanted CI on every
  op-commit, in that same repo. Check for branch-protection rules and CI
  triggers before adopting this fallback on a given host, not just ref
  acceptance.
- A repo could already have a real branch named `writ/...`, colliding with
  the encoded namespace; the writer-id/object-type segments make accidental
  collision unlikely but not impossible, worth a guard if this is ever
  implemented.
- This is a mechanical rename at the spec level (one prefix constant), not a
  redesign — canonical encoding, the op envelope, and fold are unaffected
  since none of them inspect the ref name beyond the writer-id/object-type
  segments.
- Only adopt this for the specific host(s) found to need it; don't pay the
  branch-namespace-pollution cost on hosts where the plain `refs/writ/*`
  namespace already works.

## Open follow-up

- Verify GitLab (needs a GitLab.com account or self-hosted GitLab CE with a
  longer time budget than this run had).
- Verify Bitbucket Cloud (needs an account — no viable self-host exists).
- Verify Codeberg directly (needs an account on codeberg.org — the Gitea
  result only stands in for the underlying software, not Codeberg's own
  policy/hooks).
- Revisit GitHub GC-survival after the ref has sat unfetched for longer than
  one session, or find GitHub documentation that speaks to it directly
  instead of inferring from general git-backend behavior.
- Feed these findings into WRIT-7 (Spec: ref layout & writer-id convention)
  once the remaining three hosts are checked.
