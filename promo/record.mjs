// Records the promo's clips: the real web UI (phone) and TUI, driven by real
// taps and keystrokes against demo-admin's made-up state, while the control
// API plays the agents. Each scene becomes public/clips/<scene>.mp4, and
// public/clips/manifest.json gives each clip's length and the moments the
// composition syncs its captions to. Run inside the promo image by
// scripts/promo, with the Go binaries in /work/bin.
import { chromium } from "playwright";
import { spawn, execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

const BIN = "/work/bin", WORK = "/tmp/rec", OUT = "/repo/promo/public/clips";
const SOCK = "/tmp/eldar-demo.sock", WEB = "http://127.0.0.1:18090/", CTL = "http://127.0.0.1:18099";
const S = { billing: "1b262412-8554-4305-89aa-020b76e8c838", docs: "a94f0faa-a53d-4502-b856-5c4e7b455bab" };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const procs = [];
const start = (cmd, args) => procs.push(spawn(cmd, args, { stdio: ["ignore", "ignore", "inherit"] }));
process.on("exit", () => {
  procs.forEach((p) => p.kill());
  try { execFileSync("tmux", ["kill-server"], { stdio: "ignore" }); } catch {}
});

async function until(what, fn) {
  for (let i = 0; i < 150; i++) {
    try { if (await fn()) return; } catch {}
    await sleep(100);
  }
  throw new Error("timed out waiting for " + what);
}
const ctl = (p, body) => fetch(CTL + p, { method: "POST", body: JSON.stringify(body) }).then((r) => r.json());
const ffmpeg = (args) => execFileSync("ffmpeg", ["-v", "error", "-y", ...args], { stdio: "inherit" });

// encode turns timed stills into a constant-rate clip.
function encode(name, frames, end) {
  const dir = path.join(WORK, name);
  let list = "ffconcat version 1.0\n";
  frames.forEach((f, i) => {
    const next = i + 1 < frames.length ? frames[i + 1].t : end;
    list += `file '${f.file}'\nduration ${Math.max(next - f.t, 0.001).toFixed(4)}\n`;
  });
  list += `file '${frames.at(-1).file}'\n`;
  fs.writeFileSync(path.join(dir, "list.txt"), list);
  ffmpeg(["-f", "concat", "-safe", "0", "-i", path.join(dir, "list.txt"),
    "-vf", "fps=30,scale=trunc(iw/2)*2:trunc(ih/2)*2,format=yuv420p",
    "-c:v", "libx264", "-preset", "slow", "-crf", "12", "-movflags", "+faststart", path.join(OUT, name + ".mp4")]);
}

const manifest = {};

// recordPage captures a page through the DevTools screencast (sharper than
// Playwright's video) while scene() plays.
async function recordPage(page, name, scene) {
  const dir = path.join(WORK, name);
  fs.mkdirSync(dir, { recursive: true });
  const cdp = await page.context().newCDPSession(page);
  const vp = page.viewportSize(), dpr = await page.evaluate(() => devicePixelRatio);
  const frames = [], marks = {};
  let t0 = 0;
  cdp.on("Page.screencastFrame", ({ data, metadata, sessionId }) => {
    cdp.send("Page.screencastFrameAck", { sessionId }).catch(() => {});
    if (!t0) return;
    const file = `f${String(frames.length).padStart(5, "0")}.jpg`;
    fs.writeFileSync(path.join(dir, file), Buffer.from(data, "base64"));
    frames.push({ t: Math.max(metadata.timestamp - t0, 0), file });
  });
  await cdp.send("Page.startScreencast", { format: "jpeg", quality: 95, maxWidth: vp.width * dpr, maxHeight: vp.height * dpr });
  // Nudge a repaint so the clip opens on a frame, then start the clock.
  await page.evaluate(() => document.body.style.setProperty("--rec", String(Math.random())));
  t0 = Date.now() / 1000;
  await scene((m) => (marks[m] = Date.now() / 1000 - t0));
  const end = Date.now() / 1000 - t0;
  await cdp.send("Page.stopScreencast");
  frames[0].t = 0;
  encode(name, frames, end);
  manifest[name] = { duration: end, width: vp.width * dpr, height: vp.height * dpr, marks };
  console.log(`${name}: ${end.toFixed(1)}s, ${frames.length} frames`);
}

// The TUI: sampled from tmux as it changes, then replayed into xterm.js.
const tmux = (...a) => execFileSync("tmux", a, { encoding: "utf8" });
const key = (...k) => tmux("send-keys", "-t", "tui", ...k);
const tuiClips = [];
async function recordTUI(name, scene) {
  const frames = [], marks = {};
  const t0 = Date.now() / 1000;
  let last = "";
  const grab = () => {
    const s = tmux("capture-pane", "-t", "tui", "-e", "-p");
    if (s !== last) frames.push({ t: Date.now() / 1000 - t0, ansi: (last = s) });
  };
  grab();
  const iv = setInterval(grab, 40);
  await scene((m) => (marks[m] = Date.now() / 1000 - t0));
  clearInterval(iv);
  const end = Date.now() / 1000 - t0;
  tuiClips.push({ name, frames, end });
  manifest[name] = { duration: end, marks };
  console.log(`${name}: ${end.toFixed(1)}s, ${frames.length} screens`);
}

async function renderTUI(browser) {
  const page = await browser.newPage({ deviceScaleFactor: 2 });
  await page.goto("file:///tmp/rec/tui.html", { waitUntil: "networkidle" });
  await page.evaluate(() => window.show(""));
  if (!(await page.evaluate(() => document.fonts.check("15px 'JetBrains Mono'")))) throw new Error("JetBrains Mono didn't load");
  await page.addStyleTag({ content: ".xterm-rows { letter-spacing: 0 !important; }" }); // see docs/screenshots.md
  for (const { name, frames, end } of tuiClips) {
    const dir = path.join(WORK, name);
    fs.mkdirSync(dir, { recursive: true });
    for (const [i, f] of frames.entries()) {
      await page.evaluate((a) => window.show(a), f.ansi.replace(/\n$/, "").replaceAll("\n", "\r\n"));
      f.file = `f${String(i).padStart(5, "0")}.png`;
      await page.locator("#t").screenshot({ path: path.join(dir, f.file) });
    }
    encode(name, frames, end);
    const box = await page.locator("#t").boundingBox();
    Object.assign(manifest[name], { width: Math.round(box.width * 2), height: Math.round(box.height * 2) });
  }
  await page.close();
}

// A finger: a soft dot that presses and ripples where the scene taps.
const TOUCH = `
addEventListener("DOMContentLoaded", () => {
  const st = document.createElement("style");
  st.textContent = \`.__touch{position:fixed;width:46px;height:46px;margin:-23px 0 0 -23px;border-radius:50%;pointer-events:none;z-index:2147483647;
    background:rgba(255,255,255,.28);border:2px solid rgba(255,255,255,.85);box-shadow:0 0 18px rgba(187,154,247,.6);
    transition:transform .18s ease-out,opacity .45s ease-out;transform:scale(.4);opacity:0}
    .__touch.down{transform:scale(1);opacity:1}.__touch.up{transform:scale(1.9);opacity:0;transition:transform .5s ease-out,opacity .5s ease-out}
    .xterm-rows{letter-spacing:0!important}\`;
  document.head.append(st);
});
window.__touch = (x, y, phase) => {
  let d = document.querySelector(".__touch");
  if (!d) { d = document.createElement("div"); d.className = "__touch"; document.body.append(d); }
  d.style.left = x + "px"; d.style.top = y + "px";
  d.className = "__touch " + phase;
  if (phase === "up") setTimeout(() => d.remove(), 550);
};`;

async function tap(page, sel) {
  const loc = page.locator(sel).first();
  await loc.scrollIntoViewIfNeeded();
  const b = await loc.boundingBox();
  const x = b.x + b.width / 2, y = b.y + b.height / 2;
  await page.evaluate(([x, y]) => window.__touch(x, y, "down"), [x, y]);
  await sleep(260);
  await loc.tap();
  await page.evaluate(([x, y]) => window.__touch(x, y, "up"), [x, y]);
}
const glide = (page, sel) => page.evaluate((s) => document.querySelector(s).scrollIntoView({ behavior: "smooth", block: "center" }), sel);

// ── setup ──────────────────────────────────────────────────────────────
fs.rmSync(WORK, { recursive: true, force: true });
fs.rmSync(OUT, { recursive: true, force: true });
fs.mkdirSync(WORK, { recursive: true });
fs.mkdirSync(OUT, { recursive: true });
// The TUI's replay page: the web UI's own xterm.js and font.
const VENDOR = "/repo/supervisor/web/static/vendor";
for (const f of ["xterm.js", "xterm.css", "addon-unicode11.js", "JetBrainsMono-Regular.woff2", "JetBrainsMono-Bold.woff2"]) {
  fs.copyFileSync(path.join(VENDOR, f), path.join(WORK, f));
}
fs.writeFileSync(path.join(WORK, "tui.html"), `<!doctype html><meta charset="utf-8">
<style>
  @font-face { font-family: "JetBrains Mono"; font-weight: 400; src: url(JetBrainsMono-Regular.woff2); }
  @font-face { font-family: "JetBrains Mono"; font-weight: 700; src: url(JetBrainsMono-Bold.woff2); }
  body { margin: 0; background: #1a1b26; display: inline-block; }
  #t { padding: 10px 14px; background: #1a1b26; display: inline-block; }
</style>
<link rel="stylesheet" href="xterm.css">
<div id="t"></div>
<script src="xterm.js"></script><script src="addon-unicode11.js"></script>
<script>
  const t = new Terminal({ cols: 120, rows: 34, fontSize: 15, fontFamily: "'JetBrains Mono', monospace", allowProposedApi: true,
    theme: { background: "#1a1b26", foreground: "#c0caf5" } });
  t.loadAddon(new Unicode11Addon.Unicode11Addon()); t.unicode.activeVersion = "11";
  // xterm.js measures its cell when it opens: the font must be in first.
  const ready = Promise.all([document.fonts.load("15px 'JetBrains Mono'"), document.fonts.load("bold 15px 'JetBrains Mono'")])
    .then(() => t.open(document.getElementById("t")));
  window.show = (a) => ready.then(() => new Promise((done) => { t.reset(); t.write("\\x1b[?25l" + a, done); }));
</script>`);
start(`${BIN}/demo-admin`, ["-control", "127.0.0.1:18099", SOCK]);
await until("demo-admin", () => fs.existsSync(SOCK));
fs.mkdirSync("/tmp/web-state", { recursive: true });
start(`${BIN}/supervisor`, ["web", "--dev-listen", "127.0.0.1:18090", "--socket", SOCK, "--allow-user", "demo@example.com", "--state", "/tmp/web-state"]);
await until("the web UI", () => fetch(WEB).then((r) => r.ok));
tmux("new-session", "-d", "-s", "tui", "-x", "120", "-y", "34", `TERM=xterm-256color COLORTERM=truecolor ${BIN}/supervisor --socket ${SOCK}`);
await sleep(2500);

const browser = await chromium.launch();
const phone = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, isMobile: true, hasTouch: true, colorScheme: "dark" });
await phone.addInitScript(TOUCH);
const page = await phone.newPage();
await page.goto(WEB);
await page.waitForSelector("#tabs button");
await sleep(1500);

