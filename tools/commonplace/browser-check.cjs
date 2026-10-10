/* Commonplace browser check: an isolated Go/Postgres preview with fictional
 * data only (see jazz-api/testdata/commonplace). Covers capture, the Moment
 * view, margin keep/erase/undo, search, Discord import preview and the Idea
 * space at 390 px and 1440 px, and fails on any runtime error. */
const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const zlib = require("node:zlib");
const { chromium } = require("playwright");

const root = path.resolve(__dirname, "../..");
const out = process.env.COMMONPLACE_SCREENSHOT_DIR || path.join(root, "docs/commonplace/screenshots");
fs.mkdirSync(out, { recursive: true });
const addr = process.env.COMMONPLACE_PREVIEW_ADDR || "127.0.0.1:4174";
const base = `http://${addr}`;
const API = `${base}/commonplace/api/v1/commonplace`;
const child = spawn(process.env.COMMONPLACE_GO || "go", ["test", "-run", "^TestCommonplaceBrowserPreview$", "-v", "-timeout", "10m"], {
  cwd: path.join(root, "jazz-api"),
  env: { ...process.env, COMMONPLACE_PREVIEW: "1", COMMONPLACE_PREVIEW_ADDR: addr },
});
let logs = "";
child.stdout.on("data", (d) => (logs += d));
child.stderr.on("data", (d) => (logs += d));

/** A synthetic phone-shaped PNG: dark ground with abstract message bands. Not a real screenshot. */
function syntheticShot(width, height, seed) {
  const crcTable = Array.from({ length: 256 }, (_, n) => {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    return c >>> 0;
  });
  const crc = (buf) => {
    let c = 0xffffffff;
    for (const b of buf) c = crcTable[(c ^ b) & 0xff] ^ (c >>> 8);
    return (c ^ 0xffffffff) >>> 0;
  };
  const chunk = (type, data) => {
    const len = Buffer.alloc(4);
    len.writeUInt32BE(data.length);
    const body = Buffer.concat([Buffer.from(type), data]);
    const sum = Buffer.alloc(4);
    sum.writeUInt32BE(crc(body));
    return Buffer.concat([len, body, sum]);
  };
  const header = Buffer.alloc(13);
  header.writeUInt32BE(width, 0);
  header.writeUInt32BE(height, 4);
  header[8] = 8;
  header[9] = 2;
  const bands = [];
  for (let i = 0; i < 9; i++) {
    const mine = (i + seed) % 2 === 0;
    bands.push({ y0: 0.08 + i * 0.1, y1: 0.08 + i * 0.1 + 0.07, x0: mine ? 0.3 : 0.05, x1: mine ? 0.95 : 0.7, mine });
  }
  const rows = [];
  for (let y = 0; y < height; y++) {
    const row = Buffer.alloc(1 + width * 3);
    for (let x = 0; x < width; x++) {
      const fx = x / width, fy = y / height;
      let c = [8, 8, 10];
      for (const b of bands) if (fy > b.y0 && fy < b.y1 && fx > b.x0 && fx < b.x1) c = b.mine ? [40, 110, 230] : [52, 52, 58];
      row[1 + x * 3] = c[0];
      row[2 + x * 3] = c[1];
      row[3 + x * 3] = c[2];
    }
    rows.push(row);
  }
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk("IHDR", header), chunk("IDAT", zlib.deflateSync(Buffer.concat(rows))), chunk("IEND", Buffer.alloc(0))]);
}

async function call(method, p, body) {
  const r = await fetch(API + p, { method, headers: body ? { "Content-Type": "application/json" } : {}, body: body ? JSON.stringify(body) : undefined });
  const data = await r.json().catch(() => null);
  if (!r.ok) throw new Error(`${method} ${p}: ${r.status} ${JSON.stringify(data)}`);
  return data;
}

