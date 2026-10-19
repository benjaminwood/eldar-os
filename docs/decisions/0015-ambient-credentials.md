# 0015. Ambient credentials come from the supervisor, by directory and owner

Accepted, 2026-10-19

## Context

Most credentials an agent uses wait for Ben's approval, one command at a time
(0013). A few it needs so constantly that asking each time would make the
work impossible, and they are narrow enough that asking adds nothing: API keys
for the tools it calls, and GitHub access to the repos of the orgs it works in.

## Decision

Ambient credentials are the ones Ben has explicitly decided are always safe
for an agent to use at will. The supervisor hands them out without approval,
and only them. A private file next to the policy (`ambient.yaml`) names each
one, and the values come from the profiles' 1Password Environments like every
other credential:

- environment variables by directory: a rule covers a directory and
  everything under it, deeper rules add to and override shallower ones. In
  the VM, direnv asks for them (`use supervisor` in an `.envrc`);
- GitHub tokens by repo owner, for git's credential helper and the `gh`
  wrapper. GitHub access is ambient only for the owners listed, each with a
  fine-grained personal access token scoped to the repos and permissions that
  work needs. Every other owner falls back to Eldar's App (0011), which can
  only propose changes to the staging repo; broader access is an approved
  command with Ben's own token.

Fetches are audited (the same one at most hourly) and refused in lockdown.
The helpers that use them are packaged in the system, not in the workspace,
so nothing in the VM can change what runs with a token.

## Consequences

Adding a credential to `ambient.yaml` is a decision that any agent may use it
without asking; anything that should need approval stays out. Rotating one is
an edit in 1Password, and nothing secret is in git, encrypted or not. The
supervisor and 1Password become a dependency of a shell's environment (its
cache keeps that cheap). Directory scoping organises, it doesn't protect: the
VM names the directory it asks for, so anything in there can get any
directory's set, and a token's own scope is the real limit. An owner whose
repos include the staging repo must never be listed, or Eldar could change
its own environment without Ben.
