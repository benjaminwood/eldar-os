#!/usr/bin/env bash
# One-time setup of the (Ubuntu) host that runs eldar-os as a VM. Run as root.
#
#   sudo bash scripts/host-setup.sh            real disk: packages, KVM, and
#                                              access to the target disk
#   sudo bash scripts/host-setup.sh --dry-run  packages and KVM only (CI, or
#                                              a machine without the disk)
#
# Writes to no disk. USER_NAME defaults to the user who ran sudo.
set -euo pipefail

SERIAL=S5H7NC0MB49704F   # Samsung 970 EVO 500GB — the eldar-os disk
DISK=/dev/disk/by-id/nvme-Samsung_SSD_970_EVO_500GB_${SERIAL}
DISK_BYTES=500107862016
USER_NAME=${USER_NAME:-${SUDO_USER:-}}
DRY_RUN=
[ "${1:-}" = --dry-run ] && DRY_RUN=1

[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
[ -n "$USER_NAME" ] || { echo "set USER_NAME (or run via sudo)" >&2; exit 1; }

if [ -z "$DRY_RUN" ]; then
  # Refuse to continue unless the by-id path resolves to a disk with that
  # serial and size that nothing mounts.
  real=$(readlink -f "$DISK")
  [ "$(udevadm info -q property "$real" | sed -n 's/^ID_SERIAL_SHORT=//p')" = "$SERIAL" ] || { echo "serial mismatch"; exit 1; }
  [ "$(lsblk -bdno SIZE "$real")" = "$DISK_BYTES" ] || { echo "size mismatch"; exit 1; }
  if lsblk -no MOUNTPOINTS "$real" | grep -q .; then echo "target disk is mounted"; exit 1; fi
  echo "target: $DISK -> $real (ok)"
fi

# 1. QEMU, UEFI firmware, and the tools scripts/vm uses
apt-get update -qq
apt-get install -y qemu-system-x86 qemu-utils ovmf 7zip socat acl

# 2. Give the user raw access to THIS disk only (matched by serial, so it
#    survives nvme renumbering)
if [ -z "$DRY_RUN" ]; then
  cat > /etc/udev/rules.d/99-nixos-target-disk.rules <<EOF
# eldar-os disk: owned by ${USER_NAME} so QEMU can use it without root. Only this serial.
SUBSYSTEM=="block", ENV{ID_SERIAL_SHORT}=="${SERIAL}", OWNER="${USER_NAME}", GROUP="${USER_NAME}", MODE="0600"
EOF
  udevadm control --reload
  udevadm trigger --subsystem-match=block --action=change
  udevadm settle
  ls -lL "$DISK"
fi

# 3. KVM: group membership for future logins, ACL for sessions already running
#    (scripts/vm also falls back to `sg kvm` if the ACL gets reset)
usermod -aG kvm "$USER_NAME"
setfacl -m "u:${USER_NAME}:rw" /dev/kvm

echo "done"