async function seed() {
  const manifest = fs.readFileSync(path.join(root, "jazz-api/testdata/commonplace/bundle-sample.json"));
  const fd = new FormData();
  fd.append("manifest", new Blob([manifest], { type: "application/json" }), "manifest.json");
  fd.append("shot1", new Blob([syntheticShot(214, 463, 0)], { type: "image/png" }), "screen-1.png");
  fd.append("shot2", new Blob([syntheticShot(214, 463, 1)], { type: "image/png" }), "screen-2.png");
  const r = await fetch(`${API}/import/bundle`, { method: "POST", headers: { "X-Commonplace-Upload": "1" }, body: fd });
  const rep = await r.json();
  assert.equal(r.status, 201, JSON.stringify(rep));
  // A fictional dream at 3:12 am and a quote, so the index and the space have variety.
  const dream = await call("POST", "/moments", { clientCaptureId: "00000000-0000-4000-8000-00000000d001", text: "Sample dream: a staircase made of folded paper that only opened for unasked questions.", timezone: "America/Los_Angeles" });
  await call("PATCH", `/moments/${dream.moment.id}`, { expectedRevision: 1, kind: "dream", occurredAt: "2025-11-02T11:12:00Z", people: [{ name: "Sample Friend", role: "sender" }] });
  const quote = await call("POST", "/moments", { clientCaptureId: "00000000-0000-4000-8000-00000000d002", text: "“A fixture quote, invented for tests.”", timezone: "Europe/Lisbon" });
  await call("PATCH", `/moments/${quote.moment.id}`, { expectedRevision: 1, kind: "quote", occurredAt: "2024-06-01T14:30:00Z" });
  const people = (await call("GET", "/people")).people;
  const friend = people.find((p) => p.name === "Sample Friend");
  await call("PATCH", `/people/${friend.id}`, { special: true });
  return { bundleId: rep.momentId, dreamId: dream.moment.id };
}

function discordFixtureDir() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "cp-discord-"));
  const exp = path.join(dir, "export");
  fs.mkdirSync(path.join(exp, "dreams.json_Files"), { recursive: true });
  fs.copyFileSync(path.join(root, "jazz-api/testdata/commonplace/discord-dreams.json"), path.join(exp, "dreams.json"));
  fs.writeFileSync(path.join(exp, "dreams.json_Files/sample-window-1A2B3.png"), syntheticShot(60, 60, 2));
  return exp;
}

