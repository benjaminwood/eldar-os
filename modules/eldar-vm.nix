# SPIKE: Eldar in its own microVM (microvm.nix). See docs/spikes/eldar-vm.md.
#
# The VM is the isolation boundary: Eldar gets its own kernel and network,
# reaches the host only on the supervisor's port, and shares exactly one
# directory with it (the workspace, over virtiofs). Inside, Eldar can be
# loosely restricted (rootful Docker), because nothing inside reaches the
# host's users, secrets or network beyond that.
{ config, lib, pkgs, ... }:
let
  hostAddr = "10.42.0.1";
  vmAddr = "10.42.0.2";
  supervisorPort = 9100;
  workspace = "/srv/eldar/workspace";
  eldarUid = 1500;
  workspaceGid = 1500; # same number on host and in the VM, so group perms line up
in
{
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
      vcpu = 4;
      mem = 4096;
      interfaces = [{ type = "tap"; id = "vm-eldar"; mac = "02:00:00:00:00:01"; }];
      shares = [
        { proto = "virtiofs"; tag = "ro-store"; source = "/nix/store"; mountPoint = "/nix/.ro-store"; }
        { proto = "virtiofs"; tag = "workspace"; source = workspace; mountPoint = "/home/eldar/workspace"; posixAcl = false; }
      ];
      # Relative images live in /var/lib/microvms/eldar on the host.
      volumes = [
        { image = "home.img"; mountPoint = "/home"; size = 20480; }
        { image = "docker.img"; mountPoint = "/var/lib/docker"; size = 40960; }
      ];
    };

    networking.useNetworkd = true;
    systemd.network.networks."10-eth" = {
      matchConfig.Type = "ether";
      networkConfig = {
        Address = "${vmAddr}/24";
        Gateway = hostAddr;
        DNS = [ "1.1.1.1" "9.9.9.9" ];
      };
    };
    # Docker's container links aren't networkd's business.
    systemd.network.networks."19-docker" = {
      matchConfig.Name = "veth*";
      linkConfig.Unmanaged = true;
    };

    users.groups.eldar-ws.gid = workspaceGid;
    users.users.eldar = {
      isNormalUser = true;
      uid = eldarUid;
      group = "eldar-ws";
      # SPIKE: root inside the VM. The VM is the boundary, so this doesn't
      # reach the host; open question for review.
      extraGroups = [ "docker" "wheel" ];
      openssh.authorizedKeys.keys = config.users.users.ben.openssh.authorizedKeys.keys;
    };
    services.openssh = {
      enable = true;
      settings = { PasswordAuthentication = false; PermitRootLogin = "no"; };
    };
    security.sudo.wheelNeedsPassword = false;
    # Shared workspace: new files group-writable, so ben can edit them.
    environment.extraInit = "umask 002";
    virtualisation.docker.enable = true;
    # No firewall inside: the host enforces Eldar's network policy at the tap,
    # where Eldar can't change it. A guest firewall adds nothing, and NixOS's
    # (strict reverse-path filter, input rules) breaks Docker's TCP egress.
    networking.firewall.enable = false;
    environment.systemPackages = with pkgs; [ git curl jq ];
  };
}
