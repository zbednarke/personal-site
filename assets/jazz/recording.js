(() => {
  "use strict";

  const DATA = globalThis.JAZZ_DATA;
  const API_BASE = "./api/v1";
  const MICROPHONE_STORAGE_KEY = "zach-jazz-microphone-v1";
  const CAMERA_STORAGE_KEY = "zach-jazz-camera-v1";
  const RECORDING_MODE_STORAGE_KEY = "zach-jazz-recording-mode-v1";
  const VIDEO_RESOLUTION_STORAGE_KEY = "zach-jazz-video-resolution-v1";
  const UPLOAD_CHUNK_BYTES = 8 * 1024 * 1024;
  const { MAX_TAKE_DURATION_MS, shouldAutoFinish } = globalThis.JazzRecordingPolicy;
  const $ = (selector, root = document) => root.querySelector(selector);

  let stream = null;
  let monitorStream = null;
  let cameraStream = null;
  let recordingCameraStream = null;
  let videoRecordingStream = null;
  let videoRecorder = null;
  let videoChunks = [];
  let videoContentType = "";
  let losslessRecorder = null;
  let fxRecorder = null;
  let activeFxPreset = "";
  let startedAt = 0;
  let recordedAt = "";
  let timerID = null;
  let autoStopID = null;
  let levelFrame = null;
  let audioContext = null;
  let recordedSampleRate = 0;
  let currentPracticeSessionID = "";
  let captureFinalizing = false;
  let previewURL = null;
  let activeBlockContext = null;
  let uploadQueue = null;
  let pitchHistory = [];
  let lastPitchCheckAt = 0;
  const archiveNoteSaveDelays = new Map();
  const archiveNoteSaveChains = new Map();

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
    if (shouldAutoFinish(elapsed) && losslessRecorder && !captureFinalizing) stopRecording({ automatic: true });
  }

  async function updateMicrophones() {
    const devices = await navigator.mediaDevices.enumerateDevices();
    const microphones = devices.filter((device) => device.kind === "audioinput");
    const cameras = devices.filter((device) => device.kind === "videoinput");
    const select = $("#microphone-select");
    if (!select) return;
    const selected = select.value || localStorage.getItem(MICROPHONE_STORAGE_KEY) || "";
    select.replaceChildren(new Option("Default microphone", ""));
    microphones.forEach((microphone, index) => {
      select.add(new Option(microphone.label || `Microphone ${index + 1}`, microphone.deviceId));
    });
    if ([...select.options].some((option) => option.value === selected)) select.value = selected;
    const activeName = $("#active-input-name");
    if (activeName) activeName.textContent = select.selectedOptions[0]?.textContent || "Default microphone";
    const cameraSelect = $("#camera-select");
    if (!cameraSelect) return;
    const selectedCamera = cameraSelect.value || localStorage.getItem(CAMERA_STORAGE_KEY) || "";
    cameraSelect.replaceChildren(new Option("Default camera", ""));
    cameras.forEach((camera, index) => cameraSelect.add(new Option(camera.label || `Camera ${index + 1}`, camera.deviceId)));
    if ([...cameraSelect.options].some((option) => option.value === selectedCamera)) cameraSelect.value = selectedCamera;
  }

  function recordingMode() {
    return $("#recording-mode")?.value === "video" ? "video" : "audio";
  }

  function selectedVideoProfile() {
    return $("#video-resolution")?.value === "720"
      ? { width: 1280, height: 720, frameRate: 30, bitsPerSecond: 2500000 }
      : { width: 1920, height: 1080, frameRate: 30, bitsPerSecond: 5000000 };
  }

  function selectedVideoConstraints() {
    const deviceID = $("#camera-select")?.value || "";
    const profile = selectedVideoProfile();
    return {
      ...(deviceID ? { deviceId: { exact: deviceID } } : {}),
      width: { ideal: profile.width },
      height: { ideal: profile.height },
      frameRate: { ideal: profile.frameRate, max: profile.frameRate },
    };
  }

  function syncVideoOptions() {
    const videoEnabled = recordingMode() === "video";
    document.querySelectorAll("[data-video-option]").forEach((field) => { field.hidden = !videoEnabled; });
    const previewShell = $("#camera-preview-shell");
    if (previewShell) previewShell.hidden = !videoEnabled || !(cameraStream?.active || recordingCameraStream?.active);
    const livePreview = $("#live-camera-preview");
    if (livePreview) livePreview.hidden = !videoEnabled || !(cameraStream?.active || recordingCameraStream?.active);
  }

  function attachCameraPreview(activeStream) {
    [$("#camera-preview"), $("#live-camera-preview")].forEach((video) => {
      if (!video) return;
      video.srcObject = activeStream || null;
      if (activeStream) video.play().catch(() => {});
    });
    syncVideoOptions();
  }

  function setMonitorStatus(message, tone = "") {
    const status = $("#input-monitor-status");
    if (!status) return;
    status.textContent = message;
    status.dataset.tone = tone;
  }

  function setSignalStatus(message, tone = "waiting") {
    const status = $("#input-signal-status");
    if (!status) return;
    status.textContent = message;
    status.dataset.tone = tone;
  }

  function setPreflightVisible(visible) {
    const preflight = $("#audio-preflight");
    if (preflight) preflight.hidden = !visible;
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

  function renderTuner(pitch) {
    document.querySelectorAll("[data-tuner]").forEach((tuner) => {
      const note = $("[data-tuner-note]", tuner);
      const frequency = $("[data-tuner-frequency]", tuner);
      const needle = $("[data-tuner-needle]", tuner);
      if (!pitch) {
        note.textContent = "—";
        frequency.textContent = "A4 = 440 Hz";
        needle.style.left = "50%";
        tuner.dataset.tone = "waiting";
        return;
      }
      const roundedCents = Math.round(pitch.cents);
      note.textContent = `${pitch.note}${pitch.octave}`;
      frequency.textContent = Math.abs(roundedCents) <= 4
        ? `${pitch.frequency.toFixed(1)} Hz · in tune`
        : `${pitch.frequency.toFixed(1)} Hz · ${roundedCents > 0 ? "+" : ""}${roundedCents} cents`;
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
    setPreflightVisible(true);
    setSignalStatus("Listening…", "waiting");
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
          renderTuner(null);
          setSignalStatus(level >= 0.006 ? "Signal detected" : "No signal — check input", level >= 0.006 ? "live" : "silent");
        } else {
          pitchHistory.push(detected);
          pitchHistory = pitchHistory.slice(-4);
          const stable = analysis.stablePitch(pitchHistory);
          renderTuner(stable);
          setSignalStatus("Signal detected", "live");
        }
      }
      levelFrame = requestAnimationFrame(render);
    };
    render();
  }

  function stopLiveAnalysis(hide = true) {
    if (levelFrame) cancelAnimationFrame(levelFrame);
    levelFrame = null;
    const inputMeter = $("#input-meter-fill");
    if (inputMeter) inputMeter.style.width = "0";
    audioContext?.close().catch(() => {});
    audioContext = null;
    resetLiveAudio();
    setSignalStatus("Tuner off", "waiting");
    if (hide) setPreflightVisible(false);
  }

  function selectedAudioConstraints() {
    const deviceID = $("#microphone-select")?.value || "";
    return {
      ...(deviceID ? { deviceId: { exact: deviceID } } : {}),
      echoCancellation: false,
      noiseSuppression: false,
      autoGainControl: false,
      channelCount: 1,
    };
  }

  function stopMonitoring() {
    monitorStream?.getTracks().forEach((track) => track.stop());
    monitorStream = null;
    cameraStream?.getTracks().forEach((track) => track.stop());
    cameraStream = null;
    attachCameraPreview(recordingCameraStream?.active ? recordingCameraStream : null);
    if (!stream) stopLiveAnalysis();
    const toggle = $("#toggle-input-monitor");
    if (toggle) toggle.textContent = "Start preview & tuner";
    setMonitorStatus("Input preview stopped.");
  }

  async function startMonitoring() {
    if (stream || captureFinalizing) {
      setMonitorStatus("The selected microphone is already being used by the active take.", "live");
      return;
    }
    if (!navigator.mediaDevices?.getUserMedia) {
      setMonitorStatus("Live microphone monitoring is not supported in this browser.", "error");
      return;
    }
    try {
      if (monitorStream) stopMonitoring();
      setMonitorStatus("Requesting microphone access…");
      monitorStream = await navigator.mediaDevices.getUserMedia({ audio: selectedAudioConstraints() });
      if (recordingMode() === "video") {
        setMonitorStatus("Requesting camera access…");
        cameraStream = await navigator.mediaDevices.getUserMedia({ video: selectedVideoConstraints() });
        attachCameraPreview(cameraStream);
      }
      await updateMicrophones();
      startLevelMeter(monitorStream);
      const toggle = $("#toggle-input-monitor");
      if (toggle) toggle.textContent = "Stop preview & tuner";
      setMonitorStatus(recordingMode() === "video" ? "Camera and microphone ready — play to verify the input." : "Live tuner active — play to verify the selected input.", "live");
    } catch (error) {
      monitorStream?.getTracks().forEach((track) => track.stop());
      monitorStream = null;
      cameraStream?.getTracks().forEach((track) => track.stop());
      cameraStream = null;
      attachCameraPreview(null);
      stopLiveAnalysis();
      setMonitorStatus(error.name === "NotAllowedError" ? "Camera or microphone permission was not granted." : "Could not start the selected camera and microphone.", "error");
    }
  }

  function stopCapture() {
    clearInterval(timerID);
    timerID = null;
    clearTimeout(autoStopID);
    autoStopID = null;
    stream?.getTracks().forEach((track) => track.stop());
    stream = null;
    recordingCameraStream?.getTracks().forEach((track) => track.stop());
    recordingCameraStream = null;
    videoRecordingStream = null;
    attachCameraPreview(cameraStream?.active ? cameraStream : null);
    if (monitorStream?.active) setMonitorStatus("Live tuner active — play to verify the selected input.", "live");
    else stopLiveAnalysis();
    $("#recording-light")?.classList.remove("active");
    const startButton = $("#start-recording");
    const stopButton = $("#stop-recording");
    if (startButton) startButton.disabled = false;
    if (stopButton) stopButton.disabled = true;
  }

  function preferredVideoType() {
    if (!globalThis.MediaRecorder) return "";
    return [
      "video/webm;codecs=vp9,opus",
      "video/webm;codecs=vp8,opus",
      "video/mp4;codecs=avc1.42E01E,mp4a.40.2",
      "video/webm",
      "video/mp4",
    ].find((type) => MediaRecorder.isTypeSupported(type)) || "";
  }

  function startVideoRecording(audioStream, activeCameraStream) {
    const mimeType = preferredVideoType();
    if (!mimeType) throw new Error("Video recording is not supported in this browser");
    videoRecordingStream = new MediaStream([
      ...activeCameraStream.getVideoTracks(),
      ...audioStream.getAudioTracks(),
    ]);
    const profile = selectedVideoProfile();
    videoChunks = [];
    videoRecorder = new MediaRecorder(videoRecordingStream, {
      mimeType,
      videoBitsPerSecond: profile.bitsPerSecond,
      audioBitsPerSecond: 192000,
    });
    videoContentType = videoRecorder.mimeType || mimeType;
    videoRecorder.addEventListener("dataavailable", (event) => {
      if (event.data?.size) videoChunks.push(event.data);
    });
    videoRecorder.start(1000);
  }

  function finishVideoRecording() {
    if (!videoRecorder) return Promise.resolve(null);
    const recorder = videoRecorder;
    const settings = recordingCameraStream?.getVideoTracks()[0]?.getSettings?.() || {};
    return new Promise((resolve, reject) => {
      recorder.addEventListener("error", (event) => reject(event.error || new Error("Video recording failed")), { once: true });
      recorder.addEventListener("stop", () => {
        const blob = new Blob(videoChunks, { type: videoContentType.split(";")[0] });
        const codecMatch = /codecs?=([^;]+)/i.exec(videoContentType);
        videoRecorder = null;
        videoChunks = [];
        resolve({
          blob,
          contentType: videoContentType,
          codec: codecMatch?.[1] || "",
          width: Number(settings.width || selectedVideoProfile().width),
          height: Number(settings.height || selectedVideoProfile().height),
          frameRate: Number(settings.frameRate || selectedVideoProfile().frameRate),
        });
      }, { once: true });
      if (recorder.state === "inactive") recorder.dispatchEvent(new Event("stop"));
      else recorder.stop();
    });
  }

  function discardVideoRecording() {
    if (!videoRecorder) {
      videoChunks = [];
      return Promise.resolve();
    }
    const recorder = videoRecorder;
    videoRecorder = null;
    return new Promise((resolve) => {
      const finish = () => {
        videoChunks = [];
        resolve();
      };
      recorder.addEventListener("stop", finish, { once: true });
      if (recorder.state === "inactive") finish();
      else recorder.stop();
    });
  }

  async function startRecording() {
    if (stream || captureFinalizing) return;
    if (!navigator.mediaDevices?.getUserMedia || !globalThis.JazzLosslessRecorder) {
      setRecorderState("Lossless recording is not supported in this browser", "error");
      activeBlockContext = null;
      return;
    }
    try {
      setRecorderState("Requesting microphone access...", "starting");
      const practiceSession = await globalThis.JazzPracticeSession.ensureActive();
      currentPracticeSessionID = practiceSession.id;
      captureFinalizing = false;
      const monitoring = Boolean(monitorStream?.active);
      stream = monitoring
        ? monitorStream.clone()
        : await navigator.mediaDevices.getUserMedia({ audio: selectedAudioConstraints() });
      if (!monitoring) {
        await updateMicrophones();
        startLevelMeter(stream);
      }
      if (recordingMode() === "video") {
        recordingCameraStream = cameraStream?.active
          ? cameraStream.clone()
          : await navigator.mediaDevices.getUserMedia({ video: selectedVideoConstraints() });
        attachCameraPreview(recordingCameraStream);
      }
      losslessRecorder = new globalThis.JazzLosslessRecorder();
      await losslessRecorder.start(stream);
      activeFxPreset = "";
      if (globalThis.JazzFX?.enabled()) {
        const fxCapture = await globalThis.JazzFX.start(stream);
        fxRecorder = new globalThis.JazzLosslessRecorder();
        await fxRecorder.start(fxCapture.stream);
        activeFxPreset = fxCapture.preset;
      }
      if (recordingCameraStream) startVideoRecording(stream, recordingCameraStream);
      recordedAt = new Date().toISOString();
      startedAt = performance.now();
      updateTimer();
      timerID = setInterval(updateTimer, 250);
      autoStopID = setTimeout(() => stopRecording({ automatic: true }), MAX_TAKE_DURATION_MS);
      setMonitorStatus("Recording with the selected microphone.", "live");
      $("#recording-light")?.classList.add("active");
      const startButton = $("#start-recording");
      const stopButton = $("#stop-recording");
      if (startButton) startButton.disabled = true;
      if (stopButton) stopButton.disabled = false;
      const fxSuffix = activeFxPreset ? " + live FX mix" : "";
      setRecorderState(recordingCameraStream
        ? `Recording video + lossless 24-bit audio${fxSuffix} - play the take`
        : `Recording lossless 24-bit audio${fxSuffix} - play the take`, "recording");
    } catch (error) {
      const recorder = losslessRecorder;
      losslessRecorder = null;
      const failedFxRecorder = fxRecorder;
      fxRecorder = null;
      await Promise.all([
        recorder?.cancel?.().catch(() => {}),
        failedFxRecorder?.cancel?.().catch(() => {}),
        globalThis.JazzFX?.stop().catch(() => {}),
        discardVideoRecording().catch(() => {}),
      ]);
      stopCapture();
      setRecorderState(error.name === "NotAllowedError" ? "Camera or microphone permission was not granted" : `Could not start recording: ${error.message}`, "error");
      activeBlockContext = null;
    }
  }

  function stopRecording(options = {}) {
    if (!losslessRecorder || captureFinalizing) return;
    captureFinalizing = true;
    const stopButton = $("#stop-recording");
    if (stopButton) stopButton.disabled = true;
    const automatic = options.automatic === true;
    setRecorderState(automatic ? "Four-hour limit reached - finishing the take..." : "Building the lossless take...", "processing");
    finishRecording(automatic);
  }

  async function finishRecording(automatic = false) {
    let result;
    let videoResult;
    let fxResult = null;
    try {
      [result, videoResult, fxResult] = await Promise.all([
        losslessRecorder.stop(),
        finishVideoRecording(),
        fxRecorder ? fxRecorder.stop() : Promise.resolve(null),
      ]);
    } catch (error) {
      await Promise.all([
        discardVideoRecording().catch(() => {}),
        fxRecorder?.cancel?.().catch(() => {}),
      ]);
      fxRecorder = null;
      await globalThis.JazzFX?.stop().catch(() => {});
      stopCapture();
      losslessRecorder = null;
      captureFinalizing = false;
      setRecorderState(`Could not finish the lossless take: ${error.message}`, "error");
      activeBlockContext = null;
      return;
    }
    losslessRecorder = null;
    fxRecorder = null;
    await globalThis.JazzFX?.stop().catch(() => {});
    recordedSampleRate = result.sampleRate;
    const { blob, durationMS, waveformPeaks } = result;
    const contentType = "audio/wav";
    stopCapture();
    if (previewURL) URL.revokeObjectURL(previewURL);
    previewURL = URL.createObjectURL(blob);
    const preview = $("#recording-preview");
    if (preview) {
      preview.src = previewURL;
      preview.hidden = false;
    }
    const capture = captureUpload(blob, durationMS, contentType, videoResult, waveformPeaks, fxResult);
    captureFinalizing = false;
    setRecorderState(automatic
      ? "Four-hour take captured - uploading in the background"
      : `${videoResult ? "Video take" : "Take"} captured - uploading in the background`, "complete");
    activeBlockContext = null;
    uploadQueue.enqueue(capture);
  }

  async function cancelRecording() {
    if (!losslessRecorder || captureFinalizing) return false;
    captureFinalizing = true;
    const recorder = losslessRecorder;
    losslessRecorder = null;
    const cancelledFxRecorder = fxRecorder;
    fxRecorder = null;
    setRecorderState("Cancelling and discarding the take...", "cancelling");
    try {
      await Promise.allSettled([
        recorder.cancel(),
        cancelledFxRecorder ? cancelledFxRecorder.cancel() : Promise.resolve(),
        globalThis.JazzFX?.stop() || Promise.resolve(),
        discardVideoRecording(),
      ]);
    } finally {
      stopCapture();
      captureFinalizing = false;
    }
    setRecorderState("Take cancelled - no media was uploaded; practice time was kept", "cancelled");
    activeBlockContext = null;
    return true;
  }

  function captureUpload(blob, durationMS, contentType, videoResult = null, waveformPeaks = [], fxResult = null) {
    const metadataForm = $("#recording-metadata");
    const metadata = metadataForm ? new FormData(metadataForm) : new FormData();
    const blockContext = activeBlockContext ? { ...activeBlockContext } : null;
    return {
      blob,
      durationMS,
      contentType,
      mediaKind: videoResult ? "video" : "audio",
      fxBlob: fxResult?.blob || null,
      fxContentType: fxResult ? "audio/wav" : "",
      fxPreset: fxResult ? activeFxPreset : "",
      videoBlob: videoResult?.blob || null,
      videoContentType: videoResult?.contentType || "",
      videoCodec: videoResult?.codec || "",
      videoWidth: videoResult?.width || 0,
      videoHeight: videoResult?.height || 0,
      videoFrameRate: videoResult?.frameRate || 0,
      recordedAt,
      sampleRate: recordedSampleRate,
      waveformPeaks,
      practiceSessionID: currentPracticeSessionID || globalThis.JazzPracticeSession.currentID(),
      blockContext,
      tuneId: blockContext ? String(blockContext.tuneId || "") : String(metadata.get("tuneId") || ""),
      skillIds: blockContext?.skillIds || (metadata.get("skillId") ? [String(metadata.get("skillId"))] : []),
      takeNumber: blockContext?.takeNumber || Number(metadata.get("takeNumber") || 1),
      notes: blockContext ? "" : String(metadata.get("notes") || ""),
    };
  }

  async function uploadRecording(capture, onProgress) {
    const baseType = capture.contentType.split(";")[0];
    const codecMatch = /codecs?=([^;]+)/i.exec(capture.contentType);
    const initialized = await api("/recordings/init", {
      method: "POST",
      body: JSON.stringify({
        mediaKind: capture.mediaKind,
        contentType: baseType,
        codec: capture.contentType === "audio/wav" ? "pcm_s24le" : (codecMatch?.[1] || ""),
        sizeBytes: capture.blob.size,
        durationMs: capture.durationMS,
        sampleRate: capture.sampleRate,
        channels: 1,
        waveformPeaks: capture.waveformPeaks || [],
        recordedAt: capture.recordedAt,
        practiceSessionId: capture.practiceSessionID,
        practiceBlockId: capture.blockContext?.id || "",
        tuneId: capture.tuneId,
        missionId: DATA.mission.id,
        skillIds: capture.skillIds,
        takeNumber: capture.takeNumber,
        notes: capture.notes,
        videoContentType: capture.videoContentType?.split(";")[0] || "",
        videoCodec: capture.videoCodec,
        videoSizeBytes: capture.videoBlob?.size || 0,
        videoWidth: capture.videoWidth,
        videoHeight: capture.videoHeight,
        videoFrameRate: capture.videoFrameRate,
        fxContentType: capture.fxContentType || "",
        fxSizeBytes: capture.fxBlob?.size || 0,
        fxPreset: capture.fxPreset || "",
      }),
    });
    try {
      const totalBytes = capture.blob.size + (capture.videoBlob?.size || 0) + (capture.fxBlob?.size || 0);
      const progressFor = (offset, assetBytes) => (percent) => {
        const uploaded = offset + ((percent / 100) * assetBytes);
        onProgress(totalBytes ? (uploaded / totalBytes) * 100 : 100);
      };
      await putBlob(initialized.uploadUrl, capture.blob, baseType, progressFor(0, capture.blob.size));
      await completeAsset(initialized.id, "audio");
      if (capture.videoBlob) {
        await putBlob(
          initialized.videoUploadUrl,
          capture.videoBlob,
          capture.videoContentType.split(";")[0],
          progressFor(capture.blob.size, capture.videoBlob.size),
        );
        await completeAsset(initialized.id, "video");
      }
      if (capture.fxBlob) {
        await putBlob(
          initialized.fxUploadUrl,
          capture.fxBlob,
          capture.fxContentType,
          progressFor(capture.blob.size + (capture.videoBlob?.size || 0), capture.fxBlob.size),
        );
        await completeAsset(initialized.id, "fx");
      }
    } catch (error) {
      await api(`/recordings/${initialized.id}`, { method: "DELETE" }).catch(() => {});
      throw error;
    }
  }

  function uploadMessage(job) {
    if (job.status === "queued") return "Take queued for private upload";
    if (job.status === "uploading") return `Uploading privately - ${job.progress}%`;
    if (job.status === "complete") return "Uploaded privately";
    return `Take is safe in this tab. Upload failed: ${job.error}`;
  }

  function handleUploadState(job) {
    const message = uploadMessage(job);
    const blockId = job.payload.blockContext?.id || "";
    dispatchEvent(new CustomEvent("jazz:upload-state", {
      detail: {
        id: job.id,
        blockId,
        takeNumber: job.payload.takeNumber,
        message,
        phase: job.status,
        canRetry: job.status === "failed",
      },
    }));
    if (job.status === "queued" || job.status === "uploading") {
      setServiceStatus(message, "online");
      return;
    }
    if (job.status === "failed") {
      setServiceStatus("Upload needs attention", "offline");
      return;
    }
    setServiceStatus("Private storage connected", "online");
    const takeInput = $('#recording-metadata input[name="takeNumber"]');
    if (takeInput) takeInput.value = String(Math.min(99, Number(takeInput.value || 1) + 1));
    Promise.all([loadRecordings(), globalThis.JazzPracticeSession.refresh()])
      .then(() => dispatchEvent(new CustomEvent("jazz:recordings-changed")))
      .catch(() => setServiceStatus("Recording saved - refresh to update the archive", "offline"));
  }

  function retryUpload(id) {
    return uploadQueue.retry(id);
  }

  async function putBlob(uploadURL, blob, contentType, onProgress = () => {}) {
    if (!uploadURL) throw new Error("storage upload session is missing");
    let offset = 0;
    while (offset < blob.size) {
      const end = Math.min(blob.size, offset + UPLOAD_CHUNK_BYTES);
      const chunk = blob.slice(offset, end, contentType);
      await putChunk(uploadURL, chunk, contentType, offset, end - 1, blob.size, (loaded) => {
        onProgress(((offset + loaded) / blob.size) * 100);
      });
      offset = end;
      onProgress((offset / blob.size) * 100);
    }
  }

  async function completeAsset(recordingID, asset) {
    let lastError;
    for (let attempt = 0; attempt < 3; attempt += 1) {
      try {
        return await api(`/recordings/${recordingID}/complete`, {
          method: "POST",
          body: JSON.stringify({ asset }),
        });
      } catch (error) {
        lastError = error;
        if (attempt < 2) await new Promise((resolve) => setTimeout(resolve, 400 * (attempt + 1)));
      }
    }
    throw lastError;
  }

  function putChunk(uploadURL, blob, contentType, start, end, totalSize, onProgress = () => {}) {
    let lastProgressAt = 0;
    let lastProgressPercent = -1;
    const shouldNotifyProgress = (percent) => {
      const next = Math.round(performance.now());
      if (percent === 100) return true;
      if (percent !== lastProgressPercent && (percent % 10 === 0)) return true;
      if (percent !== lastProgressPercent && next - lastProgressAt > 800) return true;
      return false;
    };

    return new Promise((resolve, reject) => {
      const request = new XMLHttpRequest();
      request.open("PUT", uploadURL);
      request.setRequestHeader("Content-Type", contentType);
      request.setRequestHeader("Content-Range", `bytes ${start}-${end}/${totalSize}`);
      request.upload.addEventListener("progress", (event) => {
        if (!event.lengthComputable) return;
        const percent = Math.round((event.loaded / event.total) * 100);
        const notify = shouldNotifyProgress(percent);
        if (notify) {
          lastProgressPercent = percent;
          lastProgressAt = performance.now();
          onProgress(event.loaded);
        }
      });
      request.addEventListener("load", () => {
        if (request.status === 308 || (request.status >= 200 && request.status < 300)) resolve();
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
        const isVideo = recording.mediaKind === "video";
        const hasFx = Boolean(recording.fxContentType);
        const baseFormat = isVideo
          ? `${recording.videoWidth || ""}${recording.videoHeight ? `×${recording.videoHeight}` : ""} video + lossless WAV`
          : (recording.contentType === "audio/wav" ? "Lossless WAV" : recording.contentType.replace("audio/", "").toUpperCase());
        const format = hasFx ? `${baseFormat} + FX mix` : baseFormat;
        const ready = recording.status === "ready";
        const card = document.createElement("article");
        card.className = "recording-item";
        card.dataset.recordingId = recording.id;
        card.dataset.durationMs = String(Number(recording.durationMs || 0));
        card.innerHTML = `
          <div class="recording-item-top"><span>${new Date(recording.recordedAt).toLocaleDateString()}</span><span>${formatDuration(recording.durationMs)} · ${format}</span></div>
          <h4>${escapeHTML(title)}${recording.takeNumber ? ` · Take ${recording.takeNumber}` : ""}</h4>
          <p data-recording-note-copy>${escapeHTML(recording.notes || "No listening note yet.")}</p>
          <span class="recording-context">${escapeHTML(context)}</span>
          <div class="recording-item-actions">
            <button type="button" class="play-recording" data-asset="${isVideo ? "video" : "audio"}" ${ready ? "" : "disabled"}>${ready ? (isVideo ? "Play video" : "Play") : "Uploading…"}</button>
            ${isVideo ? `<button type="button" class="play-audio-master" data-asset="audio" ${ready ? "" : "disabled"}>Lossless audio</button>` : ""}
            ${hasFx ? `<button type="button" class="play-fx-mix" data-asset="fx" ${ready ? "" : "disabled"}>FX mix${recording.fxPreset ? ` · ${escapeHTML(recording.fxPreset)}` : ""}</button>` : ""}
            <button type="button" class="download-recording" data-download-asset="${isVideo ? "video" : "audio"}" ${ready ? "" : "disabled"}>${isVideo ? "Download video" : "Download"}</button>
            ${isVideo ? `<button type="button" class="download-recording" data-download-asset="audio" ${ready ? "" : "disabled"}>Download WAV</button>` : ""}
            ${hasFx ? `<button type="button" class="download-recording" data-download-asset="fx" ${ready ? "" : "disabled"}>Download FX mix</button>` : ""}
            <button type="button" class="share-recording" data-share-asset="${isVideo ? "video" : "audio"}" ${ready ? "" : "disabled"}>${isVideo ? "Share video" : "Copy share link"}</button>
            ${isVideo ? `<button type="button" class="share-recording" data-share-asset="audio" ${ready ? "" : "disabled"}>Share WAV</button>` : ""}
            ${hasFx ? `<button type="button" class="share-recording" data-share-asset="fx" ${ready ? "" : "disabled"}>Share FX mix</button>` : ""}
            <button type="button" class="delete-recording">Delete</button>
            <button type="button" class="take-note-button" data-take-note-toggle aria-expanded="false">${recording.notes ? "Edit note" : "Take note"}</button>
          </div>
          <label class="take-note-editor" data-take-note-editor hidden>
            <span><strong>Take note</strong><em data-take-note-status>${recording.notes ? "Cloud synced" : "Optional"}</em></span>
            <textarea data-take-note maxlength="2000" rows="3" placeholder="What do you hear in this take?">${escapeHTML(recording.notes || "")}</textarea>
          </label>`;
        card.querySelectorAll("[data-asset]").forEach((button) => {
          button.addEventListener("click", () => playRecording(recording.id, card, button.dataset.asset, button));
        });
        card.querySelectorAll("[data-download-asset]").forEach((button) => {
          button.addEventListener("click", () => downloadRecording(recording.id, button.dataset.downloadAsset, button).catch(() => {}));
        });
        card.querySelectorAll("[data-share-asset]").forEach((button) => {
          button.addEventListener("click", () => shareRecording(recording.id, button.dataset.shareAsset, button).catch(() => {}));
        });
        $(".delete-recording", card).addEventListener("click", () => deleteRecording(recording.id));
        wireArchiveTakeNote(recording, card);
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

  async function playRecording(id, card, asset = "", trigger = null) {
    const button = trigger || $(".play-recording", card) || $("[data-section-play]", card);
    const originalLabel = button.textContent;
    button.disabled = true;
    button.textContent = "Loading...";
    try {
      const query = asset ? `?asset=${encodeURIComponent(asset)}` : "";
      const result = await api(`/recordings/${id}/playback-url${query}`, { method: "POST", body: "{}" });
      const tagName = result.contentType?.startsWith("video/") ? "video" : "audio";
      const existingPlayer = $("audio, video", card);
      existingPlayer?.closest(".video-player-shell")?.remove();
      if (existingPlayer?.isConnected) existingPlayer.remove();
      const player = document.createElement(tagName);
      player.controls = tagName === "audio";
      if (tagName === "video") player.playsInline = true;
      const expectedDurationMS = Number(result.durationMs || card.dataset.durationMs || 0);
      if (tagName === "video" && globalThis.JazzMediaPlayback?.createVideoPlayer) {
        card.appendChild(globalThis.JazzMediaPlayback.createVideoPlayer(player, { expectedDurationMS }));
      } else {
        card.appendChild(player);
      }
      player.src = result.url;
      player.preload = "metadata";
      player.load();
      await player.play();
    } catch (error) {
      setRecorderState(`Playback failed: ${error.message}`);
    } finally {
      button.disabled = false;
      button.textContent = originalLabel;
    }
  }

  async function downloadRecording(id, asset = "", trigger = null) {
    const button = trigger;
    const originalLabel = button?.textContent || "";
    if (button) {
      button.disabled = true;
      button.textContent = "Preparing…";
    }
    try {
      const query = new URLSearchParams({ download: "1" });
      if (asset) query.set("asset", asset);
      const result = await api(`/recordings/${id}/playback-url?${query}`, { method: "POST", body: "{}" });
      const link = document.createElement("a");
      link.href = result.url;
      link.download = result.filename || "jazz-practice-take";
      link.hidden = true;
      document.body.appendChild(link);
      link.click();
      link.remove();
      return result;
    } catch (error) {
      setRecorderState(`Download failed: ${error.message}`);
      throw error;
    } finally {
      if (button) {
        button.disabled = false;
        button.textContent = originalLabel;
      }
    }
  }

  async function copyShareLink(value) {
    if (navigator.clipboard?.writeText) {
      try {
        await navigator.clipboard.writeText(value);
        return;
      } catch {
        // Fall back to a temporary selection for browsers that lose clipboard
        // permission while the share URL request is in flight.
      }
    }
    const input = document.createElement("textarea");
    input.value = value;
    input.setAttribute("readonly", "");
    input.style.position = "fixed";
    input.style.opacity = "0";
    document.body.appendChild(input);
    input.select();
    const copied = document.execCommand("copy");
    input.remove();
    if (!copied) throw new Error("the browser could not copy the link");
  }

  async function shareRecording(id, asset = "", trigger = null) {
    const button = trigger;
    const originalLabel = button?.textContent || "";
    if (button) {
      button.disabled = true;
      button.textContent = "Creating link…";
    }
    let copied = false;
    try {
      const query = asset ? `?asset=${encodeURIComponent(asset)}` : "";
      const result = await api(`/recordings/${id}/share-url${query}`, { method: "POST", body: "{}" });
      await copyShareLink(result.url);
      copied = true;
      setRecorderState("Permanent share link copied to the clipboard");
      return result;
    } catch (error) {
      setRecorderState(`Share link failed: ${error.message}`);
      throw error;
    } finally {
      if (button) {
        button.disabled = false;
        button.textContent = copied ? "Copied!" : originalLabel;
        if (copied) setTimeout(() => {
          if (button.isConnected) button.textContent = originalLabel;
        }, 1600);
      }
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

  async function updateRecordingNote(id, notes) {
    return api(`/recordings/${id}`, {
      method: "PATCH",
      body: JSON.stringify({ notes }),
    });
  }

  function wireArchiveTakeNote(recording, card) {
    const toggle = $("[data-take-note-toggle]", card);
    const editor = $("[data-take-note-editor]", card);
    const textarea = $("[data-take-note]", card);
    const status = $("[data-take-note-status]", card);
    const copy = $("[data-recording-note-copy]", card);
    if (!toggle || !editor || !textarea || !status || !copy) return;

    toggle.addEventListener("click", () => {
      editor.hidden = !editor.hidden;
      toggle.setAttribute("aria-expanded", String(!editor.hidden));
      if (!editor.hidden) textarea.focus();
    });

    const save = () => {
      clearTimeout(archiveNoteSaveDelays.get(recording.id));
      archiveNoteSaveDelays.delete(recording.id);
      const notes = textarea.value.trim();
      if (notes === String(recording.notes || "")) {
        status.textContent = notes ? "Cloud synced" : "Optional";
        status.dataset.tone = notes ? "saved" : "";
        return;
      }
      status.textContent = "Saving…";
      status.dataset.tone = "saving";
      const previous = archiveNoteSaveChains.get(recording.id) || Promise.resolve();
      const next = previous.catch(() => {}).then(async () => {
        const updated = await updateRecordingNote(recording.id, notes);
        recording.notes = updated.notes || "";
        toggle.textContent = recording.notes ? "Edit note" : "Take note";
        copy.textContent = recording.notes || "No listening note yet.";
        status.textContent = "Cloud synced";
        status.dataset.tone = "saved";
      }).catch(() => {
        status.textContent = "Sync pending";
        status.dataset.tone = "pending";
      });
      archiveNoteSaveChains.set(recording.id, next);
    };
    textarea.addEventListener("input", () => {
      status.textContent = "Saving…";
      status.dataset.tone = "saving";
      clearTimeout(archiveNoteSaveDelays.get(recording.id));
      archiveNoteSaveDelays.set(recording.id, setTimeout(save, 650));
    });
    textarea.addEventListener("blur", save);
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
    if (stream || captureFinalizing) return false;
    activeBlockContext = block;
    startRecording();
    return true;
  }

  function startGeneralRecording() {
    if (stream || captureFinalizing) return false;
    activeBlockContext = null;
    startRecording();
    return true;
  }

  globalThis.JazzRecording = {
    startForBlock,
    stop: stopRecording,
    cancel: cancelRecording,
    retry: retryUpload,
    play: playRecording,
    download: downloadRecording,
    share: shareRecording,
    delete: deleteRecording,
    updateNote: updateRecordingNote,
  };

  populateMetadata();
  const recordingModeSelect = $("#recording-mode");
  const cameraSelect = $("#camera-select");
  const videoResolutionSelect = $("#video-resolution");
  if (recordingModeSelect) recordingModeSelect.value = localStorage.getItem(RECORDING_MODE_STORAGE_KEY) === "video" ? "video" : "audio";
  if (videoResolutionSelect) videoResolutionSelect.value = localStorage.getItem(VIDEO_RESOLUTION_STORAGE_KEY) === "720" ? "720" : "1080";
  syncVideoOptions();

  async function restartMonitoringIfActive() {
    if (!monitorStream?.active || stream || captureFinalizing) return;
    stopMonitoring();
    await startMonitoring();
  }

  recordingModeSelect?.addEventListener("change", async () => {
    localStorage.setItem(RECORDING_MODE_STORAGE_KEY, recordingMode());
    syncVideoOptions();
    await restartMonitoringIfActive();
  });
  cameraSelect?.addEventListener("change", async () => {
    localStorage.setItem(CAMERA_STORAGE_KEY, cameraSelect.value);
    await restartMonitoringIfActive();
  });
  videoResolutionSelect?.addEventListener("change", async () => {
    localStorage.setItem(VIDEO_RESOLUTION_STORAGE_KEY, videoResolutionSelect.value);
    await restartMonitoringIfActive();
  });
  const microphoneSelect = $("#microphone-select");
  microphoneSelect?.addEventListener("change", async () => {
    localStorage.setItem(MICROPHONE_STORAGE_KEY, microphoneSelect.value);
    const activeName = $("#active-input-name");
    if (activeName) activeName.textContent = microphoneSelect.selectedOptions[0]?.textContent || "Default microphone";
    if (monitorStream?.active && !stream && !captureFinalizing) {
      stopMonitoring();
      await startMonitoring();
    } else if (stream || captureFinalizing) {
      setMonitorStatus("Input selection saved for the next take.");
    } else {
      setMonitorStatus("Input selected. Start the tuner to verify it.");
    }
  });
  $("#toggle-input-monitor")?.addEventListener("click", () => {
    if (stream || captureFinalizing) {
      setMonitorStatus("Finish the current take before changing live monitoring.");
      return;
    }
    if (monitorStream?.active) stopMonitoring();
    else startMonitoring();
  });
  uploadQueue = new globalThis.JazzUploadQueue(uploadRecording, handleUploadState);
  addEventListener("beforeunload", (event) => {
    if (!uploadQueue.hasPending()) return;
    event.preventDefault();
    event.returnValue = "";
  });
  $("#start-recording")?.addEventListener("click", startGeneralRecording);
  $("#stop-recording")?.addEventListener("click", stopRecording);
  $("#refresh-recordings")?.addEventListener("click", loadRecordings);
  navigator.mediaDevices?.addEventListener?.("devicechange", () => updateMicrophones().catch(() => {}));
  updateMicrophones().catch(() => setMonitorStatus("Microphone list is unavailable until permission is granted."));
  loadRecordings();
})();
