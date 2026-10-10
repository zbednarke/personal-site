(() => {
  "use strict";

  const FX_ENABLED_STORAGE_KEY = "zach-jazz-fx-enabled-v1";
  const FX_PRESET_STORAGE_KEY = "zach-jazz-fx-preset-v1";
  const FX_KEY_STORAGE_KEY = "zach-jazz-fx-key-v1";
  // v2: wet monitoring now defaults off; v1 stored an implicit "on".
  const FX_MONITOR_STORAGE_KEY = "zach-jazz-fx-monitor-v2";
  const DEFAULT_PRESET = "big-hall";
  const hasDocument = typeof document !== "undefined";
  // Resolve the worklet next to this script so the chain works from any page.
  const SCRIPT_URL = (hasDocument && document.currentScript?.src) || globalThis.location?.href || "";
  const $ = (selector, root = globalThis.document) => root?.querySelector(selector) || null;

  const NOTE_NAMES = ["C", "C#", "D", "D#", "E", "F", "F#", "G", "G#", "A", "A#", "B"];
  const SCALES = {
    major: [0, 2, 4, 5, 7, 9, 11],
    minor: [0, 2, 3, 5, 7, 8, 10],
    mixolydian: [0, 2, 4, 5, 7, 9, 10],
    blues: [0, 3, 5, 6, 7, 10],
    chromatic: [0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11],
  };

  // Each preset is a full snapshot of the chain: which stages run, their
  // parameters, the pitch-engine mode, and whether the auto-chord pad plays.
  // Preset ids are stored on the recording, so keep them stable.
  const PRESETS = {
    "big-hall": {
      label: "Big Hall",
      drive: null, wah: null, delay: null,
      reverb: { size: 4.5, mix: 0.45 },
      pitch: { mode: "off" }, pad: null,
    },
    "space-echo": {
      label: "Space Echo",
      drive: null, wah: null,
      delay: { time: 0.45, feedback: 0.55, mix: 0.4 },
      reverb: { size: 2.0, mix: 0.25 },
      pitch: { mode: "off" }, pad: null,
    },
    "miles-73": {
      label: "Miles '73",
      drive: { amount: 45, mix: 0.8 },
      wah: { sensitivity: 0.65, q: 7.9 },
      delay: { time: 0.34, feedback: 0.3, mix: 0.2 },
      reverb: { size: 1.2, mix: 0.15 },
      pitch: { mode: "off" }, pad: null,
    },
    "two-horns": {
      label: "Two Horns (3rds)",
      drive: null, wah: null, delay: null,
      reverb: { size: 2.0, mix: 0.25 },
      pitch: { mode: "harmony", scaleName: "major", voicing: "third", harmMix: 0.7, glideMs: 40 },
      pad: null,
    },
    "hard-tune": {
      label: "Hard-Tune",
      drive: null, wah: null,
      delay: { time: 0.3, feedback: 0.2, mix: 0.15 },
      reverb: { size: 1.5, mix: 0.2 },
      pitch: { mode: "tune", scaleName: "major", strength: 1, glideMs: 4 },
      pad: null,
    },
    "gospel-pad": {
      label: "Gospel Pad",
      drive: null, wah: null, delay: null,
      reverb: { size: 3.0, mix: 0.35 },
      pitch: { mode: "off", scaleName: "major" },
      pad: { volume: 0.6 },
    },
    "almost-dry": {
      label: "Almost Dry",
      drive: null, wah: null, delay: null,
      reverb: { size: 0.8, mix: 0.12 },
      pitch: { mode: "off" }, pad: null,
    },
  };

  function presetLabel(id) {
    return PRESETS[id]?.label || String(id || "");
  }

  function makeImpulse(context, seconds) {
    const rate = context.sampleRate;
    const length = Math.max(1, Math.floor(rate * seconds));
    const buffer = context.createBuffer(2, length, rate);
    for (let channel = 0; channel < 2; channel += 1) {
      const data = buffer.getChannelData(channel);
      for (let index = 0; index < length; index += 1) {
        data[index] = (Math.random() * 2 - 1) * Math.pow(1 - index / length, 2.2);
      }
    }
    return buffer;
  }

  function makeDriveCurve(amount) {
    const k = amount * 4;
    const size = 1024;
    const curve = new Float32Array(size);
    for (let index = 0; index < size; index += 1) {
      const x = (index / (size - 1)) * 2 - 1;
      curve[index] = ((3 + k) * x * 20 * Math.PI / 180) / (Math.PI + k * Math.abs(x));
    }
    return curve;
  }

  class FXChain {
    constructor() {
      this.context = null;
      this.nodes = {};
      this.stream = null;
      this.preset = null;
      this.keyRoot = 0;
      this.followerTimer = null;
      this.padState = { lastMidi: 0, stable: 0, silent: 0 };
    }

    // Builds the graph in its own 48 kHz context and returns the processed
    // stream. Any failure tears down whatever was built before rethrowing.
    async start(inputStream, presetName, keyRoot, monitor) {
      const preset = PRESETS[presetName];
      if (!preset) throw new Error("Unknown FX preset");
      const AudioContext = globalThis.AudioContext || globalThis.webkitAudioContext;
      if (!AudioContext || !globalThis.AudioWorkletNode) throw new Error("Live effects are not supported in this browser");
      this.preset = preset;
      this.keyRoot = keyRoot;
      try {
        await this.build(AudioContext, inputStream, monitor);
      } catch (error) {
        await this.stop();
        throw error;
      }
      return this.stream;
    }

    async build(AudioContext, inputStream, monitor) {
      const context = new AudioContext({ sampleRate: 48000, latencyHint: "interactive" });
      this.context = context;
      await context.audioWorklet.addModule(new URL("pitch-worklet.js", SCRIPT_URL).href);
      if (this.context !== context) throw new Error("Live effects were stopped while starting");
      const preset = this.preset;
      const n = this.nodes;
      n.source = context.createMediaStreamSource(inputStream);
      n.input = context.createGain();

      n.pitch = new globalThis.AudioWorkletNode(context, "pitch-engine", {
        numberOfInputs: 1, numberOfOutputs: 1, outputChannelCount: [1],
      });
      n.pitch.port.onmessage = (event) => this.handlePitch(event.data);

      n.driveShaper = context.createWaveShaper();
      n.driveShaper.oversample = "4x";
      n.driveWet = context.createGain();
      n.driveDry = context.createGain();
      n.driveOut = context.createGain();

      n.wahFilter = context.createBiquadFilter();
      n.wahFilter.type = "bandpass";
      n.wahWet = context.createGain();
      n.wahDry = context.createGain();
      n.wahOut = context.createGain();
      n.follower = context.createAnalyser();
      n.follower.fftSize = 1024;

      n.delay = context.createDelay(2.0);
      n.delayFeedback = context.createGain();
      n.delayTone = context.createBiquadFilter();
      n.delayTone.type = "lowpass";
      n.delayTone.frequency.value = 3500;
      n.delayWet = context.createGain();
      n.delayDry = context.createGain();
      n.delayOut = context.createGain();

      n.convolver = context.createConvolver();
      n.reverbWet = context.createGain();
      n.reverbDry = context.createGain();
      n.reverbOut = context.createGain();

      n.master = context.createGain();
      // Reverb tails, harmony voices, and the pad stack on top of the dry
      // signal; a fast limiter keeps the 24-bit FX mix from hard clipping.
      n.limiter = context.createDynamicsCompressor();
      n.limiter.threshold.value = -3;
      n.limiter.knee.value = 0;
      n.limiter.ratio.value = 20;
      n.limiter.attack.value = 0.003;
      n.limiter.release.value = 0.25;
      n.monitorGain = context.createGain();
      // The lossless recorder keeps only the first channel, so fold the stereo
      // reverb down to a true mono mix here instead of capturing just the left.
      n.capture = context.createMediaStreamDestination();
      n.capture.channelCount = 1;
      n.capture.channelCountMode = "explicit";
      n.capture.channelInterpretation = "speakers";

      n.source.connect(n.input);
      n.input.connect(n.follower);
      n.input.connect(n.pitch);
      n.pitch.connect(n.driveShaper);
      n.driveShaper.connect(n.driveWet);
      n.driveWet.connect(n.driveOut);
      n.pitch.connect(n.driveDry);
      n.driveDry.connect(n.driveOut);

      n.driveOut.connect(n.wahFilter);
      n.wahFilter.connect(n.wahWet);
      n.wahWet.connect(n.wahOut);
      n.driveOut.connect(n.wahDry);
      n.wahDry.connect(n.wahOut);

      n.wahOut.connect(n.delayTone);
      n.delayTone.connect(n.delay);
      n.delay.connect(n.delayFeedback);
      n.delayFeedback.connect(n.delay);
      n.delay.connect(n.delayWet);
      n.delayWet.connect(n.delayOut);
      n.wahOut.connect(n.delayDry);
      n.delayDry.connect(n.delayOut);

      n.delayOut.connect(n.convolver);
      n.convolver.connect(n.reverbWet);
      n.reverbWet.connect(n.reverbOut);
      n.delayOut.connect(n.reverbDry);
      n.reverbDry.connect(n.reverbOut);
      n.reverbOut.connect(n.master);

      // The pad's oscillators only exist for presets that use them.
      if (preset.pad) {
        n.padGain = context.createGain();
        n.padEnv = context.createGain();
        n.padEnv.gain.value = 0;
        n.padFilter = context.createBiquadFilter();
        n.padFilter.type = "lowpass";
        n.padFilter.frequency.value = 900;
        n.padVoices = [0, 1, 2].map(() => {
          const oscillator = context.createOscillator();
          oscillator.type = "sawtooth";
          const gain = context.createGain();
          gain.gain.value = 0.25;
          oscillator.connect(gain);
          gain.connect(n.padFilter);
          oscillator.start();
          return { oscillator, gain };
        });
        n.padFilter.connect(n.padEnv);
        n.padEnv.connect(n.padGain);
        n.padGain.connect(n.master);
      }

      n.master.connect(n.limiter);
      n.limiter.connect(n.capture);
      // Wet monitoring is opt-in: through speakers the delay and reverb feed
      // the microphone and can run away. Headphones only.
      n.monitorGain.gain.value = monitor ? 1 : 0;
      n.limiter.connect(n.monitorGain);
      n.monitorGain.connect(context.destination);

      this.applyPreset();
      if (context.state === "suspended") await context.resume();
      this.envelope = 0;
      this.followerSamples = new Float32Array(n.follower.fftSize);
      if (preset.wah) this.followerTimer = setInterval(() => this.followEnvelope(), 25);
      this.stream = n.capture.stream;
    }

    applyPreset() {
      const n = this.nodes;
      const preset = this.preset;
      n.input.gain.value = 1;
      n.master.gain.value = 0.9;

      const drive = preset.drive;
      n.driveShaper.curve = makeDriveCurve(drive ? drive.amount : 0);
      n.driveWet.gain.value = drive ? drive.mix : 0;
      n.driveDry.gain.value = drive ? 1 - drive.mix : 1;

      const wah = preset.wah;
      n.wahFilter.Q.value = wah ? wah.q : 1;
      n.wahFilter.frequency.value = 800;
      n.wahWet.gain.value = wah ? 1 : 0;
      n.wahDry.gain.value = wah ? 0 : 1;

      const delay = preset.delay;
      n.delay.delayTime.value = delay ? delay.time : 0.3;
      n.delayFeedback.gain.value = delay ? delay.feedback : 0;
      n.delayWet.gain.value = delay ? delay.mix : 0;
      n.delayDry.gain.value = 1;

      const reverb = preset.reverb;
      n.convolver.buffer = makeImpulse(this.context, reverb ? reverb.size : 1);
      n.reverbWet.gain.value = reverb ? reverb.mix * 1.6 : 0;
      n.reverbDry.gain.value = 1;

      if (n.padGain) n.padGain.gain.value = preset.pad.volume;

      const pitch = preset.pitch || { mode: "off" };
      n.pitch.port.postMessage({
        mode: pitch.mode || "off",
        keyRoot: this.keyRoot,
        scale: SCALES[pitch.scaleName || "major"],
        voicing: pitch.voicing || "third",
        strength: pitch.strength ?? 0.9,
        glideMs: pitch.glideMs ?? 40,
        harmMix: pitch.harmMix ?? 0.7,
      });
    }

    // Envelope follower drives the auto-wah center frequency from input
    // level. A timer (not requestAnimationFrame) keeps it moving while the
    // tab is in the background during a take.
    followEnvelope() {
      const n = this.nodes;
      if (!this.context || !n.follower || !this.preset?.wah) return;
      const samples = this.followerSamples;
      n.follower.getFloatTimeDomainData(samples);
      let peak = 0;
      for (let index = 0; index < samples.length; index += 1) {
        const magnitude = Math.abs(samples[index]);
        if (magnitude > peak) peak = magnitude;
      }
      this.envelope = Math.max(peak, this.envelope * 0.94);
      const sensitivity = this.preset.wah.sensitivity;
      const frequency = 350 + Math.min(1, this.envelope * (1 + sensitivity * 6)) * 1800;
      n.wahFilter.frequency.setTargetAtTime(frequency, this.context.currentTime, 0.03);
    }

    handlePitch(data) {
      if (!this.context || !this.nodes.padEnv) return;
      const now = this.context.currentTime;
      const pad = this.padState;
      if (data?.f0 && data.rms > 0.01) {
        const rounded = Math.round(data.midi);
        if (rounded === pad.lastMidi) pad.stable += 1;
        else {
          pad.lastMidi = rounded;
          pad.stable = 0;
        }
        pad.silent = 0;
        if (pad.stable === 2) this.setPadChord(rounded, now);
        this.nodes.padEnv.gain.setTargetAtTime(0.5, now, 0.1);
      } else {
        pad.silent += 1;
        if (pad.silent > 8) this.nodes.padEnv.gain.setTargetAtTime(0, now, 0.4);
      }
    }

    setPadChord(rootMidi, when) {
      const scale = SCALES[(this.preset.pitch && this.preset.pitch.scaleName) || "major"];
      const pitchClass = ((rootMidi - this.keyRoot) % 12 + 12) % 12;
      let degree = 0;
      let bestDistance = 99;
      scale.forEach((step, index) => {
        const distance = Math.min(((pitchClass - step) % 12 + 12) % 12, ((step - pitchClass) % 12 + 12) % 12);
        if (distance < bestDistance) {
          bestDistance = distance;
          degree = index;
        }
      });
      const stepsUp = (count) => {
        const length = scale.length;
        const index = degree + count;
        const octave = Math.floor(index / length);
        return scale[index % length] + 12 * octave - scale[degree];
      };
      const base = rootMidi - 12;
      [0, stepsUp(2), stepsUp(4)].forEach((semitones, index) => {
        const frequency = 440 * Math.pow(2, (base + semitones - 69) / 12);
        this.nodes.padVoices[index].oscillator.frequency.setTargetAtTime(frequency, when, 0.03);
      });
    }

    setMonitor(enabled) {
      if (this.nodes.monitorGain && this.context) {
        this.nodes.monitorGain.gain.setTargetAtTime(enabled ? 1 : 0, this.context.currentTime, 0.05);
      }
    }

    // Idempotent teardown: stop timers and sources, disconnect every node so
    // nothing keeps the input stream alive, then close the context.
    async stop() {
      clearInterval(this.followerTimer);
      this.followerTimer = null;
      const context = this.context;
      const nodes = this.nodes;
      this.context = null;
      this.nodes = {};
      this.stream = null;
      if (nodes.pitch?.port) nodes.pitch.port.onmessage = null;
      (nodes.padVoices || []).forEach(({ oscillator, gain }) => {
        try { oscillator.stop(); } catch {}
        try { oscillator.disconnect(); } catch {}
        try { gain.disconnect(); } catch {}
      });
      Object.entries(nodes).forEach(([name, node]) => {
        if (name === "padVoices") return;
        try { node.disconnect(); } catch {}
      });
      nodes.capture?.stream?.getTracks?.().forEach((track) => track.stop());
      await context?.close().catch(() => {});
    }
  }

  // --- UI wiring + the surface recording.js consumes ---

  let activeChain = null;

  function readStorage(key) {
    try { return globalThis.localStorage?.getItem(key) ?? null; } catch { return null; }
  }

  function writeStorage(key, value) {
    try { globalThis.localStorage?.setItem(key, value); } catch {}
  }

  function enabled() {
    return Boolean($("#fx-enabled")?.checked);
  }

  function presetName() {
    const value = $("#fx-preset")?.value || "";
    return PRESETS[value] ? value : DEFAULT_PRESET;
  }

  function keyRoot() {
    const value = Number($("#fx-key")?.value || 0);
    return Number.isInteger(value) && value >= 0 && value < 12 ? value : 0;
  }

  function monitorRequested() {
    return Boolean($("#fx-monitor")?.checked);
  }

  async function start(inputStream) {
    await stop();
    const chain = new FXChain();
    activeChain = chain;
    const preset = presetName();
    try {
      const stream = await chain.start(inputStream, preset, keyRoot(), monitorRequested());
      if (activeChain !== chain) throw new Error("Live effects were stopped while starting");
      return { stream, preset };
    } catch (error) {
      if (activeChain === chain) activeChain = null;
      await chain.stop();
      throw error;
    }
  }

  async function stop() {
    const chain = activeChain;
    activeChain = null;
    await chain?.stop();
  }

  function setStatus(message) {
    const status = $("#fx-status");
    if (status) status.textContent = message;
  }

  function syncControls() {
    const on = enabled();
    globalThis.document?.querySelectorAll("[data-fx-option]").forEach((field) => { field.hidden = !on; });
    setStatus(on
      ? `Takes save a second “FX mix” WAV (${presetLabel(presetName())}, first hour) alongside the untouched dry master.`
      : "Off — takes record the dry lossless master only.");
  }

  function populateControls() {
    const presetSelect = $("#fx-preset");
    const keySelect = $("#fx-key");
    const enabledBox = $("#fx-enabled");
    const monitorBox = $("#fx-monitor");
    if (!presetSelect || !keySelect || !enabledBox || !monitorBox) return;
    Object.entries(PRESETS).forEach(([value, preset]) => presetSelect.add(new Option(preset.label, value)));
    NOTE_NAMES.forEach((name, index) => keySelect.add(new Option(`Key of ${name}`, String(index))));
    enabledBox.checked = readStorage(FX_ENABLED_STORAGE_KEY) === "1";
    monitorBox.checked = readStorage(FX_MONITOR_STORAGE_KEY) === "1";
    const storedPreset = readStorage(FX_PRESET_STORAGE_KEY);
    presetSelect.value = storedPreset && PRESETS[storedPreset] ? storedPreset : DEFAULT_PRESET;
    const storedKey = Number(readStorage(FX_KEY_STORAGE_KEY));
    if (Number.isInteger(storedKey) && storedKey >= 0 && storedKey < 12) keySelect.value = String(storedKey);

    enabledBox.addEventListener("change", () => {
      writeStorage(FX_ENABLED_STORAGE_KEY, enabledBox.checked ? "1" : "0");
      syncControls();
    });
    presetSelect.addEventListener("change", () => {
      writeStorage(FX_PRESET_STORAGE_KEY, presetSelect.value);
      syncControls();
    });
    keySelect.addEventListener("change", () => {
      writeStorage(FX_KEY_STORAGE_KEY, keySelect.value);
    });
    monitorBox.addEventListener("change", () => {
      writeStorage(FX_MONITOR_STORAGE_KEY, monitorBox.checked ? "1" : "0");
      activeChain?.setMonitor(monitorBox.checked);
    });
    syncControls();
  }

  globalThis.JazzFX = { enabled, presetName, presetLabel, start, stop, presets: PRESETS, FXChain };
  if (hasDocument) populateControls();
})();
