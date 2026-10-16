# 0004. Secrets: sops-nix for the machine, 1Password for the proxy

Accepted, 2026-10-16

## Context

Two kinds of secret: ones the machine needs to run at all (deploy keys,
password hashes, network auth), and the credentials an agent's actions use,
which a privileged proxy hands out per request with approval. The repo is
public, so neither can live in it, even encrypted.

## Decision

- **Machine secrets: sops-nix.** One encrypted file in a separate private repo
  (`eldar-os-secrets`), readable by an admin key (on the admin host, backed up
  in 1Password) and by the machine's SSH host key. The machine fetches it on
  each apply and decrypts at activation. The flake doesn't reference the
  private repo, so the public config builds without it.
- **Proxy-held credentials: 1Password at runtime**, as the current proxy does.
  Not built yet.
- **Deploy keys are generated on the machine** and only their public halves are
  registered with GitHub, so they never leave it.

## Consequences

Machine secrets work offline and roll back with the system. Adding one is an
edit in the secrets repo plus a reference in the config. Each new machine (or
a reinstall with a new host key) has to be added as a recipient. CI uses a
throwaway host key and a test secrets file, so it exercises decryption
without real secrets.
