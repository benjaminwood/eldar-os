# 0005. The machine applies signed `main` itself

Accepted, 2026-10-16

## Context

Changes should reach the machine through the repo, not through someone with
root running commands, so the machine always matches reviewed history. Agents
will propose changes, but an agent mustn't be able to get its own change
applied. And a bad change mustn't lock anyone out.

## Decision

A root service, `eldar-os-apply` (`modules/apply/`), runs on a 2-minute timer
and on demand (`scripts/apply`, allowed to `ben` via polkit). It:

1. fetches staging `main` and the secrets repo with read-only deploy keys;
2. **refuses it unless every commit `main` gained since the last apply is
   signed** by an allowed key, along `main`'s first-parent chain: the admin
   session's SSH key (direct pushes) or GitHub's web-flow key (PRs merged on
   github.com, i.e. approved there). A PR's own commits are covered by the
   signed merge; an unsigned commit pushed to `main` stops the apply, even
   under a signed one;
3. builds, activates with `test`, waits, and runs health checks (sshd, GitHub
   reachable, no newly failed units, secrets decrypt);
4. only then makes it the boot default. A failed check reactivates the previous
   system; a transient timer does the same if the apply dies mid-way.

## Consequences

Approving is merging. An agent can push branches and open PRs, but without an
allowed signing key its commits never apply on their own. The admin loop is
quick: `scripts/check` builds in the VM before pushing, `scripts/apply` pushes
and streams the result. A change that breaks only after a reboot (kernel,
initrd) isn't caught by health checks; the boot menu still has the previous
generations.
