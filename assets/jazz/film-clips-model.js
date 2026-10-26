(() => {
  "use strict";

  function clock(milliseconds) {
    const seconds = Math.max(0, Math.floor(Number(milliseconds || 0) / 1000));
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
  }

  // filmClipRows turns a finished Magic Film's saved project (its clips in film
  // order, each a time range inside a source take) into list rows. describe
  // resolves a recording id to its current section and take label, or null
  // when that take is no longer on the selected Studio day.
  function filmClipRows(project, describe) {
    const clips = Array.isArray(project?.clips) ? project.clips : [];
    return clips
      .filter((clip) => clip && typeof clip.recordingId === "string" && clip.recordingId
        && Number.isFinite(Number(clip.startMs)) && Number(clip.endMs) > Number(clip.startMs))
      .map((clip, index) => {
        const source = typeof describe === "function" ? describe(clip.recordingId) : null;
        const startMs = Math.max(0, Number(clip.startMs));
        return {
          order: index + 1,
          recordingId: clip.recordingId,
          startMs,
          endMs: Number(clip.endMs),
          title: String(clip.title || "").trim() || `Clip ${index + 1}`,
          source: source ? [source.section, source.take].filter(Boolean).join(" · ") : "",
          range: `${clock(startMs)}–${clock(clip.endMs)}`,
          playable: Boolean(source),
        };
      });
  }

  const api = { clock, filmClipRows };
  globalThis.JazzFilmClipsModel = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})();
