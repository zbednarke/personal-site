// Visual check of the real Jazz markup/CSS and Magic Film controller, with
// fictional API data. No production service or private recording is accessed.
const { chromium } = require("playwright");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const os = require("node:os");
const root = path.resolve(__dirname, "../..");
const out = process.env.JAZZ_SCREENSHOT_DIR || fs.mkdtempSync(path.join(os.tmpdir(), "film-errors-"));
fs.mkdirSync(out, { recursive: true });
const job = {
  id: "00000000-0000-4000-8000-000000000001", status: "failed",
  failure: { code: "provider_quota", title: "AI credits exhausted", message: "The film editor's API account has no available quota or credits. Retrying now will not help.", action: "Restore the film editor's AI access before trying again. Your ChatGPT subscription and this API account are billed separately.", retryable: false },
};

(async () => {
  const browser = await chromium.launch({ headless: true, args: ["--no-sandbox"], ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) });
  try {
    for (const width of [390, 1440]) {
      const context = await browser.newContext({ viewport: { width, height: 1000 }, isMobile: width === 390, hasTouch: width === 390 });
      await context.route("**/*", async route => {
        const url = new URL(route.request().url());
        if (url.origin !== "https://fixture.test") return route.abort();
        if (url.pathname.startsWith("/jazz/api/")) return route.fulfill({ json: { jobs: [job] } });
        if (route.request().resourceType() === "script" && !url.pathname.endsWith("/highlight-local.js")) return route.fulfill({ contentType: "application/javascript", body: "" });
        const filename = path.resolve(root, "." + url.pathname, url.pathname.endsWith("/") ? "index.html" : "");
        if (!filename.startsWith(root + path.sep) || !fs.existsSync(filename)) return route.fulfill({ status: 404, body: "" });
        const type = filename.endsWith(".css") ? "text/css" : filename.endsWith(".js") ? "application/javascript" : filename.endsWith(".html") ? "text/html" : "application/octet-stream";
        return route.fulfill({ contentType: type, body: fs.readFileSync(filename) });
      });
      const page = await context.newPage();
      const errors = [];
      page.on("pageerror", error => errors.push(error.message));
      await page.goto("https://fixture.test/jazz/#studio");
      await page.evaluate(() => {
        document.body.className = "virtuoso-studio";
        for (const section of document.querySelectorAll("main > section")) section.hidden = section.id !== "studio";
        document.querySelector("#studio").hidden = false;
        document.querySelector("#studio-date").value = "2026-10-26";
        document.dispatchEvent(new Event("jazz:studio-date-change"));
      });
      const panel = page.locator("#studio-magic-status[data-error]");
      await panel.waitFor({ state: "visible" });
      assert.match(await panel.innerText(), /AI credits exhausted/);
      assert.match(await panel.innerText(), /Retrying now will not help/);
      const bounds = await panel.boundingBox();
      assert.ok(bounds.x >= 0 && bounds.x + bounds.width <= width, `${width}: panel fits horizontally`);
      assert.ok(await panel.evaluate(node => node.scrollWidth <= node.clientWidth), `${width}: no panel overflow`);
      assert.equal(await panel.locator("p").first().evaluate(node => getComputedStyle(node).color), "rgb(245, 233, 214)", "error guidance keeps readable contrast in the output dock");
      assert.ok(await panel.locator("button").evaluate(node => node.getBoundingClientRect().height >= 44), "refresh is touch-sized");
      await panel.scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(out, `magic-film-error-${width}.png`) });
      assert.deepEqual(errors, []);
      await context.close();
    }
    console.log(`Magic Film error UI verified at 390 and 1440 px. Screenshots: ${out}`);
  } finally { await browser.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
