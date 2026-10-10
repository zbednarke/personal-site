const test = require("node:test"),
  assert = require("node:assert/strict"),
  path = require("node:path"),
  fs = require("node:fs"),
  M = require("./inspiration-model");

// Shared with jazz-api/inspiration_test.go so Go and the browser agree.
const fixtures = JSON.parse(
  fs.readFileSync(path.join(__dirname, "../../jazz-api/testdata/inspiration_urls.json"), "utf8"),
);

test("classifyURL matches the Go canonicalizer fixture list", () => {
  for (const f of fixtures) {
    const got = M.classifyURL(f.input);
    if (f.error) assert.equal(got, null, f.input);
    else
      assert.deepEqual(got, { canonical: f.canonical, provider: f.provider, mediaId: f.mediaId, format: f.format }, f.input);
  }
});

test("seed variants share one YouTube identity", () => {
  for (const url of ["https://youtube.com/shorts/qHetQ-t4Wi0?is=abc", "https://youtu.be/qHetQ-t4Wi0?si=x", "https://m.youtube.com/shorts/qHetQ-t4Wi0"])
    assert.equal(M.youtubeID(url), "qHetQ-t4Wi0");
  assert.equal(M.youtubeID("https://example.com/watch?v=qHetQ-t4Wi0"), "");
});

test("parseCaptureInput handles links, share text, schemeless links and plain text", () => {
  assert.deepEqual(M.parseCaptureInput("  https://youtube.com/shorts/qHetQ-t4Wi0  "), {
    url: "https://youtube.com/shorts/qHetQ-t4Wi0",
    why: "",
  });
  assert.deepEqual(
    M.parseCaptureInput("Check out this horn! https://www.instagram.com/reel/C9xYz12AbCd/?igsh=abc so good."),
    { url: "https://www.instagram.com/reel/C9xYz12AbCd/?igsh=abc", why: "Check out this horn! so good." },
  );
  assert.equal(M.parseCaptureInput("youtube.com/shorts/qHetQ-t4Wi0").url, "https://youtube.com/shorts/qHetQ-t4Wi0");
  assert.equal(M.parseCaptureInput("www.harrelsontrumpets.com").url, "https://www.harrelsontrumpets.com");
  assert.equal(M.parseCaptureInput("(see https://reverb.com/item/123-muse).").url, "https://reverb.com/item/123-muse");
  assert.deepEqual(M.parseCaptureInput("just some words"), { error: "Paste a link or an image" });
  assert.deepEqual(M.parseCaptureInput("   "), { empty: true });
  assert.ok(M.parseCaptureInput("https://a.test/x https://b.test/y").error);
  assert.ok(M.parseCaptureInput("javascript:alert(1)").error);
  assert.equal(M.parseCaptureInput("x ".repeat(3000) + "https://a.test").why.length, 4000);
});

test("normalizeTags lowercases, trims, dedupes and bounds", () => {
  assert.deepEqual(M.normalizeTags([" Raw  Brass", "raw brass", "", "MBS Technology"]), ["raw brass", "mbs technology"]);
  assert.equal(M.normalizeTags(Array.from({ length: 30 }, (_, i) => "t" + i)).length, 20);
  assert.deepEqual(M.normalizeTags(["x".repeat(41)]), []);
});

const item = (id, extra = {}) => ({
  id,
  kind: "link",
  provider: "youtube",
  mediaFormat: "short",
  title: "",
  maker: "",
  model: "",
  why: "",
  tags: [],
  priority: "someday",
  priceSeen: null,
  priceCurrency: "USD",
  pinned: false,
  createdAt: "2026-10-01T00:00:00Z",
  ...extra,
});

test("filterInspirations combines search, maker, AND tags, priorities, source and price", () => {
  const rows = [
    item("short", { title: "1st Harrelson Muse", maker: "Harrelson", tags: ["mbs technology", "raw brass"], priority: "someday" }),
    item("reverb", { provider: "reverb", mediaFormat: "page", maker: "Taylor", tags: ["raw brass"], priority: "hunting", priceSeen: 4200 }),
    item("photo", { provider: "upload", kind: "image", why: "Love this engraving", tags: ["engraving"], priority: "want" }),
    item("reel", { provider: "instagram", mediaFormat: "reel", authorName: "Rumors & Dreams", priority: "inspiration" }),
  ];
  const ids = (f) => M.filterInspirations(rows, f).map((x) => x.id);
  assert.deepEqual(ids({ query: "muse" }), ["short"]);
  assert.deepEqual(ids({ query: "ENGRAVING" }), ["photo"]);
  assert.deepEqual(ids({ query: "rumors" }), ["reel"]);
  assert.deepEqual(ids({ maker: "harrelson" }), ["short"]);
  assert.deepEqual(ids({ tags: ["raw brass"] }), ["short", "reverb"]);
  assert.deepEqual(ids({ tags: ["raw brass", "mbs technology"] }), ["short"]);
  assert.deepEqual(ids({ priorities: ["want", "hunting"] }), ["reverb", "photo"]);
  assert.deepEqual(ids({ source: "retailer" }), ["reverb"]);
  assert.deepEqual(ids({ source: "photo" }), ["photo"]);
  assert.deepEqual(ids({ hasPrice: true }), ["reverb"]);
  assert.deepEqual(ids({ tags: ["raw brass"], priorities: ["hunting"], hasPrice: true, source: "retailer" }), ["reverb"]);
});

