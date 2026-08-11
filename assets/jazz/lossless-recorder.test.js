const test = require("node:test");
const assert = require("node:assert/strict");

require("./lossless-recorder.js");

test("cancel releases the audio graph and discards captured samples", async () => {
  const recorder = new globalThis.JazzLosslessRecorder();
  const disconnected = [];
  let closed = false;
  recorder.context = { sampleRate: 48000, close: async () => { closed = true; } };
  recorder.source = { disconnect: () => disconnected.push("source") };
  recorder.node = { disconnect: () => disconnected.push("node"), port: { onmessage: () => {} } };
  recorder.silentGain = { disconnect: () => disconnected.push("gain") };
  recorder.chunks = [new Float32Array([0.1, 0.2])];
  recorder.frameCount = 2;

  await recorder.cancel();

  assert.equal(closed, true);
  assert.deepEqual(disconnected, ["source", "node", "gain"]);
  assert.equal(recorder.context, null);
  assert.equal(recorder.source, null);
  assert.equal(recorder.node, null);
  assert.equal(recorder.silentGain, null);
  assert.deepEqual(recorder.chunks, []);
  assert.equal(recorder.frameCount, 0);
});
