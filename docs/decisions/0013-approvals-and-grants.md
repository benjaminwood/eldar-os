# 0013. Each credentialed command is approved on its own, until Ben grants more

Accepted, 2026-10-17

## Context

Agents act with Ben's credentials: his GitHub, his cloud accounts, his Mac.
An agent can be wrong, confused, or steered by something it read, so what it
does with those credentials needs a human in the loop. But agents also run
hundreds of commands a day, across many sessions at once (0012), and a
yes/no on every one of them would leave Ben either drowning or rubber-stamping.
The supervisor (0009) is where this gets decided.

## Decision

**The default is one decision per command.** A command that needs a
credential, or runs on another host, waits for Ben. He sees exactly what will
run, where, which Claude session asked, which credentials it gets and why.
Approving runs that one command once; the next one asks again. An unanswered
request times out and doesn't run.

**Grants are how Ben stops being asked, and they're narrow by construction.**
A grant covers one target (commands in Eldar's VM, one other host, or
browser windows), one session (or, deliberately, all of them), and a scope:
which 1Password profile and credentials, which AWS roles, which browser
profiles. It lasts a chosen time or until Ben revokes it, and grants live in
memory, so a supervisor restart clears them all. Agents can ask for a grant
(`supervisor_request_grant`); Ben sees the scope and can narrow it before
approving. AWS is its own dimension: a grant that doesn't name AWS roles never
covers a request that mints AWS credentials.

**The order of checks puts the hard stops first:** lockdown, then the
policy's deny rules, then (for Eldar's VM) global bypass and the policy's
allow rules, then grants, then Ben. Nothing overrides lockdown or a deny
rule. Allow rules don't cover AWS unless they name the role (or all roles),
and "always allow" on an AWS approval is tied to that one role.

**Each target stands alone.** Global bypass and grants for Eldar's VM never
cover another host or a browser; each host has its own allow list and
grants, and every browser window is its own approval or browser grant.

**A grant follows a session, not a claim.** Each session pins a key the
first time it calls; a session-specific grant applies only to requests that
present that key, so another session can't borrow it by using the ID.

**Agents can't answer their own questions.** Answers come only through the
admin socket (group `supervisor-admin`, which the agent isn't in) or the web
UI, which admits only Ben's untagged tailnet devices, is locked behind a
passkey, and refuses the device whose browser an agent is driving.

## Consequences

The safe path is the default, and every exception is visible, scoped and
revocable. The cost is interruptions: Ben answers many approvals, so answering has
to be fast wherever he is (TUI, web, push notifications, the approval tray
over a terminal), and granting the right scope has to be easy enough that he
doesn't reach for global bypass instead. Grants vanishing on restart can
interrupt work; that's accepted over grants that outlive their purpose.
The mechanics are in `docs/supervisor.md`.
