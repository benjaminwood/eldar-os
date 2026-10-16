# eldar-os

Ben's NixOS workstation, and the future home of Eldar. One flake describes the
whole machine.

- `flake.nix` / `flake.lock` — inputs, pinned.
- `hosts/eldar-os/` — the machine: disk layout (disko), hardware, configuration,
  where it applies from.
- `modules/` — `apply/` (the machine applies signed `main` itself) and
  `secrets.nix` (sops-nix).
- `trust/` — public keys allowed to sign what gets applied.
- `scripts/`
  - `host-setup.sh` — one-time root setup of the Ubuntu host that runs the VM
  - `vm` — run eldar-os as a VM straight off its physical disk (or, with
    `--dry-run`, off a disk image)
  - `fetch-iso` — the pinned installer ISO
  - `install.sh` — runs inside the installer: disko + nixos-install
  - `verify` — checks a booted system
  - `e2e` — the whole documented install, unattended, against a disk image
  - `check` — build the working tree in the VM and diff it, before pushing
  - `apply` — push and have the machine apply now, streaming its log
  - `host-tools` — pinned sops and age for the admin host
  - `deploy` — bootstrap only: copy the tree to the VM and switch as root
  - `publish` — publish staging history to the public repo
- `docs/install.md` — how it was installed, step by step, reproducibly.
- `docs/publishing.md` — the private staging repo → public repo workflow.
- `docs/decisions/` — short records of the choices that shape the system.
- `.github/workflows/ci.yml` — builds the system, and runs `scripts/e2e`
  on every push and nightly.

This is a personal system published as a reference: fork it and adapt it,
don't run it as-is. Machine-specific values (disk serial, hardware, SSH key)
are marked where they appear.

Status: bootstrap. Installed and running as a VM straight off its physical
disk; the machine will later boot it directly.
