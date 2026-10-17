// Supervisor web UI. Talks only to this server's /api, which relays to the
// proxy's admin API — the same interface the TUI uses.
"use strict";

const $ = (s, el = document) => el.querySelector(s);
const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const short = (id) => (id || "").split("-")[0].slice(0, 8);
const plural = (n, w, s = "s") => `${n} ${w}${n === 1 ? "" : s}`;

const S = {
  data: null,           // api.State
  sessions: [],         // view.Session[]
  tab: "approvals",
  marks: new Set(),     // pending IDs marked for bulk action
  open: new Set(),      // pending IDs with the full command expanded
  extra: new Set(),     // pending IDs with the extra-actions row shown
  seen: new Set(),
  primed: false,
  live: false,
  downMsg: "",
  audit: [],
  auditFilter: "all",
  sheet: null,          // { kind, ... }
  ed: null,             // grant editor state
  html: {},             // last rendered HTML per region, to skip no-op renders
};
try { S.tab = localStorage.getItem("tab") || "approvals"; } catch {}
if (S.tab === "connections") S.tab = "settings";

// ── API ────────────────────────────────────────────────────────────────
async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { "X-Supervisor": "1", ...(body ? { "Content-Type": "application/json" } : {}) },
    body: body ? JSON.stringify(body) : undefined,
  });
  const txt = await res.text();
  let json = {};
  try { json = txt ? JSON.parse(txt) : {}; } catch { json = { error: txt.trim() }; }
  if (res.status === 401 && json.locked) lockApp();
  if (!res.ok) throw new Error(json.error || res.statusText);
  return json;
}

function toast(msg, kind = "ok", ms = 3200) {
  const el = document.createElement("div");
  el.className = `toast ${kind}`;
  el.textContent = msg;
  $("#toasts").append(el);
  setTimeout(() => { el.style.transition = "opacity .3s"; el.style.opacity = "0"; setTimeout(() => el.remove(), 300); }, ms);
}

async function act(promise, ok) {
  try { await promise; if (ok) toast(ok); return true; }
  catch (e) { toast("✗ " + e.message, "err", 5000); return false; }
}

// ── live state ─────────────────────────────────────────────────────────
function connect() {
  const es = new EventSource("/api/events");
  es.addEventListener("state", (e) => { S.live = true; S.downMsg = ""; apply(JSON.parse(e.data)); });
  es.addEventListener("down", (e) => { S.live = false; S.downMsg = JSON.parse(e.data).error; renderChrome(); renderMain(); });
  es.addEventListener("locked", () => { es.close(); lockApp(); });
  es.onerror = () => {
    S.live = false;
    renderChrome();
    // A refused stream (401) isn't retried by the browser; find out why.
    if (es.readyState === EventSource.CLOSED) checkLock().then((ok) => { if (ok) setTimeout(connect, 2000); });
  };
}

// ── lock: Face ID gates the whole app ─────────────────────────────────
// The server refuses every /api call until this device passes a passkey check.
// Locking reloads the page, so nothing seen before stays in the DOM.
const LOCK = { started: false, kind: "unlock" };

async function checkLock() {
  const st = await window.eldarTerm.status().catch((e) => ({ error: e.message }));
  if (st.unlocked) return true;
  lockApp(st);
  return false;
}

function lockApp(st) {
  if (LOCK.started) { location.reload(); return; }
  document.body.classList.add("locked");
  let el = $("#lock");
  if (!el) {
    el = document.createElement("div");
    el.id = "lock";
    el.className = "lock";
    document.body.append(el);
  }
  const fill = (st) => {
    const ok = st && st.available && window.PublicKeyCredential;
    LOCK.kind = st?.registered ? "unlock" : "register";
    el.innerHTML = `<div class="lock-box"><div class="big-sigil">✦</div><h2>Eldar is locked</h2>
      <p>${!ok ? esc(st?.error || (window.PublicKeyCredential ? "Face ID needs the tailnet HTTPS address." : "This browser doesn't support passkeys."))
        : LOCK.kind === "register" ? "Set up Face ID for this app. The passkey stays in your keychain; the server keeps only its public half."
        : "Confirm it's you to open Eldar. It locks again after 15 minutes without use."}</p>
      ${ok ? `<button class="btn btn-accent lock-btn" data-action="unlock">${LOCK.kind === "register" ? "Set up Face ID" : "Unlock with Face ID"}</button>` : ""}</div>`;
    if (ok) window.eldarTerm.prepare(LOCK.kind).catch(() => {});
  };
  if (st) fill(st); else window.eldarTerm.status().then(fill, (e) => fill({ error: e.message }));
}

async function unlock(btn) {
  btn.disabled = true;
  try {
    await window.eldarTerm.ceremony(LOCK.kind);
    document.body.classList.remove("locked");
    $("#lock")?.remove();
    start();
  } catch (e) {
    btn.disabled = false;
    toast("✗ " + (e.name === "NotAllowedError" ? "Face ID was cancelled or timed out." : e.message), "err", 5000);
    window.eldarTerm.prepare(LOCK.kind).catch(() => {});
  }
}

function start() {
  if (LOCK.started) return;
  LOCK.started = true;
  render();
  connect();
  api("GET", "/api/state").then((p) => { S.live = true; apply(p); focusHash(); }).catch((e) => { S.downMsg = e.message; renderMain(); });
}

document.addEventListener("visibilitychange", () => { if (!document.hidden && LOCK.started) checkLock(); });

function apply(p) {
  S.data = p.state;
  S.sessions = p.sessions || [];
  const live = new Set(S.data.pending.map((x) => x.id));
  const fresh = S.data.pending.filter((x) => !S.seen.has(x.id));
  fresh.forEach((x) => S.seen.add(x.id));
  for (const set of [S.marks, S.open, S.extra]) for (const id of [...set]) if (!live.has(id)) set.delete(id);
  if (S.primed && fresh.length) {
    if (navigator.vibrate) navigator.vibrate([60, 40, 60]);
    if (S.tab !== "approvals") toast(`● New approval #${fresh[fresh.length - 1].id}`, "ok");
  }
  S.primed = true;
  render();
  if (S.tab === "activity") loadAuditSoon();
  if (S.sheet && ["session", "hpane", "close-confirm"].includes(S.sheet.kind)) renderSheet();
  window.eldarTerm?.onState();
  window.eldarChat?.onState();
}

// ── helpers over state ─────────────────────────────────────────────────
const sessionById = (id) => S.sessions.find((s) => s.id === id);
const sessionTitle = (id) => (id ? sessionById(id)?.title || `session ${short(id)}` : "unknown session");
const tilde = (p) => (p || "").replace(/^\/home\/eldar\//, "~/");
// AWS profiles are "<account>.<role>"; admin roles and production-looking accounts are flagged.
const awsLabel = (p) => { const i = (p || "").lastIndexOf("."); return i > 0 ? `${p.slice(0, i)} · ${p.slice(i + 1)}` : p; };
const awsRisky = (p) => /admin|prod|security/i.test(p || "");
const awsTag = (p) => `<span class="tag${awsRisky(p) ? " risky" : ""}">${awsRisky(p) ? "⚠ " : ""}AWS ${esc(awsLabel(p))}</span>`;
const isEldarGrant = (g) => !g.target || g.target === "eldar";
// Remote hosts agents may run commands on (Ben's Mac is "mac"); a host's name
// is also the target of its requests and grants, and the prefix of their
// audit actions ("mac: brew list").
const hostOf = (t) => (S.data?.hosts || []).find((h) => h.name === t);
const isHost = (t) => !!t && t !== "eldar" && t !== "browser" && !!hostOf(t);
const hostLabel = (t) => hostOf(t)?.label || t;
// An audit entry's host, if it ran on one, and its command without the prefix.
const entryHost = (e) => (isHost(e.target) ? e.target : "");
const entryCommand = (e) => { const h = entryHost(e); return h && e.action.startsWith(h + ": ") ? e.action.slice(h.length + 2) : e.action; };
// A grant that covers everything its target can ask for.
const fullGrant = (g) => g.target === "browser" ? g.arc === "*"
  : isHost(g.target) ? g.profile === "*" && g.credentials === "*"
  : g.profile === "*" && g.credentials === "*" && g.aws === "*";
const awsScope = (aws) => aws === "*" ? "any AWS role" : (aws || []).length ? aws.map(awsTag).join("") : "no AWS";

function dur(ms) {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 10) return `${m}:${String(s % 60).padStart(2, "0")}`;
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
  return `${Math.floor(h / 24)}d`;
}
const urgency = (sec) => (sec < 20 ? "var(--red)" : sec < 60 ? "var(--orange)" : "var(--mint)");
const kindLabel = (k, target) => (k === "browser" ? `BROWSER ON ${hostLabel(target).toUpperCase()}` : isHost(target) ? `ON ${hostLabel(target).toUpperCase()}` : k === "grant_request" ? "GRANT REQUEST" : "COMMAND");
const kindClass = (p) => (p.kind === "browser" || p.kind === "grant_request" ? "k-bypass" : isHost(p.target) ? "k-mac" : `k-${p.kind}`);
const hostGrants = (h) => (S.data?.grants || []).filter((g) => g.target === h);
const hostBypassIds = (h) => hostGrants(h).filter((g) => g.session === "*" && g.profile === "*" && g.credentials === "*").map((g) => g.id);
const levelIcon = { full: "⚡", partial: "◐", none: "○", locked: "■" };
const levelText = { full: "BYPASS", partial: "partial", none: "asks", locked: "locked" };

// Fill every time-relative label. Runs every second and after each render, so
// rendered HTML stays stable between state changes.
function tick() {
  const now = Date.now();
  document.querySelectorAll("[data-exp]").forEach((el) => {
    const left = Number(el.dataset.exp) - now;
    const sec = left / 1000;
    if (el.classList.contains("bar-fill")) {
      const total = Number(el.dataset.total) || 1;
      el.style.width = `${Math.max(0, Math.min(100, (left / total) * 100))}%`;
      el.style.background = urgency(sec);
    } else {
      el.textContent = left <= 0 ? "expired" : (el.dataset.prefix || "") + dur(left) + (el.dataset.suffix || "");
      if (el.classList.contains("countdown")) el.style.color = urgency(sec);
    }
  });
  document.querySelectorAll("[data-ago]").forEach((el) => {
    const t = Number(el.dataset.ago);
    if (!t) { el.textContent = "never"; return; }
    const d = now - t;
    el.textContent = d < 5000 ? "just now" : `${dur(d)} ago`;
  });
  if (S.data?.modeExpiresAt) renderChrome();
}

// ── rendering ──────────────────────────────────────────────────────────
function setHTML(el, key, html) {
  if (S.html[key] === html) return false;
  S.html[key] = html;
  el.innerHTML = html;
  return true;
}

function render() {
  renderChrome();
  renderMain();
  tick();
}

function renderChrome() {
  const d = S.data;
  $("#conn").className = "conn" + (S.live ? " live" : "");
  $("#conn").title = S.live ? "Live" : "Reconnecting… " + (S.downMsg || "");
  const navTab = S.tab === "activity" ? "settings" : S.tab;
  document.querySelectorAll("#tabs button").forEach((b) => b.classList.toggle("active", b.dataset.tab === navTab));
  if (!d) return;
  const pill = $("#mode-pill");
  if (d.mode === "bypass") {
    pill.className = "mode-pill mode-bypass";
    pill.textContent = "⚡ BYPASS " + (d.modeExpiresAt ? dur(d.modeExpiresAt - Date.now()) : "∞");
  } else if (d.mode === "lockdown") {
    pill.className = "mode-pill mode-lockdown";
    pill.textContent = "■ LOCKDOWN";
  } else {
    pill.className = "mode-pill mode-normal";
    pill.textContent = "● NORMAL";
  }
  $("#bell").classList.toggle("on", !!d.settings?.mobilePush);
  $("#bell").title = d.settings?.mobilePush ? "Mobile push is on" : "Mobile push is off";
  const n = d.pending.length;
  $("#count-approvals").textContent = n || "";
  $("#count-sessions").textContent = S.sessions.length || "";
  const g = d.grants.length + (d.browserGrants || []).length;
  $("#count-grants").textContent = g || "";
  $("#count-settings").textContent = d.mcp?.pending?.length || "";
  $("#count-chat").textContent = d.chat?.unread || "";
  const unread = n + (d.chat?.unread || 0);
  document.title = unread ? `(${unread}) Eldar` : "Eldar";
}