// ── scenes, in story order ─────────────────────────────────────────────
// Approvals: one for the Mac is approved; a new request arrives and is denied.
await recordPage(page, "approvals", async (mark) => {
  await sleep(1400);
  await glide(page, "#p42");
  await sleep(900);
  mark("approve");
  await tap(page, '#p42 [data-action="decide"][data-d="approved"]');
  await sleep(1600);
  mark("arrive");
  await ctl("/demo/request", { session: S.docs, command: "wrangler pages deploy ./site", credentials: ["CLOUDFLARE_API_TOKEN"],
    reason: "Publish the updated API docs", cwd: "/home/eldar/workspace/projects/docs" });
  await sleep(500);
  await page.evaluate(() => scrollTo({ top: document.body.scrollHeight, behavior: "smooth" }));
  await sleep(1800);
  mark("deny");
  await tap(page, '.card.pending:last-of-type [data-action="decide"][data-d="denied"]');
  await sleep(1800);
});

// The TUI: approval #41 becomes a grant for its session (git push → GitHub
// token for an hour), and that session's next GitHub command runs unasked.
await recordTUI("grant", async (mark) => {
  key("1");
  await sleep(1500);
  mark("editor");
  key("g");
  await sleep(3200);
  mark("granted");
  key("Enter");
  await sleep(1400);
  await ctl("/demo/request", { session: S.billing, command: "gh pr create --fill", credentials: ["GITHUB_TOKEN"],
    reason: "Open the PR for the retry fix", cwd: "/home/eldar/workspace/projects/api" });
  key("3");
  mark("grants");
  await sleep(2400);
  key("5");
  mark("activity");
  await sleep(3200);
});

