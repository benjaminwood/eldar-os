# 0009. The supervisor: a Go rewrite that runs commands in Eldar's VM

Accepted, 2026-10-16

## Context

The credential proxy (TypeScript, in a Docker container) holds every
credential the agent can use, decides what runs (policy, grants, Ben's
approvals) and runs approved commands in the agent's container with
`docker exec`. Moving to eldar-os changes how it runs commands (into a VM, not
a container), and it's the most trusted process on the machine. Its admin UIs
(TUI, web) were already Go.

## Decision

- **Rewrite it in Go** as `supervisor serve`, in one module and one binary
  with the TUI and web UI (`supervisor/`), sharing the admin API's types so
  server and clients can't drift. Same agent HTTP API and admin API, so
  existing clients work unchanged. Parity tests pin the security rules
  (policy, grants, session pinning, the decision order).
- **Run commands in Eldar's VM over SSH, as the privileged user there**
  (`ben`), with the command and credentials in a script on stdin, so the
  unprivileged agent can't read them and they never appear in a process list.
  The VM's SSH host key is made on the host and pinned; the supervisor's key
  is authorized there by the host, not the repo.
- **Its own system user, hardened unit,** and an admin socket only the
  `supervisor-admin` group can open. The agent's port listens on the VM link
  only. Policy and 1Password configuration come from the private secrets repo.
- **Ported in stages.** First: commands, policy, grants, approvals, sessions,
  modes, audit, 1Password profiles, the admin API and the web UI. Later: Mac
  commands, AWS, MCP OAuth, Arc, chat, herdr control and terminals.

## Consequences

Fewer dependencies on the component that holds everything, and one toolchain.
Until the later stages land, those features still run on the old proxy.
`op environment` is beta-only, so the 1Password CLI is pinned to a beta build
(`packages/op-beta.nix`).
