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

  function canRenderCurrentFrame(video) {
    return !video.seeking && Number(video.readyState) >= 2;
  }

  function createVideoPlayer(video, options = {}) {
    let duration = durationSeconds(options.expectedDurationMS);
    const document = video.ownerDocument;
    const shell = document.createElement("div");
    shell.className = "video-player-shell";
    const stage = document.createElement("div");
    stage.className = "video-player-stage";
    const buffering = document.createElement("div");
    buffering.className = "video-player-buffering";
    buffering.hidden = true;
    buffering.setAttribute("role", "status");
    buffering.setAttribute("aria-label", "Buffering video");
    buffering.innerHTML = '<span class="video-player-spinner" aria-hidden="true"></span><span>Buffering</span>';
    const controls = document.createElement("div");
    controls.className = "video-player-controls";
    controls.innerHTML = `
      <button type="button" data-video-toggle aria-label="Play video">Paused · Play</button>
      <span data-video-time>${formatPlaybackTime(0)} / ${formatPlaybackTime(duration)}</span>
      <input data-video-seek type="range" min="0" max="${duration}" step="0.05" value="0" aria-label="Video position" ${duration ? "" : "disabled"}>
      <button type="button" data-video-mute aria-label="Mute video">Mute</button>
      <button type="button" data-video-fullscreen aria-label="View video fullscreen">Full</button>`;
    stage.append(video, buffering, controls);
    shell.append(stage);

    const toggle = controls.querySelector("[data-video-toggle]");
    const time = controls.querySelector("[data-video-time]");
    const seek = controls.querySelector("[data-video-seek]");
    const mute = controls.querySelector("[data-video-mute]");
    const fullscreen = controls.querySelector("[data-video-fullscreen]");

    const setBuffering = (active) => {
      const next = Boolean(active);
      buffering.hidden = !next;
      shell.classList.toggle("buffering", next);
      shell.setAttribute("aria-busy", String(next));
    };
    const clearBufferingWhenReady = () => {
      if (canRenderCurrentFrame(video)) setBuffering(false);
    };

    const update = () => {
      const current = clampSeekTime(video.currentTime, duration);
      seek.value = String(current);
      time.textContent = `${formatPlaybackTime(current)} / ${formatPlaybackTime(duration)}`;
      const paused = video.paused || video.ended;
      toggle.textContent = paused ? "Paused · Play" : "Playing · Pause";
      toggle.setAttribute("aria-label", paused ? "Video paused. Play video" : "Video playing. Pause video");
      toggle.setAttribute("aria-pressed", String(!paused));
      mute.textContent = video.muted ? "Unmute" : "Mute";
      mute.setAttribute("aria-label", video.muted ? "Unmute video" : "Mute video");
    };
    const togglePlayback = () => {
      if (video.paused || video.ended) {
        if (Number(video.readyState) < 3) setBuffering(true);
        video.play().catch(() => setBuffering(false));
      } else {
        video.pause();
      }
    };
    const syncNativeDuration = () => {
      const nativeDuration = Number(video.duration);
      if (!Number.isFinite(nativeDuration) || nativeDuration <= 0) return;
      duration = nativeDuration;
      seek.max = String(duration);
      seek.disabled = false;
      update();
    };

    toggle.addEventListener("click", togglePlayback);
    video.addEventListener("click", togglePlayback);
    seek.addEventListener("input", () => {
      setBuffering(true);
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
    ["waiting", "stalled"].forEach((eventName) => video.addEventListener(eventName, () => {
      if (!video.ended) setBuffering(true);
    }));
    video.addEventListener("seeking", () => setBuffering(true));
    ["playing", "canplay", "canplaythrough", "loadeddata", "seeked"].forEach((eventName) => video.addEventListener(eventName, clearBufferingWhenReady));
    video.addEventListener("pause", () => {
      if (!video.seeking) setBuffering(false);
    });
    video.addEventListener("ended", () => setBuffering(false));
    ["loadedmetadata", "durationchange"].forEach((eventName) => video.addEventListener(eventName, syncNativeDuration));
    syncNativeDuration();
    update();
    return shell;
  }

  const api = { canRenderCurrentFrame, clampSeekTime, createVideoPlayer, durationSeconds, formatPlaybackTime };
  globalThis.JazzMediaPlayback = api;
  if (typeof module !== "undefined" && module.exports) module.exports = api;
})();
