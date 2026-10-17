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

    # Eldar, the chief-of-staff Claude session: kept running in herdr
    # workspace "primary", tab "Eldar", named "Eldar", with the chat channel
    # (agent-relay/chat-channel.ts in the workspace) so Ben can talk to it from
    # the supervisor's web UI. Every 30 s: make sure the workspace and tab
    # exist and Claude runs in the tab; if it's been gone for two checks,
    # resume the newest session named Eldar (or start one). `herdr agent
    # start` only acts on a pane at a bare shell prompt, so it never types
    # into anything else.
    systemd.user.services.eldar-primary = {
      description = "Keep Eldar (the chief-of-staff session) running";
      wantedBy = [ "default.target" ];
      after = [ "herdr.service" ];
      wants = [ "herdr.service" ];
      unitConfig.ConditionUser = "eldar";
      path = [ herdr pkgs.jq pkgs.coreutils pkgs.gnugrep pkgs.findutils pkgs.bash pkgs.zsh pkgs.bun ];
      environment.PROXY_URL = "http://10.42.0.1:9100";
      serviceConfig = {
        # Claude's one-time prompts would block an unattended start: the
        # bypass-permissions confirmation (where Enter means "No, exit") and
        # trusting ~/workspace. Accept them once, merging into existing files.
        ExecStartPre = pkgs.writeShellScript "eldar-primary-prepare" ''
          set -e
          mkdir -p ~/.claude
          f=~/.claude/settings.json; [ -s "$f" ] || echo '{}' > "$f"
          jq '.skipDangerousModePermissionPrompt = true' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
          f=~/.claude.json; [ -s "$f" ] || echo '{}' > "$f"
          jq '.projects["/home/eldar/workspace"].hasTrustDialogAccepted = true' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
          # herdr's Claude hooks: report each session's ID and state to herdr
          # (the supervisor uses it to know which session is Eldar).
          herdr integration install claude >/dev/null
        '';
        ExecStart = pkgs.writeShellScript "eldar-primary" ''
          WORKSPACE=primary TAB=Eldar NAME=Eldar CWD=/home/eldar/workspace
          TRANSCRIPTS=$HOME/.claude/projects/-home-eldar-workspace
          RELAY=$CWD/projects/agent-relay
          CLAUDE_ARGS=(--dangerously-skip-permissions
            --dangerously-load-development-channels server:chat
            --mcp-config "$RELAY/chat.mcp.json")
          log() { echo "eldar-primary: $*"; }
          h() { herdr "$@" 2>/dev/null; }
          # Newest transcript whose latest name is $NAME; prints its session ID.
          eldar_session() {
            local f
            for f in $(grep -l "\"customTitle\":\"$NAME\"" "$TRANSCRIPTS"/*.jsonl 2>/dev/null | xargs -r ls -t); do
              if [[ $(grep -o '"customTitle":"[^"]*"' "$f" | tail -1) == "\"customTitle\":\"$NAME\"" ]]; then
                basename "$f" .jsonl; return
              fi
            done
          }
          misses=0
          while true; do
            sleep 30
            ws=$(h workspace list | jq -r --arg l "$WORKSPACE" '[.result.workspaces[] | select(.label == $l)][0].workspace_id // empty')
            if [[ -z $ws ]]; then
              log "no '$WORKSPACE' workspace; creating it"
              h workspace create --label "$WORKSPACE" --cwd "$CWD" --no-focus >/dev/null; continue
            fi
            tab=$(h tab list --workspace "$ws" | jq -r --arg l "$TAB" '[.result.tabs[] | select(.label == $l)][0].tab_id // empty')
            if [[ -z $tab ]]; then
              log "no '$TAB' tab in $WORKSPACE; creating it"
              h tab create --workspace "$ws" --cwd "$CWD" --label "$TAB" --no-focus >/dev/null; continue
            fi
            pane=$(h pane list --workspace "$ws" | jq -c --arg t "$tab" '[.result.panes[] | select(.tab_id == $t)][0] // empty')
            [[ -z $pane ]] && continue
            if [[ $(jq -r '.agent // empty' <<<"$pane") == claude ]]; then misses=0; continue; fi
            misses=$((misses + 1)); ((misses < 2)) && continue
            pane_id=$(jq -r '.pane_id' <<<"$pane")
            id=$(eldar_session)
            if [[ -n $id ]]; then
              log "Claude isn't running in $pane_id; resuming $NAME ($id)"; args=("''${CLAUDE_ARGS[@]}" --resume "$id")
            else
              log "Claude isn't running in $pane_id and no session is named $NAME; starting one"; args=("''${CLAUDE_ARGS[@]}" -n "$NAME")
            fi
            out=$(herdr agent start eldar --kind claude --pane "$pane_id" --timeout 90000 -- "''${args[@]}" 2>&1); rc=$?
            if [[ $(h pane list --workspace "$ws" | jq -r --arg p "$pane_id" '.result.panes[] | select(.pane_id == $p) | .agent // empty') == claude ]]; then
              # The development-channels warning waits for Enter (option 1
              # confirms); Enter at an idle, empty prompt does nothing.
              for _ in 1 2 3 4 5; do sleep 3; h pane send-keys "$pane_id" enter >/dev/null; done
              misses=0
            else
              log "agent start failed (rc $rc): $(head -c 300 <<<"$out")"
            fi
          done
        '';
        Restart = "always";
        RestartSec = 10;
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
