/* Isolated Go/Postgres browser verification. All displayed prices/photos in
 * screenshots are clearly marked test fixtures; no production data is used. */
const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const { randomBytes, createHash } = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const { chromium } = require("playwright");
const root = path.resolve(__dirname, "../..");
const out =
  process.env.TRUMPETS_SCREENSHOT_DIR ||
  path.join(root, "docs/trumpets/screenshots");
fs.mkdirSync(out, { recursive: true });
const token = randomBytes(32).toString("hex");
const child = spawn(
  process.env.TRUMPETS_GO || "go",
  ["test", "-run", "^TestTrumpetBrowserPreview$", "-v", "-timeout", "5m"],
  {
    cwd: path.join(root, "jazz-api"),
    env: {
      ...process.env,
      TRUMPETS_PREVIEW: "1",
      TRUMPETS_MACHINE_USER: "visual-test",
      TRUMPETS_MACHINE_TOKEN_SHA256: createHash("sha256")
        .update(token)
        .digest("hex"),
    },
  },
);
let logs = "";
child.stdout.on("data", (d) => (logs += d));
child.stderr.on("data", (d) => (logs += d));
const base = "http://127.0.0.1:4173";
// Synthetic gradient PNG (not a photograph) for inspiration upload fixtures.
function syntheticPng(width, height) {
  const zlib = require("node:zlib");
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
  const rows = [];
  for (let y = 0; y < height; y++) {
    const row = Buffer.alloc(1 + width * 3);
    for (let x = 0; x < width; x++) {
      row[1 + x * 3] = 90 + Math.round((120 * y) / height);
      row[2 + x * 3] = 70 + Math.round((80 * x) / width);
      row[3 + x * 3] = 40;
    }
    rows.push(row);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", header),
    chunk("IDAT", zlib.deflateSync(Buffer.concat(rows))),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}
(async () => {
  let browser;
  try {
    // The preview is a `go test` binary: with a cold build cache compiling it
    // takes well over a minute (CI runs `go test ./...` first, so it is warm).
    for (let i = 0, ready = false; !ready; i++) {
      try {
        ready = (await fetch(base + "/trumpets/")).ok;
      } catch {}
      if (ready) break;
      if (child.exitCode !== null) throw Error(logs);
      if (i >= 1200) throw Error("Go preview server did not start on " + base + " within 3 minutes\n" + logs);
      await new Promise((r) => setTimeout(r, 150));
    }
    browser = await chromium.launch({
      ...(process.env.CHROMIUM_PATH
        ? { executablePath: process.env.CHROMIUM_PATH }
        : {}),
      headless: true,
      // A fake microphone for the hold-to-talk voice note check.
      args: ["--no-sandbox", "--use-fake-ui-for-media-stream", "--use-fake-device-for-media-stream"],
    });
    const page = await browser.newPage({
      viewport: { width: 1440, height: 1450 },
    });
    // TRUMPETS_CPU_THROTTLE=4 slows the page's CPU like a busy CI runner
    // (for reproducing timing-dependent failures locally).
    const throttle = async (p) => {
      const rate = Number(process.env.TRUMPETS_CPU_THROTTLE || 1);
      if (rate > 1) await (await p.context().newCDPSession(p)).send("Emulation.setCPUThrottlingRate", { rate });
    };
    await throttle(page);
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    // Never request external photos in tests. The SVG is an instrument-study
    // illustration, explicitly labeled a fixture rather than a horn photograph.
    await page.route("https://fixtures.invalid/horn.svg", (r) =>
      r.fulfill({
        contentType: "image/svg+xml",
        body: fs.readFileSync(path.join(root, "assets/trumpets/horn.svg")),
      }),
    );
    await page.goto(base + "/trumpets/");
    await page.waitForFunction(
      () => document.querySelector("#all-count").textContent === "21",
    );
    const report = {
      externalId: "visual-fixture",
      kind: "combined",
      status: "succeeded",
      sources: [
        {
          source: "Independent dealer · TEST FIXTURE",
          status: "checked",
          domain: "fixtures.invalid",
          query: "site:fixtures.invalid used trumpet",
          pagesOpened: 3,
          verifiedOffers: 3,
          geography: "International",
          specialty: "Visual fixture",
          candidates: 3,
        },
        { source: "Reverb", status: "checked", candidates: 0 },
        {
          source: "TC Gakki / Japanese shops",
          status: "checked",
          candidates: 0,
        },
      ],
      listings: [
        {
          maker: "Harrelson",
          model: "MUSE",
          title: "Harrelson MUSE · TEST FIXTURE",
          url: "https://fixtures.invalid/muse",
          source: "Independent dealer · TEST FIXTURE",
          seller: "Visual test dealer",
          price: 4250,
          currency: "USD",
          status: "active",
          searchScore: 94,
          searchRationale:
            "TEST FIXTURE · Raw brass, unusual engineering and one-off provenance. Synthetic price for visual verification.",
          details: {
            finish: "raw brass",
            provenance: "Test fixture; no real listing or seller.",
          },
          tags: [
            "raw brass",
            "weird engineering",
            "provenance",
            "TEST FIXTURE",
          ],
          images: ["https://fixtures.invalid/horn.svg"],
        },
        {
          maker: "Taylor",
          model: "Chicago II one-off upswept",
          title: "Taylor Chicago II · TEST FIXTURE",
          url: "https://fixtures.invalid/taylor",
          source: "Independent dealer · TEST FIXTURE",
          price: 3500,
          currency: "USD",
          status: "active",
          searchScore: 91,
          searchRationale:
            "TEST FIXTURE · One-off upswept bell and raw brass. Synthetic price; illustration is not a listing photo.",
          details: { finish: "raw brass" },
          tags: ["one-off", "upswept", "raw brass", "TEST FIXTURE"],
          images: ["https://fixtures.invalid/horn.svg"],
        },
        {
          maker: "AR Resonance",
          model: "Feroce",
          title: "AR Resonance Feroce · TEST FIXTURE",
          url: "https://fixtures.invalid/feroce",
          source: "Independent dealer · TEST FIXTURE",
          price: 4100,
          currency: "EUR",
          status: "active",
          searchScore: 89,
          searchRationale:
            "TEST FIXTURE · Raw nickel and boutique engineering. Synthetic price for visual verification.",
          details: { finish: "raw nickel" },
          tags: ["raw nickel", "mixed metals", "TEST FIXTURE"],
          images: ["https://fixtures.invalid/horn.svg"],
          verifiedVia: "search-provider",
        },
      ],
    };
    report.listings.push({
      maker: "Lawler",
      model: "C7 search hint",
      title: "Lawler C7 · UNVERIFIED TEST FIXTURE",
      url: "https://fixtures.invalid/products/lawler-c7",
      source: "Independent dealer · TEST FIXTURE",
      status: "stale",
      verificationState: "candidate",
      price: null,
      searchScore: 72,
      searchRationale: "Unverified test lead. Price and availability unknown.",
      tags: ["TEST FIXTURE"],
    });
    const response = await fetch(
      base + "/trumpets/api/v1/trumpets/machine/runs",
      {
        method: "POST",
        headers: {
          Authorization: "Bearer " + token,
          "Content-Type": "application/json",
        },
        body: JSON.stringify(report),
      },
    );
    assert.equal(response.status, 200, await response.text());
    await page.getByRole("button", { name: "Refresh board" }).click();
    await page.waitForFunction(
      () => document.querySelectorAll(".card").length === 3,
    );
    // Freshness shows the latest run and how it ended, beside the last complete run.
    assert.match(await page.locator("#last-run").innerText(), /SUCCEEDED/);
    assert.equal(await page.locator("#last-run-note").innerText(), "complete");
    // Verification provenance shows subtly; direct page checks carry no label.
    assert.equal(await page.locator(".card-foot").filter({ hasText: "via search provider" }).count(), 1);
    assert.notEqual(await page.locator("#last-complete").innerText(), "Never");
    await page.screenshot({ path: path.join(out, "today.png") });
    await page.locator(".coverage > summary").click();
    await page.locator(".source-universe > summary").click();
    assert.ok(await page.locator(".source-record").count() >= 50);
    assert.match(await page.locator("#source-count").innerText(), /1 searched \/ 1 live/);
    for (const width of [320, 390, 768, 1440]) {
      await page.setViewportSize({ width, height: 1100 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `source universe overflow at ${width}px`);
    }
    await page.setViewportSize({ width: 390, height: 1450 });
    await page.screenshot({ path: path.join(out, "mobile-coverage.png") });
    await page.locator(".coverage > summary").click();
    await page.setViewportSize({ width: 1440, height: 1450 });
    await page.getByRole("button", { name: /All tracked/ }).click();
    assert.equal(await page.locator(".card").count(), 21); // References and verified offers group by physical horn.
    await page.screenshot({ path: path.join(out, "all-tracked.png") });
    assert.equal(
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      ),
      false,
    );
    await page.getByRole("button", { name: /Candidates/ }).click();
    assert.equal(await page.locator(".card").count(), 1);
    assert.match(await page.locator(".card").innerText(), /UNVERIFIED/);
    assert.match(await page.locator(".card").innerText(), /Price unverified/);
    await page.screenshot({ path: path.join(out, "candidates.png") });
    for (const width of [320, 390, 768, 1440]) {
      await page.setViewportSize({ width, height: 1000 });
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth > innerWidth,
        ),
        false,
        `candidate overflow at ${width}px`,
      );
    }
    await page.setViewportSize({ width: 1440, height: 1450 });
    await page.locator(".notes").first().click();
    await page.locator("[name=interestState]").selectOption("pass");
    await page.getByRole("button", { name: "Save feedback" }).click();
    await page.waitForFunction(
      () => document.querySelectorAll(".card").length === 0,
    );
    await page.getByRole("button", { name: "Close listing" }).click();
    await page.reload();
    await page.waitForFunction(
      () => document.querySelector("#all-count").textContent === "24",
    );
    await page.getByRole("button", { name: /Candidates/ }).click();
    assert.equal(await page.locator(".card").count(), 0);
    await page.locator("#filters [name=interest]").selectOption("pass");
    assert.equal(await page.locator(".card").count(), 1);
    await page.locator(".notes").first().click();
    await page.locator("[name=interestState]").selectOption("watch");
    await page.getByRole("button", { name: "Save feedback" }).click();
    await page.waitForFunction(
      () => document.querySelectorAll(".card").length === 0,
    );
    await page.getByRole("button", { name: "Close listing" }).click();
    await page.locator("#filters [name=interest]").selectOption("");
    assert.equal(await page.locator(".card").count(), 1);
    await page.setViewportSize({ width: 1440, height: 1450 });
    await page.getByRole("button", { name: /All tracked/ }).click();
    await page.locator(".notes").first().click();
    await page
      .locator("textarea[name=notes]")
      .fill("Verification fixture: private patina preference");
    await page.locator("[name=rating]").selectOption("5");
    await page.getByRole("button", { name: "Save feedback" }).click();
    await page.waitForFunction(
      () =>
        document.querySelector("#save-message").textContent ===
        "Saved privately.",
    );
    await page.getByRole("button", { name: "Close listing" }).click();
    await page.reload();
    await page.waitForFunction(
      () => document.querySelector("#all-count").textContent === "24",
    );
    await page.locator(".notes").first().click();
    assert.equal(
      await page.locator("textarea[name=notes]").inputValue(),
      "Verification fixture: private patina preference",
    );
    await page.getByRole("button", { name: "Close listing" }).click();
    await page.locator("[name=interest]").selectOption("watch");
    assert.equal(await page.locator(".card").count(), 3);
    await page.locator("[name=query]").fill("raw nickel");
    assert.equal(await page.locator(".card").count(), 1);
    await page.locator("[name=query]").fill("");
    await page.locator(".favorite").first().click();
    await page.waitForFunction(
      () =>
        document.querySelector(".favorite").getAttribute("aria-pressed") ===
        "true",
    );
    await page.locator("#filters [name=favorite]").check();
    assert.equal(await page.locator(".card").count(), 1);
    await page.locator("#filters [name=favorite]").uncheck();
    for (const width of [320, 390, 768, 1440]) {
      await page.setViewportSize({ width, height: width < 650 ? 1000 : 1100 });
      assert.equal(
        await page.evaluate(
          () => document.documentElement.scrollWidth > innerWidth,
        ),
        false,
        `page overflow at ${width}px`,
      );
      if (width < 650) {
        assert.equal(await page.locator("#filter-options").isVisible(), false);
        await page.getByRole("button", { name: /Filters & sort/ }).click();
        assert.equal(await page.locator("#filter-options").isVisible(), true);
        assert.equal(
          await page.evaluate(
            () => document.documentElement.scrollWidth > innerWidth,
          ),
          false,
          `expanded controls overflow at ${width}px`,
        );
        await page.getByRole("button", { name: /Filters & sort/ }).click();
        const targets = await page
          .locator(".favorite,.rating,.notes,.listing-link")
          .evaluateAll((nodes) =>
            nodes.map((n) => n.getBoundingClientRect().height),
          );
        assert.ok(
          targets.every((h) => h >= 44),
          `small touch target at ${width}px`,
        );
      }
      await page.locator(".notes").first().click();
      assert.equal(
        await page
          .locator("#detail")
          .evaluate((n) => n.scrollWidth > n.clientWidth),
        false,
        `dialog overflow at ${width}px`,
      );
      assert.ok(
        await page
          .locator("#detail")
          .evaluate((n) => n.getBoundingClientRect().right <= innerWidth),
        `dialog offscreen at ${width}px`,
      );
      await page.getByRole("button", { name: "Close listing" }).click();
    }
    await page.setViewportSize({ width: 390, height: 1450 });
    await page.screenshot({ path: path.join(out, "mobile.png") });
    await page.locator(".notes").first().click();
    // Synthetic note is used only for test persistence, not published in screenshots.
    await page.locator("textarea[name=notes]").fill("");
    await page.screenshot({ path: path.join(out, "mobile-notes.png") });
    await page.getByRole("button", { name: "Close listing" }).click();
    // ---- Horn inspiration board (synthetic fixtures only) ----------------
    // Previews come from the loopback fixture oEmbed; images are stored in the
    // preview's in-memory bucket. No third-party request is allowed until an
    // embed is explicitly played, and that one is fulfilled locally.
    const thirdParty = [];
    page.on("request", (r) => {
      if (!r.url().startsWith(base) && !r.url().startsWith("data:") && !r.url().startsWith("blob:")) thirdParty.push(r.url());
    });
    await page.route("https://www.youtube-nocookie.com/**", (r) =>
      r.fulfill({ contentType: "text/html", body: "<!doctype html><title>embed fixture</title><body style='background:#000;color:#fff'>Embed fixture</body>" }),
    );
    const cards = page.locator(".inspiration-card");
    await page.setViewportSize({ width: 1440, height: 1100 });
    await page.getByRole("button", { name: /Inspiration/ }).click();
    assert.equal(await page.evaluate(() => location.hash), "#inspiration");
    assert.equal(await page.locator("#cards").isVisible(), false);
    assert.equal(await page.locator(".telemetry").isVisible(), false);
    // First visit seeds the Harrelson short, then enriches it from the fixture.
    await page.waitForFunction(() => document.querySelectorAll(".inspiration-card").length === 1);
    await page.waitForFunction(() => /1st Harrelson Muse/.test(document.querySelector(".inspiration-card")?.textContent || ""));
    await page.waitForFunction(() => document.querySelector(".inspiration-card img")?.complete && document.querySelector(".inspiration-card img").naturalWidth > 0);
    assert.match(await cards.first().innerText(), /Harrelson · MUSE/i);
    assert.match(await cards.first().innerText(), /▶ Short/);
    // Paste a link into the empty capture bar: the card appears synchronously.
    await page.locator("#inspiration-capture").focus();
    const appeared = await page.evaluate(() => {
      const before = document.querySelectorAll(".inspiration-card").length;
      const dt = new DataTransfer();
      dt.setData("text/plain", "https://youtu.be/dQw4w9WgXcQ?si=fixture");
      const started = performance.now();
      document.querySelector("#inspiration-capture").dispatchEvent(new ClipboardEvent("paste", { clipboardData: dt, bubbles: true, cancelable: true }));
      return { added: document.querySelectorAll(".inspiration-card").length - before, ms: performance.now() - started };
    });
    assert.equal(appeared.added, 1);
    assert.ok(appeared.ms < 300, `card took ${appeared.ms}ms`);
    await page.waitForFunction(() => document.querySelector("#inspiration-toast-action")?.textContent === "Undo" && !document.querySelector("#inspiration-toast").hidden);
    await page.waitForFunction(() => /Upswept bell study/.test(document.querySelector("#inspiration-grid").textContent));
    assert.equal(await page.locator("#quick-strip").isVisible(), true);
    // A different form of the same video is a duplicate.
    await page.locator("#inspiration-capture").fill("https://www.youtube.com/watch?v=dQw4w9WgXcQ&feature=share");
    await page.keyboard.press("Enter");
    await page.waitForFunction(() => document.querySelector("#inspiration-toast-text").textContent === "Already on your board");
    assert.equal(await cards.count(), 2);
    // Plain text creates nothing.
    await page.locator("#inspiration-capture").fill("just some words");
    await page.keyboard.press("Enter");
    assert.equal(await page.locator("#capture-hint").innerText(), "Paste a link or an image");
    await page.locator("#inspiration-capture").fill("");
    // Photo via the 📷 file input at phone width; the local preview shows at once.
    const fixturePng = syntheticPng(360, 480);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.locator("#capture-file").setInputFiles({ name: "fixture-photo.png", mimeType: "image/png", buffer: fixturePng });
    await page.waitForFunction(() => document.querySelectorAll(".inspiration-card").length === 3);
    assert.match(await cards.first().locator("img").getAttribute("src"), /^blob:/);
    await page.waitForFunction(() => !/Uploading/.test(document.querySelector("#inspiration-grid").textContent));
    assert.match(await cards.first().innerText(), /Photo/);
    // One-tap priority in the "Just added" strip autosaves.
    await Promise.all([
      page.waitForResponse((r) => r.url().includes("/inspiration/") && r.request().method() === "PATCH" && r.status() === 200),
      page.locator("#quick-priority").getByRole("button", { name: "Actively hunting" }).click(),
    ]);
    await page.screenshot({ path: path.join(out, "inspiration-mobile.png") });
    for (const width of [320, 390, 768, 1440]) {
      await page.setViewportSize({ width, height: 1000 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `inspiration overflow at ${width}px`);
    }
    // Clipboard screenshot and a two-file drop each create entries.
    await page.setViewportSize({ width: 1440, height: 1100 });
    await page.evaluate((bytes) => {
      const file = (name) => new File([new Uint8Array(bytes)], name, { type: "image/png" });
      const paste = new DataTransfer();
      paste.items.add(file("pasted.png"));
      document.body.dispatchEvent(new ClipboardEvent("paste", { clipboardData: paste, bubbles: true, cancelable: true }));
      const drop = new DataTransfer();
      drop.items.add(file("drop-a.png"));
      drop.items.add(file("drop-b.png"));
      document.dispatchEvent(new DragEvent("dragenter", { dataTransfer: drop, bubbles: true, cancelable: true }));
      document.dispatchEvent(new DragEvent("drop", { dataTransfer: drop, bubbles: true, cancelable: true }));
    }, [...syntheticPng(320, 240)]);
    await page.waitForFunction(() => document.querySelectorAll(".inspiration-card").length === 6);
    await page.waitForFunction(() => !/Uploading|Saving/.test(document.querySelector("#inspiration-grid").textContent));
    assert.equal(await page.locator("#drop-overlay").isVisible(), false);
    // Filter by priority, then clear.
    await page.locator("#priority-filter").getByRole("button", { name: "Actively hunting" }).click();
    assert.equal(await cards.count(), 1);
    await page.locator("#priority-filter").getByRole("button", { name: "Actively hunting" }).click();
    await page.locator("#inspiration-filters [name=source]").selectOption("youtube");
    assert.equal(await cards.count(), 2);
    await page.locator("#inspiration-filters [name=source]").selectOption("");
    // Detail: edit the maker of the photo; it autosaves with a revision.
    await cards.filter({ hasText: "Actively hunting" }).click();
    assert.match(await page.evaluate(() => location.hash), /^#inspiration\/[0-9a-f-]{36}$/);
    await page.locator("#inspiration-form [name=maker]").fill("Monette");
    await page.waitForFunction(() => document.querySelector("#inspiration-save").textContent === "Saved");
    await page.getByRole("button", { name: "Close inspiration" }).click();
    await page.waitForFunction(() => location.hash === "#inspiration");
    // The seed suggests (never auto-links) the Observatory's Harrelson MUSE.
    await cards.filter({ hasText: "1st Harrelson Muse" }).click();
    assert.equal(await page.locator("#inspiration-media iframe").count(), 0, "embeds are click-to-load");
    await page.getByRole("button", { name: "Play video" }).click();
    assert.equal(await page.locator("#inspiration-media iframe").getAttribute("referrerpolicy"), "strict-origin-when-cross-origin");
    assert.match(await page.locator("#inspiration-media iframe").getAttribute("src"), /^https:\/\/www\.youtube-nocookie\.com\/embed\/qHetQ-t4Wi0\?autoplay=1&playsinline=1$/);
    await page.getByRole("button", { name: "Link Harrelson MUSE" }).click();
    await page.waitForFunction(() => /Tracked in Observatory/.test(document.querySelector("#inspiration-horn").textContent));
    await page.screenshot({ path: path.join(out, "inspiration-detail.png") });
    assert.equal(await page.locator("#inspiration-detail").evaluate((n) => n.scrollWidth > n.clientWidth), false);
    await page.getByRole("button", { name: "Close inspiration" }).click();
    // Share-sheet route: the link leaves the address bar immediately.
    await page.goto("about:blank");
    await page.goto(base + "/trumpets/#inspiration/add?url=" + encodeURIComponent("https://www.instagram.com/reel/C9xYz12AbCd/?igsh=fixture") + "&note=" + encodeURIComponent("Love the bell flare"));
    await page.waitForFunction(() => document.querySelectorAll(".inspiration-card").length === 7);
    assert.equal(await page.evaluate(() => location.hash), "#inspiration");
    await page.waitForFunction(() => /Instagram reel/.test(document.querySelector("#inspiration-grid").textContent));
    assert.match(await page.locator("#inspiration-grid").innerText(), /Add a screenshot for a preview/);
    assert.match(await cards.filter({ hasText: "Monette" }).innerText(), /Actively hunting/i);
    assert.match(await cards.filter({ hasText: "1st Harrelson Muse" }).innerText(), /Tracked in Observatory/);
    // Private data never reaches browser storage.
    assert.deepEqual(await page.evaluate(() => [localStorage.length, sessionStorage.length]), [0, 0]);
    await page.locator("#quick-close").click();
    await page.waitForFunction(() => document.querySelector("#inspiration-toast").hidden, null, { timeout: 10000 });
    await page.screenshot({ path: path.join(out, "inspiration.png"), fullPage: true });
    // The Observatory detail shows "Inspiration (1)" for the linked horn.
    await page.getByRole("button", { name: /All tracked/ }).click();
    assert.equal(await page.evaluate(() => location.hash), "");
    await page.locator(".card").filter({ has: page.locator(".model", { hasText: /^MUSE$/ }) }).first().locator(".notes").click();
    await page.waitForFunction(() => document.querySelector("#detail-inspiration-title")?.textContent === "Inspiration (1)");
    await page.locator("#detail-inspiration-list a").first().click();
    await page.waitForFunction(() => document.querySelector("#inspiration-detail").open && /1st Harrelson Muse/.test(document.querySelector("#inspiration-detail-title").textContent));
    assert.equal(await page.locator("#detail").evaluate((n) => n.open), false);
    await page.getByRole("button", { name: "Close inspiration" }).click();
    // ---- Taste training: Swipe and Head to head (synthetic fixtures only) ----
    // Head to head compares only active, verified listings with a photo and a
    // price (at least six): three more such fixtures join the three above.
    {
      const fixture = (maker, model, price) => ({
        maker,
        model,
        title: `${maker} ${model} · TEST FIXTURE`,
        url: `https://fixtures.invalid/compare/${model.toLowerCase().replace(/\W+/g, "-")}`,
        source: "Independent dealer · TEST FIXTURE",
        price,
        currency: "USD",
        status: "active",
        searchScore: 80,
        searchRationale: "TEST FIXTURE · A comparable listing for Head to head. Synthetic price.",
        tags: ["TEST FIXTURE"],
        images: ["https://fixtures.invalid/horn.svg"],
      });
      const comparable = await fetch(base + "/trumpets/api/v1/trumpets/machine/runs", {
        method: "POST",
        headers: { Authorization: "Bearer " + token, "Content-Type": "application/json" },
        body: JSON.stringify({
          externalId: "visual-fixture-comparable",
          kind: "combined",
          status: "succeeded",
          sources: [{ source: "Independent dealer · TEST FIXTURE", status: "checked", candidates: 3 }],
          listings: [fixture("Bach", "Stradivarius 37 fixture", 2400), fixture("Yamaha", "Xeno 8335 fixture", 1900), fixture("Monette", "Prana fixture", 6100)],
        }),
      });
      assert.equal(comparable.status, 200, await comparable.text());
      await Promise.all([page.waitForResponse((r) => r.url().endsWith("/v1/trumpets/listings")), page.locator("#refresh").evaluate((b) => b.click())]);
    }
    const tastePosts = () => page.waitForResponse((r) => r.url().includes("/taste/events") && r.request().method() === "POST" && r.status() < 300);
    const swipeId = () => page.locator(".swipe-card").getAttribute("data-id");
    await page.setViewportSize({ width: 1440, height: 1100 });
    // Opening Taste reloads its state (the queue now includes the fixtures
    // just added); read the first card only once that reply is on screen.
    const tasteLoaded = page.waitForResponse((r) => r.url().endsWith("/v1/trumpets/taste") && r.request().method() === "GET");
    await page.getByRole("button", { name: /^Taste/ }).click();
    const loadedQueue = (await (await tasteLoaded).json()).queue;
    await page.waitForFunction((id) => document.querySelector(".swipe-card")?.dataset.id === id, loadedQueue[0].listingId);
    assert.equal(await page.evaluate(() => location.hash), "#taste");
    assert.equal(await page.locator("#cards").isVisible(), false);
    assert.equal(await page.locator("#inspiration-board").isVisible(), false);
    await page.waitForSelector(".swipe-card");
    assert.match(await page.locator("#taste-confidence-text").innerText(), /Just starting · 0 reactions/);
    const firstCard = await swipeId();
    // Keyboard: → is "cool"; the next card is a different horn.
    await Promise.all([tastePosts(), page.keyboard.press("ArrowRight")]);
    await page.waitForFunction((id) => document.querySelector(".swipe-card")?.dataset.id !== id, firstCard);
    assert.match(await page.locator("#taste-confidence-text").innerText(), /1 reaction\b/);
    // Undo brings the same card back and removes the reaction.
    await Promise.all([tastePosts(), page.getByRole("button", { name: "Undo (Z)" }).first().click()]);
    await page.waitForFunction((id) => document.querySelector(".swipe-card")?.dataset.id === id, firstCard);
    assert.match(await page.locator("#taste-confidence-text").innerText(), /0 reactions/);
    // A one-tap chip on the card persists as a tag event.
    await Promise.all([tastePosts(), page.locator("#taste-chips").getByRole("button", { name: "raw brass" }).click()]);
    assert.equal(await page.locator("#taste-chips").getByRole("button", { name: "raw brass" }).getAttribute("aria-pressed"), "true");
    // Optional text note on the current card.
    await page.locator("#taste-note-box > summary").click();
    await page.locator("#taste-note").fill("Fixture note: love the raw brass, but too expensive");
    await Promise.all([tastePosts(), page.getByRole("button", { name: "Save note" }).click()]);
    await page.waitForFunction(() => /Note saved privately/.test(document.querySelector("#taste-status").textContent));
    assert.match(await page.locator("#taste-notes").innerText(), /Likes raw brass/);
    // Hold-to-talk: a fake microphone records while the button is held.
    await page.locator("#taste-voice").hover();
    await page.mouse.down();
    await page.waitForFunction(() => document.querySelector("#taste-voice").classList.contains("recording"));
    await page.waitForTimeout(700);
    await Promise.all([page.waitForResponse((r) => r.url().includes("/taste/voice?") && r.status() === 201), page.mouse.up()]);
    await page.waitForFunction(() => document.querySelector("#taste-notes audio"));
    // Pointer drag: a long drag to the right is a swipe; a short one springs back.
    const box = await page.locator(".swipe-card").boundingBox();
    const cx = box.x + box.width / 2,
      cy = box.y + 120;
    const beforeDrag = await swipeId();
    await page.mouse.move(cx, cy);
    await page.mouse.down();
    await page.mouse.move(cx + 40, cy, { steps: 4 });
    await page.mouse.up();
    assert.equal(await swipeId(), beforeDrag, "short drag springs back");
    await page.mouse.move(cx, cy);
    await page.mouse.down();
    await page.mouse.move(cx + 260, cy + 10, { steps: 10 });
    await Promise.all([tastePosts(), page.mouse.up()]);
    await page.waitForFunction((id) => document.querySelector(".swipe-card")?.dataset.id !== id, beforeDrag);
    // Review fix: a press near the card's edge that leaves the card and is
    // released outside (before the axis locks) must not leave a drag behind
    // that a later hover and click would commit.
    {
      let posts = 0;
      const count = (r) => r.method() === "POST" && r.url().includes("/taste/events") && posts++;
      page.on("request", count);
      const edgeId = await swipeId();
      const card = await page.locator(".swipe-card").boundingBox();
      const y = card.y + 120;
      await page.mouse.move(card.x + 3, y);
      await page.mouse.down();
      await page.mouse.move(card.x - 6, y); // 9 px: out of the card, no axis yet
      await page.mouse.up(); // released outside the card
      await page.mouse.move(card.x + card.width / 2, y, { steps: 3 });
      await page.mouse.move(card.x + card.width - 10, y, { steps: 6 }); // hover far to the right
      await page.mouse.down();
      await page.mouse.up();
      await page.waitForTimeout(600);
      page.off("request", count);
      assert.equal(posts, 0, "a release outside the card leaves no drag behind");
      assert.equal(await swipeId(), edgeId);
      assert.equal(await page.locator(".swipe-card").evaluate((c) => c.style.transform), "", "the card does not follow the hover");
    }
    // Buttons: must see, not for me.
    await Promise.all([tastePosts(), page.getByRole("button", { name: "Must see (up arrow)" }).click()]);
    await Promise.all([tastePosts(), page.getByRole("button", { name: "Not for me (left arrow)" }).click()]);
    await page.waitForFunction(() => /3 reactions/.test(document.querySelector("#taste-confidence-text").textContent));
    // A few more "not for me" so a disliked feature is seen on two horns (features
    // unique to one horn keep their prior).
    for (let i = 0; i < 3; i++) await Promise.all([tastePosts(), page.keyboard.press("ArrowLeft")]);
    await page.waitForFunction(() => /6 reactions/.test(document.querySelector("#taste-confidence-text").textContent));
    assert.ok((await page.locator("#taste-liked li:not(.none)").count()) > 0, "liked traits listed");
    assert.ok((await page.locator("#taste-disliked li:not(.none)").count()) > 0, "disliked traits listed");
    // Head to head: tap a side, then answer by keyboard; undo the last choice.
    await page.getByRole("button", { name: "Head to head" }).click();
    await page.waitForFunction(() => document.querySelectorAll(".pair-card").length === 2);
    assert.equal(await page.locator("#swipe-stage").isVisible(), false);
    const pairBefore = await page.locator(".pair-card").evaluateAll((n) => n.map((c) => c.getAttribute("aria-label")).join("|"));
    await Promise.all([tastePosts(), page.locator(".pair-card").first().click()]);
    await page.waitForFunction((p) => [...document.querySelectorAll(".pair-card")].map((c) => c.getAttribute("aria-label")).join("|") !== p, pairBefore);
    await Promise.all([tastePosts(), page.keyboard.press("ArrowDown")]);
    await Promise.all([tastePosts(), page.keyboard.press("z")]);
    await page.waitForFunction(() => /7 reactions/.test(document.querySelector("#taste-confidence-text").textContent));
    assert.match(await page.locator("#taste-graph").innerText(), /^Comparisons connect (all \d+|\d+ of \d+) eligible horns( \(\d+ islands?\))?$/, "the learned panel says how well comparisons connect the horns");
    await page.getByRole("button", { name: "Swipe", exact: true }).click();
    await page.evaluate(() => scrollTo(0, document.querySelector(".taste-modes").getBoundingClientRect().top + scrollY - 20));
    await page.screenshot({ path: path.join(out, "taste-desktop.png") });
    // Too few comparable listings: Head to head explains why instead of
    // pairing weak ones.
    {
      const short = async (route) => {
        const response = await route.fetch();
        const json = await response.json();
        Object.assign(json, { pairs: [], pairEligible: 2, pairMinimum: 6 });
        await route.fulfill({ response, json });
      };
      await page.route("**/trumpets/api/v1/trumpets/taste", short);
      await page.reload();
      await page.waitForSelector(".swipe-card, .taste-empty");
      await page.getByRole("button", { name: "Head to head" }).click();
      await page.locator("#pair-cards .pair-shortage").waitFor();
      assert.equal(await page.locator("#pair-cards .pair-shortage h3").innerText(), "Head to head needs more verified listings with photos — 2 eligible");
      assert.match(await page.locator("#pair-cards .pair-shortage p").innerText(), /active, verified listings with a photo and a price/);
      assert.equal(await page.locator(".pair-card").count(), 0);
      assert.equal(await page.locator("#pair-a").isDisabled(), true);
      await page.unroute("**/trumpets/api/v1/trumpets/taste", short);
      await page.getByRole("button", { name: "Swipe", exact: true }).click();
    }
    // ---- No photo: an obvious "View listing" link (new tab) that is never a
    // reaction; reactions record whether the card showed a photo. ----
    {
      const noPhoto = await fetch(base + "/trumpets/api/v1/trumpets/machine/runs", {
        method: "POST",
        headers: { Authorization: "Bearer " + token, "Content-Type": "application/json" },
        body: JSON.stringify({
          externalId: "visual-fixture-no-photo",
          kind: "combined",
          status: "succeeded",
          sources: [{ source: "Independent dealer · TEST FIXTURE", status: "checked", candidates: 1 }],
          listings: [
            {
              maker: "Benge",
              model: "3X no-photo fixture",
              title: "Benge 3X · NO PHOTO · TEST FIXTURE",
              url: "https://fixtures.invalid/products/benge-3x",
              source: "Independent dealer · TEST FIXTURE",
              price: 2100,
              currency: "USD",
              status: "active",
              searchScore: 70,
              searchRationale: "TEST FIXTURE · A listing whose source shows no photo.",
              tags: ["TEST FIXTURE"],
            },
          ],
        }),
      });
      assert.equal(noPhoto.status, 200, await noPhoto.text());
      // The source page opens in a new tab (served locally, never fetched).
      await page.context().route("https://fixtures.invalid/products/**", (r) => r.fulfill({ contentType: "text/html", body: "<title>Listing page · TEST FIXTURE</title>" }));
      const posted = [];
      const record = (r) => r.method() === "POST" && r.url().includes("/taste/events") && posted.push(JSON.parse(r.postData()));
      page.on("request", record);
      await page.reload();
      await page.waitForSelector(".swipe-card");
      // Cards with photos come first; react until a photo-less card with a link.
      for (let i = 0; i < 30 && !(await page.locator(".swipe-card .taste-view-listing").count()); i++) {
        const id = await swipeId();
        const hadPhoto = (await page.locator(".swipe-card .swipe-visual:not(.no-photo)").count()) === 1;
        assert.equal(await page.locator(".swipe-card .swipe-visual.no-photo").count(), hadPhoto ? 0 : 1);
        await Promise.all([tastePosts(), page.keyboard.press("ArrowLeft")]);
        await page.waitForFunction((id) => document.querySelector(".swipe-card")?.dataset.id !== id, id);
        assert.deepEqual(posted.at(-1).meta, { hadPhoto }, "each swipe records whether its card had a photo");
      }
      const link = page.locator(".swipe-card .taste-view-listing");
      assert.equal(await link.count(), 1, "a photo-less card shows a View listing link");
      assert.equal(await link.innerText(), "View listing ↗");
      assert.equal(await link.getAttribute("target"), "_blank");
      assert.match(await link.getAttribute("rel"), /noopener/);
      assert.match(await link.getAttribute("href"), /^https:\/\/fixtures\.invalid\/products\//);
      assert.equal(await page.locator(".swipe-card .swipe-visual.no-photo").count(), 1);
      const linkedId = await swipeId();
      const before = posted.length;
      const [tab] = await Promise.all([page.context().waitForEvent("page"), link.click()]);
      await tab.waitForLoadState();
      assert.match(tab.url(), /^https:\/\/fixtures\.invalid\/products\//);
      await tab.close();
      // A touch-style press and release on the link is not a swipe either.
      const lb = await link.boundingBox();
      await page.mouse.move(lb.x + lb.width / 2, lb.y + lb.height / 2);
      const [tab2] = await Promise.all([
        page.context().waitForEvent("page"),
        (async () => {
          await page.mouse.down();
          await page.mouse.up();
        })(),
      ]);
      await tab2.close();
      await page.waitForTimeout(500);
      assert.equal(posted.length, before, "opening the listing POSTs no reaction");
      assert.equal(await swipeId(), linkedId, "opening the listing keeps the card");
      await Promise.all([tastePosts(), page.keyboard.press("ArrowLeft")]);
      assert.deepEqual(posted.at(-1).meta, { hadPhoto: false }, "a reaction without a photo says so");
      // Head to head: the same link beside a photo-less side, never a choice.
      const photoId = await page.evaluate(async () => (await (await fetch("/trumpets/api/v1/trumpets/listings")).json()).listings.find((l) => l.images.length && !l.acquired).id);
      await page.route("**/trumpets/api/v1/trumpets/taste", async (route) => {
        const response = await route.fetch();
        const json = await response.json();
        json.pairs = [{ a: photoId, b: linkedId, p: 0.5, sd: 1 }, ...json.pairs.filter((p) => p.a !== linkedId && p.b !== linkedId)];
        await route.fulfill({ response, json });
      });
      await page.reload();
      await page.waitForSelector(".swipe-card, .taste-empty");
      await page.getByRole("button", { name: "Head to head" }).click();
      await page.waitForFunction(() => document.querySelectorAll(".pair-card").length === 2);
      const pairLink = page.locator(".pair-slot .taste-view-listing");
      assert.equal(await pairLink.count(), 1, "only the photo-less side has the link");
      assert.equal(await page.locator(".pair-slot").nth(1).locator(".taste-view-listing").count(), 1);
      assert.equal(await page.locator(".pair-card .taste-view-listing").count(), 0, "the link is not inside the choice button");
      const pairBefore = await page.locator(".pair-card").evaluateAll((n) => n.map((c) => c.getAttribute("aria-label")).join("|"));
      const pairPosts = posted.length;
      const [tab3] = await Promise.all([page.context().waitForEvent("page"), pairLink.click()]);
      await tab3.close();
      await page.waitForTimeout(500);
      assert.equal(posted.length, pairPosts, "opening a listing from head to head is not a choice");
      assert.equal(await page.locator(".pair-card").evaluateAll((n) => n.map((c) => c.getAttribute("aria-label")).join("|")), pairBefore);
      await Promise.all([tastePosts(), page.locator(".pair-card").first().click()]);
      assert.deepEqual(posted.at(-1).meta, { hadPhoto: false }, "a choice with a photo-less side says so");
      assert.equal(posted.at(-1).listingId, photoId);
      await page.unroute("**/trumpets/api/v1/trumpets/taste");
      page.off("request", record);
      await page.getByRole("button", { name: "Swipe", exact: true }).click();
    }
    await page.getByRole("button", { name: "Head to head" }).click();
    // Owner-stated preferences save as a new version and survive a reload.
    await page.locator("#taste-prefs-box > summary").click();
    await page.locator("#taste-prefs [name=statements]").fill("Fixture: cool horn, not dark horn");
    await page.locator("#taste-prefs [name=likedTraits]").fill("raw brass, upswept bell");
    await page.locator("#taste-prefs [name=dislikedMakers]").fill("Fixture Avoided Maker");
    await page.getByRole("button", { name: "Save preferences" }).click();
    await page.waitForFunction(() => document.querySelector("#taste-prefs-status").textContent === "Saved");
    await page.reload();
    await page.waitForSelector(".pair-card, .swipe-card");
    assert.equal(await page.locator("#taste-board").isVisible(), true, "#taste reopens the taste view");
    await page.locator("#taste-prefs-box > summary").click();
    assert.equal(await page.locator("#taste-prefs [name=statements]").inputValue(), "Fixture: cool horn, not dark horn");
    assert.equal(await page.locator("#taste-prefs [name=likedTraits]").inputValue(), "raw brass, upswept bell");
    // The learned score now ranks Today (and is visible on cards).
    await page.getByRole("button", { name: /All tracked/ }).click();
    assert.ok(await page.locator(".card-foot").filter({ hasText: "LEARNED" }).count() > 0, "learned score shown on cards");
    assert.deepEqual(await page.evaluate(() => [localStorage.length, sessionStorage.length]), [0, 0]);
    // Phones: touch screen, 16px fields, 44px targets, no overflow at 375/390/401.
    const phone = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true });
    const mobile = await phone.newPage();
    await throttle(mobile);
    mobile.on("pageerror", (e) => errors.push(e.message));
    await mobile.route("https://fixtures.invalid/horn.svg", (r) => r.fulfill({ contentType: "image/svg+xml", body: fs.readFileSync(path.join(root, "assets/trumpets/horn.svg")) }));
    await mobile.goto(base + "/trumpets/#taste");
    await mobile.waitForSelector(".swipe-card");
    for (const width of [375, 390, 401]) {
      await mobile.setViewportSize({ width, height: 844 });
      for (const m of ["Swipe", "Head to head"]) {
        await mobile.getByRole("button", { name: m, exact: true }).click();
        await mobile.locator("#taste-note-box").evaluate((d) => (d.open = true));
        await mobile.locator("#taste-prefs-box").evaluate((d) => (d.open = true));
        assert.equal(await mobile.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `taste ${m} overflow at ${width}px`);
        const small = await mobile.locator("#taste-board textarea, #taste-board input").evaluateAll((n) => n.filter((x) => x.offsetParent && parseFloat(getComputedStyle(x).fontSize) < 16).length);
        assert.equal(small, 0, `taste fields under 16px at ${width}px`);
        const targets = await mobile.locator("#taste-board .react:not([hidden]), #taste-board .chip, .taste-modes button").evaluateAll((n) => n.filter((x) => x.offsetParent).map((x) => x.getBoundingClientRect().height));
        assert.ok(targets.every((h) => h >= 44), `small taste target at ${width}px: ${targets}`);
      }
    }
    await mobile.setViewportSize({ width: 390, height: 844 });
    await mobile.getByRole("button", { name: "Swipe", exact: true }).click();
    await mobile.locator("#taste-note-box").evaluate((d) => (d.open = false));
    await mobile.locator("#taste-prefs-box").evaluate((d) => (d.open = false));
    await Promise.all([
      mobile.waitForResponse((r) => r.url().includes("/taste/events") && r.status() < 300),
      mobile.getByRole("button", { name: "Cool (right arrow)" }).tap(),
    ]);
    await mobile.evaluate(() => scrollTo(0, document.querySelector(".taste-modes").getBoundingClientRect().top + scrollY - 8));
    await mobile.screenshot({ path: path.join(out, "taste-swipe-mobile.png") });
    await mobile.getByRole("button", { name: "Head to head", exact: true }).tap();
    await mobile.waitForSelector(".pair-card");
    await mobile.evaluate(() => scrollTo(0, document.querySelector(".taste-modes").getBoundingClientRect().top + scrollY - 8));
    await mobile.screenshot({ path: path.join(out, "taste-pair-mobile.png") });
    // Touch drags (the iPhone report): partial drags and page scrolls over the
    // card never commit; only a deliberate drag does, with a one-tap undo.
    await mobile.getByRole("button", { name: "Swipe", exact: true }).tap();
    await mobile.waitForSelector(".swipe-card");
    let tastePostCount = 0;
    mobile.on("request", (r) => r.method() === "POST" && r.url().includes("/taste/events") && tastePostCount++);
    const cdp = await phone.newCDPSession(mobile);
    const touch = (type, x, y) => cdp.send("Input.dispatchTouchEvent", { type, touchPoints: type === "touchEnd" ? [] : [{ x, y }] });
    const touchDrag = async (dx, steps = 8) => {
      const b = await mobile.locator(".swipe-card").boundingBox();
      const x = b.x + b.width / 2,
        y = b.y + 150;
      await touch("touchStart", x, y);
      // Slow, deliberate movement (no fling), then a pause before lifting.
      for (let i = 1; i <= steps; i++) {
        await touch("touchMove", x + (dx * i) / steps, y);
        await mobile.waitForTimeout(60);
      }
      await mobile.waitForTimeout(150);
      await touch("touchEnd");
    };
    await mobile.setViewportSize({ width: 430, height: 844 });
    await mobile.locator(".swipe-card").evaluate((c) => ((c.style.width = "390px"), (c.style.maxWidth = "none")));
    assert.equal(await mobile.locator(".swipe-card").evaluate((c) => c.offsetWidth), 390);
    await mobile.evaluate(() => scrollTo(0, document.querySelector(".swipe-card").getBoundingClientRect().top + scrollY - 60));
    const partialId = await mobile.locator(".swipe-card").getAttribute("data-id");
    await touchDrag(120);
    await mobile.waitForFunction(() => !document.querySelector(".swipe-card.dragging") && !document.querySelector(".swipe-card").style.transform);
    await mobile.waitForTimeout(400);
    assert.equal(await mobile.locator(".swipe-card").getAttribute("data-id"), partialId, "a 120 px partial drag on a 390 px card springs back");
    assert.equal(tastePostCount, 0, "a partial drag posts nothing");
    // A vertical scroll that starts on the card moves the page, not the card
    // (pan-y: the browser takes the touch and cancels the pointer).
    const scrolledFrom = await mobile.evaluate(() => scrollY);
    const b = await mobile.locator(".swipe-card").boundingBox();
    const sx = b.x + b.width / 2,
      sy = b.y + 300;
    await touch("touchStart", sx, sy);
    for (let i = 1; i <= 10; i++) {
      await touch("touchMove", sx + 3, sy - 32 * i);
      await mobile.waitForTimeout(16);
    }
    await touch("touchEnd");
    await mobile.waitForTimeout(500);
    const scrolledTo = await mobile.evaluate(() => scrollY);
    assert.ok(scrolledTo > scrolledFrom + 100, `the page scrolled (${scrolledFrom} -> ${scrolledTo})`);
    assert.equal(await mobile.locator(".swipe-card").getAttribute("data-id"), partialId, "scrolling over the card is not a must-see");
    // Even when a long upward touch drag reaches the card as pointer moves
    // (no cancel), touch never commits "must see".
    await mobile.evaluate(() => scrollTo(0, document.querySelector(".swipe-card").getBoundingClientRect().top + scrollY - 60));
    const c = await mobile.locator(".swipe-card").boundingBox();
    await cdp.send("Input.synthesizeScrollGesture", { x: Math.round(c.x + c.width / 2), y: Math.round(c.y + 320), yDistance: -300, gestureSourceType: "touch", speed: 800 });
    await mobile.waitForTimeout(500);
    assert.equal(await mobile.locator(".swipe-card").getAttribute("data-id"), partialId, "an upward touch drag is not a must-see");
    assert.equal(tastePostCount, 0, "a vertical scroll over the card posts nothing");
    // A deliberate drag commits; the toast undoes it in one tap.
    await mobile.evaluate(() => scrollTo(0, document.querySelector(".swipe-card").getBoundingClientRect().top + scrollY - 60));
    await Promise.all([mobile.waitForResponse((r) => r.url().includes("/taste/events") && r.status() < 300), touchDrag(200)]);
    await mobile.waitForFunction((id) => document.querySelector(".swipe-card")?.dataset.id !== id, partialId);
    assert.equal(tastePostCount, 1);
    assert.match(await mobile.locator("#taste-toast").innerText(), /Cool\./);
    // The server's queue after an undo lists the horn again, but not
    // necessarily first (horns found in one run share first_seen). Undo must
    // still bring back the card that was just swiped, and keep it in front
    // through later responses (a chip tap): here the undo response lists it
    // last and the chip responses leave it out.
    const lastInQueue = async (route) => {
      const kind = route.request().method() === "POST" && JSON.parse(route.request().postData() || "{}").kind;
      if (kind !== "undo" && kind !== "tag") return route.continue();
      const response = await route.fetch();
      const body = await response.json();
      const i = body.queue.findIndex((q) => q.listingId === partialId);
      if (kind === "undo") {
        assert.ok(i >= 0, "the undone horn is back in the server's queue");
        body.queue.push(...body.queue.splice(i, 1));
      } else if (i >= 0) body.queue.splice(i, 1);
      return route.fulfill({ response, json: body });
    };
    await mobile.route("**/taste/events", lastInQueue);
    // A board refresh in flight during the undo answers late, with the state
    // from before the undo: that stale reply must not undo the undo.
    let releaseStale;
    const staleGate = new Promise((r) => (releaseStale = r));
    const holdTaste = async (route) => {
      const response = await route.fetch();
      await staleGate;
      return route.fulfill({ response });
    };
    await mobile.route("**/trumpets/api/v1/trumpets/taste", holdTaste);
    const staleReply = mobile.waitForResponse((r) => r.url().endsWith("/v1/trumpets/taste") && r.request().method() === "GET");
    await mobile.evaluate(() => window.TasteBoard.setActive(true)); // reloads the taste state
    await Promise.all([mobile.waitForResponse((r) => r.url().includes("/taste/events") && r.status() < 300), mobile.locator("#taste-toast").getByRole("button", { name: "Undo" }).tap()]);
    await mobile.waitForFunction(() => document.querySelector("#taste-status").textContent === "Undone.");
    assert.equal(await mobile.locator(".swipe-card").getAttribute("data-id"), partialId, "undo brings back the card just swiped, wherever the server lists it");
    const afterUndo = await mobile.locator("#taste-confidence-text").innerText();
    releaseStale();
    await (await staleReply).finished();
    await mobile.evaluate(() => new Promise((r) => setTimeout(() => setTimeout(r, 0), 0)));
    assert.equal(await mobile.locator("#taste-confidence-text").innerText(), afterUndo, "a reply sent before the undo does not replace newer state");
    assert.equal(await mobile.locator(".swipe-card").getAttribute("data-id"), partialId);
    await mobile.unroute("**/trumpets/api/v1/trumpets/taste", holdTaste);
    const chip = mobile.locator("#taste-chips .chip").first();
    for (const pressed of ["true", "false"]) {
      await chip.evaluate((c) => (c.dataset.before = "1"));
      await Promise.all([mobile.waitForResponse((r) => r.url().includes("/taste/events") && r.status() < 300), chip.tap()]);
      // The response re-renders the chips (and the card).
      await mobile.waitForFunction(() => !document.querySelector("#taste-chips .chip")?.dataset.before);
      assert.equal(await mobile.locator(".swipe-card").getAttribute("data-id"), partialId, "the card an undo brought back stays in front after a chip tap");
      assert.equal(await chip.getAttribute("aria-pressed"), pressed);
    }
    // Once the horn leaves the board's eligible set (here: passed), the pin
    // lets go at the next reply instead of bringing it back.
    const feedback = (method, body) => mobile.evaluate(async ([id, method, body]) => (await fetch(`/trumpets/api/v1/trumpets/listings/${id}/feedback`, { method, headers: { "Content-Type": "application/json" }, body })).status, [partialId, method, body]);
    assert.equal(await feedback("PUT", JSON.stringify({ interestState: "pass" })), 200);
    await Promise.all([mobile.waitForResponse((r) => r.url().endsWith("/v1/trumpets/listings")), mobile.locator("#refresh").evaluate((b) => b.click())]);
    await mobile.waitForFunction((id) => window.TrumpetApp.listing(id)?.feedback.interestState === "pass", partialId);
    await chip.evaluate((c) => (c.dataset.before = "1"));
    await Promise.all([mobile.waitForResponse((r) => r.url().includes("/taste/events") && r.status() < 300), chip.tap()]);
    await mobile.waitForFunction(() => !document.querySelector("#taste-chips .chip")?.dataset.before);
    assert.notEqual(await mobile.locator(".swipe-card").getAttribute("data-id"), partialId, "a passed horn is no longer pinned in front");
    assert.equal(await feedback("DELETE"), 200);
    await Promise.all([mobile.waitForResponse((r) => r.url().endsWith("/v1/trumpets/listings")), mobile.locator("#refresh").evaluate((b) => b.click())]);
    await mobile.unroute("**/taste/events", lastInQueue);
    assert.equal(await mobile.locator("#taste-toast").count(), 0, "the toast closes on undo");
    // Review fixes: a reaction whose POST fails after it was already undone
    // must not remove another reaction from the undo stack, and must not hide
    // a newer reaction's toast.
    let failNextPost = 0,
      failGate = null; // when set, a failing POST waits for it instead of 900 ms
    await mobile.route("**/taste/events", async (route) => {
      if (route.request().method() === "POST" && failNextPost > 0) {
        failNextPost--;
        await (failGate || new Promise((r) => setTimeout(r, 900)));
        return route.fulfill({ status: 500, contentType: "application/json", body: '{"error":"fixture failure"}' });
      }
      return route.continue();
    });
    const tasteOk = () => mobile.waitForResponse((r) => r.url().includes("/taste/events") && r.status() < 300);
    const tasteFailed = () => mobile.waitForResponse((r) => r.url().includes("/taste/events") && r.status() === 500);
    const bId = await mobile.locator(".swipe-card").getAttribute("data-id");
    await Promise.all([tasteOk(), mobile.getByRole("button", { name: "Cool (right arrow)" }).tap()]); // B, kept
    failNextPost = 1;
    const failedA = tasteFailed();
    await mobile.getByRole("button", { name: "Not for me (left arrow)" }).tap(); // A, in flight
    await mobile.keyboard.press("z"); // undo A while its POST is in flight
    await failedA;
    await mobile.waitForTimeout(300);
    // The next undo takes back B (not an older reaction).
    await Promise.all([tasteOk(), mobile.keyboard.press("z")]);
    await mobile.waitForFunction((id) => document.querySelector(".swipe-card")?.dataset.id === id, bId, { timeout: 5000 });
    // A fails after a newer reaction C: C's toast stays.
    failNextPost = 1;
    const failedA2 = tasteFailed();
    await mobile.getByRole("button", { name: "Not for me (left arrow)" }).tap(); // A, in flight
    await Promise.all([tasteOk(), mobile.getByRole("button", { name: "Cool (right arrow)" }).tap()]); // C
    await failedA2;
    await mobile.waitForTimeout(200);
    assert.match(await mobile.locator("#taste-toast").innerText(), /Cool\./, "the newest reaction keeps its undo toast");
    // A reaction to a card an undo brought back fails only after a newer undo
    // pinned another card: the newer undo keeps the front.
    await Promise.all([tasteOk(), mobile.keyboard.press("z")]); // undo C: C pinned
    await mobile.waitForFunction(() => document.querySelector("#taste-status").textContent === "Undone.");
    const pinnedId = await mobile.locator(".swipe-card").getAttribute("data-id");
    failNextPost = 1;
    let releaseFailure;
    failGate = new Promise((r) => (releaseFailure = r)); // fails after the newer undo below
    let oldFailed = false;
    const failedOld = mobile.waitForResponse((r) => r.url().includes("/taste/events") && r.status() === 500, { timeout: 0 }).then(() => (oldFailed = true));
    failedOld.catch(() => {}); // awaited below; a failure elsewhere must surface as itself
    await mobile.getByRole("button", { name: "Not for me (left arrow)" }).tap(); // on the pinned card, in flight
    await mobile.waitForFunction((id) => document.querySelector(".swipe-card")?.dataset.id !== id, pinnedId);
    const newerId = await mobile.locator(".swipe-card").getAttribute("data-id");
    await Promise.all([tasteOk(), mobile.getByRole("button", { name: "Cool (right arrow)" }).tap()]); // D
    await Promise.all([tasteOk(), mobile.keyboard.press("z")]); // undo D: D pinned, newer
    await mobile.waitForFunction((id) => document.querySelector(".swipe-card")?.dataset.id === id, newerId);
    assert.equal(oldFailed, false, "fixture: the older reaction is still in flight after the newer undo");
    releaseFailure();
    failGate = null;
    await failedOld;
    await mobile.evaluate(() => new Promise((r) => setTimeout(() => setTimeout(r, 0), 0)));
    assert.equal(await mobile.locator(".swipe-card").getAttribute("data-id"), newerId, "a late failure of an older reaction does not take the front from a newer undo");
    await mobile.unroute("**/taste/events");
    await mobile.setViewportSize({ width: 390, height: 844 });
    // Other views stay overflow-free at these widths with the extra tab.
    await mobile.goto(base + "/trumpets/");
    await mobile.waitForFunction(() => document.querySelector("#all-count").textContent !== "0");
    for (const width of [375, 390, 401]) {
      await mobile.setViewportSize({ width, height: 844 });
      assert.equal(await mobile.evaluate(() => document.documentElement.scrollWidth > innerWidth), false, `board overflow at ${width}px`);
    }
    await phone.close();
    await page.setViewportSize({ width: 1440, height: 1100 });
    // An ended session answers API calls with 401 signin_required: the page
    // shows one calm banner that signs in and returns here (no Basic dialog).
    await page.route("**/trumpets/api/v1/trumpets/__signed_out_probe", (r) =>
      r.fulfill({ status: 401, contentType: "application/json", body: '{"error":"signin_required","login":"/auth/login"}' }),
    );
    const probe = await page.evaluate(async () => (await fetch("/trumpets/api/v1/trumpets/__signed_out_probe")).status);
    assert.equal(probe, 401);
    const signedOut = page.getByRole("status").filter({ hasText: "Signed out." });
    await signedOut.waitFor();
    assert.equal(
      await signedOut.getByRole("link", { name: "Sign in again" }).getAttribute("href"),
      "/auth/login?next=" + encodeURIComponent(await page.evaluate(() => location.pathname + location.search + location.hash)),
    );
    await page.screenshot({ path: path.join(out, "signed-out-banner.png") });
    await signedOut.getByRole("button", { name: "Dismiss" }).click();
    assert.equal(await signedOut.isHidden(), true);
    assert.deepEqual(thirdParty.filter((u) => !u.startsWith("https://www.youtube-nocookie.com/") && !u.startsWith("https://fixtures.invalid/")), []);
    assert.deepEqual(errors, []);
    // Jazz live effects: a synthetic tone runs through every FX preset in real
    // Web Audio while the dry and wet lossless recorders capture side by side.
    const fxPage = await browser.newPage();
    const fxErrors = [];
    fxPage.on("pageerror", (e) => fxErrors.push(e.message));
    // Any page one directory deep resolves the recorders' worklet paths.
    await fxPage.goto(base + "/jazz/fx-check");
    await fxPage.addScriptTag({ url: "/assets/jazz/lossless-recorder.js" });
    await fxPage.addScriptTag({ url: "/assets/jazz/fx-chain.js" });
    const fx = await fxPage.evaluate(async () => {
      const source = new AudioContext({ sampleRate: 48000 });
      const tone = source.createOscillator();
      const level = source.createGain();
      level.gain.value = 0.4;
      const input = source.createMediaStreamDestination();
      tone.connect(level).connect(input);
      tone.start();
      const results = {};
      for (const preset of Object.keys(JazzFX.presets)) {
        const chain = new JazzFX.FXChain();
        const wet = await chain.start(input.stream, preset, 0, false);
        const dryRecorder = new JazzLosslessRecorder();
        const wetRecorder = new JazzLosslessRecorder();
        await dryRecorder.start(input.stream);
        await wetRecorder.start(wet);
        await new Promise((resolve) => setTimeout(resolve, 350));
        const [dry, mix] = await Promise.all([dryRecorder.stop(), wetRecorder.stop()]);
        const monitor = chain.nodes.monitorGain.gain.value;
        await chain.stop();
        results[preset] = { dryPeak: Math.max(...dry.waveformPeaks), wetPeak: Math.max(...mix.waveformPeaks), wetRate: mix.sampleRate, monitor, closed: chain.context === null };
      }
      await source.close();
      return results;
    });
    Object.entries(fx).forEach(([preset, result]) => {
      assert.ok(result.dryPeak > 0.3 && result.dryPeak < 0.5, `${preset} dry master changed: ${result.dryPeak}`);
      assert.ok(result.wetPeak > 0.05 && result.wetPeak <= 1, `${preset} FX mix is silent or clipped: ${result.wetPeak}`);
      assert.equal(result.wetRate, 48000);
      assert.equal(result.monitor, 0, `${preset} monitored the wet mix without being asked`);
      assert.equal(result.closed, true);
    });
    assert.deepEqual(fxErrors, []);
    await fxPage.close();
    console.log(
      "Browser checks passed: persistence, filters, favorite, 320/390/768/1440px layout, mobile filters, 44px touch targets, notes-dialog overflow, horn inspiration capture/upload/filter/detail/embed/share route, taste training (swipe keyboard/drag/buttons, touch partial drags and page scrolls never commit, undo toast, undo brings the card back in front and keeps it there (stale replies, newer undos, passed horns), chips, text and voice notes, head to head (comparable listings only, shortage explained, connection line), photo-less cards link to the listing without reacting and reactions record hadPhoto, stated preferences, 375/390/401px phone checks), signed-out banner, Jazz live-effects dual capture for every preset, and runtime errors.",
    );
  } finally {
    if (browser) await browser.close();
    try {
      await fetch(base + "/__preview_stop", { method: "POST" });
    } catch {}
    await new Promise((resolve) => {
      if (child.exitCode !== null) return resolve();
      child.once("exit", resolve);
      setTimeout(() => {
        child.kill();
        resolve();
      }, 5000);
    });
  }
})().catch((e) => {
  console.error(e);
  console.error(logs);
  process.exitCode = 1;
});
