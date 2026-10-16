# Installing eldar-os

eldar-os lives on the Samsung 970 EVO 500GB (serial `S5H7NC0MB49704F`). It is
built and run as a VM on the old Ubuntu host (on the 1TB disk), then later
booted directly on the same machine. Every step below is a script in this
repo or declarative config; nothing is done by hand except where noted.

Never use `/dev/nvmeXn1` names for these disks: they have swapped between boots
before (the Ubuntu host's LUKS mapping is called `nvme0n1p3_crypt` but lives on
`nvme1n1`). Everything here goes by serial.

## Pinned inputs

| What | Pin |
|---|---|
| nixpkgs | `nixos-26.05` @ `26f4107e47e3` (flake.lock) |
| disko | `725ea35e410a` (flake.lock) |
| Installer ISO | `nixos-minimal-26.05.11985.26f4107e47e3`, URL + sha256 in `scripts/fetch-iso` |
| CI actions | by commit SHA in `.github/workflows/ci.yml` |
| Lock generated with | `nixos/nix@sha256:ff480dbb23511aa1c10710aa068ceea5b93fed9bfce5f5379f3a6b07282cbc7b` (host has no Nix) |

The ISO is built from the same nixpkgs commit as the lock, so the install
mostly reuses what's already on the ISO. Bump them together.

## Reproduced in CI

`scripts/e2e` runs steps 2–6 below unattended against the dry-run disk image
(see next section) and then `scripts/verify`. CI (`.github/workflows/ci.yml`)
runs it on every push and PR and nightly, on a stock Ubuntu runner after
`scripts/host-setup.sh --dry-run`. If CI is green, these docs work.

## Dry run

Every step below works against a throwaway disk instead of the real one: put
`--dry-run` right after `scripts/vm` (and `DRY_RUN=1` before `scripts/deploy`).
The dry-run VM gets a sparse image exactly the size of the 970 EVO
(`~/.local/share/eldar-os-vm/dry-run/disk.raw`), its own LUKS key disk,
firmware vars, systemd unit (`eldar-os-vm-dry`), SSH port (2223) and VNC display
(:1). The physical disk is never attached. To the installer it looks identical
(same virtio serial, same size), so `install.sh` runs unchanged.

```bash
scripts/e2e                       # the whole thing, unattended
scripts/vm --dry-run reset        # discard the image and start over
```

## 1. Host setup (once, as root)

```bash
sudo bash scripts/host-setup.sh            # with the real disk
sudo bash scripts/host-setup.sh --dry-run  # without it (CI)
```

Verifies the target disk by serial and size, then installs QEMU, OVMF, 7zip
and socat, adds a udev rule giving the user access to that one disk (by
serial), and grants the user KVM. Writes nothing to any disk.

## 2. Installer ISO

```bash
scripts/fetch-iso
```

## 3. LUKS key disk

```bash
scripts/vm keygen
```

Creates `~/.local/share/eldar-os-vm/luks-key.img`: a 1 MiB image whose first 64
bytes are a random hex key (the rest is zeros). The VM sees it read-only as
`/dev/disk/by-id/virtio-lukskey`; disko formats LUKS with it and the initrd
unlocks with it.

**Back it up** — without it or Ben's passphrase the disk is unreadable. The key
is plain text, so it fits in a password manager:

```bash
head -c 64 ~/.local/share/eldar-os-vm/luks-key.img; echo          # back up
truncate -s 1M luks-key.img                                        # restore
printf %s '<the 64 hex chars>' | dd of=luks-key.img conv=notrunc
```

It also works as a passphrase typed at the bare-metal prompt.

## 4. Boot the installer and get SSH

```bash
ssh-keygen -t ed25519 -N "" -f ~/.ssh/eldar-os-vm   # once: the host-side key for the VM
scripts/vm install
scripts/vm authorize
```

`install` doesn't go through the ISO's GRUB menu (graphical, awkward to drive
headless). It extracts the kernel, initrd and the first menu entry's command
line from the ISO and has QEMU boot them directly, still through the UEFI
firmware, with `console=ttyS0` added. The installer auto-logs in as `nixos` on
that serial console; `authorize` types the public key into
`~/.ssh/authorized_keys` there and waits until `VM_USER=nixos scripts/vm ssh`
works.

## 5. Install

```bash
git ls-files | tar -cf - -T - | VM_USER=nixos scripts/vm ssh 'mkdir -p eldar-os && tar -xf - -C eldar-os'
VM_USER=nixos scripts/vm ssh -t 'sudo bash eldar-os/scripts/install.sh'
```

`install.sh` checks it's in the installer VM with the right disk attached, runs
disko (destroy → format → mount) and `nixos-install` from the flake, and leaves
a copy of the config at `/etc/eldar-os`.

## 6. Boot and verify

```bash
scripts/vm stop           # the installer (a clean shutdown, so writes are flushed)
scripts/vm start
scripts/verify
```

`verify` checks the hostname, that the system is running with no failed units,
the btrfs subvolumes on LUKS, compression, the fallback bootloader, and that
LUKS was unlocked by the key disk.

## 7. Ben's LUKS passphrase (Ben, interactively)

The disk only has the VM key until this runs. Needed before booting on bare metal.

```bash
scripts/vm ssh -t 'sudo cryptsetup luksAddKey /dev/disk/by-partlabel/disk-main-luks --key-file /dev/disk/by-id/virtio-lukskey --keyfile-size 64'
```

To see the bare-metal prompt without leaving the VM: `NO_KEY=1 scripts/vm start`
and look at VNC (`127.0.0.1:5900`).

## 8. Enroll: the machine applies its own config from here on

```bash
scripts/host-tools        # pinned sops + age on the admin host
scripts/enroll
```

The machine generates read-only deploy keys for the staging and secrets repos
(their public halves are registered via `gh`), and its age recipient (from its
SSH host key) must be in the secrets repo's `.sops.yaml`; `enroll` says so if
it isn't. It then clones the secrets repo on the machine and installs the
apply service with one last direct switch. See decisions 0004 and 0005.

The admin host needs an age key at `~/.config/sops/age/keys.txt`
(`age-keygen -o`, backed up in 1Password) and a commit-signing key whose
public half is in `trust/allowed_signers`.

## Changing the system

```bash
scripts/try               # activate the working tree now, uncommitted (~7 s)
git commit                # signed
scripts/apply             # push; the machine verifies, applies, health-checks (~11 s)
```

## Booting on bare metal

The bootloader is at the firmware's fallback path (`\EFI\BOOT\BOOTX64.EFI`) on
the 500GB disk's own ESP; no NVRAM entry is written. Pick the 970 EVO 500GB
from the firmware boot menu. There's no key disk there, so after 5 s the initrd
asks for Ben's passphrase. The Ubuntu disk and its ESP are untouched.
