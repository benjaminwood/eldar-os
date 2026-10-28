# The supervisor on this machine (modules/supervisor.nix). Its policy and
# 1Password configuration are private: they come from the secrets repo.
{ config, pkgs, ... }:
{
  eldar-os.supervisor = {
    enable = true;
    admins = [ "ben" ];
    # Lets a Stream Deck focus a tab in Ben's herdr here (docs/supervisor.md).
    hostAgent = { user = "ben"; path = [ (pkgs.callPackage ../../packages/herdr.nix { }) ]; };
    policyFile = "/var/lib/eldar-os/secrets/supervisor/policy.yaml";
    environmentFile = config.sops.secrets.supervisor-env.path;
    # Other machines agents may run commands on: Ben's Mac ("mac").
    hostsFile = "/var/lib/eldar-os/secrets/supervisor/hosts.json";
    # AWS profiles (one SSO session; `supervisor aws-login` signs in).
    awsConfig = "/var/lib/eldar-os/secrets/supervisor/aws-config";
    ambientFile = "/var/lib/eldar-os/secrets/supervisor/ambient.yaml";
    # iMessage through the BlueBubbles server on a Mac on the LAN, for the
    # family agent's session (its tab and contacts are in the private file).
    imessage = {
      configFile = "/var/lib/eldar-os/secrets/supervisor/imessage.yaml";
      passwordFile = config.sops.secrets.bluebubbles-password.path;
      # The Mac's new-message webhook: an immediate poll instead of within 30 s.
      webhook = { port = 9101; allowFrom = [ "192.168.1.118" ]; };
    };
    web = {
      enable = true;
      hostname = "supervisor";
      streamHost = config.eldar-os.tailnetProxies.chrome.hostname; # the Browser tab
      environmentFile = config.sops.secrets.supervisor-web-env.path;
    };
  };
  # Ben watches the VM's Chrome (modules/vm-chrome.nix) from his tailnet.
  eldar-os.tailnetProxies.chrome = {
    hostname = "eldar-chrome";
    target = "http://${config.eldar-os.eldarVm.address}:3000";
    frameHosts = [ config.eldar-os.supervisor.web.hostname ]; # the web UI's Browser tab
    environmentFile = config.sops.secrets.supervisor-web-env.path;
  };
  sops.secrets.supervisor-web-env = {
    owner = "supervisor-web";
    restartUnits = [ "supervisor-web.service" ];
  };
  sops.secrets.supervisor-env = {
    owner = "supervisor";
    restartUnits = [ "supervisor.service" ];
  };
  # Each remote host's SSH key (hosts.json says where and how to reach it).
  sops.secrets."remote-hosts/mac" = {
    owner = "supervisor";
    restartUnits = [ "supervisor.service" ];
  };
  sops.secrets."remote-hosts/quickbeam" = {
    owner = "supervisor";
    restartUnits = [ "supervisor.service" ];
  };
  sops.secrets."remote-hosts/mac-bridge" = {
    owner = "supervisor";
    restartUnits = [ "supervisor.service" ];
  };
  sops.secrets.bluebubbles-password = {
    owner = "supervisor";
    restartUnits = [ "supervisor.service" ];
  };
  # Eldar's GitHub App (docs/decisions/0011-eldar-github-app.md); its IDs
  # are in supervisor-env.
  sops.secrets.github-app-key = {
    owner = "supervisor";
    restartUnits = [ "supervisor.service" ];
  };
  systemd.services.supervisor.environment.SUPERVISOR_GITHUB_APP_KEY = config.sops.secrets.github-app-key.path;
}
