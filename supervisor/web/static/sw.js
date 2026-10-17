// Supervisor service worker: shows approval notifications and handles their
// Approve / Deny actions. No offline caching — the UI is only useful live.

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (e) => e.waitUntil(self.clients.claim()));
self.addEventListener("fetch", () => {});

self.addEventListener("push", (e) => {
  let n = {};
  try { n = e.data ? e.data.json() : {}; } catch { n = { title: "Eldar", body: e.data?.text() }; }
  const opts = {
    body: n.body || "",
    tag: n.tag || "eldar",
    renotify: true,
    requireInteraction: !!n.id,
    icon: "/icon/192.png",
    badge: "/icon/192.png",
    data: { id: n.id || 0, url: n.url || "" },
  };
  if (n.actions && n.id) opts.actions = [{ action: "approve", title: "✓ Approve" }, { action: "deny", title: "✗ Deny" }];
  e.waitUntil(self.registration.showNotification(n.title || "Eldar", opts));
});

async function openApp(id, target) {
  const all = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
  const url = target || (id ? `/#p${id}` : "/");
  for (const c of all) {
    if (new URL(c.url).origin === self.location.origin) {
      await c.focus();
      if (target) c.postMessage({ url: target });
      else if (id) c.postMessage({ focus: id });
      return;
    }
  }
  await self.clients.openWindow(url);
}

self.addEventListener("notificationclick", (e) => {
  const id = e.notification.data?.id;
  e.notification.close();
  if (id && (e.action === "approve" || e.action === "deny")) {
    const decision = e.action === "approve" ? "approved" : "denied";
    e.waitUntil(
      fetch("/api/decide", {
        method: "POST",
        headers: { "X-Supervisor": "1", "Content-Type": "application/json" },
        body: JSON.stringify({ ids: [id], decision }),
      }).then(async (r) => {
        if (r.status === 401) return openApp(id);
        if (!r.ok) {
          const msg = (await r.json().catch(() => ({}))).error || r.statusText;
          await self.registration.showNotification(`#${id} not ${decision}`, { body: msg, tag: `pending-${id}`, icon: "/icon/192.png" });
        }
      }).catch(() => openApp(id)),
    );
    return;
  }
  e.waitUntil(openApp(id, e.notification.data?.url));
});
