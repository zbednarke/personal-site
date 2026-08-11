(() => {
  "use strict";

  class LosslessRecorder {
    constructor() {
      this.context = null;
      this.source = null;
      this.node = null;
      this.silentGain = null;
      this.chunks = [];
      this.frameCount = 0;
      this.flushed = null;
    }

    async start(stream) {
      const AudioContext = globalThis.AudioContext || globalThis.webkitAudioContext;
      if (!AudioContext || !globalThis.AudioWorkletNode) throw new Error("Lossless recording is not supported in this browser");
      this.context = new AudioContext({ sampleRate: 48000, latencyHint: "interactive" });
      const workletURL = new URL("../assets/jazz/pcm-recorder-worklet.js", location.href);
      await this.context.audioWorklet.addModule(workletURL.href);
      this.source = this.context.createMediaStreamSource(stream);
      this.node = new AudioWorkletNode(this.context, "jazz-pcm-recorder", {
        numberOfInputs: 1,
        numberOfOutputs: 1,
        outputChannelCount: [1],
      });
      this.silentGain = this.context.createGain();
      this.silentGain.gain.value = 0;
      this.node.port.onmessage = (event) => {
        if (event.data?.type === "samples") {
          const samples = event.data.samples;
          this.chunks.push(samples);
          this.frameCount += samples.length;
        } else if (event.data?.type === "flushed") {
          this.flushed?.();
        }
      };
      this.source.connect(this.node);
      this.node.connect(this.silentGain).connect(this.context.destination);
      if (this.context.state === "suspended") await this.context.resume();
    }

    async stop() {
      if (!this.node || !this.context) throw new Error("No lossless recording is active");
      await new Promise((resolve) => {
        this.flushed = resolve;
        this.node.port.postMessage({ type: "flush" });
      });
      const sampleRate = this.context.sampleRate;
      this.source.disconnect();
      this.node.disconnect();
      this.silentGain.disconnect();
      await this.context.close();
      const blob = encodeWAV24(this.chunks, this.frameCount, sampleRate);
      return {
        blob,
        sampleRate,
        channels: 1,
        bitDepth: 24,
        durationMS: Math.round((this.frameCount / sampleRate) * 1000),
      };
    }

    async cancel() {
      if (!this.context) return;
      if (this.node?.port) this.node.port.onmessage = null;
      try { this.source?.disconnect(); } catch {}
      try { this.node?.disconnect(); } catch {}
      try { this.silentGain?.disconnect(); } catch {}
      await this.context.close().catch(() => {});
      this.context = null;
      this.source = null;
      this.node = null;
      this.silentGain = null;
      this.chunks = [];
      this.frameCount = 0;
      this.flushed = null;
    }
  }

  function encodeWAV24(chunks, frameCount, sampleRate) {
    const bytesPerSample = 3;
    const dataSize = frameCount * bytesPerSample;
    const buffer = new ArrayBuffer(44 + dataSize);
    const view = new DataView(buffer);
    writeASCII(view, 0, "RIFF");
    view.setUint32(4, 36 + dataSize, true);
    writeASCII(view, 8, "WAVE");
    writeASCII(view, 12, "fmt ");
    view.setUint32(16, 16, true);
    view.setUint16(20, 1, true);
    view.setUint16(22, 1, true);
    view.setUint32(24, sampleRate, true);
    view.setUint32(28, sampleRate * bytesPerSample, true);
    view.setUint16(32, bytesPerSample, true);
    view.setUint16(34, 24, true);
    writeASCII(view, 36, "data");
    view.setUint32(40, dataSize, true);

    let offset = 44;
    chunks.forEach((chunk) => {
      for (let index = 0; index < chunk.length; index += 1) {
        const sample = Math.max(-1, Math.min(1, chunk[index]));
        let value = Math.round(sample < 0 ? sample * 8388608 : sample * 8388607);
        if (value < 0) value += 16777216;
        view.setUint8(offset, value & 0xff);
        view.setUint8(offset + 1, (value >> 8) & 0xff);
        view.setUint8(offset + 2, (value >> 16) & 0xff);
        offset += 3;
      }
    });
    return new Blob([buffer], { type: "audio/wav" });
  }

  function writeASCII(view, offset, text) {
    for (let index = 0; index < text.length; index += 1) view.setUint8(offset + index, text.charCodeAt(index));
  }

  globalThis.JazzLosslessRecorder = LosslessRecorder;
})();
