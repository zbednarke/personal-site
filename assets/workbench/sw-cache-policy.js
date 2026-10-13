/* What the Workbench service worker may keep in its offline cache (pure, so
 * node tests can check it; sw.js loads it with importScripts).
 *
 * Only two kinds of request are handled at all, both explicit allowlists of
 * static files from the public repo:
 *  - "page":  a same-origin GET navigation to a static shell page
 *    (SHELL_PAGES: each app's index and the Jazz plan page);
 *  - "asset": a same-origin GET under /assets/workbench/ (the sheet's own
 *    code, styles and icons).
 * Everything else is left to the network: the worker never calls
 * respondWith and never caches it. That includes every private path (APIs,
 * the highlight service with its PDF library, films, share links, preview
 * objects), any other navigation, and any server-rendered HTML added later.
 *
 * Every app page is served "private, no-store" (the sign-in gate sets it for
 * the whole app path), so no-store is not what decides here; the allowlist
 * is. A handled response is stored only when it is a complete 200 from this
 * origin (not redirected, so never a sign-in redirect; not opaque) of the
 * expected type: text/html for a page, script, style, image or manifest for
 * an asset. So a PDF, a video or a JSON body is never kept. */
(function (root) {
  "use strict";
  const SHELL_PAGES = new Set([
    "/jazz/", "/jazz/index.html",
    "/jazz/plan/", "/jazz/plan/index.html",
    "/trumpets/", "/trumpets/index.html",
    "/commonplace/", "/commonplace/index.html",
  ]);
  const PRIVATE_SEGMENTS = ["/api/", "/highlight-api/", "/films/", "/share/"];
  const PRIVATE_PREFIXES = ["/__preview_objects/"];
  const ASSET_TYPES = /^(text\/css|text\/javascript|application\/javascript|application\/x-javascript|application\/manifest\+json|image\/(png|svg\+xml|webp|x-icon|vnd\.microsoft\.icon))\b/i;

  function isPrivatePath(pathname) {
    const path = String(pathname || "");
    return PRIVATE_SEGMENTS.some((segment) => path.includes(segment)) || PRIVATE_PREFIXES.some((prefix) => path.startsWith(prefix));
  }

  function isShellPage(pathname) {
    return SHELL_PAGES.has(String(pathname || ""));
  }

  /** "page", "asset" or null (leave it to the network, never cache). */
  function requestKind({ method, url, mode }, origin) {
    if (method !== "GET") return null;
    let parsed;
    try { parsed = new URL(url); } catch { return null; }
    if (parsed.origin !== origin || isPrivatePath(parsed.pathname)) return null;
    if (mode === "navigate") return isShellPage(parsed.pathname) ? "page" : null;
    if (parsed.pathname.startsWith("/assets/workbench/")) return "asset";
    return null;
  }

  /** Whether a response to a handled request may be stored. `headers` has get(). */
  function shouldCache(kind, { status, type, redirected, headers }) {
    if (kind !== "page" && kind !== "asset") return false;
    if (status !== 200 || type !== "basic" || redirected) return false;
    const contentType = String(headers?.get("content-type") || "").trim().toLowerCase();
    if (kind === "page") return contentType.startsWith("text/html");
    return ASSET_TYPES.test(contentType);
  }

  const api = { SHELL_PAGES, isPrivatePath, isShellPage, requestKind, shouldCache };
  if (typeof module !== "undefined" && module.exports) module.exports = api;
  root.WorkbenchCachePolicy = api;
})(typeof self !== "undefined" ? self : globalThis);
