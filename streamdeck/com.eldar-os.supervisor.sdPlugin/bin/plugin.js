// Stream Deck plugin for the eldar-os supervisor: Approve and Deny keys for
// the requests waiting on Ben.
//
// The plugin is a thin client. The supervisor's device API (on the web UI's
// tailnet node, see supervisor/web/devices.go) sends what each key should
// show, and a press is sent back as an action with the request it showed; the
// supervisor decides whether that's allowed (it turns Approve off while an
// agent can act on this machine). No dependencies: it runs on the Node.js the
// Stream Deck app ships, and talks to the app over a small WebSocket client.
//
// Holding Approve (half a second) opens the supervisor TUI instead: it shows
// the iTerm hotkey window ("Visor"), selects the iTerm tab connected to the
// host's herdr, and asks the supervisor to focus the tab running the TUI there
// (or start one).
//
// Configuration: ~/Library/Application Support/eldar-os/streamdeck.json:
//
//   {"server": "https://supervisor.<tailnet>.ts.net",
//    "terminals": {"host": "eldar-os", "eldar": "eldar-vm"}}
//
// terminals maps each herdr the supervisor can steer (host: the supervisor's
// machine, eldar: Eldar's VM) to the ssh destination of the iTerm tab showing
// it. Optionally, "approveHold": {"herdr": "eldar", "tab": "<name>"} makes the
// long press open a tab by name instead (with an optional "workspace"). The
// device token comes from pairing (press a key, approve the pairing in the
// supervisor) and is kept in the plugin's global settings.

"use strict";

const { execFile } = require("node:child_process");
const crypto = require("node:crypto");
const fs = require("node:fs");
const net = require("node:net");
const os = require("node:os");
const path = require("node:path");

const CONFIG = path.join(os.homedir(), "Library/Application Support/eldar-os/streamdeck.json");
const DEVICE_NAME = "Stream Deck";

const args = {};
for (let i = 2; i < process.argv.length - 1; i += 2) args[process.argv[i].replace(/^-/, "")] = process.argv[i + 1];

// ---- Stream Deck connection (WebSocket client, RFC 6455, text frames only) ----

let sock;
let rx = Buffer.alloc(0);
let partial = [];

function connectDeck() {
  sock = net.connect(Number(args.port), "127.0.0.1");
  const key = crypto.randomBytes(16).toString("base64");
  let open = false;
  sock.on("connect", () => {
    sock.write(`GET / HTTP/1.1\r\nHost: 127.0.0.1:${args.port}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n` +
      `Sec-WebSocket-Key: ${key}\r\nSec-WebSocket-Version: 13\r\n\r\n`);
  });
  sock.on("data", (chunk) => {
    rx = Buffer.concat([rx, chunk]);
    if (!open) {
      const end = rx.indexOf("\r\n\r\n");
      if (end < 0) return;
      if (!/^HTTP\/1\.1 101/.test(rx.subarray(0, end).toString())) process.exit(1);
      rx = rx.subarray(end + 4);
      open = true;
      send({ event: args.registerEvent, uuid: args.pluginUUID });
      send({ event: "getGlobalSettings", context: args.pluginUUID });
    }
    readFrames();
  });
  // The app closes the connection when it quits or unloads the plugin.
  sock.on("close", () => process.exit(0));
  sock.on("error", () => process.exit(1));
}

function readFrames() {
  for (;;) {
    if (rx.length < 2) return;
    const fin = rx[0] & 0x80, op = rx[0] & 0x0f;
    let len = rx[1] & 0x7f, off = 2;
    if (len === 126) { if (rx.length < 4) return; len = rx.readUInt16BE(2); off = 4; }
    else if (len === 127) { if (rx.length < 10) return; len = Number(rx.readBigUInt64BE(2)); off = 10; }
    if (rx[1] & 0x80) off += 4; // the server never masks, but skip a mask if present
    if (rx.length < off + len) return;
    const data = rx.subarray(off, off + len);
    rx = rx.subarray(off + len);
    if (op === 0x8) process.exit(0);
    if (op === 0x9) { frame(0xa, data); continue; }
    if (op === 0x1 || op === 0x0) {
      partial.push(data);
      if (fin) {
        const text = Buffer.concat(partial).toString();
        partial = [];
        try { onDeckMessage(JSON.parse(text)); } catch (e) { log(`bad message: ${e.message}`); }
      }
    }
  }
}

