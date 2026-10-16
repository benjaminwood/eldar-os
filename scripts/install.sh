#!/usr/bin/env bash
# Runs INSIDE the NixOS installer VM, as root, from a copy of this repo.
# WIPES the target disk, formats it per hosts/eldar-os/disko.nix and installs
# eldar-os. Everything comes from flake.lock, so a re-run builds the same system.
#
#   sudo bash scripts/install.sh
set -euo pipefail

TARGET=/dev/disk/by-id/virtio-eldar-os-root
KEY=/dev/disk/by-id/virtio-lukskey
DISK_BYTES=500107862016
REPO=$(cd "$(dirname "$0")/.." && pwd)

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
[ -b "$TARGET" ] || { echo "no $TARGET — not in the eldar-os installer VM?" >&2; exit 1; }
[ -b "$KEY" ] || { echo "no $KEY — key disk not attached" >&2; exit 1; }
[ "$(blockdev --getsize64 "$TARGET")" = "$DISK_BYTES" ] || { echo "target size mismatch" >&2; exit 1; }

export NIX_CONFIG="experimental-features = nix-command flakes"
cd "$REPO"

nix run .#disko -- --mode destroy,format,mount --yes-wipe-all-disks --flake .#eldar-os

nixos-install --flake .#eldar-os --no-root-passwd --no-channel-copy

# Leave a copy of the config on the installed system (scripts/deploy keeps it current).
mkdir -p /mnt/etc/eldar-os
cp -a "$REPO"/. /mnt/etc/eldar-os/

sync
echo "installed. shut the installer down (scripts/vm stop), then: scripts/vm start"
