# Docker on the host, rootful, for the household services that run here
# rather than in Eldar's VM (docs/decisions/0014-host-docker.md).
#
# No one is in the docker group: that's root by another name. The admin uses
# `sudo docker`, and containers are declared with virtualisation.oci-containers.
{ pkgs, ... }:
{
  virtualisation.docker = {
    enable = true;
    # The iptables backend sets the host's FORWARD policy to drop, which would
    # cut Eldar's VM off from the internet (its traffic is forwarded through
    # the host, modules/eldar-vm.nix). The nftables backend keeps its rules in
    # its own tables and leaves other forwarded traffic alone.
    daemon.settings.firewall-backend = "nftables";
  };
  systemd.services.docker.path = [ pkgs.nftables ]; # the backend runs `nft`
  virtualisation.oci-containers.backend = "docker";
}
