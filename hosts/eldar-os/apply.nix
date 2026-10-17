# Where this machine applies its config from, and who it trusts to sign it.
# Machine-specific: a fork points these at its own repos and keys.
{ config, ... }:
{
  eldar-os.apply = {
    repo = "git@github.com:benjaminwood/eldar-os-staging.git";
    secretsRepo = "git@github.com:benjaminwood/eldar-os-secrets.git";
    allowedSigners = ../../trust/allowed_signers;
    gpgKeys = ../../trust/github-web-flow.gpg;
    triggerUsers = [ "ben" ];
    healthChecks = [
      ''test "$(cat /run/secrets/canary)" = secrets-pipeline-ok''
    ];
    # Both repos' webhooks point here, so a merge applies right away.
    webhook = {
      enable = true;
      secretFile = config.sops.secrets.apply-webhook-secret.path;
    };
  };
  sops.secrets.apply-webhook-secret = {
    owner = "eldar-os-hook";
    restartUnits = [ "eldar-os-apply-hook.service" ];
  };
}
