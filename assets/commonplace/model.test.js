const test = require("node:test"),
  assert = require("node:assert/strict"),
  M = require("./model");

// All sample data is fictional.

test("localParts reads wall-clock time in the Moment's own zone", () => {
  const p = M.localParts("2026-03-15T06:41:00Z", "America/Los_Angeles");
  assert.deepEqual([p.year, p.month, p.day, p.hour, p.minute, p.weekday], [2026, 3, 14, 23, 41, 6]);
  assert.equal(M.hourText(p), "11:41 pm");
  assert.equal(M.formatDate(p), "Saturday 14 March 2026");
  assert.equal(M.formatDate(p, { short: true, noYear: true }), "Sat 14 Mar");
  const q = M.localParts("2026-03-15T06:41:00Z", "Asia/Tokyo");
  assert.equal(M.hourText(q), "3:41 pm");
  assert.equal(M.localParts("nonsense", "UTC"), null);
});

test("the hour lights the page; dreams are always night", () => {
  assert.equal(M.lightFor(3.2), "night");
  assert.equal(M.lightFor(4.92), "predawn");
  assert.equal(M.lightFor(7.1), "dawn");
  assert.equal(M.lightFor(10), "morning");
  assert.equal(M.lightFor(13), "day");
  assert.equal(M.lightFor(17.5), "golden");
  assert.equal(M.lightFor(20), "dusk");
  assert.equal(M.lightFor(23.7), "lamplit");
  assert.equal(M.lightFor(13, "dream"), "dream");
  assert.ok(M.ALWAYS_DARK.has("night") && M.ALWAYS_DARK.has("dream") && !M.ALWAYS_DARK.has("day"));
});

test("dial puts the hand at the hour (midnight bottom, noon top)", () => {
  const noon = M.dialSVG(12), midnight = M.dialSVG(0);
  assert.match(noon, /<line x1="22" y1="22" x2="22.00" y2="9.00"/);
  assert.match(midnight, /<line x1="22" y1="22" x2="22.00" y2="35.00"/);
});

test("durations and relative days", () => {
  assert.equal(M.duration(23 * 60000), "23 min");
  assert.equal(M.duration((6 * 60 + 29) * 60000), "6 h 29 min");
  assert.equal(M.duration(3 * 86400000), "3 days");
  const now = new Date("2026-03-16T12:00:00Z");
  assert.equal(M.relative("2026-03-16T08:00:00Z", "UTC", now), "today");
  assert.equal(M.relative("2026-03-15T08:00:00Z", "UTC", now), "yesterday");
  assert.equal(M.relative("2024-03-15T08:00:00Z", "UTC", now), "2 years ago");
});

test("wall label: span, phase and how long the reply took", () => {
  const moment = { kind: "conversation", occurredAt: "2026-03-15T06:41:00Z", endedAt: "2026-03-15T07:20:00Z", timezone: "America/Los_Angeles" };
  const lines = [
    { speaker: "p1", at: "2026-03-15T06:41:00Z" },
    { speaker: "me", at: "2026-03-15T13:10:00Z" },
  ];
  const label = M.wallLabel(moment, lines, new Date("2026-03-16T00:00:00Z"));
  assert.equal(label.phase, "lamplit");
  assert.deepEqual(label.hour, { hm: "11:41", ap: "pm" });
  assert.equal(label.span, "11:41 pm to Sun 15 Mar, 12:20 am · 39 min");
  assert.equal(label.after, "You replied at 6:10 am, 6 h 29 min later, by before dawn");
  assert.equal(label.relative, "yesterday");
  const captured = M.wallLabel({ kind: "dream", createdAt: "2026-03-15T10:00:00Z", timezone: "UTC" });
  assert.equal(captured.captured, true);
  assert.equal(captured.phase, "dream");
});

test("capture parsing and the share-sheet route", () => {
  assert.deepEqual(M.parseCapture("  look https://example.com/a?b=1). nice "), { url: "https://example.com/a?b=1", text: "look ). nice" });
  assert.deepEqual(M.parseCapture("example.com/page"), { url: "https://example.com/page", text: "" });
  assert.deepEqual(M.parseCapture("just words"), { url: "", text: "just words" });
  assert.deepEqual(M.parseShareHash("#add?url=https%3A%2F%2Fexample.com%2Fx&text=Sample%20note&title=T"), { url: "https://example.com/x", text: "Sample note", title: "T" });
  assert.equal(M.parseShareHash("#import"), null);
  assert.deepEqual(M.route("#m/abc?view=text"), { name: "moment", id: "abc", q: { view: "text" } });
  assert.equal(M.route("#space?focus=1").name, "space");
  assert.equal(M.route("").name, "index");
});

