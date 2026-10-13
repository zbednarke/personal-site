const test = require("node:test");
const assert = require("node:assert/strict");
const { frameAt, reading } = require("./long-tone-model.js");

test("source-time lookup reproduces earlier state after backward seek without interpolation", () => {
  const frames = [{ tMs: 20, state: "holding", holdMs: 20 }, { tMs: 40, state: "uncertain", holdMs: 20 }, { tMs: 30000, state: "completed", holdMs: 30000 }];
  assert.equal(frameAt(frames, 30000), frames[2]);
  assert.equal(frameAt(frames, 21), frames[0]);
  assert.equal(frameAt(frames, 40), frames[1]);
  assert.equal(frameAt(frames, 100), null);
  assert.equal(frameAt(frames, 19), null);
  assert.equal(frameAt([], 0), null);
});

test("duration completion is independent from cents centering", () => {
  const result = reading({ state: "completed", hz: 440 * 2 ** (30 / 1200), noteMidi: 69, holdMs: 30000 });
  assert.equal(result.complete, true);
  assert.ok(Math.abs(result.cents - 30) < 1e-8);
  assert.equal(reading({ state: "completed", hz: 440, noteMidi: 69, holdMs: 29980 }).complete, false);
});

test("written transposition changes labels including octave but preserves cents and duration", () => {
  const frame = { state: "holding", hz: 466.1637615, noteMidi: 70, holdMs: 12340 };
  const concert = reading(frame), written = reading(frame, true);
  assert.equal(concert.note, "B♭4");
  assert.equal(written.note, "C5");
  assert.equal(written.cents, concert.cents);
  assert.equal(written.holdMs, concert.holdMs);
});

test("unknown pitch hides tuner and cannot complete; listening clears previous hold", () => {
  const unknown = reading({ state: "uncertain", hz: 880, noteMidi: 69, holdMs: 31000 });
  assert.equal(unknown.note, "—");
  assert.equal(unknown.cents, null);
  assert.equal(unknown.complete, false);
  assert.equal(reading({ state: "listening", holdMs: 9999 }).holdMs, 0);
  assert.equal(reading(null).complete, false);
});
