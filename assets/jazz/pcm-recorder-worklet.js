class PCMRecorderProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.buffer = new Float32Array(4096);
    this.length = 0;
    this.recording = true;
    this.port.onmessage = (event) => {
      if (event.data?.type === "flush") {
        this.flush();
        this.recording = false;
        this.port.postMessage({ type: "flushed" });
      }
    };
  }

  flush() {
    if (!this.length) return;
    const samples = this.buffer.slice(0, this.length);
    this.port.postMessage({ type: "samples", samples }, [samples.buffer]);
    this.buffer = new Float32Array(4096);
    this.length = 0;
  }

  process(inputs, outputs) {
    outputs.forEach((output) => output.forEach((channel) => channel.fill(0)));
    const input = inputs[0]?.[0];
    if (!this.recording || !input) return true;
    let offset = 0;
    while (offset < input.length) {
      const count = Math.min(input.length - offset, this.buffer.length - this.length);
      this.buffer.set(input.subarray(offset, offset + count), this.length);
      this.length += count;
      offset += count;
      if (this.length === this.buffer.length) this.flush();
    }
    return true;
  }
}

registerProcessor("jazz-pcm-recorder", PCMRecorderProcessor);
