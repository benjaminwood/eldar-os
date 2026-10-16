# eldar-os

Ben's NixOS workstation and the future home of Eldar (the agent), described by
one flake. It replaces the Docker-compose stack in `~/projects/benjaminwood/eldar`,
which we migrate from piece by piece. Nothing here is shared with that repo;
tooling and config are brought over deliberately, redesigned where it helps.

## Intent: a reference, not a product

The repo is private today but is written as if it may be published one day,
**as an example to fork and adapt**, not something anyone runs directly from
here. So:

- **In the repo:** infrastructure, architecture, tooling, modules, scripts,
  docs: the parts others could learn from.
- **Not in the repo:** anything deeply personal: secrets and credentials (even
  encrypted ones, until we pick a secrets approach that's designed for it),
  personal data, Eldar's memory, persona and Claude sessions, contents of
  private work projects, email addresses, internal hostnames and account IDs
  beyond what the config strictly needs.
- **Machine-specific facts** (disk serial, hardware, hostname, public SSH keys)
  are fine but kept together and clearly marked, so a fork knows what to change.
- **Write for a stranger.** Comments and docs explain why, in terms someone
  without our history can follow. No references to private conversations.
- When something personal must exist for the system to work, it lives outside
  the repo (a private flake input, a secrets store, or host state) and the
  repo documents the interface, not the contents.

## Reproducibility

Every step, including the install itself, is a script or declarative config
here, documented in `docs/install.md`, with inputs pinned in `flake.lock`.
If something had to be done by hand, it's written down with the exact command.

## Working here

- Disks are referred to by serial, never `/dev/nvmeXn1` (the names have swapped
  between boots on this machine).
- Test against the dry-run VM (`scripts/vm --dry-run …`) before the real disk.
- `security.sudo.wheelNeedsPassword = false` is bootstrap-only and must be
  removed before cutover.