// The Browser tab: the agents' own browser (the VM's Chrome) streamed from
// its own tailnet origin, in a frame created once and kept while you switch
// tabs. Being cross-origin, nothing on that page can reach this UI's session.
function showStream(on) {
  const el = $("#stream");
  if (!el) return;
  if (on && !el.firstChild) {
    el.innerHTML = `<iframe title="Agents' browser" allow="clipboard-read; clipboard-write; fullscreen; autoplay"></iframe>
      <a class="stream-pop" href="${esc(el.dataset.src)}" target="_blank" rel="noopener">Open in its own window ↗</a>`;
    el.firstChild.src = el.dataset.src;
  }
  el.hidden = !on;
  document.body.classList.toggle("stream-open", on);
}

function renderMain() {
  const main = $("#main");
  const chat = S.tab === "chat";
  const browser = S.tab === "browser" && !!$("#stream");
  main.hidden = chat || browser;
  showStream(browser);
  if (chat) { window.eldarChat?.show(); renderBulk(); return; }
  window.eldarChat?.hide();
  if (browser) { renderBulk(); return; }
  let html;
  if (!S.data) {
    html = `<div class="empty"><div class="big-sigil">✦</div><h2>Connecting…</h2><p>${esc(S.downMsg || "Reaching the supervisor")}</p></div>`;
  } else {
    html = banner() + ({ approvals: viewApprovals, sessions: viewSessions, grants: viewGrants, settings: viewSettings, activity: viewActivity }[S.tab] || viewApprovals)();
  }
  if (setHTML(main, "main", html)) tick();
  renderBulk();
}

function banner() {
  const d = S.data;
  let b = "";
  if (!S.live && S.downMsg) b += `<div class="banner down">⚠ ${esc(S.downMsg)}</div>`;
  if (d.mode === "bypass") {
    const exp = d.modeExpiresAt ? `<span data-exp="${d.modeExpiresAt}" data-prefix="for "></span>` : "until you turn it off";
    b += `<div class="banner bypass">⚡ <span>Global bypass — everything runs without asking, ${exp}.</span><button class="btn btn-small btn-ghost" data-action="mode" data-mode="normal">Turn off</button></div>`;
  } else if (d.mode === "lockdown") {
    b += `<div class="banner lockdown">■ <span>Lockdown — every request is refused.</span><button class="btn btn-small btn-ghost" data-action="mode" data-mode="normal">End lockdown</button></div>`;
  }
  return b;
}

// approvals
function signInCards() {
  return (S.data.mcp?.pending || []).map((m) => `<article class="card pending signin">
    <div class="p-head"><span class="chip k-signin">MCP SIGN-IN</span><span class="p-id">${esc(m.name)}</span><span class="spacer"></span></div>
    <p class="dim" style="margin:10px 0 14px;overflow-wrap:anywhere">${esc(m.serverUrl)} is waiting for you to authorize it.</p>
    <div class="p-actions" style="grid-template-columns:1fr auto">
      ${m.authUrl ? `<a class="btn btn-accent" href="${esc(m.authUrl)}" target="_blank" rel="noopener noreferrer">🔗 Sign in</a>` : `<span class="dim">Link lost — start again in Settings</span>`}
      <button class="btn btn-ghost" data-tab="settings">More</button>
    </div></article>`).join("");
}

function viewApprovals() {
  const p = S.data.pending;
  const signins = signInCards();
  if (!p.length && signins) return `<div class="section-head"><h1>Waiting on you</h1></div>${signins}`;
  if (!p.length) {
    return `<div class="empty"><div class="big-sigil">✦</div><h2>All quiet.</h2><p>Approvals land here the moment an agent asks.</p>${pushNudge() || pushState()}</div>`;
  }
  const head = `<div class="section-head"><h1>${plural(p.length, "approval")} waiting</h1>${p.length > 1 ? `<button class="btn btn-small btn-ghost" data-action="mark-all">${S.marks.size === p.length ? "Clear selection" : "Select all"}</button>` : ""}</div>`;
  return head + signins + p.map(pendingCard).join("");
}

function pushNudge() {
  if (!("Notification" in window) || Notification.permission === "granted") return "";
  return `<p style="margin-top:22px"><button class="btn btn-accent" data-action="push-sheet">Get notified on this device</button></p>`;
}
function pushState() {
  return S.data?.settings?.mobilePush
    ? `<p style="margin-top:18px" class="dim">📱 Mobile push is on</p>`
    : "";
}

const check = `<svg viewBox="0 0 24 24"><path fill="currentColor" d="M9 16.2 4.8 12l-1.4 1.4L9 19 21 7l-1.4-1.4Z"/></svg>`;

function pendingCard(p) {
  const isExec = p.kind === "exec";
  const marked = S.marks.has(p.id);
  const open = S.open.has(p.id);
  const text = isExec ? p.command || p.title : p.title;
  const long = text.split("\n").length > 5 || text.length > 320;
  const sess = p.sessionId
    ? `${esc(sessionTitle(p.sessionId))} ${p.sessionVerified ? `<span class="ok" title="Request carried this session's pinned key">✓ verified</span>` : `<span class="warn" title="No pinned key presented">⚠ unverified</span>`}`
    : `<span class="dim">not reported</span>`;
  let creds = "";
  if (p.credentials == null) creds = isExec ? `<span class="warn"><b>⚠ ALL</b> credentials in the profile</span>` : "";
  else creds = p.credentials.length ? p.credentials.map((c) => `<span class="tag">${esc(c)}</span>`).join("") : `<span class="dim">${p.awsProfile ? "none from 1Password" : "none"}</span>`;
  const gr = p.kind === "grant_request" && p.grant;
  const scopeOf = (v, any, none, label = esc) => v === "*" ? `<span class="warn"><b>⚠ ${any}</b></span>` : !(v || []).length ? `<span class="dim">${none}</span>` : v.map(label).join("");
  const rows = gr ? [
    ["Session", sess + ` <span class="dim">· this session only</span>`],
    ["Covers", `<b>${gr.target === "browser" ? "Browser windows" : isHost(gr.target) ? `${esc(hostLabel(gr.target))} (commands)` : "Eldar (commands)"}</b>`],
    ...(gr.target === "browser"
      ? [["Browser", scopeOf(gr.arc === "*" ? "*" : (gr.arcNames?.length ? gr.arcNames : gr.arc), "ANY browser profile", "none", (n) => `<span class="tag">${esc(n)}</span>`)]]
      : [["Profile", gr.profile === "*" ? `<span class="warn"><b>ANY profile</b></span>` : `<span class="tag">${esc(gr.profile)}</span>`],
         ["Creds", scopeOf(gr.credentials, "ANY credentials", "none", (c) => `<span class="tag">${esc(c)}</span>`)],
         ...(isHost(gr.target) ? [] : [["AWS", scopeOf(gr.aws, "ANY AWS role", "no AWS", awsTag)]])]),
    ["For", esc(dur(p.timeoutSeconds * 1000))],
    p.reason && ["Reason", `<span class="reason">${esc(p.reason)}</span>`],
  ].filter(Boolean) : [
    isHost(p.target) && ["Runs on", `<b class="mac">${esc(p.host || hostLabel(p.target))}</b>`],
    ["Session", sess],
    p.kind === "browser" && p.arcProfileName && ["Profile", `<b class="mac">${esc(p.arcProfileName)}</b>${p.arcSpace ? ` <span class="dim">· ${esc(p.arcSpace)} space</span>` : ""}`],
    p.profile && ["Profile", `<span class="tag">${esc(p.profile)}</span>`],
    creds && ["Creds", creds],
    p.awsProfile && ["AWS", `${awsTag(p.awsProfile)} <span class="dim">minted STS credentials</span>`],
    p.timeoutSeconds && [isExec ? "Timeout" : "Duration", esc(dur(p.timeoutSeconds * 1000))],
    p.cwd && ["Cwd", `<span class="mono dim">${esc(tilde(p.cwd))}</span>`],
    p.reason && [p.kind === "browser" ? "First action" : "Reason", `<span class="reason">${esc(p.reason)}</span>`],
  ].filter(Boolean);
  const extra = S.extra.has(p.id) || gr
    ? `<div class="p-extra">${isExec ? `<button class="btn btn-small btn-ghost" data-action="decide" data-d="allow_always" data-id="${p.id}">✓ Always allow <code>${esc(p.allowAlwaysPattern || (p.command || "").split(" ")[0] + " *")}</code>${isHost(p.target) ? ` on ${esc(hostLabel(p.target))}` : ""}</button>` : ""}
       <button class="btn btn-small btn-warn" data-action="grant-from" data-id="${p.id}">${gr ? "◐ Review / narrow the scope…" : `⚡ Grant ${p.kind === "browser" ? "browser access" : `a ${isHost(p.target) ? esc(hostLabel(p.target)) + " " : ""}bypass`}${isExec || p.kind === "browser" ? " + approve" : ""}…`}</button></div>`
    : "";
  return `<article class="card pending${marked ? " marked" : ""}" id="p${p.id}" data-id="${p.id}">
    <div class="p-head">
      <button class="mark${marked ? " on" : ""}" data-action="mark" data-id="${p.id}" aria-label="Select">${marked ? check : ""}</button>
      <span class="chip ${kindClass(p)}">${kindLabel(p.kind, p.target)}</span>
      <span class="p-id">#${p.id}</span>
      <span class="spacer"></span>
      ${p.channel === "tmux" ? `<span class="dim" style="font-size:12px">also in tmux</span>` : ""}
      <span class="countdown" data-exp="${p.expiresAt}"></span>
    </div>
    <div class="bar"><i class="bar-fill" data-exp="${p.expiresAt}" data-total="${p.expiresAt - p.createdAt}"></i></div>
    <pre class="p-title${isExec ? "" : " bypass-title"}${open ? " open" : long ? " clipped" : ""}">${esc(text)}</pre>
    ${long ? `<button class="more-link" data-action="expand" data-id="${p.id}">${open ? "Show less" : "Show full command"}</button>` : ""}
    <dl class="meta">${rows.map(([k, v]) => `<dt>${k}</dt><dd>${v}</dd>`).join("")}</dl>
    <div class="p-actions">
      <button class="btn btn-approve" data-action="decide" data-d="approved" data-id="${p.id}">✓ Approve</button>
      <button class="btn btn-deny" data-action="decide" data-d="denied" data-id="${p.id}">✗ Deny</button>
      ${p.kind === "browser" ? "" : `<button class="btn btn-ghost btn-more" data-action="extra" data-id="${p.id}" aria-label="More">⋯</button>`}
    </div>
    ${extra}
  </article>`;
}

function renderBulk() {
  const bar = $("#bulkbar");
  const n = S.marks.size;
  if (S.tab !== "approvals" || n === 0) { bar.hidden = true; return; }
  bar.hidden = false;
  setHTML(bar, "bulk", `<span class="n">${n} selected</span>
    <button class="btn btn-small btn-deny" data-action="bulk" data-d="denied">✗ Deny ${n}</button>
    <button class="btn btn-small btn-approve" data-action="bulk" data-d="approved">✓ Approve ${n}</button>
    <button class="btn btn-small btn-ghost" data-action="mark-none" aria-label="Clear">✕</button>`);
}

async function decide(ids, d) {
  ids.forEach((id) => $(`#p${id}`)?.classList.add("leaving"));
  const verb = { approved: "Approved", denied: "Denied", allow_always: "Approved + allowlisted" }[d];
  const ok = await act(api("POST", "/api/decide", { ids, decision: d }), `${verb} ${ids.map((i) => "#" + i).join(", ")}`);
  if (!ok) ids.forEach((id) => $(`#p${id}`)?.classList.remove("leaving"));
  ids.forEach((id) => S.marks.delete(id));
  renderBulk();
}

// sessions
// sessions, grouped by herdr space
const statusText = { working: "working", blocked: "needs you", done: "done", idle: "idle", unknown: "" };

function herdrLocate(pred) {
  for (const sp of S.data.herdr?.spaces || []) for (const t of sp.tabs) for (const p of t.panes) if (pred(p)) return { space: sp, tab: t, pane: p };
  return null;
}
const herdrForSession = (id) => herdrLocate((p) => p.agent?.sessionId === id);

