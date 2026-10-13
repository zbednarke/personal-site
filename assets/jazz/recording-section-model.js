(function attachRecordingSectionModel(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  if (root) root.JazzRecordingSectionModel = api;
})(typeof globalThis !== "undefined" ? globalThis : this, function buildRecordingSectionModel() {
  "use strict";

  function applyFreshRecording(recording, recordings) {
    const fresh = (recordings || []).find((item) => item.id === recording?.id);
    if (!fresh) return false;
    Object.assign(recording, fresh);
    return true;
  }

  function playbackIsActive(player) {
    return Boolean(player && !player.paused && !player.ended);
  }

  function refreshLabelsWithoutInterrupting(media, render) {
    const currentTime = Number(media?.currentTime || 0);
    const wasPaused = media ? Boolean(media.paused) : true;
    render();
    return { currentTime, wasPaused };
  }

  function reparentMedia(media, mediaNode, host, wrapper) {
    const currentTime = Number(media?.currentTime || 0);
    const wasPlaying = Boolean(media && !media.paused && !media.ended);
    wrapper.append(mediaNode);
    host.append(wrapper);
    if (media && Number.isFinite(currentTime)) media.currentTime = currentTime;
    if (wasPlaying && media.paused) media.play?.().catch?.(() => {});
    return { currentTime, wasPlaying };
  }

  return { applyFreshRecording, playbackIsActive, refreshLabelsWithoutInterrupting, reparentMedia };
});
