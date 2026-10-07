const test = require("node:test"),
  assert = require("node:assert/strict"),
  M = require("./model");
const listing = (id, extra = {}) => ({
  id,
  maker: "Taylor",
  model: "Chicago II",
  status: "active",
  searchScore: 80,
  firstSeen: "2026-10-03",
  currency: "USD",
  price: 3000,
  details: { finish: "raw brass" },
  images: [],
  tags: ["upswept"],
  feedback: { rating: 4, interestState: "watch", favorite: false },
  ...extra,
});
test("Today uses persisted events and excludes acquired horns", () => {
  const b = M.shape({
    listings: [
      listing("one"),
      listing("owned", { acquired: true }),
      listing("unchanged"),
    ],
    events: [
      { listingId: "one", kind: "price drop" },
      { listingId: "owned", kind: "new listing" },
    ],
  });
  assert.deepEqual(
    M.select(b, { view: "today" }).map((l) => l.id),
    ["one"],
  );
  assert.equal(M.select(b, { view: "all" }).length, 3);
});
test("filter combinations preserve historical offers and private ranking", () => {
  const b = M.shape({
    listings: [
      listing("one", {
        feedback: { favorite: true, rating: 5, interestState: "buy" },
      }),
      listing("two", { status: "sold", maker: "Lawler" }),
      listing("three", { status: "removed" }),
    ],
  });
  assert.equal(M.select(b, { view: "all", active: true }).length, 1);
  assert.equal(
    M.select(b, { favorite: true, interest: "buy", query: "raw brass" }).length,
    1,
  );
  assert.equal(M.select(b, { status: "sold", maker: "Lawler" }).length, 1);
});
test("sort handles missing prices/checks and currency grouping", () => {
  const b = M.shape({
    listings: [
      listing("one", { price: 500, lastChecked: "2026-10-03" }),
      listing("two", { price: null, lastChecked: null }),
      listing("three", { price: 400, currency: "EUR" }),
    ],
  });
  assert.deepEqual(
    M.select(b, { sort: "unchecked" }).map((l) => l.id),
    ["two", "three", "one"],
  );
  assert.deepEqual(
    M.select(b, { sort: "price" }).map((l) => l.id),
    ["three", "one", "two"],
  );
});
test("drops use same-currency evidence; no unknown amount treated as zero", () => {
  const l = listing("one", {
    statusHistory: [
      { kind: "price drop", oldPrice: 4000, newPrice: 3000, currency: "USD" },
      { kind: "price drop", oldPrice: 20000, newPrice: 500, currency: "JPY" },
      { kind: "price drop", oldPrice: 4000, newPrice: null, currency: "USD" },
    ],
  });
  assert.equal(M.priceDrop(l), 1000);
  assert.equal(M.biggestDrop(l), 0.975);
  assert.equal(M.money(null), "Price unverified");
});
test("untrusted image/link protocols rejected and default fields shaped", () => {
  assert.equal(M.safeURL("javascript:alert(1)"), "");
  assert.equal(M.safeURL("https://user:pass@example.com"), "");
  const b = M.shape({
    listings: [
      listing("one", {
        images: ["https://example.com/horn.jpg", "javascript:bad"],
        feedback: undefined,
        details: undefined,
        tags: undefined,
      }),
    ],
  });
  assert.equal(b.listings[0].images.length, 1);
  assert.equal(b.listings[0].feedback.notes, "");
});
test("badges derive from observations and real features", () => {
  const l = listing("one");
  assert.deepEqual(
    M.badges(l, [
      { listingId: "one", kind: "price drop" },
      { listingId: "other", kind: "new listing" },
    ]),
    ["PRICE DROP", "RAW", "WEIRD"],
  );
});
test("latest successful search/recheck chooses combined or dedicated run by timestamp", () => {
  const b = {
    lastSuccess: {
      search: "2026-10-01",
      combined: "2026-10-03",
      recheck: "2026-10-04",
    },
  };
  assert.equal(M.lastSuccessFor(b, "search"), "2026-10-03");
  assert.equal(M.lastSuccessFor(b, "recheck"), "2026-10-04");
  assert.equal(M.lastSuccessFor({}, "search"), null);
});

test("unverified leads stay outside Today and tracked market records", () => {
  const b = M.shape({
    listings: [
      listing("verified"),
      listing("hint", {
        verificationState: "candidate",
        status: "stale",
        price: null,
      }),
      listing("passed", {
        verificationState: "candidate",
        feedback: { interestState: "pass" },
      }),
      listing("owned", { verificationState: "candidate", acquired: true }),
    ],
    events: [{ listingId: "hint", kind: "newly discovered" }],
  });
  assert.deepEqual(
    M.select(b, { view: "candidates" }).map((l) => l.id),
    ["hint"],
  );
  assert.deepEqual(
    M.select(b, { view: "all" }).map((l) => l.id),
    ["verified"],
  );
  assert.equal(M.select(b, { view: "today" }).length, 0);
  assert.ok(M.badges(b.listings[1], []).includes("UNVERIFIED"));
});
