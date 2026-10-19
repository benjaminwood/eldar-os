# Home Assistant for the house, in Docker on the host (docs/decisions/0014-host-docker.md).
# Machine-specific: a fork drops this file, or changes the LAN interface.
#
# Host networking, as Home Assistant expects: its integrations find devices by
# mDNS and broadcast, and its HomeKit bridge advertises itself the same way.
# All state (config, database, integration credentials) lives in
# /var/lib/homeassistant, never in this repo; docs/install.md has the command
# that moved it here from the previous OS.
#
# The image is pinned. Upgrading is a commit that changes the tag: read the
# release notes' breaking changes first, and keep a copy of /var/lib/homeassistant.
{ config, ... }:
let
  lan = "enp5s0";
in
{
  virtualisation.oci-containers.containers.homeassistant = {
    image = "homeassistant/home-assistant:2024.1.4";
    volumes = [ "/var/lib/homeassistant:/config" ];
    environment.TZ = config.time.timeZone;
    extraOptions = [ "--network=host" ];
  };

  # Created empty if missing, so a fresh machine starts a fresh setup instead
  # of Docker making the bind source on its own.
  systemd.tmpfiles.rules = [ "d /var/lib/homeassistant 0700 root root -" ];

  networking.firewall.interfaces.${lan} = {
    allowedTCPPorts = [
      8123 # web UI and API
      21064 # HomeKit bridge
    ];
    allowedUDPPorts = [ 5353 ]; # mDNS: HomeKit, ESPHome and device discovery
  };
  networking.firewall.interfaces.tailscale0.allowedTCPPorts = [ 8123 ];

  eldar-os.apply.healthChecks = [ "systemctl is-active --quiet docker-homeassistant" ];
}