function tabRow(sp, t) {
  const p = t.panes.find((x) => x.agent) || t.panes[0];
  const a = p?.agent;
  if (!a) {
    if (!p) return "";
    const onPhone = (S.data.terminals || []).some((x) => x.paneId === p.paneId);
    return `<button class="row shell" data-action="hpane" data-pane="${esc(p.paneId)}"><span class="lvl dim mono">›_</span>
      <span style="min-width:0"><div class="t">${esc(t.label)}</div><div class="s">${onPhone ? `<span class="ok">📱 open on a device</span> · ` : ""}<span class="mono">${esc(p.title && p.title !== t.label ? p.title : tilde(p.cwd || ""))}</span></div></span><span class="right dim">terminal</span></button>`;
  }
  const sess = a.sessionId ? sessionById(a.sessionId) : null;
  const level = sess?.level || (S.data.mode === "lockdown" ? "locked" : S.data.mode === "bypass" ? "full" : "none");
  const title = a.title && a.title !== t.label ? esc(a.title) : "";
  const st = a.status || "unknown";
  const onPhone = (S.data.terminals || []).some((x) => x.paneId === p.paneId);
  const sub = [onPhone ? `<span class="ok">📱 open on a device</span>` : "", `<span class="st st-${esc(st)}">${esc(statusText[st] ?? st)}</span>`, title, t.panes.length > 1 ? `${t.panes.length} panes` : ""].filter(Boolean).join(" · ");
  const attrs = a.sessionId ? `data-action="session" data-id="${esc(a.sessionId)}"` : `data-action="hpane" data-pane="${esc(p.paneId)}"`;
  return `<button class="row" ${attrs}>
    <span class="dot st-${esc(st)}"></span>
    <span style="min-width:0"><div class="t">${esc(t.label)}${a.kind !== "claude" ? ` <span class="faint">(${esc(a.kind)})</span>` : ""}</div><div class="s">${sub}</div></span>
    <span class="right">${sess?.pending ? `<span class="pill pill-pending">${sess.pending}</span>` : ""}<span class="pill pill-${level}">${levelText[level]}</span></span>
  </button>`;
}

function viewSessions() {
  const h = S.data.herdr;
  let out = `<div class="section-head"><h1>Sessions</h1><button class="btn btn-small btn-accent" data-action="launch">New</button></div>`;
  if (!h?.ok) out += `<div class="banner down">⚠ Can't read herdr${h?.error ? ": " + esc(h.error) : ""}</div>`;
  for (const sp of h?.spaces || []) {
    out += `<div class="space-head"><span class="space-name">${esc(sp.label)}</span>
      <button class="space-add" data-action="launch" data-ws="${esc(sp.workspaceId)}" title="New in ${esc(sp.label)}" aria-label="New session or terminal in ${esc(sp.label)}"><svg viewBox="0 0 12 12" width="10" height="10" aria-hidden="true"><path d="M6 1.5v9M1.5 6h9" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"/></svg></button>
      ${sp.focused ? `<span class="faint">· on your desk</span>` : ""}</div>
      <div class="card list">${sp.tabs.map((t) => tabRow(sp, t)).join("") || `<p class="dim" style="padding:12px;margin:0">No tabs.</p>`}</div>`;
  }
  const others = S.sessions.filter((x) => !x.inHerdr && x.seenByProxy);
  if (others.length) {
    out += `<div class="space-head"><span class="space-name">Outside herdr</span><span class="faint">· seen by the proxy</span></div><div class="card list">` +
      others.map((x) => `<button class="row" data-action="session" data-id="${esc(x.id)}">
        <span class="lvl lvl-${x.level}">${levelIcon[x.level]}</span>
        <span style="min-width:0"><div class="t">${esc(x.title)}</div><div class="s"><span data-ago="${x.lastSeen}"></span>${x.keyMismatches ? ` · <span class="bad">⚠ key mismatch</span>` : ""}</div></span>
        <span class="right">${x.pending ? `<span class="pill pill-pending">${x.pending}</span>` : ""}<span class="pill pill-${x.level}">${levelText[x.level]}</span></span>
      </button>`).join("") + `</div>`;
  }
  return out;
}

// herdr block + close for the session sheet
function herdrBlock(loc) {
  if (!loc) return "";
  const st = loc.pane.agent?.status || "unknown";
  return `<dt>Space</dt><dd>${esc(loc.space.label)} · tab <b>${esc(loc.tab.label)}</b></dd>
    <dt>Status</dt><dd><span class="st st-${esc(st)}">${esc(statusText[st] ?? st) || "unknown"}</span></dd>`;
}

function terminalButton(loc) {
  if (!loc?.pane.terminalId) return "";
  return `<button class="btn btn-accent" data-action="terminal" data-pane="${esc(loc.pane.paneId)}" data-label="${esc(loc.space.label + " / " + loc.tab.label)}">⌨ Open terminal</button>`;
}

function closeButton(loc) {
  if (!loc) return "";
  return `<button class="btn btn-deny" data-action="close-confirm" data-pane="${esc(loc.pane.paneId)}">✕ Close ${loc.pane.agent ? "session" : "terminal"}</button>`;
}

function closeConfirmSheet(paneId) {
  const loc = herdrLocate((p) => p.paneId === paneId);
  if (!loc) return `<h2>Session gone</h2><div class="sheet-actions"><button class="btn btn-ghost" data-action="close">Close</button></div>`;
  const whole = loc.tab.panes.length === 1;
  const where = whole ? `the <b>${esc(loc.tab.label)}</b> tab closes` : "its pane closes";
  const what = loc.pane.agent
    ? `Claude exits and ${where} in ${esc(loc.space.label)}. The conversation is saved; you can resume it later with <code>claude --resume</code>.`
    : `The shell and <b>anything running in it</b> stop, and ${where} in ${esc(loc.space.label)}.${loc.pane.title ? ` It shows <code>${esc(loc.pane.title)}</code> right now.` : ""}`;
  return `<h2>Close “${esc(loc.tab.label)}”?</h2>
    <p class="lead">${what}</p>
    ${loc.pane.agent?.status === "working" ? `<p class="warn">It's working right now — closing stops it mid-task.</p>` : ""}
    <div class="sheet-actions"><button class="btn btn-ghost" data-action="close">Cancel</button><button class="btn btn-danger" data-action="close-session" data-pane="${esc(paneId)}">✕ Close ${loc.pane.agent ? "session" : "terminal"}</button></div>`;
}

function hpaneSheet(paneId) {
  const loc = herdrLocate((p) => p.paneId === paneId);
  if (!loc) return `<h2>Session gone</h2><div class="sheet-actions"><button class="btn btn-ghost" data-action="close">Close</button></div>`;
  if (!loc.pane.agent) {
    return `<h2>${esc(loc.tab.label)}</h2><p class="lead">Terminal in ${esc(loc.space.label)}</p>
      <dl class="kv"><dt>Showing</dt><dd class="mono">${esc(loc.pane.title || "—")}</dd><dt>Cwd</dt><dd class="mono">${esc(tilde(loc.pane.cwd))}</dd></dl>
      <div class="sess-actions">${terminalButton(loc)}${closeButton(loc)}</div>`;
  }
  return `<h2>${esc(loc.tab.label)}</h2><p class="lead">${esc(loc.pane.agent?.title || "")}</p>
    <dl class="kv">${herdrBlock(loc)}<dt>Cwd</dt><dd class="mono">${esc(tilde(loc.pane.cwd))}</dd></dl>
    <p class="dim" style="font-size:13px">This session hasn't reported its Claude session ID yet, so bypass controls appear once it has.</p>
    <div class="sess-actions">${terminalButton(loc)}${closeButton(loc)}</div>`;
}

// launch
function launchSheet() {
  const L = S.launch;
  const spaces = S.data.herdr?.spaces || [];
  const sp = spaces.find((x) => x.workspaceId === L.ws) || spaces[0];
  if (!sp) return `<h2>No herdr spaces</h2><p class="lead">herdr isn't reachable right now.</p><div class="sheet-actions"><button class="btn btn-ghost" data-action="close">Close</button></div>`;
  L.ws = sp.workspaceId;
  // The space's usual directory: the one most of its tabs use.
  L.cwd = sp.cwds[0] || "/home/eldar/workspace";
  const shell = L.kind === "shell";
  return `<h2>${shell ? "New terminal" : "New Claude session"}</h2>
    <div class="seg" style="margin:6px 0 12px"><button class="${shell ? "" : "on"}" data-action="l-kind" data-v="claude">Claude session</button><button class="${shell ? "on" : ""}" data-action="l-kind" data-v="shell">Terminal</button></div>
    <p class="lead">${shell ? "Opens a tab with a shell in the space, then attaches to it here (Face ID first)." : `Opens a tab in the space and starts <code>claude --dangerously-skip-permissions -n &lt;name&gt;</code>. Credentialed commands still go through the proxy.`}
      <br><span class="faint">Starts in</span> <span class="mono" style="font-size:12.5px">${esc(tilde(L.cwd))}</span></p>
    <div class="field"><label for="l-ws">Space</label><select class="select" id="l-ws">${spaces.map((x) => `<option value="${esc(x.workspaceId)}"${x.workspaceId === L.ws ? " selected" : ""}>${esc(x.label)}</option>`).join("")}</select></div>
    <div class="field"><label for="l-name">Name</label><input class="input" id="l-name" value="${esc(L.name)}" placeholder="${shell ? "e.g. logs, deploy, scratch" : "e.g. docs-cleanup"}" maxlength="80" autocomplete="off" autocapitalize="off"></div>

    ${L.err ? `<p class="bad">✗ ${esc(L.err)}</p>` : ""}
    <div class="sheet-actions"><button class="btn btn-ghost" data-action="close"${L.busy ? " disabled" : ""}>Cancel</button>
      <button class="btn btn-approve" data-action="l-go"${L.busy ? " disabled" : ""}>${L.busy ? `<span class="spin"></span> ${shell ? "Opening…" : "Starting Claude…"}` : shell ? "▶ Open terminal" : "▶ Start session"}</button></div>`;
}

async function launch() {
  const L = S.launch;
  L.name = $("#l-name").value.trim();
  if (!L.name) { L.err = "Give the session a name."; return renderSheet(); }
  L.err = ""; L.busy = true; renderSheet();
  try {
    const sp = (S.data.herdr?.spaces || []).find((x) => x.workspaceId === L.ws);
    if (L.kind === "shell") {
      const out = await api("POST", "/api/herdr/shell", { workspaceId: L.ws, name: L.name, cwd: L.cwd });
      closeSheet();
      return window.eldarTerm.openTerminal(out.paneId, `${sp?.label || L.ws} / ${L.name}`);
    }
    await api("POST", "/api/herdr/launch", { workspaceId: L.ws, name: L.name, cwd: L.cwd, skipPermissions: true });
    toast(`Started “${L.name}” in ${sp?.label || L.ws}`);
    closeSheet();
  } catch (e) {
    L.busy = false; L.err = e.message; renderSheet();
  }
}

function coverLabel(c) {
  if (c.source === "global") return "Global bypass";
  const who = c.source === "agent" ? "Agent-held" : "Grant";
  if (c.full) return `${who}: everything`;
  return `${who}: ${c.profile === "*" ? "any profile" : esc(c.profile)} · ${esc(c.creds)}`;
}

