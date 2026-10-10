const test = require("node:test");
const assert = require("node:assert/strict");

require("./recording-policy.js");

const { MAX_TAKE_DURATION_MS, VIDEO_DATA_FLUSH_MS, preferredVideoType, shouldAutoFinish, supportsChunkFlush } = globalThis.JazzRecordingPolicy;

test("the automatic take ceiling is exactly four hours", () => {
  assert.equal(MAX_TAKE_DURATION_MS, 14_400_000);
  assert.equal(shouldAutoFinish(MAX_TAKE_DURATION_MS - 1), false);
  assert.equal(shouldAutoFinish(MAX_TAKE_DURATION_MS), true);
  assert.equal(shouldAutoFinish(MAX_TAKE_DURATION_MS + 1), true);
});

test("the FX mix stops at one hour while the dry master continues", () => {
  const { FX_MAX_DURATION_MS, shouldFinishFx } = globalThis.JazzRecordingPolicy;
  assert.equal(FX_MAX_DURATION_MS, 3_600_000);
  assert.ok(FX_MAX_DURATION_MS < MAX_TAKE_DURATION_MS);
  assert.equal(shouldFinishFx(FX_MAX_DURATION_MS - 1), false);
  assert.equal(shouldFinishFx(FX_MAX_DURATION_MS), true);
});

test("video recording prefers a chunk-safe MP4 when supported", () => {
  const supported = new Set(["video/mp4", "video/webm;codecs=vp9,opus"]);
  assert.equal(preferredVideoType((type) => supported.has(type)), "video/mp4");
  assert.equal(VIDEO_DATA_FLUSH_MS, 30_000);
  assert.equal(supportsChunkFlush("video/mp4"), true);
  assert.equal(supportsChunkFlush("video/webm;codecs=vp9,opus"), false);
});

test("video recording falls back to WebM without choosing an unsupported type", () => {
  assert.equal(preferredVideoType((type) => type === "video/webm;codecs=vp8,opus"), "video/webm;codecs=vp8,opus");
  assert.equal(preferredVideoType(() => false), "");
});
