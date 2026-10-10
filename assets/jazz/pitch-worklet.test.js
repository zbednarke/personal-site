const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

function engine() {
  let Processor;
  vm.runInNewContext(fs.readFileSync(require.resolve("./pitch-worklet.js"), "utf8"), {
    sampleRate: 48000,
    AudioWorkletProcessor: class { constructor() { this.port = { postMessage() {} }; } },
    registerProcessor: (_, processor) => { Processor = processor; },
  });
  return new Processor();
}

test("settings messages only change known, valid settings", () => {
  const processor = engine();
  const buffer = processor.rb;
  processor.port.onmessage({ data: { mode: "harmony", keyRoot: 5, scale: [0, 2, 3, 5, 7, 8, 10], voicing: "triad", strength: 4, glideMs: 0, harmMix: 0.5, rb: null, w: -1 } });
  assert.equal(processor.mode, "harmony");
  assert.equal(processor.keyRoot, 5);
  assert.deepEqual(Array.from(processor.scale), [0, 2, 3, 5, 7, 8, 10]);
  assert.equal(processor.voicing, "triad");
  assert.equal(processor.strength, 1);
  assert.equal(processor.glideMs, 1);
  assert.equal(processor.harmMix, 0.5);
  assert.equal(processor.rb, buffer, "internal buffers cannot be replaced by a message");
  assert.equal(processor.w, 0);
  processor.port.onmessage({ data: { mode: "bogus", keyRoot: 12, scale: [13], voicing: "x" } });
  assert.equal(processor.mode, "harmony");
  assert.equal(processor.keyRoot, 5);
  assert.equal(processor.scale.length, 7);
  processor.port.onmessage({ data: null });
});

test("off mode passes the input through untouched", () => {
  const processor = engine();
  const input = new Float32Array(128).map((_, index) => Math.sin(index / 5) * 0.5);
  const output = new Float32Array(128);
  assert.equal(processor.process([[input]], [[output]]), true);
  assert.deepEqual(Array.from(output), Array.from(input));
});
