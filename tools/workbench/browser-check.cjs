/* Workbench browser check: an isolated Go/Postgres preview with a scripted
 * fake model and fake GitHub (fictional data only). At 390 (touch), 768
 * (touch) and 1440 (pointer) px it checks the pill, the sheet's layout, page
 * context and no sideways scroll; then two browser contexts on one thread
 * receive the same streamed reply, sync a half-written draft, see one
 * approval decided on the other device, replay an offline outbox, send a
 * hold-to-talk voice note and show a status card. Fails on runtime errors. */
const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");
const { chromium } = require("playwright");

const root = path.resolve(__dirname, "../..");
const out = process.env.WORKBENCH_SCREENSHOT_DIR || path.join(root, "docs/workbench/screenshots");
fs.mkdirSync(out, { recursive: true });
const addr = process.env.WORKBENCH_PREVIEW_ADDR || "127.0.0.1:4175";
const base = `http://${addr}`;
const API = `${base}/workbench/api/v1/workbench`;
const child = spawn(process.env.WORKBENCH_GO || "go", ["test", "-run", "^TestWorkbenchBrowserPreview$", "-v", "-timeout", "10m"], {
  cwd: path.join(root, "jazz-api"),
  env: { ...process.env, WORKBENCH_PREVIEW: "1", WORKBENCH_PREVIEW_ADDR: addr },
});
let logs = "";
child.stdout.on("data", (d) => (logs += d));
child.stderr.on("data", (d) => (logs += d));

