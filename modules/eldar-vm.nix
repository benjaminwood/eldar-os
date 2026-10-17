# SPIKE: Eldar in its own microVM (microvm.nix). See docs/spikes/eldar-vm.md.
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
  hostAddr = "10.42.0.1";
  vmAddr = "10.42.0.2";
  supervisorPort = 9100;
  workspace = "/srv/eldar/workspace";
  eldarUid = 1500;
  workspaceGid = 1500; # same number on host and in the VM, so group perms line up
in
{
  options.eldar-os.eldarVm = {
    vcpu = lib.mkOption {
      type = lib.types.ints.positive;
      description = "vCPUs. They're scheduled, not reserved: give Eldar all of the host's threads.";
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

  # --- the VM -----------------------------------------------------------------

  microvm.vms.eldar.config = { pkgs, ... }: {
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
      settings = { PasswordAuthentication = false; PermitRootLogin = "no"; };
    };
    # The workspace mount creates /home/eldar before the user exists, as root;
    # put the homes right on every boot.
    systemd.tmpfiles.rules = [
      "d /home/eldar 0750 eldar eldar-ws -"
      "d /home/ben 0700 ben users -"
    ];
    # Shared workspace: new files group-writable, so ben can edit them.
    environment.extraInit = "umask 002";
    # Docker runs as eldar (a container that mounts / gets eldar's files, not root).
    virtualisation.docker.rootless = { enable = true; setSocketVariable = true; };
    # No firewall inside: the host enforces Eldar's network policy at the tap,
    # where Eldar can't change it. A guest firewall adds nothing, and NixOS's
    # (strict reverse-path filter, input rules) breaks Docker's TCP egress.
    networking.firewall.enable = false;
    environment.systemPackages = with pkgs; [ git curl jq ];
  };
  };
}
