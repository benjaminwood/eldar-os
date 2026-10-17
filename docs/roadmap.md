# Roadmap

What's done and what's left on the way from the Docker setup to eldar-os.
Checked items are done; the order of sections is roughly the order of work.

## Done

- [x] NixOS on its own disk, installed from a VM: disko, LUKS (VM key + Ben's
      passphrase), XFS (decisions 0002, 0007); reproducible install, tested
      end to end in CI (`scripts/e2e`)
- [x] Private staging repo published to a public repo (0003)
- [x] Machine secrets with sops-nix from a private repo (0004)
- [x] The machine applies signed staging `main` itself, with health checks
      and rollback; fast loop: `scripts/try`, then commit + `scripts/apply`
      (0005)
- [x] Ben's dotfiles via chezmoi from his dotfiles repo (0006)
- [x] Eldar's environment as a microVM; the host is the control plane (0008)
- [x] Supervisor rewritten in Go: commands, policy, grants, approvals,
      sessions, modes, audit, 1Password profiles, admin API, TUI, web UI on
      the tailnet (0009)
- [x] herdr in Eldar's VM; supervisor herdr control and live terminals
- [x] Eldar's primary session (watchdog) with chat
- [x] Eldar's VM updated in place, never restarted for software (0010)

## 1. Eldar improves its own system (PR → approve → apply)

- [ ] Signed commits required on staging `main` (GitHub ruleset), so only
      Ben's merges and the admin's signed pushes land there
- [ ] Supervisor MCP server (`supervisor_exec` and friends) in Eldar's VM, ported
      from the old client and renamed to supervisor throughout
- [ ] A GitHub credential for Eldar, limited to opening PRs on the staging
      repo, held by the supervisor
- [ ] Eldar's first PR, merged by Ben on GitHub, applied by the machine

## 2. Eldar's identity and tools: the workspace

- [ ] Persona (`CLAUDE.md`), skills, projects, helper scripts
- [ ] Cron jobs (`jobs.json`) under supercronic, or systemd timers
- [ ] Remaining MCP servers (notify, system-cron, …)

## 3. The rest of the supervisor

- [ ] `relay` channel: other agents reach Ben through Eldar (`notify_ben`)
- [ ] Commands on Ben's Mac
- [ ] Arc on Ben's Mac
- [ ] Approval nudges in herdr
- [ ] AWS credentials (needs a fresh SSO sign-in for the supervisor)
- [ ] MCP OAuth (needs fresh sign-ins)
- [ ] A replacement for `suben` / the secrets-dump socket
- [ ] Forwarder for Claude's own API credentials (later: per-request policy)

## 4. Networking and services

- [ ] Tailscale for the host and Eldar's VM (own identities, ACLs)
- [ ] `*.test` dev domains (dev-dns)
- [ ] Chrome for Playwright (headful, viewable)
- [ ] Kubernetes lab VM (kube-vm)

## 5. Cutover

- [ ] First boot on the real hardware (Radeon, NICs, passphrase prompt)
- [ ] Desktop for Ben
- [ ] Remove passwordless sudo
- [ ] Raise Eldar's VM to full size (24 vCPU / 56 GB)
- [ ] Move today's Eldar state: Claude sessions and `.claude`, chat history,
      memory
- [ ] Rename `supervisor-next` → the final web UI name; retire the Docker stack

## Housekeeping

- [ ] Run `scripts/e2e` again (last run predates chat and in-place updates)
- [ ] Publish staging to the public repo
- [ ] Merge the dotfiles `eldar-os` branch
