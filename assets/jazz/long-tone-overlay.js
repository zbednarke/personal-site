(() => {
  "use strict";
  const instances = new Map();
  const M = globalThis.JazzLongToneModel;
  const CHUNK_MS = 60000;
  const MAX_POLLS = 120;
  let removalObserver;
  function dispose(media) { instances.get(media)?.dispose(); }
  function attach(media, options) {
    dispose(media);
    if (!M || !options.recordingId) return null;
    const doc = media.ownerDocument;
    const { shell, stage } = options;
    const asset = options.asset === "video" ? "video" : "audio";
    const endpoint = `./api/v1/recordings/${encodeURIComponent(options.recordingId)}/long-tone-analysis`;
    const toolbar = doc.createElement("div");
    toolbar.className = "long-tone-toolbar";
    toolbar.innerHTML = '<button type="button" data-lt-toggle aria-pressed="false">Long-tone overlay</button><label hidden data-lt-preference>Pitch <select aria-label="Long-tone pitch display"><option value="concert">Concert</option><option value="bb-trumpet">B♭ written</option></select></label><button type="button" data-lt-retry hidden>Retry analysis</button><span data-lt-status></span><span class="long-tone-announcement" role="status" aria-live="polite"></span>';
    const panel = doc.createElement("div");
    panel.className = "long-tone-panel";
    panel.hidden = true;
    panel.innerHTML = '<div class="long-tone-rail-wrap"><div class="long-tone-rail" role="progressbar" aria-label="Continuous same-note hold" aria-valuemin="0" aria-valuemax="30" aria-valuenow="0"><i></i></div><span data-lt-hold>0.0 / 30s</span></div><div class="long-tone-tuner" aria-label="Pitch tuner"><strong data-lt-note>—</strong><div class="long-tone-pitch"><i class="long-tone-band"></i><i class="long-tone-axis"></i><i class="long-tone-center"></i><i class="long-tone-dot" hidden></i><span class="long-tone-scale"><span>−50</span><span>0</span><span>+50</span></span></div><span data-lt-cents>—</span></div>';
    stage.classList.add("long-tone-stage");
    shell.classList.add("long-tone-shell");
    stage.append(panel);
    shell.append(toolbar);
    const $ = (selector) => shell.querySelector(selector);
    const toggle = toolbar.querySelector("[data-lt-toggle]");
    const preference = toolbar.querySelector("select");
    try { preference.value = localStorage.getItem("jazz-long-tone-instrument") || "concert"; } catch {}
    const status = toolbar.querySelector("[data-lt-status]");
    const retry = toolbar.querySelector("[data-lt-retry]");
    const announce = toolbar.querySelector("[role=status]");
    let enabled = false, disposed = false, epoch = 0, poll = 0, timer = 0, animation = 0;
    let request = null, rangeRequest = null, pendingChunk = null, manifest = null, busy = false, failed = false, lastCompleted = false;
    const cache = new Map();
    const listeners = [];
    function listen(target, type, fn) { target.addEventListener(type, fn); listeners.push(() => target.removeEventListener(type, fn)); }
    function showStatus(message, error = false) {
      status.textContent = message;
      if (error) announce.textContent = message;
    }
    function layout() {
      const rect = stage.getBoundingClientRect();
      const compact = asset === "audio" || rect.width < 440 || media.getBoundingClientRect().height < 260;
      shell.classList.toggle("long-tone-compact", compact);
      if (compact && panel.parentNode !== shell) shell.insertBefore(panel, toolbar);
      else if (!compact && panel.parentNode !== stage) stage.append(panel);
      const height = options.controls?.getBoundingClientRect().height || 0;
      stage.style.setProperty("--long-tone-controls", `${height}px`);
    }
    const resize = typeof ResizeObserver !== "undefined" ? new ResizeObserver(layout) : null;
    resize?.observe(stage);
    if (options.controls) resize?.observe(options.controls);
    layout();
    function resetRequests() {
      epoch += 1;
      clearTimeout(timer); cancelAnimationFrame(animation); animation = 0;
      request?.abort(); rangeRequest?.abort(); request = rangeRequest = null;
      pendingChunk = null;
    }
    async function fetchJSON(url, init, signal) {
      const timeoutController = new AbortController();
      let timedOut = false;
      const abort = () => timeoutController.abort();
      signal.addEventListener("abort", abort, { once: true });
      const timeout = setTimeout(() => { timedOut = true; abort(); }, 30000);
      try {
        const response = await fetch(url, { ...init, signal: timeoutController.signal });
        const body = await response.json().catch(() => ({}));
        if (!response.ok) throw new Error(body.error || `Analysis unavailable (${response.status})`);
        return body;
      } catch (error) {
        if (timedOut) throw new Error("Analysis request timed out. Retry when connected.");
        throw error;
      } finally { clearTimeout(timeout); signal.removeEventListener("abort", abort); }
    }
    function failure(message, retryable = true) {
      const messages = { no_audio: "This video has no usable audio for analysis.", too_long: "This recording exceeds the four-hour analysis limit.", timeline_unavailable: "This recording’s timing could not be verified. Playback is still available.", decoder_failed: "The recording could not be analyzed. Try again.", source_unavailable: "The recording is temporarily unavailable.", source_changed: "The recording changed. Reopen it to analyze this version.", worker_lost: "Analysis stopped before finishing." };
      message = messages[message] || message;
      busy = false; failed = true;
      showStatus(message || "Analysis unavailable", true);
      retry.hidden = !retryable;
      draw(null);
    }
    function draw(frame) {
      const value = M.reading(frame, preference.value === "bb-trumpet");
      const confirming = value.state === "holding" && value.holdMs >= 30000 && !value.complete;
      const cents = value.cents == null ? null : Math.round(value.cents);
      panel.dataset.state = value.state;
      $("[data-lt-note]").textContent = value.note;
      $("[data-lt-cents]").textContent = cents == null ? "—" : `${cents > 0 ? "+" : ""}${cents}¢`;
      const dot = $(".long-tone-dot");
      dot.hidden = cents == null;
      dot.style.left = `${50 + Math.max(-50, Math.min(50, cents || 0))}%`;
      dot.classList.toggle("centered", cents != null && Math.abs(cents) <= 10);
      $(".long-tone-rail i").style.height = `${value.complete ? 100 : Math.min(99, value.holdMs / 300)}%`;
      const seconds = value.holdMs / 1000;
      const rail = $(".long-tone-rail");
      rail.setAttribute("aria-valuenow", String(Math.min(30, seconds)));
      rail.setAttribute("aria-valuetext", value.state === "uncertain" ? "Tracking uncertain; no time credited" : confirming ? "Confirming 30-second hold" : `${seconds.toFixed(1)} seconds continuously held`);
      $("[data-lt-hold]").textContent = value.state === "uncertain" ? "Tracking…" : value.complete ? "30s held" : `${seconds.toFixed(1)} / 30s`;
      if (value.complete && !lastCompleted) announce.textContent = "30 seconds held continuously";
      lastCompleted = value.complete;
      if (frame) showStatus(value.state === "uncertain" ? "Pitch uncertain" : value.state === "listening" ? "Waiting for tone" : value.state === "acquiring" ? "Finding note…" : value.complete ? "Duration goal reached" : confirming ? "Confirming hold…" : "Holding");
    }
    async function loadRange(chunk) {
      if (pendingChunk === chunk || disposed || !enabled) return;
      rangeRequest?.abort();
      const controller = new AbortController(); rangeRequest = controller;
      pendingChunk = chunk;
      const token = epoch;
      try {
        const body = await fetchJSON(`${endpoint}?asset=${asset}&startMs=${chunk}&endMs=${chunk + CHUNK_MS}`, {}, controller.signal);
        if (disposed || !enabled || token !== epoch || rangeRequest !== controller) return;
        if (body.status !== "ready") { await acceptManifest(body, token); return; }
        if (body.analysisId !== manifest?.analysisId || body.version !== manifest?.version) {
          cache.clear(); manifest = body;
        }
        cache.set(chunk, Array.isArray(body.frames) ? body.frames : []);
        while (cache.size > 3) cache.delete(cache.keys().next().value);
        pendingChunk = null;
        render();
      } catch (error) {
        if (error.name !== "AbortError" && token === epoch && rangeRequest === controller) failure(error.message);
      } finally { if (rangeRequest === controller) pendingChunk = null; }
    }
    function render() {
      if (!enabled || disposed) return;
      if (manifest?.status !== "ready" || busy || failed) return;
      const now = Math.max(0, Number(media.currentTime) || 0) * 1000;
      const chunk = Math.floor(now / CHUNK_MS) * CHUNK_MS;
      if (!cache.has(chunk)) { draw(null); showStatus("Loading analysis…"); loadRange(chunk); return; }
      const frame = M.frameAt(cache.get(chunk), now);
      draw(frame);
      if (!frame) showStatus("No analysis at this position");
    }
    function tick() {
      animation = 0;
      if (!enabled || disposed) return;
      render();
      if (!media.paused && !media.ended && !doc.hidden) animation = requestAnimationFrame(tick);
    }
    function mediaChanged() {
      render();
      if (!animation && !media.paused && !doc.hidden) animation = requestAnimationFrame(tick);
    }
    async function acceptManifest(body, token) {
      if (disposed || !enabled || token !== epoch) return;
      if (manifest?.analysisId !== body.analysisId || manifest?.version !== body.version) cache.clear();
      manifest = body;
      busy = false; failed = false;
      if (body.status === "ready") { showStatus("Loading analysis…"); render(); }
      else if (["pending", "running"].includes(body.status)) {
        draw(null); showStatus("Analyzing…");
        if (++poll >= MAX_POLLS) { failure("Analysis is taking longer. Retry to check again."); return; }
        timer = setTimeout(() => refresh(false), 2000);
      } else failure(body.error || (body.status === "missing" ? "Analysis unavailable. Retry to prepare it." : "Analysis unavailable"), body.retryable !== false);
    }
    async function refresh(create) {
      if (!enabled || disposed) return;
      const token = epoch;
      request?.abort();
      const controller = new AbortController(); request = controller;
      busy = true;
      try {
        const body = await fetchJSON(create ? endpoint : `${endpoint}?asset=${asset}`, create ? { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ asset }) } : {}, controller.signal);
        if (request === controller) await acceptManifest(body, token);
      } catch (error) {
        if (error.name !== "AbortError" && token === epoch && request === controller) failure(error.message);
      }
    }
    function enable(next) {
      resetRequests(); enabled = next;
      toggle.setAttribute("aria-pressed", String(next));
      panel.hidden = !next;
      toolbar.querySelector("[data-lt-preference]").hidden = !next;
      retry.hidden = true; announce.textContent = ""; lastCompleted = false;
      if (next) { failed = false; poll = 0; showStatus("Preparing analysis…"); draw(null); refresh(true); mediaChanged(); }
      else { showStatus(""); busy = false; }
    }
    listen(toggle, "click", () => enable(!enabled));
    listen(retry, "click", () => { resetRequests(); failed = false; retry.hidden = true; poll = 0; showStatus("Preparing analysis…"); refresh(true); mediaChanged(); });
    listen(preference, "change", () => { try { localStorage.setItem("jazz-long-tone-instrument", preference.value); } catch {} render(); });
    ["timeupdate", "seeking", "seeked", "play", "pause", "ended", "ratechange", "loadedmetadata", "waiting", "playing"].forEach((type) => listen(media, type, mediaChanged));
    listen(doc, "visibilitychange", mediaChanged);
    listen(doc, "jazz:view-change", () => {
      if (enabled && shell.closest("[hidden]")) enable(false);
    });
    listen(doc.defaultView, "pagehide", () => { if (enabled) enable(false); });
    const api = { dispose() {
      if (disposed) return;
      disposed = true; resetRequests(); resize?.disconnect(); listeners.forEach((remove) => remove());
      panel.remove(); toolbar.remove(); cache.clear(); instances.delete(media);
    }};
    instances.set(media, api);
    if (!removalObserver && typeof MutationObserver !== "undefined") {
      removalObserver = new MutationObserver(() => { for (const [element, controller] of instances) if (!element.isConnected) controller.dispose(); });
      removalObserver.observe(doc.documentElement, { childList: true, subtree: true });
    }
    return api;
  }
  globalThis.JazzLongToneOverlay = { attach, dispose };
})();
