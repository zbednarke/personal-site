const test = require("node:test");
const assert = require("node:assert/strict");

require("./audio-analysis.js");

const { detectPitch, describePitch, stablePitch } = globalThis.JazzAudioAnalysis;

function sineWave(frequency, sampleRate = 48000, length = 4096, amplitude = 0.7) {
  return Float32Array.from({ length }, (_, index) => amplitude * Math.sin((2 * Math.PI * frequency * index) / sampleRate));
}

test("detectPitch finds a concert A", () => {
  const result = detectPitch(sineWave(440), 48000);
  assert.ok(result);
  assert.ok(Math.abs(result.frequency - 440) < 1);
  assert.ok(result.clarity > 0.9);
});

test("detectPitch ignores silence", () => {
  assert.equal(detectPitch(new Float32Array(4096), 48000), null);
});

test("detectPitch keeps the fundamental of a harmonic-rich tone", () => {
  const frequency = 233.0819;
  const samples = Float32Array.from({ length: 4096 }, (_, index) => {
    const phase = (2 * Math.PI * frequency * index) / 48000;
    return (0.58 * Math.sin(phase)) + (0.26 * Math.sin(phase * 2)) + (0.12 * Math.sin(phase * 3));
  });
  const result = detectPitch(samples, 48000);
  assert.ok(result);
  assert.ok(Math.abs(result.frequency - frequency) < 1);
});

test("describePitch returns nearest note and cents", () => {
  const result = describePitch(466.1637615);
  assert.equal(result.note, "B♭");
  assert.equal(result.octave, 4);
  assert.ok(Math.abs(result.cents) < 0.01);
});

test("stablePitch requires a held note", () => {
  assert.equal(stablePitch([{ frequency: 440 }, { frequency: 450 }, { frequency: 430 }]), null);
  const result = stablePitch([{ frequency: 439.8 }, { frequency: 440.2 }, { frequency: 440.1 }]);
  assert.equal(result.note, "A");
  assert.equal(result.octave, 4);
});
