const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const { JSDOM } = require("jsdom");
const { IDBFactory } = require("fake-indexeddb");
const waitFor = async (check) => {
  for (let n = 0; n < 400; n++) {
    if (await check()) return;
    await new Promise((r) => setTimeout(r, 5));
  }
  throw new Error("Timed out waiting for trainer");
};
function harness() {
  const html = fs.readFileSync(
    require("node:path").join(__dirname, "../../jazz/index.html"),
    "utf8",
  );
  const dom = new JSDOM(html, {
    url: "http://localhost/jazz/#training",
    runScripts: "outside-only",
    pretendToBeVisual: true,
  });
  const w = dom.window;
  w.indexedDB = new IDBFactory();
  w.Blob = Blob;
  w.URL.createObjectURL = () => "blob:test";
  w.URL.revokeObjectURL = () => {};
  w.HTMLMediaElement.prototype.pause = () => {};
  w.HTMLMediaElement.prototype.play = () => Promise.resolve();
  w.HTMLElement.prototype.scrollIntoView = () => {};
  let lockHeld = false,
    context,
    worklet,
    offline = true;
  const remote = new Map();
  w.JazzTrainingSessionTake = {prepare: async id=>({id}),start:async()=>{},finish:async()=>{},offsetMs:()=>0,initialize:async()=>{}};
  Object.defineProperty(w.navigator, "locks", {
    value: {
      request: async (name, options, fn) => {
        if (lockHeld) return fn(null);
        lockHeld = true;
        try {
          return await fn({ name });
        } finally {
          lockHeld = false;
        }
      },
    },
  });
  Object.defineProperty(w.navigator, "storage", {
    value: { persist: async () => true },
  });
  Object.defineProperty(w.navigator, "mediaDevices", {
    value: {
      enumerateDevices: async () => [],
      getUserMedia: async () => ({
        getTracks: () => [{ stop() {}, addEventListener() {} }],
        getAudioTracks: () => [{ addEventListener() {} }],
      }),
    },
  });
  class Link {
    connect() {
      return this;
    }
    disconnect() {}
  }
  w.AudioContext = class {
    constructor() {
      context = this;
      this.currentTime = 0;
      this.sampleRate = 48000;
      this.state = "running";
      this.destination = {};
      this.audioWorklet = { addModule: async () => {} };
    }
    async resume() {}
    async close() {
      this.state = "closed";
    }
    addEventListener() {}
    createMediaStreamSource() {
      return new Link();
    }
    createGain() {
      const n = new Link();
      n.gain = {
        value: 0,
        setValueAtTime() {},
        linearRampToValueAtTime() {},
        exponentialRampToValueAtTime() {},
      };
      return n;
    }
    createOscillator() {
      const n = new Link();
      n.frequency = { value: 0 };
      n.start = () => {};
      n.stop = () => {};
      return n;
    }
  };
  w.AudioWorkletNode = class extends Link {
    constructor() {
      super();
      worklet = this;
      this.port = {
        onmessage: null,
        postMessage: (data) => {
          if (data.type === "start") this.start = data;
          if (data.type === "stop")
            this.port.onmessage({
              data: {
                type: "done",
                id: this.start.id,
                frame: Math.round(context.currentTime * 48000),
              },
            });
        },
      };
    }
  };
  w.fetch = async (url, options = {}) => {
    if (offline) throw new Error("Offline");
    const parts = url.split("/"),
      audio = parts.at(-1) === "audio",
      id = parts.at(audio ? -2 : -1);
    if (options.method === "PUT") {
      if (audio) remote.get(id).audioReady = true;
      else if (!remote.has(id))
        remote.set(id, {
          attempt: JSON.parse(options.body),
          audioReady: false,
        });
      return { ok: true, json: async () => ({}) };
    }
    return {
      ok: true,
      json: async () => ({ attempts: [...remote.values()], next: "" }),
    };
  };
  for (const file of [
    "audio-analysis.js",
    "study-one-engine.js",
    "study-one-store.js",
    "study-one-trainer.js",
  ])
    w.eval(fs.readFileSync(require("node:path").join(__dirname, file), "utf8"));
  return {
    w,
    dom,
    remote,
    online: () => {
      offline = false;
    },
    capture: () => ({ context, worklet }),
  };
}
test("offline trial grades promptly, commits audio, and retries sync without duplication", async () => {
  const h = harness(),
    { w } = h;
  try {
    await waitFor(() =>
      w.document
        .querySelector("#study-save")
        .textContent.startsWith("Ready ·"),
    );
    assert.equal(w.document.querySelector("#study-tempo").value, "50");
    const slider = w.document.querySelector("#study-tempo-slider");
    slider.value = "80";
    slider.dispatchEvent(new w.Event("input"));
    assert.equal(w.document.querySelector("#study-tempo").value, "80");
    assert.equal(w.localStorage.getItem("jazz-study-one-tempo"), "80");
    w.document.querySelector("#study-start").click();
    await waitFor(() => h.capture().worklet?.start);
    const { worklet, context } = h.capture(),
      start = worklet.start,
      key = w.document.querySelector("#study-prompt strong").textContent,
      ex = w.JazzStudyOne.exercise(key),
      rate = 48000,
      beat = 0.75;
    for (
      let frame = Math.round(start.start * rate);
      frame < Math.round(start.end * rate);
      frame += 4096
    ) {
      const samples = new Float32Array(
        Math.min(4096, Math.round(start.end * rate) - frame),
      );
      for (let i = 0; i < samples.length; i++) {
        const t = (frame + i) / rate - start.start - 4 * beat,
          n = ex.notes.find(
            (n) => t >= n.beat * beat && t < (n.beat + n.length) * beat,
          );
        if (n)
          samples[i] =
            0.15 *
            Math.sin(
              (2 * Math.PI * (440 * 2 ** ((n.midi - 69) / 12)) * (frame + i)) /
                rate,
            );
      }
      worklet.port.onmessage({
        data: { type: "samples", id: start.id, frame, samples },
      });
    }
    context.currentTime = start.end;
    const before = Date.now();
    worklet.port.onmessage({
      data: { type: "done", id: start.id, frame: Math.round(start.end * rate) },
    });
    assert.match(
      w.document.querySelector("#study-feedback").textContent,
      /Pass · 7\/7/,
    );
    assert.ok(Date.now() - before < 1000);
    w.document.querySelector("#study-report details").open = true;
    await waitFor(() => w.document.querySelector("#study-phase").textContent.includes("Paused to review"));
    await waitFor(() => !w.document.querySelector("#study-continue").disabled);
    assert.equal(w.document.querySelector("#study-continue").hidden, false);
    w.document.querySelector("#study-stop").click();
    await waitFor(async () => (await w.JazzStudyStore.all())[0]?.finalized);
    const attempt = (await w.JazzStudyStore.all())[0];
    assert.equal(attempt.durationMs, 12000);
    assert.equal(attempt.audioBytes, 1440044);
    assert.equal(attempt.synced, false);
    assert.equal(
      (await w.JazzStudyStore.chunks(start.id)).reduce(
        (n, c) => n + c.length,
        0,
      ),
      720000,
    );
    await waitFor(() => !w.document.querySelector("#study-sync").disabled);
    h.online();
    w.document.querySelector("#study-sync").click();
    await waitFor(async () => (await w.JazzStudyStore.all())[0]?.synced);
    await waitFor(() => !w.document.querySelector("#study-sync").disabled);
    w.document.querySelector("#study-sync").click();
    await waitFor(() => h.remote.size === 1);
    assert.equal(h.remote.get(start.id).audioReady, true);
    await waitFor(() => !w.document.querySelector("#study-sync").disabled);
  } finally {
    h.dom.window.close();
  }
});
test("local chunk failure stops the session and exposes a download instead of claiming safety", async () => {
  const h = harness(),
    { w } = h;
  try {
    await waitFor(() =>
      w.document
        .querySelector("#study-save")
        .textContent.startsWith("Ready ·"),
    );
    w.JazzStudyStore.chunk = async () => {
      throw new Error("QuotaExceededError");
    };
    w.document.querySelector("#study-start").click();
    await waitFor(() => h.capture().worklet?.start);
    const { worklet, context } = h.capture();
    context.currentTime = worklet.start.start + 0.1;
    worklet.port.onmessage({
      data: {
        type: "samples",
        id: worklet.start.id,
        frame: Math.round(worklet.start.start * 48000),
        samples: new Float32Array(4096),
      },
    });
    await waitFor(() => !w.document.querySelector("#study-emergency").hidden);
    assert.match(
      w.document.querySelector("#study-save").textContent,
      /Save failed/,
    );
    assert.equal((await w.JazzStudyStore.all())[0].finalized, false);
    assert.equal(h.remote.size, 0);
    await waitFor(() => !w.document.querySelector("#study-start").hidden);
  } finally {
    h.dom.window.close();
  }
});


