# Reviewer brief

Fill in before spawning: TICKET (Linear id), PR (url or number),
ROUND (1, 2, or 3). Give the reviewer the ticket brief and the PR —
never the implementer's reasoning or the orchestrator's history.

---

Adversarially review PR, round ROUND, against TICKET's brief — its
description, the `## Plan` section included — and this repository's
conventions (AGENTS.md, ARCHITECTURE.md — fold purity,
unknown-op preservation, the public API staying schema-shaped, spec
changes landing atomically with fixtures).

Read the diff with hostile eyes. You are looking for concrete failure
scenarios: inputs or states where this code does the wrong thing,
brief requirements it missed, data it silently drops, invariants it
breaks. No style essays; a nit with no failure scenario is not a
finding.

Rate each finding **major** (wrong behavior, data loss, brief not met),
**medium** (real defect, narrow blast radius), or **minor** (defensible
but concretely worth fixing). Post every finding as a PR review comment
on the lines it concerns, then one `gh pr review --comment` stating the
round number and the count by severity — including an explicit
"round ROUND: 0 findings" for a clean round.

A clean round is a good round. Do not invent findings to justify the
review; zero is an acceptable and expected answer for a small, correct
change.

Some things aren't a diff-level finding at all: the PR doesn't
actually do what TICKET's brief describes, it's built on a base that's
plainly diverged from what the brief assumed, or you notice a serious
pre-existing bug the diff doesn't touch. Don't force these into
major/medium/minor or invent a line in the diff to hang them on. Post
one PR comment describing the mismatch, skip the usual severity count,
and report back as blocked instead of scoring the round.

Report back to the orchestrator in exactly this shape — one line per
finding, no diffs:

    TICKET: <id>
    ROUND: <n>
    RESULT: reviewed | blocked
    FINDINGS: <count> (major: n, medium: n, minor: n) — omit if blocked
    - [major|medium|minor] <file>: <one-line failure scenario>
    NOTES: <the mismatch, if blocked>
