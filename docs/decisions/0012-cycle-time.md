# 0012. An agent waits only for Ben's approval

Accepted, 2026-10-17

## Context

Agents work fastest when nothing stands between them and what they need.
When Eldar needs a new tool or a config change, the change has to go
through Ben (0005, 0011). That approval is the one wait worth having:
everything else (queues, polling, CI, restarts) is time the agent spends
blocked for no reason, and it adds up over every change.

## Decision

Cycle time is a value: an agent proposes a change and waits only as long as
Ben takes to approve it, plus the time to build and activate it. No loops,
polls or checks in between. When we design how a change flows, the
question is "what does the agent wait for, and does it have to?"

What carries it out today:

- **The apply starts on a GitHub webhook**, not a timer. A small receiver
  (`apply-hook`) takes GitHub's push deliveries over Tailscale Funnel, checks
  their signature, and starts the apply. The delivery is only a ping: nothing
  in it is trusted or used, and the apply fetches from GitHub and verifies
  signatures itself (0005). Both the staging and secrets repos send them. The
  2-minute timer stays as the fallback for a missed delivery.
- **No CI on pull requests.** The machine builds before it applies and changes
  nothing if the build fails, so a PR build only delays the merge. CI builds
  `main` and runs the install end to end.
- **Applies never restart Eldar's VM** for software (0010), so an applied
  change is there for the agent at once, in the session that asked for it.
- **Fast admin loop**: `scripts/try` activates a working tree in seconds
  without committing; `scripts/apply` pushes and applies in about 11 s.

## Consequences

A merge is live in about the time its build takes. The receiver is reachable
from the internet; its only power is starting an apply that would run
anyway, and the shared secret keeps strangers from even that. New steps in
the change flow have to justify any wait they add.
