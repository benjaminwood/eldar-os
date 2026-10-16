# 0003. Private staging repo, published to a public repo

Accepted, 2026-10-16

## Context

The repo is meant to be public, as a reference to fork. But work in progress,
agent PRs and mistakes shouldn't be public the moment they're pushed, and once
something is public it can't be taken back.

## Decision

All work lands in a private **staging** repo, which the machine applies from.
Ben publishes to the **public** repo when he chooses, with `scripts/publish`:
a fast-forward of the same history, after a gitleaks scan of every commit not
yet public. Anything that must never be public (secrets, personal data, the
agent's memory) lives outside both, in its own private repo or outside git.

## Consequences

Unpublished history can still be rewritten, so a mistake is fixed by removing
it from history, not by a follow-up commit. Publishing the same history keeps
the two repos simple to sync. Public CI (free, larger runners) runs the full
install test after each publish; staging runs it on demand. See
`docs/publishing.md`.
