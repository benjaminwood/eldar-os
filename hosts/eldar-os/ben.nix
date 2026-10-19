# Ben: the admin's account and tools. Preferences (shell, tmux, git, editor)
# live in his dotfiles repo and are applied by chezmoi; this file only says
# which packages exist. Machine-specific: a fork replaces this with its own.
{ pkgs, dotfiles, ... }:
{
  eldar-os.chezmoi.users.ben.source = dotfiles;

  users.users.ben.packages = with pkgs; [
    tmux
    fzf
    gh
    helix
    ripgrep
    fd
    jq
    bat
    htop
    tree
    xxd # used by code-remote in .zshrc
    (callPackage ../../packages/herdr.nix { }) # same build as Eldar's VM
    (callPackage ../../packages/atuin.nix { }) # shell history sync; server in atuin.nix, address from the dotfiles
  ];

  # Claude Code on the host, for Ben working on the system itself; installed
  # and self-updating as in Eldar's VM (eldar-vm.nix).
  environment.localBinInPath = true;
  programs.nix-ld.enable = true;
  systemd.services.claude-install-ben = {
    description = "Install Claude Code for ben";
    wantedBy = [ "multi-user.target" ];
    wants = [ "network-online.target" ];
    after = [ "network-online.target" ];
    unitConfig.ConditionPathExists = "!/home/ben/.local/bin/claude";
    path = with pkgs; [ bash curl coreutils gnutar gzip ];
    environment.HOME = "/home/ben";
    serviceConfig = {
      Type = "oneshot";
      User = "ben";
      ExecStart = "${pkgs.bash}/bin/bash -c 'curl -fsSL https://claude.ai/install.sh | bash'";
    };
  };
}
