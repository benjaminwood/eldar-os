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
2. The supervisor decides (why: `docs/decisions/0013-approvals-and-grants.md`),
   in order: authorized cron → lockdown → policy deny
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
tests too). After a UI change, `scripts/screenshots` refreshes the README's
screenshots from made-up data (`docs/screenshots.md`).

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

## AWS

A command can name an AWS profile (`awsProfile`; `aws_profile` in
`supervisor_exec`): the supervisor mints short-lived role credentials through
IAM Identity Center and exports `AWS_*` for that one command. Profiles come
from `awsConfig`, an ~/.aws/config-style file in the secrets repo. The
supervisor signs in to each SSO session itself: `supervisor aws-login` (on
eldar-os, as an admin) prints a link and code Ben approves in a browser; the
token, its rotating refresh token and the client registration stay in
`/var/lib/supervisor/aws/` (mode 600). One sign-in covers every profile of the
session and lasts until the registration expires (about 90 days) or the
refresh token stops working; then requests fail with `mint_failed`, telling
the agent to ask Ben to sign in again. Minted credentials are cached in memory
until a minute before they expire.

Policy allow rules only cover an AWS request when they name its profile (or
`*`) in `aws`, and grants cover only the AWS profiles they list.

## Approval nudges

When a session in a herdr pane is waiting on Ben (a command, a browser
window, a grant), herdr shows a notification with the "request" sound:
`Approval #12 · <session title>` and what it's for. Answering still happens in
the TUI or web UI; a failed nudge never affects the approval.

## Commands on other hosts

Agents can run commands on other machines Ben has configured, through the
supervisor: `supervisor_host_exec` (MCP), or `POST /exec` with `target` set to
the host's name. Ben's Mac (`mac`) is the first. Each host is private config:
`hosts.json` in the secrets repo (`hostsFile`) gives its name, label, a
description for agents, SSH target, pinned host key and login shell; its
private key is the sops secret `remote-hosts/<name>`, readable only by the
supervisor. The supervisor reads `hosts.json` at start.

Approval is per host: global bypass and grants for Eldar's VM never cover
another host. Only that host's grants (target = its name) and its allow list
("Always allow" on one of its approvals; `host-allow.json`) skip the prompt;
lockdown and policy denies still apply, on the command or on
`<host>: <command>`. The command travels base64-encoded and runs verbatim in
the host's login shell under a watchdog; credentials are exported in the
script on stdin, never on a command line. Admin API: `/admin/hosts/{name}/test`
and `/admin/hosts/{name}/allow` (`/admin/mac/…` is the `mac` host, as the UIs
call it). The UIs show the Mac's approvals, grants and allow list; other hosts
appear as ordinary approvals until the UIs list them.

## Browsers on other hosts

A host can have a browser agents may drive: a real, logged-in profile (Arc on
Ben's Mac first), through Playwright MCP running there. `hosts.json` gives the
host's `browser`: its name, the bridge command on the host
([arc-playwright-bridge](https://github.com/benjaminwood/arc-playwright-bridge)
for Arc), origins never driven or opened (the supervisor's web UIs), and the
host's tailnet device name. Sessions use the `browser` MCP server
(`supervisor browser`): `browser_profiles`, `browser_request_access`,
`browser_open_tab`, `browser_release`, plus Playwright's tools, relayed.

Each window is one session × one profile for some minutes, and always an
explicit decision by Ben: an approval, or a browser grant he made for that
session and profile (`host:profile`, e.g. `mac:Default`). Global bypass, eldar
and host grants never cover it. The session's MCP server holds a random secret
(only its hash is kept here); it asks with `POST /browser/request` and opens
`GET /browser/channel`, a WebSocket the supervisor pipes to `<bridge> mcp` over
SSH. The channel always opens the approved profile. `browser_run_code_unsafe`
is withheld (it could lift the blocked origins). A window ends on expiry,
revoke (`DELETE /admin/browser-grants/{id}`) or lockdown: the channel closes
(4001) and the next browser action asks again. While a host has an open
window, the web UI refuses that host's tailnet device, so a page an agent
drives can't approve anything.

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

## Telling a session its PR is live

A session that opens a PR on the staging repo calls `supervisor_watch_pr`
(`POST /watch-pr`; saved in `watches.json`). Each apply ends by reporting its
revision and outcome on the admin socket (`POST /admin/applied`, from the apply
service as root). The supervisor then asks GitHub, as the App, whether each
watched PR is merged and its merge commit is in that revision, and types an
`[eldar-os] …` notice into the session's herdr pane: applied, the apply failed
(once per revision; the watch stays for the next apply), or closed unmerged.
If the session is gone, the notice goes to Eldar through the relay.

## herdr and live terminals

herdr runs in Eldar's VM as `eldar` (a user service, with a `primary`
workspace). The supervisor drives it over SSH as `eldar` for the admin UIs:
the workspace view, starting Claude sessions or shells in a new tab, closing
them, and live terminals (`herdr terminal attach … --takeover` in a PTY,
streamed over a WebSocket on the admin socket; closing it hangs up the attach).

## Not ported yet

The secrets-dump socket used by `suben`. Not
planned for now: MCP sign-ins (OAuth). Cron auto-approval exists but is unused
(no crons are authorized).
