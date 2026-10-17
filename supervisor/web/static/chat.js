// Chat with Eldar, the chief-of-staff session. Lives in its own element (not
// <main>), built once: the log grows in place and the composer is never
// re-rendered, so state pushes can't eat a half-typed message.
"use strict";

(() => {
  const C = {
    el: null,
    msgs: [],          // oldest first
    loaded: false,
    loading: false,
    hasMore: false,
    pending: [],       // {tmp, text, failed}
    lastSeen: 0,       // newest id fetched
    toasted: 0,        // newest Eldar id toasted outside the chat tab
    html: "",          // last rendered log
  };
  const coarse = matchMedia("(pointer: coarse)").matches;
  const PAGE = 100;

  // ── Markdown (small, escape-first) ──────────────────────────────────
  function inline(s) {
    return s
      .replace(/`([^`\n]+)`/g, (_, c) => `<code>${c}</code>`)
      .replace(/\*\*([^*\n]+)\*\*/g, "<b>$1</b>")
      .replace(/(^|[\s(])\*([^*\n]+)\*(?=[\s).,!?:;]|$)/g, "$1<i>$2</i>")
      .replace(/\[([^\]\n]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>')
      .replace(/(^|[\s(])(https?:\/\/[^\s<)]+[^\s<).,!?:;])/g, '$1<a href="$2" target="_blank" rel="noopener noreferrer">$2</a>');
  }
  function md(text) {
    const parts = esc(text).split(/```[^\n]*\n?([\s\S]*?)```/g);
    let out = "";
    parts.forEach((p, i) => {
      if (i % 2) { out += `<pre><code>${p.replace(/\n$/, "")}</code></pre>`; return; }
      const lines = p.split("\n");
      let list = null, para = [];
      const flush = () => { if (para.length) out += `<p>${para.map(inline).join("<br>")}</p>`; para = []; };
      const closeList = () => { if (list) out += `</${list}>`; list = null; };
      for (const l of lines) {
        const ul = l.match(/^\s*[-*•]\s+(.*)$/), ol = l.match(/^\s*\d+[.)]\s+(.*)$/);
        if (ul || ol) {
          flush();
          const kind = ul ? "ul" : "ol";
          if (list !== kind) { closeList(); out += `<${kind}>`; list = kind; }
          out += `<li>${inline((ul || ol)[1])}</li>`;
        } else if (!l.trim()) { flush(); closeList(); }
        else { closeList(); para.push(l); }
      }
      flush(); closeList();
    });
    return out;
  }

  // ── time labels ─────────────────────────────────────────────────────
  const clock = (ts) => new Date(ts).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  function dayLabel(ts) {
    const d = new Date(ts), now = new Date();
    const y = new Date(now); y.setDate(now.getDate() - 1);
    if (d.toDateString() === now.toDateString()) return "Today";
    if (d.toDateString() === y.toDateString()) return "Yesterday";
    return d.toLocaleDateString([], { weekday: "short", month: "short", day: "numeric", ...(d.getFullYear() !== now.getFullYear() ? { year: "numeric" } : {}) });
  }

  // ── DOM ─────────────────────────────────────────────────────────────
  function build() {
    const el = document.createElement("section");
    el.className = "chat";
    el.hidden = true;
    el.innerHTML = `
      <div class="chat-log" id="chat-log" aria-live="polite"></div>
      <div class="chat-status" id="chat-status" hidden></div>
      <form class="chat-compose" id="chat-form">
        <textarea id="chat-input" rows="1" placeholder="Message Eldar" autocomplete="off" enterkeyhint="send"></textarea>
      </form>`;
    document.body.append(el);
    C.el = el;
    C.grow = () => grow();
    const input = $("#chat-input"), form = $("#chat-form"), log = $("#chat-log");
    const grow = () => {
      // Measured while hidden, scrollHeight is 0; show() calls this again.
      if (C.el.hidden) return;
      input.style.height = "auto";
      input.style.height = Math.min(Math.max(input.scrollHeight + 2, 40), 160) + "px";
      try { localStorage.setItem("chat-draft", input.value); } catch {}
    };
    input.addEventListener("input", grow);
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); form.requestSubmit(); }
    });
    input.addEventListener("focus", () => setTimeout(() => { layout(); scrollEnd(true); }, 300));
    input.addEventListener("blur", () => layout());
    form.addEventListener("submit", (e) => { e.preventDefault(); send(); });
    log.addEventListener("click", (e) => {
      const b = e.target.closest("[data-chat]");
      if (!b) return;
      if (b.dataset.chat === "more") loadEarlier();
      if (b.dataset.chat === "retry") retry(Number(b.dataset.tmp));
    });
    log.addEventListener("scroll", () => { if (log.scrollTop < 40 && C.hasMore && !C.loading) loadEarlier(); }, { passive: true });
    try { input.value = localStorage.getItem("chat-draft") || ""; } catch {}
    grow();
    fullH = innerHeight;
    if (window.visualViewport) {
      visualViewport.addEventListener("resize", trackHeight);
      visualViewport.addEventListener("resize", layout);
      visualViewport.addEventListener("scroll", layout);
    }
    window.addEventListener("resize", layout);
  }

  // Keyboard up = the chat's box is focused and the visual viewport is clearly
  // shorter than the layout one. Focus alone isn't enough: iOS's Done can hide
  // the keyboard and leave the box focused.
  function keyboardUp() {
    const vv = window.visualViewport;
    return coarse && !!vv && document.activeElement === $("#chat-input") &&
      vv.height < document.documentElement.clientHeight - 100;
  }

  // While typing, iOS pans the page under the keyboard, and fixed elements are
  // then painted away from where they're hit-tested. So the chat is placed in
  // page coordinates over exactly the visible area, top bar and tabs hidden.
  function layout() {
    if (!visible()) return;
    const up = keyboardUp();
    const was = document.body.classList.contains("kb-open");
    if (up) {
      const vv = window.visualViewport;
      const root = document.documentElement.style;
      root.setProperty("--vvh", vv.height + "px");
      root.setProperty("--vvp", vv.pageTop + "px");
    }
    document.body.classList.toggle("kb-open", up);
    if (was && !up) { settle(); for (const ms of [150, 400, 800]) setTimeout(settle, ms); }
  }
  // After the keyboard goes, iOS (home-screen app) sometimes leaves the page
  // short of the screen by the status bar's height: a gap under the tab bar.
  // It re-measures once the page is taller than the screen, which is why a
  // switch to a longer tab fixed it; so lengthen the page until it does.
  let fullH = 0, kick = 0;
  function settle() {
    if (!visible() || keyboardUp()) return;
    window.scrollTo(0, 0);
    if (innerHeight < fullH - 20) {
      document.body.style.minHeight = fullH + 200 + "px";
      void document.body.offsetHeight;
      clearTimeout(kick);
      kick = setTimeout(unkick, 600);
    } else unkick();
    scrollEnd();
  }
  function unkick() {
    if (!document.body.style.minHeight) return;
    document.body.style.minHeight = "";
    window.scrollTo(0, 0);
  }
  function trackHeight() {
    if (!keyboardUp()) fullH = Math.max(fullH, innerHeight);
    if (document.body.style.minHeight && innerHeight >= fullH - 20) { clearTimeout(kick); unkick(); }
  }

  function atBottom() {
    const log = $("#chat-log");
    return log.scrollHeight - log.scrollTop - log.clientHeight < 80;
  }
  function scrollEnd(force) {
    const log = $("#chat-log");
    if (force || atBottom()) log.scrollTop = log.scrollHeight;
  }

  function bubble(m, opts = {}) {
    const mine = m.from === "ben";
    const body = md(m.text);
    const meta = opts.pending
      ? opts.failed ? `<button class="chat-retry" data-chat="retry" data-tmp="${opts.tmp}">Not sent · retry</button>` : "Sending…"
      : clock(m.ts);
    return `<div class="msg ${mine ? "mine" : "theirs"}${opts.pending ? " pending" : ""}${opts.failed ? " failed" : ""}">
      <div class="bubble">${body}</div><div class="msg-meta">${meta}</div></div>`;
  }

  function renderLog(keepPos) {
    const log = $("#chat-log");
    const stick = atBottom();
    const prevH = log.scrollHeight, prevTop = log.scrollTop;
    let html = C.hasMore ? `<button class="chat-more" data-chat="more">${C.loading ? "Loading…" : "Earlier messages"}</button>` : "";
    if (C.loaded && !C.msgs.length && !C.pending.length) {
      html += `<div class="chat-empty"><div class="big-sigil">✦</div><p>Ask Eldar anything. Your other agents reach you here too, when they need you.</p></div>`;
    }
    let day = "", prev = null;
    for (const m of C.msgs) {
      const d = dayLabel(m.ts);
      if (d !== day) { html += `<div class="chat-day">${d}</div>`; day = d; prev = null; }
      const grouped = prev && prev.from === m.from && m.ts - prev.ts < 5 * 60_000;
      html += bubble(m).replace('class="msg ', `class="msg ${grouped ? "grouped " : ""}`);
      prev = m;
    }
    for (const p of C.pending) html += bubble({ from: "ben", text: p.text, ts: Date.now() }, { pending: true, failed: p.failed, tmp: p.tmp });
    const working = S.data?.chat?.eldar?.status === "working";
    if (working) html += `<div class="msg theirs typing"><div class="bubble"><i></i><i></i><i></i></div></div>`;
    if (html === C.html) return;
    C.html = html;
    log.innerHTML = html;
    if (keepPos) log.scrollTop = prevTop + (log.scrollHeight - prevH);
    else if (stick) log.scrollTop = log.scrollHeight;
  }

  function renderStatus() {
    const c = S.data?.chat;
    const st = $("#chat-status");
    if (!st || !c) return;
    // Only problems show; "working" is the typing dots in the log.
    let txt = "", cls = "";
    if (!c.eldar) { txt = "Eldar isn't running — the eldar-primary service restarts it."; cls = "bad"; }
    else if (!c.connected) { txt = "Eldar's chat isn't connected — messages wait and arrive when it is."; cls = "warn"; }
    else if (c.eldar.status === "blocked") { txt = "Eldar is waiting on a prompt in its terminal."; cls = "warn"; }
    st.hidden = !txt;
    st.className = "chat-status " + cls;
    st.textContent = txt;
  }

  // ── data ────────────────────────────────────────────────────────────
  function merge(list) {
    const have = new Set(C.msgs.map((m) => m.id));
    for (const m of list) if (!have.has(m.id)) C.msgs.push(m);
    C.msgs.sort((a, b) => a.id - b.id);
    C.lastSeen = C.msgs.at(-1)?.id || 0;
  }

  async function loadInitial() {
    if (C.loading) return;
    C.loading = true;
    try {
      const r = await api("GET", `/api/chat?limit=${PAGE}`);
      merge(r.messages || []);
      C.hasMore = (r.messages || []).length >= PAGE;
      C.loaded = true;
      renderLog();
      scrollEnd(true);
      // Shown only once filled and scrolled, so it doesn't visibly jump;
      // fonts arriving later reflow it, so pin to the bottom again then.
      $("#chat-log").classList.add("ready");
      document.fonts?.ready.then(() => scrollEnd(true));
      markRead();
    } catch (e) { toast("✗ Chat: " + e.message, "err", 5000); $("#chat-log").classList.add("ready"); }
    finally { C.loading = false; }
  }

  async function loadNew() {
    if (!C.loaded || C.loading) return;
    C.loading = true;
    try {
      const r = await api("GET", `/api/chat?after=${C.lastSeen}&limit=500`);
      merge(r.messages || []);
      renderLog();
      markRead();
    } catch {} finally { C.loading = false; }
  }

  async function loadEarlier() {
    if (C.loading || !C.msgs.length) return;
    C.loading = true;
    renderLog(true);
    try {
      const r = await api("GET", `/api/chat?before=${C.msgs[0].id}&limit=${PAGE}`);
      merge(r.messages || []);
      C.hasMore = (r.messages || []).length >= PAGE;
    } catch {} finally { C.loading = false; renderLog(true); }
  }

  let tmpSeq = 0;
  async function post(p) {
    try {
      const r = await api("POST", "/api/chat", { text: p.text });
      C.pending = C.pending.filter((x) => x !== p);
      if (r.message) merge([r.message]);
    } catch (e) {
      p.failed = true;
      toast("✗ " + e.message, "err", 5000);
    }
    renderLog();
    scrollEnd(true);
  }
  function send() {
    const input = $("#chat-input");
    const text = input.value.trim();
    if (!text) return;
    const p = { tmp: ++tmpSeq, text, failed: false };
    C.pending.push(p);
    input.value = "";
    input.dispatchEvent(new Event("input"));
    if (!coarse) input.focus();
    renderLog();
    scrollEnd(true);
    post(p);
  }
  function retry(tmp) {
    const p = C.pending.find((x) => x.tmp === tmp);
    if (!p) return;
    p.failed = false;
    renderLog();
    post(p);
  }

  let readSent = 0;
  function markRead() {
    const c = S.data?.chat;
    if (!c || !visible() || document.hidden) return;
    const upTo = c.lastId;
    if (upTo > c.readUpTo && upTo > readSent) {
      readSent = upTo;
      api("POST", "/api/chat/read", { upTo }).catch(() => { readSent = 0; });
    }
  }

  const visible = () => C.el && !C.el.hidden;

  // ── hooks from app.js ───────────────────────────────────────────────
  function show() {
    if (!C.el) build();
    if (!C.el.hidden) return;
    C.el.hidden = false;
    document.body.classList.add("chat-open");
    layout();
    C.grow();
    if (!C.loaded) loadInitial(); else { renderLog(); scrollEnd(true); loadNew(); }
    renderStatus();
    if (!coarse) setTimeout(() => $("#chat-input")?.focus(), 50);
  }
  function hide() {
    if (!C.el || C.el.hidden) return;
    C.el.hidden = true;
    document.body.classList.remove("chat-open", "kb-open");
  }
  function onState() {
    const c = S.data?.chat;
    if (!c) return;
    if (!visible()) {
      const last = c.last;
      if (S.primed && last && last.from === "eldar" && last.id > Math.max(C.toasted, c.readUpTo) && C.toasted) {
        toast("✦ Eldar: " + last.text.split("\n")[0].slice(0, 90), "ok", 5000);
      }
      C.toasted = Math.max(C.toasted, c.last?.id || 0, 1);
      return;
    }
    C.toasted = Math.max(C.toasted, c.lastId, 1);
    renderStatus();
    if (c.lastId > C.lastSeen) loadNew();
    else renderLog();
    markRead();
  }

  document.addEventListener("visibilitychange", () => { if (!document.hidden && visible()) { loadNew(); markRead(); } });

  window.eldarChat = { show, hide, onState };
})();
