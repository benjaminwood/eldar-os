# 0008. Eldar's environment is a microVM; the host is the control plane

Proposed, 2026-10-16

## Context

Ben does his AI work inside Eldar's environment: his own Claude sessions,
Eldar's long-running agents, their tools, Docker, the workspace. Something
outside that environment has to hold the keys to everything (the
supervisor's 1Password tokens, AWS sign-in, the SSH key to Ben's Mac), decide
what the AI may do, and apply the system's config. Agents in the environment
run untrusted code all day. And Ben may also use this machine as a desktop.

## Decision

- **The host is the control plane** (supervisor, secrets, approvals, the apply
  loop) **plus Ben's desktop** when he sits at it. It isn't where AI work
  happens. It has to be the outer layer: a hypervisor protects the host from
  the guest, not the other way round.
- **Eldar's environment is one microVM** (microvm.nix, defined in this flake,
  `modules/eldar-vm.nix`), with its own kernel and network. The host lets it
  reach the internet and the supervisor's port, nothing else; it shares one
  directory with the host, the workspace.
- **Inside, `eldar` is unprivileged** (no sudo, rootless Docker) and is the
  user everyone works as. **`ben` is privileged** (sudo with a password), and
  is the user the supervisor runs credentialed commands as, so Eldar can't
  read their credentials. Changes to the environment go through this flake;
  `nix shell` covers trying things (scratch, gone on restart).
- **Sized to the machine:** all CPU threads (scheduled, not reserved) and
  most of the memory as a ceiling, with a balloon handing freed memory back.

## Consequences

An agent that gets root inside the VM still can't reach the supervisor's
credentials, the host's network identity, or Ben's desktop. Controls inside
the VM (Claude's managed settings, the eldar/ben split) are real, because
Eldar isn't root there. Costs: virtiofs for the workspace (host-side edits
may not trigger file watchers inside), VM networking to keep right, and
group/user ids that must match on both sides. Per-session VMs, which would
give each session its own network identity for per-session grants, can
come later on the same design.
