const test = require("node:test");
const assert = require("node:assert/strict");
const model = require("./film-clips-model.js");

const project = {
  version: 1,
  date: "2026-01-02",
  title: "Example film",
  clips: [
    { id: "a", candidateId: "c1", recordingId: "rec-2", startMs: 61500, endMs: 75000, title: "Second phrase" },
    { id: "b", candidateId: "c2", recordingId: "rec-1", startMs: 0, endMs: 9000, title: "" },
    { id: "c", candidateId: "c3", recordingId: "gone", startMs: 3000, endMs: 4000, title: "Removed take" },
  ],
};

test("rows keep film order with source and time range", () => {
  const sources = { "rec-1": { section: "Long tones", take: "Take 1" }, "rec-2": { section: "Ballad", take: "Take 2" } };
  const rows = model.filmClipRows(project, (id) => sources[id] || null);
  assert.deepEqual(rows.map((row) => row.order), [1, 2, 3]);
  assert.deepEqual(rows.map((row) => row.recordingId), ["rec-2", "rec-1", "gone"]);
  assert.equal(rows[0].title, "Second phrase");
  assert.equal(rows[0].source, "Ballad · Take 2");
  assert.equal(rows[0].range, "1:01–1:15");
  assert.equal(rows[0].startMs, 61500);
  assert.equal(rows[1].title, "Clip 2");
  assert.equal(rows[1].range, "0:00–0:09");
});

test("takes no longer on the day are listed but not playable", () => {
  const rows = model.filmClipRows(project, (id) => (id === "gone" ? null : { section: "S", take: "Take 1" }));
  assert.equal(rows[2].playable, false);
  assert.equal(rows[2].source, "");
  assert.equal(rows[0].playable, true);
});

test("films without a clip list give no rows", () => {
  assert.deepEqual(model.filmClipRows(null), []);
  assert.deepEqual(model.filmClipRows({ clips: null }), []);
  assert.deepEqual(model.filmClipRows({ version: 1 }), []);
});

test("malformed clips are skipped", () => {
  const rows = model.filmClipRows({ clips: [
    null,
    { recordingId: "", startMs: 0, endMs: 1000 },
    { recordingId: "r", startMs: 5000, endMs: 5000 },
    { recordingId: "r", startMs: "x", endMs: 1000 },
    { recordingId: "r", startMs: 1000, endMs: 2000, title: "Kept" },
  ] });
  assert.equal(rows.length, 1);
  assert.equal(rows[0].title, "Kept");
  assert.equal(rows[0].order, 1);
  assert.equal(rows[0].playable, false);
});
