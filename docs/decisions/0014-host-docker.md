# 0014. Household services run in rootful Docker on the host

Accepted, 2026-10-19

## Context

Some services belong to the house, not to Eldar: Home Assistant first. They
need the home network directly (mDNS and broadcast discovery, a HomeKit
bridge), and they shouldn't stop working when Eldar's VM is busy or being
rebuilt. Eldar's VM is the wrong place: its network is filtered at the host
on purpose (0008), and anything there is reachable by the agents.

These services ship as container images and expect Docker, and NixOS's own
Home Assistant module would mean replacing a working setup's packaging along
with its machine.

## Decision

The host runs Docker, rootful, with containers declared in Nix through
`virtualisation.oci-containers` and images pinned by tag. Nobody is in the
`docker` group; the admin uses `sudo`. Docker uses its nftables firewall
backend so it doesn't take over the host's forwarding policy, which Eldar's
VM depends on. Each service keeps its state under `/var/lib/<service>`,
outside the repo.

## Consequences

The Docker daemon is root, so a container escape is a host compromise. The
services that run this way are ones we'd trust on the LAN anyway, and agents
have no path to the daemon: they run in the VM and the socket is root-only.
Rootless Docker would narrow this but loses host networking's raw access to
the LAN, which these services need. Images upgrade only by a commit that
changes the tag, so updates are deliberate and roll back with the system,
though a database migration inside a service's state doesn't.