function sessionSheet(id) {
  const s = sessionById(id);
  const loc = herdrForSession(id);
  if (!s) return `<h2>Session gone</h2><p class="lead">It's no longer listed.</p><div class="sheet-actions"><button class="btn btn-ghost" data-action="close">Close</button></div>`;
  const d = S.data;
  let key;
  if (s.keyMismatches) key = `<span class="bad">⚠ ${plural(s.keyMismatches, "request")} claimed this session with a different key</span> — a restarted supervisor client, or an impostor.`;
  else if (s.pinned) key = `<span class="ok">✓ pinned</span> — session grants apply only to this session`;
  else if (s.seenByProxy) key = `<span class="warn">○ no key</span> — its supervisor client predates session keys; session grants match after it restarts`;
  else key = `<span class="dim">pins on its first call</span>`;
  const hasFull = d.grants.some((g) => isEldarGrant(g) && g.session === id && fullGrant(g));
  const specific = d.grants.filter((g) => g.session === id).length;
  const covers = (s.covers || []).map((c) => {
    const exp = c.expiresAt ? `<span class="dim" data-exp="${c.expiresAt}" data-suffix=" left"></span>` : `<span class="dim">until revoked</span>`;
    const scope = c.session === "*" && c.source !== "global" ? ` <span class="faint">(all sessions)</span>` : "";
    const x = c.source === "grant" && c.session === id ? `<button class="btn btn-small btn-ghost" data-action="revoke-grant" data-id="${c.id}">Revoke</button>`
      : "";
    return `<div class="cover"><span class="lvl lvl-${c.full ? "full" : "partial"}">${c.full ? "⚡" : "◐"}</span><span class="c-t">${coverLabel(c)}${scope}<br>${exp}</span>${x}</div>`;
  }).join("");
  const choosing = S.sheet.choose;
  const bypassBtn = hasFull
    ? `<button class="btn btn-ghost" data-action="session-bypass-off" data-id="${esc(id)}">○ Turn bypass off</button>`
    : choosing
      ? `<div class="chips">${[[15, "15 min"], [60, "1 hour"], [240, "4 hours"], [0, "Until I turn it off"]].map(([m, l]) => `<button data-action="session-bypass" data-id="${esc(id)}" data-min="${m}">${l}</button>`).join("")}</div>`
      : `<button class="btn btn-warn" data-action="session-bypass-choose">⚡ Bypass this session</button>`;
  return `<h2>${esc(s.title)}</h2>
    <p class="lead"><span class="pill pill-${s.level}">${levelText[s.level]}</span> ${s.pending ? `· <span class="warn">${plural(s.pending, "approval")} waiting</span>` : ""}</p>
    <dl class="kv">
      ${herdrBlock(loc)}
      <dt>Session</dt><dd class="mono">${esc(s.id)}</dd>
      ${s.pane ? `<dt>Pane</dt><dd class="mono">${esc(s.pane)}${s.status ? ` · ${esc(s.status)}` : ""}</dd>` : ""}
      ${s.cwd ? `<dt>Cwd</dt><dd class="mono">${esc(tilde(s.cwd))}</dd>` : ""}
      <dt>Key</dt><dd>${key}</dd>
      ${s.seenByProxy ? `<dt>Last call</dt><dd><span data-ago="${s.lastSeen}"></span> · ${plural(s.requests, "call")}</dd>` : ""}
      ${s.lastAction ? `<dt>Last</dt><dd class="mono" style="font-size:12.5px">${esc(s.lastAction)}</dd>` : ""}
    </dl>
    <div class="field"><span class="label">What skips approval</span>
      ${covers ? `<div class="covers">${covers}</div>` : `<p class="dim" style="margin:6px 0 0">${d.mode === "lockdown" ? "Lockdown — everything is refused." : "Nothing — every credentialed call asks."}</p>`}
    </div>
    <div class="sess-actions">
      ${terminalButton(loc)}
      ${bypassBtn}
      <div class="sess-secondary">
        <button class="btn btn-ghost" data-action="grant-new" data-session="${esc(id)}">◐ Fine-grained</button>
        ${specific ? `<button class="btn btn-deny" data-action="session-clear" data-id="${esc(id)}">✕ Clear</button>` : ""}
        ${s.pinned || s.keyMismatches ? `<button class="btn btn-ghost" data-action="unpin" data-id="${esc(id)}">↻ Re-pin key</button>` : ""}
      </div>
      ${closeButton(loc)}
    </div>`;
}

// grants
function viewGrants() {
  const d = S.data;
  const head = `<div class="section-head"><h1>Bypasses</h1><button class="btn btn-small btn-accent" data-action="grant-new" data-session="*">+ New grant</button></div>`;
  if (!d.grants.length && !(d.browserGrants || []).length) {
    return head + `<div class="empty" style="padding-top:8vh"><div class="big-sigil">✦</div><h2>Nothing bypasses approval.</h2><p>Grants you create and bypasses agents were granted show up here.</p></div>`;
  }
  const exp = (t) => (t ? `<span class="dim" data-exp="${t}" data-suffix=" left"></span>` : `<span class="dim">until revoked</span>`);
  const grants = d.grants.map((g) => {
    const who = g.session === "*" ? "All sessions" : sessionTitle(g.session);
    const creds = g.credentials === "*" ? "any credentials" : g.credentials.length ? g.credentials.map((c) => `<span class="tag">${esc(c)}</span>`).join("") : "no credentials";
    let scope;
    if (g.target === "browser") scope = `open browser windows in ${g.arc === "*" ? `<span class="warn"><b>any browser profile</b></span>` : (g.arcNames?.length ? g.arcNames : g.arc || []).map((n) => `<b class="mac">${esc(n)}</b>`).join(", ")}`;
    else if (fullGrant(g)) scope = `<span class="warn"><b>everything</b></span>`;
    else scope = `${g.profile === "*" ? "any profile" : `<span class="tag">${esc(g.profile)}</span>`} · ${creds}${isEldarGrant(g) ? ` · ${awsScope(g.aws)}` : ""}`;
    const chip = isHost(g.target) ? ["k-mac", g.target.toUpperCase() + " "] : g.target === "browser" ? ["k-mac", "BROWSER "] : ["k-bypass", ""];
    return `<div class="card grant">
      <div><span class="chip ${chip[0]}" style="margin-right:6px">${chip[1]}GRANT #${g.id}</span><span class="who">${esc(who)}</span><div class="scope" style="margin-top:6px">${scope}</div></div>
      <div style="text-align:right">${exp(g.expiresAt)}<div style="margin-top:8px"><button class="btn btn-small btn-deny" data-action="revoke-grant" data-id="${g.id}">Revoke</button></div></div>
      <div class="foot">${plural(g.uses, "use")}${g.note ? " · " + esc(g.note) : ""}</div>
    </div>`;
  });
  const arc = (d.browserGrants || []).map((b) => `<div class="card grant">
      <div><span class="chip k-bypass" style="margin-right:6px">BROWSER #${b.id}</span><span class="who">${esc(sessionTitle(b.sessionId))}</span><div class="scope" style="margin-top:6px">drives${b.profileName ? ` <span class="tag">${esc(b.profileName)}</span>` : " a browser"} on ${esc(hostLabel(b.host || "mac"))}${b.space ? ` <span class="dim">· ${esc(b.space)} space</span>` : ""}${b.channels ? ` · <span class="ok">connected</span>` : ""}</div></div>
      <div style="text-align:right">${exp(b.expiresAt)}<div style="margin-top:8px"><button class="btn btn-small btn-deny" data-action="revoke-browser" data-id="${b.id}">End</button></div></div>
      <div class="foot">${esc(b.client)}</div>
    </div>`);
  return head + arc.join("") + grants.join("");
}

// settings
function viewSettings() {
  const mcp = S.data.mcp || { servers: [], pending: [] };
  const crons = S.data.crons || [];
  const pending = mcp.pending.map((m) => `<div class="card conn-card">
    <div class="conn-head"><span class="chip k-signin">SIGN-IN</span><b>${esc(m.name)}</b><span class="dim mono conn-url">${esc(m.serverUrl)}</span></div>
    <div class="conn-actions">
      ${m.authUrl ? `<a class="btn btn-accent" href="${esc(m.authUrl)}" target="_blank" rel="noopener noreferrer">🔗 Sign in</a>` : ""}
      <button class="btn btn-ghost" data-action="mcp-remove" data-name="${esc(m.name)}">Cancel</button>
    </div>
    <p class="dim" style="font-size:12.5px;margin:12px 0 6px">Browser couldn't get back to the proxy afterwards? Paste the address it landed on:</p>
    <div class="paste"><input class="input" id="paste-${esc(m.name)}" placeholder="https://…/oauth/callback?code=…" autocomplete="off"><button class="btn btn-small btn-ghost" data-action="mcp-paste" data-name="${esc(m.name)}">Finish</button></div>
  </div>`).join("");
  const servers = mcp.servers.map((s) => `<div class="row static">
    <span class="lvl ${s.hasToken ? "ok" : "bad"}">${s.hasToken ? "●" : "○"}</span>
    <span style="min-width:0"><div class="t">${esc(s.name)}</div><div class="s mono">${esc(s.serverUrl)}</div></span>
    <span class="right"><button class="btn btn-small btn-ghost" data-action="mcp-remove" data-name="${esc(s.name)}">Remove</button></span></div>`).join("");
  const cronRows = crons.map((c) => `<div class="row static">
    <span class="lvl dim">◷</span>
    <span style="min-width:0"><div class="t">${c.name && c.name !== c.id ? esc(c.name) : `<span class="dim">(unnamed)</span>`}</div><div class="s mono">${esc(c.id)}</div></span>
    <span class="right"><button class="btn btn-small btn-ghost" data-action="cron-remove" data-id="${esc(c.id)}">Remove</button></span></div>`).join("");
  return `${pending ? `<div class="section-head"><h1>Waiting for you to sign in</h1></div>${pending}` : ""}
    <button class="card subpage-link" data-tab="activity" style="margin-top:${pending ? 22 : 4}px">
      <span class="lvl dim">⌁</span>
      <span style="min-width:0"><div class="t">Activity</div><div class="s">Everything that ran, asked you, or was blocked</div></span>
      <span class="right dim">›</span>
    </button>
    <button class="card subpage-link" data-action="lock-now" style="margin-top:12px">
      <span class="lvl dim">🔒</span>
      <span style="min-width:0"><div class="t">Lock now</div><div class="s">Face ID again before anything shows</div></span>
    </button>
    ${cacheSection(22)}
    ${hostsSection()}
    ${servers ? `<div class="section-head" style="margin-top:28px"><h1>MCP servers</h1></div>
    <div class="card list">${servers}</div>` : ""}
    ${cronRows ? `<div class="section-head" style="margin-top:22px"><h1>Cron auto-approval</h1></div>
    <p class="dim" style="margin:-6px 2px 10px;font-size:13px">While one of these crons is running, its requests are approved without asking.</p>
    <div class="card list">${cronRows}</div>` : ""}`;
}

function cacheSection(top) {
  const cache = S.data.cache || [];
  const rows = cache.map((e) => `<div class="row static">
    <span class="lvl ${e.kind === "aws" ? "warn" : "ok"}">●</span>
    <span style="min-width:0"><div class="t mono" style="font-size:13px">${esc(e.kind === "aws" ? e.name.replace(/^aws-/, "") : e.name)}</div>
    <div class="s">${e.kind === "aws" ? "AWS STS" : "1Password"} · cached <span data-ago="${e.cachedAt}"></span></div></span>
    <span class="right"><button class="btn btn-small btn-ghost" data-action="cache-clear" data-name="${esc(e.name)}">Clear</button></span></div>`).join("");
  return `<div class="section-head" style="margin-top:${top}px"><h1>Credential cache</h1>${cache.length ? `<button class="btn btn-small btn-ghost" data-action="cache-clear">Clear all</button>` : ""}</div>
    <p class="dim" style="margin:-6px 2px 10px;font-size:13px">The supervisor keeps each profile's credentials after the first fetch. Clear one after rotating a secret in 1Password; the next request fetches it again.</p>
    <div class="card list">${rows || `<p class="dim" style="padding:12px;margin:0">Empty. The next request for a profile fetches from 1Password.</p>`}</div>`;
}

