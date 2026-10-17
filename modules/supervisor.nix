# The supervisor: holds the agent's credentials, decides what it may run
# (policy, grants, Ben's approvals) and runs it in Eldar's VM as the
# privileged user there, over SSH. docs/decisions/0008-eldar-microvm.md
{ config, lib, pkgs, ... }:
let
  cfg = config.eldar-os.supervisor;
  vm = config.eldar-os.eldarVm;
  state = "/var/lib/supervisor";
  vmSsh = "/var/lib/eldar-vm/ssh"; # shared read-only into the VM
  opBeta = pkgs.callPackage ../packages/op-beta.nix { };
in
{
  options.eldar-os.supervisor = {
    enable = lib.mkEnableOption "the supervisor";
    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.callPackage ../packages/supervisor.nix { };
    };
    policyFile = lib.mkOption {
      type = lib.types.str;
      description = "Policy YAML (re-read on every request). Private, so not in this repo.";
    };
    environmentFile = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Credentials config: OP_ACCOUNT_*_TOKEN, OP_PROFILE_*_ACCOUNT/_ENV.";
    };
    admins = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Users who may use the admin socket (TUI, web UI).";
    };
    web = {
      enable = lib.mkEnableOption "the web UI on the tailnet (its own node, via tsnet)";
      hostname = lib.mkOption {
        type = lib.types.str;
        default = "supervisor";
        description = "Tailnet machine name. Its auth key goes in /var/lib/supervisor-web/authkey once.";
      };
      environmentFile = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "WEB_ALLOWED_USER (tailnet login allowed in), WEB_BROWSER_DEVICE, …";
      };
    };
    hostsFile = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        hosts.json: other machines agents may run commands on (name, label,
        description, SSH target, pinned host key, login shell). Private, so not
        in this repo. Each host's private key goes in the sops secret
        `remote-hosts/<name>`, owned by the supervisor. Read at start: restart
        the supervisor after changing it.
      '';
    };
    runAs = lib.mkOption {
      type = lib.types.str;
      default = "ben";
      description = "User commands run as in Eldar's VM (privileged there; not the agent).";
    };
  };

  config = lib.mkIf cfg.enable {
    nixpkgs.config.allowUnfreePredicate = pkg: builtins.elem (lib.getName pkg) [ "op" ];

    users.users.supervisor = { isSystemUser = true; group = "supervisor"; home = state; };
    users.groups.supervisor = { };
    users.groups.supervisor-admin.members = cfg.admins;
    environment.systemPackages = [ cfg.package ]; # `supervisor` = the TUI

    # The supervisor's SSH key into Eldar's VM (made once; only it can read
    # it), authorized there for runAs via the VM's host-key share, and the
    # VM's host key pinned.
    systemd.services.supervisor-keys = {
      description = "SSH key from the supervisor into Eldar's VM";
      wantedBy = [ "multi-user.target" ];
      after = [ "eldar-vm-ssh.service" ];
      requires = [ "eldar-vm-ssh.service" ];
      # Before the VM's share is served, so the VM sees authorized_keys.
      before = [ "supervisor.service" "microvm-virtiofsd@eldar.service" "microvm@eldar.service" ];
      requiredBy = [ "supervisor.service" "microvm-virtiofsd@eldar.service" ];
      path = [ pkgs.openssh pkgs.coreutils ];
      serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
      script = ''
        install -d -m 0700 -o supervisor -g supervisor ${state} ${state}/ssh
        [ -f ${state}/ssh/id_ed25519 ] || ssh-keygen -q -t ed25519 -N "" -C supervisor -f ${state}/ssh/id_ed25519
        chown supervisor:supervisor ${state}/ssh/id_ed25519 ${state}/ssh/id_ed25519.pub
        # runAs runs approved commands; the agent user is how herdr is driven
        # for the admin UIs (workspaces, sessions, live terminals).
        install -m 0644 ${state}/ssh/id_ed25519.pub ${vmSsh}/authorized_keys/${cfg.runAs}
        install -m 0644 ${state}/ssh/id_ed25519.pub ${vmSsh}/authorized_keys/eldar
        echo "${vm.address} $(cut -d' ' -f1,2 ${vmSsh}/ssh_host_ed25519_key.pub)" > ${state}/ssh/known_hosts
        chown supervisor:supervisor ${state}/ssh/known_hosts
      '';
    };

    users.users.supervisor-web = lib.mkIf cfg.web.enable { isSystemUser = true; group = "supervisor-web"; };
    users.groups.supervisor-web = lib.mkIf cfg.web.enable { };

    # The web UI: joins the tailnet as its own node and relays to the admin
    # socket. Admits only the allowed tailnet user's untagged devices, behind
    # a passkey check.
    systemd.services.supervisor-web = lib.mkIf cfg.web.enable {
      description = "Supervisor web UI (tailnet)";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" "supervisor.service" ];
      wants = [ "network-online.target" ];
      environment = {
        SUPERVISOR_SOCKET = "/run/supervisor/admin.sock";
        WEB_HOSTNAME = cfg.web.hostname;
        WEB_STATE_DIR = "/var/lib/supervisor-web";
        TS_AUTHKEY_FILE = "/var/lib/supervisor-web/authkey";
        HOME = "/var/lib/supervisor-web";
      };
      serviceConfig = {
        ExecStart = "${lib.getExe cfg.package} web --socket /run/supervisor/admin.sock";
        User = "supervisor-web";
        Group = "supervisor-web";
        SupplementaryGroups = [ "supervisor-admin" ];
        EnvironmentFile = lib.optional (cfg.web.environmentFile != null) "-${cfg.web.environmentFile}";
        StateDirectory = "supervisor-web";
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
        RestrictSUIDSGID = true;
        LockPersonality = true;
      };
    };

    systemd.services.supervisor = {
      description = "Supervisor: credentials, approvals, and running the agent's commands";
      wantedBy = [ "multi-user.target" ];
      after = [ "network.target" "supervisor-keys.service" ];
      path = [ pkgs.openssh opBeta ];
      environment = {
        SUPERVISOR_LISTEN = "${vm.hostAddress}:${toString vm.supervisorPort}";
        SUPERVISOR_ADMIN_SOCKET = "/run/supervisor/admin.sock";
        SUPERVISOR_ADMIN_GROUP = "supervisor-admin";
        SUPERVISOR_STATE_DIR = state;
        SUPERVISOR_POLICY = cfg.policyFile;
        SUPERVISOR_AGENT_HOST = vm.address;
        SUPERVISOR_RUN_USER = cfg.runAs;
        SUPERVISOR_AGENT_USER = "eldar";
        SUPERVISOR_HOST_KEYS = "/run/secrets/remote-hosts";
        HOME = state;
      } // lib.optionalAttrs (cfg.hostsFile != null) {
        SUPERVISOR_HOSTS = cfg.hostsFile;
      };
      serviceConfig = {
        ExecStart = "${lib.getExe cfg.package} serve";
        User = "supervisor";
        Group = "supervisor";
        SupplementaryGroups = [ "supervisor-admin" ]; # to hand the admin socket to the group
        EnvironmentFile = lib.optional (cfg.environmentFile != null) "-${cfg.environmentFile}";
        StateDirectory = "supervisor";
        StateDirectoryMode = "0700";
        RuntimeDirectory = "supervisor";
        RuntimeDirectoryMode = "0755";
        Restart = "on-failure";
        RestartSec = 2;
        # It holds every credential: give it nothing else.
        NoNewPrivileges = true;
        ProtectSystem = "strict";
        ProtectHome = true;
        PrivateTmp = true;
        PrivateDevices = true;
        ProtectKernelTunables = true;
        ProtectKernelModules = true;
        ProtectControlGroups = true;
        RestrictSUIDSGID = true;
        LockPersonality = true;
        RestrictAddressFamilies = [ "AF_UNIX" "AF_INET" "AF_INET6" ];
      };
    };
  };
}
