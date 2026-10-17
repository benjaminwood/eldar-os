# The supervisor on this machine (modules/supervisor.nix). Its policy and
# 1Password configuration are private: they come from the secrets repo.
{ config, ... }:
{
  eldar-os.supervisor = {
    enable = true;
    admins = [ "ben" ];
    policyFile = "/var/lib/eldar-os/secrets/supervisor/policy.yaml";
    environmentFile = config.sops.secrets.supervisor-env.path;
    web = {
      enable = true;
      # A new name while the old eldar-proxy still runs; renamed at cutover.
      hostname = "supervisor-next";
      environmentFile = config.sops.secrets.supervisor-web-env.path;
    };
  };
  sops.secrets.supervisor-web-env = {
    owner = "supervisor-web";
    restartUnits = [ "supervisor-web.service" ];
  };
  sops.secrets.supervisor-env = {
    owner = "supervisor";
    restartUnits = [ "supervisor.service" ];
  };
}
