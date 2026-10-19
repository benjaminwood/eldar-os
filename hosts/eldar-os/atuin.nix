# Atuin's sync server: shell history for Ben's machines and Eldar's VM, in
# Docker on the host (docs/decisions/0014-host-docker.md). Machine-specific:
# a fork drops this file.
#
# History is encrypted on each client with the user's key; the server only
# stores ciphertext. Reachable from the host itself, Ben's tailnet devices and Eldar's
# VM, not the LAN. Registration is closed: accounts already exist.
#
# Postgres is on Docker's bridge, published on localhost only, so connections
# reach it from the bridge and need the password (the data directory trusts
# 127.0.0.1 inside the container). Atuin uses host networking so the host
# firewall alone decides who reaches it.
#
# State lives in /var/lib/atuin, never in this repo: the database in
# postgres/, and env with ATUIN_DB_URI (it holds the database password).
# docs/install.md has the commands that moved both here from the previous OS.
#
# Images are pinned by digest. Upgrading is a commit that changes them; take
# a pg_dump first (docs/install.md).
{ config, pkgs, ... }:
let
  port = 8888;
in
{
  virtualisation.oci-containers.containers = {
    atuin-postgres = {
      image = "postgres@sha256:14bfab572eec6abf65892e1db7c3ba8d41b2a2855c1b143664907d6e146eb6e1"; # 14.24 (Debian 13)
      volumes = [ "/var/lib/atuin/postgres:/var/lib/postgresql/data" ];
      ports = [ "127.0.0.1:5432:5432" ];
    };
    atuin = {
      image = "ghcr.io/atuinsh/atuin@sha256:b9a8369b4016dddcd90d23906f1d63dd883f459c6aaecec3be87cf481e8ac88d"; # 18.24.1
      # The server has been its own binary since 18.12 (`server start` before).
      # 18.18 dropped the v1 sync API: history must be in the record store.
      cmd = [ "start" ];
      environment = {
        ATUIN_HOST = "0.0.0.0";
        ATUIN_PORT = toString port;
        ATUIN_OPEN_REGISTRATION = "false";
      };
      environmentFiles = [ "/var/lib/atuin/env" ];
      dependsOn = [ "atuin-postgres" ];
      extraOptions = [ "--network=host" ];
    };
  };

  systemd.tmpfiles.rules = [
    "d /var/lib/atuin 0700 root root -"
    "d /var/lib/atuin/postgres 0700 999 999 -" # the image's postgres user
  ];

  # Ben's devices reach it as its own tailnet node, https://atuin.<tailnet>
  # (modules/tailnet-proxy.nix): never through the host's tailnet address.
  eldar-os.tailnetProxies.atuin = {
    hostname = "atuin";
    target = "http://127.0.0.1:${toString port}";
    environmentFile = config.sops.secrets.supervisor-web-env.path; # the allowed tailnet login
  };
  eldar-os.eldarVm.hostPorts = [ port ];

  eldar-os.apply.healthChecks = [
    "systemctl is-active --quiet docker-atuin-postgres docker-atuin"
    "${pkgs.curl}/bin/curl -sf --retry 10 --retry-connrefused --retry-delay 2 http://127.0.0.1:${toString port}/healthz"
  ];
}
