const test = require("node:test");
const assert = require("node:assert/strict");

require("./upload-queue.js");

const UploadQueue = globalThis.JazzUploadQueue;
const tick = () => new Promise((resolve) => setImmediate(resolve));

test("uploads one completed take at a time without blocking enqueue", async () => {
  let releaseFirst;
  const firstUpload = new Promise((resolve) => { releaseFirst = resolve; });
  const started = [];
  const states = [];
  const queue = new UploadQueue(async (payload) => {
    started.push(payload.name);
    if (payload.name === "first") await firstUpload;
  }, (job) => states.push(`${job.id}:${job.status}`));

  const firstID = queue.enqueue({ name: "first" });
  const secondID = queue.enqueue({ name: "second" });
  assert.equal(queue.hasPending(), true);
  await tick();

  assert.deepEqual(started, ["first"]);
  assert.ok(states.includes(`${secondID}:queued`));

  releaseFirst();
  await tick();
  await tick();

  assert.deepEqual(started, ["first", "second"]);
  assert.ok(states.includes(`${firstID}:complete`));
  assert.ok(states.includes(`${secondID}:complete`));
  assert.equal(queue.hasPending(), false);
});

test("failed uploads remain retryable while later jobs continue", async () => {
  let attempts = 0;
  const completed = [];
  const queue = new UploadQueue(async (payload) => {
    if (payload.name === "retry" && attempts++ === 0) throw new Error("offline");
    completed.push(payload.name);
  }, (job) => {
    if (job.status === "complete") completed.push(`${job.payload.name}:complete`);
  });

  const failedID = queue.enqueue({ name: "retry" });
  queue.enqueue({ name: "later" });
  await tick();
  await tick();

  assert.ok(completed.includes("later"));
  assert.equal(queue.hasPending(), true);
  assert.equal(queue.retry(failedID), true);
  await tick();
  await tick();

  assert.ok(completed.includes("retry"));
  assert.equal(queue.hasPending(), false);
  assert.equal(queue.retry(failedID), false);
});