test("sortInspirations keeps pins first and groups prices by currency without FX", () => {
  const rows = [
    item("old-want", { priority: "want", createdAt: "2026-01-01T00:00:00Z", maker: "Taylor" }),
    item("new-someday", { createdAt: "2026-10-05T00:00:00Z", maker: "adams" }),
    item("hunting", { priority: "hunting", createdAt: "2026-05-01T00:00:00Z", priceSeen: 5000, priceCurrency: "USD" }),
    item("eur", { createdAt: "2026-04-01T00:00:00Z", priceSeen: 100, priceCurrency: "EUR", maker: "Van Laar" }),
    item("usd-cheap", { createdAt: "2026-03-01T00:00:00Z", priceSeen: 3000, priceCurrency: "USD", maker: "Harrelson" }),
    item("pinned", { pinned: true, createdAt: "2025-01-01T00:00:00Z", priority: "inspiration" }),
  ];
  const ids = (s) => M.sortInspirations(rows, s).map((x) => x.id);
  assert.deepEqual(ids("newest"), ["pinned", "new-someday", "hunting", "eur", "usd-cheap", "old-want"]);
  assert.deepEqual(ids("priority"), ["pinned", "hunting", "old-want", "new-someday", "eur", "usd-cheap"]);
  assert.deepEqual(ids("maker"), ["pinned", "new-someday", "usd-cheap", "old-want", "eur", "hunting"]);
  assert.deepEqual(ids("price"), ["pinned", "eur", "usd-cheap", "hunting", "new-someday", "old-want"]);
  assert.deepEqual(rows[0].id, "old-want", "sorting does not mutate");
});

test("resizePlan caps the long edge at 2048, never upscales and passes GIFs through", () => {
  assert.deepEqual(M.resizePlan(4032, 3024, "image/jpeg", 3e6), { mode: "jpeg", type: "image/jpeg", quality: 0.85, width: 2048, height: 1536 });
  assert.deepEqual(M.resizePlan(1170, 2532, "image/png", 2e6), { mode: "jpeg", type: "image/jpeg", quality: 0.85, width: 946, height: 2048 });
  assert.deepEqual(M.resizePlan(800, 600, "image/webp", 1e5), { mode: "jpeg", type: "image/jpeg", quality: 0.85, width: 800, height: 600 });
  assert.deepEqual(M.resizePlan(500, 500, "image/gif", 9e6), { mode: "original" });
  assert.equal(M.resizePlan(500, 500, "image/gif", 11e6).mode, "reject");
  assert.deepEqual(M.fallbackPlan("image/png", 1e6), { mode: "original" });
  assert.equal(M.fallbackPlan("image/heic", 1e6).reason, "This image type isn't supported. Try a screenshot.");
  assert.equal(M.fallbackPlan("image/png", 11 * 1024 * 1024).mode, "reject");
});

test("hornSuggestions matches maker and model prefix or contains (Harrelson / MUSE)", () => {
  const horns = [
    { id: "muse", maker: "Harrelson", model: "MUSE" },
    { id: "bravura", maker: "Harrelson", model: "Bravura David Castro" },
    { id: "taylor", maker: "Taylor", model: "Chicago 46 II / Harrelson-modified" },
  ];
  assert.deepEqual(M.hornSuggestions({ maker: "Harrelson", model: "MUSE" }, horns).map((h) => h.id), ["muse"]);
  assert.deepEqual(M.hornSuggestions({ maker: "harrelson", model: "mu" }, horns).map((h) => h.id), ["muse"]);
  assert.deepEqual(M.hornSuggestions({ maker: "Harrelson", model: "" }, horns).map((h) => h.id), ["muse", "bravura"]);
  assert.deepEqual(M.hornSuggestions({ maker: "", model: "MUSE" }, horns), []);
});

test("labels, titles, prices and embeds", () => {
  assert.equal(M.priorityLabel("hunting"), "Actively hunting");
  assert.equal(M.priorityLabel("inspiration"), "Just inspiration");
  assert.equal(M.badgeLabel(item("a")), "▶ Short");
  assert.equal(M.badgeLabel(item("b", { provider: "instagram", mediaFormat: "reel" })), "Reel");
  assert.equal(M.badgeLabel(item("c", { provider: "reverb" })), "Reverb");
  assert.equal(M.badgeLabel(item("d", { provider: "upload" })), "Photo");
  assert.equal(M.cardTitle(item("e", { maker: "Harrelson", model: "MUSE" })), "Harrelson MUSE");
  assert.equal(M.cardTitle(item("f", { provider: "web", sourceUrl: "https://www.dealer.test/x" })), "dealer.test");
  assert.equal(M.formatPriceSeen({ priceSeen: 4200, priceCurrency: "USD", priceSeenOn: "2026-03-14" }), "$4,200 · Mar 2026");
  assert.equal(M.formatPriceSeen({ priceSeen: null }), "");
  assert.deepEqual(M.embedFor(item("g", { providerMediaId: "qHetQ-t4Wi0" })), {
    kind: "youtube",
    src: "https://www.youtube-nocookie.com/embed/qHetQ-t4Wi0?autoplay=1&playsinline=1",
    aspect: "9:16",
  });
  assert.equal(M.embedFor(item("h", { providerMediaId: '"><script>' })), null);
  assert.equal(M.embedFor(item("i", { provider: "instagram", mediaFormat: "reel", providerMediaId: "C9xYz12AbCd" })).src, "https://www.instagram.com/reel/C9xYz12AbCd/embed");
  assert.equal(M.embedFor(item("j", { provider: "reverb" })), null);
});
