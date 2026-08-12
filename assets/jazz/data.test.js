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
