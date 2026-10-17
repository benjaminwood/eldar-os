#!/usr/bin/env bash
# Runs INSIDE the NixOS installer VM, as root, from a copy of this repo.
# Formats the target disk per hosts/eldar-os/disko.nix and installs eldar-os.
# Everything comes from flake.lock, so a re-run builds the same system.
#
#   sudo bash scripts/install.sh               WIPES the whole disk, new LUKS
#   sudo bash scripts/install.sh --keep-luks   reinstall the same machine
#       inside its existing LUKS container: the header and every key slot
#       (e.g. a passphrase someone typed) survive, and so does the machine's
#       identity (SSH host keys, hence its secrets recipient; deploy keys;
#       secrets checkout). The filesystems inside are recreated.
set -euo pipefail

TARGET=/dev/disk/by-id/virtio-eldar-os-root
KEY=/dev/disk/by-id/virtio-lukskey
LUKS=/dev/disk/by-partlabel/disk-main-luks
ESP=/dev/disk/by-partlabel/disk-main-ESP
DISK_BYTES=500107862016
REPO=$(cd "$(dirname "$0")/.." && pwd)

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
[ -b "$TARGET" ] || { echo "no $TARGET — not in the eldar-os installer VM?" >&2; exit 1; }
[ -b "$KEY" ] || { echo "no $KEY — key disk not attached" >&2; exit 1; }
[ "$(blockdev --getsize64 "$TARGET")" = "$DISK_BYTES" ] || { echo "target size mismatch" >&2; exit 1; }

export NIX_CONFIG="experimental-features = nix-command flakes"
cd "$REPO"

if [ "${1:-}" = --keep-luks ]; then
  cryptsetup isLuks "$LUKS" || { echo "$LUKS is not LUKS; use a full install" >&2; exit 1; }
  # Erase the filesystems (not the LUKS header); disko then only creates what
  # is missing: it skips luksFormat on a LUKS partition and mkfs on a device
  # that still has a filesystem.
  cryptsetup status cryptroot >/dev/null || cryptsetup open --key-file "$KEY" --keyfile-size 64 "$LUKS" cryptroot

  # Keep the machine's identity, whatever the old filesystem was.
  KEEP=/root/keep-identity
  rm -rf "$KEEP" && mkdir -p "$KEEP/etc/ssh" "$KEEP/var/lib"
  mkdir -p /mnt-old && mount -o ro /dev/mapper/cryptroot /mnt-old
  old=/mnt-old; [ -d /mnt-old/@root ] && old=/mnt-old/@root   # btrfs layout
  cp -a "$old"/etc/ssh/ssh_host_* "$KEEP/etc/ssh/" 2>/dev/null || echo "no old host keys"
  [ -d "$old/var/lib/eldar-os" ] && cp -a "$old/var/lib/eldar-os" "$KEEP/var/lib/"
  umount /mnt-old
  echo "kept: $(cd "$KEEP" && find . -type f | wc -l) files"

  wipefs -a /dev/mapper/cryptroot
  wipefs -a "$ESP"
  nix run .#disko -- --mode format,mount --flake .#eldar-os
else
  nix run .#disko -- --mode destroy,format,mount --yes-wipe-all-disks --flake .#eldar-os
fi

if [ -n "${KEEP:-}" ]; then
  # Before nixos-install, so activation finds the host key and decrypts secrets.
  cp -a "$KEEP"/. /mnt/
fi

nixos-install --flake .#eldar-os --no-root-passwd --no-channel-copy

# Leave a copy of the config on the installed system (scripts/deploy keeps it current).
mkdir -p /mnt/etc/eldar-os
cp -a "$REPO"/. /mnt/etc/eldar-os/

sync
echo "installed. shut the installer down (scripts/vm stop), then: scripts/vm start"
