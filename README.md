# eldar-os

Ben's NixOS workstation, and the future home of Eldar. One flake describes the
whole machine.

- `flake.nix` / `flake.lock` — inputs, pinned.
- `hosts/eldar-os/` — the machine: disk layout (disko), hardware, configuration.
- `scripts/`
  - `host-setup.sh` — one-time root setup of the Ubuntu host that runs the VM
  - `vm` — run eldar-os as a VM straight off its physical disk (or, with
    `--dry-run`, off a disk image)
  - `fetch-iso` — the pinned installer ISO
  - `install.sh` — runs inside the installer: disko + nixos-install
  - `verify` — checks a booted system
  - `e2e` — the whole documented install, unattended, against a disk image
  - `deploy` — push this repo to the VM and switch
- `docs/install.md` — how it was installed, step by step, reproducibly.
- `.github/workflows/ci.yml` — builds the system, and runs `scripts/e2e`
  on every push and nightly.

Status: bootstrap. Installed and running as a VM straight off its physical
disk; the machine will later boot it directly.
