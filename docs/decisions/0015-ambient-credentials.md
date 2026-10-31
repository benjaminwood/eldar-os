# 0015. Agents have their own credentials, always available and never privileged

Accepted, 2026-10-19

## Context

Most credentials an agent uses wait for Ben's approval, one command at a time
(0013), because they're Ben's: his accounts, acting as him. But some of what
an agent does needs credentials so constantly that asking every time would
make the work impossible, and for those, asking adds nothing. An AI API key
is the clearest case: a tool that calls a model needs the key on every run,
and the worst it can do is spend a bounded amount of money.

## Decision

**There are two kinds of credential: Ben's, and the agent's.** Ben's need his
approval (0013). The agent's are *ambient*: always available, handed out by
the supervisor without asking, the way a person keeps their own API keys in
their shell. A credential is the agent's because Ben decided it is, one by
one, in a private list (`ambient.yaml`); nothing becomes ambient by default.

**An ambient credential never grants privileged access.** The bar is that an
agent using it badly, or being steered into it, costs something bounded and
recoverable: AI API keys, keys for tools it calls, tokens scoped to the
repositories of the orgs it works in. Never keys that can administer
accounts, change permissions, touch production, reach Ben's identity, or
change the agent's own rules. GitHub is the sharpest edge: access is ambient
only per listed repo owner, with a token scoped to that work, and never for
an owner whose repos include this system's own, or the agent could change its
environment without Ben. Everything else stays an approved command.

The values live where all credentials live (1Password, through the
supervisor), so nothing secret is in git, and the same audit and lockdown
apply: every fetch is recorded, and lockdown refuses them all. In the VM,
environment variables come by directory (direnv asks the supervisor), and
git's credential helper and the `gh` wrapper get a token by repo owner; an
owner that isn't listed falls back to Eldar's App (0011), which can only
propose changes. Those helpers are part of the system, not the workspace, so
nothing in the VM can change what runs with a token.

## Consequences

Listing a credential as ambient is a real decision: any agent may use it, at
any time, without Ben seeing each use, so the list is short and each entry is
narrow by construction. The credential's own scope is the only limit that
matters; organising them by directory or owner is convenience, not
protection. Agents work without stalling on approvals for routine calls, and
Ben's attention goes to the commands that act as him. The supervisor becomes
a dependency of an agent's everyday shell, so it has to be fast and
available.
