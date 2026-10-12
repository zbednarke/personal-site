const test = require("node:test"),
  assert = require("node:assert/strict"),
  F = require("./fixlist");

// All sample data is fictional.

const response = {
  live: false,
  threadId: "t-fix",
  digest: {
    version: 1, day: "2026-10-12", generatedAt: "2026-10-12T13:52:00Z", verdict: "4 to fix · next: 5xx on POST /v1/sample (jazz-api)", allClear: false, toFix: 4,
    fixNext: { key: "api:5xx:jazz-api:POST /v1/sample", section: "api", label: "5xx on POST /v1/sample (jazz-api), of 40 requests", count: 3, previous: 1, score: 99 },
    run: { expectedDay: "2026-10-12", found: true, status: "partial", startedAt: "2026-10-12T13:17:04Z", notes: ["Reverb API search failed", "Recheck failed, recorded for retry: shop.example / timed out"] },
    items: [
      { key: "run:partial", section: "run", label: "Trumpet runs that were partial", count: 1, previous: 0, score: 50 },
      { key: "checks:shop.example:timed out", section: "checks", label: "shop.example: timed out", count: 2, previous: 5, domains: ["shop.example"], score: 40 },
      { key: "checks:old.example:HTTP 403", section: "checks", label: "old.example: HTTP 403", count: 0, previous: 2, score: 0 },
      { key: "checks:leads", section: "checks", label: "Lead checks that failed", count: 9, previous: 9, info: true },
      { key: "api:5xx:jazz-api:POST /v1/sample", section: "api", label: "5xx on POST /v1/sample (jazz-api), of 40 requests", count: 3, previous: 1, score: 99 },
      { key: "films:failed:render", section: "films", label: "Magic Film failed: render stopped", count: 1, previous: 1, score: 70 },
    ],
  },
  history: [{ day: "2026-10-12", verdict: "4 to fix", allClear: false }, { day: "2026-10-11", verdict: "All clear: nothing failed.", allClear: true }],
};

test("change reads new, up, down, same and fixed against the previous 24 hours", () => {
  assert.deepEqual(F.change(3, 0), { trend: "new", text: "new" });
  assert.deepEqual(F.change(5, 2), { trend: "up", text: "+3" });
  assert.deepEqual(F.change(2, 5), { trend: "down", text: "−3" });
  assert.deepEqual(F.change(4, 4), { trend: "same", text: "same" });
  assert.deepEqual(F.change(0, 2), { trend: "fixed", text: "fixed (was 2)" });
});

test("the page model has the verdict, Fix next, the run line and sections in a fixed order", () => {
  const m = F.fixListModel(response);
  assert.equal(m.title, "Fix list – 2026-10-12");
  assert.equal(m.allClear, false);
  assert.equal(m.fixNext.label, "5xx on POST /v1/sample (jazz-api), of 40 requests");
  assert.equal(m.fixNext.change, "+2");
  assert.equal(m.run.line, "Daily run 2026-10-12: partial, started 2026-10-12 13:17 UTC.");
  assert.equal(m.run.notes.length, 2);
  assert.deepEqual(m.sections.map((s) => s.id), ["run", "api", "checks", "films"]);
  const checks = m.sections.find((s) => s.id === "checks");
  // Open items first, then fixed ones, then context; the badge counts only open items.
  assert.deepEqual(checks.items.map((i) => i.trend), ["down", "fixed", "same"]);
  assert.equal(checks.items[2].info, true);
  assert.equal(checks.open, 2);
  assert.equal(m.threadId, "t-fix");
  assert.equal(m.history[1].allClear, true);
});

test("a missing run and an all-clear day read plainly; a live view is marked", () => {
  const missing = F.fixListModel({ live: true, digest: { day: "2026-10-12", run: { expectedDay: "2026-10-12", missing: true }, items: [], verdict: "1 to fix · next: daily run missing" } });
  assert.equal(missing.run.line, "No daily run for 2026-10-12 (expected at 13:17 UTC).");
  assert.equal(missing.live, true);
  assert.deepEqual(missing.sections.map((s) => s.id), ["run"]);
  const clear = F.fixListModel({ digest: { day: "2026-10-13", allClear: true, verdict: "All clear: nothing failed.", run: { expectedDay: "2026-10-13", found: true, status: "succeeded", startedAt: "2026-10-13T13:17:00Z" }, items: [] } });
  assert.equal(clear.allClear, true);
  assert.equal(clear.fixNext, null);
  assert.equal(clear.sections[0].open, 0);
});

test("an empty or malformed response still gives a usable model", () => {
  const m = F.fixListModel(null);
  assert.equal(m.title, "Fix list");
  assert.deepEqual(m.sections.map((s) => s.id), ["run"]);
  const odd = F.fixListModel({ digest: { items: [{ section: "api", count: "2", previous: null, label: 7 }] } });
  assert.equal(odd.sections[1].items[0].count, 2);
  assert.equal(odd.sections[1].items[0].label, "7");
});
