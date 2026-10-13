const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const { JSDOM } = require("jsdom");
const { IDBFactory } = require("fake-indexeddb");
function harness() {
  const dom = new JSDOM(
    '<p id="study-session-save"></p><video id="study-camera-preview"></video><div id="study-session-takes"></div><div id="study-session-recovery"></div><button id="study-session-sync"></button>',
    { runScripts: "outside-only", url: "https://example.com" },
  );
  const w = dom.window;
  w.indexedDB = new IDBFactory();
  w.Blob = Blob;
  w.URL.createObjectURL = () => "blob:test";
  w.URL.revokeObjectURL = () => {};
  w.HTMLMediaElement.prototype.play = async () => {};
  w.HTMLMediaElement.prototype.pause = () => {};
  let starts = 0,
    stops = 0,
    uploads = 0,
    failUpload = true;
  w.JazzStudyStore = { all: async () => [] };
  w.JazzPracticeSession = {
    refresh: async () => {},
    ensureGuidedBlocks: async () => ({
      session: { id: "day" },
      blocks: [
        {
          id: "section",
          blockKey: "training-study-one-ii-v-i",
          recordings: [],
        },
      ],
    }),
  };
  w.JazzRecordingPolicy = {
    preferredVideoType: () => "video/mp4",
    supportsChunkFlush: () => true,
  };
  w.JazzLosslessRecorder = class {
    constructor(onChunk) {
      this.onChunk = onChunk;
      this.context = { sampleRate: 48000 };
    }
    async start() {
      this.onChunk(new Float32Array(480), 48000);
    }
    async stop() {
      return {
        blob: new Blob(["wav"]),
        durationMS: 10000,
        sampleRate: 48000,
        waveformPeaks: [],
      };
    }
    async cancel() {}
  };
  w.MediaRecorder = class extends w.EventTarget {
    static isTypeSupported() {
      return true;
    }
    constructor() {
      super();
      this.state = "inactive";
      this.mimeType = "video/mp4";
    }
    start() {
      starts++;
      this.state = "recording";
    }
    requestData() {}
    stop() {
      stops++;
      this.state = "inactive";
      const event = new w.Event("dataavailable");
      event.data = new Blob(["video"]);
      this.dispatchEvent(event);
      this.dispatchEvent(new w.Event("stop"));
    }
  };
  w.JazzRecording = {
    uploadTrainingTake: async (capture) => {
      uploads++;
      assert.equal(capture.clientCaptureId, "run");
      if (failUpload) throw Error("offline");
      return "run";
    },
  };
  for (const name of ["training-take-store.js", "training-session-take.js"])
    w.eval(fs.readFileSync(__dirname + "/" + name, "utf8"));
  return {
    w,
    dom,
    stats: () => ({ starts, stops, uploads }),
    online: () => {
      failUpload = false;
    },
    stream: {
      getAudioTracks: () => [{}],
      getVideoTracks: () => [
        { getSettings: () => ({ width: 1280, height: 720, frameRate: 30 }) },
      ],
    },
  };
}
const wait = async (fn) => {
  for (let i = 0; i < 100; i++) {
    if (await fn()) return;
    await new Promise((r) => setTimeout(r, 5));
  }
  throw Error("timeout");
};
test("one session capture persists both assets and repeated finish creates one take", async () => {
  const h = harness();
  try {
    const api = h.w.JazzTrainingSessionTake;
    const reservation = await api.prepare("run");
    await api.start(h.stream, reservation, () => {});
    assert.equal(h.stats().starts, 1);
    // Multiple exercises do not call start/stop on the session recorder.
    assert.ok(api.offsetMs() >= 0);
    await Promise.all([api.finish(), api.finish()]);
    await wait(() => h.stats().uploads === 1);
    const saved = await h.w.JazzTrainingTakeStore.all();
    assert.equal(saved.length, 1);
    assert.equal(saved[0].state, "saved");
    assert.equal(saved[0].capture.videoBlob.size, 5);
    assert.equal(saved[0].capture.blob.size, 3);
    assert.equal(h.stats().stops, 1);
    await wait(() =>
      h.w.document
        .querySelector("#study-session-save")
        .textContent.includes("failed"),
    );
    h.online();
    await api.sync();
    assert.equal((await h.w.JazzTrainingTakeStore.all())[0].state, "synced");
    await api.sync();
    assert.equal(h.stats().uploads, 2);
    assert.equal(
      h.w.document.querySelectorAll("#study-session-takes video").length,
      1,
    );
  } finally {
    h.dom.window.close();
  }
});
test("camera is required and does not silently create an audio-only session", async () => {
  const h = harness();
  try {
    await assert.rejects(
      h.w.JazzTrainingSessionTake.start(
        { getVideoTracks: () => [], getAudioTracks: () => [{}] },
        { id: "run" },
        () => {},
      ),
      /camera/,
    );
    assert.equal(h.stats().starts, 0);
  } finally {
    h.dom.window.close();
  }
});

test("a session finished during another upload is automatically drained next", async () => {
  const h = harness();
  try {
    let release,
      began = false;
    const gate = new Promise((resolve) => {
      release = resolve;
    });
    const uploaded = [];
    h.w.JazzRecording.uploadTrainingTake = async (capture) => {
      uploaded.push(capture.clientCaptureId);
      if (capture.clientCaptureId === "old") {
        began = true;
        await gate;
      }
      return capture.clientCaptureId;
    };
    await h.w.JazzTrainingTakeStore.put({
      id: "old",
      state: "saved",
      recordedAt: new Date().toISOString(),
      capture: {
        clientCaptureId: "old",
        blob: new Blob(["audio"]),
        videoBlob: new Blob(["video"]),
        videoContentType: "video/mp4",
      },
    });
    const api = h.w.JazzTrainingSessionTake;
    const first = api.sync();
    await wait(() => began);
    await api.start(h.stream, await api.prepare("run"), () => {});
    await api.finish();
    release();
    await first;
    await wait(async () =>
      (await h.w.JazzTrainingTakeStore.all()).every(
        (t) => t.state === "synced",
      ),
    );
    assert.deepEqual(uploaded, ["old", "run"]);
  } finally {
    h.dom.window.close();
  }
});

test("failed session uploads retry automatically without another click", async () => {
  const h = harness();
  try {
    let retry;
    h.w.setTimeout = (fn) => {
      retry = fn;
      return 1;
    };
    const api = h.w.JazzTrainingSessionTake;
    await api.start(h.stream, await api.prepare("run"), () => {});
    await api.finish();
    await wait(() => Boolean(retry));
    h.online();
    await retry();
    await wait(
      async () => (await h.w.JazzTrainingTakeStore.all())[0].state === "synced",
    );
    assert.equal(h.stats().uploads, 2);
    assert.match(
      h.w.document.querySelector("#study-session-save").textContent,
      /uploaded to cloud/,
    );
  } finally {
    h.dom.window.close();
  }
});
