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
    for (let i = 0; i < 100; i++) {
      try {
        const r = await fetch(base + "/trumpets/");
        if (r.ok) break;
      } catch {}
      if (child.exitCode !== null) throw Error(logs);
      await new Promise((r) => setTimeout(r, 150));
    }
    browser = await chromium.launch({
      ...(process.env.CHROMIUM_PATH
        ? { executablePath: process.env.CHROMIUM_PATH }
        : {}),
      headless: true,
      args: ["--no-sandbox"],
    });
    const page = await browser.newPage({
      viewport: { width: 1440, height: 1450 },
    });
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
    assert.deepEqual(thirdParty.filter((u) => !u.startsWith("https://www.youtube-nocookie.com/") && !u.startsWith("https://fixtures.invalid/")), []);
    assert.deepEqual(errors, []);
    console.log(
      "Browser checks passed: persistence, filters, favorite, 320/390/768/1440px layout, mobile filters, 44px touch targets, notes-dialog overflow, horn inspiration capture/upload/filter/detail/embed/share route, and runtime errors.",
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