test("margin notes are pushed down to avoid collisions, in anchor order", () => {
  const tops = M.layoutNotes([{ anchorTop: 100, height: 50 }, { anchorTop: 20, height: 40 }, { anchorTop: 110, height: 30 }], 10);
  assert.deepEqual(tops, [100, 20, 160]);
});

test("thread geometry spaces knots in time and marks long silences", () => {
  const g = M.threadGeometry([
    { at: "2026-01-01T10:00:00Z" },
    { at: "2026-01-01T10:30:00Z" },
    { at: "2026-01-02T05:00:00Z" },
    { at: "2026-01-02T05:20:00Z" },
  ]);
  assert.equal(g.x[0], 4);
  assert.equal(Math.round(g.x[3]), 96);
  assert.ok(g.x[1] < g.x[2] && g.x[2] < g.x[3]);
  assert.equal(g.gaps.length, 1);
  assert.equal(g.gaps[0].label, "18 h 30 min");
  assert.deepEqual(M.threadGeometry([{ at: "2026-01-01T00:00:00Z" }]).x, [50]);
  const same = M.threadGeometry([{ at: "2026-01-01T00:00:00Z" }, { at: "2026-01-01T00:00:00Z" }, {}]);
  assert.ok(same.x.every((x) => x >= 4 && x <= 96));
});

test("snippets split on the server's private-use markers only", () => {
  assert.deepEqual(M.splitSnippet("a lighthouse <b>"), [
    { text: "a ", mark: false },
    { text: "light", mark: true },
    { text: "house <b>", mark: false },
  ]);
});

test("line meta, people and import helpers", () => {
  const meta = M.metaBits({ edited: true, replies: 1, reactions: [{ emoji: "❤️", count: 2 }, "👍"], replyTo: "l2" });
  assert.deepEqual(meta, { bits: ["Edited", "1 Reply"], reactions: ["❤️ 2", "👍"], replyTo: "l2", replyToName: "" });
  assert.equal(M.initials("Sample Friend"), "SF");
  assert.equal(M.hue("x"), M.hue("x"));
  assert.equal(M.withLabel([{ name: "Sample Friend", role: "sender" }, { name: "Example Cousin", role: "mentioned" }]), "Sample Friend and you");
  assert.equal(M.isSpecial({ people: [{ special: true }] }), true);
  const batches = M.batchFiles([{ size: 10 }, { size: 10 }, { size: 15 }, { size: 40 }], 30, 5);
  assert.deepEqual(batches.map((b) => b.map((f) => f.size)), [[10, 10], [15], [40]]);
  assert.equal(M.pickExportFile(["exp/dreams.json_Files/a.json", "exp/dreams.json", "exp/.hidden.json"]), "exp/dreams.json");
  assert.equal(M.suggestKind("dreams"), "dream");
  assert.match(M.newId(), /^[0-9a-f-]{36}$/);
  assert.equal(M.excerpt("one two three four five", 12), "one two…");
  assert.equal(M.pencilCount([{ state: "pencil" }, { state: "ink" }, { state: "erased" }]), 1);
});

test("wall-clock input converts through the Moment's zone, across DST", () => {
  assert.equal(M.zonedToISO("2026-03-14T23:41", "America/Los_Angeles"), "2026-03-15T06:41:00.000Z");
  assert.equal(M.zonedToISO("2026-07-01T12:00", "America/Los_Angeles"), "2026-07-01T19:00:00.000Z");
  assert.equal(M.zonedToISO("2026-07-01T12:00", "UTC"), "2026-07-01T12:00:00.000Z");
  assert.equal(M.isoToLocalInput("2026-03-15T06:41:00Z", "America/Los_Angeles"), "2026-03-14T23:41");
  assert.equal(M.zonedToISO("bad", "UTC"), "");
});
