const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const { JSDOM } = require("jsdom");

const source = fs.readFileSync(path.join(__dirname, "highlight-local.js"), "utf8");
const settle = () => new Promise(resolve => setTimeout(resolve, 0));
const fixture = `<section id="studio"><header class="clip-studio-header"></header><input id="studio-date" value="2026-10-26"><div class="studio-render-controls"><button id="studio-render">Render</button></div></section>`;

async function load(t, { job, phone = false, response, rejects = false } = {}) {
  const dom = new JSDOM(fixture, { url: "https://example.test/jazz/#studio", runScripts: "outside-only" });
  t.after(() => dom.window.close());
  const win = dom.window;
  win.matchMedia = () => ({ matches: phone, addEventListener() {} });
  const calls = [];
  win.fetch = async (url, options) => {
    calls.push({ url, options });
    if (rejects) throw new Error("private network diagnostics");
    return response || { ok: true, json: async () => ({ jobs: job ? [job] : [] }) };
  };
  win.eval(source);
  await settle();
  return { win, calls, panel: win.document.querySelector("#studio-magic-status"), button: win.document.querySelector("#studio-make-film") };
}

for (const phone of [false, true]) test(`actionable quota failure on ${phone ? "phone" : "desktop"}`, async t => {
  const { panel, button, calls } = await load(t, { phone, job: {
    id: "example-job", status: "failed", error: "private raw provider response",
    failure: { code: "provider_quota", title: "AI credits exhausted", message: "Retrying now will not help.", action: "Restore the film editor's AI access before trying again.", retryable: false },
  } });
  assert.equal(panel.getAttribute("role"), "alert");
  assert.equal(panel.hidden, false);
  assert.equal(panel.querySelector("strong").textContent, "AI credits exhausted");
  assert.match(panel.textContent, /Retrying now will not help/);
  assert.match(panel.textContent, /Job reference: example-job/);
  assert.doesNotMatch(panel.textContent, /private/);
  assert.match(button.textContent, /after fixing access/);
  assert.equal(panel.parentElement.className, phone ? "studio-magic-phone" : "studio-render-controls");
  panel.querySelector("button").click();
  await settle();
  assert.equal(calls.length, 2);
  assert.ok(calls.every(call => !call.options.method), "checking status must not submit a new job");
});

test("legacy failures never render raw provider text or HTML", async t => {
  const { panel } = await load(t, { job: { id: "old-job", status: "failed", message: "private message", error: '<img src=x onerror="alert(1)">private response' } });
  assert.match(panel.textContent, /Film could not finish/);
  assert.doesNotMatch(panel.textContent, /private/);
  assert.equal(panel.querySelector("img"), null);
});

test("temporary provider failure offers another attempt", async t => {
  const { button, panel } = await load(t, { job: { id: "example", status: "failed", failure: { title: "AI service is temporarily busy", message: "The request was limited.", action: "Wait a few minutes, then try again.", retryable: true } } });
  assert.equal(button.disabled, false);
  assert.equal(button.textContent, "Make film again");
  assert.match(panel.textContent, /Wait a few minutes/);
});

for (const code of [401, 403, 429, 503]) test(`HTTP ${code} has safe guidance`, async t => {
  const { panel } = await load(t, { response: { ok: false, status: code, json: async () => ({ error: "private backend details" }) } });
  assert.equal(panel.getAttribute("role"), "alert");
  assert.match(panel.textContent, code === 401 || code === 403 ? /Sign in to the site again/ : code === 429 ? /Wait a moment/ : /may still be running/);
  assert.doesNotMatch(panel.textContent, /private/);
});

test("network failure explains that the job may still be running", async t => {
  const { panel } = await load(t, { rejects: true });
  assert.match(panel.textContent, /may still be running/);
  assert.doesNotMatch(panel.textContent, /private/);
});

test("worker startup failure uses the server's curated explanation", async t => {
  const { panel } = await load(t, { response: { ok: false, status: 503, json: async () => ({ error: "private legacy detail", failure: { title: "Film worker could not start", message: "The cloud worker did not confirm that it started.", action: "Check the film list before trying again.", retryable: true } }) } });
  assert.match(panel.textContent, /Film worker could not start/);
  assert.match(panel.textContent, /Check the film list/);
  assert.doesNotMatch(panel.textContent, /private/);
});

test("login redirects do not appear as an empty film list", async t => {
  const { panel } = await load(t, { response: { ok: true, redirected: true, json: async () => { throw new Error("HTML login page"); } } });
  assert.match(panel.textContent, /Site sign-in required/);
});

test("successful refresh clears the alert", async t => {
  const { win, panel } = await load(t, { rejects: true });
  win.fetch = async () => ({ ok: true, json: async () => ({ jobs: [{ status: "complete", message: "Film is ready", project: {} }] }) });
  panel.querySelector("button").click();
  await settle();
  assert.equal(panel.getAttribute("role"), "status");
  assert.equal(panel.hasAttribute("data-error"), false);
  assert.match(panel.textContent, /Film is ready/);
});

for (const state of ["waiting_usage", "sign_in_required"]) test(`${state} preserves the job and offers status refresh/cancellation`, async t => {
  const { win, panel, button, calls } = await load(t, { job: { id: "paused-job", status: state, provider: "codex", nextAttemptAt: "2026-10-26T12:00:00Z" } });
  assert.equal(button.disabled, true, "paused work must not be resubmitted");
  assert.equal(win.document.querySelector("#studio-cancel-film").hidden, false);
  assert.equal(panel.getAttribute("role"), "status");
  assert.match(panel.textContent, /completed work are saved/);
  assert.match(panel.textContent, /No paid API fallback/);
  assert.match(panel.textContent, state === "waiting_usage" ? /Next check:/ : /Sign in to Codex on the private worker/);
  panel.querySelector("button").click();
  await settle();
  assert.ok(calls.every(call => !call.options.method));
});
