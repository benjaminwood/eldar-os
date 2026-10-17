# 0010. Eldar's VM is updated in place, never restarted for software

Accepted, 2026-10-17

## Context

Ben and the agents work inside Eldar's VM: Claude sessions and shells in
herdr, containers, Eldar itself. Eldar is meant to change its own environment
(packages, services, config) through the PR → approve → apply loop
(0005). microvm.nix's default restarts a VM whenever its config changes, which
ends every session in it: Eldar would end itself with every change it makes.

## Decision

Applying the host never restarts Eldar's VM for a software change.

- `eldar-vm-update` (host) runs after every host switch that changes the VM's
  system or its hypervisor arguments. If the hypervisor arguments changed,
  ignoring store paths (CPUs, memory, shares, network, disks), it restarts the
  VM: those can't change live, and they're rare and deliberate. Otherwise it
  activates the new system inside the running VM, over SSH as root with a key
  only the host has (`switch-to-configuration test`). The VM reads the host's
  store, so the new system is already there.
- A new kernel or hypervisor version isn't a hardware change: it's used from
  the VM's next restart.
- Services whose definitions changed restart, as on any NixOS switch, except
  the ones that hold Ben's work: herdr (every pane lives under it) and
  rootless Docker (its containers) never restart on a switch; a new version of
  either is used from the VM's next restart. Processes running in herdr panes
  aren't services and are never touched.
- A failed in-place switch fails the unit, so the apply's health check rolls
  the host back, which switches the VM back too.

## Consequences

Eldar can add a package or a service and keep running. The VM can run an older
kernel, herdr or Docker than its config says until it next restarts;
`booted` vs `current` in `/var/lib/microvms/eldar/` shows the difference. After
a VM restart, herdr restores sessions with a plain `claude --resume`, so the
primary session's watchdog replaces Eldar's with one that has its flags
(chat channel, permissions). The host can log into the VM as root; root on the
host is already more privileged than anything in the VM.
