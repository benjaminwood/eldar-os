// Live terminal on a herdr pane, plus the WebAuthn (Face ID) ceremony that
// unlocks the whole app. Loaded by app.js; uses its globals ($, esc, api, toast,
// openSheet, closeSheet, S).
"use strict";

// ── WebAuthn plumbing ──────────────────────────────────────────────────
const b64u = {
  dec: (s) => {
    const b = atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4));
    return Uint8Array.from(b, (c) => c.charCodeAt(0)).buffer;
  },
  enc: (buf) => {
    let s = "";
    for (const x of new Uint8Array(buf)) s += String.fromCharCode(x);
    return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  },
};

function toCreation(o) {
  const pk = o.publicKey;
  pk.challenge = b64u.dec(pk.challenge);
  pk.user.id = b64u.dec(pk.user.id);
  (pk.excludeCredentials || []).forEach((c) => (c.id = b64u.dec(c.id)));
  return pk;
}
function toRequest(o) {
  const pk = o.publicKey;
  pk.challenge = b64u.dec(pk.challenge);
  (pk.allowCredentials || []).forEach((c) => (c.id = b64u.dec(c.id)));
  return pk;
}
function credToJSON(c) {
  const r = c.response;
  const response = r.attestationObject
    ? { attestationObject: b64u.enc(r.attestationObject), clientDataJSON: b64u.enc(r.clientDataJSON), transports: r.getTransports ? r.getTransports() : [] }
    : { authenticatorData: b64u.enc(r.authenticatorData), clientDataJSON: b64u.enc(r.clientDataJSON), signature: b64u.enc(r.signature), userHandle: r.userHandle ? b64u.enc(r.userHandle) : null };
  return { id: c.id, rawId: b64u.enc(c.rawId), type: c.type, authenticatorAttachment: c.authenticatorAttachment, response, clientExtensionResults: c.getClientExtensionResults ? c.getClientExtensionResults() : {} };
}

const T = { pending: null, prepared: null };

async function apiRaw(method, path, body, headers = {}) {
  const res = await fetch(path, {
    method,
    headers: { "X-Eldar-Proxy": "1", "Content-Type": "application/json", ...headers },
    body: body === undefined ? undefined : typeof body === "string" ? body : JSON.stringify(body),
  });
  const json = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(json.error || res.statusText);
  return json;
}

// Prefetch the ceremony options so the tap can call WebAuthn immediately:
// Safari only allows it during the user gesture.
async function prepare(kind) {
  T.prepared = { kind, promise: apiRaw("POST", `/api/passkey/${kind}/begin`) };
  return T.prepared.promise;
}

async function ceremony(kind) {
  const prep = T.prepared && T.prepared.kind === kind ? T.prepared.promise : prepare(kind);
  T.prepared = null;
  const { flow, options } = await prep;
  const cred = kind === "register"
    ? await navigator.credentials.create({ publicKey: toCreation(options) })
    : await navigator.credentials.get({ publicKey: toRequest(options) });
  if (!cred) throw new Error("cancelled");
  // Success sets the unlock cookie.
  await apiRaw("POST", `/api/passkey/${kind}/finish?flow=${encodeURIComponent(flow)}`, credToJSON(cred));
}

// ── entry point ────────────────────────────────────────────────────────
// The app is already unlocked (Face ID) to get here; the server re-checks.
function openTerminal(paneId, label) {
  T.pending = { paneId, label };
  connectTerminal();
}

// ── terminal view ──────────────────────────────────────────────────────
let libs = null;
function loadLibs() {
  if (libs) return libs;
  const add = (tag, attrs) => new Promise((ok, fail) => {
    const el = document.createElement(tag);
    Object.assign(el, attrs);
    el.onload = ok; el.onerror = () => fail(new Error("couldn't load " + (attrs.src || attrs.href)));
    document.head.append(el);
  });
  libs = Promise.all([
    add("link", { rel: "stylesheet", href: "/static/vendor/xterm.css" }),
    add("script", { src: "/static/vendor/xterm.js" }),
  ]).then(() => Promise.all([
    add("script", { src: "/static/vendor/addon-fit.js" }),
    add("script", { src: "/static/vendor/addon-web-links.js" }),
  ]));
  return libs;
}

