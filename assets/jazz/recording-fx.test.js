const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");

// Drives recording.js end to end against stubbed media, Web Audio recorders,
// storage, and API so the FX failure paths can be shown never to cost the dry
// master.
function harness({ fx = {} } = {}) {
  const calls = [];
  const puts = [];
  const events = [];
  const recorders = [];
  const fxLog = { started: 0, stopped: 0 };
  const clock = { offset: 0 };
  const fxStream = { fx: true };

  class FakeRecorder {
    constructor() {
      this.paused = [];
      this.cancelled = false;
      recorders.push(this);
    }
    async start(stream) {
      this.isFx = Boolean(stream.fx);
      if (this.isFx && fx.recorderStartFails) throw new Error("fx recorder failed to start");
    }
    pause() { this.paused.push("pause"); }
    resume() { this.paused.push("resume"); }
    async stop() {
      this.stoppedAt = clock.offset;
      if (this.isFx && fx.recorderStopFails) throw new Error("fx flush failed");
      const size = this.isFx ? 30 : 20;
      return { blob: new Blob([new Uint8Array(size)], { type: "audio/wav" }), sampleRate: 48000, durationMS: 1000, waveformPeaks: [0.5] };
    }
    async cancel() { this.cancelled = true; }
  }

  const fakeTrack = { stop() {} };
  const micStream = { getTracks: () => [fakeTrack], getAudioTracks: () => [fakeTrack], clone() { return this; }, active: true };

  class FakeXHR {
    constructor() {
      this.upload = { addEventListener() {} };
      this.listeners = {};
    }
    open(method, url) { this.url = url; }
    setRequestHeader() {}
    addEventListener(name, listener) { this.listeners[name] = listener; }
    send(body) {
      puts.push({ url: this.url, size: body.size });
      queueMicrotask(() => {
        if (fx.uploadFails && this.url === "https://storage.test/fx") this.listeners.error();
        else {
          this.status = 200;
          this.listeners.load();
        }
      });
    }
  }

  async function fakeFetch(url, options = {}) {
    const path = url.replace("./api/v1", "");
    const body = options.body ? JSON.parse(options.body) : null;
    calls.push({ method: options.method || "GET", path, body });
    const respond = (status, payload) => ({ status, ok: status < 400, json: async () => payload });
    if (path === "/recordings" && !options.method) return respond(200, { recordings: [] });
    if (path === "/recordings/init") {
      return respond(201, {
        id: "rec-1",
        uploadUrl: "https://storage.test/dry",
        ...(body.fxSizeBytes && !fx.noFxSession ? { fxUploadUrl: "https://storage.test/fx" } : {}),
      });
    }
    if (path === "/recordings/rec-1/complete") return respond(200, { id: "rec-1", status: "ready" });
    if (options.method === "DELETE") return respond(204, null);
    return respond(404, { error: "unexpected" });
  }

  const context = {
    Blob, URL, Promise, Error, Math, Number, String, Boolean, Object, Array, Date, JSON, Map, Set, FormData, Uint8Array,
    console, clearTimeout, clearInterval, queueMicrotask,
    // Unref the page timers (e.g. the four-hour auto-stop) so a failing test
    // cannot keep node alive.
    setTimeout: (...args) => setTimeout(...args).unref(),
    setInterval: (...args) => setInterval(...args).unref(),
    performance: { now: () => Date.now() + clock.offset },
    document: { querySelector: () => null, querySelectorAll: () => [] },
    localStorage: { getItem: () => null, setItem() {} },
    navigator: { mediaDevices: { getUserMedia: async () => micStream, enumerateDevices: async () => [] } },
    fetch: fakeFetch,
    XMLHttpRequest: FakeXHR,
    CustomEvent: class { constructor(type, init) { this.type = type; this.detail = init?.detail; } },
    dispatchEvent: (event) => events.push(event),
    addEventListener() {},
    JAZZ_DATA: { mission: { id: "mission" }, repertoire: [], skills: [], tracks: [] },
    JazzLosslessRecorder: FakeRecorder,
    JazzPracticeSession: { ensureActive: async () => ({ id: "session-1" }), currentID: () => "session-1", refresh: async () => {} },
    JazzFX: {
      enabled: () => fx.enabled !== false,
      presetLabel: (id) => id,
      async start() {
        fxLog.started += 1;
        if (fx.chainFails) throw new Error("worklet blocked");
        return { stream: fxStream, preset: "space-echo" };
      },
      async stop() { fxLog.stopped += 1; },
    },
  };
  context.globalThis = context;
  vm.createContext(context);
  ["recording-policy.js", "upload-queue.js", "recording.js"].forEach((file) => {
    vm.runInContext(fs.readFileSync(require.resolve(`./${file}`), "utf8"), context, { filename: file });
  });

  const settle = () => new Promise((resolve) => setTimeout(resolve, 5));
  async function waitFor(predicate) {
    for (let attempt = 0; attempt < 400; attempt += 1) {
      if (predicate()) return;
      await settle();
    }
    throw new Error("timed out");
  }
  const uploadStates = () => events.filter((event) => event.type === "jazz:upload-state").map((event) => event.detail);
  return {
    recording: context.JazzRecording,
    calls,
    puts,
    recorders,
    fxLog,
    clock,
    uploadStates,
    waitFor,
    async recordTake({ pause = false, during = async () => {} } = {}) {
      context.JazzRecording.startForBlock({ id: "block-1", takeNumber: 1, tuneId: "", skillIds: [] });
      await waitFor(() => events.some((event) => event.detail?.phase === "recording" || event.detail?.phase === "error"));
      await during();
      if (pause) {
        context.JazzRecording.togglePause();
        context.JazzRecording.togglePause();
      }
      context.JazzRecording.stop();
      await waitFor(() => uploadStates().some((state) => state.phase === "complete" || state.phase === "failed"));
      return uploadStates().at(-1);
    },
  };
}

