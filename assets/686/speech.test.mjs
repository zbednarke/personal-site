import test from "node:test";
import assert from "node:assert/strict";
import { PCMPlayer, readEvents, sentenceBuffer } from "./speech.mjs";

const encode = (s) => new TextEncoder().encode(s);

test("sentences are emitted before the response finishes; chunk and decimal boundaries survive", () => {
  const spoken = [];
  const feed = sentenceBuffer((s) => spoken.push(s));
  feed("Voltage is 3.");
  feed("3 volts. Sec");
  assert.deepEqual(spoken, ["Voltage is 3.3 volts."]);
  feed("ond sentence!");
  assert.equal(spoken.length, 1);
  feed("", true);
  assert.deepEqual(spoken, ["Voltage is 3.3 volts.", "Second sentence!"]);
  feed("😀".repeat(400), true);
  assert.ok(spoken.every((s) => encode(s).length <= 1200));
  assert.equal(spoken.slice(2).join(""), "😀".repeat(400));
});

test("NDJSON tolerates split UTF-8 and reports truncated or failed streams", async () => {
  const bytes = encode('{"text":"Reclaimer 😀"}\n{"done":true}\n');
  const stream = new ReadableStream({ start(c) {
    for (const b of bytes) c.enqueue(new Uint8Array([b]));
    c.close();
  } });
  const text = [];
  await readEvents(new Response(stream), (s) => text.push(s));
  assert.deepEqual(text, ["Reclaimer 😀"]);
  await assert.rejects(readEvents(new Response('{"text":"Hello"}\n'), () => {}), /interrupted/);
  await assert.rejects(readEvents(new Response('{"error":"provider failed"}\n'), () => {}), /provider failed/);
});

function audioFixture(t) {
  const frames = [];
  t.mock.method(globalThis, "requestAnimationFrame", (fn) => { frames.push(fn); return frames.length; });
  t.mock.method(globalThis, "cancelAnimationFrame", () => {});
  const sources = [];
  const context = {
    currentTime: 0, state: "running", destination: {},
    createAnalyser: () => ({ fftSize: 256, frequencyBinCount: 128, connect() {}, disconnect() {}, getByteFrequencyData(a) { a.fill(0); } }),
    createBuffer(channels, length, rate) {
      const data = new Float32Array(length);
      return { data, duration: length / rate, getChannelData: () => data };
    },
    createBufferSource() {
      const source = { connect() {}, disconnect() {}, stop() { this.stopped = true; }, start(at) { this.at = at; } };
      sources.push(source);
      return source;
    },
  };
  return { context, sources, frames };
}
// Node has no animation clock; install properties for test.mock to replace.
globalThis.requestAnimationFrame = () => {};
globalThis.cancelAnimationFrame = () => {};

test("PCM begins before EOF, preserves odd network splits, and measures scheduled playback", async (t) => {
  const { context, sources, frames } = audioFixture(t);
  let starts = 0, ends = 0;
  const player = new PCMPlayer(context, { onStart: () => starts++, onEnd: () => ends++ });
  let controller;
  const stream = new ReadableStream({ start(c) { controller = c; } });
  const reading = player.append(new Response(stream), new AbortController().signal);
  const pcm = new Uint8Array(4802);
  pcm.set([0, 128, 255, 127]);
  controller.enqueue(pcm.slice(0, 1));
  controller.enqueue(pcm.slice(1, 4800));
  await new Promise((r) => setImmediate(r));
  assert.equal(sources.length, 1, "must schedule without waiting for EOF");
  assert.equal(sources[0].buffer.data[0], -1);
  assert.equal(sources[0].buffer.data[1], 32767 / 32768);
  frames.shift()();
  assert.equal(starts, 0, "not audible before scheduled time");
  context.currentTime = sources[0].at;
  frames.shift()();
  assert.equal(starts, 1);
  controller.enqueue(pcm.slice(4800));
  controller.close();
  await reading;
  assert.equal(sources.length, 2);
  assert.equal(sources[1].at, sources[0].at + 0.1);
  player.finish();
  assert.equal(ends, 0, "finish waits for playback");
  sources[0].onended();
  sources[1].onended();
  assert.equal(ends, 1);
});

test("stopping speech cancels scheduled sources; malformed PCM fails", async (t) => {
  const { context, sources } = audioFixture(t);
  const player = new PCMPlayer(context);
  await player.append(new Response(new Uint8Array([0, 0])), new AbortController().signal);
  player.stop();
  assert.equal(sources[0].stopped, true);
  assert.equal(sources[0].onended, null);
  const next = new PCMPlayer(context);
  await assert.rejects(next.append(new Response(new Uint8Array([1])), new AbortController().signal), /incomplete/);
  next.stop();
});
