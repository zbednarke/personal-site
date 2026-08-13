(() => {
  "use strict";

  function durationSeconds(milliseconds) {
    const value = Number(milliseconds || 0) / 1000;
    return Number.isFinite(value) && value > 0 ? value : 0;
  }

  function formatPlaybackTime(seconds) {
    const wholeSeconds = Math.max(0, Math.floor(Number(seconds) || 0));
    const minutes = Math.floor(wholeSeconds / 60);
    const remainder = wholeSeconds % 60;
    return `${String(minutes).padStart(2, "0")}:${String(remainder).padStart(2, "0")}`;
  }

  function clampSeekTime(seconds, duration) {
    const limit = Math.max(0, Number(duration) || 0);
    const value = Number(seconds);
    return Math.min(limit, Math.max(0, Number.isFinite(value) ? value : 0));
  }

  function createVideoPlayer(video, options = {}) {
    const duration = durationSeconds(options.expectedDurationMS);
    const document = video.ownerDocument;
    const shell = document.createElement("div");
    shell.className = "video-player-shell";
    const controls = document.createElement("div");
    controls.className = "video-player-controls";
    controls.innerHTML = `
      <button type="button" data-video-toggle aria-label="Play video">Play</button>
      <span data-video-time>${formatPlaybackTime(0)} / ${formatPlaybackTime(duration)}</span>
      <input data-video-seek type="range" min="0" max="${duration}" step="0.05" value="0" aria-label="Video position" ${duration ? "" : "disabled"}>
      <button type="button" data-video-mute aria-label="Mute video">Mute</button>
      <button type="button" data-video-fullscreen aria-label="View video fullscreen">Full</button>`;
    shell.append(video, controls);

    const toggle = controls.querySelector("[data-video-toggle]");
    const time = controls.querySelector("[data-video-time]");
    const seek = controls.querySelector("[data-video-seek]");
    const mute = controls.querySelector("[data-video-mute]");
    const fullscreen = controls.querySelector("[data-video-fullscreen]");

    const update = () => {
      const current = clampSeekTime(video.currentTime, duration);
      seek.value = String(current);
      time.textContent = `${formatPlaybackTime(current)} / ${formatPlaybackTime(duration)}`;
      toggle.textContent = video.paused || video.ended ? "Play" : "Pause";
      toggle.setAttribute("aria-label", video.paused || video.ended ? "Play video" : "Pause video");
      mute.textContent = video.muted ? "Unmute" : "Mute";
      mute.setAttribute("aria-label", video.muted ? "Unmute video" : "Mute video");
    };
    const togglePlayback = () => {
      if (video.paused || video.ended) video.play().catch(() => {});
      else video.pause();
    };

    toggle.addEventListener("click", togglePlayback);
    video.addEventListener("click", togglePlayback);
    seek.addEventListener("input", () => {
      video.currentTime = clampSeekTime(seek.value, duration);
      update();
    });
    mute.addEventListener("click", () => {
      video.muted = !video.muted;
      update();
    });
    fullscreen.addEventListener("click", () => {
      if (shell.requestFullscreen) shell.requestFullscreen().catch(() => {});
      else video.webkitEnterFullscreen?.();
    });
    ["play", "pause", "timeupdate", "seeking", "seeked", "ended", "volumechange"].forEach((eventName) => video.addEventListener(eventName, update));
    update();
    return shell;
  }

  const api = { clampSeekTime, createVideoPlayer, durationSeconds, formatPlaybackTime };
  globalThis.JazzMediaPlayback = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})();
