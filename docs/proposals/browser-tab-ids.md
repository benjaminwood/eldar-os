# Proposal: stable tab ids for browsers on other hosts

Status: considered, not built. A design to pick up later; nothing here is
implemented.

## The problem

Agents drive Ben's Arc through Playwright MCP (`docs/supervisor.md`, Browsers
on other hosts), and Playwright MCP addresses tabs only by their position in
its tab list. Ben keeps using the browser while an agent works, so positions
shift under the agent. That needs only one agent, and it bites in two places:

1. **Between listing and selecting.** The agent lists tabs, sees the page it
   wants at index 10, and calls `browser_tabs select 10`. If two tabs close in
   between, index 10 is now a different page, and the agent's next action runs
   there. A read on the wrong page is harmless; a click (say, revoking a
   credential) isn't.
2. **When Ben closes the agent's tab.** Playwright then makes the neighbouring
   tab current, silently (`_onPageClosed` in Playwright MCP's context). The
   agent's next click or evaluate lands on a tab it never chose.

Opening or closing other tabs after a select is safe: Playwright holds the
current tab by reference, not by position.

## What upstream says

- [playwright-mcp#893](https://github.com/microsoft/playwright-mcp/issues/893)
  (parallel agents fighting over one tab) was closed as a question for the
  agent, not the MCP server.
- [playwright#39703](https://github.com/microsoft/playwright/pull/39703) added a
  stable `tabId` per tab and an optional `tabId` on every tab tool. It was
  closed by a maintainer: "Don't think it is a good idea. Give it two browsers
  (two CLI sessions)." Follow-up questions went unanswered.
  [#39721](https://github.com/microsoft/playwright/pull/39721), the same change
  resubmitted, was withdrawn by its author.
- Upstream's direction is isolation by browser context
  ([#43087](https://github.com/microsoft/playwright/pull/43087)); `browser_tabs`
  on `main` still takes only `index`.
- Google's [chrome-devtools-mcp](https://github.com/ChromeDevTools/chrome-devtools-mcp)
  has this built in: each page gets a numeric id that never changes, and
  `select_page` and `close_page` take it (`pageId`).

The likely costs behind the refusal fall on a `tabId` added to every tool:
longer schemas for every user, an argument the model can get wrong, implied
per-tab isolation that dialogs, logs and the current tab don't have, and
background-tab actions that behave differently from foreground ones. The
design below avoids them by adding ids only where tabs are chosen.

## The design

A patch to Playwright MCP, pinned and applied in
[arc-playwright-bridge](https://github.com/benjaminwood/arc-playwright-bridge)
alongside its patched extension:

- **Ids.** Each tab gets a number when Playwright first sees it, never reused
  while the MCP server runs (one browser window). `browser_tabs list` shows it,
  e.g. `- [tab 17] (current) [Title](url)`, and `new` returns it.
- **Select and close by id.** Both take `tabId` and fail if that tab is gone.
  `index` keeps working for anything that still sends it.
- **No silent fallback.** When the current tab closes, there is no current tab:
  the next action fails with "your tab was closed; list tabs and select one"
  instead of acting on a neighbour.
- **No other tool changes.** Actions keep going to the current tab, which is
  now always one the agent chose by id.
- **`browser_open_tab`.** It opens tabs through AppleScript, which returns no
  id. The patched extension can report Arc's own tab id for the new tab so the
  bridge can return the matching Playwright id.

Cost: a second patch in the bridge, re-applied on every Playwright MCP
upgrade, like the extension's.

## Alternatives considered

- **Match tabs by URL** in the supervisor's relay, verifying before acting.
  Rejected: two tabs on the same URL are normal, and then it can only refuse.
- **An upstream PR.** Declined twice already; not worth a third.
- **Chrome DevTools MCP instead of Playwright MCP.** It has stable ids, but
  connects over the debugging port or launches its own browser, which a
  default Arc profile doesn't allow; it would need its own extension bridge.
- **A browser per agent**, upstream's answer. Doesn't apply: the point is
  Ben's own logged-in profile, and his own tab changes still shift indexes.
