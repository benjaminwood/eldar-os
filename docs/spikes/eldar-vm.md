# Spike: Eldar in its own microVM

Status: done; adopted as `docs/decisions/0008-eldar-microvm.md`. Kept as the
record of how the design was tested.

## What was built

`modules/eldar-vm.nix`, a declarative microVM (microvm.nix, cloud-hypervisor)
defined in the same flake, built and applied with the host; sizing in
`hosts/eldar-os/eldar-vm.nix`.

- **Network:** routed tap link, host `10.42.0.1`, Eldar `10.42.0.2`, NAT out.
  Host nftables: internet and the supervisor's port (9100) only; host ports,
  LAN, tailnet (`100.64/10`), link-local metadata and IPv6 rejected.
- **Shares:** host `/nix/store` (read-only, with a RAM-backed writable layer
  for `nix shell`) and `/srv/eldar/workspace` (read/write, virtiofs, no ACLs).
- **Disk:** one sparse 250 GB home image (rootless Docker's data lives there).
- **Users:** `eldar` (uid 1500, group `eldar-ws` gid 1500, no sudo, rootless
  Docker, lingering) and `ben` (uid 1000, wheel, sudo needs a password). Host
  `ben` is in `eldar-ws`.
- **Sizing:** while eldar-os is itself a VM, 6 vCPU / 6 GB. At cutover:
  24 vCPU / 56 GB, with balloon free-page reporting.

## Results (on the real eldar-os VM, two levels of nesting)

| Check | Result |
|---|---|
| Boots with the host, autostarts, no failed units | yes |
| `eldar` unprivileged, owns its home, `umask 002` | yes |
| `nix shell nixpkgs#…` as eldar | yes |
| Rootless Docker | yes, including container HTTPS (5/5) |
| Internet; supervisor port | reachable |
| Host sshd, LAN, tailnet, metadata | blocked, from the VM and from containers |
| Workspace shared both ways, both sides can edit | yes (dry-run spike) |
| Balloon device present | yes |

## Lessons

- Fixed gid for the shared group on both sides, from the first install:
  NixOS doesn't change an existing group's gid.
- The workspace mount creates `/home/eldar` as root; tmpfiles fixes ownership.
- No firewall inside the VM: the host enforces policy at the tap. NixOS's
  guest firewall broke Docker.
- networkd's wait-online off on the host (it only manages `vm-*`).
- Rootful Docker's bridge failed for TCP under three levels of nesting;
  rootless Docker at two levels works. Recheck on bare metal.

## The supervisor in this design (not built yet)

- Runs on the host as its own system user; holds the 1Password tokens, AWS
  sign-in and Mac key; approvals in eldar-proxy as today.
- **Credentialed commands run inside the VM as `ben`**, reached over SSH (as
  `docker exec` is today), so they can use Eldar's files, Docker and local
  services, and Eldar can't read their credentials.
- **Every use needs a grant:** a one-off approval is a grant for one use;
  windows (session × scope × duration) as today.
- **A forwarder for Claude's own API credentials** (and later other HTTP
  APIs, where it can enforce per-request method/path policy within a grant).
- Per-session grants rely on the session ID callers report, as today; with
  per-session VMs it could come from the network instead.

## Package inventory (today's Docker setup → here)

**Ben, on the host** (`hosts/eldar-os/ben.nix`): tmux, fzf, gh, helix,
ripgrep, fd, jq, bat, htop, tree, xxd are in. From the Brewfile, worth adding:
kubectl, helm, kubectx, autossh, mise, libpq (psql).

**Eldar, in the VM** (from `home.nix`, the Dockerfile and its base image):
- general: git, ripgrep, fd, jq, bat, htop, tree, vim, helix, gum, direnv
- runtimes: node 22 (+ typescript, eslint), bun, go, python3 (+ numpy,
  scipy, pillow, rectpack)
- cloud/dev CLIs: gh, awscli2, kubectl, heroku, wrangler, duckdb
- custom derivations to carry over: hb, rwx, codex, composio, cf, herdr
- agent: Claude Code
- projects: potrace, openscad, imagemagick (3D-printing pipeline)
- needs a decision: a private CLI (fetched with a GitHub token at build time
  today), supercronic (or move jobs to systemd timers)

**Drop:** s6-overlay, acl, the Determinate Nix installer, `persist`/
`restow`, `set-permissions`, `clone-user-shell`, `env-export`, the 1Password
CLI inside Eldar (credentials go through the supervisor).
