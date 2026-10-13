class StudyCapture extends AudioWorkletProcessor {
  constructor() {
    super();
    this.trial = null;
    this.samples = [];
    this.port.onmessage = ({ data }) => {
      if (data.type === "start") {
        this.trial = data;
        this.start = Math.round(data.start * sampleRate);
        this.end = Math.round(data.end * sampleRate);
        this.samples = [];
        this.first = 0;
      }
      if (data.type === "stop" && this.trial) {
        this.flush();
        this.port.postMessage({
          type: "done",
          id: this.trial.id,
          frame: currentFrame,
        });
        this.trial = null;
      }
    };
  }
  flush() {
    if (!this.samples.length) return;
    const samples = new Float32Array(this.samples);
    this.port.postMessage(
      { type: "samples", id: this.trial.id, frame: this.first, samples },
      [samples.buffer],
    );
    this.samples = [];
  }
  process(inputs, outputs) {
    outputs.forEach((o) => o.forEach((c) => c.fill(0)));
    if (!this.trial) return true;
    const input = inputs[0]?.[0];
    for (let i = 0; i < 128; i++) {
      const frame = currentFrame + i;
      if (frame < this.start) continue;
      if (frame >= this.end) {
        this.flush();
        this.port.postMessage({
          type: "done",
          id: this.trial.id,
          frame: this.end,
        });
        this.trial = null;
        break;
      }
      if (!this.samples.length) this.first = frame;
      this.samples.push(input?.[i] || 0);
      if (this.samples.length === 4096) this.flush();
    }
    return true;
  }
}
registerProcessor("jazz-study-capture", StudyCapture);
