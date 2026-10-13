const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { SHELL_PAGES, isPrivatePath, requestKind, shouldCache } = require("./sw-cache-policy.js");

const origin = "https://zachbednarke.com";
const nav = (p, extra = {}) => ({ method: "GET", url: origin + p, mode: "navigate", ...extra });
const res = (contentType, extra = {}) => ({
  status: 200, type: "basic", redirected: false,
  headers: new Headers({ "Content-Type": contentType, ...(extra.headers || {}) }),
  ...Object.fromEntries(Object.entries(extra).filter(([k]) => k !== "headers")),
});

test("private paths are never handled, whatever the request mode", () => {
  for (const p of [
    "/jazz/highlight-api/library",
    "/jazz/highlight-api/library/test-study-a/file",
    "/jazz/highlight-api/library/test-study-a/file?download=1",
    "/jazz/films/2026-10-01/film.mp4",
    "/jazz/films/index.json",
    "/jazz/api/v1/recordings/x",
    "/jazz/share/abc",
    "/workbench/api/v1/workbench/threads",
    "/commonplace/api/v1/commonplace",
    "/trumpets/api/v1/listings",
    "/__preview_objects/a.wav",
  ]) {
    assert.equal(requestKind(nav(p), origin), null, `navigate ${p}`);
    assert.equal(requestKind({ method: "GET", url: origin + p, mode: "no-cors" }, origin), null, `fetch ${p}`);
    assert.ok(isPrivatePath(new URL(origin + p).pathname), p);
  }
});

test("only allowlisted shell pages and Workbench assets are handled", () => {
  for (const p of ["/jazz/", "/jazz/index.html", "/jazz/plan/", "/jazz/plan/index.html", "/trumpets/", "/trumpets/index.html", "/commonplace/", "/commonplace/index.html"]) {
    assert.equal(requestKind(nav(p), origin), "page", p);
    assert.ok(SHELL_PAGES.has(p), p);
  }
  assert.equal(requestKind(nav("/jazz/?workbench=1#library"), origin), "page", "query and hash do not matter");
  for (const p of ["/jazz", "/jazz/fx-check", "/jazz/report/2026-10-01", "/jazz/other.html", "/trumpets/listing/1", "/commonplace/m/abc", "/", "/privacy/"]) {
    assert.equal(requestKind(nav(p), origin), null, `${p}: any other navigation, including future server-rendered pages`);
  }
  assert.equal(requestKind({ method: "GET", url: origin + "/assets/workbench/workbench.js", mode: "no-cors" }, origin), "asset");
  assert.equal(requestKind({ method: "GET", url: origin + "/jazz/", mode: "cors" }, origin), null, "a fetch of a shell URL is not a page load");
  assert.equal(requestKind({ method: "GET", url: origin + "/assets/jazz/library.js", mode: "no-cors" }, origin), null);
  assert.equal(requestKind({ method: "GET", url: origin + "/assets/jazz/sheets/bb-blues.pdf", mode: "cors" }, origin), null);
  assert.equal(requestKind(nav("/jazz/", { method: "POST" }), origin), null);
  assert.equal(requestKind(nav("/jazz/", { url: "https://example.com/jazz/" }), origin), null);
  assert.equal(requestKind(nav("/x", { url: "not a url" }), origin), null);
});

test("shell pages are cached as a complete 200 text/html, even though every app page is no-store", () => {
  assert.equal(shouldCache("page", res("text/html; charset=utf-8")), true);
  assert.equal(shouldCache("page", res("text/html; charset=utf-8", { headers: { "Cache-Control": "private, no-store" } })), true, "what Caddy and the preview send for every app page");
  assert.equal(shouldCache("page", res("application/pdf")), false, "a PDF navigation");
  assert.equal(shouldCache("page", res("video/mp4")), false, "a film navigation");
  assert.equal(shouldCache("page", res("application/json")), false);
  assert.equal(shouldCache("page", res("")), false, "no content type");
  assert.equal(shouldCache("page", res("text/html", { status: 206 })), false);
  assert.equal(shouldCache("page", res("text/html", { status: 404 })), false);
  assert.equal(shouldCache("page", res("text/html", { status: 401 })), false, "a signed-out page");
  assert.equal(shouldCache("page", res("text/html", { redirected: true })), false, "sign-in redirects");
  assert.equal(shouldCache("page", res("text/html", { type: "opaque" })), false);
  assert.equal(shouldCache(null, res("text/html")), false, "an unhandled request is never stored");
});

test("Workbench assets are cached only as script, style, image or manifest", () => {
  assert.equal(shouldCache("asset", res("text/javascript; charset=utf-8")), true);
  assert.equal(shouldCache("asset", res("application/javascript")), true);
  assert.equal(shouldCache("asset", res("text/css; charset=utf-8")), true);
  assert.equal(shouldCache("asset", res("image/png")), true);
  assert.equal(shouldCache("asset", res("text/css", { headers: { "Cache-Control": "no-store" } })), true, "public repo files, allowlisted by path");
  assert.equal(shouldCache("asset", res("text/html")), false);
  assert.equal(shouldCache("asset", res("application/pdf")), false);
  assert.equal(shouldCache("asset", res("text/css", { status: 304 })), false);
});

test("the worker loads the policy, uses a new cache name, and installed copies see the update", () => {
  const root = path.join(__dirname, "../..");
  const sw = fs.readFileSync(path.join(__dirname, "sw.js"), "utf8");
  assert.match(sw, /importScripts\("\/assets\/workbench\/sw-cache-policy\.js"\)/);
  assert.match(sw, /const WB_CACHE = "workbench-shell-v2";/);
  assert.match(sw, /key\.startsWith\("workbench-shell-"\) && key !== WB_CACHE/, "activate deletes older shell caches");
  assert.doesNotMatch(sw, /res\.ok && res\.type === "basic"/, "the old any-navigation rule is gone");
  for (const app of ["jazz", "trumpets", "commonplace"]) {
    const wrapper = fs.readFileSync(path.join(root, app, "workbench-sw.js"), "utf8");
    assert.match(wrapper, /Worker version 2\b/, `${app}: the wrapper's bytes changed, so installed workers update`);
    assert.match(wrapper, /importScripts\("\/assets\/workbench\/sw\.js"\)/);
  }
  const page = fs.readFileSync(path.join(__dirname, "workbench.js"), "utf8");
  assert.match(page, /serviceWorker\.register\([^)]*updateViaCache: "none"/, "updates bypass the HTTP cache for imported scripts too");
});
