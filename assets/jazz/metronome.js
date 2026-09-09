(function (root) {
  "use strict";
  const defaults = { bpm: 120, beats: 4, volume: 0.35 };
  const clamp = (value, min, max, fallback) => Number.isFinite(Number(value)) ? Math.max(min, Math.min(max, Number(value))) : fallback;
  function normalizeSettings(value = {}) {
    value = value || {};
    return {
      bpm: Math.round(clamp(value.bpm, 30, 300, defaults.bpm)),
      beats: Math.round(clamp(value.beats, 1, 7, defaults.beats)),
      volume: clamp(value.volume, 0, 1, defaults.volume),
    };
  }
  function tapTempo(taps, now) {
    const recent = taps.length && now - taps[taps.length - 1] <= 2500 ? [...taps, now].slice(-6) : [now];
    const interval = recent.length > 1 ? (recent[recent.length - 1] - recent[0]) / (recent.length - 1) : 0;
    return { taps: recent, bpm: interval > 0 ? Math.round(Math.max(30, Math.min(300, 60000 / interval))) : null };
  }

  class MetronomeEngine {
    constructor({ AudioContextClass = root.AudioContext || root.webkitAudioContext, NodeClass = root.AudioWorkletNode, moduleURL, onBeat = () => {} } = {}) {
      this.AudioContextClass = AudioContextClass;
      this.NodeClass = NodeClass;
      this.moduleURL = moduleURL;
      this.onBeat = onBeat;
      this.generation = 0;
      this.context = null;
      this.node = null;
      this.loading = null;
      this.connected = false;
    }
    async start(settings) {
      const generation = ++this.generation;
      if (!this.AudioContextClass || !this.NodeClass) throw new Error("This browser does not support the metronome audio engine.");
      if (!this.context) this.context = new this.AudioContextClass();
      const resumed = this.context.resume(); // Called inside the user's click gesture.
      if (!this.loading) this.loading = this.context.audioWorklet.addModule(this.moduleURL).catch((error) => { this.loading = null; throw error; });
      await Promise.all([resumed, this.loading]);
      if (generation !== this.generation) return false;
      if (!this.node) {
        this.node = new this.NodeClass(this.context, "jazz-metronome", { numberOfInputs: 0, numberOfOutputs: 1, outputChannelCount: [1] });
        this.node.port.onmessage = ({ data }) => { if (this.connected) this.onBeat(data); };
      }
      this.node.port.postMessage({ ...normalizeSettings(settings), running: true });
      if (!this.connected) this.node.connect(this.context.destination);
      this.connected = true;
      return true;
    }
    configure(settings) { this.node?.port.postMessage(normalizeSettings(settings)); }
    stop() {
      this.generation += 1;
      this.node?.port.postMessage({ running: false });
      if (this.connected) this.node.disconnect();
      this.connected = false;
    }
    close() {
      this.stop();
      const context = this.context;
      this.node = null;
      this.loading = null;
      this.context = null;
      return context?.close().catch(() => {});
    }
  }
  if (typeof module === "object" && module.exports) module.exports = { normalizeSettings, tapTempo, MetronomeEngine };
  if (!root.document) return;
  const panel = document.querySelector("#practice-metronome");
  if (!panel) return;
  const key = "jazz-metronome-v1";
  let settings = { ...defaults };
  try { settings = normalizeSettings(JSON.parse(localStorage.getItem(key) || "{}") || {}); } catch {}
  const $ = (selector) => panel.querySelector(selector);
  let running = false;
  let starting = false;
  let taps = [];
  const moduleURL = new URL("metronome-worklet.js", document.currentScript.src).href;
  const engine = new MetronomeEngine({ moduleURL, onBeat: ({ beat }) => {
    if (!running || document.hidden) return;
    panel.querySelectorAll("[data-metronome-beat]").forEach((dot, index) => dot.classList.toggle("active", index === beat));
    const dot = panel.querySelectorAll("[data-metronome-beat]")[beat];
    if (dot?.animate && !matchMedia("(prefers-reduced-motion: reduce)").matches) dot.animate([{ transform: "scale(1.3)" }, { transform: "scale(1)" }], { duration: 110 });
  } });
  function render(message) {
    $("#metronome-bpm").value = settings.bpm;
    $("#metronome-tempo").value = settings.bpm;
    $("#metronome-meter").value = settings.beats;
    $("#metronome-volume").value = Math.round(settings.volume * 100);
    $("#metronome-volume").setAttribute("aria-valuetext", settings.volume === 0 ? "Silent, visual beats only" : `${Math.round(settings.volume * 100)} percent`);
    $("#metronome-toggle").textContent = starting || running ? "Stop" : "Start";
    $("#metronome-toggle").setAttribute("aria-pressed", String(starting || running));
    panel.classList.toggle("metronome-running", running);
    $("#metronome-beats").innerHTML = Array.from({ length: settings.beats }, (_, i) => `<span data-metronome-beat class="${i === 0 && settings.beats > 1 ? "downbeat" : ""}"></span>`).join("");
    $("#metronome-status").textContent = message || (starting ? "Starting…" : running ? `${settings.bpm} BPM · ${settings.volume ? "Playing" : "Visual only"}` : "Ready");
  }
  function update(changes) {
    settings = normalizeSettings({ ...settings, ...changes });
    engine.configure(settings);
    try { localStorage.setItem(key, JSON.stringify(settings)); } catch {}
    render();
  }
  function stop(message) { starting = false; running = false; engine.stop(); render(message); }
  $("#metronome-toggle").addEventListener("click", async () => {
    if (running || starting) { stop(); return; }
    starting = true;
    render();
    try {
      if (!await engine.start(settings)) return;
      engine.configure(settings);
      starting = false;
      running = true;
      render();
    } catch (error) { if (starting) stop(`Could not start: ${error.message}`); }
  });
  $("#metronome-bpm").addEventListener("change", (event) => update({ bpm: event.target.value || settings.bpm }));
  $("#metronome-tempo").addEventListener("input", (event) => update({ bpm: event.target.value }));
  $("#metronome-meter").addEventListener("change", (event) => update({ beats: event.target.value }));
  $("#metronome-volume").addEventListener("input", (event) => update({ volume: Number(event.target.value) / 100 }));
  $("#metronome-slower").addEventListener("click", () => update({ bpm: settings.bpm - 1 }));
  $("#metronome-faster").addEventListener("click", () => update({ bpm: settings.bpm + 1 }));
  $("#metronome-tap").addEventListener("click", () => {
    const result = tapTempo(taps, performance.now());
    taps = result.taps;
    if (result.bpm) update({ bpm: result.bpm });
    else $("#metronome-status").textContent = "Keep tapping your beat";
  });
  addEventListener("pagehide", () => { stop(); engine.close(); });
  addEventListener("hashchange", () => { if (location.hash && location.hash !== "#today") stop(); });
  render(); // Preferences persist; playback never starts automatically.
})(globalThis);
