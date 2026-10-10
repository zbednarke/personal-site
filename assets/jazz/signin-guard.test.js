const test = require("node:test");
const assert = require("node:assert/strict");

require("./signin-guard.js");

const Guard = globalThis.SigninGuard;
const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

// Minimal DOM stand-in: enough for the banner without a browser.
function fakeWindow(respond) {
  const elements = new Map();
  const listeners = {};
  function element(tag) {
    const node = {
      tagName: tag, children: [], style: {}, attributes: {}, hidden: false, textContent: "", href: "",
      setAttribute(k, v) { this.attributes[k] = v; },
      append(...kids) { for (const kid of kids) { this.children.push(kid); if (kid.id) elements.set(kid.id, kid); } },
      addEventListener(type, fn) { this[`on${type}`] = fn; },
      querySelector(sel) { return this.children.find((c) => c.tagName === sel) || null; },
    };
    return node;
  }
  const body = element("body");
  const calls = [];
  const win = {
    location: { href: "https://zachbednarke.com/jazz/?tab=log#archive", pathname: "/jazz/", search: "?tab=log", hash: "#archive" },
    document: { body, createElement: element, getElementById: (id) => elements.get(id) || null },
    addEventListener(type, fn) { listeners[type] = fn; },
    listeners,
    calls,
    fetch(input, init) {
      calls.push({ input, init, self: this });
      return Promise.resolve(respond(input, init));
    },
  };
  return win;
}

const json = (status, body) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

test("a same-origin signin_required 401 shows one banner linking back here", async () => {
  const win = fakeWindow(() => json(401, { error: "signin_required", login: "/auth/login" }));
  assert.equal(Guard.install(win), true);
  assert.equal(Guard.install(win), false, "installs once");
  const response = await win.fetch("/jazz/api/v1/state");
  // Callers still see the original response and can read its body.
  assert.equal(response.status, 401);
  assert.deepEqual(await response.json(), { error: "signin_required", login: "/auth/login" });
  await settle();
  const banner = win.document.getElementById("signin-guard-banner");
  assert.ok(banner, "banner shown");
  assert.equal(banner.attributes.role, "status");
  const link = banner.children.find((c) => c.tagName === "a");
  assert.equal(link.textContent, "Sign in again");
  assert.equal(link.href, "/auth/login?next=%2Fjazz%2F%3Ftab%3Dlog%23archive");

  await win.fetch(new URL("https://zachbednarke.com/trumpets/api/v1/trumpets/listings"));
  await settle();
  assert.equal(win.document.body.children.length, 1, "only one banner");

  banner.children.find((c) => c.tagName === "button").onclick();
  assert.equal(banner.hidden, true);
  win.location.hash = "#repertoire";
  await win.fetch({ url: "https://zachbednarke.com/jazz/api/v1/sync" });
  await settle();
  assert.equal(banner.hidden, false, "shown again on the next signed-out call");
  assert.equal(link.href, "/auth/login?next=%2Fjazz%2F%3Ftab%3Dlog%23repertoire");
});

test("other failures and other origins never show the banner", async () => {
  const cases = [
    ["/jazz/api/v1/state", () => json(401, { error: "unauthorized" })],
    ["/jazz/api/v1/state", () => new Response("Sign in", { status: 401, headers: { "Content-Type": "text/plain" } })],
    ["/jazz/api/v1/state", () => json(500, { error: "signin_required" })],
    ["/jazz/api/v1/state", () => json(200, { ok: true })],
    ["https://storage.googleapis.com/upload", () => json(401, { error: "signin_required" })],
    ["/jazz/api/v1/state", () => new Response("{not json", { status: 401, headers: { "Content-Type": "application/json" } })],
  ];
  for (const [url, respond] of cases) {
    const win = fakeWindow(respond);
    Guard.install(win);
    await win.fetch(url);
    await settle();
    assert.equal(win.document.getElementById("signin-guard-banner"), null, url);
  }
});

test("network errors and init options pass straight through", async () => {
  const win = fakeWindow(() => { throw new TypeError("offline"); });
  win.fetch = function () { return Promise.reject(new TypeError("offline")); };
  Guard.install(win);
  await assert.rejects(win.fetch("/jazz/api/v1/sync"), /offline/);

  const ok = fakeWindow(() => json(200, {}));
  Guard.install(ok);
  const init = { method: "POST", body: "{}", keepalive: true };
  await ok.fetch("/jazz/api/v1/sync", init);
  assert.equal(ok.calls[0].init, init);
  assert.equal(ok.calls[0].self, ok, "fetch called with the window as this");
});

test("loginHref encodes the full current location", () => {
  assert.equal(Guard.loginHref({ pathname: "/trumpets/", search: "", hash: "" }), "/auth/login?next=%2Ftrumpets%2F");
});
