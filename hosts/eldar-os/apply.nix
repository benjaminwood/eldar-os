# Where this machine applies its config from, and who it trusts to sign it.
# Machine-specific: a fork points these at its own repos and keys.
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
  };
}
