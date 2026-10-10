const test = require("node:test");
const assert = require("node:assert/strict");

require("./data.js");

test("articulation and flexibility are permanent separate practice sections", () => {
  const sessions = globalThis.JAZZ_DATA.sessions;
  const articulation = sessions.find((session) => session.id === "articulation");
  const flexibility = sessions.find((session) => session.id === "flexibility");

  assert.ok(articulation);
  assert.ok(flexibility);
  assert.equal(articulation.minutes, 10);
  assert.equal(flexibility.minutes, 10);
  assert.equal(sessions.some((session) => session.id === "articulation-flexibility"), false);
  assert.equal(sessions.indexOf(flexibility), sessions.indexOf(articulation) + 1);
});


test("scheduled transcription is included only on its day without changing the daily curriculum", () => {
  const data = globalThis.JAZZ_DATA;
  const originalIDs = data.sessions.map(s => s.id);
  for (const date of ["2026-09-09", "2026-09-11"]) {
    assert.deepEqual(data.sessionsForDate(date).map(s => s.id), originalIDs);
  }
  const tomorrow = data.sessionsForDate("2026-09-10");
  const index = tomorrow.findIndex(s => s.id === "easy-to-love-transcription-2026-09-10");
  assert.equal(tomorrow[index - 1].id, "horn-down-listening");
  assert.equal(tomorrow[index].minutes, 20);
  assert.equal(tomorrow[index].title, "Prelude to a Kiss: transcribe and play");
  assert.equal(tomorrow[index].sourceURL, undefined);
  assert.deepEqual(data.sessions.map(s => s.id), originalIDs);
  assert.equal(new Set(tomorrow.map(s => s.id)).size, tomorrow.length);
});
