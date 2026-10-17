# eldar-os-apply: the machine applies its own config from the staging repo
# (docs/decisions/0005-apply-loop.md).
#
# A timer (and `scripts/apply`, on demand) fetches staging `main` and the
# private secrets repo with read-only deploy keys, refuses it unless every commit
# main gained (first-parent) is signed by an allowed key, builds, activates with `test`, runs health
# checks, and only then makes the new system the boot default. A failed
# check, or the apply dying mid-way, rolls back to the previous system.
{ config, lib, pkgs, ... }:
let
  cfg = config.eldar-os.apply;
  stateDir = "/var/lib/eldar-os";

  healthCheck = pkgs.writeShellScript "eldar-os-health" ''
    set -euo pipefail
    ${lib.concatMapStringsSep "\n" (c: "echo health: ${lib.escapeShellArg c}\n${c}") cfg.healthChecks}
  '';

  apply = pkgs.writeShellApplication {
    name = "eldar-os-apply";
    runtimeInputs = with pkgs; [ bash git openssh gnupg config.nix.package coreutils systemd gnugrep jq curl ];
    text = builtins.readFile ./apply.sh;
  };
in
{
  options.eldar-os.apply = {
    repo = lib.mkOption {
      type = lib.types.str;
      description = "SSH URL of the repo whose `main` this machine applies.";
    };
    secretsRepo = lib.mkOption {
      type = lib.types.str;
      description = "SSH URL of the private repo holding the sops file.";
    };
    flakeAttr = lib.mkOption {
      type = lib.types.str;
      default = config.networking.hostName;
      description = "nixosConfigurations attribute to build.";
    };
    allowedSigners = lib.mkOption {
      type = lib.types.path;
      description = "ssh-keygen allowed_signers file: keys whose signed commits may be applied.";
    };
    gpgKeys = lib.mkOption {
      type = lib.types.path;
      description = "Armored GPG public keys also allowed to sign (GitHub's web-flow key, for PRs merged on github.com).";
    };
    triggerUsers = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Users allowed to start an apply now (via polkit) and read its log.";
    };
    healthChecks = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      description = "Shell commands that must all succeed after activation, or the machine rolls back.";
    };
    rollbackAfter = lib.mkOption {
      type = lib.types.str;
      default = "10min";
      description = "If the apply hasn't confirmed health by then, the previous system is reactivated.";
    };
    webhook = {
      enable = lib.mkEnableOption "applying as soon as GitHub reports a push (docs/decisions/0012-cycle-time.md)";
      hostname = lib.mkOption {
        type = lib.types.str;
        default = "eldar-os-hook";
        description = "Tailnet machine name; webhooks go to https://<name>.<tailnet>.ts.net/github over Funnel. Its one-time auth key goes in /var/lib/eldar-os-hook/authkey.";
      };
      secretFile = lib.mkOption {
        type = lib.types.str;
        description = "File holding the webhook secret GitHub signs deliveries with.";
      };
      package = lib.mkOption {
        type = lib.types.package;
        default = pkgs.callPackage ../../packages/supervisor.nix { };
        description = "Package providing bin/apply-hook.";
      };
    };
  };

  config = {
    eldar-os.apply.healthChecks = lib.mkBefore [
      "systemctl is-active --quiet sshd"
      # outbound network, DNS and the deploy key all still work
      "GIT_SSH_COMMAND='ssh -i ${stateDir}/deploy-keys/staging -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=10' timeout 30 git ls-remote -q ${cfg.repo} main >/dev/null"
    ];

    systemd.services.eldar-os-apply = {
      description = "Apply eldar-os from staging main";
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      # Not enrolled yet (fresh install, CI): nothing to do, and not a failure.
      unitConfig.ConditionPathExists = "${stateDir}/deploy-keys/staging";
      # Activation must never restart or stop the apply that's running it.
      restartIfChanged = false;
      stopIfChanged = false;
      environment = {
        HOME = stateDir;
        STATE_DIR = stateDir;
        REPO = cfg.repo;
        SECRETS_REPO = cfg.secretsRepo;
        FLAKE_ATTR = cfg.flakeAttr;
        ALLOWED_SIGNERS = "${cfg.allowedSigners}";
        GPG_KEYS = "${cfg.gpgKeys}";
        HEALTH_CHECK = "${healthCheck}";
        ROLLBACK_AFTER = cfg.rollbackAfter;
      };
      serviceConfig = {
        Type = "oneshot";
        ExecStart = "${apply}/bin/eldar-os-apply";
        TimeoutStartSec = "1h";
      };
    };

    systemd.timers.eldar-os-apply = {
      wantedBy = [ "timers.target" ];
      timerConfig = {
        OnBootSec = "2min";
        OnUnitInactiveSec = "2min";
      };
    };

    systemd.tmpfiles.rules = [ "d ${stateDir} 0755 root root -" ];

    # Pinned, so fetching never trusts a host key on first use.
    programs.ssh.knownHosts."github.com".publicKey =
      "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl";

    # GitHub SSH over port 443 (its documented fallback): port 22 is blocked on
    # some networks, including this machine's at times. Same host key.
    programs.ssh.extraConfig = ''
      Host github.com
        HostName ssh.github.com
        Port 443
        HostKeyAlias github.com
    '';

    security.polkit.extraConfig = lib.mkIf (cfg.triggerUsers != [ ]) ''
      polkit.addRule(function(action, subject) {
        if (action.id == "org.freedesktop.systemd1.manage-units" &&
            action.lookup("unit") == "eldar-os-apply.service" &&
            action.lookup("verb") == "start" &&
            ${builtins.toJSON cfg.triggerUsers}.indexOf(subject.user) >= 0) {
          return polkit.Result.YES;
        }
      });
    '';

    users.users = lib.genAttrs cfg.triggerUsers (_: { extraGroups = [ "systemd-journal" ]; })
      // lib.optionalAttrs cfg.webhook.enable {
        eldar-os-hook = { isSystemUser = true; group = "eldar-os-hook"; };
      };
    users.groups = lib.mkIf cfg.webhook.enable { eldar-os-hook = { }; };
    eldar-os.apply.triggerUsers = lib.mkIf cfg.webhook.enable [ "eldar-os-hook" ];

    # GitHub webhooks over Tailscale Funnel; a signed delivery only starts an
    # apply, which fetches and checks everything itself. The timer stays as
    # the fallback for a missed delivery.
    systemd.services.eldar-os-apply-hook = lib.mkIf cfg.webhook.enable {
      description = "Start an apply when GitHub reports a push";
      wantedBy = [ "multi-user.target" ];
      wants = [ "network-online.target" ];
      after = [ "network-online.target" ];
      path = [ pkgs.systemd ];
      environment = {
        HOOK_HOSTNAME = cfg.webhook.hostname;
        HOOK_STATE_DIR = "/var/lib/eldar-os-hook";
        HOOK_SECRET_FILE = cfg.webhook.secretFile;
        HOME = "/var/lib/eldar-os-hook";
      };
      serviceConfig = {
        ExecStart = "${cfg.webhook.package}/bin/apply-hook";
        User = "eldar-os-hook";
        Group = "eldar-os-hook";
        StateDirectory = "eldar-os-hook";
        StateDirectoryMode = "0700";
        Restart = "always";
        RestartSec = 5;
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        RestrictNamespaces = true;
        LockPersonality = true;
        MemoryDenyWriteExecute = true;
        SystemCallArchitectures = "native";
        CapabilityBoundingSet = "";
      };
    };
  };
}