(async () => {
  let browser, glBrowser;
  try {
    for (let i = 0; i < 200; i++) {
      try {
        const r = await fetch(base + "/commonplace/");
        if (r.ok) break;
      } catch {}
      if (child.exitCode !== null) throw Error(logs);
      await new Promise((r) => setTimeout(r, 150));
    }
    const { bundleId } = await seed();
    const exe = process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {};
    browser = await chromium.launch({ ...exe, headless: true, args: ["--no-sandbox"] });
    // The Idea space is checked with software WebGL2 (and, on the phone, the flat fallback).
    glBrowser = await chromium.launch({ ...exe, headless: true, args: ["--no-sandbox", "--use-gl=angle", "--use-angle=swiftshader", "--enable-unsafe-swiftshader"] });
    const errors = [];
    const newPage = async (width, height, opts = {}) => {
      const ctx = await (opts.gl ? glBrowser : browser).newContext({ viewport: { width, height }, deviceScaleFactor: 1, colorScheme: opts.colorScheme || "light", hasTouch: !!opts.touch, isMobile: !!opts.touch, reducedMotion: opts.motion ? "no-preference" : "reduce" });
      const page = await ctx.newPage();
      page.on("pageerror", (e) => errors.push(`${width}px: ${e.message}`));
      page.on("console", (m) => { if (m.type() === "error" && !/fonts\.g|Failed to load resource/.test(m.text())) errors.push(`${width}px console: ${m.text()}`); });
      // Offline: no third-party requests (fonts fall back to local serif/sans).
      await page.route(/^https?:\/\/(?!127\.0\.0\.1)/, (r) => r.abort());
      return page;
    };

    // ---------------- Desktop 1440 ----------------
    const page = await newPage(1440, 900);
    await page.goto(base + "/commonplace/");
    await page.waitForSelector(".card");
    assert.equal(await page.locator(".card").count(), 3);
    assert.ok(await page.locator(".card.dream").count(), "dreams get their night card");
    assert.ok(await page.locator(".card.special").count(), "a special person gets a warm signature");
    await page.screenshot({ path: path.join(out, "desktop-index.png") });

    // Capture: words only, then details afterwards.
    await page.fill("#capText", "Sample note: the fixture moon was a lantern someone forgot.");
    await page.click("#capSave");
    await page.waitForSelector(".sheet h2");
    assert.match(await page.evaluate(() => location.hash), /^#m\//);
    await page.fill("#dTitle", "Captured fixture");
    await page.locator("#dPeople .row input").first().fill("Example Cousin");
    await page.click('#detForm button[type="submit"]');
    await page.waitForSelector(".sheet", { state: "detached" });
    await page.waitForFunction(() => document.querySelector(".prov")?.textContent.includes("Example Cousin"));
    const count = async () => (await call("GET", "/moments?limit=200")).moments.length;

    // The share-sheet route keeps once: Back and a reload never capture again.
    const before = await count();
    await page.goto(base + "/commonplace/#");
    await page.waitForSelector(".card");
    const shareURL = base + "/commonplace/#add?text=Shared%20fixture%20note&url=https%3A%2F%2Fexample.com%2Fshared";
    await page.goto(shareURL);
    await page.waitForSelector(".sheet h2");
    const sharedHash = await page.evaluate(() => location.hash.replace(/\?.*$/, ""));
    assert.match(sharedHash, /^#m\//);
    assert.equal(await count(), before + 1);
    await page.goBack();
    await page.waitForFunction(() => !location.hash.startsWith("#add"));
    await page.waitForTimeout(300);
    assert.equal(await count(), before + 1, "Back must not re-run the share capture");
    await page.goto(base + "/commonplace/");
    await page.goto(shareURL); // as if the share link were opened or reloaded again
    await page.waitForFunction((h) => location.hash.startsWith(h), sharedHash);
    assert.equal(await count(), before + 1, "the same share link returns the same Moment");

    // A failed upload can be retried without making a second Moment.
    await page.goto(base + "/commonplace/");
    await page.waitForSelector("#capText");
    let failOnce = true;
    await page.route("**/commonplace/api/v1/commonplace/moments/*/artifacts", (r) => {
      if (failOnce) { failOnce = false; return r.fulfill({ status: 500, contentType: "application/json", body: '{"error":"simulated storage outage"}' }); }
      return r.continue();
    });
    const beforeRetry = await count();
    await page.fill("#capText", "Fixture with a picture");
    await page.setInputFiles("#capFiles", { name: "fixture.png", mimeType: "image/png", buffer: syntheticShot(40, 80, 3) });
    await page.click("#capSave");
    await page.waitForFunction(() => /simulated storage outage/.test(document.querySelector("#capStatus").textContent));
    await page.click("#capSave");
    await page.waitForSelector(".sheet h2");
    assert.equal(await count(), beforeRetry + 1, "a retry continues the same Moment");
    const retried = await call("GET", `/moments/${(await page.evaluate(() => location.hash)).slice(3).replace(/\?.*$/, "")}`);
    assert.equal(retried.artifacts.filter((a) => a.kind === "image").length, 1);
    await page.unroute("**/commonplace/api/v1/commonplace/moments/*/artifacts");
    await page.keyboard.press("Escape");

    // The bundle Moment: the hour lights the page, the label, the screenshots.
    await page.goto(`${base}/commonplace/#m/${bundleId}`);
    // Wait for this Moment, not the one still on screen from the capture above.
    await page.waitForFunction(() => /Sample Friend/.test((document.querySelector(".prov") || {}).textContent || ""));
    await page.waitForSelector(".shot img");
    await page.waitForFunction(() => [...document.querySelectorAll(".shot img")].every((i) => i.complete && i.naturalWidth > 0));
    assert.equal(await page.locator(".hour").first().innerText(), "11:41pm");
    assert.equal(await page.evaluate(() => document.body.dataset.light), "lamplit");
    assert.ok((await page.locator(".prov").innerText()).includes("Sample Friend"));
    assert.ok((await page.locator(".prov").innerText()).includes("You replied") === false, "fixture has no long reply gap");
    await page.screenshot({ path: path.join(out, "desktop-moment.png") });

    // Margin: open, keep a pencil note, erase another and undo.
    await page.keyboard.press("m");
    await page.waitForSelector(".grid.margin-open");
    const pencil = page.locator(".note.pencil").first();
    const pencilId = await pencil.getAttribute("data-n");
    await pencil.locator('[data-act="keep"]').click();
    await page.waitForFunction((id) => document.querySelector(`.note[data-n="${id}"]`)?.classList.contains("ink"), pencilId);
    let full = await call("GET", `/moments/${bundleId}`);
    assert.equal(full.annotations.find((n) => n.id === pencilId).state, "ink", "keep persists");
    const other = page.locator(".note.pencil").first();
    const otherId = await other.getAttribute("data-n");
    await other.locator('[data-act="erase"]').click();
    await page.waitForFunction((id) => !document.querySelector(`.note[data-n="${id}"]`), otherId);
    full = await call("GET", `/moments/${bundleId}`);
    assert.equal(full.annotations.find((n) => n.id === otherId).state, "erased", "erase persists");
    await page.click("#toastUndo");
    await page.waitForSelector(`.note[data-n="${otherId}"]`);
    full = await call("GET", `/moments/${bundleId}`);
    assert.equal(full.annotations.find((n) => n.id === otherId).state, "pencil", "undo restores");
    await page.screenshot({ path: path.join(out, "desktop-margin.png") });

    // Views keep your place: Text, then a note of the owner's own on a line.
    await page.click('[data-view="text"]');
    await page.waitForSelector(".tx-line");
    assert.equal(await page.locator(".tx-line").count(), 4);
    await page.locator(".tx-line").nth(1).hover();
    await page.locator("[data-addline]").nth(1).click();
    await page.fill("#cTitle", "My own fixture note, in ink");
    // A double submit writes the note once.
    await page.evaluate(() => { const f = document.querySelector("#composeForm"); f.requestSubmit(); f.requestSubmit(); });
    await page.waitForFunction(() => [...document.querySelectorAll(".note.ink .note-t")].some((e) => e.textContent.includes("My own fixture note")));
    const owned = (await call("GET", `/moments/${bundleId}`)).annotations.filter((n) => n.title === "My own fixture note, in ink");
    assert.equal(owned.length, 1, "the compose form submits once");
    await page.click('[data-view="link"]');
    await page.waitForSelector(".lk-page");
    await page.click('[data-view="shots"]');
    await page.waitForSelector(".thread .knot");
    assert.ok(await page.locator(".doors .door").count() >= 1, "doorways");
    await page.locator(".thread").scrollIntoViewIfNeeded();
    await page.screenshot({ path: path.join(out, "desktop-thread.png") });

    // Search from the index: ranked hits with highlighted snippets and anchors.
    await page.goto(base + "/commonplace/");
    await page.waitForSelector(".card");
    await page.fill("#q", "lighthouse diary");
    await page.waitForSelector(".result .hit mark");
    await page.screenshot({ path: path.join(out, "desktop-search.png") });
    await page.locator(".result .hit").first().click();
    await page.waitForSelector(".moment");

    // Discord import: folder pick, dry-run preview, then confirm.
    await page.goto(base + "/commonplace/#import");
    await page.waitForSelector("#dFolder");
    await page.setInputFiles("#dFolder", discordFixtureDir());
    await page.waitForSelector(".pv-group");
    assert.equal(await page.locator("#dPreview .pv-group").count(), 3, "three Moments from the fixture");
    assert.equal(await page.inputValue("#dKind"), "dream", "#dreams suggests the dream kind");
    assert.equal((await call("GET", "/moments?source=discord")).moments.length, 0, "the preview is a dry run");
    await page.screenshot({ path: path.join(out, "desktop-import-preview.png") });
    await page.locator('#dPreview input[name="me"]').nth(2).check();
    await page.click("#dGo");
    await page.waitForFunction(() => /Done:/.test(document.querySelector("#dStatusI").textContent));
    const discord = (await call("GET", "/moments?source=discord")).moments;
    assert.equal(discord.length, 3);
    assert.ok(discord.every((m) => m.kind === "dream"));

    // Idea space: real data, regions unnamed until named, dive hands off to the Moment view.
    const spacePage = await newPage(1440, 900, { gl: true, colorScheme: "dark", motion: true });
    await spacePage.goto(base + "/commonplace/#space");
    await spacePage.waitForFunction(() => globalThis.CommonplaceSpace && globalThis.CommonplaceSpace.state.items.length >= 7);
    const st = await spacePage.evaluate(() => ({ n: CommonplaceSpace.state.items.length, regions: CommonplaceSpace.state.regions.map((r) => r.name), gl: CommonplaceSpace.state.gl, depth: CommonplaceSpace.state.S.depthOnT }));
    assert.ok(st.regions.every((n) => n === ""), "the system never names regions");
    assert.ok(st.gl, "WebGL2 renderer");
    assert.equal(st.depth, 0, "the space opens level: every year at the same depth");
    await spacePage.waitForTimeout(400);
    await spacePage.screenshot({ path: path.join(out, `desktop-space${st.gl ? "" : "-flat"}.png`) });
    // Name a region inline (persists through the API).
    const nameBtn = spacePage.locator(".rlabel .namebtn").first();
    await nameBtn.dispatchEvent("click");
    await spacePage.waitForSelector(".rlabel input");
    await spacePage.keyboard.type("Fixture region");
    await spacePage.keyboard.press("Enter");
    await spacePage.waitForFunction(() => CommonplaceSpace.state.regions.some((r) => r.name === "Fixture region"));
    const space = await call("GET", "/space");
    assert.equal(space.regions.length, 1);
    assert.equal(space.regions[0].name, "Fixture region");
    assert.equal(await spacePage.evaluate(() => __commonplace.wispsRunning()), false, "the ambient wisps pause behind the Idea space");
    await spacePage.evaluate((id) => CommonplaceSpace.startDive(id), bundleId);
    await spacePage.waitForFunction((id) => location.hash.startsWith(`#m/${id}`), bundleId);
    await spacePage.waitForSelector(".moment");
    assert.equal(await spacePage.evaluate(() => __commonplace.wispsRunning()), true, "and resume in the Moment view");

    // ---------------- Phone 390 ----------------
    const phone = await newPage(390, 844, { touch: true, colorScheme: "dark" });
    await phone.goto(base + "/commonplace/");
    await phone.waitForSelector(".card");
    await phone.screenshot({ path: path.join(out, "phone-index.png") });
    await phone.goto(`${base}/commonplace/#m/${bundleId}?view=text`);
    await phone.waitForSelector(".tx-line");
    await phone.click("#marginBtn");
    await phone.waitForSelector(".tx-line + .note");
    const overflow = await phone.evaluate(() => document.documentElement.scrollWidth - innerWidth);
    assert.ok(overflow <= 0, `no sideways scroll on phones (${overflow}px)`);
    await phone.screenshot({ path: path.join(out, "phone-moment-text.png") });
    await phone.goto(`${base}/commonplace/#m/${bundleId}?view=shots`);
    await phone.waitForSelector(".shot img");
    await phone.screenshot({ path: path.join(out, "phone-moment.png") });
    await phone.goto(`${base}/commonplace/#import`);
    await phone.setInputFiles("#dFolder", discordFixtureDir());
    await phone.waitForSelector(".pv-group");
    assert.ok(/already here/i.test(await phone.locator("#dPreview").innerText()), "re-import preview knows what is already here");
    await phone.screenshot({ path: path.join(out, "phone-import-preview.png") });
    await phone.goto(`${base}/commonplace/#space?flat=1`);
    await phone.waitForFunction(() => globalThis.CommonplaceSpace && CommonplaceSpace.state.items.length >= 7);
    await phone.waitForSelector("#sp-card:not([hidden])");
    const focus = await phone.evaluate(() => { const s = CommonplaceSpace.state; return s.items[s.S.focus] && s.items[s.S.focus].id; });
    const newest = (await call("GET", "/space")).moments.map((m) => m).sort((a, b) => (b.occurredAt || b.createdAt).localeCompare(a.occurredAt || a.createdAt))[0];
    assert.equal(focus, newest.id, "on phones the space opens on the most recent memory");
    await phone.waitForTimeout(300);
    assert.equal(await phone.evaluate(() => CommonplaceSpace.state.gl), false, "flat Canvas2D fallback");
    await phone.screenshot({ path: path.join(out, "phone-space-flat.png") });
    // A signed-out session shows the shared banner.
    await phone.route("**/commonplace/api/**", (r) => r.fulfill({ status: 401, contentType: "application/json", body: '{"error":"signin_required","login":"/auth/login"}' }));
    await phone.goto(base + "/commonplace/");
    await phone.waitForSelector("#signin-guard-banner");
    errors.splice(0, errors.length, ...errors.filter((e) => !/401/.test(e)));

    assert.deepEqual(errors, []);
    console.log("Commonplace browser checks passed: capture (share route once, retry after a failed upload), Moment view (light, label, views), margin keep/erase/undo, owner note, thread and doorways, search, Discord import preview and confirm, Idea space (level, naming, dive, phone opens on newest), 390/1440 px, signed-out banner, no runtime errors.");
  } finally {
    if (browser) await browser.close();
    if (glBrowser) await glBrowser.close();
    try { await fetch(base + "/__preview_stop", { method: "POST" }); } catch {}
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
