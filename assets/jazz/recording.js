(() => {
  "use strict";

  const DATA = globalThis.JAZZ_DATA;
  const API_BASE = "./api/v1";
  const $ = (selector, root = document) => root.querySelector(selector);

  let stream = null;
  let losslessRecorder = null;
  let startedAt = 0;
  let recordedAt = "";
  let timerID = null;
  let levelFrame = null;
  let audioContext = null;
  let recordedSampleRate = 0;
  let currentPracticeSessionID = "";
  let finishing = false;
  let previewURL = null;
  let activeBlockContext = null;
  let pendingUpload = null;
  let lastUploadProgressAt = 0;
  let lastUploadProgressPercent = -1;
  let pitchHistory = [];
  let lastPitchCheckAt = 0;

  function setServiceStatus(message, tone = "") {
    const element = $("#recording-service-status");
    if (!element) return;
    element.textContent = message;
    element.className = `cloud-status${tone ? ` ${tone}` : ""}`;
  }

  function setRecorderState(message, phase = "status", canRetry = false, notify = true) {
    const state = $("#recording-state");
    if (state) state.textContent = message;
    if (!notify) return;
    dispatchEvent(new CustomEvent("jazz:recording-state", {
      detail: { blockId: activeBlockContext?.id || "", message, phase, canRetry },
    }));
  }

  async function api(path, options = {}) {
    const response = await fetch(`${API_BASE}${path}`, {
      ...options,
      headers: {
        ...(options.body ? { "Content-Type": "application/json" } : {}),
        ...(options.headers || {}),
      },
    });
    if (response.status === 204) return null;
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.error || `Request failed (${response.status})`);
    return body;
  }

  function formatTimer(milliseconds) {
    const seconds = Math.floor(milliseconds / 1000);
    return `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
  }

  function updateTimer() {
    const elapsed = performance.now() - startedAt;
    const timer = $("#recording-timer");
    if (timer) timer.textContent = formatTimer(elapsed);
  }

  function showRetryButton(show) {
    const button = $("#retry-recording");
    if (button) button.hidden = !show;
  }

  async function updateMicrophones() {
    const devices = await navigator.mediaDevices.enumerateDevices();
    const microphones = devices.filter((device) => device.kind === "audioinput");
    const select = $("#microphone-select");
    if (!select) return;
    const selected = select.value;
    select.replaceChildren(new Option("Default microphone", ""));
    microphones.forEach((microphone, index) => {
      select.add(new Option(microphone.label || `Microphone ${index + 1}`, microphone.deviceId));
    });
    if ([...select.options].some((option) => option.value === selected)) select.value = selected;
  }

  function drawWaveforms(samples) {
    document.querySelectorAll("[data-waveform]").forEach((canvas) => {
      const bounds = canvas.getBoundingClientRect();
      if (!bounds.width || !bounds.height) return;
      const scale = Math.min(2, globalThis.devicePixelRatio || 1);
      const width = Math.max(1, Math.round(bounds.width * scale));
      const height = Math.max(1, Math.round(bounds.height * scale));
      if (canvas.width !== width || canvas.height !== height) {
        canvas.width = width;
        canvas.height = height;
      }
      const context = canvas.getContext("2d");
      context.clearRect(0, 0, width, height);
      context.strokeStyle = "rgba(255, 255, 255, 0.08)";
      context.lineWidth = 1;
      context.beginPath();
      context.moveTo(0, height / 2);
      context.lineTo(width, height / 2);
      context.stroke();

      context.strokeStyle = "#f2ad5c";
      context.lineWidth = Math.max(1.5, scale);
      context.beginPath();
      const step = Math.max(1, Math.floor(samples.length / width));
      for (let x = 0; x < width; x += 1) {
        const sample = samples[Math.min(samples.length - 1, x * step)];
        const y = (height / 2) + (sample * height * 0.43);
        if (x === 0) context.moveTo(x, y);
        else context.lineTo(x, y);
      }
      context.stroke();
    });
  }

  function renderTuner(pitch, message = "Play a held note") {
    document.querySelectorAll("[data-tuner]").forEach((tuner) => {
      const note = $("[data-tuner-note]", tuner);
      const cents = $("[data-tuner-cents]", tuner);
      const frequency = $("[data-tuner-frequency]", tuner);
      const needle = $("[data-tuner-needle]", tuner);
      if (!pitch) {
        note.textContent = "—";
        cents.textContent = message;
        frequency.textContent = "A4 = 440 Hz";
        needle.style.left = "50%";
        tuner.dataset.tone = "waiting";
        return;
      }
      const roundedCents = Math.round(pitch.cents);
      note.textContent = `${pitch.note}${pitch.octave}`;
      cents.textContent = Math.abs(roundedCents) <= 4 ? "In tune" : `${roundedCents > 0 ? "+" : ""}${roundedCents} cents`;
      frequency.textContent = `${pitch.frequency.toFixed(1)} Hz`;
      needle.style.left = `${50 + Math.max(-50, Math.min(50, pitch.cents))}%`;
      tuner.dataset.tone = Math.abs(roundedCents) <= 4 ? "tuned" : (roundedCents < 0 ? "flat" : "sharp");
    });
  }

  function resetLiveAudio() {
    pitchHistory = [];
    lastPitchCheckAt = 0;
    document.querySelectorAll("[data-waveform]").forEach((canvas) => {
      const context = canvas.getContext("2d");
      context?.clearRect(0, 0, canvas.width, canvas.height);
    });
    renderTuner(null);
  }

  function startLevelMeter(activeStream) {
    const AudioContext = globalThis.AudioContext || globalThis.webkitAudioContext;
    if (!AudioContext) return;
    audioContext = new AudioContext();
    audioContext.resume().catch(() => {});
    recordedSampleRate = audioContext.sampleRate;
    const analyser = audioContext.createAnalyser();
    analyser.fftSize = 4096;
    analyser.smoothingTimeConstant = 0.18;
    audioContext.createMediaStreamSource(activeStream).connect(analyser);
    const samples = new Float32Array(analyser.fftSize);
    const analysis = globalThis.JazzAudioAnalysis;
    pitchHistory = [];
    lastPitchCheckAt = 0;
    const render = () => {
      analyser.getFloatTimeDomainData(samples);
      const level = analysis?.rms(samples) || 0;
      const inputMeter = $("#input-meter-fill");
      if (inputMeter) inputMeter.style.width = `${Math.min(100, Math.max(1, level * 320))}%`;
      drawWaveforms(samples);
      const now = performance.now();
      if (analysis && now - lastPitchCheckAt >= 90) {
        lastPitchCheckAt = now;
        const detected = analysis.detectPitch(samples, audioContext.sampleRate);
        if (!detected || detected.clarity < 0.82) {
          pitchHistory = [];
          renderTuner(null, level >= 0.012 ? "Hold the note" : "Play a held note");
        } else {
          pitchHistory.push(detected);
          pitchHistory = pitchHistory.slice(-4);
          const stable = analysis.stablePitch(pitchHistory);
          renderTuner(stable, "Hold the note");
        }
      }
      levelFrame = requestAnimationFrame(render);
    };
    render();
  }

  function stopCapture() {
    clearInterval(timerID);
    timerID = null;
    if (levelFrame) cancelAnimationFrame(levelFrame);
    levelFrame = null;
    const inputMeter = $("#input-meter-fill");
    if (inputMeter) inputMeter.style.width = "0";
    stream?.getTracks().forEach((track) => track.stop());
    stream = null;
    audioContext?.close().catch(() => {});
    audioContext = null;
    resetLiveAudio();
    $("#recording-light")?.classList.remove("active");
    const startButton = $("#start-recording");
    const stopButton = $("#stop-recording");
    if (startButton) startButton.disabled = false;
    if (stopButton) stopButton.disabled = true;
  }

  async function startRecording() {
    if (stream || finishing) return;
    if (!navigator.mediaDevices?.getUserMedia || !globalThis.JazzLosslessRecorder) {
      setRecorderState("Lossless recording is not supported in this browser", "error");
      activeBlockContext = null;
      return;
    }
    try {
      setRecorderState("Requesting microphone access...", "starting");
      const practiceSession = await globalThis.JazzPracticeSession.ensureActive();
      currentPracticeSessionID = practiceSession.id;
      finishing = false;
      const deviceID = $("#microphone-select")?.value || "";
      stream = await navigator.mediaDevices.getUserMedia({
        audio: {
          ...(deviceID ? { deviceId: { exact: deviceID } } : {}),
          echoCancellation: false,
          noiseSuppression: false,
          autoGainControl: false,
          channelCount: 1,
        },
      });
      await updateMicrophones();
      losslessRecorder = new globalThis.JazzLosslessRecorder();
      await losslessRecorder.start(stream);
      recordedAt = new Date().toISOString();
      startedAt = performance.now();
      updateTimer();
      timerID = setInterval(updateTimer, 250);
      startLevelMeter(stream);
      $("#recording-light")?.classList.add("active");
      const startButton = $("#start-recording");
      const stopButton = $("#stop-recording");
      if (startButton) startButton.disabled = true;
      if (stopButton) stopButton.disabled = false;
      setRecorderState("Recording lossless 24-bit audio - play the take", "recording");
    } catch (error) {
      stopCapture();
      setRecorderState(error.name === "NotAllowedError" ? "Microphone permission was not granted" : "Could not start the microphone", "error");
      activeBlockContext = null;
    }
  }

  function stopRecording() {
    if (!losslessRecorder || finishing) return;
    finishing = true;
    const stopButton = $("#stop-recording");
    if (stopButton) stopButton.disabled = true;
    setRecorderState("Building the lossless take...", "processing");
    finishRecording();
  }

  async function finishRecording() {
    let result;
    try {
      result = await losslessRecorder.stop();
    } catch (error) {
      stopCapture();
      losslessRecorder = null;
      finishing = false;
      setRecorderState(`Could not finish the lossless take: ${error.message}`, "error");
      activeBlockContext = null;
      return;
    }
    losslessRecorder = null;
    recordedSampleRate = result.sampleRate;
    const { blob, durationMS } = result;
    const contentType = "audio/wav";
    stopCapture();
    if (previewURL) URL.revokeObjectURL(previewURL);
    previewURL = URL.createObjectURL(blob);
    const preview = $("#recording-preview");
    if (preview) {
      preview.src = previewURL;
      preview.hidden = false;
    }
    pendingUpload = captureUpload(blob, durationMS, contentType);
    showRetryButton(false);
    lastUploadProgressPercent = -1;
    lastUploadProgressAt = 0;
    setRecorderState("Take captured - starting private upload", "uploading");
    try {
      await uploadRecording(pendingUpload);
      pendingUpload = null;
      setRecorderState("Uploaded privately", "complete");
      setServiceStatus("Private storage connected", "online");
      const takeInput = $('#recording-metadata input[name="takeNumber"]');
      if (takeInput) takeInput.value = String(Math.min(99, Number(takeInput.value || 1) + 1));
      await loadRecordings();
      await globalThis.JazzPracticeSession.refresh();
      dispatchEvent(new CustomEvent("jazz:recordings-changed"));
    } catch (error) {
      showRetryButton(true);
      setRecorderState(`Take is safe in this tab. Upload failed: ${error.message}`, "error", true);
      setServiceStatus("Upload needs attention", "offline");
    } finally {
      finishing = false;
      activeBlockContext = null;
    }
  }

  function captureUpload(blob, durationMS, contentType) {
    const metadataForm = $("#recording-metadata");
    const metadata = metadataForm ? new FormData(metadataForm) : new FormData();
    const blockContext = activeBlockContext ? { ...activeBlockContext } : null;
    return {
      blob,
      durationMS,
      contentType,
      recordedAt,
      sampleRate: recordedSampleRate,
      practiceSessionID: currentPracticeSessionID || globalThis.JazzPracticeSession.currentID(),
      blockContext,
      tuneId: blockContext ? String(blockContext.tuneId || "") : String(metadata.get("tuneId") || ""),
      skillIds: blockContext?.skillIds || (metadata.get("skillId") ? [String(metadata.get("skillId"))] : []),
      takeNumber: blockContext?.takeNumber || Number(metadata.get("takeNumber") || 1),
      notes: blockContext ? `${blockContext.title} section take` : String(metadata.get("notes") || ""),
    };
  }

  async function uploadRecording(capture) {
    const baseType = capture.contentType.split(";")[0];
    const codecMatch = /codecs?=([^;]+)/i.exec(capture.contentType);
    const initialized = await api("/recordings/init", {
      method: "POST",
      body: JSON.stringify({
        contentType: baseType,
        codec: capture.contentType === "audio/wav" ? "pcm_s24le" : (codecMatch?.[1] || ""),
        sizeBytes: capture.blob.size,
        durationMs: capture.durationMS,
        sampleRate: capture.sampleRate,
        channels: 1,
        recordedAt: capture.recordedAt,
        practiceSessionId: capture.practiceSessionID,
        practiceBlockId: capture.blockContext?.id || "",
        tuneId: capture.tuneId,
        missionId: DATA.mission.id,
        skillIds: capture.skillIds,
        takeNumber: capture.takeNumber,
        notes: capture.notes,
      }),
    });
    try {
      await putBlob(initialized.uploadUrl, capture.blob, baseType);
      await api(`/recordings/${initialized.id}/complete`, { method: "POST", body: "{}" });
    } catch (error) {
      await api(`/recordings/${initialized.id}`, { method: "DELETE" }).catch(() => {});
      throw error;
    }
  }

  async function retryUpload() {
    if (!pendingUpload || stream || finishing) return false;
    finishing = true;
    activeBlockContext = pendingUpload.blockContext;
    showRetryButton(false);
    lastUploadProgressPercent = -1;
    lastUploadProgressAt = 0;
    setRecorderState("Retrying private upload...", "uploading");
    try {
      await uploadRecording(pendingUpload);
      pendingUpload = null;
      setRecorderState("Uploaded privately", "complete");
      setServiceStatus("Private storage connected", "online");
      await loadRecordings();
      await globalThis.JazzPracticeSession.refresh();
      dispatchEvent(new CustomEvent("jazz:recordings-changed"));
      return true;
    } catch (error) {
      showRetryButton(true);
      setRecorderState(`Take is safe in this tab. Upload failed: ${error.message}`, "error", true);
      setServiceStatus("Upload needs attention", "offline");
      return false;
    } finally {
      finishing = false;
      activeBlockContext = null;
    }
  }

  function putBlob(uploadURL, blob, contentType) {
    const shouldNotifyProgress = (percent) => {
      const next = Math.round(performance.now());
      if (percent === 100) return true;
      if (percent !== lastUploadProgressPercent && (percent % 10 === 0)) return true;
      if (percent !== lastUploadProgressPercent && next - lastUploadProgressAt > 800) return true;
      return false;
    };

    return new Promise((resolve, reject) => {
      const request = new XMLHttpRequest();
      request.open("PUT", uploadURL);
      request.setRequestHeader("Content-Type", contentType);
      request.setRequestHeader("Content-Range", `bytes 0-${blob.size - 1}/${blob.size}`);
      request.upload.addEventListener("progress", (event) => {
        if (!event.lengthComputable) return;
        const percent = Math.round((event.loaded / event.total) * 100);
        const message = `Uploading privately - ${percent}%`;
        const notify = shouldNotifyProgress(percent);
        if (notify) {
          lastUploadProgressPercent = percent;
          lastUploadProgressAt = performance.now();
        }
        setRecorderState(message, "uploading", false, notify);
      });
      request.addEventListener("load", () => {
        if (request.status >= 200 && request.status < 300) resolve();
        else reject(new Error(`storage returned ${request.status}`));
      });
      request.addEventListener("error", () => reject(new Error("network error")));
      request.send(blob);
    });
  }

  function formatDuration(milliseconds) {
    if (!milliseconds) return "--:--";
    return formatTimer(milliseconds);
  }

  async function loadRecordings() {
    const library = $("#recording-library");
    try {
      const result = await api("/recordings");
      setServiceStatus("Private storage connected", "online");
      if (!library) return;
      library.replaceChildren();
      if (!result.recordings.length) {
        library.innerHTML = '<p class="empty-recordings">No recordings yet. Your first honest take is the baseline.</p>';
        return;
      }
      result.recordings.forEach((recording) => {
        const tune = DATA.repertoire.find((item) => item.id === recording.tuneId)?.title || "Open practice";
        const skill = DATA.skills.find((item) => item.id === recording.skillIds?.[0])?.name || "General musicianship";
        const sessionTitle = recording.practiceSessionTitle || "Unassigned session";
        const blockTitle = recording.practiceBlockTitle || "";
        const track = DATA.tracks.find((item) => item.id === recording.practiceBlockTrack)?.name || recording.practiceBlockCategory || "";
        const title = blockTitle || tune;
        const context = blockTitle
          ? [formatPracticeDate(recording.practiceDate), track].filter(Boolean).join(" · ")
          : [sessionTitle, skill].filter(Boolean).join(" · ");
        const format = recording.contentType === "audio/wav" ? "Lossless WAV" : recording.contentType.replace("audio/", "").toUpperCase();
        const card = document.createElement("article");
        card.className = "recording-item";
        card.innerHTML = `
          <div class="recording-item-top"><span>${new Date(recording.recordedAt).toLocaleDateString()}</span><span>${formatDuration(recording.durationMs)} · ${format}</span></div>
          <h4>${escapeHTML(title)}${recording.takeNumber ? ` · Take ${recording.takeNumber}` : ""}</h4>
          <p>${escapeHTML(recording.notes || "No listening note yet.")}</p>
          <span class="recording-context">${escapeHTML(context)}</span>
          <div class="recording-item-actions">
            <button type="button" class="play-recording">Play</button>
            <button type="button" class="delete-recording">Delete</button>
          </div>`;
        $(".play-recording", card).addEventListener("click", () => playRecording(recording.id, card));
        $(".delete-recording", card).addEventListener("click", () => deleteRecording(recording.id));
        library.appendChild(card);
      });
    } catch {
      setServiceStatus("Private storage offline", "offline");
    }
  }

  function formatPracticeDate(value) {
    if (!value) return "";
    const [year, month, day] = value.split("-").map(Number);
    if (!year || !month || !day) return value;
    return new Date(year, month - 1, day).toLocaleDateString(undefined, { month: "short", day: "numeric" });
  }

  async function playRecording(id, card) {
    const button = $(".play-recording", card) || $("[data-section-play]", card);
    button.disabled = true;
    button.textContent = "Loading...";
    try {
      const result = await api(`/recordings/${id}/playback-url`, { method: "POST", body: "{}" });
      let player = $("audio", card);
      if (!player) {
        player = document.createElement("audio");
        player.controls = true;
        card.appendChild(player);
      }
      player.src = result.url;
      await player.play();
    } catch (error) {
      setRecorderState(`Playback failed: ${error.message}`);
    } finally {
      button.disabled = false;
      button.textContent = "Play";
    }
  }

  async function deleteRecording(id) {
    if (!confirm("Delete this take? It can be recovered from cloud soft-delete for a limited time.")) return;
    try {
      await api(`/recordings/${id}`, { method: "DELETE" });
      await loadRecordings();
      await globalThis.JazzPracticeSession.refresh();
      dispatchEvent(new CustomEvent("jazz:recordings-changed"));
    } catch (error) {
      setRecorderState(`Delete failed: ${error.message}`);
    }
  }

  function escapeHTML(value) {
    const element = document.createElement("span");
    element.textContent = value;
    return element.innerHTML;
  }

  function populateMetadata() {
    const tuneSelect = $("#recording-tune");
    const skillSelect = $("#recording-skill");
    if (!tuneSelect || !skillSelect) return;
    tuneSelect.add(new Option("Open practice", ""));
    DATA.repertoire.forEach((tune) => tuneSelect.add(new Option(tune.title, tune.id, tune.current, tune.current)));
    skillSelect.add(new Option("General musicianship", ""));
    DATA.skills.forEach((skill) => skillSelect.add(new Option(`${skill.name} - ${skill.track}`, skill.id)));
  }

  function startForBlock(block) {
    if (stream || finishing) return false;
    if (pendingUpload) {
      activeBlockContext = pendingUpload.blockContext;
      showRetryButton(true);
      setRecorderState("Retry the saved take before starting another recording", "error", true);
      activeBlockContext = null;
      return false;
    }
    activeBlockContext = block;
    startRecording();
    return true;
  }

  function startGeneralRecording() {
    if (stream || finishing) return false;
    if (pendingUpload) {
      activeBlockContext = pendingUpload.blockContext;
      showRetryButton(true);
      setRecorderState("Retry the saved take before starting another recording", "error", true);
      activeBlockContext = null;
      return false;
    }
    activeBlockContext = null;
    startRecording();
    return true;
  }

  globalThis.JazzRecording = {
    startForBlock,
    stop: stopRecording,
    retry: retryUpload,
    play: playRecording,
    delete: deleteRecording,
  };

  populateMetadata();
  $("#start-recording")?.addEventListener("click", startGeneralRecording);
  $("#stop-recording")?.addEventListener("click", stopRecording);
  $("#retry-recording")?.addEventListener("click", retryUpload);
  $("#refresh-recordings")?.addEventListener("click", loadRecordings);
  navigator.mediaDevices?.addEventListener?.("devicechange", updateMicrophones);
  loadRecordings();
})();
