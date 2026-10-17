# The promo video

`docs/promo/promo.mp4` (1080p, about 55 s) and `docs/promo/promo.gif` (the
README's) show the supervisor's web UI and TUI in use. Like the screenshots
(`docs/screenshots.md`), they're generated, never hand-edited, so they can be
remade whenever the UIs change.

```
scripts/promo           # record, compose, make the GIF (about 5 minutes)
scripts/promo record    # just re-record the clips
scripts/promo render    # just re-compose from the clips already recorded
scripts/promo studio    # Remotion's editor on http://localhost:3000, for timing work
```

Needs Docker. Everything runs in the promo image (`promo/Dockerfile`: the
pinned Playwright image plus ffmpeg, tmux and gifski).

## True to the software

The rule: everything on screen is the real software doing what it really
does. Only the data is made up.

- **The UIs are real.** The actual web UI (the supervisor's loopback
  development mode) and the actual TUI (in tmux), built from this tree.
- **The interactions are real.** Playwright taps the web UI's real buttons
  and types into its real chat box; the TUI gets real keystrokes. The dot
  that marks each tap is drawn on the page so viewers can follow along.
- **The backend is a mock with the real semantics.** `supervisor/cmd/demo-admin`
  serves the admin API from invented state. A decision resolves its approval
  and is audited; a grant is matched against later requests the way
  `server/grants.go` matches them (target, session, profile, credentials,
  AWS); its tests pin the behaviors the video shows. Its control port
  (`-control`) plays the agents: filing requests, Eldar replying.
- **Captions only say what's true,** and what the footage shows. When the
  software changes what a caption claims, change the caption.

## How it's made

1. **Record** (`promo/record.mjs`). One scripted story, in order: approvals on
   a phone, a grant in the TUI, a Claude session's terminal on the phone,
   chat, the audit log. The phone is captured through Chrome's DevTools
   screencast (sharper than Playwright's video); the TUI is sampled from tmux
   as it changes and replayed into the web UI's own xterm.js. Each scene
   becomes `promo/public/clips/<scene>.mp4`, and `manifest.json` records its
   length and the moments the captions sync to.
2. **Compose** (`promo/src/`, [Remotion](https://www.remotion.dev)). The clips
   in a phone frame or a terminal window, keyframed zooms, captions that
   spring in at the recorded moments, transitions, a title, the
   merge-to-live card (decision 0012) and an outro. Tokyo Night and the UIs'
   own fonts.
3. **The GIF.** A second, half-scale render with the ambient motion (the
   drifting light, the floating phone) turned off (`still` in
   `promo/src/ui.tsx`): that motion changes every pixel of every frame, which
   a GIF can only store whole. Then gifski at 800 px, 12 fps.

## Licensing

Remotion is free for individuals and companies of up to three people; larger
companies need a [company license](https://www.remotion.dev/license) to use
this pipeline. gifski (AGPL) is only run as a tool, never distributed. Inter
and JetBrains Mono are OFL.
