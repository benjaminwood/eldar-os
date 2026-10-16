# Users' dotfiles, applied with chezmoi from a pinned source
# (docs/decisions/0006-dotfiles.md).
#
# The dotfiles repo is the one home for a user's preferences across every
# machine they use (macOS, containers, here). Here, Nix provides the packages;
# chezmoi only places the files. Each user gets a oneshot unit that runs
# `chezmoi apply --init` as that user against the source pinned in flake.lock,
# and re-runs whenever that source changes, so dotfiles are applied, health-
# checked and rolled back along with the rest of the system.
{ config, lib, pkgs, ... }:
let
  cfg = config.eldar-os.chezmoi;
in
{
  options.eldar-os.chezmoi.users = lib.mkOption {
    default = { };
    description = "Users whose dotfiles chezmoi applies, and the source for each.";
    type = lib.types.attrsOf (lib.types.submodule {
      options.source = lib.mkOption {
        type = lib.types.path;
        description = "chezmoi source directory (e.g. a non-flake input).";
      };
    });
  };

  config = {
    environment.systemPackages = lib.mkIf (cfg.users != { }) [ pkgs.chezmoi ];

    systemd.services = lib.mapAttrs' (user: u: lib.nameValuePair "chezmoi-${user}" {
      description = "Apply ${user}'s dotfiles with chezmoi";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ]; # plugin archives are downloaded
      restartTriggers = [ u.source ];
      # Scripts in the dotfiles (e.g. tmux plugin install) run with this PATH.
      path = with pkgs; [ chezmoi git curl gnutar gzip coreutils findutils gnused gnugrep gawk bash zsh tmux ];
      environment.HOME = config.users.users.${user}.home;
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        User = user;
        # `apply --init` (re)generates the config from the source's template without
        # touching git; the source is read-only in the store.
        ExecStart = "${pkgs.chezmoi}/bin/chezmoi apply --init --force --no-tty --source ${u.source}";
        TimeoutStartSec = "10min";
      };
    }) cfg.users;
  };
}
