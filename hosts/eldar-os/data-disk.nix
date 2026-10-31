# The data disk: the machine's second NVMe drive (Samsung 970 EVO 1TB, serial
# S5H9NG0N204821J), for bulky data that can be rebuilt, not the OS. It's older
# than the system disk (it ran the previous Ubuntu install for years), so
# nothing here should be the only copy of anything. Machine-specific: a fork
# drops this file or points it at its own drive.
#
# One partition, LUKS2 around XFS, mounted at /data. The key is a sops secret
# that stage 2's crypttab reads (sops-nix decrypts during activation, before
# any crypttab unit starts); there's no passphrase, so the drive only unlocks
# on this machine. Formatted once by hand (docs/install.md, "The data disk").
#
# `nofail` with short timeouts: when eldar-os runs as a VM the drive isn't
# attached, and a missing or locked drive should never hold up boot. Anything
# that keeps its state here must require the mount itself, so it waits for the
# drive or doesn't start, instead of writing into an empty /data.
{ config, ... }:
let
  luksDevice = "/dev/disk/by-id/nvme-Samsung_SSD_970_EVO_1TB_S5H9NG0N204821J-part1";
in
{
  # 64 characters, no newline.
  sops.secrets.data-disk-key = { };

  environment.etc.crypttab.text = ''
    data ${luksDevice} ${config.sops.secrets.data-disk-key.path} luks,discard,nofail,x-systemd.device-timeout=10s
  '';

  fileSystems."/data" = {
    device = "/dev/mapper/data";
    fsType = "xfs";
    options = [ "noatime" "nofail" "x-systemd.device-timeout=10s" ];
  };
}
