# Eldar's VM on this machine (modules/eldar-vm.nix): sizing, and what's inside.
# Machine-specific: a fork picks its own tools.
{ dotfiles, ... }:
{
  # While eldar-os itself runs as a VM (8 CPUs, 12 GB), Eldar gets a slice.
  # At cutover to bare metal (Ryzen 9 3900X: 24 threads, 64 GB): vcpu = 24,
  # memMiB = 57344 (all but ~8 GB for the host, supervisor and desktop).
  eldar-os.eldarVm.vcpu = 6;
  eldar-os.eldarVm.memMiB = 6144;

  eldar-os.eldarVm.config = { pkgs, lib, ... }:
  let
    herdr = pkgs.callPackage ../../packages/herdr.nix { };
  in {
    imports = [ ../../modules/chezmoi.nix ];

    # herdr, Eldar's terminal workspace manager: the server runs as eldar
    # (lingering, so with no one logged in), and there's always a "primary"
    # workspace to start sessions in. The supervisor drives it for Ben's UIs.
    systemd.user.services.herdr = {
      description = "herdr server";
      wantedBy = [ "default.target" ];
      unitConfig.ConditionUser = "eldar";
      path = [ herdr pkgs.jq pkgs.coreutils pkgs.bash pkgs.zsh ];
      serviceConfig = {
        ExecStart = "${lib.getExe' herdr "herdr"} server";
        ExecStartPost = pkgs.writeShellScript "herdr-primary-workspace" ''
          for _ in $(seq 100); do herdr workspace list >/dev/null 2>&1 && break; sleep 0.2; done
          if [ "$(herdr workspace list | jq '[.result.workspaces[]?] | length')" = 0 ]; then
            herdr workspace create --label primary --cwd /home/eldar/workspace --no-focus >/dev/null
          fi
        '';
        Restart = "always";
        RestartSec = 2;
      };
    };

    # Ben works in here as eldar, so his dotfiles apply to both users.
    eldar-os.chezmoi.users = {
      eldar.source = dotfiles;
      ben.source = dotfiles;
    };
    programs.zsh = {
      enable = true;
      enableGlobalCompInit = false; # oh-my-zsh does it
      promptInit = "";
    };
    users.users.eldar.shell = pkgs.zsh;
    users.users.ben.shell = pkgs.zsh;
    environment.localBinInPath = true; # ~/.local/bin (Claude Code, npm -g prefixes)

    # Prebuilt Linux binaries expect /lib64/ld-linux…: Claude Code's native
    # build, npm packages with native parts, editor servers.
    programs.nix-ld.enable = true;

    environment.systemPackages = with pkgs; [
      # general
      git gh ripgrep fd jq bat htop tree vim helix gum direnv tmux fzf xxd
      # runtimes
      nodejs_22 typescript eslint bun go
      (python3.withPackages (ps: with ps; [ numpy scipy pillow rectpack ]))
      # cloud and dev CLIs
      awscli2 kubectl heroku wrangler duckdb codex
      # 3D-printing pipeline
      potrace openscad imagemagick
      # cron (jobs.json) until jobs move to systemd timers
      supercronic
      # not in nixpkgs: ../../packages
      herdr
      (callPackage ../../packages/hb.nix { })
      (callPackage ../../packages/rwx.nix { })
      (callPackage ../../packages/composio.nix { })
      (callPackage ../../packages/cf.nix { })
    ];

    # Claude Code: the native build, installed once per user and then updating
    # itself, as on every other machine. A deliberate exception to pinning:
    # it ships near-daily and nixpkgs trails far behind.
    systemd.services = builtins.listToAttrs (map (user: {
      name = "claude-install-${user}";
      value = {
        description = "Install Claude Code for ${user}";
        wantedBy = [ "multi-user.target" ];
        wants = [ "network-online.target" ];
        after = [ "network-online.target" ];
        unitConfig.ConditionPathExists = "!/home/${user}/.local/bin/claude";
        path = with pkgs; [ bash curl coreutils gnutar gzip ];
        environment.HOME = "/home/${user}";
        serviceConfig = {
          Type = "oneshot";
          User = user;
          ExecStart = "${pkgs.bash}/bin/bash -c 'curl -fsSL https://claude.ai/install.sh | bash'";
        };
      };
    }) [ "eldar" "ben" ]);
  };
}
