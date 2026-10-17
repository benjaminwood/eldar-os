# Screenshots

The README shows the supervisor's web UI and TUI. Those screenshots are part
of the repo, not a one-off: showing the work is part of publishing it. They're
generated, never hand-taken, so they can be refreshed whenever a UI changes
and never show anyone's real data.

```
scripts/screenshots        # writes docs/screenshots/*.png (about a minute)
```

Needs Docker and tmux on the admin host. Commit the PNGs with the UI change
that changed them.

## How it works

- **Made-up data.** `supervisor/cmd/demo-admin` serves an invented state
  (approvals, sessions, grants, chat, audit log) on an admin socket. Both UIs
  talk to it exactly as they talk to the real supervisor. Times are relative
  to each request, so countdowns never run out.
- **Web UI.** `supervisor web` in its loopback development mode (no tailnet, no
  passkey lock) points at the demo socket, and Playwright (pinned image)
  shoots it at phone and desktop sizes.
- **TUI.** The real TUI runs in a detached 120×34 tmux. Each screen is captured
  with its colours and replayed into xterm.js (the copy the web UI vendors),
  which Playwright shoots. A real terminal emulator renders the TUI faithfully.
  The full JetBrains Mono is loaded because Google Fonts' subset has no box
  drawing, and the Unicode 11 width table matches the TUI's for emoji.

## Keeping it honest

- The data is invented. Use documentation IP ranges (RFC 5737: 192.0.2/24,
  198.51.100/24, 203.0.113/24) and `example.com` addresses. A test in
  `demo-admin` fails the build otherwise. Never paste real state in.
- `demo-admin` is built and tested with the supervisor (`go test ./...` in the
  package's check), so a change to the admin API that the demo hasn't caught
  up with fails the build rather than the next screenshot run.
- A new feature worth showing gets demo data (a new field, a new approval
  kind) in the same change, and a fresh run of `scripts/screenshots`.
- Check every image before committing: screenshots are published with
  everything else (see [publishing.md](publishing.md)).
