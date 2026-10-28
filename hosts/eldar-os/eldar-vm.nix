# Eldar's VM on this machine (modules/eldar-vm.nix): sizing, and what's inside.
# Machine-specific: a fork picks its own tools.
{ dotfiles, config, ... }:
{
  # Eldar gets most of the machine (Ryzen 9 3900X: 24 threads, 64 GB):
  # its projects' dev stacks (databases, search, Rails, Phoenix) run in here.
  # All 24 threads (vCPUs are scheduled, not reserved), and all but 16 GB of
  # RAM: the host keeps room for its own services (Home Assistant, Atuin,
  # Docker), the supervisor and the virtiofs daemons.
  eldar-os.eldarVm.vcpu = 24;
  # Eldar's identity and Claude settings live in its workspace repo (private).
  eldar-os.eldarVm.claudeLinks = {
    "CLAUDE.md" = "user/CLAUDE.md";
    "settings.json" = "user/settings.json";
    "projects/-home-eldar-workspace/memory" = "user/memory";
  };
  # Playwright on the VM's own Chrome (modules/vm-chrome.nix), for every session.
  eldar-os.eldarVm.mcpServers.playwright = {
    command = "/run/current-system/sw/bin/npx";
    args = [ "-y" "@playwright/mcp@0.0.83" "--cdp-endpoint" "http://127.0.0.1:19222" "--viewport-size" "1280,800" ];
  };
  eldar-os.eldarVm.memMiB = 49152;

  eldar-os.eldarVm.config = { pkgs, lib, ... }:
  let
    herdr = pkgs.callPackage ../../packages/herdr.nix { };
    plannotator-tui = pkgs.callPackage ../../packages/plannotator-tui.nix { };
    # Eldar's two channels (supervisor/mcp): Ben's chat, and notify_ben events
    # from other agents, scripts and cron. Only Eldar's session loads them.
    channels = pkgs.writeText "eldar-channels.mcp.json" (builtins.toJSON {
      mcpServers = lib.genAttrs [ "chat" "relay" ] (name: {
        command = "/run/current-system/sw/bin/supervisor";
        args = [ "channel" name ];
      });
    });
    # Quickbeam, the family agent (below): its repo, Claude config, settings
    # and only MCP server.
    quickbeam = rec {
      repo = "/var/lib/quickbeam/repo";
      config = "/var/lib/quickbeam/claude";
      mcp = pkgs.writeText "quickbeam.mcp.json" (builtins.toJSON {
        mcpServers.imessage = { command = "/run/current-system/sw/bin/supervisor"; args = [ "channel" "imessage" ]; };
      });
      settings = pkgs.writeText "quickbeam-settings.json" (builtins.toJSON {
        permissions.allow = [ "mcp__imessage__reply" "mcp__imessage__react" "mcp__imessage__history" "mcp__imessage__contacts"
          "Read" "Edit" "Write" "Glob" "Grep" "WebSearch" "WebFetch" ];
        # herdr's hook (installed for Eldar): reports the session's ID to
        # herdr, which is how the supervisor knows this is Quickbeam's tab.
        hooks.SessionStart = [{ matcher = "^(startup|resume|clear|compact|fork)$";
          hooks = [{ type = "command"; command = "bash /home/eldar/.claude/hooks/herdr-agent-state.sh session"; timeout = 10; }]; }];
      });
    };
    # Claude sessions kept running in herdr, each in its own tab of workspace
    # "primary", with its own flags. Every 30 s: make sure the workspace and
    # tab exist and Claude runs in the tab; if it's been gone for two checks,
    # resume the newest session with the tab's name (or start one). `herdr
    # agent start` only acts on a pane at a bare shell prompt, so it never
    # types into anything else. herdr resumes agents after a restart with a
    # plain `claude --resume`, without these flags (or the tab's environment):
    # such a Claude is stopped, and the next checks resume it properly.
    keepClaude = unit: k: {
      inherit (k) description;
      wantedBy = [ "default.target" ];
      after = [ "herdr.service" ];
      wants = [ "herdr.service" ];
      unitConfig.ConditionUser = "eldar";
      path = [ herdr pkgs.jq pkgs.coreutils pkgs.gnugrep pkgs.findutils pkgs.bash pkgs.zsh pkgs.git ];
      serviceConfig = {
        ExecStartPre = pkgs.writeShellScript "${unit}-prepare" k.prepare;
        ExecStart = pkgs.writeShellScript unit ''
          WORKSPACE=primary TAB=${k.tab} NAME=${k.tab} CWD=${k.cwd}
          TRANSCRIPTS=${k.configDir}/projects/${builtins.replaceStrings [ "/" ] [ "-" ] k.cwd}
          CLAUDE_ARGS=(${lib.escapeShellArgs k.args})
          MARKER=${lib.escapeShellArg k.marker}  # in the command line of the right Claude
          TAB_ENV=(${lib.escapeShellArgs (lib.mapAttrsToList (n: v: "${n}=${v}") k.env)})
          log() { echo "${unit}: $*"; }
          h() { herdr "$@" 2>/dev/null; }
          # Newest transcript whose latest name is $NAME; prints its session ID.
          named_session() {
            local f
            for f in $(grep -l "\"customTitle\":\"$NAME\"" "$TRANSCRIPTS"/*.jsonl 2>/dev/null | xargs -r ls -t); do
              if [[ $(grep -o '"customTitle":"[^"]*"' "$f" | tail -1) == "\"customTitle\":\"$NAME\"" ]]; then
                basename "$f" .jsonl; return
              fi
            done
          }
          # Whether process $1 has every variable of the tab's environment.
          has_env() {
            local kv
            for kv in "''${TAB_ENV[@]}"; do
              tr '\0' '\n' < /proc/"$1"/environ 2>/dev/null | grep -qxF "$kv" || return 1
            done
          }
          env_args=(); for kv in "''${TAB_ENV[@]}"; do env_args+=(--env "$kv"); done
          misses=0
          while true; do
            sleep 30
            ws=$(h workspace list | jq -r --arg l "$WORKSPACE" '[.result.workspaces[] | select(.label == $l)][0].workspace_id // empty')
            if [[ -z $ws ]]; then
              log "no '$WORKSPACE' workspace; creating it"
              h workspace create --label "$WORKSPACE" --cwd /home/eldar/workspace --no-focus >/dev/null; continue
            fi
            tab=$(h tab list --workspace "$ws" | jq -r --arg l "$TAB" '[.result.tabs[] | select(.label == $l)][0].tab_id // empty')
            if [[ -z $tab ]]; then
              log "no '$TAB' tab in $WORKSPACE; creating it"
              h tab create --workspace "$ws" --cwd "$CWD" --label "$TAB" "''${env_args[@]}" --no-focus >/dev/null; continue
            fi
            pane=$(h pane list --workspace "$ws" | jq -c --arg t "$tab" '[.result.panes[] | select(.tab_id == $t)][0] // empty')
            [[ -z $pane ]] && continue
            pane_id=$(jq -r '.pane_id' <<<"$pane")
            if [[ $(jq -r '.agent // empty' <<<"$pane") == claude ]]; then
              fg=$(h pane process-info --pane "$pane_id" | jq -c '.result.process_info.foreground_processes[0] // empty')
              if [[ -n $fg && $(jq -r .cmdline <<<"$fg") != *"$MARKER"* ]]; then
                log "Claude in $pane_id runs without the $TAB flags ($(jq -r .cmdline <<<"$fg")); restarting it"
                kill "$(jq -r .pid <<<"$fg")"
              elif [[ -n $fg ]] && ! has_env "$(jq -r .pid <<<"$fg")"; then
                # The tab was made without its environment (by hand, say):
                # stop Claude and close the tab, so it's made again with it.
                log "Claude in $pane_id lacks the $TAB environment; closing the tab"
                kill "$(jq -r .pid <<<"$fg")"; sleep 2; h tab close "$tab" >/dev/null
              fi
              misses=0; continue
            fi
            misses=$((misses + 1)); ((misses < 2)) && continue
            id=$(named_session)
            if [[ -n $id ]]; then
              log "Claude isn't running in $pane_id; resuming $NAME ($id)"; args=("''${CLAUDE_ARGS[@]}" --resume "$id")
            else
              log "Claude isn't running in $pane_id and no session is named $NAME; starting one"; args=("''${CLAUDE_ARGS[@]}" -n "$NAME")
            fi
            out=$(herdr agent start "''${NAME,,}" --kind claude --pane "$pane_id" --timeout 90000 -- "''${args[@]}" 2>&1); rc=$?
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
  in {
    imports = [ ../../modules/chezmoi.nix ../../modules/vm-chrome.nix ];

    # Chrome agents drive freely (Playwright over CDP); Ben watches its stream.
    eldar-os.vmChrome = {
      enable = true;
      # *.test: the dev router below, reached at the VM's own address.
      hostRules = "MAP *.test ${config.eldar-os.eldarVm.address}";
      # Chrome 155.0.8059.84 (ls132)
      image = "lscr.io/linuxserver/chrome@sha256:0bd253fcf2bc409f7614a5ecd1237ebc90d04002ceb9fc1b5431a911089deab9";
    };

    # Dev stacks (the admin app's bin/dev) run an HTTPS router and a DNS server for *.test in
    # eldar's rootless Docker with host networking, which is this VM's own network namespace, where
    # ports below 1024 are root's. Let unprivileged processes bind from 53 up (DNS 53, HTTP 80,
    # HTTPS 443): only eldar runs here, and the host enforces network policy at the tap.
    boot.kernel.sysctl."net.ipv4.ip_unprivileged_port_start" = 53;
    # *.test goes to that DNS server (127.0.0.1); every other name resolves as before.
    services.resolved.settings.Resolve = { DNS = [ "127.0.0.1" ]; Domains = [ "~test" ]; };

    # herdr, Eldar's terminal workspace manager: the server runs as eldar
    # (lingering, so with no one logged in), and there's always a "primary"
    # workspace to start sessions in. The supervisor drives it for Ben's UIs.
    systemd.user.services.herdr = {
      description = "herdr server";
      wantedBy = [ "default.target" ];
      # Every pane (Claude sessions, shells) lives under this server: an
      # update never restarts it. A new herdr is used from the VM's next
      # restart (docs/decisions/0010-eldar-vm-updates-in-place.md).
      restartIfChanged = false;
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

    # herdr plugins, linked from the store instead of `herdr plugin install`
    # (which downloads into a mutable checkout). Their keybindings are
    # preferences, in the dotfiles' herdr config. Re-linking a new version
    # takes effect without restarting the server.
    systemd.user.services.herdr-plugins = {
      description = "Link herdr plugins";
      wantedBy = [ "default.target" ];
      after = [ "herdr.service" ];
      wants = [ "herdr.service" ];
      unitConfig.ConditionUser = "eldar";
      path = [ herdr pkgs.coreutils ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = pkgs.writeShellScript "herdr-plugins" ''
          set -e
          for _ in $(seq 100); do herdr plugin list >/dev/null 2>&1 && break; sleep 0.2; done
          herdr plugin link ${pkgs.callPackage ../../packages/herdr-annotate.nix { inherit plannotator-tui; }} >/dev/null
        '';
      };
    };

    # Eldar, the chief-of-staff session, with its channels: chat (Ben talks
    # to it from the supervisor's web UI) and relay (other agents reach Ben
    # through it).
    systemd.user.services.eldar-primary = keepClaude "eldar-primary" {
      description = "Keep Eldar (the chief-of-staff session) running";
      tab = "Eldar";
      cwd = "/home/eldar/workspace";
      configDir = "$HOME/.claude";
      args = [ "--dangerously-skip-permissions"
        "--dangerously-load-development-channels" "server:chat" "server:relay"
        "--mcp-config" "${channels}" ];
      marker = "server:chat server:relay";
      env = { };
      # Claude's one-time prompts would block an unattended start: the
      # bypass-permissions confirmation (where Enter means "No, exit") and
      # trusting ~/workspace. Accept them once, merging into existing files.
      prepare = ''
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
    };
    # Quickbeam, the family agent: iMessage through the supervisor's
    # BlueBubbles bridge is all it reaches. Deliberately limited: restricted
    # mode (no shell or code-running tools, file tools confined to its
    # repo), only its own channel as MCP server, tools it isn't allowed are
    # refused rather than asked about, and its own Claude config directory,
    # so none of the user-level settings, MCP servers or CLAUDE.md load. It
    # lives outside /home/eldar so no CLAUDE.md above it loads either.
    systemd.user.services.quickbeam-primary = keepClaude "quickbeam-primary" {
      description = "Keep Quickbeam (the family agent) running";
      tab = "Quickbeam";
      cwd = quickbeam.repo;
      configDir = quickbeam.config;
      args = [ "--restricted" "--permission-mode" "dontAsk"
        "--tools" "Read,Edit,Write,Glob,Grep,WebSearch,WebFetch"
        "--settings" "${quickbeam.settings}"
        "--strict-mcp-config" "--mcp-config" "${quickbeam.mcp}"
        "--dangerously-load-development-channels" "server:imessage" ];
      marker = "--restricted";
      env.CLAUDE_CONFIG_DIR = quickbeam.config;
      prepare = ''
        set -e
        install -d -m 700 ${quickbeam.config}
        f=${quickbeam.config}/.claude.json; [ -s "$f" ] || echo '{}' > "$f"
        jq '.projects["${quickbeam.repo}"].hasTrustDialogAccepted = true' "$f" > "$f.tmp" && mv "$f.tmp" "$f"
        # Its repo (identity and memory), private: git reaches it through
        # Eldar's GitHub App, which must be installed on it.
        [ -d ${quickbeam.repo}/.git ] || git clone -q https://github.com/example/quickbeam ${quickbeam.repo} || true
      '';
    };
    # Quickbeam writes its memory into its repo: commit and push it.
    systemd.user.services.quickbeam-sync = {
      description = "Commit and push Quickbeam's memory";
      unitConfig.ConditionUser = "eldar";
      path = [ pkgs.git pkgs.coreutils ];
      serviceConfig.Type = "oneshot";
      script = ''
        cd ${quickbeam.repo} 2>/dev/null || exit 0
        git add -A
        git diff --cached --quiet || git -c user.name=Quickbeam -c user.email=quickbeam@localhost commit -q -m "Quickbeam's notes"
        git pull -q --rebase && git push -q
      '';
    };
    systemd.user.timers.quickbeam-sync = {
      wantedBy = [ "timers.target" ];
      unitConfig.ConditionUser = "eldar";
      timerConfig = { OnBootSec = "5min"; OnUnitActiveSec = "15min"; };
    };
    systemd.tmpfiles.rules = [ "d /var/lib/quickbeam 0700 eldar eldar-ws -" ];

    # Ben works in here as eldar, so his dotfiles apply to both users.
    eldar-os.chezmoi.users = {
      eldar = {
        source = dotfiles;
        # herdr reads its config at start, and its server never restarts on
        # an update (panes live under it): have it re-read the new one.
        afterApply = "${lib.getExe' herdr "herdr"} server reload-config";
      };
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
      git gh ripgrep fd jq bat htop tree vim helix gum direnv tmux fzf xxd file yq-go tokei
      # on PATH for the plannotator-tui skill (`plannotator-tui herdr open`)
      plannotator-tui
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
      (callPackage ../../packages/atuin.nix { })
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
