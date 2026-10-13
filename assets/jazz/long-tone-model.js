(() => {
  "use strict";
  const names = ["C", "C♯", "D", "E♭", "E", "F", "F♯", "G", "A♭", "A", "B♭", "B"];
  // Observations describe source time. Never interpolate across unobserved audio.
  function frameAt(frames, timeMs, maxAgeMs = 40) {
    if (!Array.isArray(frames) || !Number.isFinite(timeMs)) return null;
    let low = 0, high = frames.length - 1, found = -1;
    while (low <= high) {
      const mid = (low + high) >> 1;
      if (frames[mid].tMs <= timeMs) { found = mid; low = mid + 1; } else high = mid - 1;
    }
    const frame = frames[found];
    return frame && timeMs - frame.tMs < maxAgeMs ? frame : null;
  }
  function reading(frame, written = false) {
    if (!frame) return { note: "—", cents: null, holdMs: 0, state: "missing", complete: false };
    const state = frame.state;
    const holdMs = Math.max(0, Number(frame.holdMs) || 0);
    const visible = ["holding", "completed", "acquiring"].includes(state) && Number(frame.hz) > 0;
    const soundingMidi = Number.isFinite(frame.noteMidi) ? Math.round(frame.noteMidi) : Math.round(69 + 12 * Math.log2(frame.hz / 440));
    const displayMidi = soundingMidi + (written ? 2 : 0);
    return {
      note: visible ? `${names[((displayMidi % 12) + 12) % 12]}${Math.floor(displayMidi / 12) - 1}` : "—",
      cents: visible ? 1200 * Math.log2(frame.hz / (440 * 2 ** ((soundingMidi - 69) / 12))) : null,
      holdMs: ["holding", "completed", "uncertain"].includes(state) ? holdMs : 0,
      state,
      complete: state === "completed" && holdMs >= 30000,
    };
  }
  const api = { frameAt, reading };
  globalThis.JazzLongToneModel = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})();