function frame(op, payload) {
  const mask = crypto.randomBytes(4);
  const n = payload.length;
  const head = n < 126 ? Buffer.from([0x80 | op, 0x80 | n])
    : n < 65536 ? Buffer.from([0x80 | op, 0x80 | 126, n >> 8, n & 0xff])
    : Buffer.concat([Buffer.from([0x80 | op, 0x80 | 127]), (() => { const b = Buffer.alloc(8); b.writeBigUInt64BE(BigInt(n)); return b; })()]);
  const body = Buffer.from(payload);
  for (let i = 0; i < n; i++) body[i] ^= mask[i % 4];
  sock.write(Buffer.concat([head, mask, body]));
}

function send(msg) { frame(0x1, Buffer.from(JSON.stringify(msg))); }
function log(message) { send({ event: "logMessage", payload: { message: `[supervisor] ${message}` } }); }

// ---- State ----

const keys = new Map(); // context → "approve" | "deny"
let settings = null;    // global settings: {token}
let status = "starting"; // starting | unconfigured | pair | pairing | offline | live
let view = null;        // the supervisor's DeviceView while live
let stream = null;      // AbortController of the event stream

function config() {
  try { return JSON.parse(fs.readFileSync(CONFIG, "utf8")); } catch { return {}; }
}
function server() { return (config().server || "").replace(/\/+$/, ""); }

function onDeckMessage(m) {
  const action = m.action ? m.action.split(".").pop() : "";
  switch (m.event) {
    case "didReceiveGlobalSettings":
      settings = m.payload.settings || {};
      start();
      break;
    case "willAppear":
      keys.set(m.context, action);
      draw(m.context);
      break;
    case "willDisappear":
      keys.delete(m.context);
      drawn.delete(m.context);
      break;
    case "keyDown":
      if (action === "approve") holdTimer(m.context);
      else run(m.context, press(m.context, action));
      break;
    case "keyUp":
      // Released before the hold fired: an ordinary press.
      if (holds.has(m.context)) { clearTimeout(holds.get(m.context)); holds.delete(m.context); run(m.context, press(m.context, action)); }
      break;
    case "systemDidWakeUp":
      start();
      break;
  }
}

function setStatus(s) {
  status = s;
  drawAll();
}

// start (re)connects to the supervisor with the current settings.
function start() {
  if (stream) stream.abort();
  stream = null;
  view = null;
  if (!server()) return setStatus("unconfigured");
  if (!settings?.token) return setStatus(status === "pairing" ? "pairing" : "pair");
  const ctl = (stream = new AbortController());
  listen(ctl).catch(() => {}).finally(() => {
    if (stream !== ctl) return; // replaced by a newer connection
    setStatus(settings?.token ? "offline" : "pair");
    setTimeout(() => { if (stream === ctl) start(); }, 5000);
  });
}

// listen reads the device event stream until it ends or is aborted.
async function listen(ctl) {
  const res = await fetch(`${server()}/device/events`, { headers: { Authorization: `Bearer ${settings.token}` }, signal: ctl.signal });
  if (res.status === 401) return unpaired();
  if (!res.ok) throw new Error(`events: HTTP ${res.status}`);
  // A view arrives at least every 5 seconds; silence means a dead link.
  let watchdog;
  const alive = () => { clearTimeout(watchdog); watchdog = setTimeout(() => ctl.abort(), 20000); };
  alive();
  const dec = new TextDecoder();
  let buf = "";
  try {
    for await (const chunk of res.body) {
      buf += dec.decode(chunk, { stream: true });
      let i;
      while ((i = buf.indexOf("\n\n")) >= 0) {
        const block = buf.slice(0, i);
        buf = buf.slice(i + 2);
        const ev = /^event: (.*)$/m.exec(block)?.[1];
        const data = /^data: (.*)$/m.exec(block)?.[1];
        alive();
        if (ev === "view") { view = JSON.parse(data); setStatus("live"); }
        else if (ev === "down") setStatus("offline");
        else if (ev === "unpaired") return unpaired();
      }
    }
  } finally {
    clearTimeout(watchdog);
  }
}