// Sessions → billing-retries → its terminal: the push went through, the PR is
// open, and it's asking for a log check, approved from the tray.
await page.evaluate(() => scrollTo(0, 0));
await page.locator('#tabs button[data-tab="sessions"]').first().click();
await sleep(1500);
await recordPage(page, "terminal", async (mark) => {
  await sleep(1800);
  mark("session");
  await tap(page, `[data-action="session"][data-id="${S.billing}"]`);
  await sleep(1300);
  await tap(page, '[data-action="terminal"]');
  await page.waitForSelector(".term-view.live");
  mark("terminal");
  await sleep(3000);
  mark("tray");
  await tap(page, '[data-t="tray-approve"][data-id="43"]');
  await sleep(3200);
});

// Chat with Eldar.
await page.locator('[data-t="detach"]').first().click();
await page.locator('#tabs button[data-tab="chat"]').first().click();
await sleep(1500);
await recordPage(page, "chat", async (mark) => {
  await sleep(900);
  await tap(page, "#chat-input");
  mark("type");
  await page.keyboard.type("How's the retry fix going?", { delay: 55 });
  await sleep(400);
  await page.keyboard.press("Enter");
  await sleep(1600);
  mark("reply");
  await ctl("/demo/eldar", { text: "PR #87 has the fix: failures late in the day were scheduled in the past, so they were skipped. That was 312 renewals yesterday. Once it's deployed, they retry on the next run." });
  await sleep(4200);
});

// The audit log, with everything above in it.
await recordTUI("audit", async (mark) => {
  key("1");
  await sleep(300);
  key("5");
  mark("audit");
  await sleep(1500);
  for (let i = 0; i < 4; i++) {
    key("j");
    await sleep(450);
  }
  await sleep(1500);
});

await renderTUI(browser);
await browser.close();
fs.writeFileSync(path.join(OUT, "manifest.json"), JSON.stringify(manifest, null, 2));
process.exit(0);
