const { test } = require("node:test");
const assert = require("node:assert/strict");
const { indexedDB } = require("fake-indexeddb");
globalThis.indexedDB = indexedDB;
require("./study-one-store.js");
const S = globalThis.JazzStudyStore;
test("chunk commits survive reopening and unfinished capture recovers once", async () => {
  const a = { id: "recover", tempo: 80, sampleRate: 48000, finalized: false };
  await S.put(a);
  await S.chunk(a.id, 0, new Float32Array(48000 * 3));
  await S.chunk(a.id, 1, new Float32Array(48000));
  delete require.cache[require.resolve("./study-one-store.js")];
  require("./study-one-store.js");
  const reopened = globalThis.JazzStudyStore;
  assert.equal((await reopened.chunks(a.id)).length, 2);
  await reopened.recover();
  await reopened.recover();
  const recovered = (await reopened.all()).find((a) => a.id === "recover");
  assert.equal(recovered.durationMs, 1000);
  assert.equal(recovered.audioBytes, 44 + 48000 * 4 * 2);
  assert.equal(recovered.result.status, "interrupted");
  assert.equal(recovered.finalized, true);
  assert.equal((await reopened.all()).length, 1);
});
test("retries overwrite the same chunk and finalized result is immutable during recovery", async () => {
  const a = {
    id: "complete",
    tempo: 80,
    sampleRate: 48000,
    finalized: true,
    synced: true,
    result: { status: "pass" },
    durationMs: 12000,
  };
  await S.put(a);
  await S.chunk(a.id, 0, new Float32Array([1, 2]));
  await S.chunk(a.id, 0, new Float32Array([3, 4]));
  assert.deepEqual(Array.from((await S.chunks(a.id))[0]), [3, 4]);
  await S.recover();
  assert.deepEqual(
    (await S.all()).find((a) => a.id === "complete"),
    a,
  );
});
test("failed chunk write is rejected instead of reporting a successful save", async () => {
  await assert.rejects(S.chunk("bad", 0, () => {}));
  assert.equal((await S.chunks("bad")).length, 0);
});
