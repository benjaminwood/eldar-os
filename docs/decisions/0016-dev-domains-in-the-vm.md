# 0016. Dev stacks in Eldar's VM bind low ports and own *.test

Accepted, 2026-10-22

## Context

Web projects worked on in the VM run their local stack in eldar's rootless
Docker: the admin app's `bin/dev` starts an HTTPS router (Caddy, ports 80 and
443) and a DNS server for `*.test` (port 53), both with host networking so
every slot and worktree can reach them. In rootless Docker, host networking
is the VM's own network namespace, where ports below 1024 belong to root, so
eldar can't bind them. Nothing in the VM resolved `*.test`, and the agents'
Chrome runs on rootless Docker's network (slirp4netns, host loopback
disabled), which reaches the VM only at its own address.

## Decision

Inside the VM, unprivileged processes may bind from port 53 up
(`net.ipv4.ip_unprivileged_port_start = 53`). systemd-resolved sends `*.test`
to 127.0.0.1 and everything else to the usual servers. Chrome maps `*.test` to
the VM's address with `--host-resolver-rules`, so it reaches the same router.
The app keeps real URLs (no ports), as on a developer's machine.

## Consequences

Any process in the VM can now listen on 53–1023. Only eldar runs there, and
the VM's network policy is enforced by the host at the tap (0008), so this
doesn't widen what the VM can reach or what can reach it. The VM holds one
`*.test` namespace: two stacks that both want it would collide, which bin/dev
already prevents by giving each environment its own domain. Chrome picks up
the mapping when it next restarts.
