(() => {
  "use strict";

  const DATA = globalThis.JAZZ_DATA;
  const API_BASE = "./api/v1";
  const $ = (selector, root = document) => root.querySelector(selector);

  let stream = null;
  let mediaRecorder = null;
  let chunks = [];
  let startedAt = 0;
  let recordedAt = "";
  let timerID = null;
  let levelFrame = null;
  let audioContext = null;
  let recordedSampleRate = 0;
  let previewURL = null;

  function setServiceStatus(message, tone = "") {
    const element = $("#recording-service-status");
    element.textContent = message;
    element.className = `cloud-status${tone ? ` ${tone}` : ""}`;
  }

  function setRecorderState(message) {
    $("#recording-state").textContent = message;
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

  function preferredMimeType() {
    const choices = [
      "audio/webm;codecs=opus",
      "audio/mp4",
      "audio/ogg;codecs=opus",
      "audio/webm",
    ];
    return choices.find((type) => globalThis.MediaRecorder?.isTypeSupported(type)) || "";
  }

  function formatTimer(milliseconds) {
    const seconds = Math.floor(milliseconds / 1000);
    return `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
  }

  function updateTimer() {
    $("#recording-timer").textContent = formatTimer(performance.now() - startedAt);
  }

  async function updateMicrophones() {
    const devices = await navigator.mediaDevices.enumerateDevices();
    const microphones = devices.filter((device) => device.kind === "audioinput");
    const select = $("#microphone-select");
    const selected = select.value;
    select.replaceChildren(new Option("Default microphone", ""));
    microphones.forEach((microphone, index) => {
      select.add(new Option(microphone.label || `Microphone ${index + 1}`, microphone.deviceId));
    });
    if ([...select.options].some((option) => option.value === selected)) select.value = selected;
  }

  function startLevelMeter(activeStream) {
    const AudioContext = globalThis.AudioContext || globalThis.webkitAudioContext;
    if (!AudioContext) return;
    audioContext = new AudioContext();
    recordedSampleRate = audioContext.sampleRate;
    const analyser = audioContext.createAnalyser();
    analyser.fftSize = 256;
    audioContext.createMediaStreamSource(activeStream).connect(analyser);
    const samples = new Uint8Array(analyser.fftSize);
    const render = () => {
      analyser.getByteTimeDomainData(samples);
      let energy = 0;
      samples.forEach((sample) => {
        const normalized = (sample - 128) / 128;
        energy += normalized * normalized;
      });
      const rms = Math.sqrt(energy / samples.length);
      $("#input-meter-fill").style.width = `${Math.min(100, Math.max(1, rms * 320))}%`;
      levelFrame = requestAnimationFrame(render);
    };
    render();
  }

  function stopCapture() {
    clearInterval(timerID);
    timerID = null;
    if (levelFrame) cancelAnimationFrame(levelFrame);
    levelFrame = null;
    $("#input-meter-fill").style.width = "0";
    stream?.getTracks().forEach((track) => track.stop());
    stream = null;
    audioContext?.close().catch(() => {});
    audioContext = null;
    $("#recording-light").classList.remove("active");
    $("#start-recording").disabled = false;
    $("#stop-recording").disabled = true;
  }

  async function startRecording() {
    if (!navigator.mediaDevices?.getUserMedia || !globalThis.MediaRecorder) {
      setRecorderState("Recording is not supported in this browser");
      return;
    }
    try {
      const deviceID = $("#microphone-select").value;
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
      const mimeType = preferredMimeType();
      mediaRecorder = mimeType ? new MediaRecorder(stream, { mimeType }) : new MediaRecorder(stream);
      chunks = [];
      mediaRecorder.addEventListener("dataavailable", (event) => {
        if (event.data.size) chunks.push(event.data);
      });
      mediaRecorder.addEventListener("stop", finishRecording, { once: true });
      recordedAt = new Date().toISOString();
      startedAt = performance.now();
      mediaRecorder.start(1000);
      updateTimer();
      timerID = setInterval(updateTimer, 250);
      startLevelMeter(stream);
      $("#recording-light").classList.add("active");
      $("#start-recording").disabled = true;
      $("#stop-recording").disabled = false;
      setRecorderState("Recording - play the take");
    } catch (error) {
      stopCapture();
      setRecorderState(error.name === "NotAllowedError" ? "Microphone permission was not granted" : "Could not start the microphone");
    }
  }

  function stopRecording() {
    if (mediaRecorder?.state === "recording") {
      mediaRecorder.stop();
      setRecorderState("Preparing the take...");
    }
  }

  async function finishRecording() {
    const durationMS = Math.max(1, Math.round(performance.now() - startedAt));
    const contentType = mediaRecorder.mimeType || chunks[0]?.type || "audio/webm";
    const blob = new Blob(chunks, { type: contentType });
    stopCapture();
    if (!blob.size) {
      setRecorderState("The take was empty - try again");
      return;
    }
    if (previewURL) URL.revokeObjectURL(previewURL);
    previewURL = URL.createObjectURL(blob);
    const preview = $("#recording-preview");
    preview.src = previewURL;
    preview.hidden = false;
    setRecorderState("Take captured - starting private upload");
    try {
      await uploadRecording(blob, durationMS, contentType);
      setRecorderState("Uploaded privately");
      setServiceStatus("Private storage connected", "online");
      const takeInput = $('#recording-metadata input[name="takeNumber"]');
      takeInput.value = String(Math.min(99, Number(takeInput.value || 1) + 1));
      await loadRecordings();
    } catch (error) {
      setRecorderState(`Saved for preview, but upload failed: ${error.message}`);
      setServiceStatus("Upload needs attention", "offline");
    }
  }

  async function uploadRecording(blob, durationMS, contentType) {
    const metadata = new FormData($("#recording-metadata"));
    const baseType = contentType.split(";")[0];
    const codecMatch = /codecs?=([^;]+)/i.exec(contentType);
    const initialized = await api("/recordings/init", {
      method: "POST",
      body: JSON.stringify({
        contentType: baseType,
        codec: codecMatch?.[1] || "",
        sizeBytes: blob.size,
        durationMs: durationMS,
        sampleRate: recordedSampleRate,
        channels: 1,
        recordedAt,
        practiceSessionId: "",
        tuneId: String(metadata.get("tuneId") || ""),
        missionId: DATA.mission.id,
        skillIds: metadata.get("skillId") ? [String(metadata.get("skillId"))] : [],
        takeNumber: Number(metadata.get("takeNumber") || 1),
        notes: String(metadata.get("notes") || ""),
      }),
    });
    await putBlob(initialized.uploadUrl, blob, baseType);
    await api(`/recordings/${initialized.id}/complete`, { method: "POST", body: "{}" });
  }

  function putBlob(uploadURL, blob, contentType) {
    return new Promise((resolve, reject) => {
      const request = new XMLHttpRequest();
      request.open("PUT", uploadURL);
      request.setRequestHeader("Content-Type", contentType);
      request.setRequestHeader("Content-Range", `bytes 0-${blob.size - 1}/${blob.size}`);
      request.upload.addEventListener("progress", (event) => {
        if (event.lengthComputable) setRecorderState(`Uploading privately - ${Math.round((event.loaded / event.total) * 100)}%`);
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
      library.replaceChildren();
      if (!result.recordings.length) {
        library.innerHTML = '<p class="empty-recordings">No recordings yet. Your first honest take is the baseline.</p>';
        return;
      }
      result.recordings.forEach((recording) => {
        const tune = DATA.repertoire.find((item) => item.id === recording.tuneId)?.title || "Open practice";
        const card = document.createElement("article");
        card.className = "recording-item";
        card.innerHTML = `
          <div class="recording-item-top"><span>${new Date(recording.recordedAt).toLocaleDateString()}</span><span>${formatDuration(recording.durationMs)}</span></div>
          <h4>${escapeHTML(tune)}${recording.takeNumber ? ` - Take ${recording.takeNumber}` : ""}</h4>
          <p>${escapeHTML(recording.notes || "No listening note yet.")}</p>
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

  async function playRecording(id, card) {
    const button = $(".play-recording", card);
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
    tuneSelect.add(new Option("Open practice", ""));
    DATA.repertoire.forEach((tune) => tuneSelect.add(new Option(tune.title, tune.id, tune.current, tune.current)));
    const skillSelect = $("#recording-skill");
    skillSelect.add(new Option("General musicianship", ""));
    DATA.skills.forEach((skill) => skillSelect.add(new Option(`${skill.name} - ${skill.track}`, skill.id)));
  }

  populateMetadata();
  $("#start-recording").addEventListener("click", startRecording);
  $("#stop-recording").addEventListener("click", stopRecording);
  $("#refresh-recordings").addEventListener("click", loadRecordings);
  navigator.mediaDevices?.addEventListener?.("devicechange", updateMicrophones);
  loadRecordings();
})();
