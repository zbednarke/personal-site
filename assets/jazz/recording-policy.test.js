const test = require("node:test");
const assert = require("node:assert/strict");

require("./recording-policy.js");

const { MAX_TAKE_DURATION_MS, shouldAutoFinish } = globalThis.JazzRecordingPolicy;

test("the automatic take ceiling is exactly four hours", () => {
  assert.equal(MAX_TAKE_DURATION_MS, 14_400_000);
  assert.equal(shouldAutoFinish(MAX_TAKE_DURATION_MS - 1), false);
  assert.equal(shouldAutoFinish(MAX_TAKE_DURATION_MS), true);
  assert.equal(shouldAutoFinish(MAX_TAKE_DURATION_MS + 1), true);
});
