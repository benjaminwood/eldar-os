# Decisions

Short records of choices that shape this system: what we decided, why, and
what it costs. Written for a person reading later, not for a tool. Code that
exists because of one points at it (`docs/decisions/0004-secrets.md`).

To change a decision, write a new record that supersedes the old one and mark
the old one *Superseded by NNNN*. Don't rewrite history.

| # | Decision | Status |
|---|---|---|
| [0001](0001-record-decisions.md) | Record decisions | Accepted |
| [0002](0002-disk-and-install.md) | Own disk, built from a VM: disko, LUKS, btrfs | Accepted |
| [0003](0003-staging-and-public.md) | Private staging repo, published to a public repo | Accepted |
| [0004](0004-secrets.md) | Secrets: sops-nix for the machine, 1Password for the supervisor | Accepted |
| [0005](0005-apply-loop.md) | The machine applies signed `main` itself | Accepted |
| [0006](0006-dotfiles.md) | Dotfiles: one chezmoi repo for every machine | Accepted |
