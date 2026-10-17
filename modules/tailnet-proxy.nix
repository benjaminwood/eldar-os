# Web pages from Eldar's VM on Ben's tailnet (supervisor/cmd/tailnet-proxy):
# each proxy joins the tailnet as its own node and forwards HTTPS (WebSockets
# included) to a URL only the host can reach. Only untagged devices of the
# allowed tailnet user get in; the VM itself stays off the tailnet.
#
# First start needs a single-use auth key for the node's tag in
# /var/lib/tailnet-proxy-<name>/authkey (owned by tailnet-proxy); it's deleted
# once registered.
{ config, lib, pkgs, ... }:
let
  cfg = config.eldar-os.tailnetProxies;
  package = pkgs.callPackage ../packages/supervisor.nix { };
in
{
  options.eldar-os.tailnetProxies = lib.mkOption {
    default = { };
    type = lib.types.attrsOf (lib.types.submodule {
      options = {
        hostname = lib.mkOption { type = lib.types.str; description = "Tailnet machine name."; };
        target = lib.mkOption { type = lib.types.str; description = "URL to forward to, e.g. http://10.42.0.2:3000."; };
        environmentFile = lib.mkOption {
          type = lib.types.nullOr lib.types.str;
          default = null;
          description = "Sets PROXY_ALLOWED_USER (or WEB_ALLOWED_USER): the tailnet login let in.";
        };
      };
    });
  };

  config.users.users.tailnet-proxy = lib.mkIf (cfg != { }) { isSystemUser = true; group = "tailnet-proxy"; };
  config.users.groups.tailnet-proxy = lib.mkIf (cfg != { }) { };
  config.systemd.services = lib.mapAttrs' (name: p: lib.nameValuePair "tailnet-proxy-${name}" {
    description = "${p.hostname} on the tailnet → ${p.target}";
    wantedBy = [ "multi-user.target" ];
    wants = [ "network-online.target" ];
    after = [ "network-online.target" ];
    environment = {
      PROXY_HOSTNAME = p.hostname;
      PROXY_TARGET = p.target;
      PROXY_STATE_DIR = "/var/lib/tailnet-proxy-${name}";
      HOME = "/var/lib/tailnet-proxy-${name}";
    };
    serviceConfig = {
      ExecStart = "${package}/bin/tailnet-proxy";
      EnvironmentFile = lib.optional (p.environmentFile != null) p.environmentFile;
      User = "tailnet-proxy";
      Group = "tailnet-proxy";
      StateDirectory = "tailnet-proxy-${name}";
      StateDirectoryMode = "0700";
      Restart = "always";
      RestartSec = 5;
      # Its own node (tsnet): never the host's tailnet connection.
      IPAddressDeny = [ "100.64.0.0/10" "fd7a:115c:a1e0::/48" ];
      NoNewPrivileges = true;
      ProtectSystem = "strict";
      ProtectHome = true;
      PrivateTmp = true;
      PrivateDevices = true;
      ProtectKernelTunables = true;
      ProtectKernelModules = true;
      ProtectControlGroups = true;
      RestrictNamespaces = true;
      LockPersonality = true;
      MemoryDenyWriteExecute = true;
      CapabilityBoundingSet = "";
    };
  }) cfg;
}