test("unreadable audio pauses retries and quick reset reuses the pending repetition", async () => {
  const h = harness(), {w} = h;
  try {
    await waitFor(() => w.document.querySelector("#study-save").textContent.startsWith("Ready ·"));
    w.document.querySelector("#study-start").click();
    await waitFor(() => h.capture().worklet?.start);
    const {worklet, context} = h.capture(), start = worklet.start, rate = 48000;
    let seed = 123456;
    for (let frame = Math.round(start.start * rate); frame < Math.round(start.end * rate); frame += 4096) {
      const samples = new Float32Array(Math.min(4096, Math.round(start.end * rate) - frame));
      for (let i = 0; i < samples.length; i++) {
        seed = (1664525 * seed + 1013904223) >>> 0;
        samples[i] = (seed / 4294967296 - 0.5) * 0.3;
      }
      worklet.port.onmessage({data: {type: "samples", id: start.id, frame, samples}});
    }
    context.currentTime = start.end;
    worklet.port.onmessage({data: {type: "done", id: start.id, frame: Math.round(start.end * rate)}});
    await waitFor(() => w.document.querySelector("#study-phase").textContent.includes("Audio check · paused"));
    assert.doesNotMatch(w.document.querySelector("#study-feedback").textContent, /0\/7/);
    assert.equal(w.document.querySelector("#study-continue").hidden, false);
    await new Promise(resolve => setTimeout(resolve, 4200));
    assert.equal(worklet.start.id, start.id, "must not enter an automatic retry loop");
    const prompt = w.document.querySelector("#study-prompt").textContent;
    w.document.querySelector("#study-reset").click();
    await waitFor(() => worklet.start.id !== start.id);
    assert.equal(w.document.querySelector("#study-prompt").textContent, prompt);
    assert.equal(w.document.querySelector("#study-continue").hidden, true);
    w.document.querySelector("#study-stop").click();
    await waitFor(async () => (await w.JazzStudyStore.all()).filter(a => a.finalized).length === 2);
    await waitFor(() => !w.document.querySelector("#study-sync").disabled);
    await waitFor(() => !w.document.querySelector("#study-start").disabled);
  } finally { h.dom.window.close(); }
});

