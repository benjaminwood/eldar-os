# The host on Ben's tailnet, as the tagged node `tag:eldar-os` (the tag comes
# from its auth key). The tailnet policy lets Ben's devices SSH in and lets the
# host reach only the Mac's SSH, for the supervisor's Mac commands; see
# docs/install.md. Eldar's VM can't use this connection: the host firewall
# rejects VM traffic to the tailnet range (modules/eldar-vm.nix), and a health
# check makes sure it stays that way.
#
# First start needs a single-use auth key for the tag in
# /var/lib/tailscale/authkey; it's deleted once the node is registered.
{ pkgs, ... }:
{
  services.tailscale = {
    enable = true;
    openFirewall = true;
    authKeyFile = "/var/lib/tailscale/authkey";
    extraUpFlags = [ "--hostname=eldar-os" ];
  };
  # Registered: the key has no further use.
  systemd.services.tailscaled-autoconnect = {
    unitConfig.ConditionPathExists = "/var/lib/tailscale/authkey";
    postStart = "${pkgs.coreutils}/bin/rm -f /var/lib/tailscale/authkey";
  };

  # The services facing the outside world have their own tailnet nodes
  # (tsnet); they never need the host's, so a compromise of either can't use it.
  systemd.services.supervisor-web.serviceConfig.IPAddressDeny = [ "100.64.0.0/10" "fd7a:115c:a1e0::/48" ];
  systemd.services.eldar-os-apply-hook.serviceConfig.IPAddressDeny = [ "100.64.0.0/10" "fd7a:115c:a1e0::/48" ];

  # Eldar's VM must never reach the tailnet through the host.
  eldar-os.apply.healthChecks = [
    "${pkgs.nftables}/bin/nft list chain inet eldar-vm forward | grep -q '100.64.0.0/10'"
  ];
}
