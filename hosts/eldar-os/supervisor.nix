# The supervisor on this machine (modules/supervisor.nix). Its policy and
# 1Password configuration are private: they come from the secrets repo.
{ config, ... }:
{
  eldar-os.supervisor = {
    enable = true;
    admins = [ "ben" ];
    policyFile = "/var/lib/eldar-os/secrets/supervisor/policy.yaml";
    environmentFile = config.sops.secrets.supervisor-env.path;
    # Other machines agents may run commands on: Ben's Mac ("mac").
    hostsFile = "/var/lib/eldar-os/secrets/supervisor/hosts.json";
    # AWS profiles (one SSO session; `supervisor aws-login` signs in).
    awsConfig = "/var/lib/eldar-os/secrets/supervisor/aws-config";
    web = {
      enable = true;
      # A new name while the old eldar-proxy still runs; renamed at cutover.
      hostname = "supervisor-next";
      environmentFile = config.sops.secrets.supervisor-web-env.path;
    };
  };
  # Ben watches the VM's Chrome (modules/vm-chrome.nix) from his tailnet.
  # A new name while the old stack's eldar-chrome still runs.
  eldar-os.tailnetProxies.chrome = {
    hostname = "eldar-chrome-next";
    target = "http://${config.eldar-os.eldarVm.address}:3000";
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
  # Eldar's GitHub App (docs/decisions/0011-eldar-github-app.md); its IDs
  # are in supervisor-env.
  sops.secrets.github-app-key = {
    owner = "supervisor";
    restartUnits = [ "supervisor.service" ];
  };
  systemd.services.supervisor.environment.SUPERVISOR_GITHUB_APP_KEY = config.sops.secrets.github-app-key.path;
}