// One section per remote host agents may run commands on.
function hostsSection() {
  return (S.data.hosts || []).filter((h) => h.configured).map((h) => {
    const lt = h.lastTest;
    const status = !lt ? `<span class="dim">Not tested yet</span>`
      : lt.ok ? `<span class="ok">● reachable</span> <span class="dim">· ${esc(lt.detail)} · checked <span data-ago="${lt.at}"></span></span>`
      : `<span class="bad">○ unreachable</span> <span class="dim">· ${esc(lt.detail)}</span>`;
    const bypass = hostBypassIds(h.name).length > 0;
    const grants = hostGrants(h.name).map((g) => `<div class="row static"><span class="lvl lvl-${g.session === "*" && g.profile === "*" && g.credentials === "*" ? "full" : "partial"}">⚡</span>
      <span style="min-width:0"><div class="t">${g.session === "*" ? "All sessions" : esc(sessionTitle(g.session))}</div>
      <div class="s">${g.profile === "*" ? "any profile" : esc(g.profile)} · ${g.credentials === "*" ? "any credentials" : esc(g.credentials.join(", ") || "no credentials")} · ${g.expiresAt ? `<span data-exp="${g.expiresAt}" data-suffix=" left"></span>` : "until revoked"}</div></span>
      <span class="right"><button class="btn btn-small btn-ghost" data-action="revoke-grant" data-id="${g.id}">Revoke</button></span></div>`).join("");
    const allow = (h.allow || []).map((p) => `<div class="row static"><span class="lvl dim">✓</span><span style="min-width:0"><div class="t mono" style="font-size:13px">${esc(p)}</div></span>
      <span class="right"><button class="btn btn-small btn-ghost" data-action="host-allow-remove" data-host="${esc(h.name)}" data-p="${esc(p)}">Remove</button></span></div>`).join("");
    const label = esc(h.label);
    return `<div class="section-head" style="margin-top:28px"><h1>${label}</h1><button class="btn btn-small btn-ghost" data-action="host-test" data-host="${esc(h.name)}">Test</button></div>
    <div class="card conn-card">
      <div style="font-size:13.5px"><b class="mac">${esc(h.host)}</b> · ${status}</div>
      <p class="dim" style="font-size:13px;margin:8px 0 12px">${h.description ? esc(h.description) + " " : ""}Agents run commands here with your approval. Bypasses for Eldar never cover it, only what's below.</p>
      <button class="btn ${bypass ? "btn-ghost" : "btn-mac"}" style="width:100%" data-action="host-bypass" data-host="${esc(h.name)}">${bypass ? `○ Turn the ${label} bypass off` : `⚡ Bypass ${label}…`}</button>
    </div>
    ${grants ? `<div class="card list" style="margin-bottom:12px">${grants}</div>` : ""}
    <div class="space-head"><span class="space-name">Always allowed on ${label}</span></div>
    <div class="card list">${allow || `<p class="dim" style="padding:12px;margin:0">Nothing yet. Approve one of its requests with “Always allow”, or add a pattern.</p>`}
      <div class="paste" style="padding:10px"><input class="input mono" id="host-pattern-${esc(h.name)}" placeholder="e.g. git status*" autocomplete="off" autocapitalize="off"><button class="btn btn-small btn-ghost" data-action="host-allow-add" data-host="${esc(h.name)}">Add</button></div></div>`;
  }).join("");
}

function formSheet(title, lead, fields, action, submitLabel) {
  return `<h2>${title}</h2><p class="lead">${lead}</p>
    ${fields.map(([id, label, ph]) => `<div class="field"><label for="${id}">${label}</label><input class="input" id="${id}" placeholder="${esc(ph)}" autocomplete="off"></div>`).join("")}
    <div class="sheet-actions"><button class="btn btn-ghost" data-action="close">Cancel</button><button class="btn btn-accent" data-action="${action}">${submitLabel}</button></div>`;
}

// activity
const evIcon = {
  approved: ["✓", "ok"], denied: ["✗", "bad"], allow: ["•", "dim"], bypass: ["⚡", "warn"],
  timeout: ["⌛", "warn"], deny: ["⛔", "bad"], lockdown: ["■", "bad"], ask: ["?", "dim"],
};
const filters = {
  all: () => true,
  asked: (e) => ["approved", "denied", "timeout"].includes(e.decision),
  auto: (e) => ["allow", "bypass"].includes(e.decision),
  blocked: (e) => ["deny", "lockdown", "denied"].includes(e.decision),
};
const decisionLabel = {
  approved: "Approved by you", denied: "Denied", allow: "Allowed", bypass: "Bypass", timeout: "Timed out",
  deny: "Blocked by policy", lockdown: "Blocked (lockdown)", ask: "Asked",
};
const ms = (n) => (n < 1000 ? `${n}ms` : n < 60000 ? `${(n / 1000).toFixed(n < 10000 ? 1 : 0)}s` : dur(n));

function auditMatches(e) {
  if (!filters[S.auditFilter](e)) return false;
  const q = (S.auditQuery || "").toLowerCase().trim();
  if (!q) return true;
  return [e.action, e.reason, e.decisionReason, e.profile, e.cwd].some((v) => (v || "").toLowerCase().includes(q));
}