test("quick reset replays the same key without ending the session or marking a miss", async () => {
 const h=harness(),{w}=h;let finishes=0;
 w.JazzTrainingSessionTake.finish=async()=>{finishes++;};
 try {
  await waitFor(()=>w.document.querySelector('#study-save').textContent.startsWith('Ready ·'));
  w.document.querySelector('#study-start').click();
  await waitFor(()=>h.capture().worklet?.start);
  const {worklet}=h.capture(),first=worklet.start.id,prompt=w.document.querySelector('#study-prompt').textContent;
  w.document.querySelector('#study-reset').click();
  w.document.querySelector('#study-reset').click();
  await waitFor(()=>worklet.start.id!==first);
  assert.equal(w.document.querySelector('#study-prompt').textContent,prompt);
  assert.equal(finishes,0);
  const records=await w.JazzStudyStore.all();
  assert.equal(records.length,2);
  assert.equal(records.find(a=>a.id===first).result.status,'interrupted');
  assert.equal(records[0].sessionId,records[1].sessionId);
  const second=worklet.start.id;
  w.document.querySelector('#study-tempo').dispatchEvent(new w.KeyboardEvent('keydown',{key:'r',bubbles:true}));
  assert.equal(worklet.start.id,second);
  w.document.dispatchEvent(new w.KeyboardEvent('keydown',{key:'r',bubbles:true}));
  await waitFor(()=>worklet.start.id!==second);
  assert.equal(w.document.querySelector('#study-prompt').textContent,prompt);
  assert.equal(finishes,0);
  w.document.querySelector('#study-stop').click();
  await waitFor(()=>finishes===1);
  await waitFor(()=>!w.document.querySelector("#study-start").hidden);
  await waitFor(()=>!w.document.querySelector("#study-sync").disabled);
 } finally {w.close();}
});
