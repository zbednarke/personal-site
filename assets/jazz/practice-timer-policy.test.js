const test = require("node:test");
const assert = require("node:assert/strict");

require("./practice-timer-policy.js");

const { snapshot, restoreCancelled } = globalThis.JazzPracticeTimerPolicy;

test("cancel restores practice time from before the discarded take", () => {
  const timer = { elapsedMs: 120000, running: false, startedAt: 0, completed: false, completedAt: "" };
  const saved = snapshot(timer, "warmup");
  Object.assign(timer, { elapsedMs: 480000, running: true, startedAt: Date.now(), completed: true, completedAt: "later" });

  assert.equal(restoreCancelled(timer, saved, "warmup"), true);
  assert.deepEqual(timer, { elapsedMs: 120000, running: false, startedAt: 0, completed: false, completedAt: "" });
});

test("cancel snapshot cannot be applied to a different section", () => {
  const timer = { elapsedMs: 480000, running: true, startedAt: 100, completed: false, completedAt: "" };
  const saved = snapshot({ elapsedMs: 120000 }, "warmup");
  assert.equal(restoreCancelled(timer, saved, "scales"), false);
  assert.equal(timer.elapsedMs, 480000);
});
