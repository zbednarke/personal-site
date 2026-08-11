(() => {
  "use strict";

  function snapshot(timer, sessionID) {
    return {
      sessionID,
      elapsedMs: Number(timer?.elapsedMs || 0),
      completed: Boolean(timer?.completed),
      completedAt: String(timer?.completedAt || ""),
    };
  }

  function restoreCancelled(timer, saved, sessionID) {
    if (!timer || !saved || saved.sessionID !== sessionID) return false;
    timer.elapsedMs = Math.max(0, Number(saved.elapsedMs || 0));
    timer.running = false;
    timer.startedAt = 0;
    timer.completed = Boolean(saved.completed);
    timer.completedAt = String(saved.completedAt || "");
    return true;
  }

  globalThis.JazzPracticeTimerPolicy = { snapshot, restoreCancelled };
})();
