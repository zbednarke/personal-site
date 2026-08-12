(() => {
  "use strict";

  const API_BASE = "./api/v1";
  const $ = (selector, root = document) => root.querySelector(selector);
  let sessions = [];
  let activeSession = null;
  let noteDirty = false;
  let noteSaveTimer = null;
  let initialSessionLoad = null;

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

  function defaultTitle() {
    return `Practice - ${new Date().toLocaleDateString(undefined, { month: "short", day: "numeric" })}`;
  }

  function localDateKey(date) {
    const year = date.getFullYear();
    const month = String(date.getMonth() + 1).padStart(2, "0");
    const day = String(date.getDate()).padStart(2, "0");
    return `${year}-${month}-${day}`;
  }

  function setNoteStatus(message, tone = "") {
    const status = $("#practice-session-note-status");
    if (!status) return;
    status.textContent = message;
    status.dataset.tone = tone;
  }

  function sessionSummary(session) {
    const minutes = Number.isFinite(Number(session.totalDurationMs))
      ? Math.round((Number(session.totalDurationMs) / 60000) * 10) / 10
      : Number(session.totalMinutes || 0);
    const recordings = Number(session.recordingCount || 0);
    return `${minutes} minute${minutes === 1 ? "" : "s"} practiced · ${recordings} recording${recordings === 1 ? "" : "s"}`;
  }

  async function loadSessions() {
    try {
      const result = await api("/practice-sessions");
      sessions = result.sessions || [];
      const activeSummary = sessions.find((session) => session.status === "active");
      activeSession = activeSummary ? await api(`/practice-sessions/${activeSummary.id}`) : null;
      renderActiveSession();
      renderHistory();
      return activeSession;
    } catch (error) {
      const summary = $("#practice-session-summary");
      if (summary) summary.textContent = `Practice sessions are temporarily unavailable: ${error.message}`;
      setNoteStatus("Could not sync", "error");
      return null;
    }
  }

  async function ensureActive() {
    if (initialSessionLoad) await initialSessionLoad;
    if (activeSession) return activeSession;
    const note = $("#practice-session-notes")?.value.trim() || "";
    const created = await api("/practice-sessions", {
      method: "POST",
      body: JSON.stringify({
        title: defaultTitle(),
        summary: note,
        startedAt: new Date().toISOString(),
      }),
    });
    activeSession = await api(`/practice-sessions/${created.id}`);
    noteDirty = false;
    await refreshSessionListOnly();
    renderActiveSession();
    renderHistory();
    return activeSession;
  }

  async function refreshSessionListOnly() {
    const result = await api("/practice-sessions");
    sessions = result.sessions || [];
  }

  function renderActiveSession() {
    const heading = $("#practice-session-heading");
    const summary = $("#practice-session-summary");
    const notes = $("#practice-session-notes");
    const finish = $("#finish-practice-session");
    if (!heading || !summary || !notes) return;

    if (!activeSession) {
      heading.textContent = "Today’s session";
      summary.textContent = "Your note and recordings sync privately across devices.";
      finish.hidden = true;
      if (!noteDirty) notes.value = "";
      setNoteStatus(noteDirty ? "Waiting to sync" : "Ready to sync", noteDirty ? "saving" : "");
      return;
    }

    heading.textContent = activeSession.title;
    summary.textContent = sessionSummary(activeSession);
    finish.hidden = false;
    if (!noteDirty && document.activeElement !== notes) notes.value = activeSession.summary || "";
    if (!noteDirty) setNoteStatus("Synced", "saved");
  }

  function renderHistory() {
    const history = $("#practice-session-history");
    if (!history) return;
    history.replaceChildren();
    if (!sessions.length) {
      history.innerHTML = '<p class="empty-recordings">Your sessions will appear here as you practice.</p>';
      return;
    }
    sessions.slice(0, 8).forEach((session) => {
      const card = document.createElement("article");
      card.className = `session-history-card${session.status === "active" ? " active" : ""}`;
      card.innerHTML = `
        <div class="session-history-card-top"><span>${new Date(session.startedAt).toLocaleDateString(undefined, { month: "short", day: "numeric" })}</span><span>${session.status === "active" ? "Today" : "Finished"}</span></div>
        <h4>${escapeHTML(session.title)}</h4>
        <p>${escapeHTML(session.summary || "No session note yet.")}</p>
        <div class="session-history-card-meta"><span>${Number.isFinite(Number(session.totalDurationMs)) ? (Number(session.totalDurationMs) / 60000).toFixed(1) : session.totalMinutes} min</span><span>${session.recordingCount} takes</span></div>`;
      history.appendChild(card);
    });
  }

  async function saveSessionNote() {
    clearTimeout(noteSaveTimer);
    noteSaveTimer = null;
    if (!noteDirty) return activeSession;
    setNoteStatus("Saving…", "saving");
    try {
      const session = await ensureActive();
      activeSession = await api(`/practice-sessions/${session.id}`, {
        method: "PATCH",
        body: JSON.stringify({ summary: $("#practice-session-notes").value.trim() }),
      });
      noteDirty = false;
      await refreshSessionListOnly();
      renderActiveSession();
      renderHistory();
      return activeSession;
    } catch (error) {
      noteDirty = true;
      setNoteStatus(`Not saved: ${error.message}`, "error");
      throw error;
    }
  }

  function queueSessionNoteSave() {
    noteDirty = true;
    setNoteStatus("Waiting to sync", "saving");
    clearTimeout(noteSaveTimer);
    noteSaveTimer = setTimeout(() => saveSessionNote().catch(() => {}), 700);
  }

  async function finishSession() {
    if (!activeSession || !confirm("Finish today’s practice session? Your notes and recordings will stay in Previous work.")) return;
    if (noteDirty) await saveSessionNote();
    activeSession = await api(`/practice-sessions/${activeSession.id}`, {
      method: "PATCH",
      body: JSON.stringify({ status: "completed", endedAt: new Date().toISOString() }),
    });
    activeSession = null;
    noteDirty = false;
    await refreshSessionListOnly();
    renderActiveSession();
    renderHistory();
  }

  async function rollSessionForward(practiceDate) {
    if (!activeSession || localDateKey(new Date(activeSession.startedAt)) === practiceDate) return;
    if (noteDirty) await saveSessionNote();
    await api(`/practice-sessions/${activeSession.id}`, {
      method: "PATCH",
      body: JSON.stringify({ status: "completed", endedAt: new Date().toISOString() }),
    });
    activeSession = null;
    noteDirty = false;
    const notes = $("#practice-session-notes");
    if (notes) notes.value = "";
    await refreshSessionListOnly();
    renderActiveSession();
    renderHistory();
  }

  async function logGuidedActivity(block) {
    const session = await ensureActive();
    const activity = await api(`/practice-sessions/${session.id}/activities`, {
      method: "POST",
      body: JSON.stringify({
        sourceId: String(block.sourceId || ""),
        category: String(block.category || "fundamentals"),
        title: String(block.title || "Guided practice"),
        durationMinutes: Number(block.durationMinutes),
        notes: String(block.notes || ""),
        occurredAt: block.occurredAt || new Date().toISOString(),
      }),
    });
    activeSession = await api(`/practice-sessions/${session.id}`);
    await refreshSessionListOnly();
    renderActiveSession();
    renderHistory();
    return activity;
  }

  async function ensureGuidedBlocks(practiceDate, definitions) {
    if (initialSessionLoad) await initialSessionLoad;
    await rollSessionForward(practiceDate);
    const session = await ensureActive();
    const result = await api(`/practice-sessions/${session.id}/blocks`, {
      method: "POST",
      body: JSON.stringify({ practiceDate, blocks: definitions }),
    });
    return { session, blocks: result.blocks || [] };
  }

  async function updateGuidedBlock(blockID, changes) {
    return api(`/practice-blocks/${blockID}`, {
      method: "PATCH",
      body: JSON.stringify(changes),
    });
  }

  function escapeHTML(value) {
    const element = document.createElement("span");
    element.textContent = value;
    return element.innerHTML;
  }

  globalThis.JazzPracticeSession = {
    ensureActive,
    logGuidedActivity,
    ensureGuidedBlocks,
    updateGuidedBlock,
    currentID: () => activeSession?.id || "",
    refresh: loadSessions,
  };

  $("#practice-session-notes")?.addEventListener("input", queueSessionNoteSave);
  $("#practice-session-notes")?.addEventListener("blur", () => saveSessionNote().catch(() => {}));
  $("#finish-practice-session")?.addEventListener("click", () => finishSession().catch((error) => {
    setNoteStatus(`Could not finish: ${error.message}`, "error");
  }));
  initialSessionLoad = loadSessions();
})();
