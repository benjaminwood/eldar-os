# The machine's previous OS, kept reachable: Ubuntu on the other NVMe drive
# (Samsung 970 EVO 1TB). Machine-specific: a fork drops this file.
#
# Its partition 3 is LUKS around an LVM group `vgubuntu` (root + swap). A key
# file added as an extra keyslot (the passphrase still works) unlocks it at
# boot, and Ubuntu's root is mounted read-only at /mnt/ubuntu so nothing here
# can damage it. Its swap is never used.
#
# Everything is `nofail` with short timeouts: when eldar-os runs as a VM the
# drive isn't attached (and must not be), and a missing or locked drive should
# never hold up boot.
{ config, ... }:
let
  luksDevice = "/dev/disk/by-id/nvme-Samsung_SSD_970_EVO_1TB_S5H9NG0N204821J-part3";
in
{
  # 64 bytes, no newline. Stage 2's crypttab reads it; sops-nix decrypts during
  # activation, before any crypttab unit starts.
  sops.secrets.ubuntu-disk-key = { };

  environment.etc.crypttab.text = ''
    ubuntu ${luksDevice} ${config.sops.secrets.ubuntu-disk-key.path} luks,discard,nofail,x-systemd.device-timeout=10s
  '';

  # LVM's udev rules activate vgubuntu once the mapping appears.
  fileSystems."/mnt/ubuntu" = {
    device = "/dev/vgubuntu/root";
    fsType = "ext4";
    options = [ "ro" "nofail" "x-systemd.device-timeout=10s" ];
  };
}
