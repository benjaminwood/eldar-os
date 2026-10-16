# 0002. Own disk, built from a VM: disko, LUKS, btrfs

Accepted, 2026-10-16

## Context

eldar-os replaces the machine's current OS, but has to be built while that OS
keeps running. The machine has a second disk to give it.

## Decision

- Install onto the second disk **from a QEMU VM** on the current OS, with the
  physical disk passed through (`scripts/vm`). The same install later boots on
  the bare machine. The disk is only ever addressed by serial, because NVMe
  device names have swapped between boots here.
- Describe the layout with **disko**: GPT, 1 GB ESP, **LUKS2**, then **btrfs**
  with subvolumes for `/`, `/home`, `/nix`, `/var/log`, `/.snapshots`.
- **Two LUKS keys**: a random keyfile that the VM gets as a read-only virtual
  disk (so the VM boots unattended), and Ben's passphrase for bare metal.
- **systemd-boot at the firmware's fallback path**, no NVRAM entry, so
  nothing the VM does touches the motherboard's boot settings.

## Consequences

The install is a script, so it's reproducible and runs in CI against a disk
image (`scripts/e2e`). btrfs gives snapshots, which matter once the machine
applies changes on its own. The VM keyfile lives on the old OS's (encrypted)
disk and must be backed up; losing both it and the passphrase loses the disk.
