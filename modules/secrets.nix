# Machine secrets via sops-nix (docs/decisions/0004-secrets.md).
#
# The encrypted file lives in a separate private repo and is fetched by
# eldar-os-apply into /var/lib/eldar-os/secrets, so this public flake evaluates
# and builds without it. The machine decrypts with its SSH host key.
{
  sops = {
    defaultSopsFile = "/var/lib/eldar-os/secrets/eldar-os.yaml";
    validateSopsFiles = false; # not in the store: fetched at apply time
    age.sshKeyPaths = [ "/etc/ssh/ssh_host_ed25519_key" ];
    gnupg.sshKeyPaths = [ ];

    # Proves the pipeline end to end (scripts/verify checks it).
    secrets.canary = { };
  };
}