// Key bar, modelled on Blink: tap a key for its main character, swipe up for
// the small one above it. ⌃ and ⌥ are sticky (tap = next key, double tap =
// locked) and apply to the iOS keyboard too. The arrow pad is one key: swipe
// in a direction for one press, hold to repeat, slide to change direction.
const KEYS = [
  { label: "⎋", title: "Esc", send: "\x1b" },
  { label: "⌃", title: "Ctrl (swipe up: ^C)", mod: "ctrl", up: { label: "^C", send: "\x03" } },
  { label: "⌥", title: "Alt", mod: "alt" },
  { label: "⇥", title: "Tab (swipe up: ⇧Tab)", send: "\t", up: { label: "⇤", send: "\x1b[Z" } },
  { label: "⎘", title: "Paste", paste: true },
  ...[["`", "~"], ["@", "#"], ["$", "^"], [";", ":"], ["-", "_"], ["=", "+"]].map(([a, b]) => ({ label: a, send: a, up: { label: b, send: b }, sym: true })),
  { arrows: true },
  { label: "⏎", title: "Enter", send: "\r" },
];
const ARROW = { up: "A", down: "B", right: "C", left: "D" };
const SWIPE = 14;          // px before a touch counts as a swipe
const REPEAT_DELAY = 380;  // ms held before arrows repeat
const REPEAT_EVERY = 65;   // ms between repeats

function keybarHTML() {
  return KEYS.map((k, i) => {
    if (k.arrows) return `<button class="kb-arrows" data-kb="${i}" title="Arrows: swipe, hold to repeat" aria-label="Arrow keys">
      <span class="ar ar-up"></span><span class="ar ar-left"></span><span class="ar ar-right"></span><span class="ar ar-down"></span></button>`;
    return `<button data-kb="${i}" class="${k.mod ? "kb-mod" : ""}${k.sym ? " kb-sym" : ""}" title="${esc(k.title || k.label)}" aria-label="${esc(k.title || k.label)}">${k.up ? `<small>${esc(k.up.label)}</small>` : ""}<span>${esc(k.label)}</span></button>`;
  }).join("");
}

let V = null; // live view state

function cssVar(n) { return getComputedStyle(document.documentElement).getPropertyValue("--" + n).trim(); }

