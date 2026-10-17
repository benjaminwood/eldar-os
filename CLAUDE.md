# eldar-os

Ben's NixOS workstation and the future home of Eldar (the agent), described by
one flake. It replaces the Docker-compose stack in `~/projects/benjaminwood/eldar`,
which we migrate from piece by piece. Nothing here is shared with that repo;
tooling and config are brought over deliberately, redesigned where it helps.

## Intent: a reference, not a product

This repo is public (`benjaminwood/eldar-os`), **as an example to fork and
adapt**, not something anyone runs directly from here. Work happens in the
private staging repo (`eldar-os-staging`, remote `origin`), which Ben
publishes from with `scripts/publish` (see `docs/publishing.md`). Staging is
published eventually, in full, so treat every commit as public. So:

- **In the repo:** infrastructure, architecture, tooling, modules, scripts,
  docs: the parts others could learn from.
- **Not in the repo:** anything deeply personal: secrets and credentials (even
  encrypted ones; they go in the private `eldar-os-secrets` repo, see
  `docs/decisions/0004-secrets.md`), personal data, Eldar's memory, persona and Claude sessions, contents of
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

## Decisions

Choices that shape the system get a short record in `docs/decisions/`
(see 0001): what, why, and the cost, in a few paragraphs a person reads in a
minute. Check them before changing anything security-related, link to them
from code that exists because of one, and supersede rather than rewrite.

## Changing the system

The machine applies staging `main` itself, and only if every commit it gained
(first-parent) is signed by an allowed key (`docs/decisions/0005-apply-loop.md`). Claude in Ben's admin session
may push to staging `main` directly; commits are signed with
`~/.ssh/eldar-os-signing` (configured in this repo's git config).

1. Edit, then `scripts/try` (~7 s): activates the working tree in the VM,
   uncommitted, with `nixos-rebuild test`; the boot default stays the last
   applied main. Iterate here. (`scripts/check` builds and diffs without
   activating; `scripts/check --lock` after adding or bumping an input.)
2. When it's right, commit (signed automatically) and `scripts/apply` (~11 s):
   pushes and streams the machine's own apply. A failed health check rolls
   back on its own.
3. Small, frequent commits are fine; they're rewritable until published.
   Ben's preferences live in his dotfiles repo (decision 0006): try them
   with `DOTFILES=~/projects/benjaminwood/dotfiles scripts/try`, push the
   dotfiles commit, then `scripts/check --lock` to bump the pin.
4. Don't stack blocking waits on long operations; check state directly.
5. Never restart Eldar's VM or herdr to apply a change: changes reach the VM
   in place (decision 0010). Services that hold work (herdr, rootless Docker)
   must keep `restartIfChanged = false`.

## Working here

- Push to `origin` (staging) only. Never push to `public`; publishing is
  Ben's call, via `scripts/publish`.
- If something that shouldn't be public gets committed, rewrite staging
  history before it's published. A follow-up commit doesn't remove it.
- Any secret generated for this system that would need restoring (keys,
  keyfiles, tokens) is backed up right away to the admin's 1Password vault,
  with a note on what it is, where it lives and how to replace it. Values go
  through pipes, never into output or argv.
- Showing the work is part of publishing it. When a change alters what the
  TUI or web UI looks like, extend the demo data if needed
  (`supervisor/cmd/demo-admin`), run `scripts/screenshots`, check the images,
  and commit them with the change (`docs/screenshots.md`). The README's
  feature list and diagram should keep up with what the system does.
- Disks are referred to by serial, never `/dev/nvmeXn1` (the names have swapped
  between boots on this machine).
- Test against the dry-run VM (`scripts/vm --dry-run …`) before the real disk.
- `security.sudo.wheelNeedsPassword = false` stays until cutover (Ben's call),
  then goes; the narrow `scripts/try` rule stays.
