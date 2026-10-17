# Spike: Eldar in its own microVM

Status: spike, for review. Branch `spike/eldar-vm`; not on `main`, not
applied to the real machine. Tried on the dry-run VM only.

## What was built

`modules/eldar-vm.nix`, a declarative microVM (microvm.nix, cloud-hypervisor)
defined in the same flake, so it is built and applied with the host:

- **Network:** a routed tap link, host `10.42.0.1`, Eldar `10.42.0.2`, NAT out.
  Host nftables lets Eldar reach the internet and one host port (9100, the
  future supervisor), and rejects everything else: host ports, LAN, tailnet
  (`100.64/10`), link-local metadata, IPv6.
- **Shares:** the host's `/nix/store` (read-only) and `/srv/eldar/workspace`
  (read/write, virtiofs, no ACLs).
- **Disks:** `/home` and `/var/lib/docker` on their own images in
  `/var/lib/microvms/eldar/`.
- **Inside:** user `eldar` (uid 1500, group `eldar-ws` gid 1500), rootful
  Docker, sshd. Host user `ben` is in `eldar-ws`.

Try it: `DRY_RUN=1 scripts/try` on this branch, then ssh in via the dry-run
VM (`ssh -o ProxyCommand="ssh -p 2223 ben@127.0.0.1 -W %h:%p" eldar@10.42.0.2`
with `~/.ssh/eldar-os-vm`).

## Results

| Check | Result |
|---|---|
| VM boots from the host's config, autostarts | yes, ~20 s (3 levels of nesting here; expect much less on hardware) |
| Memory | ~410 MB used of 4 GB |
| Internet from Eldar | yes |
| Supervisor port on the host | reachable |
| Host sshd, LAN, tailnet, metadata, private ranges | all blocked |
| Workspace shared both ways, each side can edit the other's files | yes (after the fixes below) |
| Docker inside the VM | runs (hello-world 3.7 s) |
| Containers' network over Docker's bridge | **TCP broken** (see open issue); `--network host` works |

## Fixes found along the way

- **Fixed gid for the shared group** on both sides (1500). NixOS doesn't
  change an existing group's gid, so this must be right from the first
  install (on the spike VM it was changed by hand).
- **`umask 002` inside the VM**, so files Eldar creates are group-writable.
- **No firewall inside the VM.** Policy is enforced by the host at the tap,
  where Eldar can't change it; NixOS's guest firewall added nothing and its
  strict reverse-path filter broke Docker.
- **networkd's wait-online off on the host**: networkd only manages `vm-*`;
  NetworkManager decides when the host is online.
- Turning on networkd for the tap also switched the host to systemd-resolved.

## Open issue: Docker bridge TCP inside the VM

UDP over Docker's bridge works (DNS), TCP doesn't, even to the VM itself
(`172.17.0.1`); the VM's own traffic and `--network host` containers are
fine. Nothing reaches the host, so it isn't the isolation rules. Suspect a
checksum-offload quirk on Docker's veth/bridge under three levels of
nesting. Next: try on the real eldar-os VM (two levels), then bare metal;
if it persists, `ethtool -K docker0 tx off` / veth offloads, or Docker's
nftables backend.

## Decisions for Ben

1. **Root inside the VM?** The spike gives `eldar` passwordless sudo there.
   The VM is the boundary, so it doesn't reach the host; it makes Eldar much
   less restricted day to day (install anything, run Docker freely).
2. **One Eldar VM, or also per-session VMs** later (see the earlier
   discussion)? This module is written for one.
3. **Where Ben's own Claude sessions run.** Today they're in the Eldar
   container as `eldar`. Options: in the Eldar VM (simple, same as today), or
   as `ben` on the host (more access, less isolation).
4. **Nix inside the VM.** It uses the host's store read-only; letting Eldar
   install tools ad hoc needs a writable store overlay and its own
   nix-daemon in the VM.
5. **Sizing:** 4 vCPU / 4 GB / 20 GB home / 40 GB Docker.
6. **Cost:** the VM's closure is built with the host's, so CI builds it too.

## Package inventory (today's Docker setup → here)

**Ben, on the host** (`hosts/eldar-os/ben.nix`): tmux, fzf, gh, helix,
ripgrep, fd, jq, bat, htop, tree, xxd are in. From the Brewfile, worth
adding: kubectl, helm, kubectx, autossh, mise, libpq (psql).

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