async function waitForServer() {
  for (let i = 0; i < 600; i++) {
    try {
      const r = await fetch(`${API}/spend`);
      if (r.ok) return;
    } catch { /* starting */ }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error("preview did not start");
}

(async () => {
  let browser;
  try {
    await waitForServer();
    const exe = process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {};
    browser = await chromium.launch({ ...exe, headless: true, args: ["--no-sandbox", "--use-fake-ui-for-media-stream", "--use-fake-device-for-media-stream"] });
    const errors = [];
    const newPage = async (name, width, height, touch) => {
      const ctx = await browser.newContext({ viewport: { width, height }, deviceScaleFactor: 1, hasTouch: touch, isMobile: touch, reducedMotion: "reduce", permissions: ["microphone"] });
      const page = await ctx.newPage();
      page.on("pageerror", (e) => errors.push(`${name}: ${e.message}`));
      page.on("console", (m) => { if (m.type() === "error" && !/Failed to load resource|404|502|503/.test(m.text())) errors.push(`${name} console: ${m.text()}`); });
      page.ctx = ctx;
      page.touch = touch;
      page.name = name;
      return page;
    };
    const press = (page, selector) => (page.touch ? page.tap(selector) : page.click(selector));
    const noSideScroll = async (page, label) => {
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
      assert.ok(overflow <= 0, `${label}: no sideways scroll (${overflow}px)`);
    };

    // ---------------- Layout at three widths, on all three pages ----------------
    const layouts = [
      { name: "phone", width: 390, height: 844, touch: true, page: "/commonplace/", mode: "sheet" },
      { name: "tablet", width: 768, height: 1024, touch: true, page: "/trumpets/", mode: "split" },
      { name: "desktop", width: 1440, height: 900, touch: false, page: "/jazz/", mode: "panel" },
    ];
    for (const L of layouts) {
      const page = await newPage(L.name, L.width, L.height, L.touch);
      await page.goto(base + L.page);
      await page.waitForSelector(".wb-pill");
      await noSideScroll(page, `${L.name} closed`);
      await press(page, ".wb-pill");
      await page.waitForSelector(".wb-sheet:not([hidden])");
      await page.waitForFunction(() => __workbench.state.thread && __workbench.connected());
      assert.equal(await page.evaluate(() => __workbench.mode()), L.mode, `${L.name} layout`);
      const box = await page.locator(".wb-sheet").boundingBox();
      if (L.mode === "sheet") {
        assert.ok(box.width === L.width && Math.abs(box.y + box.height - L.height) <= 1, `phone: a full-width bottom sheet (${JSON.stringify(box)})`);
        const composer = await page.locator(".wb-input").boundingBox();
        assert.ok(composer.y > L.height * 0.75, "phone: the composer is thumb-reachable at the bottom");
      } else if (L.mode === "split") {
        const margin = await page.evaluate(() => parseFloat(getComputedStyle(document.body).marginRight));
        assert.ok(margin >= 300 && Math.abs(box.x + box.width - L.width) <= 1, `tablet: split view moves the page over (${margin}px)`);
      } else {
        assert.ok(box.width === 420 && box.x + box.width <= L.width - 15, `desktop: a side panel (${JSON.stringify(box)})`);
      }
      await noSideScroll(page, `${L.name} open`);
      const manifest = await page.evaluate(async () => (await fetch(document.querySelector('link[rel="manifest"]').href, { credentials: "include" })).json());
      assert.equal(manifest.scope, L.page, "the PWA is scoped to the private page");
      const sw = await page.waitForFunction(() => navigator.serviceWorker.getRegistration().then((r) => r && r.scope), null, { timeout: 10000 });
      assert.equal(await sw.jsonValue(), base + L.page, "service worker scope");
      await page.screenshot({ path: path.join(out, `${L.name}-${L.mode}.png`) });
      await page.ctx.close();
    }

    // ---------------- Two devices, one thread ----------------
    const laptop = await newPage("laptop", 1440, 900, false);
    const phone = await newPage("phone2", 390, 844, true);
    await laptop.goto(base + "/jazz/");
    await laptop.click(".wb-pill");
    await laptop.waitForFunction(() => __workbench.state.thread && __workbench.connected());
    const threadId = await laptop.evaluate(() => __workbench.state.thread.id);
    await phone.goto(`${base}/jazz/?workbench=${threadId}`);
    await phone.waitForFunction((id) => __workbench.isOpen() && __workbench.state.thread && __workbench.state.thread.id === id && __workbench.connected(), threadId);

    // Keyboard toggle on desktop.
    await laptop.keyboard.press("Escape");
    await laptop.waitForSelector(".wb-sheet[hidden]", { state: "attached" });
    await laptop.locator("body").press("`");
    await laptop.waitForSelector(".wb-sheet:not([hidden])");

    // The phone sends; both watch the same reply stream in.
    await phone.tap(".wb-input");
    await phone.fill(".wb-input", "What should I practise today?");
    await phone.tap(".wb-send");
    await laptop.waitForSelector(".wb-msg-user >> text=What should I practise today?");
    await laptop.waitForSelector(".wb-msg-assistant.wb-streaming", { timeout: 10000 });
    const partial = await laptop.locator(".wb-msg-assistant.wb-streaming").innerText();
    assert.ok(partial.length > 0 && !partial.includes("one chorus of guide tones."), "the laptop sees the reply mid-stream");
    await phone.screenshot({ path: path.join(out, "phone-streaming.png") });
    await laptop.waitForFunction(() => !__workbench.state.running && __workbench.state.items.some((i) => i.role === "assistant" && i.status === "done" && i.text.endsWith("guide tones.")), null, { timeout: 15000 });
    await phone.waitForFunction(() => !__workbench.state.running && __workbench.state.items.some((i) => i.role === "assistant" && i.status === "done" && i.text.endsWith("guide tones.")), null, { timeout: 15000 });
    const view = (p) => p.evaluate(() => JSON.stringify(WorkbenchModel.timeline(__workbench.state).map((i) => [i.id, i.role || i.kind, i.text || ""])));
    assert.equal(await view(phone), await view(laptop), "both devices hold the same thread");
    const statusText = await phone.locator(".wb-status").innerText();
    assert.ok(/month (<)?\$[0-9.]+ of \$25\.00/.test(statusText), `spend is shown: ${statusText}`);

    // A half-written draft follows the owner from the laptop to the phone.
    await laptop.click(".wb-input");
    await laptop.keyboard.type("Half a thought about ballads");
    await laptop.locator(".wb-log").click({ position: { x: 10, y: 10 } });
    await phone.waitForFunction(() => document.querySelector(".wb-input").value === "Half a thought about ballads", null, { timeout: 5000 });
    await phone.waitForSelector(".wb-status >> text=Typing on another device", { timeout: 3000 }).catch(() => {});
    await phone.fill(".wb-input", "");
    await phone.evaluate(() => document.querySelector(".wb-input").dispatchEvent(new Event("input")));

    // An approval card appears on both; the laptop answers first and wins.
    await laptop.fill(".wb-input", "Please file an issue to make the metronome click calmer");
    await laptop.keyboard.press("Enter");
    await phone.waitForSelector('.wb-card[data-status="pending"] .wb-btn-primary', { timeout: 15000 });
    await laptop.waitForSelector('.wb-card[data-status="pending"] .wb-btn-primary');
    await laptop.screenshot({ path: path.join(out, "desktop-approval.png") });
    await laptop.click('.wb-card[data-status="pending"] .wb-btn-primary');
    await phone.waitForSelector('.wb-card[data-status="approved"] >> text=Created issue', { timeout: 10000 });
    assert.equal(await phone.locator('.wb-card[data-status="pending"]').count(), 0, "the phone's card updates without a refresh");
    assert.equal(await (await fetch(`${base}/__preview_issues`)).text(), "1", "the issue was created once");
    await phone.screenshot({ path: path.join(out, "phone-approved.png") });

    // Offline: the message waits in the outbox, then replays exactly once.
    await phone.ctx.setOffline(true);
    await phone.fill(".wb-input", "Sent from a tunnel");
    await phone.tap(".wb-send");
    await phone.waitForSelector(".wb-outbox >> text=1 waiting to send");
    await phone.waitForSelector(".wb-msg.wb-pending");
    await phone.screenshot({ path: path.join(out, "phone-offline-outbox.png") });
    await phone.ctx.setOffline(false);
    await phone.evaluate(() => dispatchEvent(new Event("online")));
    await laptop.waitForSelector(".wb-msg-user >> text=Sent from a tunnel", { timeout: 15000 });
    await phone.waitForFunction(() => !document.querySelector(".wb-outbox"), null, { timeout: 15000 });
    await phone.waitForFunction(() => __workbench.connected(), null, { timeout: 15000 });
    await laptop.waitForFunction(() => !__workbench.state.running, null, { timeout: 15000 });
    const tunnel = await laptop.evaluate(() => __workbench.state.items.filter((i) => i.text === "Sent from a tunnel").length);
    assert.equal(tunnel, 1, "the replayed message lands once");

    // Hold to talk (Chromium's fake microphone): a voice note is stored and announced.
    const mic = await laptop.locator(".wb-mic").boundingBox();
    await laptop.mouse.move(mic.x + mic.width / 2, mic.y + mic.height / 2);
    await laptop.mouse.down();
    await laptop.waitForSelector(".wb-rec");
    await laptop.waitForTimeout(1200);
    await laptop.mouse.up();
    await phone.waitForSelector(".wb-msg-user .wb-att >> text=Voice note attached", { timeout: 15000 });
    await laptop.waitForSelector(".wb-msg-assistant >> text=Got your voice note", { timeout: 15000 });

    // CI, PR and deploy status as a card.
    await laptop.fill(".wb-input", "What's the site status?");
    await laptop.keyboard.press("Enter");
    await laptop.waitForSelector('.wb-card[data-card="status"] .wb-light i[data-light="red"]', { timeout: 15000 });
    await phone.waitForSelector('.wb-card[data-card="status"]', { timeout: 10000 });
    await laptop.screenshot({ path: path.join(out, "desktop-status.png") });
    await noSideScroll(phone, "phone with cards");

    assert.deepEqual(errors, []);
    console.log("Workbench browser checks passed: 390/768/1440 px layouts (sheet, split, panel) on /commonplace/, /trumpets/ and /jazz/, PWA manifest and service worker scope, two devices on one thread (same streamed reply, draft sync, first-wins approval), offline outbox replay, hold-to-talk voice note, status card, no runtime errors.");
  } finally {
    if (browser) await browser.close();
    try { await fetch(base + "/__preview_stop", { method: "POST" }); } catch { /* stopped */ }
    await new Promise((resolve) => {
      if (child.exitCode !== null) return resolve();
      child.once("exit", resolve);
      setTimeout(() => { child.kill(); resolve(); }, 5000);
    });
  }
})().catch((e) => {
  console.error(e);
  console.error(logs.slice(-4000));
  process.exitCode = 1;
});