// The line that best describes a command: first that isn't blank, a comment,
// or a `set -e`-style option (mirrors view.CommandSummary in Go).
function commandSummary(cmd) {
  const lines = cmd.trim().split("\n");
  const line = lines.find((l) => !/^\s*(#.*|set\s+[-+][A-Za-z]+.*|set\s+-o\s+\S+.*|)\s*$/.test(l)) ?? lines[0];
  return lines.length > 1 ? `${line.trim()}  (+${lines.length - 1} lines)` : line.trim();
}

function feedHTML() {
  const list = S.audit.map((e, i) => [e, i]).filter(([e]) => auditMatches(e)).reverse();
  const items = list.map(([e, i]) => {
    const [ico, cls] = evIcon[e.decision] || ["·", "dim"];
    const t = Date.parse(e.timestamp);
    const host = entryHost(e);
    const bits = [
      e.exitCode != null ? (e.exitCode === 0 ? `<span class="ok">exit 0</span>` : `<span class="bad">exit ${e.exitCode}</span>`) : "",
      e.durationMs != null ? ms(e.durationMs) : "",
      e.timedOut ? `<span class="warn">timed out</span>` : "",
    ].filter(Boolean).join(" · ");
    return `<button class="ev" data-action="audit" data-i="${i}">
      <span class="ico ${cls}">${ico}</span>
      <span class="act">${host ? `<span class="chip k-mac mini">${esc(host.toUpperCase())}</span> ` : ""}${esc(commandSummary(entryCommand(e)))}</span>
      <span class="when" data-ago="${t}"></span>
      <span class="why">${esc(e.decisionReason)}${e.profile ? ` · ${esc(e.profile)}` : ""}${bits ? ` · ${bits}` : ""}${e.reason ? `<br><span class="faint">${esc(e.reason)}</span>` : ""}</span>
    </button>`;
  }).join("");
  return `<div class="card feed">${items || `<p class="dim" style="padding:16px;margin:0">${S.auditQuery ? "No matches." : "Nothing here yet."}</p>`}</div>`;
}

function viewActivity() {
  const f = Object.keys(filters).map((k) => `<button class="${S.auditFilter === k ? "on" : ""}" data-action="filter" data-f="${k}">${{ all: "All", asked: "Asked you", auto: "Auto-approved", blocked: "Blocked" }[k]}</button>`).join("");
  return `<button class="subpage-back" data-tab="settings">‹ Settings</button>
    <div class="section-head"><h1>Activity</h1></div>
    <input class="input" id="act-search" placeholder="Search commands, reasons, profiles…" value="${esc(S.auditQuery || "")}" autocomplete="off" autocapitalize="off" style="margin-bottom:10px">
    <div class="filters" style="margin-bottom:12px">${f}</div>
    <div id="feed-wrap">${feedHTML()}</div>`;
}

function auditSheet(e) {
  const [ico, cls] = evIcon[e.decision] || ["·", "dim"];
  const t = new Date(e.timestamp);
  const host = entryHost(e);
  const command = entryCommand(e);
  let creds = "";
  if (e.credentials === null) creds = `<span class="warn"><b>⚠ ALL</b> credentials in the profile</span>`;
  else if (Array.isArray(e.credentials)) creds = e.credentials.length ? e.credentials.map((c) => `<span class="tag">${esc(c)}</span>`).join("") : `<span class="dim">none</span>`;
  const row = (k, v) => (v ? `<dt>${k}</dt><dd>${v}</dd>` : "");
  const exit = e.exitCode != null ? (e.exitCode === 0 ? `<span class="ok">0</span>` : `<span class="bad">${e.exitCode}</span>`) : "";
  const isCommand = e.requestId > 0 && !/^(GRANT|REVOKE|MODE|SETTINGS|UNPIN|MCP|CRON|TERMINAL|HERDR|MAC ALLOW|MAC DISALLOW|ALLOW ADD|ALLOW REMOVE|BROWSER|AWS SIGN-IN|SECRETS_DUMP|AWS_CREDS_DUMP)/.test(e.action);
  return `<h2><span class="${cls}">${ico}</span> ${esc(decisionLabel[e.decision] || e.decision)}</h2>
    <p class="lead">${esc(t.toLocaleString())} · <span data-ago="${t.getTime()}"></span>${e.requestId ? ` · request #${e.requestId}` : ""}</p>
    <div class="field"><span class="label">${host ? `Command <span class="chip k-mac mini">ON ${esc(hostLabel(host).toUpperCase())}</span>` : "Action"}</span>
      <pre class="audit-cmd">${esc(command)}</pre>
      <button class="btn btn-small btn-ghost" data-action="copy-cmd">Copy</button></div>
    <dl class="kv">
      ${row("Why", esc(e.decisionReason))}
      ${row("Reason", e.reason ? `<span class="reason">${esc(e.reason)}</span>` : "")}
      ${row("Profile", e.profile ? `<span class="tag">${esc(e.profile)}</span>` : "")}
      ${row("Creds", creds)}
      ${row("AWS", e.awsProfile ? `<span class="tag">${esc(e.awsProfile)}</span>` : "")}
      ${row("Session", e.sessionId ? `${esc(sessionTitle(e.sessionId))} <span class="faint mono">${esc(short(e.sessionId))}</span>` : "")}
      ${row("Cwd", e.cwd ? `<span class="mono">${esc(tilde(e.cwd))}</span>` : "")}
      ${row("Exit", exit)}
      ${row("Took", e.durationMs != null ? esc(ms(e.durationMs)) : "")}
      ${row("Flags", [e.timedOut && `<span class="warn">timed out</span>`, e.truncated && `<span class="warn">output truncated</span>`].filter(Boolean).join(" · "))}
      ${row("Mode", e.mode && e.mode !== "normal" ? esc(e.mode) : "")}
    </dl>
    ${isCommand && e.exitCode != null ? `<div id="audit-out"><button class="btn btn-ghost" style="width:100%" data-action="audit-output">Show output</button></div>` : ""}
    <div class="sheet-actions"><button class="btn btn-ghost" data-action="close">Close</button></div>`;
}

async function loadAuditOutput() {
  const e = S.sheet?.entry;
  const box = $("#audit-out");
  if (!e || !box) return;
  box.innerHTML = `<p class="dim">Loading…</p>`;
  try {
    const full = await api("GET", `/api/audit/entry?ts=${encodeURIComponent(e.timestamp)}&requestId=${e.requestId}`);
    const r = full.result || {};
    const block = (label, text) => text ? `<div class="field"><span class="label">${label}</span><pre class="audit-out">${esc(text)}</pre></div>` : "";
    box.innerHTML = (block("Output", r.stdout) + block("Errors (stderr)", r.stderr)) || `<p class="dim">No output.</p>`;
    box.innerHTML += `<p class="faint" style="font-size:12px;margin:0 0 6px">The audit log keeps the first 1000 characters of each stream.</p>`;
  } catch (err) {
    box.innerHTML = `<p class="bad">✗ ${esc(err.message)}</p>`;
  }
}

let auditTimer = null;
function loadAuditSoon() {
  clearTimeout(auditTimer);
  auditTimer = setTimeout(loadAudit, 600);
}
async function loadAudit() {
  try {
    S.audit = (await api("GET", "/api/audit?limit=300")).entries || [];
    if (S.tab !== "activity") return;
    // Refresh just the feed when it's on screen, so a focused search box keeps focus.
    const wrap = $("#feed-wrap");
    if (wrap) { wrap.innerHTML = feedHTML(); S.html.main = null; tick(); } else renderMain();
  } catch (e) { toast("✗ " + e.message, "err"); }
}

// ── sheets ─────────────────────────────────────────────────────────────
function openSheet(sheet) {
  S.sheet = sheet;
  S.html.sheet = null;
  $("#backdrop").hidden = false;
  $("#sheet").hidden = false;
  renderSheet();
}
function closeSheet() {
  S.sheet = null;
  S.ed = null;
  $("#backdrop").hidden = true;
  $("#sheet").hidden = true;
  S.html.sheet = null;
}
function renderSheet() {
  if (!S.sheet) return;
  const k = S.sheet.kind;
  const html = k === "mode" ? modeSheet() : k === "lockdown" ? lockdownSheet() : k === "session" ? sessionSheet(S.sheet.id)
    : k === "grant" ? editorSheet() : k === "push" ? S.sheet.html : k === "launch" ? launchSheet()
    : k === "audit" ? auditSheet(S.sheet.entry)
    : k === "hpane" ? hpaneSheet(S.sheet.pane) : k === "close-confirm" ? closeConfirmSheet(S.sheet.pane) : "";
  if (setHTML($("#sheet"), "sheet", html)) tick();
}

function modeSheet() {
  const m = S.data?.mode;
  const opt = (ico, t, s, attrs, cur) => `<button class="opt${cur ? " current" : ""}" ${attrs}><span class="o-ico">${ico}</span><span><div class="o-t">${t}</div>${s ? `<div class="o-s">${s}</div>` : ""}</span></button>`;
  return `<h2>Proxy mode</h2><p class="lead">Applies to every session.</p><div class="opts">
    ${opt('<span class="ok">●</span>', "Normal", "Ask per policy", 'data-action="mode" data-mode="normal"', m === "normal")}
    ${opt('<span class="warn">⚡</span>', "Bypass for 15 minutes", "", 'data-action="mode" data-mode="bypass" data-min="15"')}
    ${opt('<span class="warn">⚡</span>', "Bypass for 1 hour", "", 'data-action="mode" data-mode="bypass" data-min="60"')}
    ${opt('<span class="warn">⚡</span>', "Bypass for 2 hours", "", 'data-action="mode" data-mode="bypass" data-min="120"')}
    ${opt('<span class="warn">⚡</span>', "Bypass until I turn it off", "", 'data-action="mode" data-mode="bypass" data-min="0"', m === "bypass")}
    ${opt('<span class="bad">■</span>', "Lockdown", "Refuse everything and revoke every bypass", 'data-action="lockdown-confirm"', m === "lockdown")}
  </div>`;
}
function lockdownSheet() {
  return `<h2>Engage lockdown?</h2><p class="lead">Every request is refused, and all grants and agent-held bypasses are revoked.</p>
    <div class="sheet-actions"><button class="btn btn-ghost" data-action="close">Cancel</button><button class="btn btn-danger" data-action="mode" data-mode="lockdown">■ Lock it down</button></div>`;
}

// grant editor
const DURS = [[15, "15m"], [60, "1h"], [240, "4h"], [480, "8h"], [0, "Until revoked"]];
// Scope pickers: credentials, AWS roles (grouped by account) and Arc profiles
// share one searchable checklist. mode: "any" | "none" | "only".
function newEditor(session, from) {
  const ed = {
    target: "eldar", session: session || "*", profile: "*", minutes: 60, note: "", approve: 0, err: "",
    credMode: "any", awsMode: "none", arcMode: "any",
    sel: { creds: new Set(), aws: new Set(), arc: new Set() },
    filter: { creds: "", aws: "", arc: "" },
    creds: [], credsFor: "", loading: false,
  };
  ed.durs = DURS;
  if (from && from.kind === "grant_request" && from.grant) {
    const g = from.grant;
    const mode = (v) => v === "*" ? "any" : (v || []).length ? "only" : "none";
    Object.assign(ed, { request: true, approve: from.id, target: g.target, session: g.session, profile: g.profile || "*" });
    ed.credMode = mode(g.credentials); if (ed.credMode === "only") g.credentials.forEach((c) => ed.sel.creds.add(c));
    ed.awsMode = mode(g.aws); if (ed.awsMode === "only") g.aws.forEach((a) => ed.sel.aws.add(a));
    ed.arcMode = mode(g.arc) === "only" ? "only" : "any"; if (ed.arcMode === "only") g.arc.forEach((a, i) => { ed.sel.arc.add(a); if (g.arcNames?.[i] && !ed.extraArc) ed.extraArc = { id: a, name: g.arcNames[i] }; });
    if (g.minutes) {
      ed.minutes = g.minutes;
      if (!DURS.some(([m]) => m === g.minutes)) ed.durs = [[g.minutes, `${g.minutes}m (asked)`], ...DURS];
    }
  } else if (from) {
    ed.session = from.sessionId || "*";
    if (from.kind === "browser") {
      ed.target = "browser";
      if (from.arcProfile) { ed.arcMode = "only"; ed.sel.arc.add(from.arcProfile); ed.extraArc = { id: from.arcProfile, name: from.arcProfileName || from.arcProfile }; }
    } else {
      if (from.profile && S.data.profiles.includes(from.profile)) ed.profile = from.profile;
      if (from.credentials && ed.profile !== "*") {
        ed.credMode = from.credentials.length ? "only" : "none";
        from.credentials.forEach((c) => ed.sel.creds.add(c));
      }
      ed.target = isHost(from.target) ? from.target : "eldar";
      if (from.awsProfile) { ed.awsMode = "only"; ed.sel.aws.add(from.awsProfile); }
    }
    if (from.kind === "exec" || from.kind === "browser") ed.approve = from.id;
  }
  S.ed = ed;
  openSheet({ kind: "grant" });
  loadCreds();
}
async function loadCreds() {
  const ed = S.ed;
  if (!ed || ed.target === "browser" || ed.profile === "*" || ed.credsFor === ed.profile) return;
  ed.credsFor = ed.profile;
  ed.loading = true;
  ed.creds = [];
  renderSheet();
  try {
    const names = (await api("GET", "/api/credentials?profile=" + encodeURIComponent(ed.profile))).credentials || [];
    if (S.ed !== ed || ed.credsFor !== ed.profile) return;
    ed.creds = names;
  } catch (e) { ed.err = e.message; }
  ed.loading = false;
  renderSheet();
}
// Items for a picker: {value, label, group?, warn?}. Picks the list no longer has stay listed.
function edItems(key) {
  const ed = S.ed;
  let items;
  if (key === "creds") items = ed.creds.map((c) => ({ value: c, label: c }));
  else if (key === "aws") items = (S.data.awsProfiles || []).map((p) => {
    const i = p.lastIndexOf(".");
    return { value: p, label: i > 0 ? p.slice(i + 1) : p, group: i > 0 ? p.slice(0, i) : "", warn: awsRisky(p) };
  });
  else {
    items = (S.data.arcProfiles || []).map((a) => ({ value: a.id, label: a.name + (a.ready ? "" : " (not set up)"), name: a.name }));
    if (ed.extraArc && !items.some((x) => x.value === ed.extraArc.id)) items.push({ value: ed.extraArc.id, label: ed.extraArc.name, name: ed.extraArc.name });
  }
  for (const v of ed.sel[key]) if (!items.some((x) => x.value === v)) items.push({ value: v, label: v });
  // Picks (and their groups) first, so a prefilled list shows what's ticked.
  const sel = ed.sel[key], pickedGroups = new Set(items.filter((x) => sel.has(x.value)).map((x) => x.group || ""));
  const first = (x) => (x.group ? pickedGroups.has(x.group) : sel.has(x.value)) ? 0 : 1;
  return items.sort((a, b) => first(a) - first(b) || (a.group || "").localeCompare(b.group || "") || a.label.localeCompare(b.label));
}
function edVisible(key) {
  const f = S.ed.filter[key].toLowerCase();
  return edItems(key).filter((x) => !f || x.label.toLowerCase().includes(f) || (x.group || "").toLowerCase().includes(f));
}
function edChecklist(key) {
  const ed = S.ed;
  if (key === "creds" && ed.loading) return `<p class="dim" style="padding:12px;margin:0">Loading credential names…</p>`;
  const vis = edVisible(key);
  if (!vis.length) return `<p class="dim" style="padding:12px;margin:0">${edItems(key).length ? "No matches" : "Nothing to pick"}</p>`;
  const sel = ed.sel[key];
  let out = "", last = null;
  for (const x of vis) {
    if (x.group && x.group !== last) {
      const members = vis.filter((y) => y.group === x.group);
      const n = members.filter((y) => sel.has(y.value)).length;
      const st = n === members.length ? " on" : n ? " some" : "";
      out += `<button class="check group${st}" data-action="ed-group" data-k="${key}" data-g="${esc(x.group)}"><span class="box">${n === members.length ? check : n ? "–" : ""}</span>${esc(x.group)}</button>`;
    }
    last = x.group || null;
    out += `<button class="check${x.group ? " sub" : ""}${sel.has(x.value) ? " on" : ""}" data-action="ed-pick" data-k="${key}" data-v="${esc(x.value)}"><span class="box">${sel.has(x.value) ? check : ""}</span>${x.warn ? `<span class="warn">⚠</span> ` : ""}${esc(x.label)}</button>`;
  }
  return out;
}
function edPicker(key, noun) {
  const ed = S.ed;
  return `<input class="input" id="ed-filter-${key}" data-k="${key}" placeholder="Filter ${noun}…" value="${esc(ed.filter[key])}" autocomplete="off" autocapitalize="off" style="margin-top:10px">
    <div class="checklist" id="ed-list-${key}">${edChecklist(key)}</div>
    <div class="dim" style="font-size:12.5px;margin-top:6px"><span id="ed-count-${key}">${ed.sel[key].size} selected</span> · <button class="linkish" data-action="ed-all" data-k="${key}">toggle all shown</button></div>`;
}
function edModes(field, modes, cur, disabled) {
  const labels = { any: "Any", none: "None", only: "Only these" };
  return `<div class="seg">${modes.map((v) => `<button class="${cur === v ? "on" : ""}" data-action="ed-mode" data-f="${field}" data-v="${v}"${disabled && disabled(v) ? ` disabled title="Pick a profile first"` : ""}>${labels[v]}</button>`).join("")}</div>`;
}
const edScope = (mode, set) => mode === "any" ? "*" : mode === "none" ? [] : [...set].sort();
function edInput() {
  const ed = S.ed;
  if (ed.target === "browser") {
    const arc = edScope(ed.arcMode, ed.sel.arc);
    const names = Array.isArray(arc) ? arc.map((id) => edItems("arc").find((x) => x.value === id)?.name || id) : [];
    return { target: "browser", session: ed.session, profile: "*", credentials: [], aws: [], arc, arcNames: names };
  }
  return {
    target: ed.target, session: ed.session, profile: ed.profile,
    credentials: edScope(ed.credMode, ed.sel.creds),
    aws: ed.target === "eldar" ? edScope(ed.awsMode, ed.sel.aws) : [],
  };
}
function edPicked(set, label, noun) {
  const v = [...set].sort();
  return !v.length ? `(pick ${noun})` : v.length > 3 ? `${v.length} ${noun}` : v.map(label).join(", ");
}
function edSummary() {
  const ed = S.ed;
  const who = ed.session === "*" ? "every session" : sessionTitle(ed.session);
  const d = ed.minutes ? `for ${ed.durs.find(([m]) => m === ed.minutes)[1]}` : "until you revoke it";
  const g = edInput();
  let s;
  if (ed.target === "browser") {
    const prof = ed.arcMode === "any" ? "any browser profile" : edPicked(ed.sel.arc, (id) => edItems("arc").find((x) => x.value === id)?.name || id, "browser profiles");
    s = `Open browser windows for <b>${esc(who)}</b> in <b>${esc(prof)}</b> without asking, <b>${esc(d)}</b>. Each window lasts no longer than the grant.`;
  } else {
    const prof = ed.profile === "*" ? "any profile" : `the ${ed.profile} profile`;
    const creds = ed.credMode === "any" ? "any credentials" : ed.credMode === "none" ? "no credentials" : edPicked(ed.sel.creds, (c) => c, "credentials");
    const aws = ed.target !== "eldar" ? "" : ", " + (ed.awsMode === "any" ? "any AWS role" : ed.awsMode === "none" ? "no AWS" : "AWS " + edPicked(ed.sel.aws, awsLabel, "AWS roles"));
    s = `Auto-approve commands${isHost(ed.target) ? ` on ${esc(hostLabel(ed.target))}` : ""} from <b>${esc(who)}</b> using <b>${esc(prof)}</b> with <b>${esc(creds)}${esc(aws)}</b>, <b>${esc(d)}</b>.`;
  }
  if (ed.request) s += ` <span class="ok">Approves request #${ed.approve} with this scope.</span>`;
  else if (ed.approve) s += ` <span class="ok">Then approve #${ed.approve}.</span>`;
  if (ed.session === "*" && fullGrant(g)) s += `<br><span class="warn">This covers ${isHost(ed.target) ? `every command on ${esc(hostLabel(ed.target))}` : ed.target === "browser" ? "every browser profile, for every session" : "everything here — same as global bypass"}.</span>`;
  else if (ed.target === "eldar" && (ed.awsMode === "any" || (ed.awsMode === "only" && [...ed.sel.aws].some(awsRisky)))) s += `<br><span class="warn">⚠ ${ed.awsMode === "any" ? "Every AWS role, admin and production included." : "Includes admin or production AWS roles."}</span>`;
  return s;
}
function editorSheet() {
  const ed = S.ed;
  const sessOpts = [`<option value="*">All sessions</option>`, ...S.sessions.map((s) => `<option value="${esc(s.id)}"${s.id === ed.session ? " selected" : ""}>${esc(s.title)}${s.pane ? " · " + esc(s.pane) : ""}</option>`)];
  if (ed.session !== "*" && !sessionById(ed.session)) sessOpts.push(`<option value="${esc(ed.session)}" selected>session ${esc(short(ed.session))}</option>`);
  const hosts = (S.data.hosts || []).filter((h) => h.configured);
  const targets = [["eldar", "Eldar"], ...hosts.map((h) => [h.name, esc(h.label)]), ...(S.data.arcProfiles?.length ? [["browser", "Browsers"]] : [])];
  const targetSeg = targets.length > 1 ? `<div class="field"><span class="label">Covers</span><div class="seg">${targets.map(([v, l]) => `<button class="${ed.target === v ? "on" : ""}" data-action="ed-target" data-v="${v}">${l}</button>`).join("")}</div></div>` : "";
  let scope;
  if (ed.target === "browser") {
    scope = `<div class="field"><span class="label">Browser profiles</span>${edModes("arc", ["any", "only"], ed.arcMode)}${ed.arcMode === "only" ? edPicker("arc", "browser profiles") : ""}</div>`;
  } else {
    const profOpts = [`<option value="*">Any profile</option>`, ...S.data.profiles.map((p) => `<option value="${esc(p)}"${p === ed.profile ? " selected" : ""}>${esc(p)}</option>`)];
    scope = `<div class="field"><label for="ed-profile">Profile</label><select class="select" id="ed-profile">${profOpts.join("")}</select></div>
      <div class="field"><span class="label">Credentials</span>${edModes("cred", ["any", "none", "only"], ed.credMode, (v) => v === "only" && ed.profile === "*")}${ed.credMode === "only" ? edPicker("creds", `${ed.creds.length || ""} credentials`) : ""}</div>`;
    if (ed.target === "eldar") {
      scope += `<div class="field"><span class="label">AWS roles</span>${edModes("aws", ["any", "none", "only"], ed.awsMode)}${ed.awsMode === "any" ? `<div class="warn" style="font-size:12.5px;margin-top:6px">⚠ Every role, admin and production included.</div>` : ""}${ed.awsMode === "only" ? edPicker("aws", "accounts and roles") : ""}</div>`;
    }
  }
  return `<h2>${ed.request ? `Review grant request #${ed.approve}` : ed.approve ? `Grant + approve #${ed.approve}` : "New bypass grant"}</h2>
    <p class="lead">${ed.target === "browser" ? "Arc windows open without asking. Lockdown still ends them." : "Matching commands run without asking. Policy denies and lockdown still apply."}</p>
    ${targetSeg}
    <div class="field"><label for="ed-session">Session</label><select class="select" id="ed-session">${sessOpts.join("")}</select></div>
    ${scope}
    <div class="field"><span class="label">For</span><div class="chips">${ed.durs.map(([m, l]) => `<button class="${ed.minutes === m ? "on" : ""}" data-action="ed-dur" data-m="${m}">${l}</button>`).join("")}</div></div>
    <div class="field"><label for="ed-note">Note</label><input class="input" id="ed-note" placeholder="Optional — why this grant exists" value="${esc(ed.note)}" maxlength="200"></div>
    <div class="summary" id="ed-summary">${edSummary()}</div>
    ${ed.err ? `<p class="bad" style="margin:10px 0 0">✗ ${esc(ed.err)}</p>` : ""}
    <div class="sheet-actions"><button class="btn btn-ghost" data-action="close">Cancel</button><button class="btn btn-approve" data-action="ed-submit">${ed.request ? "Approve with this scope" : ed.approve ? "Grant + approve" : "Create grant"}</button></div>`;
}
// Re-render one picker in place, so the filter box keeps focus and the list its scroll.
function edRefresh(key) {
  const list = $(`#ed-list-${key}`);
  if (list) { const top = list.scrollTop; list.innerHTML = edChecklist(key); list.scrollTop = top; }
  const n = $(`#ed-count-${key}`);
  if (n) n.textContent = `${S.ed.sel[key].size} selected`;
  $("#ed-summary").innerHTML = edSummary();
  S.html.sheet = null;
}
async function submitEditor() {
  const ed = S.ed;
  const empty = ed.target === "browser" ? ed.arcMode === "only" && !ed.sel.arc.size && "Pick at least one browser profile, or choose Any."
    : (ed.credMode === "only" && !ed.sel.creds.size && "Pick at least one credential, or choose None.")
      || (ed.target === "eldar" && ed.awsMode === "only" && !ed.sel.aws.size && "Pick at least one AWS role, or choose None.");
  if (empty) { ed.err = empty; renderSheet(); return; }
  const body = { ...edInput(), minutes: ed.minutes || null, note: ed.note.trim() };
  const ok = ed.request
    ? await act(api("POST", `/api/grant-requests/${ed.approve}/approve`, body), `Request #${ed.approve} approved with your scope`)
    : await act(api("POST", "/api/grants", { ...body, approve: ed.approve }), ed.approve ? `Grant created and #${ed.approve} approved` : "Grant created");
  if (ok) closeSheet();
}

// notifications
const isIOS = /iP(hone|ad|od)/.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
const standalone = window.matchMedia("(display-mode: standalone)").matches || navigator.standalone;
function b64ToBytes(b64) {
  const s = atob((b64 + "=".repeat((4 - (b64.length % 4)) % 4)).replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from(s, (c) => c.charCodeAt(0));
}
function switchRow(key, title, sub) {
  const on = !!S.data?.settings?.[key];
  return `<button class="opt switch-row" data-action="setting" data-key="${key}" data-on="${on ? 1 : 0}" role="switch" aria-checked="${on}">
    <span><div class="o-t">${title}</div><div class="o-s">${sub}</div></span><span class="switch${on ? " on" : ""}"><i></i></span></button>`;
}
async function pushSheet() {
  let body;
  if (!("serviceWorker" in navigator) || !("PushManager" in window)) {
    body = isIOS && !standalone
      ? `<p class="lead">On iPhone, notifications work from the installed app:</p><ol style="padding-left:20px;line-height:1.8"><li>Tap <b>Share</b> in Safari</li><li><b>Add to Home Screen</b></li><li>Open <b>Eldar</b> from your home screen and come back here</li></ol>`
      : `<p class="lead">This browser doesn't support web push.</p>`;
    body += `<div class="sheet-actions"><button class="btn btn-ghost" data-action="close">Close</button></div>`;
  } else {
    const reg = await navigator.serviceWorker.ready;
    const sub = await reg.pushManager.getSubscription();
    const info = await api("GET", "/api/push/key").catch(() => ({ devices: 0 }));
    const denied = Notification.permission === "denied";
    body = `<p class="lead">${sub ? `<span class="ok">✓ This device gets a notification</span> the moment an approval is waiting.` : denied ? `<span class="bad">Notifications are blocked</span> for this site — allow them in the browser's site settings.` : "Get a notification on this device the moment an approval is waiting — with Approve and Deny right on it where the platform supports it."}</p>
      <p class="dim" style="font-size:13px">${plural(info.devices || 0, "device")} subscribed.</p>
      <div class="sheet-actions">${sub
        ? `<button class="btn btn-ghost" data-action="push-off">Turn off here</button><button class="btn btn-accent" data-action="push-test">Send a test</button>`
        : `<button class="btn btn-ghost" data-action="close">Not now</button><button class="btn btn-accent" data-action="push-on"${denied ? " disabled" : ""}>Enable notifications</button>`}</div>`;
  }
  const toggles = `<div class="opts" style="margin:4px 0 18px">
    ${switchRow("mobilePush", "📱 Mobile push", "Send new approvals and MCP sign-ins to subscribed devices. Turn on when you leave your desk.")}</div>
    <div class="field" style="margin-bottom:6px"><span class="label">This device</span></div>`;
  openSheet({ kind: "push", html: `<h2>Notifications</h2>${toggles}${body}` });
}
async function pushOn() {
  try {
    if ((await Notification.requestPermission()) !== "granted") throw new Error("Permission not granted");
    const { key } = await api("GET", "/api/push/key");
    const reg = await navigator.serviceWorker.ready;
    const sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: b64ToBytes(key) });
    await api("POST", "/api/push/subscribe", sub.toJSON());
    if (!S.data?.settings?.mobilePush) await api("POST", "/api/settings", { mobilePush: true });
    toast("Notifications on for this device");
    pushSheet();
  } catch (e) { toast("✗ " + e.message, "err", 5000); }
}
async function pushOff() {
  const reg = await navigator.serviceWorker.ready;
  const sub = await reg.pushManager.getSubscription();
  if (sub) {
    await api("POST", "/api/push/unsubscribe", { endpoint: sub.endpoint }).catch(() => {});
    await sub.unsubscribe();
  }
  toast("Notifications off for this device");
  pushSheet();
}

// ── events ─────────────────────────────────────────────────────────────
function setTab(tab) {
  S.tab = tab;
  try { localStorage.setItem("tab", tab); } catch {}
  render();
  window.scrollTo({ top: 0 });
  if (tab === "activity") loadAudit();
}

document.addEventListener("click", async (ev) => {
  const tabBtn = ev.target.closest("[data-tab]");
  if (tabBtn) return setTab(tabBtn.dataset.tab);
  if (ev.target.id === "backdrop") return closeSheet();
  const el = ev.target.closest("[data-action]");
  if (!el) return;
  const a = el.dataset.action;
  const id = Number(el.dataset.id);
  const sid = el.dataset.id;
  switch (a) {
    case "decide": return decide([id], el.dataset.d);
    case "bulk": return decide([...S.marks], el.dataset.d);
    case "mark": S.marks.has(id) ? S.marks.delete(id) : S.marks.add(id); return renderMain();
    case "mark-all":
      if (S.marks.size === S.data.pending.length) S.marks.clear(); else S.data.pending.forEach((p) => S.marks.add(p.id));
      return renderMain();
    case "mark-none": S.marks.clear(); return renderMain();
    case "expand": S.open.has(id) ? S.open.delete(id) : S.open.add(id); return renderMain();
    case "extra": S.extra.has(id) ? S.extra.delete(id) : S.extra.add(id); return renderMain();
    case "grant-from": { const p = S.data.pending.find((x) => x.id === id); if (p) newEditor(null, p); return; }
    case "grant-new": return newEditor(el.dataset.session);
    case "mode-sheet": return openSheet({ kind: "mode" });
    case "lockdown-confirm": return openSheet({ kind: "lockdown" });
    case "mode": {
      const mode = el.dataset.mode;
      const min = Number(el.dataset.min) || null;
      const label = { normal: "Normal mode", bypass: "Global bypass on", lockdown: "Lockdown engaged" }[mode];
      if (await act(api("POST", "/api/mode", { mode, minutes: min }), label)) closeSheet();
      return;
    }
    case "session": return openSheet({ kind: "session", id: sid });
    case "hpane": return openSheet({ kind: "hpane", pane: el.dataset.pane });
    case "terminal": closeSheet(); return window.eldarTerm.openTerminal(el.dataset.pane, el.dataset.label);
    case "unlock": return unlock(el);
    case "lock-now": return api("POST", "/api/passkey/lock").finally(() => location.reload());
    case "close-confirm": return openSheet({ kind: "close-confirm", pane: el.dataset.pane });
    case "close-session": {
      const loc = herdrLocate((p) => p.paneId === el.dataset.pane);
      el.disabled = true;
      if (await act(api("POST", "/api/herdr/close", { paneId: el.dataset.pane }), `Closed “${loc?.tab.label || el.dataset.pane}”`)) closeSheet();
      else el.disabled = false;
      return;
    }
    case "launch":
      S.launch = { ws: el.dataset.ws || (S.data.herdr?.spaces || []).find((x) => x.focused)?.workspaceId, name: "", cwd: "", kind: el.dataset.kind || "claude", err: "", busy: false };
      return openSheet({ kind: "launch" });
    case "l-kind": S.launch.kind = el.dataset.v; S.launch.name = $("#l-name").value; return renderSheet();
    case "l-go": return launch();
    case "session-bypass-choose": S.sheet.choose = true; return renderSheet();
    case "session-bypass": {
      const m = Number(el.dataset.min);
      await act(api("POST", "/api/grants", { session: sid, profile: "*", credentials: "*", aws: "*", minutes: m || null, note: "session bypass" }), "Bypass on for " + sessionTitle(sid));
      S.sheet.choose = false;
      return;
    }
    case "session-bypass-off": {
      const ids = S.data.grants.filter((g) => isEldarGrant(g) && g.session === sid && fullGrant(g)).map((g) => g.id);
      await act(Promise.all(ids.map((g) => api("DELETE", `/api/grants/${g}`))), "Bypass off for " + sessionTitle(sid));
      return;
    }
    case "session-clear": {
      const g = S.data.grants.filter((x) => x.session === sid).map((x) => api("DELETE", `/api/grants/${x.id}`));
      await act(Promise.all(g), "Cleared bypasses for " + sessionTitle(sid));
      return;
    }
    case "unpin": return act(api("POST", `/api/sessions/${encodeURIComponent(sid)}/unpin`), "Key unpinned — it re-pins on the session's next call");
    case "revoke-grant": return act(api("DELETE", `/api/grants/${id}`), `Grant #${id} revoked`);
    case "revoke-browser": return act(api("DELETE", `/api/browser-grants/${id}`), `Arc access #${id} ended`);
    case "filter": S.auditFilter = el.dataset.f; return renderMain();
    case "audit": { const e = S.audit[Number(el.dataset.i)]; if (e) openSheet({ kind: "audit", entry: e }); return; }
    case "audit-output": return loadAuditOutput();
    case "copy-cmd": {
      const text = S.sheet?.entry ? entryCommand(S.sheet.entry) : "";
      try { await navigator.clipboard.writeText(text); toast("Copied"); } catch { toast("✗ Couldn't copy", "err"); }
      return;
    }
    case "close": return closeSheet();
    case "push-sheet": return pushSheet();
    case "setting": {
      const key = el.dataset.key, on = el.dataset.on !== "1";
      const label = { mobilePush: "Mobile push" }[key];
      if (await act(api("POST", "/api/settings", { [key]: on }), `${label} ${on ? "on" : "off"}`)) {
        S.data.settings = { ...S.data.settings, [key]: on };
        renderChrome();
        pushSheet();
      }
      return;
    }
    case "push-on": return pushOn();
    case "push-off": return pushOff();
    case "push-test": return act(api("POST", "/api/push/test"), "Test sent");
    case "ed-mode": {
      const f = el.dataset.f;
      S.ed[{ cred: "credMode", aws: "awsMode", arc: "arcMode" }[f]] = el.dataset.v; S.ed.err = "";
      renderSheet(); return loadCreds();
    }
    case "ed-pick": {
      const set = S.ed.sel[el.dataset.k], v = el.dataset.v;
      set.has(v) ? set.delete(v) : set.add(v);
      return edRefresh(el.dataset.k);
    }
    case "ed-group": {
      const k = el.dataset.k, set = S.ed.sel[k];
      const members = edVisible(k).filter((x) => x.group === el.dataset.g);
      const all = members.every((x) => set.has(x.value));
      members.forEach((x) => (all ? set.delete(x.value) : set.add(x.value)));
      return edRefresh(k);
    }
    case "ed-all": {
      const k = el.dataset.k, set = S.ed.sel[k], vis = edVisible(k);
      const all = vis.every((x) => set.has(x.value));
      vis.forEach((x) => (all ? set.delete(x.value) : set.add(x.value)));
      return edRefresh(k);
    }
    case "ed-dur": S.ed.minutes = Number(el.dataset.m); return renderSheet();
    case "ed-target": S.ed.target = el.dataset.v; renderSheet(); return loadCreds();
    case "host-test": el.disabled = true; {
      const h = el.dataset.host, label = hostLabel(h);
      try { const t = await api("POST", `/api/hosts/${encodeURIComponent(h)}/test`); t.ok ? toast(`${label} reachable — ${t.detail}`) : toast(`✗ ${label} unreachable: ${t.detail}`, "err", 6000); }
      catch (e) { toast("✗ " + e.message, "err"); }
      el.disabled = false; return;
    }
    case "host-bypass": {
      const h = el.dataset.host, label = hostLabel(h);
      const ids = hostBypassIds(h);
      if (ids.length) return act(Promise.all(ids.map((g) => api("DELETE", `/api/grants/${g}`))), `${label} bypass off`);
      return openSheet({ kind: "push", html: `<h2>Bypass ${esc(label)}</h2><p class="lead">Every command there runs without asking. Policy denies and lockdown still apply.</p>
        <div class="chips">${[[15, "15 min"], [60, "1 hour"], [240, "4 hours"], [0, "Until I turn it off"]].map(([m, l]) => `<button data-action="host-bypass-on" data-host="${esc(h)}" data-min="${m}">${l}</button>`).join("")}</div>
        <div class="sheet-actions"><button class="btn btn-ghost" data-action="close">Cancel</button></div>` });
    }
    case "host-bypass-on": {
      const h = el.dataset.host;
      if (await act(api("POST", "/api/grants", { target: h, session: "*", profile: "*", credentials: "*", minutes: Number(el.dataset.min) || null, note: `${hostLabel(h)} bypass` }), `${hostLabel(h)} bypass on`)) closeSheet();
      return;
    }
    case "host-allow-add": {
      const h = el.dataset.host;
      const inp = document.getElementById(`host-pattern-${h}`); const pattern = inp.value.trim();
      if (!pattern) return;
      if (await act(api("POST", `/api/hosts/${encodeURIComponent(h)}/allow`, { pattern }), `Always allowed on ${hostLabel(h)}: ${pattern}`)) inp.value = "";
      return;
    }
    case "host-allow-remove": return act(api("DELETE", `/api/hosts/${encodeURIComponent(el.dataset.host)}/allow`, { pattern: el.dataset.p }), `Removed from the ${hostLabel(el.dataset.host)} allow list`);
    case "ed-submit": return submitEditor();
    case "mcp-new":
      return openSheet({ kind: "push", html: formSheet("Connect an MCP server", "Starts an OAuth sign-in. The link appears here and on Approvals.",
        [["f-name", "Name", "e.g. linear"], ["f-url", "Server URL", "https://mcp.example.com/mcp"]], "mcp-register", "Start sign-in") });
    case "mcp-register": {
      const name = $("#f-name").value.trim(), url = $("#f-url").value.trim();
      if (await act(api("POST", "/api/mcp/register", { name, url }), `Sign-in started for ${name}`)) { closeSheet(); setTab("settings"); }
      return;
    }
    case "mcp-paste": {
      const url = $(`#paste-${CSS.escape(el.dataset.name)}`).value.trim();
      return act(api("POST", "/api/mcp/callback", { url }), `${el.dataset.name} is connected`);
    }
    case "mcp-remove":
      if (!confirm(`Remove ${el.dataset.name}? Its tokens are deleted.`)) return;
      return act(api("DELETE", `/api/mcp/${encodeURIComponent(el.dataset.name)}`), `${el.dataset.name} removed`);
    case "cron-new":
      return openSheet({ kind: "push", html: formSheet("Allow a cron to auto-approve", "While that cron is running, its requests are approved without asking.",
        [["f-id", "Cron ID", "the job's id from jobs.json"], ["f-cname", "Name", "what it does"]], "cron-allow", "Allow") });
    case "cron-allow": {
      const id = $("#f-id").value.trim(), name = $("#f-cname").value.trim();
      if (await act(api("POST", "/api/crons", { id, name }), "Cron allowed")) closeSheet();
      return;
    }
    case "cache-clear": {
      const name = el.dataset.name;
      if (!name && !confirm("Clear every cached credential set? Each profile refetches on its next request.")) return;
      return act(api("POST", "/api/cache/clear", name ? { name } : {}), name ? `Cleared ${name.replace(/^aws-/, "")}` : "Credential cache cleared");
    }
    case "cron-remove":
      return act(api("DELETE", `/api/crons/${encodeURIComponent(el.dataset.id)}`), "Cron removed");
  }
});

// Form inputs in the editor update state without re-rendering, so typing and
// focus are never interrupted.
document.addEventListener("input", (ev) => {
  if (ev.target.id === "act-search") {
    S.auditQuery = ev.target.value;
    $("#feed-wrap").innerHTML = feedHTML();
    S.html.main = null;
    tick();
    return;
  }
  if (!S.ed) return;
  const t = ev.target;
  if (t.id?.startsWith("ed-filter-")) { S.ed.filter[t.dataset.k] = t.value; edRefresh(t.dataset.k); }
  if (t.id === "ed-note") S.ed.note = t.value;
  $("#ed-summary").innerHTML = edSummary();
  S.html.sheet = null;
});
document.addEventListener("change", (ev) => {
  const t0 = ev.target;
  if (S.launch && S.sheet?.kind === "launch" && t0.id === "l-ws") {
    S.launch.name = $("#l-name").value;
    S.launch.ws = t0.value;
    return renderSheet();
  }
  if (!S.ed) return;
  const t = ev.target;
  if (t.id === "ed-session") { S.ed.session = t.value; renderSheet(); }

  if (t.id === "ed-profile") {
    S.ed.profile = t.value;
    S.ed.sel.creds.clear();
    if (t.value === "*" && S.ed.credMode === "only") S.ed.credMode = "any";
    renderSheet();
    loadCreds();
  }
});

document.addEventListener("keydown", (ev) => {
  if (document.body.classList.contains("term-open")) return;
  if (ev.key === "Escape" && S.sheet) return closeSheet();
  if (S.sheet || ev.target.matches("input, select, textarea")) return;
  const k = ev.key;
  if (k >= "1" && k <= "5") return setTab(["approvals", "sessions", "chat", "grants", "settings"][Number(k) - 1]);
  if (k === "m") return openSheet({ kind: "mode" });
  if (S.tab === "approvals" && S.data?.pending.length) {
    const target = S.marks.size ? [...S.marks] : [S.data.pending[0].id];
    if (k === "a") return decide(target, "approved");
    if (k === "d") return decide(target, "denied");
  }
});

// Notification clicks open /#p<id>; scroll that approval into view.
function focusHash() {
  if (location.hash === "#chat") { setTab("chat"); history.replaceState(null, "", "/"); return; }
  if (location.hash === "#settings" || location.hash === "#connections") { setTab("settings"); history.replaceState(null, "", "/"); return; }
  const m = location.hash.match(/^#p(\d+)$/);
  if (!m) return;
  if (S.tab !== "approvals") setTab("approvals");
  setTimeout(() => {
    const el = document.getElementById("p" + m[1]);
    if (el) { el.scrollIntoView({ behavior: "smooth", block: "center" }); el.animate([{ boxShadow: "0 0 0 3px var(--accent)" }, { boxShadow: "none" }], { duration: 1600 }); }
  }, 200);
  history.replaceState(null, "", "/");
}
window.addEventListener("hashchange", focusHash);

// ── boot ───────────────────────────────────────────────────────────────
if ("serviceWorker" in navigator) {
  navigator.serviceWorker.register("/sw.js").then(async (reg) => {
  }).catch(() => {});
  navigator.serviceWorker.addEventListener("message", (e) => {
    if (e.data?.focus) { location.hash = "p" + e.data.focus; focusHash(); }
    if (e.data?.url) { location.hash = e.data.url.replace(/^\/?/, "").replace(/^#?/, "#"); focusHash(); }
  });
}
setInterval(tick, 1000);
// term.js (the passkey ceremony) loads after this file.
document.addEventListener("DOMContentLoaded", () => checkLock().then((ok) => ok && start()));
