# The supervisor

Holds the credentials agents use, decides what they may run, runs it in
Eldar's VM, and records everything. Decision record:
`docs/decisions/0009-supervisor.md`. Code: `supervisor/` (one Go module:
`server/` is the supervisor, `tui/` and `web/` are Ben's UIs, `api/` is what
they share). Nix: `modules/supervisor.nix`, configured in
`hosts/eldar-os/supervisor.nix`.

## How a command runs

1. An agent in Eldar's VM POSTs to `http://10.42.0.1:9100/exec` (the only
   host port the VM can reach) with the command, the 1Password profile, the
   credential names it needs, and its session ID and key.
2. The supervisor decides, in order: authorized cron → lockdown → policy deny
   → global bypass → policy allow → a grant → ask Ben (TUI, web UI, phone).
3. If approved, it fetches the named credentials from 1Password (cached per
   profile until cleared) and runs the command in the VM as `ben` over SSH,
   with the credentials exported in a script on stdin.
4. The result goes back to the agent and into the audit log
   (`/var/lib/supervisor/audit.jsonl`).

## Running it

| | |
|---|---|
| Service | `supervisor.service` (user `supervisor`), state in `/var/lib/supervisor` |
| Agent API | `10.42.0.1:9100` (VM link only) |
| Admin API | `/run/supervisor/admin.sock`, group `supervisor-admin` |
| TUI | `supervisor` (as a member of `supervisor-admin`) |
| Web UI | `supervisor-web.service`, its own tailnet node; first start needs a one-time auth key in `/var/lib/supervisor-web/authkey` (tagged for the UI) |
| Policy | `/var/lib/eldar-os/secrets/supervisor/policy.yaml` (private repo), re-read per request |
| Credentials config | sops secret `supervisor-env`: `OP_ACCOUNT_*_TOKEN`, `OP_PROFILE_*_ACCOUNT`, `OP_PROFILE_*_ENV` |

Develop: `scripts/go test ./...`, then `scripts/try` (the Nix build runs the
tests too).

## Agents' client: the MCP server

Claude sessions in Eldar's VM reach the supervisor through its MCP server,
`supervisor mcp` (stdio), registered for `eldar` and `ben` in `~/.claude.json`
by `claude-mcp-<user>` (`modules/eldar-vm.nix`). Tools: `supervisor_exec`
(run a command with credentials; the text runs verbatim under bash, as `ben`),
`supervisor_request_grant`, `supervisor_policy_check`, `supervisor_audit`,
`supervisor_credentials`, `supervisor_status`. It names the 1Password profiles
and the default one from `/status`, so they stay configuration
(`SUPERVISOR_DEFAULT_PROFILE` in `supervisor-env`). Each process makes a
random session key at start, which the supervisor pins to the session ID. A
session picks the server up when it starts.

## Chat, relay and Eldar's channels

Eldar, the chief-of-staff session, loads two Claude channels from the
supervisor binary (its watchdog passes them; no other session has them):
`supervisor channel chat` (Ben's chat from the web UI; `reply` and `history`
tools) and `supervisor channel relay` (events from other agents, scripts and
cron). Other sessions reach Ben with the `notify_ben` tool (in `supervisor
mcp`); scripts and cron with `supervisor notify [--origin WHO] MESSAGE`. Both
POST `/relay`; the supervisor keeps events in `relay.jsonl` until Eldar's relay
channel takes them, so nothing is lost while Eldar restarts. Eldar carries
Ben's answer back with `supervisor tell-agent PANE SESSION MESSAGE`, which
types `[Ben, via Eldar] …` into that session's herdr pane.

Only the Claude session in herdr workspace `primary`, tab `Eldar` may open the
chat or relay stream, with a per-process key pinned to its session. A channel
connecting while its stream is closed takes over the pin (a resumed Eldar is a
new process); while a stream is open, only its key is accepted.

## GitHub as Eldar's App

`GET /github/token` (agent API) returns an installation token for Eldar's
GitHub App, limited to the repos in `SUPERVISOR_GITHUB_REPOS`, made from the
App's key (`SUPERVISOR_GITHUB_APP_KEY`) and cached until ten minutes before
it expires. No approval; each new token is audited. In the VM, git uses it
through `supervisor git-credential` (system git config) and gh through
`GH_TOKEN=$(supervisor github-token)`. Why: `docs/decisions/0011-eldar-github-app.md`.

## herdr and live terminals

herdr runs in Eldar's VM as `eldar` (a user service, with a `primary`
workspace). The supervisor drives it over SSH as `eldar` for the admin UIs:
the workspace view, starting Claude sessions or shells in a new tab, closing
them, and live terminals (`herdr terminal attach … --takeover` in a PTY,
streamed over a WebSocket on the admin socket; closing it hangs up the attach).

## Not ported yet

Mac commands, AWS credentials, MCP OAuth, Arc, the herdr
approval nudges, and the secrets-dump socket used by `suben`. Requests for them get a clear "not available yet" error.
