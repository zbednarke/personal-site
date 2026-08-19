(() => {
  "use strict";

  const API_BASE = "./api/v1";
  const MAX_DRILL_MS = 4 * 60 * 60 * 1000;
  const PRACTICE_SECTION_ID = "blue-bossa-guide-tones";
  const INPUT_STORAGE_KEY = "jazz-guide-tone-input";
  const E = globalThis.JazzGuideToneEngine;
  const A = globalThis.JazzAudioAnalysis;
  const tune = globalThis.JAZZ_DATA?.tunes?.find((candidate) => candidate.id === "blue-bossa");
  if (!E || !A || !tune) return;

  const $ = (selector, root = document) => root.querySelector(selector);
  const NOTE_NAMES = ["C", "C♯", "D", "E♭", "E", "F", "F♯", "G", "A♭", "A", "B♭", "B"];
  const state = {
    initialized: false,
    running: false,
    starting: false,
    events: E.flattenTune(tune),
    eventIndex: 0,
    degree: 3,
    chorus: 1,
    promptStartedAt: 0,
    startedAt: 0,
    elapsedMS: 0,
    drill: null,
    stream: null,
    audioContext: null,
    analyser: null,
    samples: null,
    pitchHistory: [],
    lastAnalysisAt: 0,
    lastSoundAt: 0,
    latched: false,
    transitionPending: false,
    promptAttempted: false,
    promptCorrect: false,
    attempts: 0,
    correct: 0,
    streak: 0,
    responseTotal: 0,
    responseCount: 0,
    animationFrame: 0,
    eventTimer: 0,
    heartbeat: 0,
    autoFinish: 0,
    attemptSaveChain: Promise.resolve(),
    practiceTimerStarted: false,
  };

  async function api(path, options = {}) {
    const response = await fetch(`${API_BASE}${path}`, {
      ...options,
      headers: {
        ...(options.body ? { "Content-Type": "application/json" } : {}),
        ...(options.headers || {}),
      },
    });
    const body = response.status === 204 ? null : await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body?.error || `Request failed (${response.status})`);
    return body;
  }

  function setting(id) {
    return $(id)?.value || "";
  }

  function instrument() {
    return setting("#guide-tone-instrument") || "bb-trumpet";
  }

  function mode() {
    return setting("#guide-tone-mode") || "learn";
  }

  function tempo() {
    return Number(setting("#guide-tone-tempo") || 72);
  }

  function elapsedMS() {
    return Math.min(MAX_DRILL_MS, state.elapsedMS + (state.running ? Date.now() - state.startedAt : 0));
  }

  function formatClock(milliseconds) {
    const totalSeconds = Math.max(0, Math.floor(Number(milliseconds || 0) / 1000));
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const seconds = totalSeconds % 60;
    return hours ? `${hours}:${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}` : `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
  }

  function currentEvent() {
    return state.events[state.eventIndex];
  }

  function writtenNoteName(nearestMidi) {
    const transposition = instrument() === "bb-trumpet" ? 2 : 0;
    return NOTE_NAMES[E.modulo(nearestMidi + transposition)];
  }

  function chooseDegree() {
    state.degree = Math.random() < 0.5 ? 3 : 7;
  }

  function renderChart() {
    const chart = $("#guide-tone-chart");
    if (!chart) return;
    chart.replaceChildren();
    tune.bars.forEach((bar, barIndex) => {
      const cell = document.createElement("div");
      cell.className = "guide-tone-bar";
      cell.dataset.guideToneBar = String(barIndex);
      const symbols = bar.map((chord, chordIndex) => `<strong data-guide-tone-chord-index="${chordIndex}">${E.chordSymbol(chord, instrument())}</strong>`).join("<i></i>");
      cell.innerHTML = `<span>${String(barIndex + 1).padStart(2, "0")}</span><div>${symbols}</div>`;
      chart.appendChild(cell);
    });
    $("#guide-tone-concert-note").textContent = instrument() === "bb-trumpet" ? "Written pitch for B♭ trumpet" : "Concert pitch";
    renderPrompt();
  }

  function renderPrompt() {
    const event = currentEvent();
    if (!event) return;
    $("#guide-tone-chord").textContent = E.chordSymbol(event, instrument());
    $("#guide-tone-degree").textContent = state.degree === 3 ? "3rd" : "7th";
    $("#guide-tone-location").textContent = `Bar ${event.measureNumber} of 16 · Chorus ${state.chorus}`;
    document.querySelectorAll("[data-guide-tone-bar]").forEach((bar) => {
      bar.classList.toggle("current", Number(bar.dataset.guideToneBar) === event.barIndex);
    });
    document.querySelectorAll("[data-guide-tone-chord-index]").forEach((symbol) => symbol.removeAttribute("aria-current"));
    const activeBar = $(`[data-guide-tone-bar="${event.barIndex}"]`);
    $(`[data-guide-tone-chord-index="${event.chordIndex}"]`, activeBar)?.setAttribute("aria-current", "true");
  }

  function setFeedback(tone, note, copy, detail) {
    const feedback = $("#guide-tone-feedback");
    feedback.dataset.tone = tone;
    $("#guide-tone-detected-note").textContent = note || "—";
    $("#guide-tone-feedback-copy").textContent = copy;
    $("#guide-tone-feedback-detail").textContent = detail;
  }

  function updateStats() {
    const accuracy = state.attempts ? `${Math.round((state.correct / state.attempts) * 100)}%` : "—";
    $("#guide-tone-accuracy").textContent = accuracy;
    $("#guide-tone-streak").textContent = String(state.streak);
    $("#guide-tone-response").textContent = state.responseCount ? `${(state.responseTotal / state.responseCount / 1000).toFixed(1)}s` : "—";
    $("#guide-tone-attempts").textContent = String(state.attempts);
  }

  function markBar(tone) {
    const event = currentEvent();
    const bar = $(`[data-guide-tone-bar="${event.barIndex}"]`);
    if (!bar) return;
    bar.classList.remove("correct", "wrong");
    void bar.offsetWidth;
    bar.classList.add(tone);
  }

  function attemptPayload(event, target, pitch, correct) {
    return {
      measureNumber: event.measureNumber,
      chordIndex: event.chordIndex,
      chordSymbol: E.chordSymbol(event, instrument()),
      targetDegree: state.degree,
      expectedPitchClass: target.concertPitchClass,
      playedMidi: pitch ? pitch.nearestMidi : null,
      playedPitchClass: pitch ? E.modulo(pitch.nearestMidi) : null,
      cents: pitch ? Math.round(pitch.cents * 10) / 10 : null,
      correct,
      responseMs: Math.min(120000, Math.max(0, Date.now() - state.promptStartedAt)),
      occurredAt: new Date().toISOString(),
    };
  }

  function saveAttempt(payload) {
    if (!state.drill?.id) return Promise.resolve();
    const drillID = state.drill.id;
    state.attemptSaveChain = state.attemptSaveChain.catch(() => {}).then(() => api(`/guide-tone-drills/${drillID}/attempts`, { method: "POST", body: JSON.stringify(payload) })).catch(() => {
      $("#guide-tone-mic-status").textContent = "Attempt captured locally; cloud sync missed this response.";
      $("#guide-tone-mic-status").dataset.tone = "error";
    });
    return state.attemptSaveChain;
  }

  function gradeStablePitch(pitch) {
    if (!state.running || state.latched || state.transitionPending || (mode() === "tempo" && state.promptCorrect)) return;
    const event = currentEvent();
    const target = E.targetForChord(event, state.degree, instrument());
    const result = E.gradePitch(pitch.nearestMidi, target.concertPitchClass);
    if (!result) return;
    const responseMS = Math.max(0, Date.now() - state.promptStartedAt);
    state.latched = true;
    state.promptAttempted = true;
    state.attempts += 1;
    const heard = writtenNoteName(pitch.nearestMidi);
    if (result.correct) {
      state.correct += 1;
      state.streak += 1;
      state.responseTotal += responseMS;
      state.responseCount += 1;
      state.promptCorrect = true;
      markBar("correct");
      setFeedback("correct", heard, "Correct", `${target.displayName} is the ${state.degree === 3 ? "third" : "seventh"}${instrument() === "bb-trumpet" ? ` · sounds ${target.concertName}` : ""}.`);
    } else {
      state.streak = 0;
      markBar("wrong");
      setFeedback("wrong", heard, "Not this landing spot", `Stay on ${E.chordSymbol(event, instrument())} and try the ${state.degree === 3 ? "third" : "seventh"} again.`);
    }
    updateStats();
    saveAttempt(attemptPayload(event, target, pitch, result.correct));
    if (result.correct && mode() === "learn") {
      state.transitionPending = true;
      setTimeout(() => {
        if (!state.running) return;
        state.transitionPending = false;
        advancePrompt();
      }, 650);
    }
  }

  function resetPrompt() {
    chooseDegree();
    state.promptStartedAt = Date.now();
    state.promptAttempted = false;
    state.promptCorrect = false;
    state.pitchHistory = [];
    state.latched = false;
    setFeedback("listening", "—", "Listening", "Play the requested guide tone in any octave.");
    renderPrompt();
  }

  function scheduleTempoEvent() {
    clearTimeout(state.eventTimer);
    if (!state.running || mode() !== "tempo") return;
    state.eventTimer = setTimeout(async () => {
      if (!state.running) return;
      if (!state.promptAttempted) {
        const event = currentEvent();
        const target = E.targetForChord(event, state.degree, instrument());
        state.attempts += 1;
        state.streak = 0;
        updateStats();
        await saveAttempt(attemptPayload(event, target, null, false));
      }
      advancePrompt();
    }, E.eventDurationMS(currentEvent(), tempo()));
  }

  function advancePrompt() {
    state.eventIndex += 1;
    if (state.eventIndex >= state.events.length) {
      state.eventIndex = 0;
      state.chorus += 1;
      document.querySelectorAll("[data-guide-tone-bar]").forEach((bar) => bar.classList.remove("correct", "wrong"));
    }
    resetPrompt();
    scheduleTempoEvent();
  }

  function analyse(now) {
    if (!state.running || !state.analyser) return;
    state.animationFrame = requestAnimationFrame(analyse);
    if (now - state.lastAnalysisAt < 75) return;
    state.lastAnalysisAt = now;
    state.analyser.getFloatTimeDomainData(state.samples);
    const level = A.rms(state.samples);
    $("#guide-tone-signal-fill").style.width = `${Math.min(100, level * 850)}%`;
    $("#guide-tone-clock").textContent = formatClock(elapsedMS());
    const detected = A.detectPitch(state.samples, state.audioContext.sampleRate, { minimumFrequency: 65, maximumFrequency: 1400, minimumRMS: 0.012, analysisSize: 2048 });
    if (!detected || detected.clarity < 0.82) {
      state.pitchHistory = [];
      if (level < 0.009 && now - state.lastSoundAt > 240) state.latched = false;
      return;
    }
    state.lastSoundAt = now;
    state.pitchHistory.push(detected);
    if (state.pitchHistory.length > 4) state.pitchHistory.shift();
    const pitch = A.stablePitch(state.pitchHistory, 22);
    if (pitch) gradeStablePitch(pitch);
  }

  async function populateInputs() {
    if (!navigator.mediaDevices?.enumerateDevices) return;
    const select = $("#guide-tone-input");
    const selected = select.value || localStorage.getItem(INPUT_STORAGE_KEY) || "";
    const devices = (await navigator.mediaDevices.enumerateDevices()).filter((device) => device.kind === "audioinput");
    select.replaceChildren(new Option("Default microphone", ""));
    devices.forEach((device, index) => select.add(new Option(device.label || `Microphone ${index + 1}`, device.deviceId)));
    if ([...select.options].some((option) => option.value === selected)) select.value = selected;
  }

  async function waitForPracticeContext() {
    let context = globalThis.JazzPracticeTimer?.context?.(PRACTICE_SECTION_ID) || null;
    for (let attempt = 0; attempt < 30 && !context?.ready; attempt += 1) {
      await new Promise((resolve) => setTimeout(resolve, 100));
      context = globalThis.JazzPracticeTimer?.context?.(PRACTICE_SECTION_ID) || context;
    }
    return context;
  }

  async function openMicrophone() {
    const deviceID = $("#guide-tone-input").value;
    state.stream = await navigator.mediaDevices.getUserMedia({
      audio: {
        ...(deviceID ? { deviceId: { exact: deviceID } } : {}),
        channelCount: 1,
        echoCancellation: false,
        noiseSuppression: false,
        autoGainControl: false,
      },
      video: false,
    });
    state.audioContext = new AudioContext();
    await state.audioContext.resume();
    state.analyser = state.audioContext.createAnalyser();
    state.analyser.fftSize = 4096;
    state.analyser.smoothingTimeConstant = 0;
    state.samples = new Float32Array(state.analyser.fftSize);
    state.audioContext.createMediaStreamSource(state.stream).connect(state.analyser);
    await populateInputs();
    const track = state.stream.getAudioTracks()[0];
    $("#guide-tone-mic-status").textContent = `Listening through ${track?.label || "the selected microphone"}.`;
    $("#guide-tone-mic-status").dataset.tone = "live";
  }

  function closeMicrophone() {
    cancelAnimationFrame(state.animationFrame);
    state.animationFrame = 0;
    state.stream?.getTracks().forEach((track) => track.stop());
    state.stream = null;
    state.audioContext?.close().catch(() => {});
    state.audioContext = null;
    state.analyser = null;
    $("#guide-tone-signal-fill").style.width = "0";
  }

  async function syncDrill(endedAt = null, keepalive = false) {
    if (!state.drill?.id) return;
    const body = JSON.stringify({ elapsedMs: Math.round(elapsedMS()), endedAt });
    if (keepalive) {
      fetch(`${API_BASE}/guide-tone-drills/${state.drill.id}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body, keepalive: true }).catch(() => {});
      return;
    }
    await api(`/guide-tone-drills/${state.drill.id}`, { method: "PATCH", body });
  }

  async function start() {
    if (state.running || state.starting) return;
    state.starting = true;
    const button = $("#guide-tone-start");
    button.disabled = true;
    button.textContent = "Connecting…";
    $("#guide-tone-mic-status").dataset.tone = "";
    try {
      if (!navigator.mediaDevices?.getUserMedia) throw new Error("This browser cannot access a microphone");
      const sectionContext = await waitForPracticeContext();
      if (sectionContext?.recorderBusy) throw new Error("Finish the current take before starting this trainer");
      const practiceSession = await globalThis.JazzPracticeSession?.ensureActive?.();
      state.drill = await api("/guide-tone-drills", {
        method: "POST",
        body: JSON.stringify({
          practiceSessionId: practiceSession?.id || "",
          practiceBlockId: sectionContext?.practiceBlockID || "",
          tuneId: tune.id,
          instrument: instrument(),
          mode: mode(),
          tempo: tempo(),
          startedAt: new Date().toISOString(),
        }),
      });
      try {
        await openMicrophone();
        if (typeof globalThis.JazzPracticeTimer?.begin !== "function") throw new Error("Today’s practice timer is still connecting");
        await globalThis.JazzPracticeTimer.begin(PRACTICE_SECTION_ID);
        state.practiceTimerStarted = true;
      } catch (error) {
        closeMicrophone();
        await api(`/guide-tone-drills/${state.drill.id}`, { method: "PATCH", body: JSON.stringify({ elapsedMs: 0, endedAt: new Date().toISOString() }) }).catch(() => {});
        state.drill = null;
        throw error;
      }
      state.running = true;
      state.startedAt = Date.now();
      state.elapsedMS = 0;
      state.eventIndex = 0;
      state.chorus = 1;
      state.attempts = 0;
      state.correct = 0;
      state.streak = 0;
      state.responseTotal = 0;
      state.responseCount = 0;
      state.attemptSaveChain = Promise.resolve();
      resetPrompt();
      updateStats();
      $("#guide-tone-start").hidden = true;
      $("#guide-tone-stop").hidden = false;
      document.querySelectorAll(".guide-tone-toolbar select, .guide-tone-toolbar input").forEach((control) => { control.disabled = true; });
      state.animationFrame = requestAnimationFrame(analyse);
      state.heartbeat = setInterval(() => {
        globalThis.JazzPracticeTimer?.checkpoint?.(PRACTICE_SECTION_ID).catch(() => {});
        syncDrill().catch(() => {});
      }, 30000);
      state.autoFinish = setTimeout(() => stop(), MAX_DRILL_MS);
      scheduleTempoEvent();
    } catch (error) {
      $("#guide-tone-mic-status").textContent = `Could not start: ${error.message}`;
      $("#guide-tone-mic-status").dataset.tone = "error";
      setFeedback("idle", "—", "Start failed", "Check the microphone choice and try again.");
    } finally {
      state.starting = false;
      button.disabled = false;
      button.textContent = "Start drill";
    }
  }

  async function stop() {
    if (!state.running) return;
    const finishedAt = new Date().toISOString();
    state.elapsedMS = elapsedMS();
    state.running = false;
    clearTimeout(state.eventTimer);
    clearInterval(state.heartbeat);
    clearTimeout(state.autoFinish);
    closeMicrophone();
    $("#guide-tone-stop").disabled = true;
    $("#guide-tone-stop").textContent = "Saving…";
    $("#guide-tone-clock").textContent = formatClock(state.elapsedMS);
    try {
      await state.attemptSaveChain.catch(() => {});
      if (state.practiceTimerStarted) await globalThis.JazzPracticeTimer?.end?.(PRACTICE_SECTION_ID).catch(() => {});
      state.practiceTimerStarted = false;
      await syncDrill(finishedAt);
      $("#guide-tone-mic-status").textContent = "Drill saved to today’s practice session.";
      $("#guide-tone-mic-status").dataset.tone = "live";
      setFeedback("idle", "—", "Session complete", `${state.correct} correct from ${state.attempts} attempts.`);
      await loadSummary();
    } catch (error) {
      $("#guide-tone-mic-status").textContent = `Drill ended; final sync failed: ${error.message}`;
      $("#guide-tone-mic-status").dataset.tone = "error";
    } finally {
      state.drill = null;
      $("#guide-tone-stop").hidden = true;
      $("#guide-tone-stop").disabled = false;
      $("#guide-tone-stop").textContent = "Finish & save";
      $("#guide-tone-start").hidden = false;
      document.querySelectorAll(".guide-tone-toolbar select, .guide-tone-toolbar input").forEach((control) => { control.disabled = false; });
      $("#guide-tone-tempo").disabled = mode() !== "tempo";
    }
  }

  async function loadSummary() {
    try {
      const summary = await api(`/guide-tone-drills/summary?tuneId=${encodeURIComponent(tune.id)}`);
      $("#guide-tone-history-accuracy").textContent = summary.attemptCount ? `${summary.accuracy}% accurate` : "No attempts yet";
      $("#guide-tone-history-detail").textContent = summary.attemptCount ? `${summary.correctCount} / ${summary.attemptCount} correct · ${summary.drillCount} drills` : "Blue Bossa";
    } catch {
      $("#guide-tone-history-accuracy").textContent = "History unavailable";
      $("#guide-tone-history-detail").textContent = "The practice tool is ready";
    }
  }

  async function initialize() {
    if (state.initialized) return;
    state.initialized = true;
    renderChart();
    updateStats();
    await populateInputs().catch(() => {});
    await loadSummary();
  }

  $("#guide-tone-start")?.addEventListener("click", start);
  $("#guide-tone-stop")?.addEventListener("click", stop);
  $("#guide-tone-instrument")?.addEventListener("change", renderChart);
  $("#guide-tone-mode")?.addEventListener("change", () => {
    $("#guide-tone-tempo").disabled = mode() !== "tempo";
  });
  $("#guide-tone-tempo")?.addEventListener("input", () => { $("#guide-tone-tempo-value").textContent = `${tempo()} BPM`; });
  $("#guide-tone-input")?.addEventListener("change", () => localStorage.setItem(INPUT_STORAGE_KEY, $("#guide-tone-input").value));
  document.addEventListener("jazz:view-change", (event) => {
    if (event.detail?.view === "guide-tones") initialize();
    else if (state.running) stop();
  });
  addEventListener("pagehide", () => {
    if (!state.running) return;
    state.elapsedMS = elapsedMS();
    state.running = false;
    closeMicrophone();
    if (state.practiceTimerStarted) Promise.resolve(globalThis.JazzPracticeTimer?.end?.(PRACTICE_SECTION_ID)).catch(() => {});
    state.practiceTimerStarted = false;
    syncDrill(new Date().toISOString(), true);
  });

  if (location.hash === "#guide-tones") initialize();
  $("#guide-tone-tempo").disabled = true;
})();
