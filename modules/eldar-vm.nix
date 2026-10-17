# Eldar's environment: a microVM (microvm.nix). docs/decisions/0008-eldar-microvm.md
# (how it was worked out: docs/spikes/eldar-vm.md).
#
# The host is the control plane (supervisor, secrets, approvals, the apply
# loop); the VM is where Ben and the AI work. The VM gets its own kernel and
# network, reaches the host only on the supervisor's port, and shares one
# directory with it (the workspace, over virtiofs). Inside: `eldar` is the
# unprivileged user everyone works as; `ben` is the privileged one (sudo, and
# the user the supervisor runs credentialed commands as).
{ config, lib, pkgs, ... }:
let
  cfg = config.eldar-os.eldarVm;
  hostAddr = cfg.hostAddress;
  vmAddr = cfg.address;
  supervisorPort = cfg.supervisorPort;
  vmSsh = "/var/lib/eldar-vm/ssh"; # host-made SSH material, shared read-only into the VM
  workspace = "/srv/eldar/workspace";
  eldarUid = 1500;
  workspaceGid = 1500; # same number on host and in the VM, so group perms line up
  supervisor = config.eldar-os.supervisor.package; # its `mcp` subcommand is the agents' client
in
{
  options.eldar-os.eldarVm = {
    address = lib.mkOption { type = lib.types.str; default = "10.42.0.2"; readOnly = true; };
    hostAddress = lib.mkOption { type = lib.types.str; default = "10.42.0.1"; readOnly = true; };
    supervisorPort = lib.mkOption { type = lib.types.port; default = 9100; readOnly = true; };
    vcpu = lib.mkOption {
      type = lib.types.ints.positive;
      description = "vCPUs. They're scheduled, not reserved: give Eldar all of the host's threads.";
    };
    config = lib.mkOption {
      type = lib.types.deferredModule;
      default = { };
      description = "Extra NixOS config for the VM, from the host: its users' tools, dotfiles and so on.";
    };
    memMiB = lib.mkOption {
      type = lib.types.ints.positive;
      description = ''
        Memory ceiling. Not allocated up front, and the balloon's free-page
        reporting hands memory the guest frees back to the host, so this can
        be most of the machine; leave headroom for the host itself.
      '';
    };
  };

  config = {
  # --- host side --------------------------------------------------------------

  # Owner of the shared workspace on the host. Never logs in here; it exists so
  # files Eldar writes (uid 1500 inside the VM) have a name on the host. Ben
  # gets in through the eldar-ws group.
  users.users.eldar = { uid = eldarUid; isSystemUser = true; group = "eldar-ws"; };
  users.groups.eldar-ws = { gid = workspaceGid; members = [ "ben" ]; };
  systemd.tmpfiles.rules = [ "d ${workspace} 2770 eldar eldar-ws -" ];

  # The VM's SSH host key lives on the host: a microVM's root is rebuilt on
  # every boot, and a stable key can be pinned by whoever connects (the
  # supervisor). Shared read-only, with authorized_keys/<user> for logins the
  # host grants (see modules/supervisor.nix).
  systemd.services.eldar-vm-ssh = {
    description = "SSH host key for Eldar's VM";
    wantedBy = [ "multi-user.target" ];
    before = [ "microvm-virtiofsd@eldar.service" "microvm@eldar.service" ];
    requiredBy = [ "microvm-virtiofsd@eldar.service" ];
    path = [ pkgs.openssh pkgs.coreutils ];
    serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
    script = ''
      install -d -m 0755 ${vmSsh} ${vmSsh}/authorized_keys
      [ -f ${vmSsh}/ssh_host_ed25519_key ] || ssh-keygen -q -t ed25519 -N "" -C eldar-vm -f ${vmSsh}/ssh_host_ed25519_key
      # The host's own key into the VM as root (only for eldar-vm-update; root
      # on the host is already more privileged than anything in the VM).
      [ -f ${vmSsh}/admin ] || ssh-keygen -q -t ed25519 -N "" -C eldar-vm-admin -f ${vmSsh}/admin
      chmod 0600 ${vmSsh}/admin
      install -m 0644 ${vmSsh}/admin.pub ${vmSsh}/authorized_keys/root
      echo "${vmAddr} $(cut -d' ' -f1,2 ${vmSsh}/ssh_host_ed25519_key.pub)" > ${vmSsh}/known_hosts_admin
    '';
  };

  # The VM's link: a tap device the host routes and NATs. NetworkManager keeps
  # the real NICs; systemd-networkd only handles vm-*.
  networking.networkmanager.unmanaged = [ "interface-name:vm-*" ];
  systemd.network.enable = true;
  # NetworkManager decides when the network is online; networkd only has vm-*.
  systemd.network.wait-online.enable = false;
  systemd.network.networks."30-vm-eldar" = {
    matchConfig.Name = "vm-eldar";
    networkConfig.Address = "${hostAddr}/24";
    linkConfig.RequiredForOnline = "no";
  };
  boot.kernel.sysctl."net.ipv4.ip_forward" = 1;

  networking.nftables.enable = true;
  networking.nftables.tables.eldar-vm = {
    family = "inet";
    content = ''
      # Eldar reaches the host only on the supervisor's port.
      chain input {
        type filter hook input priority filter - 1; policy accept;
        iifname "vm-eldar" ct state established,related accept
        iifname "vm-eldar" tcp dport ${toString supervisorPort} accept
        iifname "vm-eldar" icmp type echo-request accept
        iifname "vm-eldar" drop
      }
      # Out to the internet, but not to private networks (LAN, tailnet,
      # link-local metadata), and no IPv6 for now.
      chain forward {
        type filter hook forward priority filter - 1; policy accept;
        iifname "vm-eldar" ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16 } reject
        iifname "vm-eldar" meta nfproto ipv6 reject
      }
      chain postrouting {
        type nat hook postrouting priority srcnat;
        iifname "vm-eldar" oifname != "vm-eldar" masquerade
      }
    '';
  };
  # NixOS's own firewall would drop forwarded traffic from vm-eldar.
  networking.firewall.trustedInterfaces = [ "vm-eldar" ];

  microvm.autostart = [ "eldar" ];
  # Applying the host never blindly restarts Eldar's VM: Ben and the agents
  # work in there (Eldar included), and a restart ends every session.
  # eldar-vm-update below switches the VM's system in place instead, and
  # restarts it only for hardware changes.
  microvm.vms.eldar.restartIfChanged = false;

  # Bring the VM up to date on every host switch that changes it:
  #   - hardware changed (CPUs, memory, shares, network, disks — the
  #     hypervisor's arguments, store paths aside): restart the VM;
  #   - otherwise: switch its system in place, over SSH as root
  #     (switch-to-configuration test; the new system is already in the
  #     host's store, which the VM reads). Running sessions keep going.
  # A new kernel isn't hardware: it's used from the VM's next restart.
  # A failed switch fails this unit, so the apply's health check rolls the
  # host back, which runs this again with the previous system.
  systemd.services.eldar-vm-update =
    let
      guest = config.microvm.vms.eldar.config.config;
      toplevel = guest.system.build.toplevel;
      runner = guest.microvm.declaredRunner;
      dir = "/var/lib/microvms/eldar";
    in {
      description = "Bring Eldar's VM up to date (in place unless its hardware changed)";
      wantedBy = [ "multi-user.target" ];
      after = [ "eldar-vm-ssh.service" "install-microvm-eldar.service" ];
      requires = [ "eldar-vm-ssh.service" ];
      restartTriggers = [ toplevel runner ];
      path = [ pkgs.openssh pkgs.coreutils pkgs.gnused pkgs.diffutils pkgs.systemd ];
      serviceConfig = { Type = "oneshot"; RemainAfterExit = true; TimeoutStartSec = "10min"; };
      script = ''
        ln -sTf ${runner} ${dir}/current
        ln -sTf ${toplevel} ${dir}/toplevel
        if ! systemctl is-active -q microvm@eldar; then
          echo "Eldar's VM isn't running; it starts with this config"
          exit 0
        fi
        norm() { sed -E 's|/nix/store/[^ \x27"]*|<store>|g' "$1/bin/microvm-run"; }
        if ! cmp -s <(norm ${dir}/booted) <(norm ${runner}); then
          echo "Eldar's VM hardware changed; restarting it:"
          diff <(norm ${dir}/booted) <(norm ${runner}) | head -20 || true
          systemctl restart microvm@eldar
          exit 0
        fi
        ssh_vm() {
          ssh -i ${vmSsh}/admin -o BatchMode=yes -o IdentitiesOnly=yes \
            -o StrictHostKeyChecking=yes -o UserKnownHostsFile=${vmSsh}/known_hosts_admin \
            -o ConnectTimeout=5 root@${vmAddr} "$@"
        }
        for _ in $(seq 60); do ssh_vm true 2>/dev/null && break; sleep 2; done
        if [ "$(ssh_vm readlink -f /run/current-system)" = "${toplevel}" ]; then
          echo "Eldar's VM already runs ${toplevel}"
          exit 0
        fi
        echo "switching Eldar's VM in place to ${toplevel}"
        ssh_vm ${toplevel}/bin/switch-to-configuration test
      '';
    };
  # Machines without (nested) KVM, e.g. some CI runners, skip the VM rather
  # than fail.
  systemd.services."microvm@eldar".unitConfig.ConditionPathExists = "/dev/kvm";

  # --- the VM -----------------------------------------------------------------

  microvm.vms.eldar.config = { pkgs, ... }: {
    imports = [ cfg.config ];
    system.stateVersion = "26.05";
    networking.hostName = "eldar";

    microvm = {
      hypervisor = "cloud-hypervisor";
      vcpu = cfg.vcpu;
      mem = cfg.memMiB;
      balloon = true; # with free page reporting: freed guest memory goes back to the host
      interfaces = [{ type = "tap"; id = "vm-eldar"; mac = "02:00:00:00:00:01"; }];
      shares = [
        { proto = "virtiofs"; tag = "ro-store"; source = "/nix/store"; mountPoint = "/nix/.ro-store"; }
        { proto = "virtiofs"; tag = "workspace"; source = workspace; mountPoint = "/home/eldar/workspace"; posixAcl = false; }
        { proto = "virtiofs"; tag = "host-ssh"; source = vmSsh; mountPoint = "/etc/ssh/from-host"; posixAcl = false; readOnly = true; }
      ];
      # Relative images live in /var/lib/microvms/eldar on the host. Sparse:
      # they only take space as they fill. Rootless Docker's data lives in
      # eldar's home.
      volumes = [
        { image = "home.img"; mountPoint = "/home"; size = 256000; }
      ];
      # `nix shell` / `nix profile install` inside the VM land in a RAM-backed
      # layer over the host's read-only store: scratch, gone on restart.
      # Permanent tools go into this flake.
      writableStoreOverlay = "/nix/.rw-store";
    };
    fileSystems."/nix/.rw-store" = {
      fsType = "tmpfs";
      options = [ "mode=0755" "size=16G" ];
      neededForBoot = true;
    };
    nix.settings.experimental-features = [ "nix-command" "flakes" ];

    networking.useNetworkd = true;
    systemd.network.networks."10-eth" = {
      matchConfig.Type = "ether";
      networkConfig = {
        Address = "${vmAddr}/24";
        Gateway = hostAddr;
        DNS = [ "1.1.1.1" "9.9.9.9" ];
      };
    };
    users.groups.eldar-ws.gid = workspaceGid;
    # Unprivileged: no sudo, no docker group (that would be root). Changes to
    # its environment go through this flake; `nix shell` covers trying things.
    users.users.eldar = {
      isNormalUser = true;
      uid = eldarUid;
      group = "eldar-ws";
      linger = true; # its user services (rootless Docker, agents) run without a login
      openssh.authorizedKeys.keys = config.users.users.ben.openssh.authorizedKeys.keys;
    };
    # Privileged: Ben's admin identity inside the VM. sudo needs a password
    # (not set yet), so nothing gets root without Ben.
    users.users.ben = {
      isNormalUser = true;
      uid = 1000;
      extraGroups = [ "wheel" "eldar-ws" ];
      openssh.authorizedKeys.keys = config.users.users.ben.openssh.authorizedKeys.keys;
    };
    services.openssh = {
      enable = true;
      # Root only with a key, which only the host has (eldar-vm-update).
      settings = { PasswordAuthentication = false; PermitRootLogin = "prohibit-password"; };
      hostKeys = [{ path = "/etc/ssh/from-host/ssh_host_ed25519_key"; type = "ed25519"; }];
      # Logins the host grants, read through a command: sshd's StrictModes
      # rejects key files under the share (owned by microvm.nix's user), and
      # the command itself must be a root-owned file outside the group-
      # writable /nix/store.
      authorizedKeysCommand = "/etc/ssh/keys-from-host %u";
      authorizedKeysCommandUser = "nobody";
    };
    environment.etc."ssh/keys-from-host" = {
      mode = "0755";
      text = ''
        #!${pkgs.bash}/bin/bash
        f=/etc/ssh/from-host/authorized_keys/"$1"
        [ -f "$f" ] && exec ${pkgs.coreutils}/bin/cat "$f"
        exit 0
      '';
    };
    # The workspace mount creates /home/eldar before the user exists, as root;
    # put the homes right on every boot.
    systemd.tmpfiles.rules = [
      "d /home/eldar 0750 eldar eldar-ws -"
      "d /home/ben 0700 ben users -"
    ];
    # The supervisor, as agents in here reach it (PROXY_URL for clients not
    # yet moved over from the old setup).
    environment.variables = {
      SUPERVISOR_URL = "http://${hostAddr}:${toString supervisorPort}";
      PROXY_URL = "http://${hostAddr}:${toString supervisorPort}";
    };
    # Shared workspace: new files group-writable, so ben can edit them.
    environment.extraInit = "umask 002";
    # Docker runs as eldar (a container that mounts / gets eldar's files, not root).
    virtualisation.docker.rootless = { enable = true; setSocketVariable = true; };
    # Containers live under it: an update never restarts it.
    systemd.user.services.docker.restartIfChanged = false;
    # No firewall inside: the host enforces Eldar's network policy at the tap,
    # where Eldar can't change it. A guest firewall adds nothing, and NixOS's
    # (strict reverse-path filter, input rules) breaks Docker's TCP egress.
    networking.firewall.enable = false;
    environment.systemPackages = (with pkgs; [ git curl jq ]) ++ [ supervisor ];
    # Every Claude session in here gets the supervisor's MCP server
    # (supervisor_exec and friends), registered in each user's ~/.claude.json.
    # The command is the current system's binary, so new sessions pick up
    # updates. Written in place (not replaced) so the file keeps its mode, and
    # only when it differs, since running sessions write the file too.
    systemd.services = { sshd.unitConfig.RequiresMountsFor = [ "/etc/ssh/from-host" ]; } // builtins.listToAttrs (map (user: {
      name = "claude-mcp-${user}";
      value = {
        description = "Register the supervisor's MCP server for ${user}'s Claude";
        wantedBy = [ "multi-user.target" ];
        after = [ "home.mount" ];
        path = [ pkgs.jq pkgs.coreutils ];
        serviceConfig = { Type = "oneshot"; RemainAfterExit = true; User = user; };
        script = ''
          f=/home/${user}/.claude.json
          [ -s "$f" ] || echo '{}' > "$f"
          want='{"type":"stdio","command":"/run/current-system/sw/bin/supervisor","args":["mcp"],"env":{}}'
          if ! jq -e --argjson w "$want" '.mcpServers.supervisor == $w' "$f" >/dev/null; then
            new=$(jq --argjson w "$want" '.mcpServers.supervisor = $w' "$f")
            printf '%s\n' "$new" > "$f"
          fi
        '';
      };
    }) [ "eldar" "ben" ]);
  };
  };
}
