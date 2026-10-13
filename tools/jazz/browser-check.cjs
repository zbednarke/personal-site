/* Jazz smoke check: every /jazz/ view opens at 390 px (touch, through the
 * phone menu drawer) and 1440 px (sidebar), with no sideways scroll and no
 * runtime errors. Runs against the isolated Go/Postgres preview that the
 * Workbench check uses (real API routes and migrations, fictional data), and
 * stands in for the site VM's film service (/jazz/highlight-api) and the
 * published film index with fictional fixtures, so the restored Library,
 * Training, note-map film and film-editor entry points render for real. */
const assert = require("node:assert/strict");
const { spawn } = require("node:child_process");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
// Let context.route() answer requests the service worker makes, too.
process.env.PW_EXPERIMENTAL_SERVICE_WORKER_NETWORK_EVENTS = "1";
const { chromium } = require("playwright");

const root = path.resolve(__dirname, "../..");
const out = process.env.JAZZ_SCREENSHOT_DIR || fs.mkdtempSync(path.join(os.tmpdir(), "jazz-smoke-"));
fs.mkdirSync(out, { recursive: true });
const addr = process.env.JAZZ_PREVIEW_ADDR || "127.0.0.1:4176";
const base = `http://${addr}`;
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
      const r = await fetch(`${base}/jazz/api/health`);
      if (r.ok) return;
    } catch { /* starting */ }
    await new Promise((r) => setTimeout(r, 250));
  }
  throw new Error("preview did not start");
}

// Every view in the nav, with the section it shows and one thing that proves it rendered.
const VIEWS = [
  { view: "today", section: "#today", ready: "#today-title" },
  { view: "repertoire", section: "#repertoire", ready: "#repertoire h1, #repertoire h2" },
  { view: "training", section: "#training", ready: "#study-start" },
  { view: "guide-tones", section: "#guide-tones", ready: "#guide-tone-title" },
  { view: "effects", section: "#effects", ready: "#effects-frame" },
  { view: "archive", section: "#archive", ready: "#archive-title" },
  { view: "library", section: "#library", ready: "#library-books .library-book" },
  { view: "studio", section: "#studio", ready: "#studio-make-server-film" },
];
const LABELS = { today: "TODAY", repertoire: "REPERTOIRE", training: "TRAINING", "guide-tones": "GUIDE TONES", effects: "LIVE EFFECTS", archive: "ARCHIVE", library: "LIBRARY", studio: "CLIP STUDIO" };

// Fictional stand-ins for the site VM's film service.
const today = new Date().toISOString().slice(0, 10);
const LIBRARY = [{ id: "00000000-0000-4000-8000-000000000001", title: "Example Etudes", author: "Example Author", sizeBytes: 2048, addedAt: `${today}T09:00:00Z`, resources: [{ title: "Example Recording", artist: "Example Artist", url: "https://example.com/listen" }] }];
const FILMS = [{ id: `${today}-example`, date: today, title: "Example practice film", description: "Fictional fixture.", duration: "0:42", video: `/jazz/films/${today}-example.mp4`, poster: `/jazz/films/${today}-example.jpg`, noteMap: `/jazz/films/${today}-example.notes.html` }];