function unpaired() {
  log("the supervisor no longer knows this device; pair again");
  settings = {};
  send({ event: "setGlobalSettings", context: args.pluginUUID, payload: settings });
  setStatus("pair");
}

async function press(context, action) {
  if (status === "pair") return pair();
  if (status !== "live") return send({ event: "showAlert", context });
  const ctl = view?.controls?.[action];
  if (!ctl?.enabled) return send({ event: "showAlert", context });
  const res = await fetch(`${server()}/device/actions/${action}`, {
    method: "POST",
    headers: { Authorization: `Bearer ${settings.token}`, "Content-Type": "application/json", "X-Supervisor": "1" },
    body: JSON.stringify({ ref: ctl.ref }),
  });
  if (!res.ok) log(`${action} #${ctl.ref}: HTTP ${res.status} ${await res.text()}`);
  send({ event: res.ok ? "showOk" : "showAlert", context });
}

function run(context, p) {
  p.catch((e) => { log(`${keys.get(context)}: ${e.message}`); send({ event: "showAlert", context }); });
}

// ---- Long press on Approve: open a herdr tab ----

const HOLD_MS = 500;
const holds = new Map(); // context → timer while Approve is down

function holdTimer(context) {
  clearTimeout(holds.get(context));
  holds.set(context, setTimeout(() => {
    holds.delete(context);
    run(context, openHerdr(context, config().approveHold));
  }, HOLD_MS));
}

// openHerdr asks the supervisor to bring up a herdr tab, then the iTerm tab
// showing that herdr. spec "supervisor" (the default) is the supervisor TUI
// in the host's herdr, found by its command and started if it isn't running;
// {herdr, tab, workspace} is a tab by name. Either is allowed whatever
// Approve's state.
async function openHerdr(context, spec = "supervisor") {
  const tui = spec === "supervisor";
  if (!tui && (!spec?.herdr || !spec?.tab)) throw new Error('approveHold must be "supervisor" or {herdr, tab}');
  if (status !== "live") throw new Error(`not connected (${status})`);
  const action = tui ? "supervisor-tui" : "herdr-focus";
  const res = await fetch(`${server()}/device/actions/${action}`, {
    method: "POST",
    headers: { Authorization: `Bearer ${settings.token}`, "Content-Type": "application/json", "X-Supervisor": "1" },
    body: JSON.stringify(tui ? {} : { args: { herdr: spec.herdr, tab: spec.tab, workspace: spec.workspace || "" } }),
  });
  const focused = res.ok || (log(`${action}: HTTP ${res.status} ${await res.text()}`), false);
  await showTerminal(config().terminals?.[tui ? "host" : spec.herdr]); // even if herdr failed: you'll see why
  send({ event: focused ? "showOk" : "showAlert", context });
}

const exec = (file, argv) => new Promise((resolve, reject) =>
  execFile(file, argv, { timeout: 10000 }, (err, stdout, stderr) => (err ? reject(new Error((stderr || err.message).trim())) : resolve(stdout))));

// showTerminal finds the iTerm session running `ssh <dest>` (by its tty, so
// tab order and titles don't matter), selects it, and shows its window,
// revealing the hotkey window if that's where it is.
async function showTerminal(dest) {
  if (!dest) throw new Error("no terminal configured for this herdr");
  const ps = await exec("/bin/ps", ["-axo", "tty=,command="]);
  const tty = ps.split("\n").map((l) => l.trim().split(/\s+/)).find(([t, cmd, ...rest]) =>
    t && t !== "??" && path.basename(cmd || "") === "ssh" && rest.some((a) => a === dest || a.endsWith(`@${dest}`)))?.[0];
  if (!tty) throw new Error(`no iTerm tab is running ssh ${dest}`);
  const script = `on run argv
  set want to item 1 of argv
  tell application "iTerm2"
    repeat with w in windows
      repeat with t in tabs of w
        repeat with s in sessions of t
          if tty of s is want then
            if is hotkey window of w then
              reveal hotkey window w
            else
              activate
              select w
            end if
            select t
            select s
            return "ok"
          end if
        end repeat
      end repeat
    end repeat
    return "missing"
  end tell
end run`;
  const out = (await exec("/usr/bin/osascript", ["-e", script, `/dev/${tty}`])).trim();
  if (out !== "ok") throw new Error(`iTerm has no session on /dev/${tty}`);
}

