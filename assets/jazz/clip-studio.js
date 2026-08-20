(() => {
  "use strict";

  const API_BASE = "./api/v1";
  const U = globalThis.JazzArchiveUtils;
  if (!U) return;
  const $ = (selector, root = document) => root.querySelector(selector);
  const state = {
    date: U.dateKey(new Date()),
    initialized: false,
    recordings: [],
    candidates: [],
    current: null,
    media: null,
    request: 0,
    savePromise: Promise.resolve(),
  };

  async function api(path, options = {}) {
    const response = await fetch(`${API_BASE}${path}`, {
      ...options,
      headers: { ...(options.body ? { "Content-Type": "application/json" } : {}), ...(options.headers || {}) },
    });
    const body = response.status === 204 ? null : await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body?.error || `Request failed (${response.status})`);
    return body;
  }

  function escapeHTML(value) {
    const span = document.createElement("span");
    span.textContent = String(value ?? "");
    return span.innerHTML;
  }

  function recordingFor(candidate) {
    return state.recordings.find((recording) => recording.id === candidate.recordingId);
  }

  function titleFor(recording) {
    return recording?.practiceBlockTitle || recording?.practiceSessionTitle || "Open practice";
  }

  function setStatus(message, tone = "") {
    const element = $("#studio-status");
    element.textContent = message;
    element.dataset.tone = tone;
  }

  function formatClock(milliseconds) {
    return U.formatPlaybackTime(Math.max(0, Number(milliseconds || 0)) / 1000);
  }

  function initialize() {
    if (state.initialized) return;
    state.initialized = true;
    $("#studio-date").value = state.date;
    $("#studio-date").max = U.dateKey(new Date());
    $("#studio-date").addEventListener("change", (event) => loadDay(event.target.value));
    $("#studio-previous-day").addEventListener("click", () => shiftDay(-1));
    $("#studio-next-day").addEventListener("click", () => shiftDay(1));
    $("#studio-scan").addEventListener("click", scanDay);
    $("#studio-save-boundaries").addEventListener("click", saveCandidate);
    $("#studio-keep").addEventListener("click", () => reviewCandidate("kept"));
    $("#studio-reject").addEventListener("click", () => reviewCandidate("rejected"));
    $("#studio-candidate-notes").addEventListener("blur", saveCandidate);
    loadDay(state.date);
  }

  function shiftDay(delta) {
    const date = U.parseDateKey(state.date);
    if (!date) return;
    date.setDate(date.getDate() + delta);
    const next = U.dateKey(date);
    if (next > U.dateKey(new Date())) return;
    loadDay(next);
  }

  async function loadDay(date) {
    if (!U.parseDateKey(date)) return;
    const request = ++state.request;
    state.date = date;
    $("#studio-date").value = date;
    setStatus("Loading lossless masters…");
    try {
      const result = await api(`/studio/days/${date}`);
      if (request !== state.request) return;
      state.recordings = result.recordings || [];
      state.candidates = result.candidates || [];
      state.current = null;
      closePreview();
      render();
      setStatus(state.recordings.length ? "Ready to listen or rescan this day." : "No completed recordings on this day.");
    } catch (error) {
      if (request !== state.request) return;
      state.recordings = [];
      state.candidates = [];
      render();
      setStatus(`Studio unavailable · ${error.message}`, "error");
    }
  }

  async function scanDay() {
    const button = $("#studio-scan");
    button.disabled = true;
    button.textContent = "Scanning…";
    setStatus("Finding sustained musical activity in the day’s lossless masters…");
    try {
      const result = await api(`/studio/days/${state.date}/scan`, { method: "POST", body: "{}" });
      state.candidates = result.candidates || [];
      state.current = null;
      closePreview();
      render();
      setStatus(`Scanned ${result.scannedRecordings} take${result.scannedRecordings === 1 ? "" : "s"} · ${state.candidates.length} suggestions ready.`);
    } catch (error) {
      setStatus(`Scan failed · ${error.message}`, "error");
    } finally {
      button.disabled = false;
      button.textContent = "Scan this day";
    }
  }

  function render() {
    const duration = state.recordings.reduce((sum, recording) => sum + Number(recording.durationMs || 0), 0);
    $("#studio-total-time").textContent = formatClock(duration);
    $("#studio-take-count").textContent = String(state.recordings.length);
    $("#studio-candidate-count").textContent = String(state.candidates.filter((item) => item.reviewStatus !== "rejected").length);
    renderRecordings();
    renderCandidates();
  }

  function renderRecordings() {
    const host = $("#studio-recordings");
    if (!state.recordings.length) {
      host.innerHTML = '<p class="clip-studio-empty">No completed recordings on this practice day.</p>';
      return;
    }
    host.innerHTML = state.recordings.map((recording) => {
      const candidates = state.candidates.filter((candidate) => candidate.recordingId === recording.id && candidate.reviewStatus !== "rejected");
      const duration = Math.max(1, Number(recording.durationMs || 0));
      const peaks = Array.isArray(recording.waveformPeaks) ? recording.waveformPeaks : [];
      const sampled = Array.from({ length: Math.min(100, peaks.length) }, (_, index) => {
        const source = Math.floor((index / Math.min(100, peaks.length)) * peaks.length);
        return peaks[source] || 0;
      });
      const waveform = sampled.map((peak) => `<i style="height:${Math.max(3, Math.round(Number(peak) * 100))}%"></i>`).join("");
      const markers = candidates.map((candidate) => `<button type="button" data-studio-candidate="${candidate.id}" aria-label="Open candidate ${formatClock(candidate.startMs)} to ${formatClock(candidate.endMs)}" style="left:${candidate.startMs / duration * 100}%;width:${Math.max(.5, (candidate.endMs - candidate.startMs) / duration * 100)}%"></button>`).join("");
      return `<article class="clip-studio-recording"><header><div><strong>${escapeHTML(titleFor(recording))}</strong><span>${escapeHTML(U.takeLabel(recording))} · ${recording.mediaKind === "video" ? "video + WAV" : "lossless WAV"}</span></div><time>${formatClock(duration)}</time></header><div class="clip-studio-waveform">${waveform}<span>${markers}</span></div></article>`;
    }).join("");
    host.querySelectorAll("[data-studio-candidate]").forEach((button) => button.addEventListener("click", () => selectCandidate(button.dataset.studioCandidate)));
  }

  function renderCandidates() {
    const host = $("#studio-candidate-list");
    if (!state.candidates.length) {
      host.innerHTML = `<p class="clip-studio-empty">${state.recordings.length ? "Run an activity scan to create suggestions." : "Recordings from the selected day will appear here."}</p>`;
      return;
    }
    host.innerHTML = state.candidates.map((candidate, index) => {
      const recording = recordingFor(candidate);
      const reasons = Array.isArray(candidate.reasons) ? candidate.reasons : [];
      return `<article class="clip-candidate ${candidate.reviewStatus}${state.current?.id === candidate.id ? " active" : ""}"><button class="clip-candidate-open" type="button" data-open-candidate="${candidate.id}"><span>${candidate.reviewStatus === "kept" ? "Kept" : candidate.reviewStatus === "rejected" ? "Rejected" : `Suggestion ${index + 1}`}</span><strong>${escapeHTML(titleFor(recording))} · ${escapeHTML(U.takeLabel(recording))}</strong><time>${formatClock(candidate.startMs)} — ${formatClock(candidate.endMs)}</time><em>${Math.round(Number(candidate.score || 0) * 100)}% activity confidence</em></button><div class="clip-candidate-reasons">${reasons.map((reason) => `<span>${escapeHTML(reason)}</span>`).join("")}</div></article>`;
    }).join("");
    host.querySelectorAll("[data-open-candidate]").forEach((button) => button.addEventListener("click", () => selectCandidate(button.dataset.openCandidate)));
  }

  async function selectCandidate(id) {
    const candidate = state.candidates.find((item) => item.id === id);
    const recording = recordingFor(candidate);
    if (!candidate || !recording) return;
    state.current = candidate;
    renderCandidates();
    $("#studio-preview-title").textContent = `${titleFor(recording)} · ${U.takeLabel(recording)}`;
    $("#studio-start").value = (candidate.startMs / 1000).toFixed(1);
    $("#studio-end").value = (candidate.endMs / 1000).toFixed(1);
    $("#studio-candidate-notes").value = candidate.notes || "";
    $("#studio-editor").hidden = false;
    const request = ++state.request;
    $("#studio-media").innerHTML = '<p class="clip-studio-empty">Loading private playback…</p>';
    try {
      const asset = recording.mediaKind === "video" ? "video" : "audio";
      const result = await api(`/recordings/${recording.id}/playback-url?asset=${asset}`, { method: "POST", body: "{}" });
      if (request !== state.request || state.current?.id !== id) return;
      state.media?.pause();
      const media = document.createElement(asset === "video" ? "video" : "audio");
      media.controls = true;
      media.preload = "metadata";
      media.playsInline = true;
      media.src = result.url;
      media.addEventListener("loadedmetadata", () => {
        media.currentTime = candidate.startMs / 1000;
        media.play().catch(() => {});
      }, { once: true });
      media.addEventListener("timeupdate", () => {
        if (media.currentTime >= candidate.endMs / 1000) media.pause();
      });
      $("#studio-media").replaceChildren(media);
      state.media = media;
    } catch (error) {
      $("#studio-media").innerHTML = `<p class="clip-studio-empty">Playback unavailable · ${escapeHTML(error.message)}</p>`;
    }
  }

  async function saveCandidate() {
    if (!state.current) return;
    const startMs = Math.round(Number($("#studio-start").value) * 1000);
    const endMs = Math.round(Number($("#studio-end").value) * 1000);
    const notes = $("#studio-candidate-notes").value.trim();
    await patchCurrent({ startMs, endMs, notes }, "Boundaries and note synced.");
  }

  async function reviewCandidate(reviewStatus) {
    if (!state.current) return;
    await patchCurrent({ reviewStatus, notes: $("#studio-candidate-notes").value.trim() }, reviewStatus === "kept" ? "Added to the edit queue." : "Suggestion rejected.");
  }

  async function patchCurrent(payload, successMessage) {
    const candidate = state.current;
    if (!candidate) return;
    state.savePromise = state.savePromise.catch(() => {}).then(async () => {
      try {
        const result = await api(`/studio/candidates/${candidate.id}`, { method: "PATCH", body: JSON.stringify(payload) });
        Object.assign(candidate, result);
        render();
        setStatus(successMessage);
      } catch (error) {
        setStatus(`Could not save · ${error.message}`, "error");
      }
    });
    return state.savePromise;
  }

  function closePreview() {
    state.media?.pause();
    state.media = null;
    $("#studio-preview-title").textContent = "Choose a suggestion";
    $("#studio-media").innerHTML = '<p class="clip-studio-empty">The preview will stop automatically at the candidate’s out point.</p>';
    $("#studio-editor").hidden = true;
  }

  document.addEventListener("jazz:view-change", (event) => {
    if (event.detail?.view === "studio") initialize();
    else state.media?.pause();
  });
  if (location.hash === "#studio") initialize();
})();
