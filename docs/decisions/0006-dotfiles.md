# 0006. Dotfiles: one chezmoi repo for every machine

Accepted, 2026-10-16

## Context

Ben's preferences (shell, tmux, git, editor) are needed in three kinds of
place: this NixOS machine, his Mac, and Docker containers, including ones
he doesn't control. He already keeps them in a public chezmoi dotfiles repo
used on the Mac and in containers. Two homes for preferences would drift.

## Decision

The dotfiles repo stays the single source of preferences, and chezmoi stays
the single way they're placed, everywhere. On eldar-os:

- the repo is a pinned, non-flake input (`dotfiles` in `flake.lock`);
- Nix provides the packages (`hosts/eldar-os/ben.nix`); chezmoi only places
  files, run by a oneshot unit per user (`modules/chezmoi.nix`) that re-runs
  whenever the pinned source changes;
- the dotfiles know they're on NixOS (`is_nixos`), and their plugin
  downloads are pinned to exact commits with checksums.

Home-manager was the alternative. It would give Nix-native config here, but
a second mechanism for the same preferences, and Nix is too heavy for
arbitrary containers, so chezmoi would stay anyway.

## Consequences

Changing a preference is a commit in the dotfiles repo, then a pin bump here
(`scripts/check --lock`). `DOTFILES=<checkout> scripts/try` tries a local,
uncommitted dotfiles tree without either. Dotfiles are applied and health-
checked with the system, but rolling back the system doesn't un-place files;
reverting the dotfiles commit does. Because the repo is public and pinned
here, eldar-os can only pin commits that are already public.
