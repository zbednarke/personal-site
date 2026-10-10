const test = require("node:test");
const assert = require("node:assert/strict");
const { canRenderCurrentFrame, clampSeekTime, durationSeconds, formatPlaybackTime } = require("./media-playback.js");

test("known recording duration drives a finite video timeline", () => {
  assert.equal(durationSeconds(171432), 171.432);
  assert.equal(formatPlaybackTime(171.432), "02:51");
  assert.equal(formatPlaybackTime(0), "00:00");
});

test("video seek positions stay inside the known recording duration", () => {
  assert.equal(clampSeekTime(60, 171.432), 60);
  assert.equal(clampSeekTime(-4, 171.432), 0);
  assert.equal(clampSeekTime(999, 171.432), 171.432);
  assert.equal(clampSeekTime(Infinity, 171.432), 0);
});

test("buffering clears only after the requested frame can render", () => {
  assert.equal(canRenderCurrentFrame({ seeking: true, readyState: 4 }), false);
  assert.equal(canRenderCurrentFrame({ seeking: false, readyState: 1 }), false);
  assert.equal(canRenderCurrentFrame({ seeking: false, readyState: 2 }), true);
});
