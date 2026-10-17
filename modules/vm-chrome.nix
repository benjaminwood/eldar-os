# Chrome for agents in Eldar's VM: a browser they may drive freely (not one of
# Ben's real ones; see browsers on other hosts in docs/supervisor.md), through
# Playwright MCP over CDP. Ben can watch it from his tailnet (the web stream on
# :3000, proxied on the host by modules/vm-chrome-view.nix).
#
# It's LinuxServer's chrome image (Chrome plus a desktop streamed by selkies)
# in eldar's rootless Docker, run by a user service. Like herdr, an update
# never restarts it (open tabs and logins live there); a new image is used
# from the VM's next restart. The profile persists in a Docker volume.
{ config, lib, pkgs, ... }:
let
  cfg = config.eldar-os.vmChrome;

  # Chrome only listens for CDP on 127.0.0.1 (--remote-debugging-address is
  # ignored), and rejects a Host header that isn't localhost or an IP literal
  # (DNS-rebinding defense). The image's own nginx forwards :19222 to it with
  # Host 127.0.0.1:19222, which is also where the VM reaches the published
  # port, so the WebSocket URLs Chrome hands out work from here.
  cdpProxy = pkgs.writeText "chrome-cdp.conf" ''
    server {
      listen 19222;
      location / {
        proxy_pass http://127.0.0.1:9222;
        proxy_set_header Host 127.0.0.1:19222;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        # CDP sessions are long-lived and mostly idle.
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
      }
    }
  '';

  # Window-manager autostart (openbox; POSIX sh): the image's default line plus
  # flags CHROME_CLI can't carry (it's split unquoted).
  autostart = pkgs.writeText "chrome-autostart" ''
    #!/bin/sh
    if [ -n "''${CHROME_HOST_RULES:-}" ]; then
      wrapped-chrome ''${CHROME_CLI} "--host-resolver-rules=''${CHROME_HOST_RULES}"
    else
      wrapped-chrome ''${CHROME_CLI}
    fi
  '';

  # Chrome policy: extensions installed from the Web Store and kept installed
  # (agents driving the browser can't remove or disable them).
  policy = pkgs.writeText "eldar-chrome-policy.json" (builtins.toJSON {
    ExtensionInstallForcelist = map (id: "${id};https://clients2.google.com/service/update2/crx") cfg.extensions;
  });

  # Chrome leaves Singleton* locks behind after a hard stop, which block the
  # next start.
  clearLocks = pkgs.writeText "chrome-clear-locks" ''
    #!/usr/bin/with-contenv bash
    PROFILE_DIR=/config/chrome-profile
    if [ -d "$PROFILE_DIR" ]; then
      rm -f "$PROFILE_DIR/SingletonLock" "$PROFILE_DIR/SingletonCookie" \
            "$PROFILE_DIR/SingletonSocket" "$PROFILE_DIR/lockfile"
    fi
  '';
in
{
  options.eldar-os.vmChrome = {
    enable = lib.mkEnableOption "Chrome for agents (Playwright over CDP)";
    image = lib.mkOption {
      type = lib.types.str;
      description = "LinuxServer chrome image, pinned by digest.";
    };
    timeZone = lib.mkOption { type = lib.types.str; default = "America/Los_Angeles"; };
    extensions = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Chrome Web Store extension ids to force-install.";
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.user.services.eldar-chrome = {
      description = "Chrome for agents (rootless Docker)";
      wantedBy = [ "default.target" ];
      after = [ "docker.service" ];
      requires = [ "docker.service" ];
      unitConfig.ConditionUser = "eldar";
      restartIfChanged = false; # tabs and logins live here (decision 0010)
      path = [ pkgs.docker ];
      environment.DOCKER_HOST = "unix://%t/docker.sock";
      serviceConfig = {
        ExecStartPre = "-${pkgs.docker}/bin/docker rm -f eldar-chrome";
        ExecStart = pkgs.writeShellScript "eldar-chrome" ''
          exec docker run --rm --name eldar-chrome \
            --shm-size 1g \
            -p 3000:3000 -p 127.0.0.1:19222:19222 \
            -v eldar-chrome-config:/config \
            -v ${cdpProxy}:/etc/nginx/conf.d/cdp.conf:ro \
            -v ${autostart}:/config/.config/openbox/autostart:ro \
            -v ${clearLocks}:/custom-cont-init.d/01-clear-chrome-locks:ro \
            -v ${policy}:/etc/opt/chrome/policies/managed/eldar-os.json:ro \
            -e PUID=1000 -e PGID=1000 -e TZ=${cfg.timeZone} \
            -e PIXELFLUX_WAYLAND=false \
            -e SELKIES_USE_CSS_SCALING=true -e SELKIES_FRAMERATE=8-30 \
            -e CHROME_CLI="--user-data-dir=/config/chrome-profile --remote-debugging-port=9222 --remote-allow-origins=*" \
            ${cfg.image}
        '';
        # The stream (selkies, :3000) is for Ben, through the host; CDP
        # (:19222) only for agents in this VM. X11, not Wayland: on Wayland
        # Chrome paints only while someone watches the stream, which stalls
        # Playwright when nobody does. No GPU, so selkies encodes in software:
        # a HiDPI client asking for its native size wedges it; CSS scaling and
        # a frame-rate range keep the stream (and its clicks) working.
        ExecStop = "${pkgs.docker}/bin/docker stop -t 20 eldar-chrome";
        Restart = "always";
        RestartSec = 10;
        TimeoutStartSec = "15min"; # the first start pulls ~4.6 GB
      };
    };
  };
}
