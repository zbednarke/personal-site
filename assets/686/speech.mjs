// Keep protocol parsing and PCM scheduling separate from the page and renderer.
export async function readEvents(response, onEvent) {
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let pending = "";
  let done = false;
  const consume = (line) => {
    if (!line.trim()) return;
    if (done) throw new Error("Unexpected data after conversation finished.");
    const event = JSON.parse(line);
    if (event.error) throw new Error(event.error);
    if (event.done) done = true;
    else if (typeof event.text === "string") onEvent(event.text);
    else throw new Error("Invalid conversation stream.");
  };
  try {
    while (true) {
      const chunk = await reader.read();
      pending += decoder.decode(chunk.value, { stream: !chunk.done });
      let newline;
      while ((newline = pending.indexOf("\n")) !== -1) {
        consume(pending.slice(0, newline));
        pending = pending.slice(newline + 1);
      }
      if (pending.length > 65536) throw new Error("Conversation event is too large.");
      if (chunk.done) break;
    }
    if (pending.trim()) consume(pending);
    if (!done) throw new Error("Conversation stream interrupted.");
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

export function sentenceBuffer(emit) {
  let pending = "";
  return (text = "", final = false) => {
    pending += text;
    while (pending.trim()) {
      // Wait for whitespace after punctuation, so chunk boundaries and decimal
      // points do not accidentally split a sentence. Cap unpunctuated passages.
      const boundary = /[.!?。！？](?:["”']?)(?=\s)/u.exec(pending);
      let end = boundary ? boundary.index + boundary[0].length : 0;
      if ((!end || end > 240) && pending.length > 240) {
        end = pending.lastIndexOf(" ", 240);
        if (end < 1) end = 240;
        if (/[\uD800-\uDBFF]/.test(pending[end - 1])) end--;
      }
      if (!end && final) end = pending.length;
      if (!end) break;
      const sentence = pending.slice(0, end).trim();
      pending = pending.slice(end);
      if (sentence) emit(sentence);
    }
  };
}

// ElevenLabs pcm_24000 is signed 16-bit little-endian, mono, with no WAV header.
// Schedule against the AudioContext clock, preserving partial samples across
// network chunks and keeping at most one second of audio queued ahead.
export class PCMPlayer {
  constructor(context, { onStart = () => {}, onEnd = () => {}, onLevel = () => {} } = {}) {
    this.context = context;
    this.onStart = onStart;
    this.onEnd = onEnd;
    this.onLevel = onLevel;
    this.analyser = context.createAnalyser();
    this.analyser.fftSize = 256;
    this.analyser.connect(context.destination);
    this.sources = new Set();
    this.nextTime = 0;
    this.started = false;
    this.closed = false;
    this.finished = false;
    this.frame = 0;
  }

  async append(response, signal) {
    const reader = response.body.getReader();
    let pending = new Uint8Array(0);
    let bytes = 0;
    try {
      while (true) {
        signal.throwIfAborted();
        const { value, done } = await reader.read();
        if (value) {
          bytes += value.length;
          if (bytes > 4 * 1024 * 1024) throw new Error("Speech audio is too large.");
          const joined = new Uint8Array(pending.length + value.length);
          joined.set(pending);
          joined.set(value, pending.length);
          pending = joined;
        }
        while (pending.length >= 4800 || (done && pending.length >= 2)) {
          while (this.nextTime - this.context.currentTime > 1) {
            await new Promise((resolve) => setTimeout(resolve, 20));
            signal.throwIfAborted();
          }
          signal.throwIfAborted();
          const count = Math.min(2400, Math.floor(pending.length / 2));
          this.schedule(pending.subarray(0, count * 2));
          pending = pending.subarray(count * 2);
        }
        if (done) break;
      }
      if (!bytes || pending.length) throw new Error("Speech audio is incomplete.");
    } finally {
      await reader.cancel().catch(() => {});
      reader.releaseLock();
    }
  }

  schedule(bytes) {
    if (this.closed) throw new Error("Speech playback stopped.");
    const buffer = this.context.createBuffer(1, bytes.length / 2, 24000);
    const samples = buffer.getChannelData(0);
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    for (let i = 0; i < samples.length; i++) samples[i] = view.getInt16(i * 2, true) / 32768;
    const source = this.context.createBufferSource();
    source.buffer = buffer;
    source.connect(this.analyser);
    this.sources.add(source);
    const at = Math.max(this.context.currentTime + 0.03, this.nextTime);
    this.nextTime = at + buffer.duration;
    source.onended = () => {
      source.disconnect();
      this.sources.delete(source);
      if (this.finished && !this.sources.size && !this.closed) this.complete();
    };
    source.start(at);
    if (!this.started) {
      this.started = true;
      const data = new Uint8Array(this.analyser.frequencyBinCount);
      let announced = false;
      const tick = () => {
        if (this.closed) return;
        const outputTime = this.context.getOutputTimestamp?.().contextTime ?? this.context.currentTime;
        if (!announced && this.context.state === "running" && outputTime >= at) {
          announced = true;
          this.onStart();
        }
        this.analyser.getByteFrequencyData(data);
        this.onLevel(Math.min(1, data.reduce((sum, n) => sum + n, 0) / data.length / 255 * 2.8));
        this.frame = requestAnimationFrame(tick);
      };
      this.frame = requestAnimationFrame(tick);
    }
  }

  finish() {
    this.finished = true;
    if (!this.sources.size && !this.closed) this.complete();
  }

  complete() {
    this.stop();
    this.onEnd();
  }

  stop() {
    this.closed = true;
    cancelAnimationFrame(this.frame);
    for (const source of this.sources) {
      source.onended = null;
      source.stop();
      source.disconnect();
    }
    this.sources.clear();
    this.analyser.disconnect();
    this.onLevel(0);
  }
}
