// Generate clicks on the audio sample clock, independent of UI timer throttling.
class JazzMetronomeProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.running = false;
    this.bpm = 120;
    this.beats = 4;
    this.volume = 0.35;
    this.remaining = 0;
    this.beat = 0;
    this.clickAge = Infinity;
    this.frequency = 900;
    this.port.onmessage = ({ data }) => {
      if (Number.isFinite(data.bpm)) {
        const bpm = Math.max(30, Math.min(300, data.bpm));
        this.remaining *= this.bpm / bpm;
        this.bpm = bpm;
      }
      if (Number.isFinite(data.beats)) {
        this.beats = Math.max(1, Math.min(7, Math.round(data.beats)));
        this.beat %= this.beats;
      }
      if (Number.isFinite(data.volume)) this.volume = Math.max(0, Math.min(1, data.volume));
      if (typeof data.running === "boolean") {
        this.running = data.running;
        this.remaining = 0;
        this.beat = 0;
        this.clickAge = Infinity;
      }
    };
  }

  process(inputs, outputs) {
    const channel = outputs[0]?.[0];
    if (!channel) return true;
    if (!this.running) { channel.fill(0); return true; }
    for (let frame = 0; frame < channel.length; frame += 1) {
      if (this.remaining <= 0) {
        const accent = this.beat === 0 && this.beats > 1;
        this.frequency = accent ? 1400 : 900;
        this.clickAge = 0;
        this.port.postMessage({ beat: this.beat, accent });
        this.beat = (this.beat + 1) % this.beats;
        this.remaining += sampleRate * 60 / this.bpm;
      }
      const t = this.clickAge / sampleRate;
      channel[frame] = t < 0.035
        ? Math.sin(2 * Math.PI * this.frequency * t) * (1 - Math.exp(-3000 * t)) * Math.exp(-180 * t) * this.volume * 0.7
        : 0;
      this.clickAge += 1;
      this.remaining -= 1;
    }
    return true;
  }
}
registerProcessor("jazz-metronome", JazzMetronomeProcessor);
