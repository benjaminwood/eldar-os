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

## Not ported yet

Mac commands, AWS credentials, MCP OAuth, Arc, chat with Eldar, herdr control
and live terminals, the tmux/herdr approval nudges, and the secrets-dump socket
used by `suben`. Requests for them get a clear "not available yet" error.
