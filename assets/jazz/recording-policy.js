(() => {
  "use strict";

  const MAX_TAKE_DURATION_MS = 4 * 60 * 60 * 1000;
  const VIDEO_DATA_FLUSH_MS = 30000;
  // The processed FX mix is a second in-memory PCM capture. Cap it so a long
  // take cannot exhaust the tab and take the dry master down with it.
  const FX_MAX_DURATION_MS = 60 * 60 * 1000;
  const VIDEO_TYPES = [
    "video/mp4;codecs=avc1.42E01E,mp4a.40.2",
    "video/mp4",
    "video/webm;codecs=vp9,opus",
    "video/webm;codecs=vp8,opus",
    "video/webm",
  ];

  function shouldAutoFinish(elapsedMS) {
    return Number(elapsedMS) >= MAX_TAKE_DURATION_MS;
  }

  function shouldFinishFx(elapsedMS) {
    return Number(elapsedMS) >= FX_MAX_DURATION_MS;
  }

  function preferredVideoType(isTypeSupported) {
    if (typeof isTypeSupported !== "function") return "";
    return VIDEO_TYPES.find((type) => isTypeSupported(type)) || "";
  }

  function supportsChunkFlush(mimeType) {
    return String(mimeType || "").toLowerCase().startsWith("video/mp4");
  }

  globalThis.JazzRecordingPolicy = { MAX_TAKE_DURATION_MS, VIDEO_DATA_FLUSH_MS, FX_MAX_DURATION_MS, preferredVideoType, shouldAutoFinish, shouldFinishFx, supportsChunkFlush };
})();
