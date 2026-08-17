(() => {
  "use strict";

  const FX_ENABLED_STORAGE_KEY = "zach-jazz-fx-enabled-v1";
  const FX_PRESET_STORAGE_KEY = "zach-jazz-fx-preset-v1";
  const FX_KEY_STORAGE_KEY = "zach-jazz-fx-key-v1";
  const FX_MONITOR_STORAGE_KEY = "zach-jazz-fx-monitor-v1";
  const $ = (selector, root = document) => root.querySelector(selector);

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
      this.monitor = false;
      this.followerFrame = null;
      this.padState = { lastMidi: 0, stable: 0, silent: 0 };
    }

    async start(inputStream, presetName, keyRoot, monitor) {
      const preset = PRESETS[presetName];
      if (!preset) throw new Error("Unknown FX preset");
      this.preset = preset;
      this.keyRoot = keyRoot;
      this.monitor = monitor;
      const AudioContext = globalThis.AudioContext || globalThis.webkitAudioContext;
      const context = new AudioContext({ sampleRate: 48000, latencyHint: "interactive" });
      this.context = context;
      const workletURL = new URL("../assets/jazz/pitch-worklet.js", location.href);
      await context.audioWorklet.addModule(workletURL.href);

      const n = this.nodes;
      n.source = context.createMediaStreamSource(inputStream);
      n.input = context.createGain();

      n.pitch = new AudioWorkletNode(context, "pitch-engine", {
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

      n.padGain = context.createGain();
      n.padEnv = context.createGain();
      n.padEnv.gain.value = 0;
      n.padFilter = context.createBiquadFilter();
      n.padFilter.type = "lowpass";
      n.padFilter.frequency.value = 900;
      n.padOscillators = [0, 1, 2].map(() => {
        const oscillator = context.createOscillator();
        oscillator.type = "sawtooth";
        const gain = context.createGain();
        gain.gain.value = 0.25;
        oscillator.connect(gain);
        gain.connect(n.padFilter);
        oscillator.start();
        return oscillator;
      });
      n.padFilter.connect(n.padEnv);
      n.padEnv.connect(n.padGain);

      n.master = context.createGain();
      n.monitorGain = context.createGain();
      n.capture = context.createMediaStreamDestination();

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
      n.padGain.connect(n.master);
      n.master.connect(n.capture);
      n.monitorGain.gain.value = monitor ? 1 : 0;
      n.master.connect(n.monitorGain);
      n.monitorGain.connect(context.destination);

      this.applyPreset();
      if (context.state === "suspended") await context.resume();
      this.runFollower();
      this.stream = n.capture.stream;
      return this.stream;
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

      n.padGain.gain.value = preset.pad ? preset.pad.volume : 0;

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

    // Envelope follower drives the auto-wah center frequency from input level.
    runFollower() {
      const n = this.nodes;
      const samples = new Float32Array(n.follower.fftSize);
      let envelope = 0;
      const tick = () => {
        if (!this.context) return;
        if (this.preset.wah) {
          n.follower.getFloatTimeDomainData(samples);
          let peak = 0;
          for (let index = 0; index < samples.length; index += 1) {
            const magnitude = Math.abs(samples[index]);
            if (magnitude > peak) peak = magnitude;
          }
          envelope = Math.max(peak, envelope * 0.94);
          const sensitivity = this.preset.wah.sensitivity;
          const frequency = 350 + Math.min(1, envelope * (1 + sensitivity * 6)) * 1800;
          n.wahFilter.frequency.setTargetAtTime(frequency, this.context.currentTime, 0.03);
        }
        this.followerFrame = requestAnimationFrame(tick);
      };
      tick();
    }

    handlePitch(data) {
      if (!this.context || !this.preset.pad) return;
      const now = this.context.currentTime;
      const pad = this.padState;
      if (data.f0 && data.rms > 0.01) {
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
        this.nodes.padOscillators[index].frequency.setTargetAtTime(frequency, when, 0.03);
      });
    }

    setMonitor(enabled) {
      this.monitor = enabled;
      if (this.nodes.monitorGain && this.context) {
        this.nodes.monitorGain.gain.setTargetAtTime(enabled ? 1 : 0, this.context.currentTime, 0.05);
      }
    }

    async stop() {
      if (this.followerFrame) cancelAnimationFrame(this.followerFrame);
      this.followerFrame = null;
      if (this.nodes.pitch?.port) this.nodes.pitch.port.onmessage = null;
      await this.context?.close().catch(() => {});
      this.context = null;
      this.nodes = {};
      this.stream = null;
    }
  }

  // --- UI wiring + the surface recording.js consumes ---

  let activeChain = null;

  function enabled() {
    return Boolean($("#fx-enabled")?.checked);
  }

  function presetName() {
    return $("#fx-preset")?.value || "big-hall";
  }

  function keyRoot() {
    return Number($("#fx-key")?.value || 0);
  }

  function monitorRequested() {
    return Boolean($("#fx-monitor")?.checked);
  }

  async function start(inputStream) {
    if (activeChain) await activeChain.stop();
    activeChain = new FXChain();
    const stream = await activeChain.start(inputStream, presetName(), keyRoot(), monitorRequested());
    return { stream, preset: presetName() };
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
    document.querySelectorAll("[data-fx-option]").forEach((field) => { field.hidden = !on; });
    setStatus(on
      ? `Takes will save a second “FX mix” WAV (${PRESETS[presetName()].label}) alongside the dry master.`
      : "Off — takes record the dry lossless master only.");
  }

  function populateControls() {
    const presetSelect = $("#fx-preset");
    const keySelect = $("#fx-key");
    if (!presetSelect || !keySelect) return;
    Object.entries(PRESETS).forEach(([value, preset]) => presetSelect.add(new Option(preset.label, value)));
    NOTE_NAMES.forEach((name, index) => keySelect.add(new Option(`Key of ${name}`, String(index))));
    const enabledBox = $("#fx-enabled");
    const monitorBox = $("#fx-monitor");
    enabledBox.checked = localStorage.getItem(FX_ENABLED_STORAGE_KEY) === "1";
    monitorBox.checked = localStorage.getItem(FX_MONITOR_STORAGE_KEY) !== "0";
    const storedPreset = localStorage.getItem(FX_PRESET_STORAGE_KEY);
    if (storedPreset && PRESETS[storedPreset]) presetSelect.value = storedPreset;
    const storedKey = localStorage.getItem(FX_KEY_STORAGE_KEY);
    if (storedKey !== null && keySelect.querySelector(`option[value="${storedKey}"]`)) keySelect.value = storedKey;

    enabledBox.addEventListener("change", () => {
      localStorage.setItem(FX_ENABLED_STORAGE_KEY, enabledBox.checked ? "1" : "0");
      syncControls();
    });
    presetSelect.addEventListener("change", () => {
      localStorage.setItem(FX_PRESET_STORAGE_KEY, presetSelect.value);
      syncControls();
    });
    keySelect.addEventListener("change", () => {
      localStorage.setItem(FX_KEY_STORAGE_KEY, keySelect.value);
    });
    monitorBox.addEventListener("change", () => {
      localStorage.setItem(FX_MONITOR_STORAGE_KEY, monitorBox.checked ? "1" : "0");
      activeChain?.setMonitor(monitorBox.checked);
    });
    syncControls();
  }

  globalThis.JazzFX = { enabled, presetName, start, stop, presets: PRESETS };
  populateControls();
})();
