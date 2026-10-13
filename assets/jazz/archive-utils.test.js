const test = require("node:test");
const assert = require("node:assert/strict");
const utils = require("./archive-utils.js");

test("month grid starts on Monday and always contains six weeks", () => {
  const grid = utils.monthGrid(new Date(2026, 7, 1));
  assert.equal(grid.length, 42);
  assert.equal(grid[0].key, "2026-07-27");
  assert.equal(grid[41].key, "2026-09-06");
});

test("recording title is always derived from persisted context", () => {
  assert.equal(utils.recordingTitle({ practiceBlockTitle: "Scales", practiceSessionTitle: "Practice - Aug 9", tuneId: "blue-bossa" }), "Scales");
  assert.equal(utils.recordingTitle({ practiceSessionTitle: "Practice - Aug 9", tuneId: "blue-bossa" }), "Practice - Aug 9");
  assert.equal(utils.recordingTitle({ tuneId: "blue-bossa" }), "Uncategorized practice");
});

test("long playback durations retain hours", () => {
  assert.equal(utils.formatPlaybackTime(27 * 60 + 6), "27:06");
  assert.equal(utils.formatPlaybackTime(4 * 3600), "4:00:00");
});

test("day note maps use the first valid published film for that date", () => {
  const films = [
    { date: "2026-09-28", noteMap: "https://evil.test/map.html" },
    { date: "2026-09-29", noteMap: "/jazz/films/other.notes.html" },
    { id: "canonical", date: "2026-09-28", noteMap: "/jazz/films/day-one.notes.html" },
    { id: "older", date: "2026-09-28", noteMap: "/jazz/films/day-two.notes.html" },
  ];
  assert.equal(utils.noteMapForDate(films, "2026-09-28").id, "canonical");
  assert.equal(utils.noteMapForDate(films, "2026-09-30"), null);
  assert.equal(utils.noteMapForDate(films, "not-a-date"), null);
});