const init = (h) => h.calls.find((call) => call.path === "/recordings/init").body;
const completes = (h) => h.calls.filter((call) => call.path === "/recordings/rec-1/complete").map((call) => call.body.asset);
const deletes = (h) => h.calls.filter((call) => call.method === "DELETE").map((call) => call.path);

test("a take with live effects uploads the dry master first, then the FX mix", async () => {
  const h = harness();
  const final = await h.recordTake({ pause: true });
  assert.equal(final.phase, "complete");
  assert.equal(final.message, "Uploaded privately");
  assert.equal(init(h).sizeBytes, 20);
  assert.equal(init(h).fxSizeBytes, 30);
  assert.equal(init(h).fxContentType, "audio/wav");
  assert.equal(init(h).fxPreset, "space-echo");
  assert.deepEqual(h.puts.map((put) => put.url), ["https://storage.test/dry", "https://storage.test/fx"]);
  assert.deepEqual(completes(h), ["audio", "fx"]);
  assert.deepEqual(deletes(h), []);
  const fxRecorder = h.recorders.find((recorder) => recorder.isFx);
  assert.deepEqual(fxRecorder.paused, ["pause", "resume"], "pausing the take pauses the FX capture too");
  assert.equal(h.fxLog.stopped >= 1, true, "the effects chain is released after the take");
});

test("an effects chain that cannot start leaves a dry-only take", async () => {
  const h = harness({ fx: { chainFails: true } });
  const final = await h.recordTake();
  assert.equal(final.phase, "complete");
  assert.equal(init(h).fxSizeBytes, 0);
  assert.equal(init(h).fxPreset, "");
  assert.deepEqual(completes(h), ["audio"]);
  assert.equal(h.recorders.filter((recorder) => !recorder.isFx).length, 1);
  assert.equal(h.fxLog.stopped >= 1, true);
});

test("an FX recorder that fails to start is cancelled and the dry take continues", async () => {
  const h = harness({ fx: { recorderStartFails: true } });
  const final = await h.recordTake();
  assert.equal(final.phase, "complete");
  assert.equal(init(h).fxSizeBytes, 0);
  assert.equal(h.recorders.find((recorder) => recorder.isFx).cancelled, true);
});

test("an FX mix that cannot be finished never blocks the dry master", async () => {
  const h = harness({ fx: { recorderStopFails: true } });
  const final = await h.recordTake();
  assert.equal(final.phase, "complete");
  assert.equal(init(h).fxSizeBytes, 0);
  assert.deepEqual(completes(h), ["audio"]);
});

test("a failed FX upload drops only the FX asset and keeps the dry take", async () => {
  const h = harness({ fx: { uploadFails: true } });
  const final = await h.recordTake();
  assert.equal(final.phase, "complete");
  assert.match(final.message, /FX mix upload failed \(network error\); the dry master is saved/);
  assert.deepEqual(completes(h), ["audio"]);
  assert.deepEqual(deletes(h), ["/recordings/rec-1/fx"]);
});

test("a missing FX upload session is reported without touching the dry take", async () => {
  const h = harness({ fx: { noFxSession: true } });
  const final = await h.recordTake();
  assert.equal(final.phase, "complete");
  assert.match(final.message, /FX mix upload failed/);
  assert.deepEqual(h.puts.map((put) => put.url), ["https://storage.test/dry"]);
  assert.deepEqual(deletes(h), []);
});

test("takes without live effects never touch the effects chain", async () => {
  const h = harness({ fx: { enabled: false } });
  const final = await h.recordTake();
  assert.equal(final.phase, "complete");
  assert.equal(h.fxLog.started, 0);
  assert.equal(init(h).fxSizeBytes, 0);
});

test("the FX capture stops at its one-hour cap while the dry take keeps going", async () => {
  const h = harness();
  const final = await h.recordTake({
    pause: true,
    during: async () => {
      h.clock.offset = 3_600_000 + 1000;
      await h.waitFor(() => h.recorders.find((recorder) => recorder.isFx)?.stoppedAt !== undefined);
      h.clock.offset = 3_700_000;
    },
  });
  assert.equal(final.phase, "complete");
  const fxRecorder = h.recorders.find((recorder) => recorder.isFx);
  const dryRecorder = h.recorders.find((recorder) => !recorder.isFx);
  assert.equal(fxRecorder.stoppedAt, 3_601_000);
  assert.equal(dryRecorder.stoppedAt, 3_700_000);
  assert.deepEqual(fxRecorder.paused, [], "a finished FX capture is not paused again");
  assert.equal(init(h).fxSizeBytes, 30);
});
