(() => {
  "use strict";

  const MAX_TAKE_DURATION_MS = 4 * 60 * 60 * 1000;

  function shouldAutoFinish(elapsedMS) {
    return Number(elapsedMS) >= MAX_TAKE_DURATION_MS;
  }

  globalThis.JazzRecordingPolicy = { MAX_TAKE_DURATION_MS, shouldAutoFinish };
})();
