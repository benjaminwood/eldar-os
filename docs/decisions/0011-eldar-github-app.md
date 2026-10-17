# 0011. Eldar works on GitHub as its own App; only Ben merges

Accepted, 2026-10-17

## Context

Eldar changes its environment by proposing changes to the staging repo
(0005): it pushes a branch and opens a PR, Ben merges, the machine applies.
The apply accepts commits signed by GitHub's web-flow key, but GitHub signs
everything made through it, including merges and file edits made with an API
token. So whatever GitHub identity Eldar has must not be able to change
`main`. A token from Ben's own account can't be held back that way: it acts
as Ben, the repo admin.

## Decision

- Eldar's GitHub identity is a GitHub App owned by Ben, installed on the
  staging repo only, with contents and pull requests read/write and read-only
  CI status. No workflows permission, so it can't change what CI runs.
- A ruleset on `main` blocks updates, deletion and force-pushes for everyone
  but repo admins (Ben, and the admin session signing as him). Rebase-merge is
  off; merges and squashes are signed by web-flow.
- The supervisor holds the App's private key and gives agents in Eldar's VM
  tokens for that one repo (an hour each, audited when made) without asking
  Ben: the App can only propose. In the VM, git gets them through a credential
  helper (`supervisor git-credential`), and gh through
  `GH_TOKEN=$(supervisor github-token)`.

## Consequences

Approving a change is merging its PR on GitHub. Eldar can't merge its own
PRs or push to `main`, and the apply refuses unsigned commits on `main`
anyway (0005). Anything in the VM can get a token, so the App's permissions
are the limit: keep them narrow. Eldar's PRs show as the App, not as Ben.
