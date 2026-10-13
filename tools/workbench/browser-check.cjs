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
      if (L.mode === "panel") {
        // The desktop panel sits beside the page (text selected there is context), so an outside click leaves it open.
        await page.click("#today-title");
        assert.equal(await page.evaluate(() => __workbench.isOpen()), true, "desktop: an outside click leaves the panel open");
      } else {
        // Phone sheet and tablet split: a tap outside the sheet closes it, and the scrim then gets out of the way.
        await page.touchscreen.tap(L.mode === "sheet" ? L.width / 2 : 40, 40);
        await page.waitForSelector(".wb-sheet[hidden]", { state: "attached" });
        assert.equal(await page.locator(".wb-scrim").isHidden(), true, `${L.name}: the scrim goes with the sheet`);
        assert.equal(await page.evaluate(() => document.elementFromPoint(40, 300).closest(".wb")), null, `${L.name}: a closed sheet swallows no taps`);
        if (L.mode === "split") assert.equal(await page.evaluate(() => parseFloat(getComputedStyle(document.body).marginRight)), 0, "tablet: the page moves back");
      }
      await page.ctx.close();
    }

    // ---------------- Phones: every view fits, nav and sheet are reachable ----------------
    // iOS widens the layout viewport to fit anything wider than the screen, and
    // fixed bars (the top bar, this sheet) then size to the wider page, so their
    // right edge goes off-screen. Every view must fit at common iPhone widths.
    const jazzViews = ["today", "repertoire", "training", "guide-tones", "effects", "archive", "library", "studio"];
    const otherPages = ["/trumpets/", "/trumpets/#inspiration", "/commonplace/"];
    const inside = (b, w, h, label) => assert.ok(b && b.x >= -0.5 && b.y >= -0.5 && b.x + b.width <= w + 0.5 && b.y + b.height <= h + 0.5, `${label} is fully on screen (${JSON.stringify(b)})`);
    // Every nav item can be scrolled into view and tapped (the hit test lands on it, not on something covering it).
    const navReachable = async (page, label) => {
      const count = await page.locator(".virtuoso-nav-links > *").count();
      assert.ok(count >= 9, `${label}: nav items present`);
      for (let i = 0; i < count; i++) {
        const item = page.locator(".virtuoso-nav-links > *").nth(i);
        await item.scrollIntoViewIfNeeded();
        const hit = await item.evaluate((n) => {
          const b = n.getBoundingClientRect();
          const top = document.elementFromPoint(b.left + b.width / 2, b.top + b.height / 2);
          return { b: { x: b.left, y: b.top, width: b.width, height: b.height }, hit: !!top && n.contains(top) };
        });
        inside(hit.b, await page.evaluate(() => innerWidth), await page.evaluate(() => innerHeight), `${label}: nav item ${i + 1}`);
        assert.ok(hit.hit, `${label}: nav item ${i + 1} is tappable`);
      }
    };
    for (const width of [375, 390, 401]) {
      const page = await newPage(`phone-${width}`, width, 844, true);
      for (const view of jazzViews) {
        const label = `${width}px #${view}`;
        await page.goto(`${base}/jazz/#${view}`);
        await page.waitForSelector(".wb-pill");
        await page.waitForFunction((v) => document.querySelector(`[data-jazz-view="${v}"]`).classList.contains("active"), view);
        await page.waitForTimeout(300);
        await noSideScroll(page, label);
        // Issue #84: the top bar's menu button opens the nav in portrait.
        const menu = page.locator("#jazz-menu-button");
        assert.ok(await menu.isVisible(), `${label}: the menu button is visible`);
        inside(await menu.boundingBox(), width, 844, `${label}: the menu button`);
        inside(await page.locator("#mobile-view-label").boundingBox(), width, 844, `${label}: the view label`);
        await menu.tap();
        await page.waitForSelector("body.jazz-nav-open .virtuoso-nav");
        await navReachable(page, `${label} drawer`);
        await noSideScroll(page, `${label} drawer`);
        if (view === "today" && width === 390) await page.screenshot({ path: path.join(out, "phone-jazz-nav.png") });
        await page.touchscreen.tap(width - 20, 400); // the scrim
        await page.waitForSelector("body:not(.jazz-nav-open)");
        assert.ok(await page.locator(".virtuoso-nav").isHidden(), `${label}: the drawer closes on an outside tap`);

        // The Workbench sheet: inside the screen, × and Send fully visible, closes on an outside tap.
        await page.tap(".wb-pill");
        await page.waitForSelector(".wb-sheet:not([hidden])");
        for (const sel of [".wb-sheet", '.wb-head [aria-label="Close Workbench"]', ".wb-send", ".wb-input"]) inside(await page.locator(sel).boundingBox(), width, 844, `${label}: ${sel}`);
        await noSideScroll(page, `${label} sheet`);
        await page.touchscreen.tap(width / 2, 30);
        await page.waitForSelector(".wb-sheet[hidden]", { state: "attached" });
      }
      // Navigating from the drawer goes to the view and closes the drawer.
      await page.tap("#jazz-menu-button");
      await page.tap('.virtuoso-nav [data-jazz-view="archive"]');
      await page.waitForFunction(() => document.body.classList.contains("virtuoso-archive") && !document.body.classList.contains("jazz-nav-open"));
      assert.equal(await page.locator("#mobile-view-label").innerText(), "ARCHIVE");
      for (const url of otherPages) {
        await page.goto(base + url);
        await page.waitForSelector(".wb-pill");
        await page.waitForTimeout(300);
        await noSideScroll(page, `${width}px ${url}`);
        await page.tap(".wb-pill");
        await page.waitForSelector(".wb-sheet:not([hidden])");
        for (const sel of ['.wb-head [aria-label="Close Workbench"]', ".wb-send"]) inside(await page.locator(sel).boundingBox(), width, 844, `${width}px ${url}: ${sel}`);
        await noSideScroll(page, `${width}px ${url} sheet`);
        await page.touchscreen.tap(width / 2, 30);
        await page.waitForSelector(".wb-sheet[hidden]", { state: "attached" });
      }
      await page.ctx.close();
    }

    // Landscape phones: the sidebar scrolls on its own, so every item is reachable.
    for (const [width, height] of [[844, 390], [667, 375]]) {
      const page = await newPage(`landscape-${width}`, width, height, true);
      for (const view of jazzViews) {
        const label = `${width}x${height} #${view}`;
        await page.goto(`${base}/jazz/#${view}`);
        await page.waitForFunction((v) => document.querySelector(`[data-jazz-view="${v}"]`).classList.contains("active"), view);
        await noSideScroll(page, label);
        if (await page.locator("#jazz-menu-button").isVisible()) await page.tap("#jazz-menu-button");
        await navReachable(page, label);
        if (view === "today" && width === 844) await page.screenshot({ path: path.join(out, "landscape-jazz-nav.png") });
      }
      await page.ctx.close();
    }

    // The keyboard and pinch zoom shrink the visual viewport; the sheet follows
    // it so the composer, × and Send stay on screen. Simulated by standing in
    // for window.visualViewport (headless Chromium has no on-screen keyboard).
    {
      const page = await newPage("phone-keyboard", 390, 844, true);
      await page.addInitScript(() => {
        const fake = new EventTarget();
        const v = { offsetLeft: 0, offsetTop: 0, scale: 1, pageLeft: 0, pageTop: 0 };
        for (const k of ["width", "height", ...Object.keys(v)]) Object.defineProperty(fake, k, { get: () => (k in v ? v[k] : k === "width" ? innerWidth : innerHeight) });
        Object.defineProperty(window, "visualViewport", { value: fake, configurable: true });
        window.__setViewport = (next) => { Object.assign(v, next); fake.dispatchEvent(new Event("resize")); };
      });
      await page.goto(base + "/jazz/");
      await page.waitForSelector(".wb-pill");
      await page.tap(".wb-pill");
      await page.waitForSelector(".wb-sheet:not([hidden])");
      const visible = async (label, vv) => {
        for (const sel of [".wb-sheet", '.wb-head [aria-label="Close Workbench"]', ".wb-send", ".wb-input"]) {
          const b = await page.locator(sel).boundingBox();
          assert.ok(b.x >= vv.offsetLeft - 0.5 && b.y >= vv.offsetTop - 0.5 && b.x + b.width <= vv.offsetLeft + vv.width + 0.5 && b.y + b.height <= vv.offsetTop + vv.height + 0.5,
            `${label}: ${sel} sits inside the visual viewport (${JSON.stringify(b)} vs ${JSON.stringify(vv)})`);
        }
      };
      await page.tap(".wb-input");
      const keyboard = { width: 390, height: 844 - 336, offsetLeft: 0, offsetTop: 0, scale: 1 };
      await page.evaluate((vv) => __setViewport(vv), keyboard);
      await visible("keyboard open", keyboard);
      assert.ok(await page.evaluate(() => document.querySelector(".wb").classList.contains("wb-kb-open")), "keyboard open: no home-indicator gap above the keyboard");
      await page.screenshot({ path: path.join(out, "phone-keyboard.png") });
      const zoomed = { width: 390 / 1.25, height: (844 - 336) / 1.25, offsetLeft: 40, offsetTop: 120, scale: 1.25 };
      await page.evaluate((vv) => __setViewport(vv), zoomed);
      await visible("zoomed with the keyboard open", zoomed);
      await page.evaluate(() => __setViewport({ width: 390, height: 844, offsetLeft: 0, offsetTop: 0, scale: 1 }));
      await visible("keyboard closed", { width: 390, height: 844, offsetLeft: 0, offsetTop: 0 });
      // Swipe the handle down to close.
      const grab = await page.locator(".wb-grab").boundingBox();
      await page.mouse.move(grab.x + grab.width / 2, grab.y + grab.height / 2);
      await page.mouse.down();
      await page.mouse.move(grab.x + grab.width / 2, grab.y + 200, { steps: 8 });
      await page.mouse.up();
      await page.waitForSelector(".wb-sheet[hidden]", { state: "attached" });
      // Touch screens get 16px fields so iOS never zooms the page on focus.
      const small = await page.evaluate(() => [...document.querySelectorAll("input, select, textarea")]
        .filter((n) => !["checkbox", "radio", "range", "file", "hidden", "color"].includes(n.type) && parseFloat(getComputedStyle(n).fontSize) < 16)
        .map((n) => n.id || n.className));
      assert.deepEqual(small, [], "no field under 16px on a touch screen");
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
    // Poll for a partial reply: the streaming bubble can exist before its first words render.
    const midStream = await laptop.waitForFunction(() => {
      const el = document.querySelector(".wb-msg-assistant.wb-streaming");
      const text = el ? el.innerText.trim() : "";
      return text.length > 0 && !text.includes("one chorus of guide tones.");
    }, null, { timeout: 10000 }).then(() => true, () => false);
    assert.ok(midStream, "the laptop sees the reply mid-stream");
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
    const preview = await laptop.locator('.wb-card[data-status="pending"] .wb-preview').innerText();
    assert.ok(preview.endsWith("END-OF-BODY") && preview.length > 1000, "the whole issue body is shown before approval");
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

    // Hostile Markdown renders as text and plain links, with no handlers or script URLs.
    await laptop.fill(".wb-input", "markdown check");
    await laptop.keyboard.press("Enter");
    await laptop.waitForSelector(".wb-msg-assistant a[href^='https://x.com/']", { timeout: 15000 });
    const unsafe = await laptop.evaluate(() => [...document.querySelectorAll(".wb *")].filter((n) =>
      [...n.attributes].some((a) => /^on/i.test(a.name)) || /^\s*javascript:/i.test(n.getAttribute("href") || "")).map((n) => n.outerHTML));
    assert.deepEqual(unsafe, [], "no event-handler attributes or script URLs in rendered replies");
    assert.ok(!(await laptop.title()).includes("PWNED"));

    // ---------------- The daily fix list at phone width (#fixlist) ----------------
    assert.equal(await (await fetch(`${base}/__preview_fixlist`, { method: "POST" })).text(), "ok", "a fictional fix list is sent");
    {
      const page = await newPage("phone-fixlist", 390, 844, true);
      await page.goto(`${base}/jazz/#fixlist`);
      await page.waitForSelector(".wb-fixlist:not([hidden]) .wb-fl-next");
      assert.match(await page.locator(".wb-fl-title").innerText(), /^Fix list – \d{4}-\d{2}-\d{2}$/);
      assert.match(await page.locator(".wb-fl-verdict").innerText(), /^\d+ to fix · next: /);
      assert.equal(await page.locator(".wb-fl-next .wb-fl-kicker").innerText(), "FIX NEXT");
      const sections = await page.locator(".wb-fl-section[data-section]").evaluateAll((n) => n.map((s) => s.dataset.section));
      assert.deepEqual(sections, ["run", "api", "checks", "sources", "market", "films"], "every section with something in it, in order");
      assert.match(await page.locator('.wb-fl-section[data-section="run"] .wb-fl-run').innerText(), /Daily run \d{4}-\d{2}-\d{2}: partial/);
      const row = page.locator('.wb-fl-section[data-section="checks"] .wb-fl-item').first();
      assert.equal(await row.locator(".wb-fl-count").innerText(), "3");
      assert.equal(await row.locator(".wb-fl-change").innerText(), "new", "each count says how it changed");
      await noSideScroll(page, "390px fix list");
      for (const sel of [".wb-fl-close", ".wb-fl-next", ".wb-fl-verdict"]) inside(await page.locator(sel).boundingBox(), 390, 844, `fix list: ${sel}`);
      const wide = await page.evaluate(() => [...document.querySelectorAll(".wb-fixlist *")].filter((n) => n.getBoundingClientRect().right > innerWidth + 0.5).map((n) => n.className));
      assert.deepEqual(wide, [], "nothing in the fix list runs off the right edge");
      await page.screenshot({ path: path.join(out, "phone-fixlist.png") });
      // "Ask Workbench to fix one" opens the Fix list thread with the digest in it.
      await page.tap(".wb-fl-thread");
      await page.waitForFunction(() => __workbench.isOpen() && __workbench.state.thread && __workbench.state.thread.title === "Fix list");
      await page.waitForSelector(".wb-msg-assistant >> text=Fix next:");
      await page.touchscreen.tap(195, 30);
      await page.waitForSelector(".wb-sheet[hidden]", { state: "attached" });
      await page.tap(".wb-fl-close");
      await page.waitForSelector(".wb-fixlist[hidden]", { state: "attached" });
      assert.equal(await page.evaluate(() => location.hash), "", "closing leaves the page where it was");
      await page.ctx.close();
    }

    assert.deepEqual(errors, []);
    console.log("Workbench browser checks passed: 390/768/1440 px layouts (sheet, split, panel) on /commonplace/, /trumpets/ and /jazz/, outside tap closes the phone sheet and split view (not the desktop panel), every /jazz/ view plus /trumpets/ and /commonplace/ fit 375/390/401 px with the sheet's × and Send on screen, phone menu drawer and landscape sidebar reach every nav item, keyboard and zoom keep the sheet in the visual viewport, swipe-down close, 16px touch fields, PWA manifest and service worker scope, two devices on one thread (same streamed reply, draft sync, first-wins approval), offline outbox replay, hold-to-talk voice note, status card, full approval preview, hostile Markdown rendered inert, the daily fix list at 390 px (verdict, Fix next, sections, changes, its thread), CSP in place, no runtime errors.");
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