// pair asks the supervisor to pair this device; Ben approves it there.
async function pair() {
  setStatus("pairing");
  try {
    const res = await fetch(`${server()}/device/pair`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Supervisor": "1" },
      body: JSON.stringify({ name: DEVICE_NAME }),
      signal: AbortSignal.timeout(300000),
    });
    const body = await res.json().catch(() => ({}));
    if (!res.ok || !body.token) throw new Error(body.error || `HTTP ${res.status}`);
    settings = { token: body.token };
    send({ event: "setGlobalSettings", context: args.pluginUUID, payload: settings });
    log(`paired as device #${body.device?.id}`);
    start();
  } catch (e) {
    log(`pairing: ${e.message}`);
    setStatus("pair");
    for (const c of keys.keys()) send({ event: "showAlert", context: c });
  }
}

// ---- Drawing (144×144 SVG per key) ----

const GREEN = "#16a34a", RED = "#dc2626", AMBER = "#d97706", DIM = "#4b5563", BG = "#111111";
const CHECK = (c) => `<polyline points="36,76 60,100 100,56" fill="none" stroke="${c}" stroke-width="14" stroke-linecap="round" stroke-linejoin="round"/>`;
const CROSS = (c) => `<path d="M46 46 L98 98 M98 46 L46 98" stroke="${c}" stroke-width="14" stroke-linecap="round"/>`;
const esc = (s) => String(s).replace(/[&<>"]/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[ch]);

function svg(bg, glyph, label, badge) {
  const text = label ? `<text x="72" y="132" font-family="Helvetica, Arial, sans-serif" font-size="20" font-weight="700" fill="#ffffff" text-anchor="middle">${esc(label)}</text>` : "";
  const dot = badge ? `<circle cx="116" cy="28" r="22" fill="#ffffff"/><text x="116" y="36" font-family="Helvetica, Arial, sans-serif" font-size="${badge > 9 ? 20 : 24}" font-weight="800" fill="${bg === BG ? AMBER : bg}" text-anchor="middle">${badge > 99 ? "99+" : badge}</text>` : "";
  const s = `<svg xmlns="http://www.w3.org/2000/svg" width="144" height="144" viewBox="0 0 144 144"><rect width="144" height="144" rx="18" fill="${bg}"/><g transform="translate(0,-8)">${glyph}</g>${text}${dot}</svg>`;
  return "data:image/svg+xml;charset=utf8," + encodeURIComponent(s);
}

function image(action) {
  const approve = action === "approve";
  const glyph = (c) => (approve ? CHECK(c) : CROSS(c));
  switch (status) {
    case "unconfigured": return svg(BG, glyph(DIM), "Set up");
    case "pair": return svg(BG, glyph(DIM), "Pair");
    case "pairing": return svg(BG, glyph(AMBER), "Approve pairing");
    case "live": break;
    default: return svg(BG, glyph(DIM), "Offline");
  }
  const ctl = view?.controls?.[action] || {};
  if (approve) {
    if (ctl.enabled) return svg(GREEN, glyph("#ffffff"), "Approve", ctl.badge > 1 ? ctl.badge : 0);
    if (ctl.badge > 0) return svg(BG, glyph(AMBER), "Locked", ctl.badge); // waiting, but an agent can act on this Mac
    return svg(BG, glyph(DIM), "");
  }
  return ctl.enabled ? svg(RED, glyph("#ffffff"), "Deny") : svg(BG, glyph(DIM), "");
}

const drawn = new Map(); // context → last image, to skip redundant updates
function draw(context) {
  const img = image(keys.get(context));
  if (drawn.get(context) === img) return;
  drawn.set(context, img);
  send({ event: "setImage", context, payload: { image: img, target: 0 } });
}
function drawAll() { for (const c of keys.keys()) draw(c); }

if (require.main === module) connectDeck();
else module.exports = { svg, CHECK, CROSS, GREEN, RED, AMBER, DIM, BG }; // for previews
