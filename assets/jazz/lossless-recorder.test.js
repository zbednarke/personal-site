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
  recorder.chunkPeaks = [0.2];
  recorder.frameCount = 2;

  await recorder.cancel();

  assert.equal(closed, true);
  assert.deepEqual(disconnected, ["source", "node", "gain"]);
  assert.equal(recorder.context, null);
  assert.equal(recorder.source, null);
  assert.equal(recorder.node, null);
  assert.equal(recorder.silentGain, null);
  assert.deepEqual(recorder.chunks, []);
  assert.deepEqual(recorder.chunkPeaks, []);
  assert.equal(recorder.frameCount, 0);
});

test("waveform peaks are compact, normalized, and bounded", () => {
  const peaks = Array.from({ length: 1200 }, (_, index) => index % 3 === 0 ? 1.2 : index / 1200);
  const compact = globalThis.JazzLosslessRecorder.compactPeaks(peaks, 120);
  assert.equal(compact.length, 120);
  assert.ok(compact.every((peak) => peak >= 0 && peak <= 1));
  assert.equal(Math.max(...compact), 1);
});
