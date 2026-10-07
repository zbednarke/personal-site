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
    await page.getByRole("button", { name: /All tracked/ }).click();
    assert.equal(await page.locator(".card").count(), 24);
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
    assert.deepEqual(errors, []);
    console.log(
      "Browser checks passed: persistence, filters, favorite, 320/390/768/1440px layout, mobile filters, 44px touch targets, notes-dialog overflow and runtime errors.",
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
