(() => {
  "use strict";

  const DATA = globalThis.JAZZ_DATA;
  const STORAGE_KEY = "zach-jazz-project-v1";
  const SYNC_META_KEY = "zach-jazz-project-sync-v1";
  const OUTBOX_KEY = "zach-jazz-project-outbox-v1";
  const CLOUD_BOUND_KEY = "zach-jazz-project-cloud-bound-v1";
  const TIMER_STORAGE_KEY = "zach-jazz-practice-timers-v2";
  const API_BASE = "./api/v1";
  const MAX_SKILL_LEVEL = 4;
  const MAX_TAKES_PER_SECTION = 20;
  const MAX_SECTION_PRACTICE_MS = MAX_TAKES_PER_SECTION * 4 * 60 * 60 * 1000;
  const stateDefaults = {
    version: DATA.version,
    skillLevels: {},
    objectives: {},
    repertoire: {},
    bosses: {},
    scene: {},
    practice: [],
    peopleCanCall: 0,
  };

  let state = loadState();
  let activeTrack = "all";
  let practiceSections = DATA.sessions.map((session, position) => ({ ...session, position }));
  let activeSkillId = null;
  let toastTimer = null;
  let syncRevision = loadSyncRevision();
  let syncOutbox = loadOutbox();
  let syncInFlight = false;
  let syncPaused = false;
  let timerState = loadTimerState();
  const cloudLoggingTimers = new Set();
  const timerSaveChains = new Map();
  let guidedBlocks = new Map();
  let guidedBlocksReady = false;
  const noteSaveDelays = new Map();
  const takeNoteSaveDelays = new Map();
  const takeNoteSaveChains = new Map();
  let activeSectionRecordingID = "";
  let activeSectionRecordingMessage = "";
  let activeSectionRecordingPhase = "";
  const sectionUploadJobs = new Map();
  let recordingTimerSessionID = "";
  let selectedPracticeSectionID = "";

  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

  function normalizeState(saved) {
    if (!saved || typeof saved !== "object") return structuredClone(stateDefaults);
    const people = Number(saved.peopleCanCall || 0);
    const practice = Array.isArray(saved.practice)
      ? saved.practice.flatMap((entry) => {
          if (!entry || typeof entry !== "object" || !/^\d{4}-\d{2}-\d{2}$/.test(String(entry.date || ""))) return [];
          const minutes = Number(entry.minutes);
          if (!Number.isFinite(minutes) || minutes <= 0) return [];
          return [{
            id: String(entry.id || `imported-${crypto.randomUUID()}`),
            date: String(entry.date),
            minutes: Math.min(360, Math.round(minutes * 10000) / 10000),
            track: String(entry.track || "trumpet").slice(0, 30),
            note: String(entry.note || "").slice(0, 100),
            preset: Boolean(entry.preset),
          }];
        })
      : [];
    return {
      ...structuredClone(stateDefaults),
      ...saved,
      skillLevels: { ...(saved.skillLevels || {}) },
      objectives: { ...(saved.objectives || {}) },
      repertoire: { ...(saved.repertoire || {}) },
      bosses: { ...(saved.bosses || {}) },
      scene: { ...(saved.scene || {}) },
      practice,
      peopleCanCall: Number.isFinite(people) ? Math.max(0, Math.min(999, Math.round(people))) : 0,
    };
  }

  function loadState() {
    try {
      const saved = JSON.parse(localStorage.getItem(STORAGE_KEY) || "null");
      return normalizeState(saved);
    } catch {
      return structuredClone(stateDefaults);
    }
  }

  function persistState() {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
  }

  function saveState(eventType = "campaign.changed") {
    persistState();
    syncOutbox.push({
      clientMutationId: crypto.randomUUID(),
      eventType,
      state: structuredClone(state),
    });
    if (syncOutbox.length > 200) syncOutbox = syncOutbox.slice(-200);
    saveOutbox();
    flushOutbox();
  }

  function loadSyncRevision() {
    try {
      return Math.max(0, Number(JSON.parse(localStorage.getItem(SYNC_META_KEY) || "{}").revision || 0));
    } catch {
      return 0;
    }
  }

  function saveSyncRevision() {
    localStorage.setItem(SYNC_META_KEY, JSON.stringify({ revision: syncRevision, updatedAt: new Date().toISOString() }));
  }

  function loadOutbox() {
    try {
      const saved = JSON.parse(localStorage.getItem(OUTBOX_KEY) || "[]");
      return Array.isArray(saved) ? saved : [];
    } catch {
      return [];
    }
  }

  function saveOutbox() {
    localStorage.setItem(OUTBOX_KEY, JSON.stringify(syncOutbox));
  }

  function hasMeaningfulProgress(candidate) {
    return candidate.practice.length > 0
      || Number(candidate.peopleCanCall) > 0
      || [candidate.skillLevels, candidate.objectives, candidate.repertoire, candidate.bosses, candidate.scene]
        .some((group) => Object.values(group).some(Boolean));
  }

  function setSyncStatus(message, tone = "") {
    const element = document.getElementById("sync-status");
    if (!element) return;
    element.textContent = message;
    element.dataset.tone = tone;
  }

  async function apiRequest(path, options = {}) {
    const response = await fetch(`${API_BASE}${path}`, {
      ...options,
      headers: {
        ...(options.body ? { "Content-Type": "application/json" } : {}),
        ...(options.headers || {}),
      },
    });
    const body = await response.json().catch(() => ({}));
    if (!response.ok) {
      const error = new Error(body.error || `Cloud request failed (${response.status})`);
      error.status = response.status;
      error.body = body;
      throw error;
    }
    return body;
  }

  async function initializeCloudSync() {
    setSyncStatus("Connecting private cloud progress...");
    try {
      const remote = await apiRequest("/state");
      syncRevision = Number(remote.revision || 0);
      saveSyncRevision();
      const cloudBound = localStorage.getItem(CLOUD_BOUND_KEY) === "1";

      if (syncOutbox.length) {
        localStorage.setItem(CLOUD_BOUND_KEY, "1");
        await flushOutbox();
        return;
      }

      if (remote.hasState) {
        if (hasMeaningfulProgress(state) && !cloudBound) {
          const useCloud = confirm("Cloud progress already exists. Load it here? Cancel keeps this browser copy unchanged so you can export it first.");
          if (!useCloud) {
            syncPaused = true;
            setSyncStatus("Browser copy preserved; cloud sync paused.", "offline");
            return;
          }
        }
        state = normalizeState(remote.state);
        persistState();
        localStorage.setItem(CLOUD_BOUND_KEY, "1");
        renderAll();
        setSyncStatus("Progress saved privately in the cloud.", "online");
        return;
      }

      if (hasMeaningfulProgress(state) && !cloudBound) {
        const migrate = confirm("Move this browser's Jazz Project progress into your private cloud account now?");
        if (!migrate) {
          syncPaused = true;
          setSyncStatus("Progress remains in this browser; cloud sync paused.", "offline");
          return;
        }
        localStorage.setItem(CLOUD_BOUND_KEY, "1");
        saveState("campaign.browser_imported");
        return;
      }

      localStorage.setItem(CLOUD_BOUND_KEY, "1");
      setSyncStatus("Private cloud progress is ready.", "online");
    } catch {
      setSyncStatus("Cloud unavailable; changes are queued safely on this device.", "offline");
    }
  }

  async function flushOutbox() {
    if (syncPaused || syncInFlight || !syncOutbox.length) return;
    syncInFlight = true;
    setSyncStatus(`Syncing ${syncOutbox.length} queued change${syncOutbox.length === 1 ? "" : "s"}...`);
    try {
      while (syncOutbox.length) {
        const mutation = syncOutbox[0];
        try {
          const response = await apiRequest("/sync", {
            method: "POST",
            body: JSON.stringify({ ...mutation, baseRevision: syncRevision }),
          });
          syncRevision = Number(response.revision || syncRevision + 1);
          saveSyncRevision();
          syncOutbox.shift();
          saveOutbox();
        } catch (error) {
          if (error.status === 409 && Number.isFinite(Number(error.body?.revision))) {
            syncRevision = Number(error.body.revision);
            saveSyncRevision();
            continue;
          }
          throw error;
        }
      }
      localStorage.setItem(CLOUD_BOUND_KEY, "1");
      setSyncStatus("Progress saved privately in the cloud.", "online");
    } catch {
      setSyncStatus("Cloud unavailable; changes are queued safely on this device.", "offline");
    } finally {
      syncInFlight = false;
    }
  }

  function localDateKey(date = new Date()) {
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, "0");
    const day = String(date.getDate()).padStart(2, "0");
    return `${year}-${month}-${day}`;
  }

  function addDays(date, amount) {
    const next = new Date(date);
    next.setDate(next.getDate() + amount);
    return next;
  }

  function startOfWeek(date = new Date()) {
    const day = date.getDay();
    const mondayOffset = day === 0 ? -6 : 1 - day;
    const monday = addDays(new Date(date.getFullYear(), date.getMonth(), date.getDate()), mondayOffset);
    monday.setHours(0, 0, 0, 0);
    return monday;
  }

  function liveGuidedMinutesForDate(dateKey) {
    if (timerState.date !== dateKey) return 0;
    return practiceSections.reduce((sum, session) => {
      const timer = timerFor(session);
      const logged = state.practice.find((entry) => entry.id === guidedLogId(session));
      return sum + Math.max(0, (elapsedFor(timer) / 60000) - Number(logged?.minutes || 0));
    }, 0);
  }

  function practiceMinutesForDate(dateKey) {
    const logged = state.practice
      .filter((entry) => entry.date === dateKey)
      .reduce((sum, entry) => sum + Number(entry.minutes || 0), 0);
    return logged + liveGuidedMinutesForDate(dateKey);
  }

  function setText(id, value) {
    const element = document.getElementById(id);
    if (element) element.textContent = value;
  }

  function showToast(message) {
    const toast = $("#toast");
    toast.textContent = message;
    toast.classList.add("show");
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => toast.classList.remove("show"), 2400);
  }

  function escapeHTML(value) {
    const element = document.createElement("span");
    element.textContent = String(value || "");
    return element.innerHTML;
  }

  function renderStats() {
    const totalMinutes = state.practice.reduce((sum, item) => sum + Number(item.minutes || 0), 0);
    const startedTunes = Object.values(state.repertoire).filter((stage) => Number(stage) > 0).length;
    const bosses = Object.values(state.bosses).filter(Boolean).length;
    const skillLevels = Object.values(state.skillLevels).reduce((sum, level) => sum + Number(level || 0), 0);
    const objectives = Object.values(state.objectives).filter(Boolean).length;
    const repertoireStages = Object.values(state.repertoire).reduce((sum, stage) => sum + Number(stage || 0), 0);
    const sceneSteps = Object.values(state.scene).filter(Boolean).length;
    const xp = totalMinutes + skillLevels * 80 + objectives * 35 + repertoireStages * 45 + bosses * 250 + sceneSteps * 90;
    const level = Math.floor(xp / 1000) + 1;

    const skillPart = skillLevels / (DATA.skills.length * MAX_SKILL_LEVEL);
    const missionPart = objectives / DATA.mission.objectives.length;
    const tunePart = repertoireStages / (DATA.repertoire.length * 6);
    const bossPart = bosses / DATA.bosses.length;
    const scenePart = sceneSteps / DATA.sceneSteps.length;
    const progress = Math.round((skillPart * 0.42 + missionPart * 0.16 + tunePart * 0.16 + bossPart * 0.18 + scenePart * 0.08) * 100);

    setText("stat-hours", (totalMinutes / 60).toFixed(1));
    setText("stat-streak", calculateStreak());
    setText("stat-tunes", startedTunes);
    setText("stat-bosses", bosses);
    setText("stat-people", Number(state.peopleCanCall || 0));
    setText("campaign-xp", xp.toLocaleString());
    setText("campaign-level", `LV ${String(level).padStart(2, "0")}`);
    setText("campaign-progress", `${progress}%`);
    $("#campaign-meter").style.width = `${progress}%`;
    setText("skills-earned", skillLevels);
  }

  function calculateStreak() {
    const activeDates = new Set(state.practice.filter((entry) => Number(entry.minutes) > 0).map((entry) => entry.date));
    if (!activeDates.size) return 0;
    let cursor = new Date();
    if (!activeDates.has(localDateKey(cursor))) cursor = addDays(cursor, -1);
    let streak = 0;
    while (activeDates.has(localDateKey(cursor))) {
      streak += 1;
      cursor = addDays(cursor, -1);
    }
    return streak;
  }

  function guidedBlockFor(session) {
    return guidedBlocks.get(session.id) || null;
  }

  function applyPracticeBlocks(blocks) {
    guidedBlocks = new Map((blocks || []).map((block) => [block.blockKey, block]));
    if (!blocks?.length) return;
    practiceSections = [...blocks]
      .sort((a, b) => Number(a.position) - Number(b.position))
      .map((block) => {
        const curriculum = DATA.sessions.find((session) => session.id === block.blockKey) || {};
        const minutes = Number(block.targetMinutes || curriculum.minutes || 10);
        return {
          ...curriculum,
          id: block.blockKey,
          position: Number(block.position),
          time: `${minutes} min`,
          minutes,
          track: block.track,
          category: block.category,
          title: block.title,
          detail: block.instructions || curriculum.detail || "Open practice block.",
          win: curriculum.win || "",
        };
      });
  }

  async function hydrateGuidedBlocks() {
    if (typeof globalThis.JazzPracticeSession?.ensureGuidedBlocks !== "function") {
      setTimeout(hydrateGuidedBlocks, 350);
      return;
    }
    try {
      const definitions = DATA.sessions.map((session, position) => ({
        blockKey: session.id,
        position,
        title: session.title,
        instructions: session.detail,
        category: session.category,
        track: session.track,
        targetMinutes: session.minutes,
      }));
      const result = await globalThis.JazzPracticeSession.ensureGuidedBlocks(localDateKey(), definitions);
      applyPracticeBlocks(result.blocks || []);
      practiceSections.forEach((session) => {
        const block = guidedBlockFor(session);
        if (!block) return;
        const timer = timerFor(session);
        const targetMs = session.minutes * 60 * 1000;
        const localElapsed = Math.max(0, Number(timer.elapsedMs || 0));
        const cloudHasProgress = block.status !== "pending" || Number(block.elapsedMs) > 0;
        if (!cloudHasProgress && localElapsed > 0) {
          timer.running = false;
          timer.startedAt = 0;
          saveTimerBlock(session, timer);
          return;
        }
        const cloudElapsedMs = Math.max(0, Number(block.elapsedMs || 0));
        if (localElapsed > cloudElapsedMs && localElapsed > 0) {
          timer.elapsedMs = Math.min(MAX_SECTION_PRACTICE_MS, localElapsed);
          timer.running = false;
          timer.completed = timer.elapsedMs >= targetMs;
          timer.startedAt = 0;
          if (timer.completed) timer.completedAt = timer.completedAt || block.completedAt || new Date().toISOString();
          saveTimerBlock(session, timer);
          return;
        }
        timer.elapsedMs = Math.min(MAX_SECTION_PRACTICE_MS, cloudElapsedMs);
        timer.completed = block.status === "completed" || cloudElapsedMs >= targetMs;
        timer.running = false;
        timer.startedAt = 0;
        timer.completedAt = block.completedAt || timer.completedAt || "";
        if (block.status === "running") saveTimerBlock(session, timer);
      });
      guidedBlocksReady = true;
      persistTimerState();
      renderSessions();
      tickGuidedTimers();
    } catch {
      guidedBlocksReady = true;
      renderSessions();
    }
  }

  async function saveTimerBlock(session, timer) {
    const block = guidedBlockFor(session);
    if (!block || typeof globalThis.JazzPracticeSession?.updateGuidedBlock !== "function") return;
    const snapshot = {
      elapsedMs: Math.min(MAX_SECTION_PRACTICE_MS, Math.round(timer.elapsedMs)),
      status: timer.running ? "running" : (timer.completed ? "completed" : (timer.elapsedMs > 0 ? "paused" : "pending")),
      timerStartedAt: timer.running && timer.startedAt ? new Date(timer.startedAt).toISOString() : "",
      completedAt: timer.completed && !timer.running ? (timer.completedAt || new Date().toISOString()) : "",
    };
    const previous = timerSaveChains.get(session.id) || Promise.resolve();
    const save = previous.catch(() => {}).then(async () => {
      const currentBlock = guidedBlockFor(session) || block;
      const updated = await globalThis.JazzPracticeSession.updateGuidedBlock(currentBlock.id, snapshot);
      guidedBlocks.set(session.id, { ...currentBlock, ...updated });
    });
    timerSaveChains.set(session.id, save);
    try {
      await save;
      updateSectionSyncStatus(session.id, "Saved", "saved");
    } catch {
      updateSectionSyncStatus(session.id, "Sync pending", "pending");
    } finally {
      if (timerSaveChains.get(session.id) === save) timerSaveChains.delete(session.id);
    }
  }

  function queueBlockNoteSave(session, value) {
    const block = guidedBlockFor(session);
    if (!block) return;
    block.notes = value;
    updateSectionSyncStatus(session.id, "Saving...", "saving");
    clearTimeout(noteSaveDelays.get(session.id));
    noteSaveDelays.set(session.id, setTimeout(() => saveBlockNote(session), 650));
  }

  async function saveBlockNote(session) {
    const block = guidedBlockFor(session);
    if (!block || typeof globalThis.JazzPracticeSession?.updateGuidedBlock !== "function") return;
    clearTimeout(noteSaveDelays.get(session.id));
    noteSaveDelays.delete(session.id);
    try {
      const updated = await globalThis.JazzPracticeSession.updateGuidedBlock(block.id, { notes: block.notes || "" });
      guidedBlocks.set(session.id, { ...block, ...updated });
      updateSectionSyncStatus(session.id, "Saved", "saved");
    } catch {
      updateSectionSyncStatus(session.id, "Sync pending", "pending");
    }
  }

  function updateSectionSyncStatus(sessionID, message, tone = "") {
    document.querySelectorAll(`[data-session-id="${sessionID}"] [data-section-sync]`).forEach((status) => {
      status.textContent = message;
      status.dataset.tone = tone;
    });
  }

  function formatRecordingDuration(milliseconds) {
    if (!milliseconds) return "00:00";
    return formatTimer(milliseconds);
  }

  function sectionRecordingMarkup(block) {
    if (!block) return '<p class="section-empty">Connect the practice session to add section takes.</p>';
    const recordings = Array.isArray(block.recordings) ? block.recordings : [];
    if (!recordings.length) return '<p class="section-empty">No takes yet.</p>';
    return recordings.map((recording, index) => {
      const isVideo = recording.mediaKind === "video";
      const note = String(recording.notes || "");
      return `
        <article class="section-take" data-section-take="${recording.id}" data-duration-ms="${Number(recording.durationMs || 0)}">
          <span>Take ${recording.takeNumber || index + 1} · ${formatRecordingDuration(recording.durationMs)}${isVideo ? " · Video" : ""}${recording.status && recording.status !== "ready" ? ` (${recording.status})` : ""}</span>
          <div>
            <button type="button" data-section-play data-asset="${isVideo ? "video" : "audio"}" ${recording.status === "ready" ? "" : "disabled"}>${isVideo ? "Video" : "Play"}</button>
            ${isVideo ? `<button type="button" data-section-play data-asset="audio" ${recording.status === "ready" ? "" : "disabled"}>Audio</button>` : ""}
            <button type="button" data-section-delete>Delete</button>
            <button class="take-note-button" type="button" data-take-note-toggle aria-expanded="false">${note ? "Edit note" : "Take note"}</button>
          </div>
          <label class="take-note-editor" data-take-note-editor hidden>
            <span><strong>Take note</strong><em data-take-note-status>${note ? "Cloud synced" : "Optional"}</em></span>
            <textarea data-take-note maxlength="2000" rows="3" placeholder="What do you hear in this take?">${escapeHTML(note)}</textarea>
          </label>
        </article>`;
    }).join("");
  }

  function activeBlockRecordings(block) {
    return (block?.recordings || []).filter((recording) => recording.status === "ready" || recording.status === "uploading");
  }

  function uploadJobsForBlock(block) {
    if (!block) return [];
    return [...sectionUploadJobs.values()].filter((job) => job.blockId === block.id);
  }

  function reservedTakeNumbers(block) {
    const numbers = new Set(activeBlockRecordings(block).map((recording) => Number(recording.takeNumber)).filter((number) => Number.isFinite(number) && number > 0));
    uploadJobsForBlock(block).forEach((job) => numbers.add(Number(job.takeNumber)));
    return numbers;
  }

  function activeBlockTakeCount(block) {
    const recordings = activeBlockRecordings(block);
    const recordedTakeNumbers = new Set(recordings.map((recording) => Number(recording.takeNumber)).filter((number) => Number.isFinite(number) && number > 0));
    const transientTakes = uploadJobsForBlock(block).filter((job) => !recordedTakeNumbers.has(Number(job.takeNumber))).length;
    return recordings.length + transientTakes;
  }

  function nextBlockTakeNumber(block) {
    const reserved = reservedTakeNumbers(block);
    for (let takeNumber = 1; takeNumber <= MAX_TAKES_PER_SECTION; takeNumber += 1) {
      if (!reserved.has(takeNumber)) return takeNumber;
    }
    return MAX_TAKES_PER_SECTION + 1;
  }

  function wireTakeNoteEditor(recording, take) {
    const toggle = $("[data-take-note-toggle]", take);
    const editor = $("[data-take-note-editor]", take);
    const textarea = $("[data-take-note]", take);
    const status = $("[data-take-note-status]", take);
    if (!toggle || !editor || !textarea || !status) return;

    toggle.addEventListener("click", () => {
      editor.hidden = !editor.hidden;
      toggle.setAttribute("aria-expanded", String(!editor.hidden));
      if (!editor.hidden) textarea.focus();
    });

    const save = () => {
      clearTimeout(takeNoteSaveDelays.get(recording.id));
      takeNoteSaveDelays.delete(recording.id);
      const notes = textarea.value.trim();
      if (notes === String(recording.notes || "")) {
        status.textContent = notes ? "Cloud synced" : "Optional";
        status.dataset.tone = notes ? "saved" : "";
        return;
      }
      status.textContent = "Saving…";
      status.dataset.tone = "saving";
      const previous = takeNoteSaveChains.get(recording.id) || Promise.resolve();
      const next = previous.catch(() => {}).then(async () => {
        const updated = await globalThis.JazzRecording.updateNote(recording.id, notes);
        recording.notes = updated.notes || "";
        toggle.textContent = recording.notes ? "Edit note" : "Take note";
        status.textContent = "Cloud synced";
        status.dataset.tone = "saved";
      }).catch(() => {
        status.textContent = "Sync pending";
        status.dataset.tone = "pending";
      });
      takeNoteSaveChains.set(recording.id, next);
    };
    textarea.addEventListener("input", () => {
      status.textContent = "Saving…";
      status.dataset.tone = "saving";
      clearTimeout(takeNoteSaveDelays.get(recording.id));
      takeNoteSaveDelays.set(recording.id, setTimeout(save, 650));
    });
    textarea.addEventListener("blur", save);
  }

  function sectionUploadMarkup(block) {
    return uploadJobsForBlock(block).map((job) => `
      <div class="section-upload-job" data-upload-job="${job.id}" data-tone="${job.phase === "failed" ? "error" : "active"}">
        <span>Take ${job.takeNumber} · ${escapeHTML(job.message)}</span>
        ${job.canRetry ? `<button type="button" data-retry-upload="${job.id}">Retry upload</button>` : ""}
      </div>`).join("");
  }

  function wireSectionTools(card, session, block) {
    const notes = $("[data-section-notes]", card);
    if (notes) {
      notes.addEventListener("input", () => queueBlockNoteSave(session, notes.value));
      notes.addEventListener("blur", () => saveBlockNote(session));
    }
    const recordButton = $("[data-section-record]", card);
    if (recordButton) recordButton.addEventListener("click", () => {
      if (activeSectionRecordingID === block?.id) {
        if (activeSectionRecordingPhase === "recording") globalThis.JazzRecording?.stop();
        return;
      }
      if (!block || !globalThis.JazzRecording?.startForBlock) {
        showToast("Section recorder is still connecting");
        return;
      }
      if (activeBlockTakeCount(block) >= MAX_TAKES_PER_SECTION) {
        showToast(`This section already has ${MAX_TAKES_PER_SECTION} recordings`);
        return;
      }
      globalThis.JazzRecording.startForBlock({
        id: block.id,
        title: session.title,
        blockKey: session.id,
        practiceDate: block.practiceDate,
        category: session.category,
        track: session.track,
        tuneId: session.tuneId || "",
        skillIds: session.skillIds || [],
        takeNumber: nextBlockTakeNumber(block),
      });
    });
    $("[data-section-cancel]", card)?.addEventListener("click", () => {
      if (confirm("Cancel and permanently discard this take? It will not be uploaded, but the practice time will remain counted.")) {
        globalThis.JazzRecording?.cancel();
      }
    });
    $$('[data-retry-upload]', card).forEach((button) => {
      button.addEventListener("click", () => globalThis.JazzRecording?.retry(button.dataset.retryUpload));
    });
    $("[data-audio-options]", card)?.addEventListener("click", () => $("#audio-options-dialog")?.showModal());
    $$('[data-section-take]', card).forEach((take) => {
      const recordingID = take.dataset.sectionTake;
      $$('[data-section-play]', take).forEach((button) => {
        button.addEventListener("click", () => globalThis.JazzRecording?.play(recordingID, take, button.dataset.asset, button));
      });
      $("[data-section-delete]", take).addEventListener("click", () => globalThis.JazzRecording?.delete(recordingID));
      const recording = block.recordings.find((candidate) => String(candidate.id) === recordingID);
      if (recording) wireTakeNoteEditor(recording, take);
    });
  }

  function freshTimerState() {
    return { date: localDateKey(), timers: {} };
  }

  function loadTimerState() {
    try {
      const saved = JSON.parse(localStorage.getItem(TIMER_STORAGE_KEY) || "null");
      if (!saved || saved.date !== localDateKey() || typeof saved.timers !== "object") return freshTimerState();
      return saved;
    } catch {
      return freshTimerState();
    }
  }

  function persistTimerState() {
    localStorage.setItem(TIMER_STORAGE_KEY, JSON.stringify(timerState));
  }

  function guidedLogId(session) {
    return `guide-${timerState.date}-${session.id}`;
  }

  function timerFor(session) {
    if (timerState.date !== localDateKey()) timerState = freshTimerState();
    let timer = timerState.timers[session.id];
    if (!timer || typeof timer !== "object") {
      timer = { elapsedMs: 0, running: false, startedAt: 0, completed: false, cloudLogged: false, completedAt: "" };
      timerState.timers[session.id] = timer;
    }
    timer.elapsedMs = Math.max(0, Number(timer.elapsedMs || 0));
    timer.running = Boolean(timer.running);
    timer.startedAt = Number(timer.startedAt || 0);
    timer.completed = Boolean(timer.completed);
    timer.cloudLogged = Boolean(timer.cloudLogged);
    timer.completedAt = String(timer.completedAt || "");
    return timer;
  }

  function elapsedFor(timer) {
    return timer.elapsedMs + (timer.running && timer.startedAt ? Math.max(0, Date.now() - timer.startedAt) : 0);
  }

  function formatTimer(milliseconds) {
    const totalSeconds = Math.max(0, Math.floor(milliseconds / 1000));
    const minutes = Math.floor(totalSeconds / 60);
    const seconds = totalSeconds % 60;
    return `${String(minutes).padStart(2, "0")}:${String(seconds).padStart(2, "0")}`;
  }

  function stopOtherRecordingTimers(activeID) {
    practiceSections.forEach((session) => {
      if (session.id === activeID) return;
      const timer = timerFor(session);
      if (!timer.running) return;
      timer.elapsedMs = elapsedFor(timer);
      timer.running = false;
      timer.startedAt = 0;
      syncGuidedPracticeEntry(session);
      saveTimerBlock(session, timer);
    });
  }

  function sessionForRecording(blockID) {
    if (blockID) return practiceSections.find((session) => guidedBlockFor(session)?.id === blockID) || null;
    return practiceSections.find((session) => timerFor(session).running && !timerFor(session).completed)
      || practiceSections.find((session) => !timerFor(session).completed)
      || null;
  }

  function beginRecordingPractice(blockID) {
    const session = sessionForRecording(blockID);
    if (!session) return;
    const timer = timerFor(session);
    recordingTimerSessionID = session.id;
    stopOtherRecordingTimers(session.id);
    if (!timer.running) {
      timer.running = true;
      timer.startedAt = Date.now();
    }
    persistTimerState();
    renderSessions();
    updateWeekLive();
    saveTimerBlock(session, timer);
  }

  function endRecordingPractice(blockID) {
    const session = practiceSections.find((candidate) => candidate.id === recordingTimerSessionID);
    if (!session || (blockID && guidedBlockFor(session)?.id !== blockID)) return;
    const timer = timerFor(session);
    if (timer.running) {
      timer.elapsedMs = Math.min(MAX_SECTION_PRACTICE_MS, elapsedFor(timer));
      timer.running = false;
      timer.startedAt = 0;
    }
    if (!timer.completed && timer.elapsedMs >= session.minutes * 60 * 1000) {
      timer.completed = true;
      timer.completedAt = new Date().toISOString();
    }
    recordingTimerSessionID = "";
    persistTimerState();
    syncGuidedPracticeEntry(session);
    saveTimerBlock(session, timer);
    renderAll();
    logGuidedBlockToCloud(session);
  }

  function checkpointRecordingPractice() {
    const session = practiceSections.find((candidate) => candidate.id === recordingTimerSessionID);
    if (!session) return;
    const timer = timerFor(session);
    if (!timer.running) return;
    timer.elapsedMs = Math.min(MAX_SECTION_PRACTICE_MS, elapsedFor(timer));
    timer.startedAt = Date.now();
    persistTimerState();
    saveTimerBlock(session, timer);
  }

  function markGuidedGoalMet(session) {
    const timer = timerFor(session);
    if (timer.completed) return;
    timer.completed = true;
    timer.completedAt = new Date().toISOString();
    persistTimerState();
    saveTimerBlock(session, timer);
    syncGuidedPracticeEntry(session);
    renderAll();
    showToast(`${session.title} goal met - recording continues`);
  }

  function syncGuidedPracticeEntry(session) {
    const timer = timerFor(session);
    if (timer.elapsedMs <= 0) return false;
    const logId = guidedLogId(session);
    const minutes = Math.min(360, Math.round((timer.elapsedMs / 60000) * 10000) / 10000);
    const existing = state.practice.find((entry) => entry.id === logId);
    if (existing && Math.abs(Number(existing.minutes) - minutes) < 0.0001) return false;
    if (existing) {
      existing.minutes = minutes;
      existing.note = session.title;
    } else {
      state.practice.push({ id: logId, date: timerState.date, minutes, track: session.track, note: session.title, preset: true });
    }
    saveState("practice.recording_time_updated");
    return true;
  }

  async function logGuidedBlockToCloud(session) {
    const timer = timerFor(session);
    if (timer.elapsedMs <= 0 || cloudLoggingTimers.has(session.id)) return;
    if (typeof globalThis.JazzPracticeSession?.logGuidedActivity !== "function") return;
    cloudLoggingTimers.add(session.id);
    try {
      await globalThis.JazzPracticeSession.logGuidedActivity({
        sourceId: guidedLogId(session),
        category: session.category,
        title: session.title,
        durationMinutes: Math.max(1, Math.min(360, Math.round(timer.elapsedMs / 60000))),
        notes: guidedBlockFor(session)?.notes || session.detail,
        occurredAt: timer.completedAt || new Date().toISOString(),
      });
      timer.cloudLogged = true;
      persistTimerState();
    } catch {
      // Keep the completed block pending; online/load retries make this resilient.
    } finally {
      cloudLoggingTimers.delete(session.id);
    }
  }

  function syncCompletedGuidedBlocks() {
    let restoredPracticeLog = false;
    practiceSections.forEach((session) => {
      const timer = timerFor(session);
      if (timer.elapsedMs > 0) restoredPracticeLog = syncGuidedPracticeEntry(session) || restoredPracticeLog;
      if (timer.elapsedMs > 0) logGuidedBlockToCloud(session);
    });
    if (restoredPracticeLog) {
      renderStats();
      renderWeek();
    }
  }

  function tickGuidedTimers() {
    practiceSections.forEach((session) => {
      const timer = timerFor(session);
      if (!timer.running || timer.completed) return;
      if (elapsedFor(timer) >= session.minutes * 60 * 1000) markGuidedGoalMet(session);
    });
    updateTimerElements();
  }

  function updateTimerElements() {
    let totalElapsedMs = 0;
    practiceSections.forEach((session) => {
      const timer = timerFor(session);
      const targetMs = session.minutes * 60 * 1000;
      const elapsedMs = elapsedFor(timer);
      totalElapsedMs += elapsedMs;
      document.querySelectorAll(`[data-session-id="${session.id}"]`).forEach((card) => {
        card.classList.toggle("running", timer.running);
        const readout = $("[data-timer-readout]", card);
        const progress = $("[data-timer-progress]", card);
        if (readout) readout.textContent = `${formatTimer(elapsedMs)} recorded / ${formatTimer(targetMs)} goal`;
        if (progress) {
          progress.style.width = `${Math.min(100, (elapsedMs / targetMs) * 100)}%`;
          progress.parentElement?.setAttribute("aria-valuenow", String(Math.min(100, Math.round((elapsedMs / targetMs) * 100))));
        }
      });
    });
    const practicedMinutes = (totalElapsedMs / 60000).toFixed(1);
    setText("today-total-minutes", practicedMinutes);
    setText("today-stage-minutes", practicedMinutes);
  }

  function renderSessions() {
    const list = $("#session-list");
    const activePanel = $("#active-section-panel");
    if (!list || !activePanel) return;
    list.replaceChildren();
    const firstIncomplete = practiceSections.findIndex((session) => !timerFor(session).completed);
    const selectedExists = practiceSections.some((session) => session.id === selectedPracticeSectionID);
    selectedPracticeSectionID = (selectedExists ? selectedPracticeSectionID : "")
      || practiceSections[Math.max(0, firstIncomplete)]?.id
      || practiceSections[0]?.id
      || "";

    const goalMinutes = practiceSections.reduce((sum, session) => sum + Number(session.minutes || 0), 0);
    const totalElapsedMs = practiceSections.reduce((sum, session) => sum + elapsedFor(timerFor(session)), 0);
    const totalTakes = practiceSections.reduce((sum, session) => sum + activeBlockTakeCount(guidedBlockFor(session)), 0);
    const practicedMinutes = (totalElapsedMs / 60000).toFixed(1);
    setText("today-date", new Date().toLocaleDateString(undefined, { weekday: "long", month: "long", day: "numeric" }));
    setText("today-total-minutes", practicedMinutes);
    setText("today-stage-minutes", practicedMinutes);
    setText("today-goal-minutes", goalMinutes);
    setText("today-stage-goal", goalMinutes);
    setText("today-total-takes", totalTakes);
    setText("today-stage-takes", totalTakes);

    practiceSections.forEach((session, index) => {
      const timer = timerFor(session);
      const complete = timer.completed;
      const block = guidedBlockFor(session);
      const takeCount = activeBlockTakeCount(block);
      const uploadJobs = uploadJobsForBlock(block);
      const selected = session.id === selectedPracticeSectionID;
      const recordingHere = activeSectionRecordingID === block?.id;
      const recordingStatus = recordingHere
        ? (activeSectionRecordingPhase === "processing" ? "Processing" : "Recording")
        : (uploadJobs.some((job) => job.phase === "failed") ? "Upload failed" : (uploadJobs.length ? "Uploading" : ""));
      const targetMs = session.minutes * 60 * 1000;
      const elapsedMs = elapsedFor(timer);
      const card = document.createElement("article");
      card.dataset.sessionId = session.id;
      card.className = `plan-card${complete ? " complete" : ""}${timer.running ? " running" : ""}${recordingHere ? " recording-owner" : ""}${index === firstIncomplete ? " current" : ""}${selected ? " selected" : ""}`;
      card.innerHTML = `
        <button class="practice-plan-select" type="button" aria-pressed="${selected}" aria-label="Open ${escapeHTML(session.title)}">
          <span class="plan-step">${complete ? "✓" : String(index + 1).padStart(2, "0")}</span>
          <span class="plan-copy"><strong>${escapeHTML(session.title)}</strong><small>${escapeHTML(session.time)} · ${takeCount} take${takeCount === 1 ? "" : "s"}</small></span>
          <span class="plan-status">${recordingStatus || (complete ? "Complete" : (elapsedMs > 0 ? "In progress" : `${session.minutes} min`))}</span>
          <span class="session-timer-track" role="progressbar" aria-label="${escapeHTML(session.title)} recording progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.min(100, Math.round((elapsedMs / targetMs) * 100))}"><span data-timer-progress style="width:${Math.min(100, (elapsedMs / targetMs) * 100)}%"></span></span>
        </button>`;
      $(".practice-plan-select", card).addEventListener("click", () => {
        selectedPracticeSectionID = session.id;
        renderSessions();
      });
      list.appendChild(card);
    });

    const selectedSession = practiceSections.find((session) => session.id === selectedPracticeSectionID) || practiceSections[0];
    renderActiveSection(selectedSession, guidedBlockFor(selectedSession));
    const addButton = $("#add-practice-section");
    if (addButton) {
      addButton.disabled = !guidedBlocksReady || practiceSections.length >= 20;
      addButton.title = practiceSections.length >= 20 ? "Today’s plan already has 20 sections" : "Add a cloud-synced section";
    }
  }

  function renderActiveSection(session, block) {
    const panel = $("#active-section-panel");
    if (!panel || !session) return;
    const timer = timerFor(session);
    const complete = timer.completed;
    const takeCount = activeBlockTakeCount(block);
    const uploadJobs = uploadJobsForBlock(block);
    const recordingHere = activeSectionRecordingID === block?.id;
    const recordingActionLocked = activeSectionRecordingID && activeSectionRecordingPhase && !recordingHere
      && (activeSectionRecordingPhase === "starting" || activeSectionRecordingPhase === "recording" || activeSectionRecordingPhase === "processing");
    const recordingOwner = recordingActionLocked ? sessionForRecording(activeSectionRecordingID) : null;
    const processingHere = recordingHere && activeSectionRecordingPhase !== "recording";
    const failedHere = uploadJobs.some((job) => job.phase === "failed");
    const targetMs = session.minutes * 60 * 1000;
    const elapsedMs = elapsedFor(timer);
    const stateLabel = recordingHere
      ? (activeSectionRecordingPhase === "recording" ? "Recording now" : "Processing take")
      : (failedHere ? "Upload needs attention" : (uploadJobs.length ? "Uploading in background" : (complete ? "Goal met" : (elapsedMs > 0 ? "In progress" : "Ready"))));
    setText("current-section-state", stateLabel);

    panel.replaceChildren();
    if (recordingOwner) {
      const banner = document.createElement("aside");
      banner.className = "background-recording-banner";
      banner.innerHTML = `
        <span><em>${activeSectionRecordingPhase === "processing" ? "Processing" : "Recording continues"}</em><strong>${escapeHTML(recordingOwner.title)}</strong><small>${escapeHTML(activeSectionRecordingMessage || "You can browse the plan without interrupting this take.")}</small></span>
        <div><button type="button" data-return-to-recording>Return to recorder</button>${activeSectionRecordingPhase === "recording" ? '<button type="button" class="cancel-background-recording" data-cancel-background-recording>Cancel take</button><button type="button" class="stop-background-recording" data-stop-background-recording>Stop take</button>' : ""}</div>`;
      $("[data-return-to-recording]", banner).addEventListener("click", () => {
        selectedPracticeSectionID = recordingOwner.id;
        renderSessions();
      });
      $("[data-cancel-background-recording]", banner)?.addEventListener("click", () => {
        if (confirm("Cancel and permanently discard this take? It will not be uploaded, but the practice time will remain counted.")) {
          globalThis.JazzRecording?.cancel();
        }
      });
      $("[data-stop-background-recording]", banner)?.addEventListener("click", () => globalThis.JazzRecording?.stop());
      panel.appendChild(banner);
    }
    const card = document.createElement("article");
    card.dataset.sessionId = session.id;
    card.className = `selected-section-card${complete ? " complete" : ""}${timer.running ? " running" : ""}`;
    card.innerHTML = `
      <header class="selected-section-head">
        <div>
          <span class="selected-section-kicker">${escapeHTML(session.category || session.track || "Practice")}</span>
          <h3>${escapeHTML(session.title)}</h3>
          <p>${escapeHTML(session.detail)}</p>
          ${session.win ? `<div class="selected-section-win"><span>Today’s win</span><strong>${escapeHTML(session.win)}</strong></div>` : ""}
        </div>
        <span class="selected-section-target">${session.minutes}<small>min goal</small></span>
      </header>
      <div class="selected-progress">
        <div class="session-timer-track" role="progressbar" aria-label="${escapeHTML(session.title)} recording progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.min(100, Math.round((elapsedMs / targetMs) * 100))}"><span data-timer-progress style="width:${Math.min(100, (elapsedMs / targetMs) * 100)}%"></span></div>
        <span class="timer-readout" data-timer-readout aria-live="off">${formatTimer(elapsedMs)} recorded / ${formatTimer(targetMs)} goal</span>
      </div>
      <div class="section-recording-panel">
        <div class="section-recording-head">
          <span><strong>Section takes</strong><em>${takeCount} / ${MAX_TAKES_PER_SECTION}</em></span>
          <div class="section-recording-actions"><button class="audio-options-button" data-audio-options type="button" aria-label="Recording options" title="Recording options">⚙</button>${recordingHere && activeSectionRecordingPhase === "recording" ? '<button class="section-cancel-button" data-section-cancel type="button">Cancel take</button>' : ""}<button class="section-record-button${recordingHere && activeSectionRecordingPhase === "recording" ? " recording" : ""}" data-section-record type="button" ${!block || processingHere || recordingActionLocked || (takeCount >= MAX_TAKES_PER_SECTION && !recordingHere) ? "disabled" : ""}>${recordingHere ? (processingHere ? "Processing…" : "Stop recording") : (recordingActionLocked ? "Recorder busy" : "+ Record take")}</button></div>
        </div>
        ${recordingHere && activeSectionRecordingMessage ? `<p class="section-recording-state">${escapeHTML(activeSectionRecordingMessage)}</p>` : ""}
        ${sectionUploadMarkup(block)}
        <div class="section-take-list">${sectionRecordingMarkup(block)}</div>
      </div>
      <label class="section-notes-field">
        <span><strong>Section notes</strong><em data-section-sync data-tone="saved">${block ? "Cloud synced" : "Waiting for cloud"}</em></span>
        <textarea data-section-notes maxlength="4000" rows="5" ${block ? "" : "disabled"} placeholder="What did you work on during ${session.title.toLowerCase()}?">${escapeHTML(block?.notes || "")}</textarea>
      </label>`;
    panel.appendChild(card);
    wireSectionTools(card, session, block);
  }

  function renderWeek() {
    const monday = startOfWeek();
    const chart = $("#week-chart");
    chart.replaceChildren();
    for (let index = 0; index < 7; index += 1) {
      const date = addDays(monday, index);
      const key = localDateKey(date);
      const column = document.createElement("div");
      column.dataset.practiceDate = key;
      column.className = `day-column${key === localDateKey() ? " today" : ""}`;
      column.innerHTML = `<div class="day-bar-track"><span class="day-bar"></span></div><span class="day-label">${date.toLocaleDateString(undefined, { weekday: "short" }).slice(0, 2)}</span>`;
      chart.appendChild(column);
    }
    updateWeekLive();
  }

  function updateWeekLive() {
    const monday = startOfWeek();
    const dailyMinutes = Array.from({ length: 7 }, (_, index) => {
      const date = addDays(monday, index);
      const key = localDateKey(date);
      return { key, value: practiceMinutesForDate(key) };
    });
    const minutes = dailyMinutes.reduce((sum, day) => sum + day.value, 0);
    const rawPercent = (minutes / DATA.weeklyTargetMinutes) * 100;
    const percent = Math.min(100, rawPercent);
    setText("week-hours", (minutes / 60).toFixed(2));
    setText("week-percent", `${percent.toFixed(1)}%`);
    $("#week-meter").style.width = `${percent}%`;
    setText("week-note", minutes >= DATA.weeklyTargetMinutes
      ? "Weekly target cleared. Protect the streak; do not manufacture fatigue."
      : `${Math.ceil(Math.max(0, DATA.weeklyTargetMinutes - minutes))} focused minutes remain in this week's campaign.`);

    const max = Math.max(100, ...dailyMinutes.map((day) => day.value));
    dailyMinutes.forEach((day) => {
      const column = document.querySelector(`[data-practice-date="${day.key}"]`);
      if (!column) return;
      column.title = `${day.value.toFixed(1)} minutes`;
      const bar = $(".day-bar", column);
      if (bar) bar.style.height = `${Math.max(2, (day.value / max) * 100)}%`;
    });
  }

  function renderMission() {
    const list = $("#objective-list");
    list.replaceChildren();
    DATA.mission.objectives.forEach((objective, index) => {
      const complete = Boolean(state.objectives[index]);
      const button = document.createElement("button");
      button.type = "button";
      button.className = `objective${complete ? " complete" : ""}`;
      button.setAttribute("aria-pressed", complete);
      button.textContent = objective;
      button.addEventListener("click", () => {
        state.objectives[index] = !complete;
        saveState("mission.objective_toggled");
        renderAll();
      });
      list.appendChild(button);
    });
    const completed = Object.values(state.objectives).filter(Boolean).length;
    setText("mission-count", `${completed} / ${DATA.mission.objectives.length}`);
    setText("mission-unlock", completed === DATA.mission.objectives.length
      ? "Mission clear: Blue Bossa is ready for the real room."
      : "Complete the mission to unlock Boss 3: play Blue Bossa from memory.");
  }

  function skillLevel(id) {
    return Math.max(0, Math.min(MAX_SKILL_LEVEL, Number(state.skillLevels[id] || 0)));
  }

  function skillUnlocked(skill) {
    return skill.prereqs.every((id) => skillLevel(id) >= 1);
  }

  function renderTrackTabs() {
    const tabs = $("#track-tabs");
    tabs.replaceChildren();
    DATA.tracks.forEach((track) => {
      const button = document.createElement("button");
      button.type = "button";
      button.role = "tab";
      button.className = "track-tab";
      button.setAttribute("aria-selected", track.id === activeTrack);
      button.textContent = track.name;
      button.addEventListener("click", () => {
        activeTrack = track.id;
        renderTrackTabs();
        renderSkillTree();
      });
      tabs.appendChild(button);
    });
  }

  function renderSkillTree() {
    const tree = $("#skill-tree-grid");
    tree.replaceChildren();
    const visible = DATA.skills.filter((skill) => activeTrack === "all" || skill.track === activeTrack);
    const tiers = [...new Set(visible.map((skill) => skill.tier))].sort((a, b) => a - b);
    tiers.forEach((tierNumber) => {
      const tier = document.createElement("section");
      tier.className = "skill-tier";
      const label = document.createElement("div");
      label.className = "tier-label";
      label.textContent = `Tier ${tierNumber} · ${["Foundation", "Connection", "Fluency", "Bandstand", "First call"][tierNumber - 1]}`;
      const grid = document.createElement("div");
      grid.className = "skill-tier-grid";
      visible.filter((skill) => skill.tier === tierNumber).forEach((skill) => {
        const level = skillLevel(skill.id);
        const unlocked = skillUnlocked(skill);
        const node = document.createElement("button");
        node.type = "button";
        node.className = `skill-node${!unlocked ? " locked" : ""}${level === MAX_SKILL_LEVEL ? " mastered" : ""}${unlocked && level === 0 ? " active" : ""}`;
        node.disabled = !unlocked;
        node.innerHTML = `
          <div class="node-top"><span class="node-track">${trackName(skill.track)}</span><span class="node-level">${unlocked ? `LV ${level}/${MAX_SKILL_LEVEL}` : "LOCKED"}</span></div>
          <h3>${skill.name}</h3>
          <p>${skill.summary}</p>
          ${unlocked ? `<div class="node-pips">${[1,2,3,4].map((n) => `<span class="node-pip${n <= level ? " on" : ""}"></span>`).join("")}</div>` : `<span class="node-lock">Requires ${skill.prereqs.map(skillName).join(" + ")}</span>`}`;
        if (unlocked) node.addEventListener("click", () => openSkill(skill.id));
        grid.appendChild(node);
      });
      tier.append(label, grid);
      tree.appendChild(tier);
    });
  }

  function trackName(id) {
    return DATA.tracks.find((track) => track.id === id)?.name || id;
  }

  function skillName(id) {
    return DATA.skills.find((skill) => skill.id === id)?.name || id;
  }

  function openSkill(id) {
    activeSkillId = id;
    renderSkillDialog();
    $("#skill-dialog").showModal();
  }

  function renderSkillDialog() {
    const skill = DATA.skills.find((item) => item.id === activeSkillId);
    if (!skill) return;
    const level = skillLevel(skill.id);
    setText("skill-dialog-track", `${trackName(skill.track)} · Tier ${skill.tier}`);
    setText("skill-dialog-title", skill.name);
    setText("skill-dialog-copy", skill.summary);
    setText("skill-dialog-unlock", skill.unlock);
    const road = $("#skill-level-road");
    road.replaceChildren();
    skill.levels.forEach((description, index) => {
      const stepLevel = index + 1;
      const step = document.createElement("div");
      step.className = `level-step${stepLevel <= level ? " complete" : ""}${stepLevel === level + 1 ? " current" : ""}`;
      step.innerHTML = `<span class="level-index">LV ${stepLevel}</span><span>${description}</span>`;
      road.appendChild(step);
    });
    $("#level-down").disabled = level <= 0;
    $("#level-up").disabled = level >= MAX_SKILL_LEVEL;
    $("#level-up").textContent = level >= MAX_SKILL_LEVEL ? "Skill mastered" : "Complete next level";
  }

  function renderRepertoire() {
    const grid = $("#repertoire-grid");
    grid.replaceChildren();
    DATA.repertoire.forEach((tune, index) => {
      const stage = Math.max(0, Math.min(6, Number(state.repertoire[tune.id] || 0)));
      const card = document.createElement("article");
      card.className = `tune-card${tune.current ? " current" : ""}`;
      card.innerHTML = `
        <span class="tune-index">${String(index + 1).padStart(2, "0")}${tune.current ? " · CURRENT" : ""}</span>
        <h3>${tune.title}</h3>
        <p class="tune-lesson">${tune.lesson}</p>
        <div class="stars" aria-label="${stage} of 6 stages">${[1,2,3,4,5,6].map((n) => `<span class="${n <= stage ? "on" : ""}">★</span>`).join("")}</div>
        <div class="tune-stage">${DATA.repertoireStages[stage]}</div>
        <div class="tune-actions">
          <button type="button" data-direction="down" aria-label="Move ${tune.title} back one stage" ${stage === 0 ? "disabled" : ""}>−</button>
          <button type="button" data-direction="up" aria-label="Advance ${tune.title} one stage" ${stage === 6 ? "disabled" : ""}>${stage === 6 ? "Gig ready" : "+ Advance"}</button>
        </div>`;
      $$("button", card).forEach((button) => button.addEventListener("click", () => {
        const direction = button.dataset.direction === "up" ? 1 : -1;
        state.repertoire[tune.id] = Math.max(0, Math.min(6, stage + direction));
        saveState("repertoire.stage_changed");
        renderAll();
      }));
      grid.appendChild(card);
    });
  }

  function renderRoadmap() {
    const grid = $("#roadmap-grid");
    grid.replaceChildren();
    DATA.roadmap.forEach((phase, index) => {
      const section = document.createElement("article");
      section.className = `roadmap-phase${index === 0 ? " current" : ""}`;
      section.innerHTML = `<span class="phase-number">${phase.number}</span><h3>${phase.title}</h3><p>${phase.copy}</p><ul>${phase.items.map((item) => `<li>${item}</li>`).join("")}</ul>`;
      grid.appendChild(section);
    });
  }

  function renderScene() {
    const route = $("#scene-route");
    route.replaceChildren();
    DATA.sceneSteps.forEach((step, index) => {
      const complete = Boolean(state.scene[step.id]);
      const unlocked = index === 0 || Boolean(state.scene[DATA.sceneSteps[index - 1].id]);
      const stop = document.createElement("article");
      stop.className = `scene-stop${complete ? " complete" : ""}${unlocked && !complete ? " current" : ""}`;
      stop.innerHTML = `<button type="button" class="stop-dot" ${!unlocked ? "disabled" : ""} aria-label="${complete ? "Reopen" : "Complete"} ${step.title}" aria-pressed="${complete}">${complete ? "✓" : index + 1}</button><h3>${step.title}</h3><p>${step.detail}</p>`;
      $("button", stop).addEventListener("click", () => {
        if (!unlocked) return;
        const newValue = !complete;
        state.scene[step.id] = newValue;
        if (!newValue) DATA.sceneSteps.slice(index + 1).forEach((future) => { state.scene[future.id] = false; });
        saveState("scene.step_toggled");
        renderAll();
      });
      route.appendChild(stop);
    });
    setText("scene-count", Object.values(state.scene).filter(Boolean).length);
  }

  function renderBosses() {
    const list = $("#boss-list");
    list.replaceChildren();
    DATA.bosses.forEach((boss, index) => {
      const complete = Boolean(state.bosses[index]);
      const unlocked = index === 0 || Boolean(state.bosses[index - 1]);
      const card = document.createElement("article");
      card.className = `boss-card${complete ? " complete" : ""}${unlocked && !complete ? " current" : ""}${!unlocked ? " locked" : ""}`;
      card.innerHTML = `<span class="boss-number">B${String(index + 1).padStart(2, "0")}</span><div class="boss-copy"><h3>${boss.title}</h3><p>${boss.detail}</p></div><button type="button" class="boss-toggle" ${!unlocked ? "disabled" : ""} aria-label="${complete ? "Reopen" : "Clear"} ${boss.title}" aria-pressed="${complete}">✓</button>`;
      $("button", card).addEventListener("click", () => {
        if (!unlocked) return;
        const newValue = !complete;
        state.bosses[index] = newValue;
        if (!newValue) DATA.bosses.slice(index + 1).forEach((_, futureIndex) => { state.bosses[index + futureIndex + 1] = false; });
        saveState("boss.status_changed");
        renderAll();
        showToast(newValue ? `Boss ${index + 1} cleared · +250 XP` : `Boss ${index + 1} reopened`);
      });
      list.appendChild(card);
    });
  }

  function renderAll() {
    renderStats();
    renderSessions();
    renderWeek();
    renderMission();
    renderTrackTabs();
    renderSkillTree();
    renderRepertoire();
    renderScene();
    renderBosses();
    if ($("#skill-dialog").open) renderSkillDialog();
  }

  function setupDialogs() {
    $$('[data-close-dialog]').forEach((button) => button.addEventListener("click", () => button.closest("dialog").close()));
    $$('dialog').forEach((dialog) => dialog.addEventListener("click", (event) => {
      const rect = dialog.getBoundingClientRect();
      const outside = event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom;
      if (outside) dialog.close();
    }));
    $("#level-up").addEventListener("click", () => changeSkillLevel(1));
    $("#level-down").addEventListener("click", () => changeSkillLevel(-1));
  }

  function setupPracticeSectionCreator() {
    const dialog = $("#section-dialog");
    const form = $("#section-form");
    const openButton = $("#add-practice-section");
    const status = $("#section-form-status");
    if (!dialog || !form || !openButton || !status) return;

    const closeDialog = () => {
      status.textContent = "";
      dialog.close();
    };
    openButton.addEventListener("click", () => {
      if (!guidedBlocksReady) {
        showToast("The practice plan is still syncing");
        return;
      }
      dialog.showModal();
      $("#new-section-title")?.focus();
    });
    $("#close-section-dialog")?.addEventListener("click", closeDialog);
    $("#cancel-section-dialog")?.addEventListener("click", closeDialog);

    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      const submit = $("button[type='submit']", form);
      const title = $("#new-section-title").value.trim();
      const instructions = $("#new-section-instructions").value.trim();
      const type = $("#new-section-type").value;
      const minutes = Number($("#new-section-minutes").value);
      const presets = {
        fundamentals: { category: "fundamentals", track: "trumpet" },
        technique: { category: "technique", track: "trumpet" },
        scales: { category: "scales", track: "language" },
        listening: { category: "listening", track: "musician" },
        tune: { category: "repertoire", track: "musician" },
        improvisation: { category: "improvisation", track: "language" },
        other: { category: "general", track: "musician" },
      };
      if (!title || !Number.isInteger(minutes) || minutes < 1 || minutes > 360 || practiceSections.length >= 20) {
        status.textContent = practiceSections.length >= 20 ? "Today’s plan already has 20 sections." : "Add a title and a target from 1 to 360 minutes.";
        return;
      }
      if (typeof globalThis.JazzPracticeSession?.ensureGuidedBlocks !== "function") {
        status.textContent = "The private practice service is still connecting.";
        return;
      }
      const blockKey = `custom-${crypto.randomUUID()}`;
      const position = Math.min(99, Math.max(-1, ...practiceSections.map((section) => Number(section.position ?? -1))) + 1);
      submit.disabled = true;
      status.textContent = "Adding section…";
      try {
        const preset = presets[type] || presets.other;
        const result = await globalThis.JazzPracticeSession.ensureGuidedBlocks(localDateKey(), [{
          blockKey,
          position,
          title,
          instructions,
          category: preset.category,
          track: preset.track,
          targetMinutes: minutes,
        }]);
        applyPracticeBlocks(result.blocks || []);
        selectedPracticeSectionID = blockKey;
        form.reset();
        closeDialog();
        renderSessions();
        showToast(`${title} added to today’s plan`);
      } catch (error) {
        status.textContent = `Could not add section: ${error.message}`;
      } finally {
        submit.disabled = false;
      }
    });
  }

  function changeSkillLevel(direction) {
    const skill = DATA.skills.find((item) => item.id === activeSkillId);
    if (!skill || !skillUnlocked(skill)) return;
    const current = skillLevel(skill.id);
    const next = Math.max(0, Math.min(MAX_SKILL_LEVEL, current + direction));
    state.skillLevels[skill.id] = next;
    saveState("skill.level_changed");
    renderAll();
    showToast(direction > 0 ? `${skill.name} · level ${next}` : `${skill.name} adjusted`);
  }

  function setupDataActions() {
    $("#edit-people").addEventListener("click", () => {
      const answer = prompt("How many musicians could realistically call you for a rehearsal or gig today?", String(state.peopleCanCall || 0));
      if (answer === null) return;
      const value = Number(answer);
      if (!Number.isInteger(value) || value < 0 || value > 999) {
        showToast("Enter a whole number from 0 to 999");
        return;
      }
      state.peopleCanCall = value;
      saveState("network.people_count_changed");
      renderStats();
    });

    $("#export-data").addEventListener("click", () => {
      const blob = new Blob([JSON.stringify(state, null, 2)], { type: "application/json" });
      const link = document.createElement("a");
      link.href = URL.createObjectURL(blob);
      link.download = `jazz-project-${localDateKey()}.json`;
      link.click();
      setTimeout(() => URL.revokeObjectURL(link.href), 1000);
      showToast("Progress exported");
    });

    $("#import-data").addEventListener("change", async (event) => {
      const file = event.target.files?.[0];
      if (!file) return;
      try {
        const imported = JSON.parse(await file.text());
        if (!imported || typeof imported !== "object" || !Array.isArray(imported.practice)) throw new Error("Invalid file");
        state = normalizeState(imported);
        saveState("campaign.file_imported");
        renderAll();
        showToast("Progress imported");
      } catch {
        showToast("That progress file could not be read");
      } finally {
        event.target.value = "";
      }
    });

    $("#reset-data").addEventListener("click", () => {
      if (!confirm("Reset every skill, practice log, tune, and boss fight? Export first if you may want it back.")) return;
      state = structuredClone(stateDefaults);
      saveState("campaign.reset");
      renderAll();
      showToast("Campaign reset");
    });
  }

  renderRoadmap();
  setupDialogs();
  setupPracticeSectionCreator();
  setupDataActions();
  addEventListener("jazz:activity-logged", (event) => {
    const activity = event.detail;
    if (!activity?.id || state.practice.some((entry) => entry.id === `activity-${activity.id}`)) return;
    const occurredAt = new Date(activity.occurredAt || Date.now());
    const languageCategories = new Set(["scales", "ear-training", "improvisation"]);
    state.practice.push({
      id: `activity-${activity.id}`,
      date: localDateKey(occurredAt),
      minutes: Number(activity.durationMinutes),
      track: languageCategories.has(activity.category) ? "language" : "trumpet",
      note: String(activity.title || "Practice activity").slice(0, 100),
    });
    saveState("practice.activity_logged");
    renderAll();
    showToast(`+${activity.durationMinutes} minutes logged`);
  });
  addEventListener("jazz:recording-state", (event) => {
    const detail = event.detail || {};
    if (detail.phase === "recording" && recordingTimerSessionID !== sessionForRecording(detail.blockId)?.id) {
      beginRecordingPractice(detail.blockId);
    } else if (detail.phase !== "recording" && recordingTimerSessionID) {
      endRecordingPractice(detail.blockId);
    }
    if (!detail.blockId) return;
    activeSectionRecordingID = detail.phase === "idle" || detail.phase === "complete" || detail.phase === "cancelled" || detail.phase === "error" ? "" : detail.blockId;
    activeSectionRecordingMessage = detail.message || "";
    activeSectionRecordingPhase = detail.phase || "";
    renderSessions();
  });
  addEventListener("jazz:upload-state", (event) => {
    const detail = event.detail || {};
    if (!detail.id || !detail.blockId) return;
    sectionUploadJobs.set(detail.id, detail);
    const existing = document.querySelector(`[data-upload-job="${detail.id}"]`);
    if (existing && detail.phase === "uploading") {
      const message = $("span", existing);
      if (message) message.textContent = `Take ${detail.takeNumber} · ${detail.message}`;
      return;
    }
    renderSessions();
  });
  addEventListener("jazz:recordings-changed", async () => {
    await hydrateGuidedBlocks();
    sectionUploadJobs.forEach((job, id) => {
      if (job.phase === "complete") sectionUploadJobs.delete(id);
    });
    renderSessions();
  });
  renderAll();
  addEventListener("online", flushOutbox);
  addEventListener("online", syncCompletedGuidedBlocks);
  const animateGuidedTimers = () => {
    tickGuidedTimers();
    if (practiceSections.some((session) => timerFor(session).running)) updateWeekLive();
    requestAnimationFrame(animateGuidedTimers);
  };
  requestAnimationFrame(animateGuidedTimers);
  setInterval(updateWeekLive, 30000);
  setInterval(checkpointRecordingPractice, 30000);
  setTimeout(() => {
    tickGuidedTimers();
    syncCompletedGuidedBlocks();
  }, 1000);
  hydrateGuidedBlocks();
  initializeCloudSync().finally(syncCompletedGuidedBlocks);
})();