async function connectTerminal() {
  const { paneId, label } = T.pending;
  await loadLibs();
  if (V) teardown();

  const root = document.createElement("div");
  root.className = "term-view";
  root.innerHTML = `
    <header class="term-bar">
      <button class="term-btn" data-t="detach">✕</button>
      <div class="term-title"><span class="term-dot"></span><span>${esc(label)}</span></div>
      <button class="term-btn tray-pill" data-t="tray-show" hidden></button>
      <button class="term-btn" data-t="smaller">A−</button><button class="term-btn" data-t="bigger">A+</button>
      <button class="term-btn kbd" data-t="kbd" title="Show or hide the keyboard">⌨</button>
    </header>
    <div class="term-host"><div class="term-el"></div><div class="tray" hidden></div><div class="term-overlay" hidden></div></div>
    <div class="keybar">${keybarHTML()}</div>`;
  document.body.append(root);
  document.body.classList.add("term-open");

  let fontSize = 13;
  try { fontSize = Number(localStorage.getItem("termFont")) || 13; } catch {}
  const term = new window.Terminal({
    fontFamily: "'JetBrains Mono', ui-monospace, Menlo, monospace",
    fontSize,
    cursorBlink: true,
    allowProposedApi: true,
    scrollback: 5000,
    macOptionIsMeta: true,
    theme: {
      background: cssVar("bg"), foreground: cssVar("fg"), cursor: cssVar("accent"), selectionBackground: cssVar("border"),
      black: cssVar("overlay"), red: cssVar("red"), green: cssVar("teal"), yellow: cssVar("yellow"), blue: cssVar("deep"),
      magenta: cssVar("orange"), cyan: cssVar("cyan"), white: cssVar("lavender"), brightBlack: cssVar("border"),
      brightRed: cssVar("red"), brightGreen: cssVar("mint"), brightYellow: cssVar("yellow"), brightBlue: cssVar("violet"),
      brightMagenta: cssVar("orange"), brightCyan: cssVar("cyan"), brightWhite: cssVar("bright"),
    },
  });
  const fit = new window.FitAddon.FitAddon();
  term.loadAddon(fit);
  term.loadAddon(new window.WebLinksAddon.WebLinksAddon((e, uri) => window.open(uri, "_blank", "noopener")));
  term.open(root.querySelector(".term-el"));

  V = { root, term, fit, ws: null, paneId, label, closed: false, tray: { idx: 0, hidden: new Set(), open: false, seen: new Set((S.data?.pending || []).map((p) => p.id)) } };
  const layout = () => {
    const vv = window.visualViewport;
    root.style.height = (vv ? vv.height : window.innerHeight) + "px";
    root.style.top = (vv ? vv.offsetTop : 0) + "px";
    try { fit.fit(); } catch {}
  };
  V.layout = layout;
  window.visualViewport?.addEventListener("resize", layout);
  window.visualViewport?.addEventListener("scroll", layout);
  window.addEventListener("resize", layout);
  layout();

  const enc = new TextEncoder();
  const send = (s) => { if (V.ws?.readyState === 1) V.ws.send(enc.encode(s)); };

  // Sticky modifiers: "off" | "once" | "lock".
  const mods = { ctrl: "off", alt: "off" };
  const paintMods = () => {
    for (const el of root.querySelectorAll(".kb-mod")) {
      const m = KEYS[el.dataset.kb].mod;
      el.classList.toggle("on", mods[m] !== "off");
      el.classList.toggle("locked", mods[m] === "lock");
    }
  };
  const spendMods = () => {
    let changed = false;
    for (const m of ["ctrl", "alt"]) if (mods[m] === "once") { mods[m] = "off"; changed = true; }
    if (changed) paintMods();
  };
  const ctrlChar = (c) => {
    if (c === " " || c === "@" || c === "2") return "\x00";
    const u = c.toUpperCase().charCodeAt(0);
    if (u >= 64 && u <= 95) return String.fromCharCode(u & 31);  // A–Z [ \\ ] ^ _
    if (c === "/" || c === "-") return "\x1f";
    if (c === "?" || c === "8") return "\x7f";
    return c;
  };
  // Apply held modifiers to one key's output.
  const modded = (seq) => {
    const ctrl = mods.ctrl !== "off", alt = mods.alt !== "off";
    if (!ctrl && !alt) return seq;
    const arrow = /^\x1b[\[O]([ABCD])$/.exec(seq);
    if (arrow) return `\x1b[1;${1 + (alt ? 2 : 0) + (ctrl ? 4 : 0)}${arrow[1]}`;
    let out = seq;
    if (ctrl && [...seq].length === 1) out = ctrlChar(seq);
    if (alt) out = "\x1b" + out;
    return out;
  };
  const press = (seq) => { send(modded(seq)); spendMods(); };
  const arrowSeq = (dir) => (term.modes.applicationCursorKeysMode ? "\x1bO" : "\x1b[") + ARROW[dir];

  term.onData((d) => {
    // Typed on the iOS keyboard while ⌃/⌥ is armed: modify single keystrokes.
    if ((mods.ctrl !== "off" || mods.alt !== "off") && [...d].length === 1) return press(d);
    send(d);
  });
  term.onBinary((s) => { if (V.ws?.readyState === 1) V.ws.send(Uint8Array.from(s, (c) => c.charCodeAt(0))); });
  term.onResize(({ cols, rows }) => { if (V.ws?.readyState === 1) V.ws.send(JSON.stringify({ type: "resize", cols, rows })); });

  root.addEventListener("click", (e) => {
    const t = e.target.closest("[data-t]");
    if (!t) return;
    if (t.dataset.t === "detach") return teardown();
    if (t.dataset.t === "tray-approve" || t.dataset.t === "tray-deny") return trayDecide(Number(t.dataset.id), t.dataset.t === "tray-approve" ? "approved" : "denied");
    if (t.dataset.t === "tray-later") { V.tray.hidden.add(Number(t.dataset.id)); return renderTray(); }
    if (t.dataset.t === "tray-show") { V.tray.hidden.clear(); return renderTray(); }
    if (t.dataset.t === "tray-prev" || t.dataset.t === "tray-next") { V.tray.idx += t.dataset.t === "tray-next" ? 1 : -1; V.tray.open = false; return renderTray(); }
    if (t.dataset.t === "tray-expand") { V.tray.open = !V.tray.open; return renderTray(); }
    if (t.dataset.t === "kbd") { if (document.activeElement?.classList.contains("xterm-helper-textarea")) term.blur(); else term.focus(); return; }
    if (t.dataset.t === "reconnect") return openTerminal(V.paneId, V.label);
    if (t.dataset.t === "smaller" || t.dataset.t === "bigger") {
      const n = Math.max(8, Math.min(22, term.options.fontSize + (t.dataset.t === "bigger" ? 1 : -1)));
      term.options.fontSize = n;
      try { localStorage.setItem("termFont", String(n)); } catch {}
      layout();
    }
  });
  // Touch scrolling. herdr's attach runs in the alternate screen with mouse
  // tracking on (Claude Code keeps its own history), so a finger drag becomes
  // mouse-wheel events (one per line moved, with momentum on a flick).
  // Without mouse tracking, it scrolls xterm's own buffer. Taps aren't
  // touched, so they still focus the terminal.
  const host = root.querySelector(".term-el");
  const sc = { y: 0, acc: 0, v: 0, t: 0, raf: 0, active: false, x: 0 };
  const cellH = () => (host.clientHeight / term.rows) || 16;
  const wheel = (lines, clientX, clientY) => {
    if (!lines) return;
    if (term.modes.mouseTrackingMode === "none") { term.scrollLines(lines); return; }
    const r = host.getBoundingClientRect();
    const col = Math.max(1, Math.min(term.cols, Math.floor((clientX - r.left) / (r.width / term.cols)) + 1));
    const row = Math.max(1, Math.min(term.rows, Math.floor((clientY - r.top) / cellH()) + 1));
    const btn = lines < 0 ? 64 : 65; // wheel up / down
    send(`\x1b[<${btn};${col};${row}M`.repeat(Math.abs(lines)));
  };
  // Lines per cell of finger travel: 3 at rest (a desktop wheel notch),
  // rising to 9 on a fast drag or flick.
  const SCROLL_GAIN = 3;
  const step = (dy, x, y) => {
    sc.acc += dy;
    const speed = Math.min(2, Math.abs(sc.v)); // px per ms
    const h = cellH() / (SCROLL_GAIN * (1 + speed));
    const lines = Math.trunc(sc.acc / h);
    if (lines) { sc.acc -= lines * h; wheel(-lines, x, y); } // finger down = scroll back (up)
  };
  host.addEventListener("touchstart", (e) => {
    if (e.touches.length !== 1) return;
    cancelAnimationFrame(sc.raf);
    const t = e.touches[0];
    Object.assign(sc, { y: t.clientY, x: t.clientX, y0: t.clientY, acc: 0, v: 0, t: performance.now(), active: false });
  }, { passive: true });
  host.addEventListener("touchmove", (e) => {
    if (e.touches.length !== 1) return;
    const t = e.touches[0];
    if (!sc.active && Math.abs(t.clientY - sc.y0) < 8) return; // still a tap
    sc.active = true;
    e.preventDefault();
    const now = performance.now();
    const dy = t.clientY - sc.y;
    sc.v = dy / Math.max(1, now - sc.t); // px per ms
    sc.y = t.clientY; sc.x = t.clientX; sc.t = now;
    step(dy, t.clientX, t.clientY);
  }, { passive: false });
  host.addEventListener("touchend", () => {
    if (!sc.active) return;
    sc.active = false;
    let v = sc.v * 16; // px per frame
    if (Math.abs(v) < 1.5) return;
    const glide = () => {
      v *= 0.965;
      sc.v = v / 16;
      if (Math.abs(v) < 0.5) return;
      step(v, sc.x, sc.y);
      sc.raf = requestAnimationFrame(glide);
    };
    sc.raf = requestAnimationFrame(glide);
  }, { passive: true });

  // Paste: mobile browsers don't hand the terminal a paste. Read the
  // clipboard (iOS asks the first time); if that's refused, a text box the
  // user can long-press-paste into. term.paste() sends it as a real paste
  // (bracketed when the program asked for that).
  async function pasteClipboard() {
    let text = "";
    try { text = await navigator.clipboard.readText(); } catch {}
    if (!text) text = window.prompt("Paste into the terminal:") || "";
    if (text) term.paste(text);
    term.focus();
  }

  // Key bar gestures. Pointer events drive everything; touchstart/mousedown
  // are cancelled so the bar never takes focus (and the iOS keyboard) away
  // from the terminal.
  const bar = root.querySelector(".keybar");
  bar.addEventListener("touchstart", (e) => e.preventDefault(), { passive: false });
  bar.addEventListener("mousedown", (e) => e.preventDefault());
  let g = null; // the gesture in progress
  const lastTap = { ctrl: 0, alt: 0 };
  const stopRepeat = () => { if (g) { clearTimeout(g.delay); clearInterval(g.every); g.delay = g.every = null; } };
  const dirOf = (dx, dy) => (Math.hypot(dx, dy) < SWIPE ? null : Math.abs(dx) > Math.abs(dy) ? (dx > 0 ? "right" : "left") : (dy > 0 ? "down" : "up"));
  const showDir = (el, dir) => { el.dataset.dir = dir || ""; };
  bar.addEventListener("pointerdown", (e) => {
    const el = e.target.closest("[data-kb]");
    if (!el) return;
    e.preventDefault();
    try { el.setPointerCapture(e.pointerId); } catch {}
    g = { el, key: KEYS[el.dataset.kb], x: e.clientX, y: e.clientY, id: e.pointerId, dir: null };
    el.classList.add("down");
  });
  bar.addEventListener("pointermove", (e) => {
    if (!g || e.pointerId !== g.id) return;
    const dir = dirOf(e.clientX - g.x, e.clientY - g.y);
    if (g.key.arrows) {
      if (dir === g.dir) return;
      stopRepeat();
      g.dir = dir;
      showDir(g.el, dir);
      if (!dir) return;   // back to the middle: stop
      press(arrowSeq(dir));
      g.delay = setTimeout(() => { g.every = setInterval(() => press(arrowSeq(dir)), REPEAT_EVERY); }, REPEAT_DELAY);
      return;
    }
    g.el.classList.toggle("alt", dir === "up" && !!g.key.up);
  });
  const end = (e, cancelled) => {
    if (!g || e.pointerId !== g.id) return;
    const { el, key, x, y } = g;
    stopRepeat();
    el.classList.remove("down", "alt");
    if (key.arrows) showDir(el, null);
    g = null;
    if (cancelled || key.arrows) return;
    const swipedUp = dirOf(e.clientX - x, e.clientY - y) === "up";
    if (swipedUp && key.up) return press(key.up.send);
    if (key.paste) return pasteClipboard();
    if (key.mod) {
      const now = Date.now();
      const m = key.mod;
      mods[m] = mods[m] !== "off" ? (now - lastTap[m] < 350 && mods[m] === "once" ? "lock" : "off") : "once";
      lastTap[m] = now;
      return paintMods();
    }
    press(key.send);
  };
  bar.addEventListener("pointerup", (e) => end(e, false));
  bar.addEventListener("pointercancel", (e) => end(e, true));

  const proto = location.protocol === "https:" ? "wss" : "ws";
  const qs = new URLSearchParams({ pane: paneId, cols: term.cols, rows: term.rows });
  const ws = new WebSocket(`${proto}://${location.host}/api/terminal?${qs}`);
  ws.binaryType = "arraybuffer";
  V.ws = ws;
  ws.onopen = () => { root.classList.add("live"); term.focus(); };
  renderTray();
  ws.onmessage = (e) => term.write(typeof e.data === "string" ? e.data : new Uint8Array(e.data));
  ws.onclose = () => {
    if (!V || V.ws !== ws || V.closed) return;
    root.classList.remove("live");
    const ov = root.querySelector(".term-overlay");
    ov.hidden = false;
    ov.innerHTML = `<div><p>Disconnected.</p><button class="btn btn-accent" data-t="reconnect">Reconnect</button> <button class="btn btn-ghost" data-t="detach">Close</button></div>`;
  };
}

// ── approvals over the terminal ────────────────────────────────────────
// Pending approvals float over the top of the terminal (above the keyboard),
// this session's first. "Later" tucks one away into the top-bar pill.
function attachedSessionId() {
  for (const sp of S.data?.herdr?.spaces || []) for (const t of sp.tabs) for (const p of t.panes) {
    if (p.paneId === V?.paneId) return p.agent?.sessionId;
  }
  return undefined;
}

function renderTray() {
  if (!V) return;
  const tray = V.root.querySelector(".tray");
  const pill = V.root.querySelector(".tray-pill");
  const mine = attachedSessionId();
  const all = (S.data?.pending || []).slice().sort((a, b) => (b.sessionId === mine) - (a.sessionId === mine) || a.createdAt - b.createdAt);
  for (const p of all) {
    if (!V.tray.seen.has(p.id)) { V.tray.seen.add(p.id); if (navigator.vibrate) navigator.vibrate([40, 30, 40]); }
  }
  const shown = all.filter((p) => !V.tray.hidden.has(p.id));
  pill.hidden = all.length === 0 || shown.length > 0;
  pill.textContent = `● ${all.length}`;
  pill.title = `${all.length} approval${all.length === 1 ? "" : "s"} waiting`;
  if (!shown.length) { tray.hidden = true; tray.innerHTML = ""; return; }
  V.tray.idx = Math.max(0, Math.min(shown.length - 1, V.tray.idx));
  const p = shown[V.tray.idx];
  const isExec = p.kind === "exec";
  const text = isExec ? p.command || p.title : p.title;
  const creds = p.credentials == null ? (isExec ? `<span class="warn"><b>ALL</b> creds</span>` : "") : p.credentials.length ? esc(p.credentials.join(", ")) : "no creds";
  const here = p.sessionId && p.sessionId === mine;
  const who = (p.target === "mac" ? `<b class="mac">on ${esc(p.host || "your Mac")}</b> · ` : "") + (here ? `<span class="ok">this session</span>` : esc(sessionTitle(p.sessionId)));
  tray.hidden = false;
  tray.innerHTML = `<div class="tray-card${here ? " here" : ""}">
    <div class="tray-head">
      <span class="chip ${kindClass(p)}">${kindLabel(p.kind, p.target)}</span><b>#${p.id}</b>
      ${shown.length > 1 ? `<span class="tray-nav"><button data-t="tray-prev"${V.tray.idx === 0 ? " disabled" : ""}>‹</button>${V.tray.idx + 1}/${shown.length}<button data-t="tray-next"${V.tray.idx === shown.length - 1 ? " disabled" : ""}>›</button></span>` : ""}
      <span class="countdown" data-exp="${p.expiresAt}"></span>
    </div>
    <button class="tray-cmd${V.tray.open ? " open" : ""}" data-t="tray-expand">${esc(text)}</button>
    <div class="tray-meta">${who}${p.profile ? ` · ${esc(p.profile)}` : ""}${creds ? ` · ${creds}` : ""}${p.reason ? `<div class="tray-reason">${esc(p.reason)}</div>` : ""}</div>
    <div class="tray-actions">
      <button class="btn btn-small btn-ghost" data-t="tray-later" data-id="${p.id}">Later</button>
      <button class="btn btn-small btn-deny" data-t="tray-deny" data-id="${p.id}">✗ Deny</button>
      <button class="btn btn-small btn-approve" data-t="tray-approve" data-id="${p.id}">✓ Approve</button>
    </div></div>`;
  if (typeof tick === "function") tick();
}

async function trayDecide(id, d) {
  V.root.querySelectorAll(".tray-actions button").forEach((b) => (b.disabled = true));
  await decide([id], d);
  V?.term.focus();
}

function teardown() {
  if (!V) return;
  V.closed = true;
  try { V.ws?.close(); } catch {}
  window.visualViewport?.removeEventListener("resize", V.layout);
  window.visualViewport?.removeEventListener("scroll", V.layout);
  window.removeEventListener("resize", V.layout);
  V.term.dispose();
  V.root.remove();
  document.body.classList.remove("term-open");
  V = null;
}

// Hooks for app.js's click delegation.
window.eldarTerm = { openTerminal, ceremony, teardown, prepare, onState: renderTray, status: () => apiRaw("GET", "/api/passkey") };