(async () => {
  let browser;
  const errors = [];
  try {
    await waitForServer();
    const exe = process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {};
    browser = await chromium.launch({ ...exe, headless: true, args: ["--no-sandbox", "--use-fake-ui-for-media-stream", "--use-fake-device-for-media-stream"] });
    const newPage = async (name, width, height, touch) => {
      const ctx = await browser.newContext({ viewport: { width, height }, deviceScaleFactor: 1, hasTouch: touch, isMobile: touch, reducedMotion: "reduce", permissions: ["microphone", "camera"] });
      await ctx.route("**/jazz/highlight-api/library", (r) => r.fulfill({ json: LIBRARY }));
      await ctx.route("**/jazz/highlight-api/jobs", (r) => r.fulfill({ json: [] }));
      await ctx.route("**/jazz/films/index.json", (r) => r.fulfill({ json: FILMS }));
      const page = await ctx.newPage();
      page.on("pageerror", (e) => errors.push(`${name}: ${e.message}`));
      page.on("console", (m) => { if (m.type() === "error" && !/Failed to load resource|404|502|503/.test(m.text())) errors.push(`${name} console: ${m.text()}`); });
      page.ctx = ctx;
      return page;
    };
    const noSideScroll = async (page, label) => {
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - innerWidth);
      assert.ok(overflow <= 0, `${label}: no sideways scroll (${overflow}px)`);
    };
    const shows = async (page, { view, section, ready }, label) => {
      await page.waitForFunction((v) => document.querySelector(`.virtuoso-nav [data-jazz-view="${v}"]`)?.classList.contains("active"), view);
      for (const other of VIEWS) {
        const hidden = await page.locator(other.section).evaluate((n) => n.hidden || getComputedStyle(n).display === "none");
        assert.equal(hidden, other.view !== view, `${label}: ${other.section} is ${other.view === view ? "shown" : "hidden"}`);
      }
      await page.waitForSelector(ready, { state: "attached" });
      await page.waitForTimeout(250);
      await noSideScroll(page, label);
    };

    // Desktop: the sidebar reaches every view.
    {
      const page = await newPage("desktop", 1440, 900, false);
      await page.goto(`${base}/jazz/`);
      for (const v of VIEWS) {
        await page.click(`.virtuoso-nav [data-jazz-view="${v.view}"]`);
        await shows(page, v, `1440px #${v.view}`);
        await page.screenshot({ path: path.join(out, `desktop-${v.view}.png`) });
      }
      // Restored entry points beyond the views themselves.
      await page.click('.virtuoso-nav [data-jazz-view="studio"]');
      await page.locator("#studio-date").evaluate((input, date) => { input.value = date; input.dispatchEvent(new Event("change", { bubbles: true })); }, today);
      await page.waitForSelector(".finished-film button:text('Edit this film')", { state: "attached" });
      assert.ok(await page.locator(".finished-film a:text('Explore note map')").count(), "studio: the film shelf links its note map");
      assert.ok(await page.locator("#studio-make-film").count(), "studio: the cloud Magic Film button is still there");
      await page.click('.virtuoso-nav [data-jazz-view="library"]');
      assert.equal(await page.locator("#library-books h2").first().innerText(), "Example Etudes");
      await page.goto(`${base}/jazz/plan/`);
      await page.waitForLoadState("load");
      await noSideScroll(page, "1440px /jazz/plan/");
      await page.ctx.close();
    }

    // The Workbench service worker (scope /jazz/) keeps an offline copy of
    // the app shell only. Private media and data must never land in its
    // cache: note maps and films, the film service (jobs, edits, previews,
    // Library PDFs) and every API call, including training audio.
    {
      const page = await newPage("service-worker", 1440, 900, false);
      const ok = (body, contentType) => (r) => r.fulfill({ status: 200, contentType, body });
      await page.ctx.route("**/jazz/films/*.notes.html", ok("<!doctype html><title>Note map</title><p>fixture</p>", "text/html"));
      await page.ctx.route("**/jazz/films/*.mp4", ok("not really a film", "video/mp4"));
      await page.ctx.route("**/jazz/highlight-api/library/*/file*", ok("%PDF-1.4 fixture", "application/pdf"));
      await page.ctx.route("**/jazz/highlight-api/jobs/*/preview.mp4", ok("preview", "video/mp4"));
      await page.ctx.route("**/jazz/highlight-api/edits/*", ok("{}", "application/json"));
      await page.ctx.route("**/jazz/api/v1/training/attempts/*/audio", ok("RIFF fixture", "audio/wav"));
      await page.goto(`${base}/jazz/`);
      await page.waitForFunction(() => navigator.serviceWorker?.controller, null, { timeout: 20000 });
      const film = FILMS[0];
      const privateURLs = [
        film.noteMap,
        film.video,
        `/jazz/highlight-api/library/${LIBRARY[0].id}/file`,
        "/jazz/highlight-api/jobs/00000000-0000-4000-8000-000000000002/preview.mp4",
        "/jazz/highlight-api/edits/00000000-0000-4000-8000-000000000003",
        "/jazz/highlight-api/jobs",
        "/jazz/api/v1/training/attempts/00000000-0000-4000-8000-000000000004/audio",
      ];
      // Both ways the page reaches them: a navigation (open in a new tab) and a fetch.
      for (const url of privateURLs) {
        await page.goto(base + url).catch(() => {});
        await page.goto(`${base}/jazz/#studio`);
        await page.evaluate((u) => fetch(u, { credentials: "same-origin" }).then((r) => r.arrayBuffer()).catch(() => {}), url);
      }
      await page.waitForTimeout(500);
      const cached = await page.evaluate(async () => {
        const paths = [];
        for (const name of await caches.keys()) for (const req of await (await caches.open(name)).keys()) paths.push(new URL(req.url).pathname);
        return paths;
      });
      // (The preview serves pages with no-store, so the shell itself may not be
      // kept here; this checks only that nothing private ever is.)
      const leaked = cached.filter((p) => /\/(highlight-api|api|films)\//.test(p));
      assert.deepEqual(leaked, [], "service worker: no private media, film-service or API responses cached");
      await page.ctx.close();
    }

    // Phone: the top bar's menu button opens the drawer, and every view is reachable from it.
    {
      const page = await newPage("phone", 390, 844, true);
      await page.goto(`${base}/jazz/`);
      for (const v of VIEWS) {
        await page.tap("#jazz-menu-button");
        await page.waitForSelector("body.jazz-nav-open .virtuoso-nav");
        await page.tap(`.virtuoso-nav [data-jazz-view="${v.view}"]`);
        await page.waitForFunction(() => !document.body.classList.contains("jazz-nav-open"));
        await shows(page, v, `390px #${v.view}`);
        assert.ok(await page.locator("#jazz-menu-button").isVisible(), `390px #${v.view}: the menu button is visible`);
        assert.equal((await page.locator("#mobile-view-label").innerText()).toUpperCase(), LABELS[v.view], `390px #${v.view}: the top bar names the view`);
        await page.screenshot({ path: path.join(out, `phone-${v.view}.png`) });
      }
      await page.goto(`${base}/jazz/plan/`);
      await page.waitForLoadState("load");
      await noSideScroll(page, "390px /jazz/plan/");
      await page.ctx.close();
    }

    assert.deepEqual(errors, [], "no runtime errors");
    console.log(`Jazz smoke check passed: ${VIEWS.map((v) => v.view).join(", ")} and /jazz/plan/ at 390 px (drawer) and 1440 px (sidebar), no sideways scroll, film shelf note map and editor entry, service worker caches no private media or API responses, Library fixture rendered, no runtime errors. Screenshots: ${out}`);
  } catch (error) {
    console.error(error);
    if (errors.length) console.error(errors.join("\n"));
    console.error(logs.slice(-4000));
    process.exitCode = 1;
  } finally {
    if (browser) await browser.close();
    try { await fetch(`${base}/__preview_stop`, { method: "POST" }); } catch { /* already stopped */ }
    child.kill();
  }
})();
