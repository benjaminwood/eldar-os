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
  ];
}
