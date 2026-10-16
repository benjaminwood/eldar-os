# Disk layout for the Samsung 970 EVO 500GB (serial S5H7NC0MB49704F).
#
# GPT: 1G ESP + LUKS2 → btrfs with subvolumes.
#
# `device` is only read when formatting, which happens from the installer VM,
# where scripts/vm attaches this disk as virtio with serial "eldar-os-root".
# Booting finds everything by partition label / UUID, so the bare-metal device
# name (/dev/nvme0n1 or /dev/nvme1n1 — they have swapped before) never matters.
#
# LUKS unlock:
#   - VM: scripts/vm attaches a read-only key disk with serial "lukskey"; the
#     first 64 bytes are the key. It's also what disko formats with.
#   - Bare metal: no key disk, so after keyFileTimeout the initrd asks for
#     Ben's passphrase (added to its own key slot, see docs/install.md).
{
  disko.devices.disk.main = {
    type = "disk";
    device = "/dev/disk/by-id/virtio-eldar-os-root";
    content = {
      type = "gpt";
      partitions = {
        ESP = {
          size = "1G";
          type = "EF00";
          content = {
            type = "filesystem";
            format = "vfat";
            mountpoint = "/boot";
            mountOptions = [ "umask=0077" ];
          };
        };
        luks = {
          size = "100%";
          content = {
            type = "luks";
            name = "cryptroot";
            settings = {
              allowDiscards = true;
              keyFile = "/dev/disk/by-id/virtio-lukskey";
              keyFileSize = 64;
              keyFileTimeout = 5;
            };
            content = {
              type = "btrfs";
              extraArgs = [ "-f" "-L" "eldar-os" ];
              subvolumes =
                let opts = [ "compress=zstd" "noatime" ];
                in {
                  "@root" = { mountpoint = "/"; mountOptions = opts; };
                  "@home" = { mountpoint = "/home"; mountOptions = opts; };
                  "@nix" = { mountpoint = "/nix"; mountOptions = opts; };
                  "@log" = { mountpoint = "/var/log"; mountOptions = opts; };
                  "@snapshots" = { mountpoint = "/.snapshots"; mountOptions = opts; };
                };
            };
          };
        };
      };
    };
  };
}
