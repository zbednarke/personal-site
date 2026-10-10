const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

// A minimal Web Audio stand-in that records the graph so tests can prove the
// chain tears everything down.
function fakeAudio({ failModule = false } = {}) {
  const created = [];
  const contexts = [];
  const param = () => ({ value: 0, setTargetAtTime() {} });
  class Node {
    constructor(kind) {
      this.kind = kind;
      this.connections = 0;
      this.disconnected = false;
      this.gain = param();
      this.frequency = param();
      this.Q = param();
      this.delayTime = param();
      created.push(this);
    }
    connect(target) { this.connections += 1; return target; }
    disconnect() { this.disconnected = true; }
  }
  class AudioContext {
    constructor(options) {
      this.options = options;
      this.sampleRate = options?.sampleRate || 48000;
      this.state = "running";
      this.closed = false;
      this.currentTime = 0;
      this.destination = new Node("destination");
      this.audioWorklet = { addModule: async (url) => {
        this.moduleURL = url;
        if (failModule) throw new Error("worklet blocked");
      } };
      contexts.push(this);
    }
    createMediaStreamSource() { return new Node("source"); }
    createGain() { return new Node("gain"); }
    createWaveShaper() { return new Node("shaper"); }
    createBiquadFilter() { return new Node("filter"); }
    createAnalyser() {
      const node = new Node("analyser");
      node.fftSize = 2048;
      node.getFloatTimeDomainData = (samples) => samples.fill(0.2);
      return node;
    }
    createDelay() { return new Node("delay"); }
    createDynamicsCompressor() {
      const node = new Node("limiter");
      ["threshold", "knee", "ratio", "attack", "release"].forEach((name) => { node[name] = param(); });
      return node;
    }
    createConvolver() { return new Node("convolver"); }
    createBuffer(channels, length) {
      const data = Array.from({ length: channels }, () => new Float32Array(length));
      return { getChannelData: (channel) => data[channel] };
    }
    createOscillator() {
      const node = new Node("oscillator");
      node.started = false;
      node.stopped = false;
      node.start = () => { node.started = true; };
      node.stop = () => { node.stopped = true; };
      return node;
    }
    createMediaStreamDestination() {
      const node = new Node("capture");
      node.track = { stopped: false, stop() { this.stopped = true; } };
      node.stream = { getTracks: () => [node.track] };
      return node;
    }
    async resume() { this.state = "running"; }
    async close() { this.closed = true; this.state = "closed"; }
  }
  class AudioWorkletNode extends Node {
    constructor(context, name) {
      super(`worklet:${name}`);
      this.messages = [];
      this.port = { onmessage: null, postMessage: (message) => this.messages.push(message) };
    }
  }
  return { AudioContext, AudioWorkletNode, created, contexts };
}

function loadFX(audio, document) {
  const context = {
    document,
    URL,
    Float32Array,
    Math,
    Number,
    Object,
    Array,
    Error,
    Promise,
    String,
    Boolean,
    console,
    setInterval,
    clearInterval,
    location: { href: "https://zachbednarke.com/jazz/" },
    AudioContext: audio?.AudioContext,
    AudioWorkletNode: audio?.AudioWorkletNode,
  };
  context.globalThis = context;
  vm.runInNewContext(fs.readFileSync(require.resolve("./fx-chain.js"), "utf8"), context);
  return context.JazzFX;
}

test("every preset is complete and labelled", () => {
  const fx = loadFX();
  const ids = Object.keys(fx.presets);
  assert.ok(ids.includes("big-hall"));
  ids.forEach((id) => {
    const preset = fx.presets[id];
    assert.match(id, /^[a-z0-9][a-z0-9-]{0,39}$/, "preset ids must satisfy the API pattern");
    assert.ok(preset.label);
    assert.ok(["off", "tune", "harmony"].includes(preset.pitch.mode));
    if (preset.delay) assert.ok(preset.delay.feedback < 1, `${id} delay feedback must decay`);
    assert.equal(fx.presetLabel(id), preset.label);
  });
  assert.equal(fx.presetLabel("unknown-preset"), "unknown-preset");
  assert.equal(fx.presetLabel(""), "");
});

test("the chain resolves its worklet beside the script, captures mono, and monitors only on request", async () => {
  const audio = fakeAudio();
  const fx = loadFX(audio, {
    currentScript: { src: "https://zachbednarke.com/assets/jazz/fx-chain.js" },
    querySelector: () => null,
    querySelectorAll: () => [],
  });
  const chain = new fx.FXChain();
  const stream = await chain.start({}, "gospel-pad", 2, false);
  const [context] = audio.contexts;
  assert.equal(context.moduleURL, "https://zachbednarke.com/assets/jazz/pitch-worklet.js");
  assert.equal(context.options.sampleRate, 48000);
  assert.equal(chain.nodes.monitorGain.gain.value, 0);
  assert.equal(chain.nodes.capture.channelCount, 1);
  assert.equal(chain.nodes.capture.channelCountMode, "explicit");
  assert.equal(stream, chain.nodes.capture.stream);
  assert.equal(chain.nodes.pitch.messages.at(-1).keyRoot, 2);
  assert.ok(audio.created.filter((node) => node.kind === "oscillator").every((node) => node.started));
  await chain.stop();
});

test("stop tears down the whole graph and is safe to repeat", async () => {
  const audio = fakeAudio();
  const fx = loadFX(audio);
  const chain = new fx.FXChain();
  await chain.start({}, "miles-73", 0, true);
  assert.ok(chain.followerTimer, "auto-wah presets run the envelope follower");
  chain.followEnvelope();
  const capture = chain.nodes.capture;
  const pitch = chain.nodes.pitch;
  await chain.stop();
  await chain.stop();
  const [context] = audio.contexts;
  assert.equal(context.closed, true);
  assert.equal(chain.followerTimer, null);
  assert.equal(pitch.port.onmessage, null);
  assert.equal(capture.track.stopped, true);
  audio.created.filter((node) => node.kind !== "destination").forEach((node) => {
    assert.equal(node.disconnected, true, `${node.kind} was left connected`);
  });
});

test("presets without a pad create no oscillators", async () => {
  const audio = fakeAudio();
  const fx = loadFX(audio);
  const chain = new fx.FXChain();
  await chain.start({}, "big-hall", 0, false);
  assert.equal(audio.created.filter((node) => node.kind === "oscillator").length, 0);
  chain.handlePitch({ f0: 440, midi: 69, rms: 0.2 });
  await chain.stop();
});

test("a failed start closes its context instead of leaking it", async () => {
  const audio = fakeAudio({ failModule: true });
  const fx = loadFX(audio);
  const chain = new fx.FXChain();
  await assert.rejects(chain.start({}, "big-hall", 0, false), /worklet blocked/);
  assert.equal(audio.contexts[0].closed, true);
  assert.equal(chain.context, null);
  await assert.rejects(new fx.FXChain().start({}, "not-a-preset", 0, false), /Unknown FX preset/);
});

test("JazzFX.start reports failures and leaves no active chain", async () => {
  const audio = fakeAudio({ failModule: true });
  const fx = loadFX(audio);
  await assert.rejects(fx.start({}), /worklet blocked/);
  assert.equal(audio.contexts[0].closed, true);
  await fx.stop();
});
