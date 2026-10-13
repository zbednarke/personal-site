/* What the Workbench service worker may keep in its offline cache (pure, so
 * node tests can check it; sw.js loads it with importScripts).
 *
 * Only two kinds of request are handled at all:
 *  - "page":  a same-origin GET navigation to an app page (the shell HTML);
 *  - "asset": a same-origin GET under /assets/workbench/ (the sheet's own
 *    public code, styles and icons, which are in the public repo).
 * Everything else, and always anything under a private path (APIs, the
 * highlight service with its PDF library, films, share links, preview
 * objects), is left to the network: the worker never calls respondWith and
 * never caches it.
 *
 * A handled response is stored only when it is a complete 200 from this
 * origin (not redirected, not opaque), does not say no-store, and is the
 * expected type: text/html for a page, script/style/image/manifest for an
 * asset. So a PDF, a video or a JSON body is never kept, even if one is
 * served at an unexpected path. */
(function (root) {
  "use strict";
  const PRIVATE_SEGMENTS = ["/api/", "/highlight-api/", "/films/", "/share/"];
  const PRIVATE_PREFIXES = ["/__preview_objects/"];
  const ASSET_TYPES = /^(text\/css|text\/javascript|application\/javascript|application\/x-javascript|application\/manifest\+json|image\/(png|svg\+xml|webp|x-icon|vnd\.microsoft\.icon))\b/i;

  function isPrivatePath(pathname) {
    const path = String(pathname || "");
    return PRIVATE_SEGMENTS.some((segment) => path.includes(segment)) || PRIVATE_PREFIXES.some((prefix) => path.startsWith(prefix));
  }

  /** "page", "asset" or null (leave it to the network, never cache). */
  function requestKind({ method, url, mode }, origin) {
    if (method !== "GET") return null;
    let parsed;
    try { parsed = new URL(url); } catch { return null; }
    if (parsed.origin !== origin || isPrivatePath(parsed.pathname)) return null;
    if (mode === "navigate") return "page";
    if (parsed.pathname.startsWith("/assets/workbench/")) return "asset";
    return null;
  }

  /** Whether a response to a handled request may be stored. `headers` has get(). */
  function shouldCache(kind, { status, type, redirected, headers }) {
    if (kind !== "page" && kind !== "asset") return false;
    if (status !== 200 || type !== "basic" || redirected) return false;
    const control = String(headers?.get("cache-control") || "").toLowerCase();
    if (/(^|[\s,])no-store([\s,]|$)/.test(control)) return false;
    const contentType = String(headers?.get("content-type") || "").trim().toLowerCase();
    if (kind === "page") return contentType.startsWith("text/html");
    return ASSET_TYPES.test(contentType);
  }

  const api = { isPrivatePath, requestKind, shouldCache };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  root.WorkbenchCachePolicy = api;
})(typeof self !== "undefined" ? self : globalThis);
