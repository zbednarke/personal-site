/* Signed-out notice for the private apps (Jazz and Trumpets).
 *
 * When a session ends, the sign-in service answers API calls with
 * 401 {"error":"signin_required","login":"/auth/login"} instead of a Basic
 * dialog. This wraps window.fetch once per page: every same-origin 401 of that
 * shape shows one calm banner linking to the sign-in page, which returns here.
 * Responses are passed through untouched, so callers, offline outboxes and
 * retry queues behave exactly as before. Load it before the app scripts. */
(() => {
  "use strict";
  const LOGIN = "/auth/login";
  const BANNER_ID = "signin-guard-banner";

  function loginHref(location) {
    const here = `${location.pathname || "/"}${location.search || ""}${location.hash || ""}`;
    return `${LOGIN}?next=${encodeURIComponent(here)}`;
  }

  function requestURL(input) {
    if (typeof input === "string") return input;
    if (input && typeof input.url === "string") return input.url;
    if (input && typeof input.href === "string") return input.href;
    return String(input);
  }

  function sameOrigin(url, base) {
    try {
      return new URL(url, base).origin === new URL(base).origin;
    } catch {
      return false;
    }
  }

  async function isSigninRequired(response) {
    if (!response || response.status !== 401) return false;
    const type = (response.headers && response.headers.get("content-type")) || "";
    if (!type.includes("application/json")) return false;
    try {
      const body = await response.clone().json();
      return Boolean(body) && body.error === "signin_required";
    } catch {
      return false;
    }
  }

  function showBanner(win) {
    const doc = win.document;
    if (!doc || !doc.body) return null;
    let banner = doc.getElementById(BANNER_ID);
    if (banner) {
      banner.hidden = false;
      banner.querySelector("a").href = loginHref(win.location);
      return banner;
    }
    banner = doc.createElement("div");
    banner.id = BANNER_ID;
    banner.setAttribute("role", "status");
    banner.setAttribute("aria-live", "polite");
    banner.style.cssText = [
      "position:fixed", "left:50%", "bottom:max(16px, env(safe-area-inset-bottom))", "transform:translateX(-50%)",
      "z-index:2147483000", "display:flex", "align-items:center", "gap:14px", "max-width:calc(100vw - 32px)",
      "padding:12px 14px 12px 18px", "border-radius:999px", "border:1px solid #3a3150",
      "background:#110d1cf2", "color:#efeafd", "box-shadow:0 12px 40px #0009",
      "font:500 15px/1.3 -apple-system,'Helvetica Neue',Helvetica,Arial,sans-serif",
    ].join(";");
    const text = doc.createElement("span");
    text.textContent = "Signed out.";
    const link = doc.createElement("a");
    link.href = loginHref(win.location);
    link.textContent = "Sign in again";
    link.style.cssText = "color:#f2ad5c;font-weight:600;text-decoration:underline;text-underline-offset:3px";
    const close = doc.createElement("button");
    close.type = "button";
    close.setAttribute("aria-label", "Dismiss");
    close.textContent = "×";
    close.style.cssText = "border:0;background:transparent;color:#9a92bd;font:400 20px/1 sans-serif;padding:4px 6px;cursor:pointer;min-width:32px;min-height:32px";
    close.addEventListener("click", () => {
      banner.hidden = true;
    });
    banner.append(text, link, close);
    doc.body.append(banner);
    // Keep the return path current as the app changes views.
    win.addEventListener("hashchange", () => {
      link.href = loginHref(win.location);
    });
    return banner;
  }

  function install(win) {
    const original = win.fetch;
    if (typeof original !== "function" || original.signinGuard) return false;
    const guarded = function fetch(input, init) {
      return original.call(win, input, init).then((response) => {
        if (response && response.status === 401 && sameOrigin(requestURL(input), win.location.href)) {
          isSigninRequired(response).then((signedOut) => {
            if (signedOut) showBanner(win);
          });
        }
        return response;
      });
    };
    guarded.signinGuard = true;
    win.fetch = guarded;
    return true;
  }

  globalThis.SigninGuard = { install, isSigninRequired, loginHref, showBanner };
  if (typeof window !== "undefined" && window.document) install(window);
})();
