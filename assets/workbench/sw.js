/* Workbench service worker core, imported by /jazz/, /trumpets/ and
 * /commonplace/workbench-sw.js so each private app is its own installable
 * scope. It shows Web Push notifications (no message content: they only say
 * something is waiting; the daily fix list says what failed, in counts),
 * answers approvals from the notification's buttons, and keeps a
 * network-first copy of the app pages so the sheet opens offline and its
 * outbox can queue. Only the allowlisted static shell pages and the sheet's
 * own public assets are cached (sw-cache-policy.js); APIs, the highlight
 * service and its PDF library, films, share links, any other page and any
 * response of another type are left to the network and never stored. */
"use strict";
importScripts("/assets/workbench/sw-cache-policy.js");
// v2: v1 could also keep private navigations (library PDFs, films); activate deletes it.
const WB_CACHE = "workbench-shell-v2";
const WB_API = "/workbench/api/v1/workbench";

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => {
  event.waitUntil((async () => {
    for (const key of await caches.keys()) if (key.startsWith("workbench-shell-") && key !== WB_CACHE) await caches.delete(key);
    await self.clients.claim();
  })());
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  const kind = self.WorkbenchCachePolicy.requestKind(req, location.origin);
  if (!kind) return; // private, cross-origin or not part of the shell: straight to the network
  event.respondWith((async () => {
    try {
      const res = await fetch(req);
      // Only complete same-origin shell HTML and Workbench assets: never redirects, no-store or other types.
      if (self.WorkbenchCachePolicy.shouldCache(kind, res)) {
        const cache = await caches.open(WB_CACHE);
        await cache.put(req, res.clone());
      }
      return res;
    } catch (err) {
      const hit = await caches.match(req, { ignoreSearch: req.mode === "navigate" });
      if (hit) return hit;
      throw err;
    }
  })());
});

self.addEventListener("push", (event) => {
  let data = {};
  try { data = event.data ? event.data.json() : {}; } catch { data = {}; }
  // Approve / Not now only when the server allows it (never for GitHub writes).
  const actions = data.kind === "approval" && data.approvalId && data.actions ? [{ action: "approve", title: "Approve" }, { action: "reject", title: "Not now" }] : [];
  event.waitUntil(self.registration.showNotification(data.title || "Workbench", {
    // The fix list's all-clear days arrive quietly (no sound or vibration).
    body: data.body || "Something is waiting.", tag: data.tag || "workbench", renotify: !data.quiet, silent: !!data.quiet,
    data: { url: data.url || "/jazz/", kind: data.kind || "", approvalId: data.approvalId || "", threadId: data.threadId || "" }, actions,
  }));
});

self.addEventListener("notificationclick", (event) => {
  const n = event.notification;
  const data = n.data || {};
  n.close();
  event.waitUntil((async () => {
    if ((event.action === "approve" || event.action === "reject") && data.approvalId) {
      const res = await fetch(`${WB_API}/approvals/${encodeURIComponent(data.approvalId)}`, {
        method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ decision: event.action, deviceId: "notification" }),
      }).catch(() => null);
      if (res && (res.ok || res.status === 409)) return; // decided here, or already on another device
    }
    const target = new URL(data.url || "/jazz/", location.origin);
    for (const client of await self.clients.matchAll({ type: "window", includeUncontrolled: true })) {
      if (new URL(client.url).pathname === target.pathname && "focus" in client) {
        // The fix list opens its page section; everything else opens the thread.
        client.postMessage(data.kind === "fixlist" ? { type: "workbench-fixlist", hash: target.hash } : { type: "workbench-open", threadId: data.threadId });
        return client.focus();
      }
    }
    return self.clients.openWindow(target.href);
  })());
});
